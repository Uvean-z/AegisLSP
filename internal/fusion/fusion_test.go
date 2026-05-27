package fusion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uvean-z/aegislsp/internal/lspclient"
	"github.com/Uvean-z/aegislsp/internal/sandbox"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// ---------------------------------------------------------------------------
// Mock LSPClient
// ---------------------------------------------------------------------------

type mockLSPClient struct {
	definitionFn func(ctx context.Context, uri string, pos types.Position) ([]types.Location, error)
	hoverFn      func(ctx context.Context, uri string, pos types.Position) (json.RawMessage, error)
}

func (m *mockLSPClient) Initialize(ctx context.Context, rootURI string, workspaceFolders []types.WorkspaceFolder) error {
	return nil
}
func (m *mockLSPClient) Shutdown(ctx context.Context) error { return nil }
func (m *mockLSPClient) OpenDocument(ctx context.Context, uri, lang, content string) error {
	return nil
}
func (m *mockLSPClient) CloseDocument(ctx context.Context, uri string) error           { return nil }
func (m *mockLSPClient) ChangeDocument(ctx context.Context, uri, content string) error { return nil }
func (m *mockLSPClient) TypeDefinition(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return nil, nil
}
func (m *mockLSPClient) Implementation(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return nil, nil
}
func (m *mockLSPClient) References(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return nil, nil
}
func (m *mockLSPClient) DocumentSymbols(ctx context.Context, uri string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockLSPClient) WorkspaceSymbols(ctx context.Context, query string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockLSPClient) Diagnostics(ctx context.Context, uri string) ([]types.Diagnostic, error) {
	return nil, nil
}
func (m *mockLSPClient) OnNotification(method string, handler lspclient.NotificationHandler) {}
func (m *mockLSPClient) IsInitialized() bool                                                 { return true }

func (m *mockLSPClient) Definition(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	if m.definitionFn != nil {
		return m.definitionFn(ctx, uri, pos)
	}
	return nil, nil
}

func (m *mockLSPClient) Hover(ctx context.Context, uri string, pos types.Position) (json.RawMessage, error) {
	if m.hoverFn != nil {
		return m.hoverFn(ctx, uri, pos)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// ErrorCorrelator tests
// ---------------------------------------------------------------------------

func TestErrorCorrelator_EmptyDiagnostics(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "err"}
	diags, err := corr.Correlate(context.Background(), entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected 0, got %d", len(diags))
	}
}

func TestErrorCorrelator_ExactMatch(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "undefined: foo"}

	diags := []types.Diagnostic{
		{
			Range:    types.Range{Start: types.Position{Line: 9, Character: 4}, End: types.Position{Line: 9, Character: 7}},
			Severity: types.SeverityError,
			Message:  "undefined: foo",
		},
	}

	matched, err := corr.Correlate(context.Background(), entry, diags)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Message != "undefined: foo" {
		t.Errorf("message = %q", matched[0].Message)
	}
}

func TestErrorCorrelator_LineMismatch(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "err"}

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 20, Character: 0}, End: types.Position{Line: 20, Character: 10}}},
	}

	matched, _ := corr.Correlate(context.Background(), entry, diags)
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matched))
	}
}

func TestErrorCorrelator_ColumnBeforeStart(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 2, Message: "err"}

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 9, Character: 5}, End: types.Position{Line: 9, Character: 10}}},
	}

	matched, _ := corr.Correlate(context.Background(), entry, diags)
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matched))
	}
}

func TestErrorCorrelator_ColumnAfterEnd(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 15, Message: "err"}

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 9, Character: 5}, End: types.Position{Line: 9, Character: 10}}},
	}

	matched, _ := corr.Correlate(context.Background(), entry, diags)
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matched))
	}
}

func TestErrorCorrelator_MultiLineRange(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 12, Column: 3, Message: "err"}

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 10, Character: 0}, End: types.Position{Line: 15, Character: 0}}},
	}

	matched, _ := corr.Correlate(context.Background(), entry, diags)
	if len(matched) != 1 {
		t.Errorf("expected 1 match, got %d", len(matched))
	}
}

func TestErrorCorrelator_MultipleMatches(t *testing.T) {
	corr := NewErrorCorrelator()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "err"}

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 9, Character: 0}, End: types.Position{Line: 9, Character: 20}}, Message: "d1"},
		{Range: types.Range{Start: types.Position{Line: 9, Character: 3}, End: types.Position{Line: 9, Character: 8}}, Message: "d2"},
		{Range: types.Range{Start: types.Position{Line: 20, Character: 0}, End: types.Position{Line: 20, Character: 5}}, Message: "d3"},
	}

	matched, _ := corr.Correlate(context.Background(), entry, diags)
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matched))
	}
}

// ---------------------------------------------------------------------------
// ContextEnricher tests
// ---------------------------------------------------------------------------

