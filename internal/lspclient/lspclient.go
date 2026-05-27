package lspclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Uvean-z/aegislsp/internal/types"
)

// ---------------------------------------------------------------------------
// Low-level framing
// ---------------------------------------------------------------------------

// FrameReader reads Content-Length framed JSON-RPC messages from an io.Reader.
// The LSP wire format is:
//
//	Content-Length: <byte-count>\r\n
//	\r\n
//	<JSON body of exactly <byte-count> bytes>
type FrameReader interface {
	// ReadMessage reads the next complete message from the underlying reader.
	// It blocks until a full frame is available or an error occurs.
	ReadMessage() (json.RawMessage, error)
}

// FrameWriter writes Content-Length framed JSON-RPC messages to an io.Writer.
type FrameWriter interface {
	// WriteMessage writes msg with a Content-Length header.
	WriteMessage(msg json.RawMessage) error
}

// ---------------------------------------------------------------------------
// Concrete implementations
// ---------------------------------------------------------------------------

// frameReader implements FrameReader over an io.Reader.
type frameReader struct {
	br *bufio.Reader
}

// NewFrameReader returns a FrameReader that reads from r.
func NewFrameReader(r io.Reader) FrameReader {
	return &frameReader{br: bufio.NewReader(r)}
}

// ReadMessage reads one complete LSP frame. It parses headers until the blank
// line, extracts Content-Length, then reads exactly that many bytes as the body.
func (r *frameReader) ReadMessage() (json.RawMessage, error) {
	// Parse headers until we hit the empty line (\r\n).
	contentLength := -1
	for {
		line, err := r.readLine()
		if err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		// Empty line signals end of headers.
		if line == "" {
			break
		}
		// Headers are case-insensitive per LSP spec, but Content-Length is
		// always sent as "Content-Length" in practice.
		key, value, ok := splitHeader(line)
		if !ok {
			continue // skip malformed header lines
		}
		if strings.EqualFold(key, "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q: %w", value, err)
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	// Read exactly contentLength bytes — no more, no less.
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r.br, body); err != nil {
		return nil, fmt.Errorf("read body (%d bytes): %w", contentLength, err)
	}
	return json.RawMessage(body), nil
}

// readLine reads a single header line terminated by \r\n. Returns the line
// content without the trailing \r\n. An empty line ("\r\n" alone) returns "".
func (r *frameReader) readLine() (string, error) {
	var buf []byte
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return "", err
		}
		buf = append(buf, b)
		if len(buf) >= 2 && buf[len(buf)-2] == '\r' && buf[len(buf)-1] == '\n' {
			return string(buf[:len(buf)-2]), nil
		}
	}
}

// splitHeader splits "Key: Value" into key and value.
func splitHeader(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return line[:idx], line[idx+1:], true
}

// frameWriter implements FrameWriter over an io.Writer.
type frameWriter struct {
	w io.Writer
}

// NewFrameWriter returns a FrameWriter that writes to w.
func NewFrameWriter(w io.Writer) FrameWriter {
	return &frameWriter{w: w}
}

// WriteMessage writes a single LSP frame: "Content-Length: N\r\n\r\n" + body.
func (w *frameWriter) WriteMessage(msg json.RawMessage) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(msg))
	if _, err := io.WriteString(w.w, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// JSON-RPC message types
// ---------------------------------------------------------------------------

// Request represents an outgoing JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents an incoming JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *json.RawMessage `json:"error,omitempty"`
}

// Notification represents an incoming JSON-RPC 2.0 notification (no id field).
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ---------------------------------------------------------------------------
// Request/response correlation
// ---------------------------------------------------------------------------

// RequestCorrelator manages the lifecycle of in-flight requests.
// It maps request IDs to one-shot buffered channels so the read loop
// can dispatch responses to the correct waiting goroutine.
type RequestCorrelator interface {
	// Register records a new pending request with the given ID and returns
	// a pair of buffered(1) channels: one for the result, one for errors.
	// The caller blocks on these channels until the read loop resolves or
	// rejects the request.
	Register(id int) (resultCh chan json.RawMessage, errCh chan error)

	// Resolve sends result on the channel registered for id, unblocking
	// the caller. It is a no-op if id is not registered (already resolved
	// or never registered).
	Resolve(id int, result json.RawMessage)

	// Reject sends err on the error channel registered for id.
	Reject(id int, err error)

	// RejectAll sends err on every pending request's error channel and
	// clears the pending map. Called on subprocess crash.
	RejectAll(err error)
}

