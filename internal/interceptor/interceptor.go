package interceptor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Uvean-z/aegislsp/internal/types"
)

// ProcessConfig holds the parameters for spawning a child process.
type ProcessConfig struct {
	Name    string        // Executable name or path
	Args    []string      // Command-line arguments
	Dir     string        // Working directory (empty = inherit)
	Env     []string      // Environment variables (nil = inherit)
	Timeout time.Duration // Maximum process lifetime (0 = no limit)
}

// ProcessSpawner spawns a child process and provides access to its streams.
// Implementations must guarantee that the returned io.Readers remain valid
// until Kill or Wait is called.
type ProcessSpawner interface {
	// Spawn starts the process described by config and returns readers for
	// its stdout and stderr. The context governs the spawn operation itself;
	// use ProcessConfig.Timeout for the process lifetime.
	Spawn(ctx context.Context, config ProcessConfig) (stdout io.Reader, stderr io.Reader, err error)

	// Kill forcefully terminates the process.
	Kill() error

	// Wait blocks until the process exits and returns its exit code.
	// Wait must be called after Spawn to avoid zombie processes.
	Wait() (int, error)

	// Pid returns the process ID, or -1 if the process has not been spawned.
	Pid() int
}

// StreamParser reads raw bytes from an io.Reader and emits structured entries.
// Implementations must respect context cancellation and close the returned
// channel when the reader is exhausted or the context is cancelled.
type StreamParser interface {
	// Parse reads from r, interprets each line according to source, and sends
	// parsed Entry values on the returned channel. The channel is closed when
	// r returns io.EOF or ctx is cancelled. The caller must not close the
	// returned channel.
	Parse(ctx context.Context, r io.Reader, source types.StreamSource) <-chan types.Entry

	// ParseError attempts to extract structured error fields from an entry.
	// Returns the ErrorEntry and true if the entry matches a known error
	// pattern (compiler error, linter warning, etc.), or zero-value and false
	// otherwise.
	ParseError(entry types.Entry) (types.ErrorEntry, bool)
}

// LineEmitter is a goroutine-safe entry sink backed by a buffered channel.
// It decouples producers (parsers) from consumers (the fusion engine or
// downstream handlers).
type LineEmitter interface {
	// Emit sends an entry to the emitter. Emit must never block for longer
	// than the channel buffer allows; if the buffer is full, the oldest
	// entry is dropped with a warning.
	Emit(entry types.Entry)

	// Output returns a receive-only channel for consuming emitted entries.
	Output() <-chan types.Entry

	// Close signals the emitter to stop accepting entries and waits for
	// all buffered entries to be drained by consumers. Close must be
	// idempotent.
	Close()
}

// ErrorEmitter is a goroutine-safe error-entry sink, parallel to LineEmitter
// but typed for ErrorEntry values so the error path does not require
// type assertions downstream.
type ErrorEmitter interface {
	Emit(entry types.ErrorEntry)
	Output() <-chan types.ErrorEntry
	Close()
}

// Interceptor is the high-level orchestrator that composes spawning, parsing,
// and emitting into a single lifecycle.
//
// Usage:
//
//	inter := interceptor.New(spawner, parser, lineEmitter, errEmitter)
//	if err := inter.Start(ctx, config); err != nil { ... }
//	defer inter.Stop()
//	for entry := range inter.Entries() { ... }
//	if err := inter.Wait(); err != nil { ... }
type Interceptor interface {
	// Start spawns the process and begins intercepting stdout and stderr in
	// separate goroutines. Each goroutine is tracked by a sync.WaitGroup to
	// prevent leaks. Start returns an error if the process fails to spawn.
	Start(ctx context.Context, config ProcessConfig) error

	// Entries returns a receive-only channel of all parsed entries (both
	// stdout and stderr, interleaved). The channel is closed when both
	// stream goroutines finish.
	Entries() <-chan types.Entry

	// Errors returns a receive-only channel of entries that matched a known
	// error pattern. Only entries where ParseError returns true appear here.
	Errors() <-chan types.ErrorEntry

	// Wait blocks until the process exits and all stream goroutines have
	// drained. Wait must be called after Start. If the process exited with
	// a non-zero code, Wait returns an error wrapping the exit code.
	Wait() error

	// Stop forcefully terminates the process (if still running) and waits
	// for all goroutines to finish. Stop is safe to call multiple times.
	Stop() error
}

// ---------------------------------------------------------------------------
// Concrete implementations
// ---------------------------------------------------------------------------

// compilerErrorRe matches Go compiler error format: file.go:line:col: message
var compilerErrorRe = regexp.MustCompile(`^([^:]+):(\d+):(\d+):\s*(.+)$`)

// ErrorPatternDef is a compiled error pattern with its language label.
type ErrorPatternDef struct {
	Language string
	Regex    *regexp.Regexp
	Priority int
}