func TestContextEnricher_NilClient(t *testing.T) {
	enricher := NewContextEnricher()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "err"}

	enriched, err := enricher.Enrich(context.Background(), nil, entry)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.Definition != nil {
		t.Error("expected nil definition")
	}
	if enriched.Diagnostic != nil {
		t.Error("expected nil diagnostic")
	}
}

func TestContextEnricher_DefinitionAndHover(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, uri string, pos types.Position) ([]types.Location, error) {
			if uri == "" {
				t.Error("expected non-empty URI")
			}
			return []types.Location{
				{URI: "file:///other.go", Range: types.Range{Start: types.Position{Line: 2, Character: 0}}},
			}, nil
		},
		hoverFn: func(_ context.Context, uri string, pos types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"func foo() int"}}`), nil
		},
	}

	enricher := NewContextEnricher()
	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "undefined: foo"}

	enriched, err := enricher.Enrich(context.Background(), client, entry)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.Definition == nil {
		t.Fatal("expected definition")
	}
	if enriched.Definition.URI != "file:///other.go" {
		t.Errorf("definition URI = %q", enriched.Definition.URI)
	}
	if enriched.Diagnostic == nil {
		t.Fatal("expected diagnostic from hover")
	}
	if enriched.Diagnostic.Message != "func foo() int" {
		t.Errorf("hover message = %q", enriched.Diagnostic.Message)
	}
}

func TestContextEnricher_DefinitionError(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			return nil, errors.New("not found")
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"some docs"}}`), nil
		},
	}

	enricher := NewContextEnricher()
	entry := types.ErrorEntry{File: "main.go", Line: 5, Column: 1, Message: "err"}

	enriched, err := enricher.Enrich(context.Background(), client, entry)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.Definition != nil {
		t.Error("expected nil definition on error")
	}
	if enriched.Diagnostic == nil {
		t.Fatal("expected hover diagnostic even when definition fails")
	}
}

func TestContextEnricher_HoverError(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			return []types.Location{{URI: "file:///def.go"}}, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return nil, errors.New("hover failed")
		},
	}

	enricher := NewContextEnricher()
	entry := types.ErrorEntry{File: "main.go", Line: 5, Column: 1, Message: "err"}

	enriched, err := enricher.Enrich(context.Background(), client, entry)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.Definition == nil {
		t.Fatal("expected definition even when hover fails")
	}
	if enriched.Diagnostic != nil {
		t.Error("expected nil diagnostic on hover error")
	}
}

func TestContextEnricher_ContextCancellation(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(ctx context.Context, _ string, _ types.Position) ([]types.Location, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		hoverFn: func(ctx context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	enricher := NewContextEnricher()
	entry := types.ErrorEntry{File: "main.go", Line: 5, Column: 1, Message: "err"}

	enriched, err := enricher.Enrich(ctx, client, entry)
	if err != nil {
		t.Fatal(err)
	}
	// Should return gracefully with zero-value enrichment.
	if enriched.ErrorEntry.Message != "err" {
		t.Error("expected original error preserved")
	}
}

// ---------------------------------------------------------------------------
// FusionEngine tests
// ---------------------------------------------------------------------------

func TestFusionEngine_Process_WithCorrelatedDiagnosticAndHover(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			return []types.Location{{URI: "file:///foo.go", Range: types.Range{Start: types.Position{Line: 0, Character: 0}}}}, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"type Foo struct { x int }"}}`), nil
		},
	}

	engine := NewFusionEngine(client)

	// Pre-populate diagnostics cache using the same URI generator as the engine.
	uri := lspclient.CreateFileURI("main.go")
	engine.(*fusionEngineImpl).StoreDiagnostics(uri, []types.Diagnostic{
		{
			Range:    types.Range{Start: types.Position{Line: 9, Character: 4}, End: types.Position{Line: 9, Character: 7}},
			Severity: types.SeverityError,
			Source:   "compiler",
			Message:  "undefined: foo",
		},
	})

	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "undefined: foo"}
	ctx := context.Background()

	enriched, err := engine.Process(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}

	// Should have correlated diagnostic.
	if enriched.Diagnostic == nil {
		t.Fatal("expected diagnostic")
	}
	if enriched.Diagnostic.Source != "compiler" {
		t.Errorf("source = %q, want compiler", enriched.Diagnostic.Source)
	}
	// Hover docs should be merged into message.
	if enriched.Diagnostic.Message == "" {
		t.Error("expected non-empty diagnostic message")
	}

	// Should have definition.
	if enriched.Definition == nil {
		t.Fatal("expected definition")
	}
	if enriched.Definition.URI != "file:///foo.go" {
		t.Errorf("definition URI = %q", enriched.Definition.URI)
	}
}

func TestFusionEngine_Process_NoDiagnostics_HoverOnly(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			return nil, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"func bar() string"}}`), nil
		},
	}

	engine := NewFusionEngine(client)

	entry := types.ErrorEntry{File: "main.go", Line: 5, Column: 1, Message: "syntax error"}
	enriched, err := engine.Process(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}

	// No correlated diagnostic, but hover should fill Diagnostic.
	if enriched.Diagnostic == nil {
		t.Fatal("expected hover-derived diagnostic")
	}
	if enriched.Diagnostic.Source != "hover" {
		t.Errorf("source = %q, want hover", enriched.Diagnostic.Source)
	}
	if enriched.Diagnostic.Message != "func bar() string" {
		t.Errorf("message = %q", enriched.Diagnostic.Message)
	}
}

