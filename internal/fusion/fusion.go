package fusion

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Uvean-z/aegislsp/internal/lspclient"
	"github.com/Uvean-z/aegislsp/internal/sandbox"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// ErrorCorrelator matches a terminal error against a set of LSP diagnostics
// to find the ones that correspond to the same source location.
type ErrorCorrelator interface {
	// Correlate filters diagnostics to those that overlap with the error's
	// file, line, and column. Returns an empty slice (not nil) if no
	// diagnostics match.
	Correlate(ctx context.Context, err types.ErrorEntry, diagnostics []types.Diagnostic) ([]types.Diagnostic, error)
}

// ContextEnricher augments a terminal error with AST context from the LSP
// server: symbol information at the error location, go-to-definition, and
// references.
type ContextEnricher interface {
	// Enrich queries the LSP server for symbols, definitions, and references
	// at the error's location and returns an EnrichedError. If the LSP
	// server is unavailable or the file cannot be opened, Enrich returns
	// an EnrichedError with zero-value enrichment fields (not an error).
	Enrich(ctx context.Context, client lspclient.LSPClient, err types.ErrorEntry) (*types.EnrichedError, error)
}

// FusionEngine is the top-level orchestrator that combines correlation and
// enrichment into a single call. It owns the LSP client reference and the
// diagnostic cache.
type FusionEngine interface {
	// Process takes a single terminal error and returns an enriched version
	// with correlated diagnostics and AST context. Process is safe for
	// concurrent use.
	Process(ctx context.Context, err types.ErrorEntry) (*types.EnrichedError, error)

	// ProcessBatch processes multiple errors concurrently (up to the
	// engine's configured concurrency limit) and returns enriched errors
	// in the same order as the input. Errors that fail to enrich are
	// included with zero-value enrichment fields.
	ProcessBatch(ctx context.Context, errors []types.ErrorEntry) ([]types.EnrichedError, error)

	// SetClient updates the LSP client used for enrichment. This allows
	// the fusion engine to be wired before the LSP client is initialized
	// (e.g., the interceptor starts before start_lsp is called).
	SetClient(client lspclient.LSPClient)
}

// ---------------------------------------------------------------------------
// ErrorCorrelator implementation
// ---------------------------------------------------------------------------

type errorCorrelatorImpl struct{}

// NewErrorCorrelator returns a new ErrorCorrelator.
func NewErrorCorrelator() ErrorCorrelator {
	return &errorCorrelatorImpl{}
}

// Correlate filters diagnostics to those whose 0-based range overlaps with the
// error's position (converted from 1-based compiler coordinates). Returns an
// empty slice (never nil) when no diagnostics match.
func (c *errorCorrelatorImpl) Correlate(_ context.Context, entry types.ErrorEntry, diagnostics []types.Diagnostic) ([]types.Diagnostic, error) {
	if len(diagnostics) == 0 {
		return []types.Diagnostic{}, nil
	}

	// Compiler errors use 1-based lines; LSP diagnostics use 0-based.
	errLine := entry.Line - 1
	errCol := entry.Column - 1

	var matched []types.Diagnostic
	for _, d := range diagnostics {
		if errLine < d.Range.Start.Line || errLine > d.Range.End.Line {
			continue
		}
		if errLine == d.Range.Start.Line && errCol < d.Range.Start.Character {
			continue
		}
		if errLine == d.Range.End.Line && errCol > d.Range.End.Character {
			continue
		}
		matched = append(matched, d)
	}
	if matched == nil {
		return []types.Diagnostic{}, nil
	}
	return matched, nil
}

// ---------------------------------------------------------------------------
// ContextEnricher implementation
// ---------------------------------------------------------------------------

type contextEnricherImpl struct{}

// NewContextEnricher returns a new ContextEnricher.
func NewContextEnricher() ContextEnricher {
	return &contextEnricherImpl{}
}

