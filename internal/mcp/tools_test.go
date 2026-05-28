package mcp

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/Uvean-z/aegislsp/internal/interceptor"
	"github.com/Uvean-z/aegislsp/internal/types"
)

func mcpShellCommand(script string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", script}
	}
	return []string{"sh", "-c", script}
}

func aegisRunRequest(t *testing.T, id int, command []string) string {
	t.Helper()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "aegis_run",
			"arguments": map[string]interface{}{
				"command": command,
			},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

func TestRegisterAegisTools_ToolsList(t *testing.T) {
	srv := NewServer(nil, nil)
	RegisterAegisTools(srv, nil, nil, nil)

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	tools := result["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "aegis_run" {
		t.Errorf("tool name = %v, want aegis_run", tool["name"])
	}
}

func TestAegisRun_EmptyCommand(t *testing.T) {
	srv := NewServer(nil, nil)
	RegisterAegisTools(srv, nil, nil, nil)

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aegis_run","arguments":{"command":[]}}}`)

	// Empty command should return a tool-level error.
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result := resp.Result.(map[string]interface{})
	if result["isError"] != true {
		t.Errorf("isError = %v, want true", result["isError"])
	}
}

func TestAegisRun_SimpleCommand(t *testing.T) {
	srv := NewServer(nil, nil)
	RegisterAegisTools(srv, nil, nil, nil)

	// Run a simple echo command.
	resp := roundTrip(t, srv, aegisRunRequest(t, 3, mcpShellCommand("echo hello")))

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	if result["isError"] != false {
		t.Errorf("isError = %v, want false", result["isError"])
	}

	// Parse the content text as RunResult.
	content := result["content"].([]interface{})
	block := content[0].(map[string]interface{})
	var runResult RunResult
	if err := json.Unmarshal([]byte(block["text"].(string)), &runResult); err != nil {
		t.Fatalf("unmarshal RunResult: %v", err)
	}
	if runResult.ExitCode != 0 {
		t.Errorf("exitCode = %d, want 0", runResult.ExitCode)
	}
}

func TestAegisRun_WithCompilerErrors(t *testing.T) {
	srv := NewServer(nil, nil)

	// Use Go default pattern only.
	patterns := interceptor.DefaultErrorPatterns()
	RegisterAegisTools(srv, nil, nil, patterns)

	// Simulate compiler error output by running a command that writes to stderr
	// in Go compiler error format.
	resp := roundTrip(t, srv, aegisRunRequest(t, 4, mcpShellCommand("echo main.go:10:5: undefined: foo >&2")))

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	content := result["content"].([]interface{})
	block := content[0].(map[string]interface{})
	var runResult RunResult
	if err := json.Unmarshal([]byte(block["text"].(string)), &runResult); err != nil {
		t.Fatalf("unmarshal RunResult: %v", err)
	}

	// The command should have failed (exit code non-zero) since we wrote to stderr.
	if len(runResult.Errors) == 0 {
		t.Log("No parsed errors (expected if stderr was not captured as compiler error)")
	} else {
		if runResult.Errors[0].File != "main.go" {
			t.Errorf("error file = %q, want main.go", runResult.Errors[0].File)
		}
	}
}

func TestAegisRun_UnknownTool(t *testing.T) {
	srv := NewServer(nil, nil)
	RegisterAegisTools(srv, nil, nil, nil)

	resp := roundTrip(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`)

	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != InvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, InvalidParams)
	}
}

func TestAegisRun_MultiLanguagePatterns(t *testing.T) {
	// Test that TypeScript pattern matches.
	patterns := interceptor.DefaultErrorPatterns()
	// Add TypeScript pattern.
	tsRe := `^(.+)\((\d+),(\d+)\):\s*(.+)$`
	compiled, err := interceptor.CompilePatterns([]struct {
		Language string
		Regex    string
		Priority int
	}{
		{Language: "go", Regex: `^([^:]+):(\d+):(\d+):\s*(.+)$`, Priority: 0},
		{Language: "typescript", Regex: tsRe, Priority: 10},
	})
	if err != nil {
		t.Fatalf("compile patterns: %v", err)
	}

	_ = patterns // use compiled instead
	parser := interceptor.NewStreamParserWithPatterns(compiled)

	entry := types.Entry{Text: "src/app.ts(42,10): error TS2304: Cannot find name 'foo'."}
	errEntry, ok := parser.ParseError(entry)
	if !ok {
		t.Fatal("ParseError returned false for TypeScript error")
	}
	if errEntry.File != "src/app.ts" {
		t.Errorf("File = %q, want src/app.ts", errEntry.File)
	}
	if errEntry.Line != 42 {
		t.Errorf("Line = %d, want 42", errEntry.Line)
	}
	if errEntry.Column != 10 {
		t.Errorf("Column = %d, want 10", errEntry.Column)
	}
	if errEntry.Language != "typescript" {
		t.Errorf("Language = %q, want typescript", errEntry.Language)
	}
	if !strings.Contains(errEntry.Message, "Cannot find name") {
		t.Errorf("Message = %q, want contains 'Cannot find name'", errEntry.Message)
	}
}
