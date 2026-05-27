package lspclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uvean-z/aegislsp/internal/types"
)

// ---------------------------------------------------------------------------
// FrameWriter tests
// ---------------------------------------------------------------------------

func TestFrameWriter_BasicMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	msg := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if err := w.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	want := "Content-Length: 46\r\n\r\n" + string(msg)
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFrameWriter_EmptyBody(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	msg := json.RawMessage(`{}`)
	if err := w.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	want := "Content-Length: 2\r\n\r\n{}"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFrameWriter_LargeBody(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	payload := `{"data":"` + strings.Repeat("x", 10000) + `"}`
	msg := json.RawMessage(payload)
	if err := w.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	expectedHeader := "Content-Length: 10011\r\n\r\n"
	got := buf.String()
	if !strings.HasPrefix(got, expectedHeader) {
		t.Errorf("header = %q, want prefix %q", got[:len(expectedHeader)], expectedHeader)
	}
	body := got[len(expectedHeader):]
	if body != payload {
		t.Errorf("body length = %d, want %d", len(body), len(payload))
	}
}

// ---------------------------------------------------------------------------
// FrameReader tests
// ---------------------------------------------------------------------------

func TestFrameReader_BasicMessage(t *testing.T) {
	raw := "Content-Length: 24\r\n\r\n" + `{"jsonrpc":"2.0","id":1}`
	r := NewFrameReader(strings.NewReader(raw))

	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg) != `{"jsonrpc":"2.0","id":1}` {
		t.Errorf("got %s", string(msg))
	}
}

func TestFrameReader_EmptyBody(t *testing.T) {
	raw := "Content-Length: 2\r\n\r\n{}"
	r := NewFrameReader(strings.NewReader(raw))

	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg) != "{}" {
		t.Errorf("got %s", string(msg))
	}
}

func TestFrameReader_MissingContentLength(t *testing.T) {
	raw := "Some-Other-Header: value\r\n\r\n{}"
	r := NewFrameReader(strings.NewReader(raw))

	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

func TestFrameReader_InvalidContentLength(t *testing.T) {
	raw := "Content-Length: not_a_number\r\n\r\n{}"
	r := NewFrameReader(strings.NewReader(raw))

	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for invalid Content-Length")
	}
}

func TestFrameReader_NegativeContentLength(t *testing.T) {
	raw := "Content-Length: -5\r\n\r\n"
	r := NewFrameReader(strings.NewReader(raw))

	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for negative Content-Length")
	}
}

func TestFrameReader_TruncatedBody(t *testing.T) {
	raw := "Content-Length: 100\r\n\r\nshort"
	r := NewFrameReader(strings.NewReader(raw))

	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for truncated body")
	}
}

func TestFrameReader_ExtraHeaders(t *testing.T) {
	raw := "Content-Length: 8\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\nX-Custom: foo\r\n\r\n" + `{"id":1}`
	r := NewFrameReader(strings.NewReader(raw))

	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg) != `{"id":1}` {
		t.Errorf("got %s", string(msg))
	}
}

func TestFrameReader_EOF(t *testing.T) {
	r := NewFrameReader(strings.NewReader(""))

	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error on EOF")
	}
	if err != io.EOF && !strings.Contains(err.Error(), "EOF") {
		t.Errorf("expected EOF error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests: Write then Read
// ---------------------------------------------------------------------------

func TestFrameWriterReader_RoundTrip_Single(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	original := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"rootUri":"file:///tmp"}}`)
	if err := w.WriteMessage(original); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	r := NewFrameReader(&buf)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("got %s, want %s", string(got), string(original))
	}
}

func TestFrameWriterReader_RoundTrip_Multiple(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	messages := []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`),
		json.RawMessage(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///main.go"}}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":null}`),
	}

	for _, msg := range messages {
		if err := w.WriteMessage(msg); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
	}

	r := NewFrameReader(&buf)
	for i, want := range messages {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		if string(got) != string(want) {
			t.Errorf("message[%d] = %s, want %s", i, string(got), string(want))
		}
	}
}