// DefaultErrorPatterns returns the built-in Go compiler error pattern.
func DefaultErrorPatterns() []ErrorPatternDef {
	return []ErrorPatternDef{
		{Language: "go", Regex: compilerErrorRe, Priority: 0},
	}
}

// CompilePatterns compiles a slice of language/regex config entries into
// compiled ErrorPatternDef values, sorted by priority (lower = higher priority).
// Returns an error if any regex fails to compile.
func CompilePatterns(configs []struct {
	Language string
	Regex    string
	Priority int
}) ([]ErrorPatternDef, error) {
	defs := make([]ErrorPatternDef, 0, len(configs))
	for _, c := range configs {
		re, err := regexp.Compile(c.Regex)
		if err != nil {
			return nil, fmt.Errorf("compile regex for %q: %w", c.Language, err)
		}
		defs = append(defs, ErrorPatternDef{
			Language: c.Language,
			Regex:    re,
			Priority: c.Priority,
		})
	}
	// Sort by priority (lower value = higher priority, tried first).
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Priority < defs[j].Priority
	})
	return defs, nil
}

// --- lineEmitter ---

const emitterBufSize = 256

type lineEmitter struct {
	ch     chan types.Entry
	once   sync.Once
	closed bool
}

func newLineEmitter() *lineEmitter {
	return &lineEmitter{ch: make(chan types.Entry, emitterBufSize)}
}

func (e *lineEmitter) Emit(entry types.Entry) {
	select {
	case e.ch <- entry:
	default:
		log.Printf("lineEmitter: buffer full, dropping entry line=%d", entry.LineNum)
	}
}

func (e *lineEmitter) Output() <-chan types.Entry {
	return e.ch
}

func (e *lineEmitter) Close() {
	e.once.Do(func() {
		close(e.ch)
		e.closed = true
	})
}

// --- errorEmitter ---

type errorEmitter struct {
	ch   chan types.ErrorEntry
	once sync.Once
}

func newErrorEmitter() *errorEmitter {
	return &errorEmitter{ch: make(chan types.ErrorEntry, emitterBufSize)}
}

func (e *errorEmitter) Emit(entry types.ErrorEntry) {
	select {
	case e.ch <- entry:
	default:
		log.Printf("errorEmitter: buffer full, dropping error entry line=%d", entry.LineNum)
	}
}

func (e *errorEmitter) Output() <-chan types.ErrorEntry {
	return e.ch
}

func (e *errorEmitter) Close() {
	e.once.Do(func() {
		close(e.ch)
	})
}

// --- processSpawner ---

type processSpawner struct {
	cmd *exec.Cmd
}

// NewProcessSpawner returns a new ProcessSpawner.
func NewProcessSpawner() ProcessSpawner {
	return &processSpawner{}
}

// Spawn starts the child process described by config and returns readers for
// its stdout and stderr. If config.Timeout is positive, the context is wrapped
// with a deadline equal to the timeout duration. The caller must call Wait or
// Kill to avoid zombie processes.
func (s *processSpawner) Spawn(ctx context.Context, config ProcessConfig) (io.Reader, io.Reader, error) {
	var cmd *exec.Cmd
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		_ = cancel // context lifetime tied to process
	}
	cmd = exec.CommandContext(ctx, config.Name, config.Args...)
	if config.Dir != "" {
		cmd.Dir = config.Dir
	}
	if config.Env != nil {
		cmd.Env = config.Env
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start process: %w", err)
	}
	s.cmd = cmd
	return stdout, stderr, nil
}

// Kill sends SIGKILL to the child process. Returns nil if no process has
// been spawned.
func (s *processSpawner) Kill() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// Wait blocks until the child process exits. Returns the exit code and any
// error. A zero exit code with nil error indicates clean exit.
func (s *processSpawner) Wait() (int, error) {
	if s.cmd == nil {
		return -1, fmt.Errorf("process not started")
	}
	err := s.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), err
	}
	return -1, err
}

// Pid returns the OS process ID, or -1 if the process has not been spawned.
func (s *processSpawner) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return -1
	}
	return s.cmd.Process.Pid
}

// --- streamParser ---

type streamParser struct {
	patterns []ErrorPatternDef
}

// NewStreamParser returns a new StreamParser with default Go error patterns.
func NewStreamParser() StreamParser {
	return &streamParser{patterns: DefaultErrorPatterns()}
}

// NewStreamParserWithPatterns returns a StreamParser that matches errors using
// the provided patterns. Patterns are tried in order; first match wins.
func NewStreamParserWithPatterns(patterns []ErrorPatternDef) StreamParser {
	if len(patterns) == 0 {
		patterns = DefaultErrorPatterns()
	}
	return &streamParser{patterns: patterns}
}