// ---------------------------------------------------------------------------
// Notification handling
// ---------------------------------------------------------------------------

// NotificationHandler processes server-initiated notifications.
// Implementations are registered with the client and called from the
// read loop goroutine for each incoming notification.
type NotificationHandler interface {
	// HandleNotification processes a notification identified by method.
	// The params payload is raw JSON; implementations unmarshal only the
	// fields they need. HandleNotification runs on the read loop goroutine,
	// so it must not block for extended periods.
	HandleNotification(ctx context.Context, method string, params json.RawMessage) error
}

// ---------------------------------------------------------------------------
// Lifecycle management
// ---------------------------------------------------------------------------

// LifecycleState represents the state of the LSP client lifecycle.
type LifecycleState int

const (
	// StateUninitialized is the initial state before Initialize is called.
	StateUninitialized LifecycleState = iota
	// StateInitializing is set while the initialize request is in flight.
	StateInitializing
	// StateInitialized is set after the server responds to initialize and
	// the "initialized" notification has been sent.
	StateInitialized
	// StateShuttingDown is set while the shutdown request is in flight.
	StateShuttingDown
	// StateShutdown is set after the "exit" notification has been sent.
	StateShutdown
)

// LifecycleManager manages the Initialize/Shutdown/Exit handshake sequence.
type LifecycleManager interface {
	// State returns the current lifecycle state.
	State() LifecycleState

	// Initialize sends the LSP "initialize" request with the given root URI
	// and workspace folders, waits for the response, captures server
	// capabilities, then sends the "initialized" notification.
	// The context must carry a timeout; typical values are 30s for fast
	// servers, 300s for cold-start JVM servers.
	Initialize(ctx context.Context, rootURI string, workspaceFolders []types.WorkspaceFolder) error

	// Shutdown sends the LSP "shutdown" request and waits for the response.
	// It does not send "exit"; call Exit separately.
	Shutdown(ctx context.Context) error

	// Exit sends the LSP "exit" notification. Call after Shutdown.
	Exit(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Main LSP client
// ---------------------------------------------------------------------------

// LSPClient is the primary interface for interacting with a language server.
// All methods accept context.Context and return error as the last value.
// json.RawMessage is used for polymorphic LSP responses to avoid premature
// deserialization — normalization to concrete types happens at the consumer layer.
type LSPClient interface {
	// --- Lifecycle ---

	// Initialize performs the full LSP handshake (initialize request +
	// initialized notification). Must be called before any other method.
	Initialize(ctx context.Context, rootURI string, workspaceFolders []types.WorkspaceFolder) error

	// Shutdown performs the LSP shutdown/exit sequence and tears down
	// internal goroutines (read loop, stderr drainer, exit monitor).
	Shutdown(ctx context.Context) error

	// --- Document synchronization ---

	// OpenDocument sends textDocument/didOpen for a new document.
	OpenDocument(ctx context.Context, uri string, languageID string, content string) error

	// CloseDocument sends textDocument/didClose.
	CloseDocument(ctx context.Context, uri string) error

	// ChangeDocument sends textDocument/didChange with the full new content.
	ChangeDocument(ctx context.Context, uri string, content string) error

	// --- Navigation ---

	// Definition sends textDocument/definition and returns the locations.
	Definition(ctx context.Context, uri string, position types.Position) ([]types.Location, error)

	// TypeDefinition sends textDocument/typeDefinition.
	TypeDefinition(ctx context.Context, uri string, position types.Position) ([]types.Location, error)

	// Implementation sends textDocument/implementation.
	Implementation(ctx context.Context, uri string, position types.Position) ([]types.Location, error)

	// References sends textDocument/references.
	References(ctx context.Context, uri string, position types.Position) ([]types.Location, error)

	// --- Analysis ---

	// Hover sends textDocument/hover. Returns raw JSON because hover content
	// is polymorphic (MarkupContent | MarkedString | MarkedString[]).
	Hover(ctx context.Context, uri string, position types.Position) (json.RawMessage, error)

	// DocumentSymbols sends textDocument/documentSymbol. Returns raw JSON
	// because the response may be DocumentSymbol[] or SymbolInformation[].
	DocumentSymbols(ctx context.Context, uri string) (json.RawMessage, error)

	// WorkspaceSymbols sends workspace/symbol.
	WorkspaceSymbols(ctx context.Context, query string) (json.RawMessage, error)

	// Diagnostics returns the last published diagnostics for the given URI.
	Diagnostics(ctx context.Context, uri string) ([]types.Diagnostic, error)

	// --- Notifications ---

	// OnNotification registers a handler for server-initiated notifications
	// with the given method name. Multiple handlers per method are allowed.
	OnNotification(method string, handler NotificationHandler)

	// --- Status ---

	// IsInitialized returns true if the client has completed the initialize
	// handshake and has not yet begun shutdown.
	IsInitialized() bool
}

// ---------------------------------------------------------------------------
// Multi-server routing
// ---------------------------------------------------------------------------

// ClientResolver routes file paths to the appropriate LSPClient based on
// file extension. In single-server mode, all methods return the same client.
type ClientResolver interface {
	// ClientForFile returns the LSPClient responsible for the given file
	// path, determined by file extension. Falls back to DefaultClient if
	// no extension-specific client is registered.
	ClientForFile(filePath string) LSPClient

	// DefaultClient returns the primary (or only) LSPClient.
	DefaultClient() LSPClient

	// AllClients returns every registered LSPClient.
	AllClients() []LSPClient

	// Shutdown calls Shutdown on every registered client.
	Shutdown(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// RequestCorrelator implementation
// ---------------------------------------------------------------------------

type pendingEntry struct {
	resultCh chan json.RawMessage
	errCh    chan error
}

type requestCorrelatorImpl struct {
	mu      sync.Mutex
	pending map[int]pendingEntry
}

// NewRequestCorrelator returns a new RequestCorrelator.
func NewRequestCorrelator() RequestCorrelator {
	return &requestCorrelatorImpl{
		pending: make(map[int]pendingEntry),
	}
}

// Register records a new pending request with the given ID. It returns two
// buffered(1) channels: one for the successful result, one for errors. The
// caller blocks on these until Resolve or Reject is called from the read loop.
// Safe for concurrent use (guarded by c.mu).
func (c *requestCorrelatorImpl) Register(id int) (chan json.RawMessage, chan error) {
	ch := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)
	c.mu.Lock()
	c.pending[id] = pendingEntry{resultCh: ch, errCh: errCh}
	c.mu.Unlock()
	return ch, errCh
}

// Resolve delivers result on the channel registered for id and removes the
// pending entry. No-op if id is not registered (already resolved or unknown).
// Safe for concurrent use.
func (c *requestCorrelatorImpl) Resolve(id int, result json.RawMessage) {
	c.mu.Lock()
	entry, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		entry.resultCh <- result
	}
}

// Reject delivers err on the error channel registered for id and removes the
// pending entry. No-op if id is not registered. Safe for concurrent use.
func (c *requestCorrelatorImpl) Reject(id int, err error) {
	c.mu.Lock()
	entry, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		entry.errCh <- err
	}
}

// RejectAll sends err on every pending request's error channel and clears the
// pending map. Typically called when the server connection is lost (EOF or
// crash) to unblock all waiting goroutines. Safe for concurrent use.
func (c *requestCorrelatorImpl) RejectAll(err error) {
	c.mu.Lock()
	entries := make(map[int]pendingEntry, len(c.pending))
	for id, e := range c.pending {
		entries[id] = e
	}
	c.pending = make(map[int]pendingEntry)
	c.mu.Unlock()

	for _, e := range entries {
		e.errCh <- err
	}
}

// ---------------------------------------------------------------------------
// LSPClient implementation
// ---------------------------------------------------------------------------

type lspClientImpl struct {
	frameR FrameReader
	frameW FrameWriter
	corr   RequestCorrelator

	mu     sync.Mutex
	nextID int
	state  LifecycleState

	writeMu  sync.Mutex // guards frameW writes separately from state mu
	readDone chan struct{}

	handlers    map[string][]NotificationHandler
	handlerMu   sync.RWMutex
	diagnostics map[string][]types.Diagnostic
	diagMu      sync.RWMutex
}

// NewLSPClient creates a new LSPClient over the given reader/writer.
func NewLSPClient(r io.Reader, w io.Writer) LSPClient {
	return &lspClientImpl{
		frameR:      NewFrameReader(r),
		frameW:      NewFrameWriter(w),
		corr:        NewRequestCorrelator(),
		state:       StateUninitialized,
		readDone:    make(chan struct{}),
		handlers:    make(map[string][]NotificationHandler),
		diagnostics: make(map[string][]types.Diagnostic),
	}
}

// nextRequestID returns the next monotonically increasing request ID.
func (c *lspClientImpl) nextRequestID() int {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()
	return id
}

// sendRequest writes a JSON-RPC request to the wire.
func (c *lspClientImpl) sendRequest(req Request) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.frameW.WriteMessage(raw)
}

