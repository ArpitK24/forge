package plugins

import (
	"context"
	"encoding/json"
	"fmt"
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
	resp envelope
	err  error
}

// client is the per-plugin connection. One client owns
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
//     serialized at the transport boundary (Transport holds
//     mu for the full write+flush).
//   - close() is the only way to drain the read goroutine;
//     it stops the transport, which unblocks Recv, which
//     lets the read goroutine exit.
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

	// notifyCh is the channel for plugin-initiated notifications
	// (id == 0). Reserved for future use (progress, etc.).
	notifyCh chan notification

	// closed is set by close() so the read goroutine can
	// short-circuit on transport errors that arrive after
	// a deliberate close.
	closed atomic.Bool
}

// notification is the (method, params) pair carried by a
// plugin-initiated notification. Params is raw JSON because
// the shape is plugin-defined.
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
		notifyCh: make(chan notification, 8),
	}
}

// start spawns the transport and the read goroutine. It must
// be called exactly once before any call()/notify().
func (c *client) start(ctx context.Context) error {
	if err := c.t.Start(ctx); err != nil {
		return err
	}
	go c.readLoop()
	return nil
}

// readLoop runs until the transport is closed or ctx is cancelled.
// It demuxes incoming frames by JSON-RPC id.
func (c *client) readLoop() {
	for {
		body, err := c.t.Recv(context.Background())
		if err != nil {
			// Transport error (EOF, closed pipe, etc.).
			// Fail all pending calls and exit.
			c.mu.Lock()
			if c.closed.Load() {
				c.mu.Unlock()
				return
			}
			for _, ch := range c.pending {
				ch <- pendingResp{err: err}
			}
			c.pending = nil
			c.mu.Unlock()
			return
		}

		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			c.log.Debug("plugin: malformed frame", "plugin", c.name, "err", err)
			continue
		}

		if env.ID == 0 {
			// Notification (no response expected). Drop for now;
			// notifyCh exists for a future consumer.
			_ = env
			continue
		}

		// Response to an in-flight call.
		c.mu.Lock()
		ch, ok := c.pending[env.ID]
		if ok {
			delete(c.pending, env.ID)
		}
		c.mu.Unlock()

		if ok {
			ch <- pendingResp{resp: env}
		} // else: stray response (cancelled call), drop
	}
}

// call sends a JSON-RPC request and waits for the response.
// Returns the raw response envelope (may contain Error).
func (c *client) call(ctx context.Context, method string, params any) (Response, error) {
	id := c.nextID.Add(1)

	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  method,
	}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return Response{}, fmt.Errorf("marshal params: %w", err)
		}
		req.Params = p
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	// Register the pending channel before sending so we don't
	// miss the response if it arrives extremely fast.
	respCh := make(chan pendingResp, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return Response{}, core.Newf(core.KindMCP, "plugin %q: client closed", c.name)
	}
	c.pending[id] = respCh
	c.mu.Unlock()

	if err := c.t.Send(reqBytes); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Response{}, err
	}

	select {
	case r := <-respCh:
		// Extract Response from envelope
		resp := Response{
			JSONRPC: r.resp.JSONRPC,
			ID:      r.resp.ID,
			Result:  r.resp.Result,
			Error:   r.resp.Error,
		}
		return resp, r.err
	case <-ctx.Done():
		// Call cancelled. Remove from pending; the response
		// will be dropped when it arrives.
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Response{}, core.Wrap(core.KindCancelled, ctx.Err(), fmt.Sprintf("plugin %q: call cancelled", c.name))
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *client) notify(ctx context.Context, method string, params any) error {
	req := Request{
		JSONRPC: JSONRPCVersion,
		Method:  method,
	}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		req.Params = p
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	return c.t.Send(reqBytes)
}

// Close shuts down the client and its transport.
func (c *client) Close() error {
	c.closed.Store(true)
	return c.t.Close()
}

// ---------------------------------------------------------------------------
// JSON-RPC types (mirroring mcp package; plugins use the same wire format)
// ---------------------------------------------------------------------------

const JSONRPCVersion = "2.0"

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// envelope is the union of Request and Response for demuxing.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}