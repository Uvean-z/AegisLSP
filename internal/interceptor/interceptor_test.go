package interceptor

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Uvean-z/aegislsp/internal/types"
)

// ---------------------------------------------------------------------------
// LineEmitter tests
// ---------------------------------------------------------------------------

func TestLineEmitter_BasicEmitOutput(t *testing.T) {
	em := newLineEmitter()
	defer em.Close()

	want := []types.Entry{
		{Source: types.SourceStdout, LineNum: 1, Text: "hello"},
		{Source: types.SourceStdout, LineNum: 2, Text: "world"},
		{Source: types.SourceStderr, LineNum: 1, Text: "error"},
	}
	for _, e := range want {
		em.Emit(e)
	}

	var got []types.Entry
	for e := range em.Output() {
		got = append(got, e)
		if len(got) == len(want) {
			em.Close()
		}
	}

	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g.Text != want[i].Text {
			t.Errorf("entry[%d].Text = %q, want %q", i, g.Text, want[i].Text)
		}
	}
}

func TestLineEmitter_CloseIdempotent(t *testing.T) {
	em := newLineEmitter()
	em.Close()
	em.Close() // must not panic
}

// ---------------------------------------------------------------------------
// ErrorEmitter tests
// ---------------------------------------------------------------------------

func TestErrorEmitter_BasicEmitOutput(t *testing.T) {
	em := newErrorEmitter()
	defer em.Close()

	want := types.ErrorEntry{
		Entry:   types.Entry{Source: types.SourceStderr, LineNum: 1, Text: "main.go:12:5: undefined: foo"},
		File:    "main.go",
		Line:    12,
		Column:  5,
		Message: "undefined: foo",
	}
	em.Emit(want)

	em.Close()
	got, ok := <-em.Output()
	if !ok {
		t.Fatal("channel closed before receiving entry")
	}
	if got.File != want.File || got.Line != want.Line || got.Column != want.Column {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestErrorEmitter_CloseIdempotent(t *testing.T) {
	em := newErrorEmitter()
	em.Close()
	em.Close() // must not panic
}

// ---------------------------------------------------------------------------
// StreamParser tests
// ---------------------------------------------------------------------------

func TestStreamParser_ParseCompilerErrors(t *testing.T) {
	parser := NewStreamParser()

	input := "main.go:12:5: undefined: foo\n"
	r := strings.NewReader(input)
	ctx := context.Background()

	ch := parser.Parse(ctx, r, types.SourceStderr)

	var entries []types.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Text != "main.go:12:5: undefined: foo" {
		t.Errorf("text = %q", entries[0].Text)
	}

	errEntry, ok := parser.ParseError(entries[0])
	if !ok {
		t.Fatal("ParseError returned false for compiler error")
	}
	if errEntry.File != "main.go" {
		t.Errorf("File = %q, want %q", errEntry.File, "main.go")
	}
	if errEntry.Line != 12 {
		t.Errorf("Line = %d, want 12", errEntry.Line)
	}
	if errEntry.Column != 5 {
		t.Errorf("Column = %d, want 5", errEntry.Column)
	}
	if errEntry.Message != "undefined: foo" {
		t.Errorf("Message = %q, want %q", errEntry.Message, "undefined: foo")
	}
}

func TestStreamParser_ParseNestedPath(t *testing.T) {
	parser := NewStreamParser()

	input := "pkg/utils/helper.go:42:10: cannot use x (type int) as type string\n"
	r := strings.NewReader(input)
	ctx := context.Background()

	ch := parser.Parse(ctx, r, types.SourceStderr)
	entry := <-ch

	errEntry, ok := parser.ParseError(entry)
	if !ok {
		t.Fatal("ParseError returned false for nested path error")
	}
	if errEntry.File != "pkg/utils/helper.go" {
		t.Errorf("File = %q, want %q", errEntry.File, "pkg/utils/helper.go")
	}
	if errEntry.Line != 42 {
		t.Errorf("Line = %d, want 42", errEntry.Line)
	}
	if errEntry.Column != 10 {
		t.Errorf("Column = %d, want 10", errEntry.Column)
	}
}

func TestStreamParser_ParseNonErrorLines(t *testing.T) {
	parser := NewStreamParser()

	nonErrors := []string{
		"Building...",
		"ok  \tgithub.com/Uvean-z/aegislsp\t0.5s",
		"# github.com/Uvean-z/aegislsp",
		"",
		"PASS",
	}

	for _, line := range nonErrors {
		entry := types.Entry{Text: line}
		_, ok := parser.ParseError(entry)
		if ok {
			t.Errorf("ParseError(%q) should return false", line)
		}
	}
}

func TestStreamParser_MultipleLines(t *testing.T) {
	parser := NewStreamParser()

	input := "line one\nmain.go:1:1: err1\nline three\nfoo.go:10:20: err2\n"
	r := strings.NewReader(input)
	ctx := context.Background()

	ch := parser.Parse(ctx, r, types.SourceStderr)

	var entries []types.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	// Check line numbers
	for i, e := range entries {
		if e.LineNum != i+1 {
			t.Errorf("entry[%d].LineNum = %d, want %d", i, e.LineNum, i+1)
		}
	}

	// Check which ones are errors
	parser2 := NewStreamParser()
	wantError := []bool{false, true, false, true}
	for i, e := range entries {
		_, ok := parser2.ParseError(e)
		if ok != wantError[i] {
			t.Errorf("ParseError(entry[%d]) = %v, want %v", i, ok, wantError[i])
		}
	}
}

func TestStreamParser_ContextCancellation(t *testing.T) {
	parser := NewStreamParser()

	// Create a reader that blocks forever
	pr, pw := io.Pipe()
	defer pw.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := parser.Parse(ctx, pr, types.SourceStdout)

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Channel should close due to context cancellation
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for channel to close after context cancellation")
	}
}

func TestStreamParser_SourceTracking(t *testing.T) {
	parser := NewStreamParser()

	stdoutReader := strings.NewReader("stdout line\n")
	stderrReader := strings.NewReader("stderr line\n")
	ctx := context.Background()

	stdoutCh := parser.Parse(ctx, stdoutReader, types.SourceStdout)
	stderrCh := parser.Parse(ctx, stderrReader, types.SourceStderr)

	stdoutEntry := <-stdoutCh
	stderrEntry := <-stderrCh

	if stdoutEntry.Source != types.SourceStdout {
		t.Errorf("stdout entry Source = %d, want SourceStdout", stdoutEntry.Source)
	}
	if stderrEntry.Source != types.SourceStderr {
		t.Errorf("stderr entry Source = %d, want SourceStderr", stderrEntry.Source)
	}
}

// ---------------------------------------------------------------------------
// ProcessSpawner tests
// ---------------------------------------------------------------------------

func testShellCommand(script string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", script}
	}
	return "sh", []string{"-c", script}
}

func longRunningCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "ping -n 60 127.0.0.1"}
	}
	return "sh", []string{"-c", "sleep 60"}
}

func busyCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "for /L %i in (1,1,1000000) do @echo %i"}
	}
	return "sh", []string{"-c", "while true; do echo x; done"}
}

func mixedOutputCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo stdout_line & echo main.go:10:5: some_error 1>&2"}
	}
	return "sh", []string{"-c", "echo stdout_line; echo main.go:10:5: some_error >&2"}
}

func TestProcessSpawner_SuccessCommand(t *testing.T) {
	spawner := NewProcessSpawner()
	ctx := context.Background()

	name, args := testShellCommand("echo hello")
	config := ProcessConfig{
		Name: name,
		Args: args,
	}

	stdout, stderr, err := spawner.Spawn(ctx, config)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&outBuf, stdout)
		close(done)
	}()
	io.Copy(&errBuf, stderr)
	<-done

	exitCode, _ := spawner.Wait()
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	output := strings.TrimSpace(outBuf.String())
	if output != "hello" {
		t.Errorf("stdout = %q, want %q", output, "hello")
	}
}

func TestProcessSpawner_FailingCommand(t *testing.T) {
	spawner := NewProcessSpawner()
	ctx := context.Background()

	name, args := testShellCommand("exit 42")
	config := ProcessConfig{
		Name: name,
		Args: args,
	}

	stdout, stderr, err := spawner.Spawn(ctx, config)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Drain streams
	go io.Copy(io.Discard, stdout)
	io.Copy(io.Discard, stderr)

	exitCode, waitErr := spawner.Wait()
	if exitCode != 42 {
		t.Errorf("exit code = %d, want 42", exitCode)
	}
	if waitErr == nil {
		t.Error("expected non-nil error for failing command")
	}
}

