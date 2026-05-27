package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Sandbox.Enabled {
		t.Error("expected sandbox enabled by default")
	}
	if cfg.Sandbox.Type != "docker" {
		t.Errorf("sandbox type = %q, want docker", cfg.Sandbox.Type)
	}
	if cfg.Sandbox.Image != "golang:1.24-alpine" {
		t.Errorf("sandbox image = %q, want golang:1.24-alpine", cfg.Sandbox.Image)
	}
	if cfg.Sandbox.Timeout != 300 {
		t.Errorf("sandbox timeout = %d, want 300", cfg.Sandbox.Timeout)
	}
	if cfg.Sandbox.MemoryLimit != 512 {
		t.Errorf("memory limit = %d, want 512", cfg.Sandbox.MemoryLimit)
	}
	if cfg.Sandbox.CPULimit != 1.0 {
		t.Errorf("CPU limit = %f, want 1.0", cfg.Sandbox.CPULimit)
	}
	if !cfg.Approvals.Enabled {
		t.Error("expected approvals enabled by default")
	}
	if cfg.Approvals.RiskThreshold != "medium" {
		t.Errorf("risk threshold = %q, want medium", cfg.Approvals.RiskThreshold)
	}
}

func TestConfig_Validate_ValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestConfig_Validate_InvalidSandboxType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Type = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid sandbox type")
	}
}

func TestConfig_Validate_DockerWithoutImage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Image = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for docker sandbox without image")
	}
}

func TestConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Timeout = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative timeout")
	}
}

func TestConfig_Validate_NegativeMemoryLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.MemoryLimit = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative memory limit")
	}
}

func TestConfig_Validate_NegativeCPULimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.CPULimit = -1.0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative CPU limit")
	}
}

func TestConfig_Validate_InvalidRiskThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Approvals.RiskThreshold = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid risk threshold")
	}
}

func TestConfig_Validate_InvalidRiskLevelInRule(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Approvals.Rules = []ApprovalRule{
		{Operation: "test", RiskLevel: "invalid", AutoApprove: false},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid risk level in rule")
	}
}

func TestConfig_Validate_SandboxDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Enabled = false
	cfg.Sandbox.Type = "invalid" // Should not matter when disabled
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled sandbox should skip type validation: %v", err)
	}
}

func TestConfig_Validate_NamespaceType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Type = "namespace"
	if err := cfg.Validate(); err != nil {
		t.Errorf("namespace type should be valid: %v", err)
	}
}

func TestConfig_Validate_NoneType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Type = "none"
	if err := cfg.Validate(); err != nil {
		t.Errorf("none type should be valid: %v", err)
	}
}

func TestLoadConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Image != "golang:1.24-alpine" {
		t.Errorf("expected default image, got %q", cfg.Sandbox.Image)
	}
}

func TestLoadConfig_NonexistentFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/to/config.toml")
	if err == nil {
		t.Error("expected error for nonexistent config file")
	}
}

func TestSanitizePath_ValidPath(t *testing.T) {
	path, err := sanitizePath("some/valid/path")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
}

