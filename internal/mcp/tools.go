package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Uvean-z/aegislsp/internal/fusion"
	"github.com/Uvean-z/aegislsp/internal/interceptor"
	"github.com/Uvean-z/aegislsp/internal/sandbox"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// RunResult is the structured JSON output of the aegis_run tool.
type RunResult struct {
	ExitCode int                 `json:"exitCode"`
	Stdout   string              `json:"stdout,omitempty"`
	Stderr   string              `json:"stderr,omitempty"`
	Errors   []EnrichedErrorJSON `json:"errors,omitempty"`
}

// EnrichedErrorJSON is the JSON representation of an enriched error for MCP output.
type EnrichedErrorJSON struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Message    string `json:"message"`
	Language   string `json:"language,omitempty"`
	Count      int    `json:"count"`
	Definition string `json:"definition,omitempty"`
	Hover      string `json:"hover,omitempty"`
}

// RegisterAegisTools registers the aegis_run tool on the given MCP server.
// cfg is the sandbox config (nil = use defaults).
// client is the LSP client (nil = no enrichment).
// patterns are the compiled error patterns (nil = use Go default).
func RegisterAegisTools(srv *Server, cfg *sandbox.Config, client fusion.FusionEngine, patterns []interceptor.ErrorPatternDef) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Command and arguments to execute"
			},
			"work_dir": {
				"type": "string",
				"description": "Working directory (optional, defaults to current)"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (optional, defaults to 300)"
			},
			"use_sandbox": {
				"type": "boolean",
				"description": "Whether to use Docker sandbox (optional, defaults to config)"
			}
		},
		"required": ["command"]
	}`)

	srv.RegisterTool(ToolDef{
		Name:        "aegis_run",
		Description: "Execute a command in the AegisLSP pipeline: intercept stderr, parse multi-language compiler/linter errors, and optionally enrich them with LSP context (definitions, hover info). Returns structured error data instead of raw terminal text.",
		InputSchema: schema,
	}, makeRunHandler(cfg, client, patterns))
}

// makeRunHandler creates the tool handler for aegis_run.
func makeRunHandler(cfg *sandbox.Config, engine fusion.FusionEngine, patterns []interceptor.ErrorPatternDef) ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		var params struct {
			Command    []string `json:"command"`
			WorkDir    string   `json:"work_dir"`
			Timeout    int      `json:"timeout"`
			UseSandbox *bool    `json:"use_sandbox"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if len(params.Command) == 0 {
			return nil, fmt.Errorf("command must not be empty")
		}

		timeout := 300
		if params.Timeout > 0 {
			timeout = params.Timeout
		}

		useSandbox := cfg != nil && cfg.Sandbox.Enabled && cfg.Sandbox.Type != "none"
		if params.UseSandbox != nil {
			useSandbox = *params.UseSandbox
		}

		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		var exitCode int
		var stdoutBuf, stderrBuf bytes.Buffer

		if useSandbox && cfg != nil {
			code, err := runInSandbox(ctx, cfg, params.Command, params.WorkDir, &stderrBuf)
			exitCode = code
			if err != nil && exitCode == 0 {
				exitCode = 1
			}
		} else {
			code, err := runDirect(ctx, params.Command, params.WorkDir, &stdoutBuf, &stderrBuf)
			exitCode = code
			if err != nil && exitCode == 0 {
				exitCode = 1
			}
		}

		// Parse errors from stderr using configured patterns.
		errorEntries := parseErrors(stderrBuf.String(), patterns)

		// Enrich errors with LSP context if engine available.
		var enriched []EnrichedErrorJSON
		if len(errorEntries) > 0 && engine != nil {
			batchResults, err := engine.ProcessBatch(ctx, errorEntries)
			if err == nil {
				for _, e := range batchResults {
					enriched = append(enriched, toEnrichedJSON(e))
				}
			}
		}
		if enriched == nil && len(errorEntries) > 0 {
			for _, e := range errorEntries {
				enriched = append(enriched, toEnrichedJSON(types.EnrichedError{ErrorEntry: e}))
			}
		}

		result := RunResult{
			ExitCode: exitCode,
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			Errors:   enriched,
		}

		resultJSON, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal result: %w", err)
		}

		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: string(resultJSON)}},
			IsError: exitCode != 0,
		}, nil
	}
}

// runDirect executes a command directly (no sandbox) and captures stdout/stderr.
func runDirect(ctx context.Context, command []string, workDir string, stdout, stderr *bytes.Buffer) (int, error) {
	var cmd *exec.Cmd
	if len(command) == 1 {
		cmd = exec.CommandContext(ctx, command[0])
	} else {
		cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), err
	}
	return -1, err
}

// runInSandbox executes a command in the Docker sandbox.
func runInSandbox(ctx context.Context, cfg *sandbox.Config, command []string, workDir string, stderr *bytes.Buffer) (int, error) {
	launcher, err := sandbox.NewSandboxLauncher(&cfg.Sandbox)
	if err != nil {
		return -1, fmt.Errorf("create sandbox launcher: %w", err)
	}
	defer launcher.Close()

	runCfg := sandbox.RunConfig{
		Command: command,
		WorkDir: workDir,
	}

	return launcher.Run(ctx, runCfg, stderr)
}

// parseErrors parses stderr text into ErrorEntries using the configured patterns.
func parseErrors(stderr string, patterns []interceptor.ErrorPatternDef) []types.ErrorEntry {
	parser := interceptor.NewStreamParserWithPatterns(patterns)
	lines := strings.Split(stderr, "\n")

	var entries []types.ErrorEntry
	for i, line := range lines {
		if line == "" {
			continue
		}
		entry := types.Entry{
			Source:  types.SourceStderr,
			LineNum: i + 1,
			Text:    line,
		}
		if errEntry, ok := parser.ParseError(entry); ok {
			entries = append(entries, errEntry)
		}
	}
	return entries
}

// toEnrichedJSON converts an EnrichedError to the JSON-friendly struct.
func toEnrichedJSON(e types.EnrichedError) EnrichedErrorJSON {
	out := EnrichedErrorJSON{
		File:     e.File,
		Line:     e.Line,
		Column:   e.Column,
		Message:  e.Message,
		Language: e.Language,
		Count:    e.Count,
	}
	if out.Count == 0 {
		out.Count = 1
	}
	if e.Definition != nil {
		out.Definition = fmt.Sprintf("%s:%d:%d",
			strings.TrimPrefix(e.Definition.URI, "file:///"),
			e.Definition.Range.Start.Line+1,
			e.Definition.Range.Start.Character+1)
	}
	if e.Diagnostic != nil && e.Diagnostic.Message != "" {
		out.Hover = e.Diagnostic.Message
	}
	return out
}