func TestProcessSpawner_CommandNotFound(t *testing.T) {
	spawner := NewProcessSpawner()
	ctx := context.Background()

	config := ProcessConfig{
		Name: "nonexistent_command_xyz_12345",
		Args: []string{},
	}

	_, _, err := spawner.Spawn(ctx, config)
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestProcessSpawner_Kill(t *testing.T) {
	spawner := NewProcessSpawner()
	ctx := context.Background()

	name, args := longRunningCommand()
	config := ProcessConfig{
		Name: name,
		Args: args,
	}

	stdout, stderr, err := spawner.Spawn(ctx, config)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(io.Discard, stdout) }()
	go func() { defer wg.Done(); io.Copy(io.Discard, stderr) }()

	// Give process time to start
	time.Sleep(100 * time.Millisecond)

	if spawner.Pid() <= 0 {
		t.Errorf("Pid = %d, want > 0", spawner.Pid())
	}

	killErr := spawner.Kill()
	if killErr != nil {
		t.Errorf("Kill: %v", killErr)
	}

	// Wait for process to be reaped and pipes to close
	spawner.Wait()
	wg.Wait()
}

func TestProcessSpawner_Timeout(t *testing.T) {
	spawner := NewProcessSpawner()
	ctx := context.Background()

	name, args := busyCommand()
	config := ProcessConfig{
		Name:    name,
		Args:    args,
		Timeout: 500 * time.Millisecond,
	}

	stdout, stderr, err := spawner.Spawn(ctx, config)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	go io.Copy(io.Discard, stdout)
	go io.Copy(io.Discard, stderr)

	// Wait should return once the timeout kills the process
	done := make(chan struct{})
	go func() {
		spawner.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success — process was killed by timeout
	case <-time.After(5 * time.Second):
		t.Fatal("Wait() did not return after timeout — process may not have been killed")
	}
}

func TestProcessSpawner_PidBeforeSpawn(t *testing.T) {
	spawner := NewProcessSpawner()
	if spawner.Pid() != -1 {
		t.Errorf("Pid before Spawn = %d, want -1", spawner.Pid())
	}
}

func TestProcessSpawner_WaitBeforeSpawn(t *testing.T) {
	spawner := NewProcessSpawner()
	_, err := spawner.Wait()
	if err == nil {
		t.Error("expected error when Wait called before Spawn")
	}
}

// ---------------------------------------------------------------------------
// Interceptor tests
// ---------------------------------------------------------------------------

// mockSpawner implements ProcessSpawner for testing Interceptor without real processes.
type mockSpawner struct {
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	exitCode int
	waitErr  error
	pid      int
	killed   bool
}

func (m *mockSpawner) Spawn(_ context.Context, _ ProcessConfig) (io.Reader, io.Reader, error) {
	return m.stdout, m.stderr, nil
}

func (m *mockSpawner) Kill() error {
	m.killed = true
	return nil
}

func (m *mockSpawner) Wait() (int, error) {
	// Close pipes so Parse goroutines see EOF and exit
	if m.stdout != nil {
		m.stdout.Close()
	}
	if m.stderr != nil {
		m.stderr.Close()
	}
	return m.exitCode, m.waitErr
}

func (m *mockSpawner) Pid() int {
	return m.pid
}

// nopCloser wraps a reader to satisfy io.ReadCloser.
type nopCloser struct {
	r io.Reader
}

