package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Uvean-z/aegislsp/internal/cache"
	"github.com/Uvean-z/aegislsp/internal/fusion"
	"github.com/Uvean-z/aegislsp/internal/interceptor"
	"github.com/Uvean-z/aegislsp/internal/lspclient"
	mcpsrv "github.com/Uvean-z/aegislsp/internal/mcp"
	"github.com/Uvean-z/aegislsp/internal/sandbox"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// ANSI color codes for rich terminal output.
const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorWhite   = "\033[37m"
	colorBold    = "\033[1m"
	colorDim     = "\033[2m"
	bgRed        = "\033[41m"
	bgBlue       = "\033[44m"
)

var version = "dev"

func main() {
	// Handle panic recovery.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "%s%sAegisLSP panic: %v%s\n", colorRed, colorBold, r, colorReset)
			os.Exit(1)
		}
	}()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		handleRun(os.Args[2:])
	case "mcp":
		handleMCP(os.Args[2:])
	case "version":
		fmt.Printf("AegisLSP %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "%sUnknown command: %s%s\n", colorRed, os.Args[1], colorReset)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`%s%sAegisLSP%s — Semantic Terminal Interception Middleware

%sUSAGE:%s
  aegis run [--config <path>] [--sandbox-image <image>] [--no-sandbox] [--lsp <path>] -- <command> [args...]
  aegis mcp [--config <path>] [--cache <path>]
  aegis version
  aegis help

%sCOMMANDS:%s
  run        Execute a command in the AegisLSP pipeline
  mcp        Start as an MCP server over stdio (for LLM tool integration)

%sFLAGS:%s
  --config <path>        Path to TOML configuration file
  --sandbox-image <img>  Docker image for sandbox (default: golang:1.24-alpine)
  --no-sandbox           Disable sandboxing (run command directly)
  --lsp <path>           Path to LSP server binary (default: gopls)
  --timeout <seconds>    Command timeout (default: 300)
  --cache <path>         Path to semantic cache file (skips gopls cold-start on cache hit)

%sEXAMPLES:%s
  aegis run -- go build ./...
  aegis run --config aegis.toml -- go test ./...
  aegis run --cache .aegis-cache.gob -- go build ./...
  aegis run --no-sandbox --lsp /usr/bin/gopls -- go vet ./...
`, colorBold, colorCyan, colorReset,
		colorBold, colorReset,
		colorBold, colorReset,
		colorBold, colorReset,
		colorBold, colorReset)
}

func handleRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to TOML configuration file")
	sandboxImage := fs.String("sandbox-image", "", "Docker image for sandbox")
	noSandbox := fs.Bool("no-sandbox", false, "Disable sandboxing")
	lspPath := fs.String("lsp", "gopls", "Path to LSP server binary")
	timeout := fs.Int("timeout", 300, "Command timeout in seconds")
	cachePath := fs.String("cache", "", "Path to semantic cache file (enables persistent LSP result caching)")

	// Parse flags; everything after "--" is the command.
	var command []string
	for i, arg := range args {
		if arg == "--" {
			command = args[i+1:]
			break
		}
		if !strings.HasPrefix(arg, "-") {
			// Not a flag and no "--" separator — treat rest as command.
			command = args[i:]
			break
		}
	}

	// Parse flags from the args before "--".
	flagArgs := args
	for i, arg := range args {
		if arg == "--" {
			flagArgs = args[:i]
			break
		}
	}
	fs.Parse(flagArgs)

	if len(command) == 0 {
		fmt.Fprintf(os.Stderr, "%sError: no command specified. Use 'aegis run -- <command>'.%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	// Load configuration.
	cfg, err := sandbox.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError loading config: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// Apply CLI overrides.
	if *noSandbox {
		cfg.Sandbox.Enabled = false
		cfg.Sandbox.Type = "none"
	}
	if *sandboxImage != "" {
		cfg.Sandbox.Image = *sandboxImage
	}
	if *timeout > 0 {
		cfg.Sandbox.Timeout = *timeout
	}

	// Set up context with signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\n%sReceived interrupt, shutting down...%s\n", colorYellow, colorReset)
		cancel()
	}()

	if err := runPipeline(ctx, cfg, *lspPath, command, *cachePath); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s%s%s%s\n", colorRed, colorBold, err, colorReset)
		os.Exit(1)
	}
}

// handleMCP starts AegisLSP as an MCP server over stdio.
func handleMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to TOML configuration file")
	cachePath := fs.String("cache", "", "Path to semantic cache file (enables persistent LSP result caching)")
	fs.Parse(args)

	cfg, err := sandbox.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError loading config: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// Compile error patterns from config.
	patternDefs := make([]struct {
		Language string
		Regex    string
		Priority int
	}, len(cfg.ErrorPatterns))
	for i, ep := range cfg.ErrorPatterns {
		patternDefs[i] = struct {
			Language string
			Regex    string
			Priority int
		}{Language: ep.Language, Regex: ep.Regex, Priority: ep.Priority}
	}
	compiledPatterns, err := interceptor.CompilePatterns(patternDefs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError compiling patterns: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// Create fusion engine — with semantic cache if --cache is set.
	var engine fusion.FusionEngine
	if *cachePath != "" {
		nullClient := lspclient.NewNullLSPClient()
		cachedClient, err := cache.NewCachedLSPClient(nullClient, *cachePath)
		if err != nil {
			engine = fusion.NewFusionEngine(nil)
		} else {
			engine = fusion.NewFusionEngineWithConfig(cachedClient, &cfg.Dedup)
			defer func() { _ = cachedClient.Save() }()
		}
	} else {
		engine = fusion.NewFusionEngine(nil)
	}

	// Create and start MCP server over stdio.
	srv := mcpsrv.NewServer(os.Stdin, os.Stdout)
	mcpsrv.RegisterAegisTools(srv, cfg, engine, compiledPatterns)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%sMCP server error: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}
}

// runPipeline orchestrates the full AegisLSP pipeline:
// 1. Start LSP server (gopls) in the background
// 2. Initialize LSP client
// 3. Create interceptor and fusion engine
// 4. Run user command in sandbox
// 5. If errors detected, enrich and display them
func runPipeline(ctx context.Context, cfg *sandbox.Config, lspPath string, command []string, cachePath string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// --- Phase 1: Start LSP server ---
	printPhase("Phase 1", "Starting LSP server", lspPath)

	lspStdinR, lspStdinW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	lspStdoutR, lspStdoutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	lspCmd := exec.CommandContext(ctx, lspPath)
	lspCmd.Stdin = lspStdinR
	lspCmd.Stdout = lspStdoutW
	lspCmd.Stderr = io.Discard

	if err := lspCmd.Start(); err != nil {
		return fmt.Errorf("start LSP server %s: %w", lspPath, err)
	}

	// Close the child-side ends in the parent process.
	lspStdinR.Close()
	lspStdoutW.Close()

	// Clean up LSP process on exit.
	defer func() {
		lspStdinW.Close()
		lspStdoutR.Close()
		if lspCmd.Process != nil {
			lspCmd.Process.Kill()
			lspCmd.Wait()
		}
	}()

	// --- Phase 2: Initialize LSP client ---
	printPhase("Phase 2", "Initializing LSP client", "")

	var client lspclient.LSPClient
	client = lspclient.NewLSPClient(lspStdoutR, lspStdinW)

	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()

	rootURI := lspclient.CreateFileURI(workDir)
	folders := []types.WorkspaceFolder{
		{URI: rootURI, Name: filepath.Base(workDir)},
	}

	if err := client.Initialize(initCtx, rootURI, folders); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning: LSP initialization failed: %v%s\n", colorYellow, err, colorReset)
		fmt.Fprintf(os.Stderr, "%sContinuing without LSP enrichment...%s\n", colorDim, colorReset)
		client = nil
	} else {
		printSuccess("LSP server initialized")
	}

	// Wrap LSP client with persistent semantic cache if --cache is set.
	if cachePath != "" && client != nil {
		cachedClient, err := cache.NewCachedLSPClient(client, cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sWarning: cache init failed: %v%s\n", colorYellow, err, colorReset)
		} else {
			client = cachedClient
			printSuccess(fmt.Sprintf("Semantic cache active (%s)", cachePath))
			// Save cache on exit.
			defer func() {
				if err := cachedClient.Save(); err != nil {
					fmt.Fprintf(os.Stderr, "%sWarning: cache save failed: %v%s\n", colorYellow, err, colorReset)
				}
			}()
		}
	}

	// --- Phase 3: Create interceptor and fusion engine ---
	printPhase("Phase 3", "Assembling pipeline components", "")

	// Compile error patterns from config.
	patternDefs := make([]struct {
		Language string
		Regex    string
		Priority int
	}, len(cfg.ErrorPatterns))
	for i, ep := range cfg.ErrorPatterns {
		patternDefs[i] = struct {
			Language string
			Regex    string
			Priority int
		}{Language: ep.Language, Regex: ep.Regex, Priority: ep.Priority}
	}
	compiledPatterns, err := interceptor.CompilePatterns(patternDefs)
	if err != nil {
		return fmt.Errorf("compile error patterns: %w", err)
	}
	parser := interceptor.NewStreamParserWithPatterns(compiledPatterns)
	lineEmitter := newStdLineEmitter()
	errEmitter := newStdErrorEmitter()
	spawner := interceptor.NewProcessSpawner()
	inter := interceptor.New(spawner, parser, lineEmitter, errEmitter)

	engine := fusion.NewFusionEngineWithConfig(client, &cfg.Dedup)
	printSuccess("Pipeline assembled")

	// --- Phase 3.5: Approval gate ---
	sandbox.SetStderr(os.Stderr)
	var promptFn sandbox.PromptFunc
	if isInteractive() {
		promptFn = sandbox.NewConsolePrompt(os.Stdin)
	} else {
		fmt.Fprintf(os.Stderr, "  %s⚠ Non-interactive environment detected; high-risk operations will be auto-denied%s\n", colorYellow, colorReset)
	}
	gate := sandbox.NewApprovalGate(&cfg.Approvals, promptFn)
	result := gate.Check(command)
	if !result.Allowed {
		fmt.Fprintf(os.Stderr, "\n%s%s操作被拒绝: %s%s\n", colorRed, colorBold, result.Reason, colorReset)
		return fmt.Errorf("command denied by approval gate: %s", result.Reason)
	}
	if result.Decision == sandbox.DecisionApproveAll {
		fmt.Fprintf(os.Stderr, "  %s✓ %s%s\n", colorGreen, result.Reason, colorReset)
	} else if result.Decision == sandbox.DecisionApprove {
		fmt.Fprintf(os.Stderr, "  %s✓ 已批准执行%s\n", colorGreen, colorReset)
	}

	// --- Phase 4: Run user command ---
	printPhase("Phase 4", "Executing command", strings.Join(command, " "))

	var exitCode int
	var stderrBuf bytes.Buffer

	if cfg.Sandbox.Enabled && cfg.Sandbox.Type != "none" {
		// Run in Docker sandbox.
		exitCode, err = runInSandbox(ctx, cfg, command, workDir, &stderrBuf)
	} else {
		// Run directly via interceptor.
		exitCode, err = runDirectly(ctx, inter, command, workDir, &stderrBuf)
	}

	// --- Phase 5: Process errors ---
	if exitCode != 0 || err != nil {
		fmt.Fprintf(os.Stderr, "\n")
		return processErrors(ctx, engine, inter, &stderrBuf, exitCode, compiledPatterns)
	}

	printSuccess("Command completed successfully")
	return nil
}

// runInSandbox executes the command inside a Docker container.
func runInSandbox(ctx context.Context, cfg *sandbox.Config, command []string, workDir string, output io.Writer) (int, error) {
	launcher, err := sandbox.NewSandboxLauncher(&cfg.Sandbox)
	if err != nil {
		return -1, fmt.Errorf("create sandbox launcher: %w", err)
	}
	defer launcher.Close()

	runCfg := sandbox.RunConfig{
		Command: command,
		WorkDir: workDir,
	}

	timeout := time.Duration(cfg.Sandbox.Timeout) * time.Second
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	return launcher.Run(ctx, runCfg, output)
}

// runDirectly executes the command using the interceptor (no sandbox).
func runDirectly(ctx context.Context, inter interceptor.Interceptor, command []string, workDir string, stderrBuf *bytes.Buffer) (int, error) {
	config := interceptor.ProcessConfig{
		Name: command[0],
		Args: command[1:],
		Dir:  workDir,
	}

	if err := inter.Start(ctx, config); err != nil {
		return -1, fmt.Errorf("start command: %w", err)
	}

	// Drain entries in background.
	var allEntries []types.Entry
	var allErrors []types.ErrorEntry
	drainDone := make(chan struct{})
	go func() {
		for e := range inter.Entries() {
			allEntries = append(allEntries, e)
			// Forward stdout to terminal.
			if e.Source == types.SourceStdout {
				fmt.Println(e.Text)
			}
		}
		for e := range inter.Errors() {
			allErrors = append(allErrors, e)
		}
		close(drainDone)
	}()

	// Capture stderr for error processing.
	go func() {
		for e := range inter.Entries() {
			if e.Source == types.SourceStderr {
				stderrBuf.WriteString(e.Text)
				stderrBuf.WriteByte('\n')
			}
		}
	}()

	waitErr := inter.Wait()
	<-drainDone

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), waitErr
		}
		return -1, waitErr
	}

	// Also check errors channel for compiler errors.
	if len(allErrors) > 0 {
		return 1, fmt.Errorf("compiler errors detected")
	}

	return 0, nil
}

// processErrors enriches and displays errors in a beautiful format.
func processErrors(ctx context.Context, engine fusion.FusionEngine, inter interceptor.Interceptor, stderrBuf *bytes.Buffer, exitCode int, patterns []interceptor.ErrorPatternDef) error {
	// Parse stderr for compiler errors.
	parser := interceptor.NewStreamParserWithPatterns(patterns)
	lines := strings.Split(stderrBuf.String(), "\n")

	var errorEntries []types.ErrorEntry
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
			errorEntries = append(errorEntries, errEntry)
		}
	}

	if len(errorEntries) == 0 {
		// No structured compiler errors — just show the raw stderr.
		fmt.Fprintf(os.Stderr, "%s%sCommand failed with exit code %d%s\n", colorRed, colorBold, exitCode, colorReset)
		if stderrBuf.Len() > 0 {
			fmt.Fprintf(os.Stderr, "\n%s%s%s\n", colorDim, stderrBuf.String(), colorReset)
		}
		return fmt.Errorf("command exited with code %d", exitCode)
	}

	// Enrich errors with LSP context.
	printPhase("Phase 5", "Enriching errors with LSP context", fmt.Sprintf("%d error(s) found", len(errorEntries)))

	enrichedErrors, err := engine.ProcessBatch(ctx, errorEntries)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning: enrichment failed: %v%s\n", colorYellow, err, colorReset)
		enrichedErrors = make([]types.EnrichedError, len(errorEntries))
		for i, e := range errorEntries {
			enrichedErrors[i] = types.EnrichedError{ErrorEntry: e}
		}
	}

	// Display enriched errors.
	displayEnrichedErrors(enrichedErrors)

	return fmt.Errorf("command exited with code %d (%d error(s))", exitCode, len(errorEntries))
}

// displayEnrichedErrors renders enriched errors in a beautiful terminal format.
func displayEnrichedErrors(errors []types.EnrichedError) {
	width := getTerminalWidth()
	separator := strings.Repeat("─", width)

	for i, e := range errors {
		if i > 0 {
			fmt.Fprintf(os.Stderr, "\n")
		}

		// ┌─ Error Header ─────────────────────────────────────┐
		fmt.Fprintf(os.Stderr, "%s%s┌─ Error %d/%d ─%s\n", colorRed, colorBold, i+1, len(errors), colorReset)

		// Compiler error message.
		foldSuffix := ""
		if e.Count > 1 {
			foldSuffix = fmt.Sprintf(" %s(×%d similar errors folded)%s", colorCyan, e.Count, colorReset)
		}
		fmt.Fprintf(os.Stderr, "%s│%s %s%s%s%s\n", colorRed, colorReset, colorBold, e.Message, foldSuffix, colorReset)
		fmt.Fprintf(os.Stderr, "%s│%s %sat %s:%d:%d%s\n", colorRed, colorReset, colorDim, e.File, e.Line, e.Column, colorReset)

		// ┌─ Hover / Symbol Signature ─────────────────────────┐
		if e.Diagnostic != nil && e.Diagnostic.Message != "" {
			fmt.Fprintf(os.Stderr, "%s│%s\n", colorRed, colorReset)
			fmt.Fprintf(os.Stderr, "%s├─ %sSymbol Info%s\n", colorCyan, colorBold, colorReset)

			// Word-wrap the hover content.
			lines := strings.Split(e.Diagnostic.Message, "\n")
			for _, line := range lines {
				if line = strings.TrimSpace(line); line != "" {
					fmt.Fprintf(os.Stderr, "%s│%s %s%s%s\n", colorCyan, colorReset, colorWhite, line, colorReset)
				}
			}
		}

		// ┌─ Definition Location ──────────────────────────────┐
		if e.Definition != nil {
			fmt.Fprintf(os.Stderr, "%s│%s\n", colorRed, colorReset)
			fmt.Fprintf(os.Stderr, "%s├─ %sDefinition%s\n", colorGreen, colorBold, colorReset)

			defFile := strings.TrimPrefix(e.Definition.URI, "file:///")
			defLine := e.Definition.Range.Start.Line + 1
			defCol := e.Definition.Range.Start.Character + 1
			fmt.Fprintf(os.Stderr, "%s│%s %s→ %s:%d:%d%s\n", colorGreen, colorReset, colorGreen, defFile, defLine, defCol, colorReset)
		}

		// ┌─ AI Fix Suggestion ────────────────────────────────┐
		fmt.Fprintf(os.Stderr, "%s│%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "%s└─ %sSuggested Fix%s\n", colorMagenta, colorBold, colorReset)
		fmt.Fprintf(os.Stderr, "  %s%s%s\n", colorYellow, generateFixHint(e), colorReset)
	}

	fmt.Fprintf(os.Stderr, "\n%s%s%s\n", colorDim, separator, colorReset)
}

// generateFixHint produces a contextual fix suggestion based on the error.
func generateFixHint(e types.EnrichedError) string {
	msg := strings.ToLower(e.Message)

	switch {
	case strings.Contains(msg, "undefined"):
		symbol := extractUndefinedSymbol(e.Message)
		if e.Definition != nil {
			return fmt.Sprintf("The symbol '%s' is not defined in this scope. "+
				"It is defined at %s — did you forget an import?", symbol, e.Definition.URI)
		}
		return fmt.Sprintf("The symbol '%s' is not defined. Check for typos or missing imports.", symbol)

	case strings.Contains(msg, "cannot use"):
		return "Type mismatch detected. Verify the expected type with 'go doc' or LSP hover and convert accordingly."

	case strings.Contains(msg, "not enough arguments"):
		return "Function call has too few arguments. Use LSP signature help (Ctrl+Shift+Space) to see the expected parameters."

	case strings.Contains(msg, "too many arguments"):
		return "Function call has too many arguments. Check the function signature."

	case strings.Contains(msg, "syntax error"):
		return "Syntax error detected. Check for missing braces, parentheses, or semicolons near the indicated position."

	case strings.Contains(msg, "imported and not used"):
		return "Remove the unused import or use the imported package in your code."

	case strings.Contains(msg, "declared but not used"):
		return "Remove the unused variable or use it (e.g., assign to _ if intentionally unused)."

	default:
		return "Review the error location and surrounding code. Use 'go doc' or LSP hover for more context."
	}
}

// extractUndefinedSymbol parses the symbol name from an "undefined: X" error.
func extractUndefinedSymbol(msg string) string {
	// Pattern: "undefined: symbolName"
	idx := strings.Index(msg, "undefined:")
	if idx < 0 {
		return msg
	}
	rest := strings.TrimSpace(msg[idx+len("undefined:"):])
	// Take until the next space or end of string.
	if spaceIdx := strings.Index(rest, " "); spaceIdx >= 0 {
		return rest[:spaceIdx]
	}
	return rest
}

// getTerminalWidth returns the terminal width, defaulting to 80.
func getTerminalWidth() int {
	// Try to get terminal size; default to 80 if not available.
	return 80
}

// isInteractive returns true if stdin is connected to a terminal (TTY).
// In CI/CD pipelines or when stdin is piped, it returns false.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// printPhase prints a phase header.
func printPhase(phase, description, detail string) {
	if detail != "" {
		fmt.Fprintf(os.Stderr, "\n%s%s[%s]%s %s — %s%s%s\n",
			colorBold, colorBlue, phase, colorReset,
			description, colorDim, detail, colorReset)
	} else {
		fmt.Fprintf(os.Stderr, "\n%s%s[%s]%s %s\n",
			colorBold, colorBlue, phase, colorReset, description)
	}
}

// printSuccess prints a success message.
func printSuccess(msg string) {
	fmt.Fprintf(os.Stderr, "  %s✓ %s%s\n", colorGreen, msg, colorReset)
}

// ---------------------------------------------------------------------------
// Emitter adapters for the interceptor
// ---------------------------------------------------------------------------

// stdoutLineEmitter forwards entries to stdout.
type stdoutLineEmitter struct {
	ch   chan types.Entry
	once sync.Once
}

func newStdLineEmitter() *stdoutLineEmitter {
	return &stdoutLineEmitter{ch: make(chan types.Entry, 256)}
}

func (e *stdoutLineEmitter) Emit(entry types.Entry) {
	select {
	case e.ch <- entry:
	default:
		// Drop if buffer full.
	}
}

func (e *stdoutLineEmitter) Output() <-chan types.Entry { return e.ch }

func (e *stdoutLineEmitter) Close() {
	e.once.Do(func() { close(e.ch) })
}

// stderrErrorEmitter collects ErrorEntry values.
type stderrErrorEmitter struct {
	ch   chan types.ErrorEntry
	once sync.Once
}

func newStdErrorEmitter() *stderrErrorEmitter {
	return &stderrErrorEmitter{ch: make(chan types.ErrorEntry, 256)}
}

func (e *stderrErrorEmitter) Emit(entry types.ErrorEntry) {
	select {
	case e.ch <- entry:
	default:
		// Drop if buffer full.
	}
}

func (e *stderrErrorEmitter) Output() <-chan types.ErrorEntry { return e.ch }

func (e *stderrErrorEmitter) Close() {
	e.once.Do(func() { close(e.ch) })
}
