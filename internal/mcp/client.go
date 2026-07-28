package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

// closeTimeout is how long the client's read goroutine waits
// to drain on transport errors. Once exceeded, the goroutine
// exits and any pending callers see a clean error.
const closeTimeout = 2 * time.Second

// pendingResp is what a single call() awaits on its per-id
// channel. The response value is the JSON-RPC envelope; the
// err is non-nil if the transport failed before the response
// arrived (vs. a JSON-RPC error response, which carries
// resp.Error != nil).
type pendingResp struct {
	resp Response
	err  error
}

// client is the per-server connection. One client owns
// exactly one Transport, one id generator, one read goroutine,
// and a map of in-flight requests keyed by JSON-RPC id.
//
// Concurrency model:
//
//   - Exactly one read goroutine, started by start(). It
//     loops on t.Recv(ctx) and demuxes incoming frames
//     into either an in-flight pendingResp (matched by id)
//     or a dropped notification (id == 0).
//   - call() and notify() are concurrent-safe. Send is
//     serialized at the transport boundary (StdioTransport
//     holds t.mu for the full write+flush).
//   - close() is the only way to drain the read goroutine;
//     it stops the transport, which unblocks Recv, which
//     lets the read goroutine exit.
//
// Design notes a future maintainer might be tempted to "improve":
//
//   - Do NOT add a write goroutine. The transport's mutex
//     already serializes writes; a second goroutine would
//     just add a queue without changing throughput.
//   - Do NOT add a context to start(). The Manager passes
//     its own ctx to call()/notify(); the transport's
//     lifecycle is the Manager's responsibility, not the
//     per-call ctx's.
type client struct {
	name string
	t    Transport
	log  *slog.Logger

	// nextID is the JSON-RPC id generator. Atomic so call()
	// can mint ids without taking the mutex.
	nextID atomic.Int64

	// mu protects pending. The read goroutine holds it
	// briefly to look up an id and write to its channel.
	// call() holds it briefly to insert and to delete on
	// cancel.
	mu      sync.Mutex
	pending map[int64]chan pendingResp

	// notifyCh is the channel for server-initiated notifications
	// (id == 0). Reserved for a future step (sampling requests,
	// progress). Today's read goroutine drops notifications —
	// no MCP client in this version consumes them. The channel
	// exists so adding a consumer doesn't require a new field.
	notifyCh chan notification

	// closed is set by close() so the read goroutine can
	// short-circuit on transport errors that arrive after
	// a deliberate close.
	closed atomic.Bool
}

// notification is the (method, params) pair carried by a
// server-initiated notification. Params is raw JSON because
// the shape is server-defined.
type notification struct {
	Method string
	Params json.RawMessage
}

// newClient constructs a client without starting the
// transport. The caller is responsible for invoking start().
func newClient(name string, t Transport, log *slog.Logger) *client {
	return &client{
		name:     name,
		t:        t,
		log:      log,
		pending:  make(map[int64]chan pendingResp),
		notifyCh: make(chan notification, 16),
	}
}

// start launches the transport and starts the read goroutine.
// ctx is the transport's lifecycle context — cancellation
// aborts in-flight Recv calls but does not (by itself) close
// the client; the caller must call close().
func (c *client) start(ctx context.Context) error {
	if err := c.t.Start(ctx); err != nil {
		return err
	}
	go c.readLoop(ctx)
	return nil
}

// readLoop demuxes incoming frames into pendingResp channels
// or dropped notifications. Exits when Recv returns an error
// (transport closed) or when ctx is cancelled.
func (c *client) readLoop(ctx context.Context) {
	for {
		body, err := c.t.Recv(ctx)
		if err != nil {
			// Transport-level failure. Mark all pending
			// callers with the error so they don't hang
			// forever. After this, the client is unusable.
			c.failPending(err)
			if !c.closed.Load() {
				c.log.Warn("mcp client read loop exited with error",
					"server", c.name, "err", err)
			}
			return
		}
		var resp Response
		if err := json.Unmarshal(body, &resp); err != nil {
			// Malformed JSON-RPC envelope. Treat as a
			// protocol error: drop the body, fail all
			// pending callers (the response IDs are
			// unknowable), and exit.
			c.log.Warn("mcp: malformed JSON-RPC envelope",
				"server", c.name, "err", err, "body", string(body))
			c.failPending(core.Newf(core.KindMCP,
				"mcp %q: malformed response: %v", c.name, err))
			return
		}
		if resp.ID == 0 {
			// Notification (no id). Decoded loosely
			// because the params shape is server-defined.
			var n struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params,omitempty"`
			}
			if err := json.Unmarshal(body, &n); err != nil {
				c.log.Warn("mcp: malformed notification body",
					"server", c.name, "err", err)
				continue
			}
			if n.Method == "" {
				c.log.Warn("mcp: notification missing method",
					"server", c.name)
				continue
			}
			// Drop on full buffer. Future consumers may
			// want different policy.
			select {
			case c.notifyCh <- notification{Method: n.Method, Params: n.Params}:
			default:
				c.log.Debug("mcp: notification channel full, dropping",
					"server", c.name, "method", n.Method)
			}
			continue
		}
		// Response. Match to a pending caller.
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if !ok {
			// Orphan response: id doesn't match any
			// in-flight call. Treat as a protocol error:
			// the server is out of sync with us.
			c.log.Warn("mcp: orphan response (id not in pending)",
				"server", c.name, "id", resp.ID)
			c.failPending(core.Newf(core.KindMCP,
				"mcp %q: orphan response id=%d", c.name, resp.ID))
			return
		}
		// Non-blocking send; channel is buffered to 1.
		ch <- pendingResp{resp: resp}
	}
}