// Enrich queries the LSP server for Definition and Hover at the error's
// location. The two requests are issued concurrently; a failure in either
// does not block the other. If the client is nil, enrichment fields are
// left at their zero values. Context cancellation is respected and returns
// whatever partial results are available.
func (e *contextEnricherImpl) Enrich(ctx context.Context, client lspclient.LSPClient, errEntry types.ErrorEntry) (*types.EnrichedError, error) {
	enriched := &types.EnrichedError{
		ErrorEntry: errEntry,
	}

	if client == nil {
		return enriched, nil
	}

	uri := lspclient.CreateFileURI(errEntry.File)
	pos := types.Position{
		Line:      errEntry.Line - 1,
		Character: errEntry.Column - 1,
	}

	// Fire Definition and Hover concurrently.
	type defResult struct {
		locations []types.Location
		err       error
	}
	type hoverResult struct {
		raw json.RawMessage
		err error
	}

	defCh := make(chan defResult, 1)
	hoverCh := make(chan hoverResult, 1)

	go func() {
		locs, err := client.Definition(ctx, uri, pos)
		defCh <- defResult{locations: locs, err: err}
	}()

	go func() {
		raw, err := client.Hover(ctx, uri, pos)
		hoverCh <- hoverResult{raw: raw, err: err}
	}()

	// Collect results — don't fail on individual errors, just leave fields zero.
	for i := 0; i < 2; i++ {
		select {
		case r := <-defCh:
			if r.err == nil && len(r.locations) > 0 {
				enriched.Definition = &r.locations[0]
			}
		case r := <-hoverCh:
			if r.err == nil && r.raw != nil {
				// Parse hover to extract a useful text representation.
				enriched.Diagnostic = hoverToDiagnostic(r.raw, errEntry)
			}
		case <-ctx.Done():
			return enriched, nil
		}
	}

	return enriched, nil
}

// hoverToDiagnostic converts raw hover JSON into a Diagnostic with the
// hover content as the message, preserving the error's range.
func hoverToDiagnostic(raw json.RawMessage, errEntry types.ErrorEntry) *types.Diagnostic {
	var hover struct {
		Contents struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &hover); err != nil {
		return nil
	}

	msg := hover.Contents.Value
	if msg == "" {
		// Try MarkedString format.
		var ms struct {
			Contents string `json:"contents"`
		}
		if json.Unmarshal(raw, &ms) == nil {
			msg = ms.Contents
		}
	}
	if msg == "" {
		return nil
	}

	return &types.Diagnostic{
		Range: types.Range{
			Start: types.Position{Line: errEntry.Line - 1, Character: errEntry.Column - 1},
			End:   types.Position{Line: errEntry.Line - 1, Character: errEntry.Column},
		},
		Severity: types.SeverityError,
		Source:   "hover",
		Message:  msg,
	}
}

// ---------------------------------------------------------------------------
// FusionEngine implementation
// ---------------------------------------------------------------------------

const defaultConcurrency = 16

type fusionEngineImpl struct {
	mu        sync.RWMutex
	client    lspclient.LSPClient
	corr      ErrorCorrelator
	enricher  ContextEnricher
	dedup     Deduplicator
	sem       chan struct{}
	diagCache map[string][]types.Diagnostic
	diagMu    sync.RWMutex
}

// NewFusionEngine creates a FusionEngine with the given LSP client (may be nil).
func NewFusionEngine(client lspclient.LSPClient) FusionEngine {
	return &fusionEngineImpl{
		client:    client,
		corr:      NewErrorCorrelator(),
		enricher:  NewContextEnricher(),
		dedup:     NewDeduplicator(),
		sem:       make(chan struct{}, defaultConcurrency),
		diagCache: make(map[string][]types.Diagnostic),
	}
}

// NewFusionEngineWithDeps creates a FusionEngine with injectable dependencies.
func NewFusionEngineWithDeps(client lspclient.LSPClient, corr ErrorCorrelator, enricher ContextEnricher) FusionEngine {
	return &fusionEngineImpl{
		client:    client,
		corr:      corr,
		enricher:  enricher,
		dedup:     NewDeduplicator(),
		sem:       make(chan struct{}, defaultConcurrency),
		diagCache: make(map[string][]types.Diagnostic),
	}
}

// NewFusionEngineWithConfig creates a FusionEngine with config-driven deduplication.
func NewFusionEngineWithConfig(client lspclient.LSPClient, dedupCfg *sandbox.DedupConfig) FusionEngine {
	return &fusionEngineImpl{
		client:    client,
		corr:      NewErrorCorrelator(),
		enricher:  NewContextEnricher(),
		dedup:     NewDeduplicatorWithConfig(dedupCfg),
		sem:       make(chan struct{}, defaultConcurrency),
		diagCache: make(map[string][]types.Diagnostic),
	}
}