func TestSanitizePath_EmptyPath(t *testing.T) {
	_, err := sanitizePath("")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestSanitizePath_NullByte(t *testing.T) {
	_, err := sanitizePath("path\x00with\x00nulls")
	if err == nil {
		t.Error("expected error for path with null bytes")
	}
}

func TestContainerPath_WindowsPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`C:\Users\foo`, "/c/Users/foo"},
		{`D:\workspace\project`, "/d/workspace/project"},
		{"/unix/path", "/unix/path"},
		{"relative/path", "relative/path"},
	}
	for _, tt := range tests {
		got := containerPath(tt.input)
		if got != tt.want {
			t.Errorf("containerPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRunConfig_Sanitize_EmptyCommand(t *testing.T) {
	cfg := RunConfig{}
	_, err := cfg.sanitize()
	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestRunConfig_Sanitize_InvalidEnvKey(t *testing.T) {
	cfg := RunConfig{
		Command: []string{"echo", "hello"},
		Env:     map[string]string{"BAD=KEY": "value"},
	}
	_, err := cfg.sanitize()
	if err == nil {
		t.Error("expected error for invalid env key with '='")
	}
}

func TestRunConfig_Sanitize_EnvKeyWithShellMetachar(t *testing.T) {
	metachars := []string{"$", "`", "\\", "'", "\"", ";", "\n", "\r"}
	for _, ch := range metachars {
		cfg := RunConfig{
			Command: []string{"echo"},
			Env:     map[string]string{"KEY" + ch: "value"},
		}
		_, err := cfg.sanitize()
		if err == nil {
			t.Errorf("expected error for env key containing %q", ch)
		}
	}
}

func TestRunConfig_Sanitize_ValidConfig(t *testing.T) {
	cfg := RunConfig{
		Command: []string{"go", "build", "./..."},
		WorkDir: "/home/user/project",
		Env:     map[string]string{"GOPATH": "/go"},
	}
	sanitized, err := cfg.sanitize()
	if err != nil {
		t.Fatal(err)
	}
	if len(sanitized.Command) != 3 {
		t.Errorf("command length = %d, want 3", len(sanitized.Command))
	}
}

func TestNewSandboxLauncher_NilConfig(t *testing.T) {
	_, err := NewSandboxLauncher(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestNewSandboxLauncher_UnsupportedType(t *testing.T) {
	cfg := &SandboxConfig{Type: "unsupported"}
	_, err := NewSandboxLauncher(cfg)
	if err == nil {
		t.Error("expected error for unsupported sandbox type")
	}
}

func TestNewSandboxLauncher_NoneType(t *testing.T) {
	cfg := &SandboxConfig{Type: "none"}
	launcher, err := NewSandboxLauncher(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer launcher.Close()
	if _, ok := launcher.(*NativeLauncher); !ok {
		t.Error("expected NativeLauncher for type 'none'")
	}
}

// ---------------------------------------------------------------------------
// NativeLauncher tests
// ---------------------------------------------------------------------------

func TestNativeLauncher_Run_SuccessCommand(t *testing.T) {
	launcher := NewNativeLauncher()
	defer launcher.Close()

	var buf strings.Builder
	code, err := launcher.Run(context.Background(), RunConfig{
		Command: []string{"cmd", "/c", "echo", "hello"},
	}, &buf)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("output = %q, want it to contain 'hello'", buf.String())
	}
}

func TestNativeLauncher_Run_FailingCommand(t *testing.T) {
	launcher := NewNativeLauncher()
	defer launcher.Close()

	var buf strings.Builder
	code, err := launcher.Run(context.Background(), RunConfig{
		Command: []string{"cmd", "/c", "exit", "42"},
	}, &buf)

	if err == nil {
		t.Error("expected error for failing command")
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestNativeLauncher_Run_EmptyCommand(t *testing.T) {
	launcher := NewNativeLauncher()
	defer launcher.Close()

	_, err := launcher.Run(context.Background(), RunConfig{}, nil)
	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestNativeLauncher_Run_WithEnvVars(t *testing.T) {
	launcher := NewNativeLauncher()
	defer launcher.Close()

	var buf strings.Builder
	code, err := launcher.Run(context.Background(), RunConfig{
		Command: []string{"cmd", "/c", "echo", "%AEGIS_TEST_VAR%"},
		Env:     map[string]string{"AEGIS_TEST_VAR": "test_value"},
	}, &buf)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestNativeLauncher_Run_WithWorkDir(t *testing.T) {
	launcher := NewNativeLauncher()
	defer launcher.Close()

	var buf strings.Builder
	code, err := launcher.Run(context.Background(), RunConfig{
		Command: []string{"cmd", "/c", "cd"},
		WorkDir: t.TempDir(),
	}, &buf)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestNativeLauncher_Close(t *testing.T) {
	launcher := NewNativeLauncher()
	if err := launcher.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}