// sendNotification writes a JSON-RPC notification (no id) to the wire.
func (c *lspClientImpl) sendNotification(method string, params json.RawMessage) error {
	msg := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.frameW.WriteMessage(raw)
}

// call sends a request and blocks until the response arrives or ctx is done.
func (c *lspClientImpl) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var paramsRaw json.RawMessage
	if params != nil {
		var err error
		paramsRaw, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
	}

	id := c.nextRequestID()
	resultCh, errCh := c.corr.Register(id)

	req := Request{JSONRPC: "2.0", ID: id, Method: method, Params: paramsRaw}
	if err := c.sendRequest(req); err != nil {
		c.corr.Reject(id, err)
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		c.corr.Reject(id, ctx.Err())
		return nil, ctx.Err()
	}
}

// sendCall is like call but discards the result (used for methods where we only care about success).
func (c *lspClientImpl) sendCall(ctx context.Context, method string, params any) error {
	_, err := c.call(ctx, method, params)
	return err
}

// readLoop reads frames from the server and dispatches them.
// It runs until the reader returns an error (typically EOF on connection close).
func (c *lspClientImpl) readLoop() {
	defer close(c.readDone)

	for {
		raw, err := c.frameR.ReadMessage()
		if err != nil {
			// Connection closed or read error — reject all pending requests.
			c.corr.RejectAll(fmt.Errorf("read loop: %w", err))
			return
		}

		// Determine if this is a response (has "id", no "method") or a notification.
		var probe struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  json.RawMessage `json:"error,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			// Malformed message — skip.
			continue
		}

		if probe.ID != nil && probe.Method == "" {
			// This is a response to a request we sent.
			if probe.Error != nil {
				var errObj struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(probe.Error, &errObj) == nil {
					c.corr.Reject(*probe.ID, fmt.Errorf("LSP error %d: %s", errObj.Code, errObj.Message))
				} else {
					c.corr.Reject(*probe.ID, fmt.Errorf("LSP error: %s", string(probe.Error)))
				}
			} else {
				c.corr.Resolve(*probe.ID, probe.Result)
			}
		} else if probe.Method != "" {
			// Server-initiated notification.
			c.dispatchNotification(probe.Method, probe.Params)
		}
	}
}

// dispatchNotification delivers a notification to all registered handlers.
func (c *lspClientImpl) dispatchNotification(method string, params json.RawMessage) {
	// Always handle publishDiagnostics internally.
	if method == "textDocument/publishDiagnostics" {
		c.handlePublishDiagnostics(params)
	}

	c.handlerMu.RLock()
	handlers := c.handlers[method]
	c.handlerMu.RUnlock()

	for _, h := range handlers {
		h.HandleNotification(context.Background(), method, params)
	}
}

// handlePublishDiagnostics stores diagnostics for the given URI.
func (c *lspClientImpl) handlePublishDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string             `json:"uri"`
		Diagnostics []types.Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	c.diagMu.Lock()
	c.diagnostics[p.URI] = p.Diagnostics
	c.diagMu.Unlock()
}

// ---------------------------------------------------------------------------
// Lifecycle methods
// ---------------------------------------------------------------------------

// State returns the current lifecycle state. Safe for concurrent use.
func (c *lspClientImpl) State() LifecycleState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// IsInitialized returns true if the client has completed the initialize
// handshake and has not yet begun shutdown. Safe for concurrent use.
func (c *lspClientImpl) IsInitialized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == StateInitialized
}

// Initialize performs the full LSP handshake: starts the read loop goroutine,
// sends the "initialize" request, transitions to StateInitialized, and sends
// the "initialized" notification. Returns an error if the client is not in
// StateUninitialized or if the server rejects the request.
func (c *lspClientImpl) Initialize(ctx context.Context, rootURI string, workspaceFolders []types.WorkspaceFolder) error {
	c.mu.Lock()
	if c.state != StateUninitialized {
		c.mu.Unlock()
		return fmt.Errorf("cannot initialize: current state %d", c.state)
	}
	c.state = StateInitializing
	c.mu.Unlock()

	// Start read loop.
	go c.readLoop()

	// Build initialize params.
	initParams := struct {
		ProcessID        int                     `json:"processId"`
		RootURI          string                  `json:"rootUri"`
		Capabilities     map[string]any          `json:"capabilities"`
		WorkspaceFolders []types.WorkspaceFolder `json:"workspaceFolders,omitempty"`
	}{
		ProcessID: 0,
		RootURI:   rootURI,
		Capabilities: map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"definition": map[string]any{
					"dynamicRegistration": false,
				},
			},
		},
		WorkspaceFolders: workspaceFolders,
	}

	result, err := c.call(ctx, "initialize", initParams)
	if err != nil {
		c.mu.Lock()
		c.state = StateUninitialized
		c.mu.Unlock()
		return fmt.Errorf("initialize: %w", err)
	}

	// Discard result for now (could parse server capabilities).
	_ = result

	c.mu.Lock()
	c.state = StateInitialized
	c.mu.Unlock()

	// Send initialized notification.
	if err := c.sendNotification("initialized", json.RawMessage(`{}`)); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	return nil
}

// Shutdown sends the LSP "shutdown" request followed by the "exit"
// notification and transitions to StateShutdown. Returns an error if the
// client is not in StateInitialized.
func (c *lspClientImpl) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.state != StateInitialized {
		c.mu.Unlock()
		return fmt.Errorf("cannot shutdown: current state %d", c.state)
	}
	c.state = StateShuttingDown
	c.mu.Unlock()

	if err := c.sendCall(ctx, "shutdown", nil); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	if err := c.sendNotification("exit", nil); err != nil {
		return fmt.Errorf("exit notification: %w", err)
	}

	c.mu.Lock()
	c.state = StateShutdown
	c.mu.Unlock()

	return nil
}

// ---------------------------------------------------------------------------
// Document synchronization (notifications — no response expected)
// ---------------------------------------------------------------------------

// OpenDocument sends textDocument/didOpen to notify the server of a newly
// opened document. This is a notification (no response expected).
func (c *lspClientImpl) OpenDocument(ctx context.Context, uri string, languageID string, content string) error {
	params := struct {
		TextDocument struct {
			URI        string `json:"uri"`
			LanguageID string `json:"languageId"`
			Version    int    `json:"version"`
			Text       string `json:"text"`
		} `json:"textDocument"`
	}{}
	params.TextDocument.URI = uri
	params.TextDocument.LanguageID = languageID
	params.TextDocument.Version = 1
	params.TextDocument.Text = content

	raw, _ := json.Marshal(params)
	return c.sendNotification("textDocument/didOpen", raw)
}

// CloseDocument sends textDocument/didClose to notify the server that a
// document has been closed. This is a notification (no response expected).
func (c *lspClientImpl) CloseDocument(ctx context.Context, uri string) error {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}{}
	params.TextDocument.URI = uri

	raw, _ := json.Marshal(params)
	return c.sendNotification("textDocument/didClose", raw)
}

// ChangeDocument sends textDocument/didChange with the full new content of
// the document. This is a notification (no response expected).
func (c *lspClientImpl) ChangeDocument(ctx context.Context, uri string, content string) error {
	params := struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int    `json:"version"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}{}
	params.TextDocument.URI = uri
	params.TextDocument.Version = 1
	params.ContentChanges = []struct {
		Text string `json:"text"`
	}{{Text: content}}

	raw, _ := json.Marshal(params)
	return c.sendNotification("textDocument/didChange", raw)
}