// SetClient replaces the LSP client used for enrichment. It is safe for
// concurrent use (guarded by e.mu). This enables late binding: the engine
// can be wired before the LSP server is ready.
func (e *fusionEngineImpl) SetClient(client lspclient.LSPClient) {
	e.mu.Lock()
	e.client = client
	e.mu.Unlock()
}

func (e *fusionEngineImpl) getClient() lspclient.LSPClient {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.client
}

// StoreDiagnostics caches diagnostics for a URI (called by the notification handler).
func (e *fusionEngineImpl) StoreDiagnostics(uri string, diagnostics []types.Diagnostic) {
	e.diagMu.Lock()
	e.diagCache[uri] = diagnostics
	e.diagMu.Unlock()
}

// GetDiagnostics returns cached diagnostics for a URI.
func (e *fusionEngineImpl) GetDiagnostics(uri string) []types.Diagnostic {
	e.diagMu.RLock()
	defer e.diagMu.RUnlock()
	return e.diagCache[uri]
}

// Process enriches a single error by correlating it against cached diagnostics
// and querying the LSP server for Definition and Hover. Correlated diagnostics
// take precedence for the Diagnostic field; hover content is appended when both
// are available. Safe for concurrent use.
func (e *fusionEngineImpl) Process(ctx context.Context, errEntry types.ErrorEntry) (*types.EnrichedError, error) {
	client := e.getClient()

	// Step 1: Correlate with cached diagnostics.
	uri := lspclient.CreateFileURI(errEntry.File)
	diags := e.GetDiagnostics(uri)

	correlated, corrErr := e.corr.Correlate(ctx, errEntry, diags)
	if corrErr != nil {
		correlated = []types.Diagnostic{}
	}

	// Step 2: Enrich via LSP (Definition + Hover).
	enriched, enrichErr := e.enricher.Enrich(ctx, client, errEntry)
	if enrichErr != nil {
		enriched = &types.EnrichedError{ErrorEntry: errEntry}
	}

	// Step 3: Assemble — correlated LSP diagnostics take priority for the
	// Diagnostic field. Hover content is preserved in the Diagnostic.Message
	// only when no correlated diagnostic exists.
	if len(correlated) > 0 {
		corrDiag := correlated[0]
		// Merge: keep the correlated diagnostic's range/severity but append
		// hover documentation if available.
		if enriched.Diagnostic != nil && enriched.Diagnostic.Source == "hover" {
			corrDiag.Message = corrDiag.Message + "\n\n" + enriched.Diagnostic.Message
		}
		enriched.Diagnostic = &corrDiag
	}
	// If no correlated diagnostic, enriched.Diagnostic already holds the
	// hover-derived content (set by Enrich), which is the best we have.

	return enriched, nil
}

// ProcessBatch first deduplicates the input errors via the configured
// Deduplicator, then enriches each unique entry concurrently (up to 16
// goroutines, controlled by a semaphore). The returned slice preserves the
// order of the deduplicated input. Each EnrichedError's Count field is set
// to the number of original errors it represents. Safe for concurrent use.
func (e *fusionEngineImpl) ProcessBatch(ctx context.Context, errors []types.ErrorEntry) ([]types.EnrichedError, error) {
	// Step 0: Deduplicate similar errors to reduce noise from cascading failures.
	deduped, counts := e.dedup.Dedup(errors)

	results := make([]types.EnrichedError, len(deduped))
	var wg sync.WaitGroup

	for i, errEntry := range deduped {
		wg.Add(1)
		go func(idx int, entry types.ErrorEntry) {
			defer wg.Done()

			// Acquire semaphore.
			select {
			case e.sem <- struct{}{}:
				defer func() { <-e.sem }()
			case <-ctx.Done():
				results[idx] = types.EnrichedError{ErrorEntry: entry, Count: counts[idx]}
				return
			}

			enriched, _ := e.Process(ctx, entry)
			if enriched != nil {
				enriched.Count = counts[idx]
				results[idx] = *enriched
			} else {
				results[idx] = types.EnrichedError{ErrorEntry: entry, Count: counts[idx]}
			}
		}(i, errEntry)
	}

	wg.Wait()
	return results, nil
}
