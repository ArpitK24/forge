package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newEchoServer builds a PluginServer with one "echo" tool and one
// "fail" tool, mirroring examples/plugins/echo.
func newEchoServer() *PluginServer {
	s := NewPluginServer("echo", "Echo tool plugin")
	s.RegisterTool(ToolDef{
		Name:        "echo",
		Description: "Echo back the input text",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}, func(ctx context.Context, input json.RawMessage) ToolResult {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return ToolResult{Text: "invalid input: " + err.Error(), IsError: true}
		}
		return ToolResult{Text: in.Text}
	})
	s.RegisterTool(ToolDef{Name: "fail"}, func(ctx context.Context, input json.RawMessage) ToolResult {
		return ToolResult{Text: "boom", IsError: true}
	})
	return s
}

// roundTrip runs ServeIO over the given framed requests and returns
// the decoded responses.
func roundTrip(t *testing.T, s *PluginServer, reqs ...Request) []Response {
	t.Helper()
	var in bytes.Buffer
	for _, r := range reqs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writeFrame(&in, b); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := s.ServeIO(&in, &out); err != nil {
		t.Fatalf("ServeIO returned %v on clean EOF, want nil", err)
	}
	var resps []Response
	r := bufio.NewReader(&out)
	for {
		body, err := readFrame(r)
		if err != nil {
			if isCleanEOF(err) {
				break
			}
			t.Fatalf("server wrote a bad frame: %v", err)
		}
		var resp Response
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("server wrote bad JSON %q: %v", body, err)
		}
		resps = append(resps, resp)
	}
	return resps
}

func TestServer_SpeaksContentLengthFraming(t *testing.T) {
	// Regression: Serve used json.Encoder (newline-delimited JSON)
	// while the host transport expects Content-Length frames, so
	// no plugin could complete initialize.
	resps := roundTrip(t, newEchoServer(), Request{JSONRPC: JSONRPCVersion, ID: 1, Method: MethodInitialize})
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].ID != 1 || resps[0].Error != nil {
		t.Fatalf("initialize response = %+v", resps[0])
	}
}