// ---------------------------------------------------------------------------
// Navigation methods
// ---------------------------------------------------------------------------

// Definition sends textDocument/definition and returns the locations of the
// symbol definition. Handles servers that return a single Location instead
// of an array by normalizing to a slice.
func (c *lspClientImpl) Definition(ctx context.Context, uri string, position types.Position) ([]types.Location, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position types.Position `json:"position"`
	}{
		Position: position,
	}
	params.TextDocument.URI = uri

	result, err := c.call(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}

	var locations []types.Location
	if err := json.Unmarshal(result, &locations); err != nil {
		// Some servers return a single Location instead of an array.
		var single types.Location
		if err2 := json.Unmarshal(result, &single); err2 == nil {
			return []types.Location{single}, nil
		}
		return nil, fmt.Errorf("unmarshal definition: %w", err)
	}
	return locations, nil
}

// TypeDefinition sends textDocument/typeDefinition and returns the locations
// of the type definition. Normalizes single-Location responses to a slice.
func (c *lspClientImpl) TypeDefinition(ctx context.Context, uri string, position types.Position) ([]types.Location, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position types.Position `json:"position"`
	}{}
	params.TextDocument.URI = uri
	params.Position = position

	result, err := c.call(ctx, "textDocument/typeDefinition", params)
	if err != nil {
		return nil, err
	}

	var locations []types.Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var single types.Location
		if err2 := json.Unmarshal(result, &single); err2 == nil {
			return []types.Location{single}, nil
		}
		return nil, fmt.Errorf("unmarshal typeDefinition: %w", err)
	}
	return locations, nil
}