func TestFusionEngine_Process_NilClient(t *testing.T) {
	engine := NewFusionEngine(nil)

	entry := types.ErrorEntry{File: "main.go", Line: 10, Column: 5, Message: "err"}
	enriched, err := engine.Process(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.ErrorEntry.Message != "err" {
		t.Error("expected original error preserved")
	}
}

func TestFusionEngine_SetClient(t *testing.T) {
	engine := NewFusionEngine(nil)

	// First call with nil client.
	entry := types.ErrorEntry{File: "main.go", Line: 5, Column: 1, Message: "err"}
	enriched, _ := engine.Process(context.Background(), entry)
	if enriched.Definition != nil {
		t.Error("expected nil definition with nil client")
	}

	// Set client and try again.
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			return []types.Location{{URI: "file:///def.go"}}, nil
		},
	}
	engine.SetClient(client)

	enriched, _ = engine.Process(context.Background(), entry)
	if enriched.Definition == nil {
		t.Error("expected definition after SetClient")
	}
}

func TestFusionEngine_ProcessBatch(t *testing.T) {
	var callCount atomic.Int32
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			callCount.Add(1)
			return []types.Location{{URI: "file:///def.go"}}, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"docs"}}`), nil
		},
	}

	engine := NewFusionEngine(client)

	errors := []types.ErrorEntry{
		{File: "a.go", Line: 1, Column: 1, Message: "err1"},
		{File: "b.go", Line: 2, Column: 3, Message: "err2"},
		{File: "c.go", Line: 5, Column: 10, Message: "err3"},
	}

	results, err := engine.ProcessBatch(context.Background(), errors)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify order preserved.
	for i, r := range results {
		if r.ErrorEntry.Message != errors[i].Message {
			t.Errorf("result[%d].Message = %q, want %q", i, r.ErrorEntry.Message, errors[i].Message)
		}
	}

	// All should have been enriched.
	if callCount.Load() != 3 {
		t.Errorf("expected 3 definition calls, got %d", callCount.Load())
	}
}

func TestFusionEngine_ProcessBatch_ConcurrentExecution(t *testing.T) {
	var maxConcurrent atomic.Int32
	var current atomic.Int32

	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			n := current.Add(1)
			// Track max concurrency.
			for {
				old := maxConcurrent.Load()
				if n <= old || maxConcurrent.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			return nil, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return nil, nil
		},
	}

	engine := NewFusionEngine(client)

	// Create 50 errors in different files to trigger concurrent processing
	// without deduplication collapsing them.
	errs := make([]types.ErrorEntry, 50)
	for i := range errs {
		errs[i] = types.ErrorEntry{File: fmt.Sprintf("f%d.go", i), Line: 1, Column: 1, Message: "err"}
	}

	results, err := engine.ProcessBatch(context.Background(), errs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 50 {
		t.Fatalf("expected 50 results, got %d", len(results))
	}

	// With 50 concurrent errors and semaphore of 16, max concurrency should be <= 16.
	if maxConcurrent.Load() > 16 {
		t.Errorf("max concurrency = %d, want <= 16", maxConcurrent.Load())
	}
}

func TestFusionEngine_ProcessBatch_PreservesOrder(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			// Variable latency to stress ordering.
			time.Sleep(time.Duration(1+time.Now().UnixNano()%5) * time.Millisecond)
			return nil, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return nil, nil
		},
	}

	engine := NewFusionEngine(client)

	// Use different files so deduplication doesn't collapse them.
	errs := make([]types.ErrorEntry, 100)
	for i := range errs {
		errs[i] = types.ErrorEntry{File: fmt.Sprintf("f%d.go", i), Line: 1, Column: 1, Message: "err"}
	}

	var wg sync.WaitGroup
	for trial := 0; trial < 10; trial++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, _ := engine.ProcessBatch(context.Background(), errs)
			if len(results) != 100 {
				t.Errorf("expected 100 results, got %d", len(results))
				return
			}
			for i, r := range results {
				if r.ErrorEntry.Message != errs[i].Message {
					t.Errorf("trial: result[%d] = %q, want %q", i, r.ErrorEntry.Message, errs[i].Message)
				}
			}
		}()
	}
	wg.Wait()
}

func TestFusionEngine_ProcessBatch_ContextCancellation(t *testing.T) {
	client := &mockLSPClient{
		definitionFn: func(ctx context.Context, _ string, _ types.Position) ([]types.Location, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		hoverFn: func(ctx context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return nil, ctx.Err()
		},
	}

	engine := NewFusionEngine(client)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	// Use errors in different files so deduplication doesn't collapse them.
	errs := make([]types.ErrorEntry, 20)
	for i := range errs {
		errs[i] = types.ErrorEntry{File: fmt.Sprintf("f%d.go", i), Line: 1, Column: 1, Message: "err"}
	}

	results, err := engine.ProcessBatch(ctx, errs)
	if err != nil {
		t.Fatal(err)
	}
	// Should still return results (with zero-value enrichment).
	if len(results) != 20 {
		t.Fatalf("expected 20 results, got %d", len(results))
	}
}

func TestFusionEngine_StoreAndGetDiagnostics(t *testing.T) {
	engine := NewFusionEngine(nil)

	diags := []types.Diagnostic{
		{Range: types.Range{Start: types.Position{Line: 0, Character: 0}, End: types.Position{Line: 0, Character: 5}}, Message: "d1"},
	}
	engine.(*fusionEngineImpl).StoreDiagnostics("file:///main.go", diags)

	got := engine.(*fusionEngineImpl).GetDiagnostics("file:///main.go")
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Message != "d1" {
		t.Errorf("message = %q", got[0].Message)
	}

	// Non-existent URI returns nil.
	got = engine.(*fusionEngineImpl).GetDiagnostics("file:///nope.go")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestFusionEngine_ConcurrentSetClient(t *testing.T) {
	engine := NewFusionEngine(nil)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			engine.SetClient(&mockLSPClient{})
		}()
	}
	wg.Wait()

	// Should not panic.
	entry := types.ErrorEntry{File: "main.go", Line: 1, Column: 1, Message: "err"}
	engine.Process(context.Background(), entry)
}

// ---------------------------------------------------------------------------
// Integration: mock interceptor → fusion engine
// ---------------------------------------------------------------------------

func TestFusionEngine_Integration_InterceptorFlow(t *testing.T) {
	// Simulate: interceptor emits an error, fusion engine enriches it.
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, uri string, pos types.Position) ([]types.Location, error) {
			if pos.Line != 9 || pos.Character != 4 {
				t.Errorf("unexpected position: line=%d col=%d", pos.Line, pos.Character)
			}
			return []types.Location{
				{URI: "file:///fmt/print.go", Range: types.Range{Start: types.Position{Line: 100, Character: 0}}},
			}, nil
		},
		hoverFn: func(_ context.Context, uri string, pos types.Position) (json.RawMessage, error) {
			return json.RawMessage(`{"contents":{"kind":"markdown","value":"func Println(a ...any) (n int, err error)\n\nPrintln formats using the default formats for its operands and writes to standard output."}}`), nil
		},
	}

	engine := NewFusionEngine(client)

	// Pre-seed diagnostics (as if LSP published them).
	uri := lspclient.CreateFileURI("main.go")
	engine.(*fusionEngineImpl).StoreDiagnostics(uri, []types.Diagnostic{
		{
			Range:    types.Range{Start: types.Position{Line: 9, Character: 4}, End: types.Position{Line: 9, Character: 11}},
			Severity: types.SeverityError,
			Source:   "compiler",
			Message:  "undefined: fmt.Println",
		},
	})

	// Simulate interceptor output.
	entry := types.ErrorEntry{
		File:    "main.go",
		Line:    10,
		Column:  5,
		Message: "undefined: fmt.Println",
	}

	enriched, err := engine.Process(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the EnrichedError is fully assembled.
	if enriched.ErrorEntry.File != "main.go" {
		t.Errorf("file = %q", enriched.ErrorEntry.File)
	}
	if enriched.Diagnostic == nil {
		t.Fatal("expected diagnostic")
	}
	if enriched.Diagnostic.Source != "compiler" {
		t.Errorf("diagnostic source = %q", enriched.Diagnostic.Source)
	}
	if enriched.Definition == nil {
		t.Fatal("expected definition")
	}
	if enriched.Definition.URI != "file:///fmt/print.go" {
		t.Errorf("definition URI = %q", enriched.Definition.URI)
	}
}

// ---------------------------------------------------------------------------
// Deduplicator tests
// ---------------------------------------------------------------------------

// goDedupConfig returns a sandbox.DedupConfig matching the old hardcoded Go
// normalization rules (4 regexes + 22 keywords).
func goDedupConfig() *sandbox.DedupConfig {
	return &sandbox.DedupConfig{
		Languages: []sandbox.DedupLangConfig{
			{
				Language: "go",
				Keywords: []string{"undefined", "cannot", "use", "as", "type", "in", "call", "to",
					"not", "enough", "many", "arguments", "missing", "return",
					"syntax", "error", "expected", "declared", "but", "unused",
					"imported", "and"},
				Rules: []sandbox.DedupNormRule{
					{Regex: `(undefined:\s*)\w+`, Replacement: `${1}VAR`},
					{Regex: `(cannot use\s+)\w+(\s+\(type\s+)\w+(\)\s+as type\s+)\w+`, Replacement: `${1}VAR${2}TYPE${3}TYPE`},
					{Regex: `(\w+)\s+(declared but not used)`, Replacement: `VAR ${2}`},
					{Regex: `(in call to\s+)\S+`, Replacement: `${1}FUNC`},
				},
			},
		},
	}
}

// tsDedupConfig returns a sandbox.DedupConfig for TypeScript dedup rules.
func tsDedupConfig() *sandbox.DedupConfig {
	return &sandbox.DedupConfig{
		Languages: []sandbox.DedupLangConfig{
			{
				Language: "typescript",
				Keywords: []string{"error", "cannot", "find", "name", "type", "is", "not",
					"assignable", "parameter", "expected", "module", "declared"},
				Rules: []sandbox.DedupNormRule{
					{Regex: `(?i)cannot find name '(\w+)'`, Replacement: "Cannot find name 'VAR'"},
					{Regex: `(?i)type '([^']+)' is not assignable to type '([^']+)'`, Replacement: "Type 'TYPE' is not assignable to type 'TYPE'"},
				},
			},
		},
	}
}

// pythonDedupConfig returns a sandbox.DedupConfig for Python dedup rules.
func pythonDedupConfig() *sandbox.DedupConfig {
	return &sandbox.DedupConfig{
		Languages: []sandbox.DedupLangConfig{
			{
				Language: "python",
				Keywords: []string{"name", "is", "not", "defined", "type", "error", "unexpected",
					"syntax", "import", "no", "module", "attribute"},
				Rules: []sandbox.DedupNormRule{
					{Regex: `(?i)name '(\w+)' is not defined`, Replacement: "name 'VAR' is not defined"},
					{Regex: `(?i)no module named '(\w+)'`, Replacement: "No module named 'MOD'"},
				},
			},
		},
	}
}

func TestDeduplicator_Empty(t *testing.T) {
	d := NewDeduplicator()
	entries, counts := d.Dedup(nil)
	if len(entries) != 0 || len(counts) != 0 {
		t.Errorf("expected empty result, got %d entries", len(entries))
	}
}

func TestDeduplicator_AllUnique(t *testing.T) {
	d := NewDeduplicator()
	errs := []types.ErrorEntry{
		{File: "a.go", Line: 1, Column: 1, Message: "undefined: foo"},
		{File: "a.go", Line: 2, Column: 1, Message: "cannot use x (type int) as type string"},
		{File: "b.go", Line: 5, Column: 3, Message: "syntax error"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	for i, c := range counts {
		if c != 1 {
			t.Errorf("counts[%d] = %d, want 1", i, c)
		}
	}
}

func TestDeduplicator_AllIdentical(t *testing.T) {
	d := NewDeduplicator()
	errs := make([]types.ErrorEntry, 10)
	for i := range errs {
		errs[i] = types.ErrorEntry{File: "main.go", Line: i + 1, Column: 1, Message: "undefined: foo"}
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 10 {
		t.Errorf("count = %d, want 10", counts[0])
	}
}

func TestDeduplicator_SamePatternDifferentNames(t *testing.T) {
	d := NewDeduplicatorWithConfig(goDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Language: "go", Message: "undefined: foo"},
		{File: "main.go", Line: 11, Column: 1, Language: "go", Message: "undefined: bar"},
		{File: "main.go", Line: 12, Column: 1, Language: "go", Message: "undefined: baz"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 3 {
		t.Errorf("count = %d, want 3", counts[0])
	}
}

func TestDeduplicator_DifferentFilesNotFolded(t *testing.T) {
	d := NewDeduplicator()
	errs := []types.ErrorEntry{
		{File: "a.go", Line: 1, Column: 1, Message: "undefined: foo"},
		{File: "b.go", Line: 1, Column: 1, Message: "undefined: foo"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
	for _, c := range counts {
		if c != 1 {
			t.Errorf("count = %d, want 1", c)
		}
	}
}

func TestDeduplicator_CannotUsePattern(t *testing.T) {
	d := NewDeduplicatorWithConfig(goDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Language: "go", Message: `cannot use x (type int) as type string`},
		{File: "main.go", Line: 11, Column: 1, Language: "go", Message: `cannot use y (type float64) as type string`},
		{File: "main.go", Line: 12, Column: 1, Language: "go", Message: `cannot use z (type bool) as type string`},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 3 {
		t.Errorf("count = %d, want 3", counts[0])
	}
}

func TestDeduplicator_InterleavedPatterns(t *testing.T) {
	d := NewDeduplicatorWithConfig(goDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Language: "go", Message: "undefined: foo"},
		{File: "main.go", Line: 11, Column: 1, Language: "go", Message: "syntax error"},
		{File: "main.go", Line: 12, Column: 1, Language: "go", Message: "undefined: bar"},
	}
	entries, counts := d.Dedup(errs)
	// "undefined: foo" and "undefined: bar" have the same normalized pattern,
	// but they are not consecutive (syntax error is between them), so all 3
	// are kept.
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	for _, c := range counts {
		if c != 1 {
			t.Errorf("count = %d, want 1", c)
		}
	}
}

func TestNormalizeMessage_WithGoConfig(t *testing.T) {
	goLang := langNormSet{
		rules: []normRule{
			{re: regexp.MustCompile(`(undefined:\s*)\w+`), replacement: `${1}VAR`},
			{re: regexp.MustCompile(`(cannot use\s+)\w+(\s+\(type\s+)\w+(\)\s+as type\s+)\w+`), replacement: `${1}VAR${2}TYPE${3}TYPE`},
			{re: regexp.MustCompile(`(\w+)\s+(declared but not used)`), replacement: `VAR ${2}`},
			{re: regexp.MustCompile(`(in call to\s+)\S+`), replacement: `${1}FUNC`},
		},
		keywords: map[string]bool{
			"undefined": true, "cannot": true, "use": true, "as": true, "type": true,
			"in": true, "call": true, "to": true, "not": true, "enough": true,
			"many": true, "arguments": true, "missing": true, "return": true,
			"syntax": true, "error": true, "expected": true, "declared": true,
			"but": true, "unused": true, "imported": true, "and": true,
		},
	}

	tests := []struct {
		input string
		want  string
	}{
		{"undefined: foo", "undefined: VAR"},
		{"undefined: Bar", "undefined: VAR"},
		{"undefined: baz123", "undefined: VAR"},
		// "cannot use" patterns: lang rule replaces identifiers, typeAnnotRe strips (type X) → ().
		{`cannot use x (type int) as type string`, `cannot use VAR () as type VAR`},
		{`cannot use y (type float64) as type string`, `cannot use VAR () as type VAR`},
		// callToRe handles "in call to X" → "in call to FUNC", identRe replaces FUNC→VAR, too→VAR.
		{`too many arguments in call to fmt.Println`, `VAR many arguments in call to VAR`},
		{`not enough arguments in call to Process`, `not enough arguments in call to VAR`},
		{`missing return statement`, `missing return VAR`},
		{`count declared but not used`, `VAR declared but not VAR`},
		{`total declared but not used`, `VAR declared but not VAR`},
	}
	for _, tt := range tests {
		got := normalizeMessage(tt.input, goLang)
		if got != tt.want {
			t.Errorf("normalizeMessage(%q)\n  got  = %q\n  want = %q", tt.input, got, tt.want)
		}
	}

	// Key invariant: different identifiers/names produce the same pattern.
	if normalizeMessage("undefined: foo", goLang) != normalizeMessage("undefined: bar", goLang) {
		t.Error("undefined errors with different names should normalize identically")
	}
	if normalizeMessage("cannot use x (type int) as type string", goLang) != normalizeMessage("cannot use y (type float64) as type string", goLang) {
		t.Error("cannot-use errors with different types should normalize identically")
	}
	if normalizeMessage("count declared but not used", goLang) != normalizeMessage("total declared but not used", goLang) {
		t.Error("declared-but-not-used errors should normalize identically")
	}
}

func TestNormalizeMessage_GenericRules(t *testing.T) {
	// With empty langNormSet, all identifiers become VAR (no keywords to preserve).
	empty := langNormSet{}

	tests := []struct {
		input string
		want  string
	}{
		{`"some string" in code`, `"" VAR VAR`},
		{`value is 42`, `VAR VAR 0`},
		{`type (int) annotation`, `VAR () VAR`},
		{`simple message`, `VAR VAR`},
	}
	for _, tt := range tests {
		got := normalizeMessage(tt.input, empty)
		if got != tt.want {
			t.Errorf("normalizeMessage(%q)\n  got  = %q\n  want = %q", tt.input, got, tt.want)
		}
	}
}

func TestDeduplicator_NoLangConfig_GenericOnly(t *testing.T) {
	// NewDeduplicator() has no language-specific rules.
	// Go "undefined: X" errors should NOT fold because generic normalization
	// keeps "undefined" as-is but replaces the identifier, producing
	// "undefined: VAR" — which IS the same pattern. However, without the
	// lang-specific `undefinedRe` rule, the colon+space handling differs.
	// Actually, generic identRe still normalizes identifiers to VAR, so
	// "undefined: foo" and "undefined: bar" both become "undefined: VAR".
	d := NewDeduplicator()
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Message: "undefined: foo"},
		{File: "main.go", Line: 11, Column: 1, Message: "undefined: bar"},
	}
	entries, counts := d.Dedup(errs)
	// Generic normalization still folds these (identRe replaces foo/bar → VAR).
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 2 {
		t.Errorf("count = %d, want 2", counts[0])
	}
}

func TestDeduplicator_WithGoConfig(t *testing.T) {
	d := NewDeduplicatorWithConfig(goDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Language: "go", Message: "undefined: foo"},
		{File: "main.go", Line: 11, Column: 1, Language: "go", Message: "undefined: bar"},
		{File: "main.go", Line: 12, Column: 1, Language: "go", Message: "undefined: baz"},
		{File: "main.go", Line: 13, Column: 1, Language: "go", Message: "syntax error"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if counts[0] != 3 {
		t.Errorf("counts[0] = %d, want 3", counts[0])
	}
	if counts[1] != 1 {
		t.Errorf("counts[1] = %d, want 1", counts[1])
	}
}

func TestDeduplicator_WithTSConfig(t *testing.T) {
	d := NewDeduplicatorWithConfig(tsDedupConfig())
	errs := []types.ErrorEntry{
		{File: "app.ts", Line: 10, Column: 1, Language: "typescript", Message: "Cannot find name 'foo'"},
		{File: "app.ts", Line: 11, Column: 1, Language: "typescript", Message: "Cannot find name 'bar'"},
		{File: "app.ts", Line: 12, Column: 1, Language: "typescript", Message: "Cannot find name 'baz'"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 3 {
		t.Errorf("count = %d, want 3", counts[0])
	}
}

func TestDeduplicator_WithTSConfig_Assignable(t *testing.T) {
	d := NewDeduplicatorWithConfig(tsDedupConfig())
	errs := []types.ErrorEntry{
		{File: "app.ts", Line: 10, Column: 1, Language: "typescript", Message: "Type 'string' is not assignable to type 'number'"},
		{File: "app.ts", Line: 11, Column: 1, Language: "typescript", Message: "Type 'boolean' is not assignable to type 'number'"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 2 {
		t.Errorf("count = %d, want 2", counts[0])
	}
}

func TestDeduplicator_WithPythonConfig(t *testing.T) {
	d := NewDeduplicatorWithConfig(pythonDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.py", Line: 10, Column: 1, Language: "python", Message: "Name 'foo' is not defined"},
		{File: "main.py", Line: 11, Column: 1, Language: "python", Message: "Name 'bar' is not defined"},
		{File: "main.py", Line: 12, Column: 1, Language: "python", Message: "Name 'baz' is not defined"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 3 {
		t.Errorf("count = %d, want 3", counts[0])
	}
}

func TestDeduplicator_WithPythonConfig_Module(t *testing.T) {
	d := NewDeduplicatorWithConfig(pythonDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.py", Line: 10, Column: 1, Language: "python", Message: "No module named 'requests'"},
		{File: "main.py", Line: 11, Column: 1, Language: "python", Message: "No module named 'flask'"},
	}
	entries, counts := d.Dedup(errs)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 2 {
		t.Errorf("count = %d, want 2", counts[0])
	}
}

func TestDeduplicator_UnknownLang_GenericFallback(t *testing.T) {
	// Config has Go rules, but errors are "rust" — should use generic normalization.
	d := NewDeduplicatorWithConfig(goDedupConfig())
	errs := []types.ErrorEntry{
		{File: "main.rs", Line: 10, Column: 1, Language: "rust", Message: "cannot find value `foo` in this scope"},
		{File: "main.rs", Line: 11, Column: 1, Language: "rust", Message: "cannot find value `bar` in this scope"},
	}
	entries, counts := d.Dedup(errs)
	// Generic normalization: both become "cannot find value VAR in this scope".
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if counts[0] != 2 {
		t.Errorf("count = %d, want 2", counts[0])
	}
}

func TestDeduplicator_MixedLanguages(t *testing.T) {
	// Config has both Go and TypeScript rules.
	cfg := &sandbox.DedupConfig{
		Languages: []sandbox.DedupLangConfig{
			goDedupConfig().Languages[0],
			tsDedupConfig().Languages[0],
		},
	}
	d := NewDeduplicatorWithConfig(cfg)
	errs := []types.ErrorEntry{
		{File: "main.go", Line: 10, Column: 1, Language: "go", Message: "undefined: foo"},
		{File: "main.go", Line: 11, Column: 1, Language: "go", Message: "undefined: bar"},
		{File: "app.ts", Line: 10, Column: 1, Language: "typescript", Message: "Cannot find name 'x'"},
		{File: "app.ts", Line: 11, Column: 1, Language: "typescript", Message: "Cannot find name 'y'"},
	}
	entries, counts := d.Dedup(errs)
	// 2 entries: one Go (count 2), one TS (count 2).
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if counts[0] != 2 {
		t.Errorf("counts[0] = %d, want 2", counts[0])
	}
	if counts[1] != 2 {
		t.Errorf("counts[1] = %d, want 2", counts[1])
	}
}

// TestDeduplicator_CascadingCrashes verifies that 50 highly similar
// compiler errors are reduced by at least 60%.
func TestDeduplicator_CascadingCrashes(t *testing.T) {
	d := NewDeduplicatorWithConfig(goDedupConfig())

	// Generate 50 errors that simulate cascading compiler failures:
	// 15 "undefined: X" errors
	// 15 "cannot use X (type T) as type string" errors
	// 10 "not enough arguments in call to X" errors
	// 10 "X declared but not used" errors
	symbols := []string{
		"foo", "bar", "baz", "qux", "quux", "corge", "grault", "garply",
		"waldo", "fred", "plugh", "xyzzy", "thud", "blep", "boop",
	}
	types_list := []string{"int", "float64", "bool", "byte", "rune"}
	funcs := []string{"Process", "Handle", "Execute", "Run", "Init", "Start", "Stop", "Reset", "Parse", "Validate"}
	vars := []string{"count", "total", "index", "offset", "limit", "size", "length", "width", "height", "depth"}

	var errs []types.ErrorEntry

	// 15 "undefined" errors
	for i := 0; i < 15; i++ {
		errs = append(errs, types.ErrorEntry{
			File:     "main.go",
			Line:     10 + i,
			Column:   1,
			Language: "go",
			Message:  fmt.Sprintf("undefined: %s", symbols[i%len(symbols)]),
		})
	}

	// 15 "cannot use" errors
	for i := 0; i < 15; i++ {
		errs = append(errs, types.ErrorEntry{
			File:     "main.go",
			Line:     30 + i,
			Column:   1,
			Language: "go",
			Message:  fmt.Sprintf("cannot use x (type %s) as type string", types_list[i%len(types_list)]),
		})
	}

	// 10 "not enough arguments" errors
	for i := 0; i < 10; i++ {
		errs = append(errs, types.ErrorEntry{
			File:     "handler.go",
			Line:     5 + i,
			Column:   1,
			Language: "go",
			Message:  fmt.Sprintf("not enough arguments in call to %s", funcs[i%len(funcs)]),
		})
	}

	// 10 "declared but not used" errors
	for i := 0; i < 10; i++ {
		errs = append(errs, types.ErrorEntry{
			File:     "handler.go",
			Line:     20 + i,
			Column:   1,
			Language: "go",
			Message:  fmt.Sprintf("%s declared but not used", vars[i%len(vars)]),
		})
	}

	if len(errs) != 50 {
		t.Fatalf("expected 50 input errors, got %d", len(errs))
	}

	entries, counts := d.Dedup(errs)

	// Verify reduction: we expect 4 unique patterns (one per error category)
	// across 2 files = 4 entries. That's 92% reduction, well above 60%.
	reductionPct := 100 * (1 - float64(len(entries))/float64(len(errs)))
	t.Logf("Input: %d errors → Output: %d entries (%.1f%% reduction)", len(errs), len(entries), reductionPct)

	if len(entries) > 20 {
		t.Errorf("expected at most 20 entries (60%% reduction), got %d", len(entries))
	}

	// Verify total count matches input.
	totalCount := 0
	for _, c := range counts {
		totalCount += c
	}
	if totalCount != 50 {
		t.Errorf("total count = %d, want 50", totalCount)
	}

	// Verify each entry has Count >= 1.
	for i, c := range counts {
		if c < 1 {
			t.Errorf("counts[%d] = %d, want >= 1", i, c)
		}
	}
}

// TestFusionEngine_ProcessBatch_WithDedup verifies the full pipeline:
// dedup → enrich, with Count set on each result.
func TestFusionEngine_ProcessBatch_WithDedup(t *testing.T) {
	var enrichCount atomic.Int32
	client := &mockLSPClient{
		definitionFn: func(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
			enrichCount.Add(1)
			return []types.Location{{URI: "file:///def.go"}}, nil
		},
		hoverFn: func(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
			return nil, nil
		},
	}

	engine := NewFusionEngineWithConfig(client, goDedupConfig())

	// 10 errors with the same normalized pattern → dedup to 1.
	errs := make([]types.ErrorEntry, 10)
	for i := range errs {
		errs[i] = types.ErrorEntry{File: "main.go", Line: i + 1, Column: 1, Language: "go", Message: fmt.Sprintf("undefined: sym%d", i)}
	}

	results, err := engine.ProcessBatch(context.Background(), errs)
	if err != nil {
		t.Fatal(err)
	}

	// Should be deduped to 1 entry.
	if len(results) != 1 {
		t.Fatalf("expected 1 result after dedup, got %d", len(results))
	}

	// Count should be 10.
	if results[0].Count != 10 {
		t.Errorf("Count = %d, want 10", results[0].Count)
	}

	// LSP should have been called only once (not 10 times).
	if enrichCount.Load() != 1 {
		t.Errorf("enrich calls = %d, want 1", enrichCount.Load())
	}

	// The one result should still be enriched.
	if results[0].Definition == nil {
		t.Error("expected definition on the enriched result")
	}
}
