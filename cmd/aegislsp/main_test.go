package main

import (
	"os"
	"testing"
)

func TestIsInteractive_Pipe(t *testing.T) {
	// In `go test`, stdin is typically a pipe (not a TTY).
	// isInteractive() should return false.
	fi, err := os.Stdin.Stat()
	if err != nil {
		t.Skipf("cannot stat stdin: %v", err)
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		t.Skip("stdin is a TTY; pipe-only test skipped")
	}

	if isInteractive() {
		t.Error("expected isInteractive()=false when stdin is a pipe")
	}
}