func TestFrameWriterReader_RoundTrip_NoOverRead(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	msg1 := json.RawMessage(`{"id":1}`)
	msg2 := json.RawMessage(`{"id":2}`)
	w.WriteMessage(msg1)
	w.WriteMessage(msg2)

	r := NewFrameReader(&buf)

	got1, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("first ReadMessage: %v", err)
	}
	if string(got1) != string(msg1) {
		t.Errorf("first message = %s, want %s", string(got1), string(msg1))
	}

	got2, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("second ReadMessage: %v", err)
	}
	if string(got2) != string(msg2) {
		t.Errorf("second message = %s, want %s", string(got2), string(msg2))
	}
}

// ---------------------------------------------------------------------------
// Pipe-based concurrent test
// ---------------------------------------------------------------------------

func TestFrameWriterReader_PipeConcurrent(t *testing.T) {
	pr, pw := io.Pipe()

	messages := []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
		json.RawMessage(`{"jsonrpc":"2.0","method":"$/logTrace","params":{"message":"hello"}}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`),
	}

	go func() {
		w := NewFrameWriter(pw)
		for _, msg := range messages {
			if err := w.WriteMessage(msg); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()

	r := NewFrameReader(pr)
	for i, want := range messages {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		if string(got) != string(want) {
			t.Errorf("message[%d] = %s, want %s", i, string(got), string(want))
		}
	}

	_, err := r.ReadMessage()
	if err == nil {
		t.Error("expected EOF after all messages consumed")
	}
}

// ---------------------------------------------------------------------------
// Realistic LSP payload tests
// ---------------------------------------------------------------------------

func TestFrameWriterReader_InitializeRequest(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	params := `{"processId":1234,"rootUri":"file:///workspace","capabilities":{"textDocument":{"hover":{"contentFormat":["markdown"]}}}}`
	msg := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":` + params + `}`)
	w.WriteMessage(msg)

	r := NewFrameReader(&buf)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	var parsed struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Method != "initialize" {
		t.Errorf("method = %q, want %q", parsed.Method, "initialize")
	}
}

func TestFrameWriterReader_DiagnosticsNotification(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///main.go","diagnostics":[{"range":{"start":{"line":10,"character":4},"end":{"line":10,"character":10}},"severity":1,"message":"undefined: foo"}]}}`)
	w.WriteMessage(msg)

	r := NewFrameReader(&buf)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	var parsed struct {
		Params struct {
			URI         string `json:"uri"`
			Diagnostics []struct {
				Message string `json:"message"`
			} `json:"diagnostics"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Params.URI != "file:///main.go" {
		t.Errorf("uri = %q", parsed.Params.URI)
	}
	if len(parsed.Params.Diagnostics) != 1 || parsed.Params.Diagnostics[0].Message != "undefined: foo" {
		t.Errorf("diagnostics = %+v", parsed.Params.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// Edge case: Content-Length with leading/trailing whitespace
// ---------------------------------------------------------------------------

func TestFrameReader_ContentLengthWhitespace(t *testing.T) {
	raw := "Content-Length:   5  \r\n\r\nhello"
	r := NewFrameReader(strings.NewReader(raw))

	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg) != "hello" {
		t.Errorf("got %q, want %q", string(msg), "hello")
	}
}

// ---------------------------------------------------------------------------
// Edge case: Zero Content-Length
// ---------------------------------------------------------------------------

func TestFrameReader_ZeroContentLength(t *testing.T) {
	raw := "Content-Length: 0\r\n\r\n"
	r := NewFrameReader(strings.NewReader(raw))

	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if len(msg) != 0 {
		t.Errorf("got %q, want empty", string(msg))
	}
}

// ---------------------------------------------------------------------------
// RequestCorrelator tests
// ---------------------------------------------------------------------------

func TestRequestCorrelator_Resolve(t *testing.T) {
	corr := NewRequestCorrelator()
	ch, _ := corr.Register(1)
	corr.Resolve(1, json.RawMessage(`{"ok":true}`))

	select {
	case result := <-ch:
		if string(result) != `{"ok":true}` {
			t.Errorf("got %s", string(result))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestRequestCorrelator_Reject(t *testing.T) {
	corr := NewRequestCorrelator()
	_, errCh := corr.Register(1)
	corr.Reject(1, errors.New("server error"))

	select {
	case err := <-errCh:
		if err.Error() != "server error" {
			t.Errorf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error")
	}
}

func TestRequestCorrelator_RejectAll(t *testing.T) {
	corr := NewRequestCorrelator()
	const n = 10
	errChs := make([]chan error, n)
	for i := 0; i < n; i++ {
		_, errChs[i] = corr.Register(i)
	}

	corr.RejectAll(errors.New("crash"))

	for i := 0; i < n; i++ {
		select {
		case err := <-errChs[i]:
			if err.Error() != "crash" {
				t.Errorf("[%d] got %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("[%d] timeout waiting for error", i)
		}
	}
}

func TestRequestCorrelator_ResolveUnknown(t *testing.T) {
	corr := NewRequestCorrelator()
	// Should not panic.
	corr.Resolve(999, json.RawMessage(`{}`))
	corr.Reject(999, errors.New("nope"))
}

func TestRequestCorrelator_ConcurrentRegisterResolve(t *testing.T) {
	corr := NewRequestCorrelator()
	const n = 1000

	var wg sync.WaitGroup
	results := make([]chan json.RawMessage, n)
	errs := make([]chan error, n)

	// Register all.
	for i := 0; i < n; i++ {
		results[i], errs[i] = corr.Register(i)
	}

	// Resolve half concurrently, reject the other half concurrently.
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				corr.Resolve(id, json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)))
			} else {
				corr.Reject(id, fmt.Errorf("err-%d", id))
			}
		}(i)
	}
	wg.Wait()

	// Verify all results.
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			select {
			case r := <-results[i]:
				expected := fmt.Sprintf(`{"id":%d}`, i)
				if string(r) != expected {
					t.Errorf("[%d] got %s, want %s", i, string(r), expected)
				}
			default:
				t.Errorf("[%d] expected result, channel empty", i)
			}
		} else {
			select {
			case e := <-errs[i]:
				expected := fmt.Sprintf("err-%d", i)
				if e.Error() != expected {
					t.Errorf("[%d] got %v, want %s", i, e, expected)
				}
			default:
				t.Errorf("[%d] expected error, channel empty", i)
			}
		}
	}
}

func TestRequestCorrelator_ConcurrentRejectAll(t *testing.T) {
	corr := NewRequestCorrelator()
	const n = 500

	errChs := make([]chan error, n)
	for i := 0; i < n; i++ {
		_, errChs[i] = corr.Register(i)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		corr.RejectAll(errors.New("bulk-crash"))
	}()
	wg.Wait()

	rejected := 0
	for i := 0; i < n; i++ {
		select {
		case <-errChs[i]:
			rejected++
		default:
		}
	}
	if rejected != n {
		t.Errorf("rejected %d/%d", rejected, n)
	}
}

func TestRequestCorrelator_ConcurrentRegisterAndRejectAll(t *testing.T) {
	corr := NewRequestCorrelator()
	const goroutines = 100
	const opsPerG = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half register + resolve.
	for g := 0; g < goroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				id := base*opsPerG + j
				ch, _ := corr.Register(id)
				corr.Resolve(id, json.RawMessage(`"ok"`))
				<-ch
			}
		}(g)
	}

	// Half register + rejectAll.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				id := goroutines*opsPerG + g*opsPerG + j
				_, errCh := corr.Register(id)
				// Might be rejected by RejectAll or by individual Reject.
				corr.Reject(id, errors.New("individual"))
				select {
				case <-errCh:
				default:
				}
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// LSPClient: mock pipe helpers
// ---------------------------------------------------------------------------

// mockServer simulates an LSP server over an io.Pipe pair.
type mockServer struct {
	reader *io.PipeReader // reads what the client wrote
	writer *io.PipeWriter // writes responses to the client
	frameR FrameReader
	frameW FrameWriter
}

func newMockServer(clientR io.Reader, clientW io.Writer) *mockServer {
	return &mockServer{
		reader: clientR.(*io.PipeReader),
		writer: clientW.(*io.PipeWriter),
		frameR: NewFrameReader(clientR),
		frameW: NewFrameWriter(clientW),
	}
}

// readRequest reads one request from the client and parses it.
func (s *mockServer) readRequest() (Request, error) {
	raw, err := s.frameR.ReadMessage()
	if err != nil {
		return Request{}, err
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// sendResponse sends a JSON-RPC response back to the client.
func (s *mockServer) sendResponse(id int, result json.RawMessage) error {
	resp := Response{JSONRPC: "2.0", ID: id, Result: result}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return s.frameW.WriteMessage(raw)
}

// sendErrorResponse sends a JSON-RPC error response.
func (s *mockServer) sendErrorResponse(id int, code int, message string) error {
	errObj := struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}
	errRaw, _ := json.Marshal(errObj)
	errMsg := json.RawMessage(errRaw)
	resp := Response{JSONRPC: "2.0", ID: id, Error: &errMsg}
	raw, _ := json.Marshal(resp)
	return s.frameW.WriteMessage(raw)
}

// sendNotification sends a server notification.
func (s *mockServer) sendNotification(method string, params json.RawMessage) error {
	msg := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.frameW.WriteMessage(raw)
}

// close closes the server's writer (simulates connection drop).
func (s *mockServer) close() {
	s.writer.Close()
}

// newClientPipe creates an LSPClient connected via io.Pipe to a mock server.
func newClientPipe(t *testing.T) (*lspClientImpl, *mockServer) {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	client := NewLSPClient(clientR, clientW).(*lspClientImpl)
	server := newMockServer(serverR, serverW)
	return client, server
}

// ---------------------------------------------------------------------------
// LSPClient: Initialize
// ---------------------------------------------------------------------------

func TestLSPClient_Initialize_Success(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if req.Method != "initialize" {
			t.Errorf("method = %q, want initialize", req.Method)
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))

		// Read the "initialized" notification.
		raw, err := server.frameR.ReadMessage()
		if err != nil {
			t.Errorf("server read initialized: %v", err)
			return
		}
		var notif struct {
			Method string `json:"method"`
		}
		json.Unmarshal(raw, &notif)
		if notif.Method != "initialized" {
			t.Errorf("notification method = %q, want initialized", notif.Method)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Initialize(ctx, "file:///workspace", nil)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !client.IsInitialized() {
		t.Error("expected IsInitialized() == true")
	}
	if client.State() != StateInitialized {
		t.Errorf("state = %d, want %d", client.State(), StateInitialized)
	}
}

func TestLSPClient_Initialize_Error(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendErrorResponse(req.ID, -32603, "internal error")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Initialize(ctx, "file:///workspace", nil)
	if err == nil {
		t.Fatal("expected error from Initialize")
	}
	if client.IsInitialized() {
		t.Error("expected IsInitialized() == false after error")
	}
}

func TestLSPClient_Initialize_Timeout(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	// Server never responds — just drain.
	go func() {
		for {
			_, err := server.readRequest()
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.Initialize(ctx, "file:///workspace", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Definition
// ---------------------------------------------------------------------------

func TestLSPClient_Definition_Success(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Single server goroutine handles initialize then definition.
	go func() {
		// Handle initialize request.
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage() // drain "initialized" notification

		// Handle definition request.
		req, err = server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`[{"uri":"file:///foo.go","range":{"start":{"line":5,"character":10},"end":{"line":5,"character":15}}}]`))
	}()

	client.Initialize(ctx, "file:///workspace", nil)

	locations, err := client.Definition(ctx, "file:///workspace/main.go", types.Position{Line: 10, Character: 5})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locations) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locations))
	}
	if locations[0].URI != "file:///foo.go" {
		t.Errorf("URI = %q", locations[0].URI)
	}
	if locations[0].Range.Start.Line != 5 {
		t.Errorf("line = %d, want 5", locations[0].Range.Start.Line)
	}
}

func TestLSPClient_Definition_SingleLocation(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		req, err = server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"uri":"file:///single.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}`))
	}()

	client.Initialize(ctx, "file:///workspace", nil)

	locations, err := client.Definition(ctx, "file:///workspace/main.go", types.Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locations) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locations))
	}
	if locations[0].URI != "file:///single.go" {
		t.Errorf("URI = %q", locations[0].URI)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Hover
// ---------------------------------------------------------------------------

func TestLSPClient_Hover_Success(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	hoverResult := json.RawMessage(`{"contents":{"kind":"markdown","value":"# Hello\n\nA test function."},"range":{"start":{"line":5,"character":5},"end":{"line":5,"character":10}}}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		req, err = server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, hoverResult)
	}()

	client.Initialize(ctx, "file:///workspace", nil)

	result, err := client.Hover(ctx, "file:///workspace/main.go", types.Position{Line: 5, Character: 5})
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if string(result) != string(hoverResult) {
		t.Errorf("result = %s", string(result))
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Notifications
// ---------------------------------------------------------------------------

func TestLSPClient_DiagnosticsNotification(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize.
	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		// Send diagnostics notification.
		diagParams := json.RawMessage(`{"uri":"file:///main.go","diagnostics":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}},"severity":1,"message":"undefined: foo"}]}`)
		server.sendNotification("textDocument/publishDiagnostics", diagParams)
	}()
	client.Initialize(ctx, "file:///workspace", nil)

	// Wait for notification to be processed.
	time.Sleep(100 * time.Millisecond)

	diags, err := client.Diagnostics(ctx, "file:///main.go")
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Message != "undefined: foo" {
		t.Errorf("message = %q", diags[0].Message)
	}
}

func TestLSPClient_CustomNotificationHandler(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	var received sync.Map
	handler := &testNotificationHandler{
		fn: func(method string, params json.RawMessage) {
			received.Store(method, string(params))
		},
	}
	client.OnNotification("custom/test", handler)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		server.sendNotification("custom/test", json.RawMessage(`{"value":42}`))
	}()
	client.Initialize(ctx, "file:///workspace", nil)

	time.Sleep(100 * time.Millisecond)

	val, ok := received.Load("custom/test")
	if !ok {
		t.Fatal("expected custom notification to be received")
	}
	if val != `{"value":42}` {
		t.Errorf("params = %v", val)
	}
}

type testNotificationHandler struct {
	fn func(method string, params json.RawMessage)
}

func (h *testNotificationHandler) HandleNotification(_ context.Context, method string, params json.RawMessage) error {
	h.fn(method, params)
	return nil
}

// ---------------------------------------------------------------------------
// LSPClient: EOF cleanup (connection drop)
// ---------------------------------------------------------------------------

func TestLSPClient_EOFRejectsAllPending(t *testing.T) {
	client, server := newClientPipe(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Server goroutine: handle initialize, then drain all subsequent requests
	// without responding (simulating a server that stops responding).
	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		// Drain requests without responding.
		for {
			_, err := server.readRequest()
			if err != nil {
				return
			}
		}
	}()

	client.Initialize(ctx, "file:///workspace", nil)

	// Start several requests that will never get responses.
	const n = 20
	errChs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := client.Hover(ctx, "file:///workspace/main.go", types.Position{Line: 0, Character: 0})
			errChs <- err
		}()
	}

	// Give goroutines time to register their pending requests.
	time.Sleep(100 * time.Millisecond)

	// Kill the connection (close server->client pipe).
	server.close()

	// All pending requests should be rejected.
	rejected := 0
	timeout := time.After(3 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case err := <-errChs:
			if err != nil {
				rejected++
			}
		case <-timeout:
			t.Fatalf("timeout: only %d/%d rejected", rejected, n)
		}
	}
	if rejected != n {
		t.Errorf("rejected %d/%d pending requests", rejected, n)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Concurrent requests stress test
// ---------------------------------------------------------------------------

func TestLSPClient_ConcurrentRequests(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initialize.
	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()
	}()
	client.Initialize(ctx, "file:///workspace", nil)

	const n = 100
	var wg sync.WaitGroup
	var errCount atomic.Int32

	wg.Add(n)

	// Server goroutine: reads requests and responds.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for i := 0; i < n; i++ {
			req, err := server.readRequest()
			if err != nil {
				return
			}
			result := json.RawMessage(fmt.Sprintf(`[{"uri":"file:///def_%d.go","range":{"start":{"line":%d,"character":0},"end":{"line":%d,"character":5}}}]`, req.ID, req.ID, req.ID))
			server.sendResponse(req.ID, result)
		}
	}()

	// Client goroutines: send concurrent requests.
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			locations, err := client.Definition(ctx, "file:///workspace/main.go", types.Position{Line: idx, Character: 0})
			if err != nil {
				errCount.Add(1)
				return
			}
			if len(locations) == 0 {
				errCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	if ec := errCount.Load(); ec > 0 {
		t.Errorf("got %d errors out of %d concurrent requests", ec, n)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Document synchronization
// ---------------------------------------------------------------------------

func TestLSPClient_OpenDocument(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		// Read the didOpen notification.
		raw, err := server.frameR.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				TextDocument struct {
					URI  string `json:"uri"`
					Text string `json:"text"`
				} `json:"textDocument"`
			} `json:"params"`
		}
		json.Unmarshal(raw, &msg)
		if msg.Method != "textDocument/didOpen" {
			t.Errorf("method = %q, want textDocument/didOpen", msg.Method)
		}
		if msg.Params.TextDocument.URI != "file:///main.go" {
			t.Errorf("uri = %q", msg.Params.TextDocument.URI)
		}
		if msg.Params.TextDocument.Text != "package main" {
			t.Errorf("text = %q", msg.Params.TextDocument.Text)
		}
	}()

	client.Initialize(ctx, "file:///workspace", nil)
	err := client.OpenDocument(ctx, "file:///main.go", "go", "package main")
	if err != nil {
		t.Fatalf("OpenDocument: %v", err)
	}
}

func TestLSPClient_ChangeDocument(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		raw, err := server.frameR.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			} `json:"params"`
		}
		json.Unmarshal(raw, &msg)
		if msg.Method != "textDocument/didChange" {
			t.Errorf("method = %q", msg.Method)
		}
		if len(msg.Params.ContentChanges) != 1 || msg.Params.ContentChanges[0].Text != "updated" {
			t.Errorf("content = %v", msg.Params.ContentChanges)
		}
	}()

	client.Initialize(ctx, "file:///workspace", nil)
	err := client.ChangeDocument(ctx, "file:///main.go", "updated")
	if err != nil {
		t.Fatalf("ChangeDocument: %v", err)
	}
}

func TestLSPClient_CloseDocument(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		raw, err := server.frameR.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			} `json:"params"`
		}
		json.Unmarshal(raw, &msg)
		if msg.Method != "textDocument/didClose" {
			t.Errorf("method = %q", msg.Method)
		}
		if msg.Params.TextDocument.URI != "file:///main.go" {
			t.Errorf("uri = %q", msg.Params.TextDocument.URI)
		}
	}()

	client.Initialize(ctx, "file:///workspace", nil)
	err := client.CloseDocument(ctx, "file:///main.go")
	if err != nil {
		t.Fatalf("CloseDocument: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: Shutdown
// ---------------------------------------------------------------------------

func TestLSPClient_Shutdown_Success(t *testing.T) {
	client, server := newClientPipe(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		// Read shutdown request.
		req2, err := server.readRequest()
		if err != nil {
			return
		}
		if req2.Method != "shutdown" {
			t.Errorf("method = %q, want shutdown", req2.Method)
		}
		server.sendResponse(req2.ID, json.RawMessage(`null`))

		// Read exit notification.
		server.frameR.ReadMessage()
	}()

	client.Initialize(ctx, "file:///workspace", nil)
	err := client.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if client.State() != StateShutdown {
		t.Errorf("state = %d, want %d", client.State(), StateShutdown)
	}
}

func TestLSPClient_Shutdown_NotInitialized(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	defer serverW.Close()
	defer serverR.Close()

	client := NewLSPClient(clientR, clientW).(*lspClientImpl)

	ctx := context.Background()
	err := client.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error shutting down uninitialized client")
	}
}

// ---------------------------------------------------------------------------
// LSPClient: High-pressure concurrent stress test
// ---------------------------------------------------------------------------

func TestLSPClient_HighPressureConcurrent(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Initialize.
	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()
	}()
	client.Initialize(ctx, "file:///workspace", nil)

	const writers = 20
	const readers = 20
	const opsPerG = 50

	var wg sync.WaitGroup
	var errCount atomic.Int32

	// Server goroutine: respond to everything.
	serverWg := sync.WaitGroup{}
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		total := writers*opsPerG + readers*opsPerG
		for i := 0; i < total; i++ {
			raw, err := server.frameR.ReadMessage()
			if err != nil {
				return
			}
			var probe struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			json.Unmarshal(raw, &probe)
			if probe.ID != nil {
				server.sendResponse(*probe.ID, json.RawMessage(`null`))
			}
			// Notifications have no id — just drain.
		}
	}()

	// Writer goroutines: send didChange notifications.
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				if err := client.ChangeDocument(ctx, fmt.Sprintf("file:///f%d.go", idx), fmt.Sprintf("content-%d-%d", idx, j)); err != nil {
					errCount.Add(1)
				}
			}
		}(w)
	}

	// Reader goroutines: send Definition requests.
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				_, err := client.Definition(ctx, fmt.Sprintf("file:///f%d.go", idx), types.Position{Line: j, Character: 0})
				if err != nil {
					errCount.Add(1)
				}
			}
		}(r)
	}

	wg.Wait()
	if ec := errCount.Load(); ec > 0 {
		t.Errorf("high-pressure test: %d errors", ec)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: RequestCorrelator integration — error response from server
// ---------------------------------------------------------------------------

func TestLSPClient_ErrorResponseFromServer(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()

		req, err = server.readRequest()
		if err != nil {
			return
		}
		server.sendErrorResponse(req.ID, -32601, "Method not found")
	}()

	client.Initialize(ctx, "file:///workspace", nil)

	_, err := client.Hover(ctx, "file:///main.go", types.Position{Line: 0, Character: 0})
	if err == nil {
		t.Fatal("expected error from server")
	}
	if !strings.Contains(err.Error(), "Method not found") {
		t.Errorf("error = %v, want contains 'Method not found'", err)
	}
}

// ---------------------------------------------------------------------------
// LSPClient: context cancellation mid-flight
// ---------------------------------------------------------------------------

func TestLSPClient_ContextCancellation(t *testing.T) {
	client, server := newClientPipe(t)
	defer server.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize.
	go func() {
		req, err := server.readRequest()
		if err != nil {
			return
		}
		server.sendResponse(req.ID, json.RawMessage(`{"capabilities":{}}`))
		server.frameR.ReadMessage()
	}()
	client.Initialize(ctx, "file:///workspace", nil)

	// Server never responds to hover.
	go func() {
		for {
			_, err := server.readRequest()
			if err != nil {
				return
			}
			// Don't respond.
		}
	}()

	reqCtx, reqCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer reqCancel()

	_, err := client.Hover(reqCtx, "file:///main.go", types.Position{Line: 0, Character: 0})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateFileURI
// ---------------------------------------------------------------------------

func TestCreateFileURI(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/project/main.go", "file:///home/user/project/main.go"},
		{"C:/Users/test/main.go", "file:///C:/Users/test/main.go"},
	}
	for _, tt := range tests {
		got := CreateFileURI(tt.input)
		if !strings.HasPrefix(got, "file:///") {
			t.Errorf("CreateFileURI(%q) = %q, want file:/// prefix", tt.input, got)
		}
	}
}