// Implementation sends textDocument/implementation and returns the locations
// of concrete implementations. Normalizes single-Location responses to a slice.
func (c *lspClientImpl) Implementation(ctx context.Context, uri string, position types.Position) ([]types.Location, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position types.Position `json:"position"`
	}{}
	params.TextDocument.URI = uri
	params.Position = position

	result, err := c.call(ctx, "textDocument/implementation", params)
	if err != nil {
		return nil, err
	}

	var locations []types.Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var single types.Location
		if err2 := json.Unmarshal(result, &single); err2 == nil {
			return []types.Location{single}, nil
		}
		return nil, fmt.Errorf("unmarshal implementation: %w", err)
	}
	return locations, nil
}

// References sends textDocument/references with includeDeclaration=true and
// returns all reference locations for the symbol at the given position.
func (c *lspClientImpl) References(ctx context.Context, uri string, position types.Position) ([]types.Location, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position types.Position `json:"position"`
		Context  struct {
			IncludeDeclaration bool `json:"includeDeclaration"`
		} `json:"context"`
	}{}
	params.TextDocument.URI = uri
	params.Position = position
	params.Context.IncludeDeclaration = true

	result, err := c.call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}

	var locations []types.Location
	if err := json.Unmarshal(result, &locations); err != nil {
		return nil, fmt.Errorf("unmarshal references: %w", err)
	}
	return locations, nil
}

