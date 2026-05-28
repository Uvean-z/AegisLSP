package main

import (
	"os"
	"testing"
)

func TestSplitRunArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantFlags   []string
		wantCommand []string
	}{
		{
			name:        "flags before separator",
			args:        []string{"--no-sandbox", "--lsp", "/usr/bin/gopls", "--", "go", "build", "./..."},
			wantFlags:   []string{"--no-sandbox", "--lsp", "/usr/bin/gopls"},
			wantCommand: []string{"go", "build", "./..."},
		},
		{
			name:        "command without separator is parsed by flag package",
			args:        []string{"--no-sandbox", "go", "test", "./..."},
			wantFlags:   []string{"--no-sandbox", "go", "test", "./..."},
			wantCommand: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFlags, gotCommand := splitRunArgs(tt.args)
			if !equalStringSlices(gotFlags, tt.wantFlags) {
				t.Fatalf("flags = %#v, want %#v", gotFlags, tt.wantFlags)
			}
			if !equalStringSlices(gotCommand, tt.wantCommand) {
				t.Fatalf("command = %#v, want %#v", gotCommand, tt.wantCommand)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

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