func TestServer_InitializeAdvertisesRealDescriptionsAndSchemas(t *testing.T) {
	resps := roundTrip(t, newEchoServer(), Request{JSONRPC: JSONRPCVersion, ID: 1, Method: MethodInitialize})
	var res initializeResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ServerInfo.Name != "echo" {
		t.Fatalf("serverInfo.name = %q", res.ServerInfo.Name)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "echo" || res.Tools[1].Name != "fail" {
		t.Fatalf("tools = %+v, want [echo fail] in sorted order", res.Tools)
	}
	echo := res.Tools[0]
	if echo.Description != "Echo back the input text" {
		t.Fatalf("description dropped: %q", echo.Description)
	}
	if !strings.Contains(string(echo.InputSchema), `"required":["text"]`) {
		t.Fatalf("schema dropped: %s", echo.InputSchema)
	}
	// A tool registered without description/schema gets safe defaults.
	fail := res.Tools[1]
	if fail.Description != "Plugin tool: fail" || !strings.Contains(string(fail.InputSchema), `"type":"object"`) {
		t.Fatalf("defaults wrong: %+v", fail)
	}
}

func TestServer_ToolsCallAndErrors(t *testing.T) {
	call := func(id int64, name, args string) Request {
		p, _ := json.Marshal(toolsCallParams{Name: name, Arguments: json.RawMessage(args)})
		return Request{JSONRPC: JSONRPCVersion, ID: id, Method: MethodToolsCall, Params: p}
	}
	resps := roundTrip(t, newEchoServer(),
		call(1, "echo", `{"text":"hi forge"}`),
		call(2, "nope", `{}`),
		Request{JSONRPC: JSONRPCVersion, ID: 3, Method: "bogus/method"},
		Request{JSONRPC: JSONRPCVersion, ID: 4, Method: MethodToolsCall, Params: json.RawMessage(`"not an object"`)},
		Request{JSONRPC: JSONRPCVersion, Method: MethodInitialize}, // notification: no reply
		call(5, "fail", `{}`),
	)
	if len(resps) != 5 {
		t.Fatalf("got %d responses, want 5 (notification must not be answered)", len(resps))
	}
	var ok ToolResult
	if err := json.Unmarshal(resps[0].Result, &ok); err != nil || ok.Text != "hi forge" || ok.IsError {
		t.Fatalf("echo result = %+v (%v)", ok, err)
	}
	if resps[1].Error == nil || resps[1].Error.Code != -32601 || !strings.Contains(resps[1].Error.Message, "tool not found") {
		t.Fatalf("unknown tool = %+v", resps[1])
	}
	if resps[2].Error == nil || resps[2].Error.Code != -32601 {
		t.Fatalf("unknown method = %+v", resps[2])
	}
	if resps[3].Error == nil || resps[3].Error.Code != -32602 {
		t.Fatalf("bad params = %+v", resps[3])
	}
	var failed ToolResult
	_ = json.Unmarshal(resps[4].Result, &failed)
	if resps[4].ID != 5 || !failed.IsError || failed.Text != "boom" {
		t.Fatalf("fail tool = %+v / %+v", resps[4], failed)
	}
}

func TestServer_ParseErrorKeepsServing(t *testing.T) {
	var in bytes.Buffer
	writeFrame(&in, []byte(`{not json`))
	b, _ := json.Marshal(Request{JSONRPC: JSONRPCVersion, ID: 7, Method: MethodInitialize})
	writeFrame(&in, b)
	var out bytes.Buffer
	if err := newEchoServer().ServeIO(&in, &out); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(&out)
	var first, second Response
	b1, _ := readFrame(r)
	b2, _ := readFrame(r)
	json.Unmarshal(b1, &first)
	json.Unmarshal(b2, &second)
	if first.Error == nil || first.Error.Code != -32700 {
		t.Fatalf("parse error response = %+v", first)
	}
	if second.ID != 7 || second.Error != nil {
		t.Fatalf("server stopped after parse error: %+v", second)
	}
}

func TestServer_TruncatedFrameIsAnError(t *testing.T) {
	err := newEchoServer().ServeIO(strings.NewReader("Content-Length: 50\r\n\r\n{}"), io.Discard)
	if err == nil {
		t.Fatal("truncated frame should surface an error, not a clean exit")
	}
}

// pipeTransport connects a client directly to an in-process
// PluginServer, exercising the real client + framing code without
// spawning a subprocess.
type pipeTransport struct {
	toServer   *io.PipeWriter
	fromServer *bufio.Reader
	closers    []io.Closer
	done       chan error
	sendMu     sync.Mutex
}

func newPipeTransport(s *PluginServer) *pipeTransport {
	sr, sw := io.Pipe() // host -> server
	cr, cw := io.Pipe() // server -> host
	pt := &pipeTransport{toServer: sw, fromServer: bufio.NewReader(cr), closers: []io.Closer{sw, cr}, done: make(chan error, 1)}
	go func() {
		err := s.ServeIO(sr, cw)
		cw.Close()
		pt.done <- err
	}()
	return pt
}

func (p *pipeTransport) Start(ctx context.Context) error { return nil }
func (p *pipeTransport) Send(b []byte) error {
	// Mirror StdioTransport: writes are serialized so concurrent
	// calls never interleave frames on the pipe.
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	_, err := writeFrame(p.toServer, b)
	return err
}
func (p *pipeTransport) Recv(ctx context.Context) ([]byte, error) { return readFrame(p.fromServer) }
func (p *pipeTransport) Close() error {
	for _, c := range p.closers {
		c.Close()
	}
	return nil
}

func TestClient_PluginToolExecute_InProcess(t *testing.T) {
	pt := newPipeTransport(newEchoServer())
	cli := newClient("echo", pt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := cli.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tool := &PluginTool{client: cli, plugin: "echo", name: "echo"}
	res := tool.Execute(ctx, json.RawMessage(`{"text":"ranked context"}`), nil)
	if res.IsError || res.Text != "ranked context" {
		t.Fatalf("Execute = %+v", res)
	}

	bad := &PluginTool{client: cli, plugin: "echo", name: "missing"}
	res = bad.Execute(ctx, json.RawMessage(`{}`), nil)
	if !res.IsError || !strings.Contains(res.Text, "tool not found") {
		t.Fatalf("missing tool = %+v", res)
	}

	failing := &PluginTool{client: cli, plugin: "echo", name: "fail"}
	res = failing.Execute(ctx, json.RawMessage(`{}`), nil)
	if !res.IsError || res.Text != "boom" {
		t.Fatalf("tool error not propagated: %+v", res)
	}
}

func TestClient_ConcurrentCalls(t *testing.T) {
	pt := newPipeTransport(newEchoServer())
	cli := newClient("echo", pt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cli.start(context.Background())
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tool := &PluginTool{client: cli, plugin: "echo", name: "echo"}

	const n = 20
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			want := strings.Repeat("x", i+1)
			in, _ := json.Marshal(map[string]string{"text": want})
			res := tool.Execute(ctx, in, nil)
			if res.IsError || res.Text != want {
				errs <- res.Text
				return
			}
			errs <- ""
		}(i)
	}
	for i := 0; i < n; i++ {
		if e := <-errs; e != "" {
			t.Fatalf("concurrent call got wrong/failed result: %q", e)
		}
	}
}

func TestClient_TransportDeathFailsPendingCalls(t *testing.T) {
	// A server that reads one frame then hangs up without answering.
	sr, sw := io.Pipe()
	cr, cw := io.Pipe()
	go func() {
		readFrame(bufio.NewReader(sr))
		cw.Close()
	}()
	pt := &pipeTransport{toServer: sw, fromServer: bufio.NewReader(cr), closers: []io.Closer{sw, cr}}
	cli := newClient("dead", pt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cli.start(context.Background())
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := cli.call(ctx, MethodInitialize, nil)
	if err == nil {
		t.Fatal("call on a dead plugin should fail, not hang or succeed")
	}
	if ctx.Err() != nil {
		t.Fatal("call hung until the context deadline instead of failing fast")
	}
}

func TestClient_CallCancelled(t *testing.T) {
	// A server that never answers.
	sr, sw := io.Pipe()
	cr, _ := io.Pipe()
	go io.Copy(io.Discard, sr)
	pt := &pipeTransport{toServer: sw, fromServer: bufio.NewReader(cr), closers: []io.Closer{sw, cr}}
	cli := newClient("slow", pt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cli.start(context.Background())
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := cli.call(ctx, MethodInitialize, nil); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want a cancellation", err)
	}
}

func TestPluginTool_NamespacingAndPermission(t *testing.T) {
	tool := &PluginTool{plugin: "mytool", name: "do_something", desc: "d", inputJSON: json.RawMessage(`{}`)}
	if got := tool.Name(); got != "plugin__mytool__do_something" {
		t.Fatalf("Name() = %q", got)
	}
	if tool.Description() != "d" || string(tool.InputSchema()) != `{}` {
		t.Fatal("Description/InputSchema not passed through")
	}
	if tool.PermissionLevel() == 0 {
		// PermExecute is the conservative default for subprocess tools.
		t.Log("permission level:", tool.PermissionLevel())
	}
}

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	m, err := readManifest(write("ok.json", `{"name":"echo","version":"1.0.0","entrypoint":"echo-plugin","args":["-v"],"env":{"K":"V"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "echo" || m.Entrypoint != "echo-plugin" || len(m.Args) != 1 || m.Env["K"] != "V" {
		t.Fatalf("manifest = %+v", m)
	}
	for name, body := range map[string]string{
		"noname.json":  `{"entrypoint":"x"}`,
		"blank.json":   `{"name":"   ","entrypoint":"x"}`,
		"noentry.json": `{"name":"x"}`,
		"bad.json":     `{name:`,
	} {
		if _, err := readManifest(write(name, body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := readManifest(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: want error")
	}
}
