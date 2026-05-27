package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// helper to send a JSON-RPC request and read the response line.
func roundTrip(t *testing.T, srv *Server, input string) Response {
	t.Helper()
	r := strings.NewReader(input + "\n")
	var out strings.Builder
	s := NewServer(r, &out)
	// Copy tools from srv.
	srv.mu.Lock()
	for name, entry := range srv.tools {
		s.tools[name] = entry
	}
	srv.mu.Unlock()

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Parse the last (only) line of output.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp Response
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, lines[len(lines)-1])
	}
	return resp
}

func TestServer_Initialize(t *testing.T) {
	srv := NewServer(nil, nil)
	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", result["protocolVersion"], ProtocolVersion)
	}
	info := result["serverInfo"].(map[string]interface{})
	if info["name"] != ServerName {
		t.Errorf("serverInfo.name = %v, want %s", info["name"], ServerName)
	}
}

func TestServer_ToolsList_Empty(t *testing.T) {
	srv := NewServer(nil, nil)
	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	tools := result["tools"].([]interface{})
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestServer_ToolsList_WithTool(t *testing.T) {
	srv := NewServer(nil, nil)
	schema := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	srv.RegisterTool(ToolDef{
		Name:        "echo",
		Description: "Echo input",
		InputSchema: schema,
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return &ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
	})

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	tools := result["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "echo" {
		t.Errorf("tool name = %v, want echo", tool["name"])
	}
}

func TestServer_ToolsCall_Success(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.RegisterTool(ToolDef{
		Name:        "greet",
		Description: "Greet someone",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		var params struct {
			Name string `json:"name"`
		}
		json.Unmarshal(args, &params)
		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: "Hello, " + params.Name}},
		}, nil
	})

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	content := result["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if block["text"] != "Hello, World" {
		t.Errorf("text = %v, want 'Hello, World'", block["text"])
	}
	if result["isError"] != false {
		t.Errorf("isError = %v, want false", result["isError"])
	}
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	srv := NewServer(nil, nil)
	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`)

	// Should be a JSON-RPC error for unknown tool.
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != InvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, InvalidParams)
	}
}

func TestServer_ToolsCall_HandlerError(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.RegisterTool(ToolDef{
		Name:        "fail",
		Description: "Always fails",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return nil, fmt.Errorf("something went wrong")
	})

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"fail","arguments":{}}}`)

	// Handler errors should be tool-level errors (isError: true), not JSON-RPC errors.
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result := resp.Result.(map[string]interface{})
	if result["isError"] != true {
		t.Errorf("isError = %v, want true", result["isError"])
	}
}

func TestServer_MethodNotFound(t *testing.T) {
	srv := NewServer(nil, nil)
	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":7,"method":"unknown/method","params":{}}`)

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != MethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, MethodNotFound)
	}
}

func TestServer_ParseError(t *testing.T) {
	r := strings.NewReader("not valid json\n")
	var out strings.Builder
	s := NewServer(r, &out)

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp Response
	json.Unmarshal([]byte(lines[0]), &resp)

	if resp.Error == nil {
		t.Fatal("expected parse error")
	}
	if resp.Error.Code != ParseError {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ParseError)
	}
}

func TestServer_NotificationIgnored(t *testing.T) {
	// Notifications have no "id" field and should not produce a response.
	r := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out strings.Builder
	s := NewServer(r, &out)

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output for notification, got: %s", out.String())
	}
}

func TestServer_ContextCancellation(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	var out strings.Builder
	s := NewServer(pr, &out)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx)
	}()

	// Cancel and unblock the reader.
	cancel()
	pw.Close()

	err := <-done
	if err != context.Canceled {
		t.Errorf("Run() = %v, want context.Canceled", err)
	}
}