// Parse reads lines from r and emits Entry values on the returned channel.
// The channel is closed when r returns io.EOF or ctx is cancelled. If r
// implements io.Closer, it is closed on context cancellation to unblock
// pending reads, and on normal exit to prevent resource leaks.
func (p *streamParser) Parse(ctx context.Context, r io.Reader, source types.StreamSource) <-chan types.Entry {
	ch := make(chan types.Entry, emitterBufSize)
	go func() {
		defer close(ch)
		// If the reader is also a Closer (e.g. *exec.Cmd pipe), close it
		// when context is cancelled to unblock pending Read/Scan calls,
		// and on normal exit to prevent leaks. sync.Once prevents double-close.
		closer, canClose := r.(io.Closer)
		if canClose {
			var once sync.Once
			closeReader := func() { once.Do(func() { closer.Close() }) }
			go func() {
				<-ctx.Done()
				closeReader()
			}()
			defer closeReader()
		}
		scanner := bufio.NewScanner(r)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			entry := types.Entry{
				Source:    source,
				LineNum:   lineNum,
				Text:      scanner.Text(),
				Timestamp: time.Now(),
			}
			select {
			case ch <- entry:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("streamParser: scan error (source=%v): %v", source, err)
		}
	}()
	return ch
}

// ParseError attempts to match entry.Text against each registered error
// pattern (in priority order). On the first match it extracts file, line,
// column, and message from the regex capture groups and returns the structured
// ErrorEntry. Returns false if no pattern matches.
func (p *streamParser) ParseError(entry types.Entry) (types.ErrorEntry, bool) {
	for _, pat := range p.patterns {
		matches := pat.Regex.FindStringSubmatch(entry.Text)
		if matches == nil {
			continue
		}
		line, _ := strconv.Atoi(matches[2])
		col, _ := strconv.Atoi(matches[3])
		return types.ErrorEntry{
			Entry:    entry,
			File:     matches[1],
			Line:     line,
			Column:   col,
			Message:  matches[4],
			Language: pat.Language,
		}, true
	}
	return types.ErrorEntry{}, false
}

// --- interceptorImpl ---

type interceptorImpl struct {
	spawner     ProcessSpawner
	parser      StreamParser
	lineEmitter LineEmitter
	errEmitter  ErrorEmitter
	wg          sync.WaitGroup
	mu          sync.Mutex
	started     bool
	stopped     bool
}

// New creates an Interceptor from its component parts.
func New(spawner ProcessSpawner, parser StreamParser, le LineEmitter, ee ErrorEmitter) Interceptor {
	return &interceptorImpl{
		spawner:     spawner,
		parser:      parser,
		lineEmitter: le,
		errEmitter:  ee,
	}
}

// Start spawns the process and launches two goroutines to parse stdout and
// stderr concurrently. Parsed entries are sent to lineEmitter; entries that
// match an error pattern are additionally sent to errEmitter. Both goroutines
// are tracked by a sync.WaitGroup. Returns an error if the process fails to
// spawn or if Start has already been called.
func (i *interceptorImpl) Start(ctx context.Context, config ProcessConfig) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started {
		return fmt.Errorf("interceptor already started")
	}

	stdout, stderr, err := i.spawner.Spawn(ctx, config)
	if err != nil {
		return err
	}
	i.started = true

	// Goroutine: parse stdout → lineEmitter
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		for entry := range i.parser.Parse(ctx, stdout, types.SourceStdout) {
			i.lineEmitter.Emit(entry)
		}
	}()

	// Goroutine: parse stderr → lineEmitter + errEmitter (if error pattern matches)
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		for entry := range i.parser.Parse(ctx, stderr, types.SourceStderr) {
			i.lineEmitter.Emit(entry)
			if errEntry, ok := i.parser.ParseError(entry); ok {
				i.errEmitter.Emit(errEntry)
			}
		}
	}()

	return nil
}

// Entries returns a receive-only channel of all parsed entries (stdout and
// stderr interleaved). The channel is closed when both stream goroutines finish.
func (i *interceptorImpl) Entries() <-chan types.Entry {
	return i.lineEmitter.Output()
}

// Errors returns a receive-only channel of entries that matched a known
// error pattern. The channel is closed when the stderr goroutine finishes.
func (i *interceptorImpl) Errors() <-chan types.ErrorEntry {
	return i.errEmitter.Output()
}

// Wait blocks until the process exits and all stream goroutines have drained.
// After Wait returns, the Entries and Errors channels are closed. Returns an
// error wrapping the exit code if the process exited non-zero.
func (i *interceptorImpl) Wait() error {
	i.mu.Lock()
	started := i.started
	i.mu.Unlock()
	if !started {
		return fmt.Errorf("interceptor not started")
	}

	_, waitErr := i.spawner.Wait()
	i.wg.Wait()
	i.lineEmitter.Close()
	i.errEmitter.Close()
	return waitErr
}

// Stop forcefully terminates the process (if still running) and calls Wait
// to drain all goroutines. Stop is safe to call multiple times and from
// multiple goroutines; only the first call sends the kill signal.
func (i *interceptorImpl) Stop() error {
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		return nil
	}
	i.stopped = true
	i.mu.Unlock()

	_ = i.spawner.Kill()
	return i.Wait()
}