func (c *nopCloser) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *nopCloser) Close() error {
	if closer, ok := c.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func newNopCloser(s string) io.ReadCloser {
	return &nopCloser{r: strings.NewReader(s)}
}

func TestInterceptor_WithMockSpawner(t *testing.T) {
	// Use io.Pipe so we can control EOF precisely.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	spawner := &mockSpawner{stdout: stdoutR, stderr: stderrR, pid: 42}
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	ctx := context.Background()
	err := inter.Start(ctx, ProcessConfig{Name: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Start draining entries and errors BEFORE Wait closes the emitters.
	var allEntries []types.Entry
	var allErrors []types.ErrorEntry
	done := make(chan struct{})
	go func() {
		for e := range inter.Entries() {
			allEntries = append(allEntries, e)
		}
		for e := range inter.Errors() {
			allErrors = append(allErrors, e)
		}
		close(done)
	}()

	// Write test data and close writers so scanners see EOF.
	go func() {
		stdoutW.Write([]byte("building...\n"))
		stdoutW.Close()
	}()
	go func() {
		stderrW.Write([]byte("main.go:5:3: undefined: bar\n"))
		stderrW.Close()
	}()

	// Give goroutines time to start reading from pipes before Wait
	// closes the pipe read ends via mockSpawner.Wait().
	time.Sleep(50 * time.Millisecond)

	// Run Wait — it closes the emitters once goroutines drain.
	inter.Wait()

	// Wait for the drain goroutine to finish consuming buffered entries.
	<-done

	// Should have 2 entries (stdout + stderr)
	if len(allEntries) != 2 {
		t.Errorf("got %d entries, want 2", len(allEntries))
	}

	if len(allErrors) != 1 {
		t.Fatalf("got %d errors, want 1", len(allErrors))
	}
	if allErrors[0].File != "main.go" || allErrors[0].Line != 5 {
		t.Errorf("error = %+v", allErrors[0])
	}
}

func TestInterceptor_ContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	spawner := &mockSpawner{stdout: pr, stderr: newNopCloser(""), pid: 1}
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	ctx, cancel := context.WithCancel(context.Background())
	err := inter.Start(ctx, ProcessConfig{Name: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel context — this should cause Parse goroutines to exit
	cancel()

	// Write something to unblock any blocking reads
	pw.Write([]byte("data\n"))
	pw.Close()

	// Wait should complete without hanging
	done := make(chan error, 1)
	go func() {
		done <- inter.Wait()
	}()

	select {
	case <-done:
		// success — goroutines exited
	case <-time.After(5 * time.Second):
		t.Fatal("Wait() did not return after context cancellation — possible goroutine leak")
	}
}

func TestInterceptor_StopBeforeStart(t *testing.T) {
	spawner := &mockSpawner{stdout: newNopCloser(""), stderr: newNopCloser("")}
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	// Stop before Start should not panic
	err := inter.Stop()
	if err == nil {
		t.Log("Stop before Start returned nil (acceptable)")
	} else {
		t.Logf("Stop before Start returned: %v", err)
	}
}

func TestInterceptor_DoubleStop(t *testing.T) {
	stdout := newNopCloser("")
	stderr := newNopCloser("")
	spawner := &mockSpawner{stdout: stdout, stderr: stderr}
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	ctx := context.Background()
	inter.Start(ctx, ProcessConfig{Name: "test"})

	// First Stop
	inter.Stop()
	// Second Stop must not panic
	inter.Stop()
}

func TestInterceptor_DoubleStart(t *testing.T) {
	spawner := &mockSpawner{stdout: newNopCloser(""), stderr: newNopCloser("")}
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	ctx := context.Background()
	err := inter.Start(ctx, ProcessConfig{Name: "test"})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	err = inter.Start(ctx, ProcessConfig{Name: "test"})
	if err == nil {
		t.Error("expected error on second Start")
	}
}

func TestInterceptor_FullLifecycle(t *testing.T) {
	spawner := NewProcessSpawner()
	parser := NewStreamParser()
	le := newLineEmitter()
	ee := newErrorEmitter()

	inter := New(spawner, parser, le, ee)

	ctx := context.Background()
	name, args := mixedOutputCommand()
	err := inter.Start(ctx, ProcessConfig{
		Name: name,
		Args: args,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var entries []types.Entry
	var errs []types.ErrorEntry
	done := make(chan struct{})
	go func() {
		for e := range inter.Entries() {
			entries = append(entries, e)
		}
		for e := range inter.Errors() {
			errs = append(errs, e)
		}
		close(done)
	}()

	waitErr := inter.Wait()
	if waitErr != nil {
		t.Logf("Wait returned: %v (may be non-zero due to mixed output)", waitErr)
	}
	<-done

	if len(entries) == 0 {
		t.Error("expected at least 1 entry from full lifecycle")
	}

	t.Logf("Got %d entries, %d errors", len(entries), len(errs))
}

func TestInterceptor_NoGoroutineLeak(t *testing.T) {
	// Run the interceptor lifecycle many times to detect goroutine leaks.
	for i := 0; i < 20; i++ {
		spawner := &mockSpawner{
			stdout:   newNopCloser("line\n"),
			stderr:   newNopCloser("main.go:1:1: err\n"),
			exitCode: 0,
		}
		parser := NewStreamParser()
		le := newLineEmitter()
		ee := newErrorEmitter()

		inter := New(spawner, parser, le, ee)
		ctx := context.Background()

		if err := inter.Start(ctx, ProcessConfig{Name: "test"}); err != nil {
			t.Fatalf("iteration %d: Start: %v", i, err)
		}

		// Drain all channels and run Wait concurrently
		done := make(chan struct{})
		go func() {
			for range inter.Entries() {
			}
			for range inter.Errors() {
			}
			close(done)
		}()
		go func() { inter.Wait() }()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timeout — possible goroutine leak", i)
		}
	}
	// If we get here without hanging or panicking, goroutines are leaking cleanly.
}