// ---------------------------------------------------------------------------
// Analysis methods
// ---------------------------------------------------------------------------

// Hover sends textDocument/hover and returns the raw JSON response. The
// response is polymorphic (MarkupContent | MarkedString | MarkedString[])
// so it is returned as-is for the consumer to normalize.
func (c *lspClientImpl) Hover(ctx context.Context, uri string, position types.Position) (json.RawMessage, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position types.Position `json:"position"`
	}{}
	params.TextDocument.URI = uri
	params.Position = position

	return c.call(ctx, "textDocument/hover", params)
}

// DocumentSymbols sends textDocument/documentSymbol and returns the raw JSON
// response, which may be either DocumentSymbol[] or SymbolInformation[]
// depending on server capabilities.
func (c *lspClientImpl) DocumentSymbols(ctx context.Context, uri string) (json.RawMessage, error) {
	params := struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}{}
	params.TextDocument.URI = uri

	return c.call(ctx, "textDocument/documentSymbol", params)
}

// WorkspaceSymbols sends workspace/symbol with the given query string and
// returns the raw JSON array of SymbolInformation objects.
func (c *lspClientImpl) WorkspaceSymbols(ctx context.Context, query string) (json.RawMessage, error) {
	params := struct {
		Query string `json:"query"`
	}{Query: query}

	return c.call(ctx, "workspace/symbol", params)
}

// Diagnostics returns the last published diagnostics for the given URI from
// the internal cache (populated by textDocument/publishDiagnostics
// notifications). Returns nil if no diagnostics have been received for the URI.
// Safe for concurrent use.
func (c *lspClientImpl) Diagnostics(ctx context.Context, uri string) ([]types.Diagnostic, error) {
	c.diagMu.RLock()
	diags := c.diagnostics[uri]
	c.diagMu.RUnlock()
	return diags, nil
}

// ---------------------------------------------------------------------------
// Notification registration
// ---------------------------------------------------------------------------

// OnNotification registers a handler for server-initiated notifications with
// the given method name. Multiple handlers per method are supported. Handlers
// are called from the read loop goroutine. Safe for concurrent use.
func (c *lspClientImpl) OnNotification(method string, handler NotificationHandler) {
	c.handlerMu.Lock()
	c.handlers[method] = append(c.handlers[method], handler)
	c.handlerMu.Unlock()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// CreateFileURI converts a file path to a file:// URI.
func CreateFileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	return "file:///" + strings.TrimPrefix(abs, "/")
}