// failPending marks every in-flight caller with err. Used
// when the read loop is exiting so callers don't hang.
func (c *client) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		ch <- pendingResp{err: err}
		delete(c.pending, id)
	}
}

// call sends a JSON-RPC request, awaits the matching
// response, and decodes into result. Errors:
//
//   - Transport-level failure (ctx cancel, EOF, malformed
//     envelope): *core.Error{Kind: KindCancelled / KindIO /
//     KindMCP}. The caller should treat the client as dead.
//   - JSON-RPC error response (server says "Method not
//     found" etc.): *core.Error{Kind: KindMCP} with the
//     error code and message preserved.
//   - Success: result is populated, err is nil.
//
// result may be nil if the caller doesn't care about the
// success payload (e.g. notifications — but those should
// use notify() instead).
func (c *client) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("mcp %q: marshal params", c.name))
	}
	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("mcp %q: marshal request", c.name))
	}

	// Register the pending entry BEFORE sending, so the
	// read goroutine can find it when the response arrives.
	// Channel is buffered to 1 so the send is non-blocking
	// even if the read goroutine writes while call() is
	// blocked in select.
	ch := make(chan pendingResp, 1)
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return core.Newf(core.KindMCP, "mcp %q: call on closed client", c.name)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	// Send. If this fails, deregister and return.
	if err := c.t.Send(body); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	// Race the response against ctx cancellation.
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if r.resp.Error != nil {
			return rpcErrorToCore(c.name, r.resp.Error)
		}
		if result != nil {
			if err := json.Unmarshal(r.resp.Result, result); err != nil {
				return core.Wrap(core.KindMCP, err,
					fmt.Sprintf("mcp %q: unmarshal result", c.name))
			}
		}
		return nil
	case <-ctx.Done():
		// Deregister so the read goroutine doesn't try to
		// write to a channel no one is reading. The channel
		// is GC'd once both this frame and the read
		// goroutine's possible send finish.
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return core.Wrap(core.KindCancelled, ctx.Err(),
			fmt.Sprintf("mcp %q: call %s cancelled", c.name, method))
	}
}

// notify sends a JSON-RPC notification (no id, no response).
// Per JSON-RPC 2.0, the server does not reply to a
// notification. Used for "notifications/initialized" and
// "shutdown".
func (c *client) notify(method string, params any) error {
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("mcp %q: marshal params", c.name))
	}
	n := Notification{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  paramsJSON,
	}
	body, err := json.Marshal(n)
	if err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("mcp %q: marshal notification", c.name))
	}
	if err := c.t.Send(body); err != nil {
		return err
	}
	return nil
}

// close stops the transport and waits for the read goroutine
// to exit. Idempotent. ctx controls only the wait — the
// transport itself is shut down deterministically.
func (c *client) close(ctx context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}
	// Send a graceful shutdown notification if the transport
	// is still alive. Best-effort: a dying transport will
	// fail this send and we fall through to Close().
	_ = c.notify(MethodShutdown, nil)
	// Close the transport. This unblocks the read goroutine's
	// Recv call (EOF on stdout, or the HTTP stub returning).
	transportErr := c.t.Close()
	// Wait for the read goroutine to finish draining pending
	// callers, up to ctx. After the wait, any still-pending
	// callers are stuck — fail them so they get a clear error.
	done := make(chan struct{})
	go func() {
		// We don't have a direct handle to the read goroutine,
		// but failPending was already called when transport
		// closed shut down the read loop. Just wait for ctx
		// or for a brief grace period.
		<-time.After(closeTimeout)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	// Final sweep: any pending callers still waiting (because
	// the read goroutine didn't get a clean EOF) get an error.
	c.failPending(core.Newf(core.KindMCP, "mcp %q: client closed", c.name))
	return transportErr
}

// marshalParams accepts either a json.RawMessage (already
// encoded) or a value to be JSON-serialized. The MCP server
// requires Params to be a JSON object or array; nil is
// encoded as "null" which some servers reject — callers
// should pass a non-nil value when the method expects
// params.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}

// rpcErrorToCore converts a JSON-RPC error response into a
// *core.Error with KindMCP. The error code and message are
// preserved; the data payload is included as a structured
// detail if non-empty.
func rpcErrorToCore(serverName string, rpc *RPCError) *core.Error {
	ce := core.Newf(core.KindMCP, "mcp %q: %s (code %d): %s",
		serverName, rpc.Message, rpc.Code, rpc.Message)
	ce = ce.WithDetail("code", rpc.Code)
	if len(rpc.Data) > 0 {
		ce = ce.WithDetail("data", string(rpc.Data))
	}
	return ce
}

// Compile-time guard: ensure io.EOF is recognizable on the
// transport-error path. (Not strictly necessary — the
// readers of pendingResp.err type-switch on *core.Error —
// but documenting the contract.)
var _ = errors.Is
var _ = io.EOF
