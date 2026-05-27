package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 message types used by MCP.

// Request represents an incoming JSON-RPC 2.0 request with an ID field.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents an outgoing JSON-RPC 2.0 response containing either
// a result or an error.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification represents an incoming JSON-RPC 2.0 notification (no ID field).
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error object with a numeric code,
// human-readable message, and optional additional data.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
// Standard JSON-RPC 2.0 error codes as defined in the specification.
const (
	// ParseError indicates the JSON was malformed or could not be parsed.
	ParseError = -32700
	// InvalidRequest indicates the JSON-RPC request object is not valid.
	InvalidRequest = -32600
	// MethodNotFound indicates the requested method does not exist.
	MethodNotFound = -32601
	// InvalidParams indicates the method parameters are invalid.
	InvalidParams = -32602
	// InternalError indicates an internal JSON-RPC error.
	InternalError = -32603
)

// MCP protocol constants.
const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "aegislsp"
	ServerVersion   = "0.1.0"
)

// ToolDef describes a single tool exposed by the MCP server.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolResult is the content returned by a tool invocation.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// ContentBlock is a single content item in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolHandler is the function signature for tool implementations.
// It receives the parsed arguments and returns a ToolResult or error.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolResult, error)

// toolEntry pairs a tool definition with its handler.
type toolEntry struct {
	def     ToolDef
	handler ToolHandler
}

// Server is a minimal MCP server over stdio transport.
// It reads newline-delimited JSON-RPC from stdin and writes responses to stdout.
type Server struct {
	reader io.Reader
	writer io.Writer
	tools  map[string]toolEntry
	mu     sync.Mutex
	// initialized tracks whether the initialize handshake completed.
	initialized bool
}

// NewServer creates a new MCP server with the given stdio transport.
func NewServer(r io.Reader, w io.Writer) *Server {
	return &Server{
		reader: r,
		writer: w,
		tools:  make(map[string]toolEntry),
	}
}

// RegisterTool registers a tool with its definition and handler.
func (s *Server) RegisterTool(def ToolDef, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[def.Name] = toolEntry{def: def, handler: handler}
}

// Run starts the server loop. It blocks until the context is cancelled or
// the reader returns io.EOF. Run is safe to call only once.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)
	// Allow up to 1MB per line (tool arguments can be large).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	enc := json.NewEncoder(s.writer)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Distinguish request (has "id") from notification (no "id").
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			resp := s.errorResponse(nil, ParseError, "parse error", err.Error())
			enc.Encode(resp)
			continue
		}

		if msg.ID != nil {
			// It's a request — dispatch and respond.
			var req Request
			if err := json.Unmarshal(line, &req); err != nil {
				resp := s.errorResponse(nil, InvalidRequest, "invalid request", nil)
				enc.Encode(resp)
				continue
			}
			resp := s.dispatch(ctx, &req)
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("write response: %w", err)
			}
		} else {
			// It's a notification — handle silently (no response).
			var notif Notification
			json.Unmarshal(line, &notif)
			s.handleNotification(&notif)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// If the loop ended due to EOF but the context was cancelled, prefer ctx.Err().
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// dispatch routes a request to the appropriate handler.
func (s *Server) dispatch(ctx context.Context, req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return s.errorResponse(req.ID, MethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method), nil)
	}
}

// handleInitialize processes the MCP initialize handshake.
func (s *Server) handleInitialize(req *Request) *Response {
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	result := map[string]interface{}{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]interface{}{
			"name":    ServerName,
			"version": ServerVersion,
		},
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsList returns the list of registered tools.
func (s *Server) handleToolsList(req *Request) *Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	tools := make([]ToolDef, 0, len(s.tools))
	for _, entry := range s.tools {
		tools = append(tools, entry.def)
	}

	result := map[string]interface{}{
		"tools": tools,
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsCall invokes a registered tool by name.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.errorResponse(req.ID, InvalidParams, "invalid params", err.Error())
	}

	s.mu.Lock()
	entry, ok := s.tools[params.Name]
	s.mu.Unlock()

	if !ok {
		return s.errorResponse(req.ID, InvalidParams,
			fmt.Sprintf("unknown tool: %s", params.Name), nil)
	}

	result, err := entry.handler(ctx, params.Arguments)
	if err != nil {
		// Return a tool-level error (isError: true) rather than a JSON-RPC error.
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolResult{
				Content: []ContentBlock{{Type: "text", Text: err.Error()}},
				IsError: true,
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleNotification processes notifications (currently a no-op).
func (s *Server) handleNotification(_ *Notification) {
	// notifications/initialized — no action required.
}

// errorResponse builds a JSON-RPC error response.
func (s *Server) errorResponse(id json.RawMessage, code int, msg string, data interface{}) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	}
}
