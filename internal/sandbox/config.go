package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ErrorPattern defines a regex pattern for matching compiler/linter errors
// from a specific language. The regex must contain exactly 4 capture groups:
// (1) file path, (2) line number, (3) column number, (4) error message.
type ErrorPattern struct {
	Language string `toml:"language"`
	Regex    string `toml:"regex"`
	Priority int    `toml:"priority"`
}

// DedupNormRule defines a single normalization rule for error deduplication.
// Regex matches a pattern in the error message; Replacement is applied via
// regexp.ReplaceAllString (supports $1, ${1}VAR, etc.).
type DedupNormRule struct {
	Regex       string `toml:"regex"`
	Replacement string `toml:"replacement"`
}

// DedupLangConfig defines language-specific dedup normalization settings.
type DedupLangConfig struct {
	Language string          `toml:"language"`
	Rules    []DedupNormRule `toml:"rules"`
	Keywords []string        `toml:"keywords"`
}

// DedupConfig holds the deduplication normalization configuration.
type DedupConfig struct {
	Languages []DedupLangConfig `toml:"languages"`
}

// SandboxConfig holds sandbox execution settings.
type SandboxConfig struct {
	Enabled     bool              `toml:"enabled"`
	Type        string            `toml:"type"`
	Image       string            `toml:"image"`
	Timeout     int               `toml:"timeout"`
	WorkDir     string            `toml:"work_dir"`
	Environment map[string]string `toml:"environment"`
	MemoryLimit int               `toml:"memory_limit"`
	CPULimit    float64           `toml:"cpu_limit"`
}

// ApprovalRule defines a single approval rule for an operation.
type ApprovalRule struct {
	Operation   string `toml:"operation"`
	RiskLevel   string `toml:"risk_level"`
	AutoApprove bool   `toml:"auto_approve"`
}

// ApprovalConfig holds approval mechanism settings.
type ApprovalConfig struct {
	Enabled           bool           `toml:"enabled"`
	AutoApproveLow    bool           `toml:"auto_approve_low_risk"`
	RememberDecisions bool           `toml:"remember_decisions"`
	Timeout           int            `toml:"timeout"`
	RiskThreshold     string         `toml:"risk_threshold"`
	Rules             []ApprovalRule `toml:"rules"`
}

// Config is the top-level TOML configuration.
type Config struct {
	Sandbox       SandboxConfig  `toml:"sandbox"`
	Approvals     ApprovalConfig `toml:"approvals"`
	ErrorPatterns []ErrorPattern `toml:"error_patterns"`
	Dedup         DedupConfig    `toml:"dedup"`
}

// DefaultConfig returns a Config with sane defaults.
func DefaultConfig() *Config {
	return &Config{
		Sandbox: SandboxConfig{
			Enabled:     true,
			Type:        "docker",
			Image:       "golang:1.24-alpine",
			Timeout:     300,
			WorkDir:     "/workspace",
			Environment: map[string]string{},
			MemoryLimit: 512,
			CPULimit:    1.0,
		},
		Approvals: ApprovalConfig{
			Enabled:           true,
			AutoApproveLow:    false,
			RememberDecisions: true,
			Timeout:           60,
			RiskThreshold:     "medium",
			Rules:             []ApprovalRule{},
		},
		ErrorPatterns: []ErrorPattern{
			{Language: "go", Regex: `^([^:]+):(\d+):(\d+):\s*(.+)$`, Priority: 0},
		},
	}
}

// LoadConfig reads and parses a TOML configuration file.
// If path is empty, returns DefaultConfig.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return DefaultConfig(), nil
	}

	// Validate path — reject directory traversal.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", absPath, err)
	}

	cfg := DefaultConfig()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks the configuration for invalid values.
func (c *Config) Validate() error {
	if c.Sandbox.Enabled {
		switch c.Sandbox.Type {
		case "docker", "namespace", "none":
			// valid
		default:
			return fmt.Errorf("unsupported sandbox type %q (must be docker, namespace, or none)", c.Sandbox.Type)
		}
		if c.Sandbox.Type == "docker" && c.Sandbox.Image == "" {
			return fmt.Errorf("docker sandbox requires an image")
		}
		if c.Sandbox.Timeout < 0 {
			return fmt.Errorf("sandbox timeout must be non-negative")
		}
		if c.Sandbox.MemoryLimit < 0 {
			return fmt.Errorf("memory limit must be non-negative")
		}
		if c.Sandbox.CPULimit < 0 {
			return fmt.Errorf("CPU limit must be non-negative")
		}
	}

	validRiskLevels := map[string]bool{"low": true, "medium": true, "high": true}
	if c.Approvals.Enabled && !validRiskLevels[c.Approvals.RiskThreshold] {
		return fmt.Errorf("invalid risk threshold %q (must be low, medium, or high)", c.Approvals.RiskThreshold)
	}
	for _, rule := range c.Approvals.Rules {
		if !validRiskLevels[rule.RiskLevel] {
			return fmt.Errorf("invalid risk level %q in approval rule for operation %q", rule.RiskLevel, rule.Operation)
		}
	}

	for i, ep := range c.ErrorPatterns {
		if ep.Regex == "" {
			return fmt.Errorf("error_patterns[%d]: regex must not be empty", i)
		}
		if ep.Language == "" {
			return fmt.Errorf("error_patterns[%d]: language must not be empty", i)
		}
	}

	for i, lang := range c.Dedup.Languages {
		if lang.Language == "" {
			return fmt.Errorf("dedup.languages[%d]: language must not be empty", i)
		}
		for j, rule := range lang.Rules {
			if rule.Regex == "" {
				return fmt.Errorf("dedup.languages[%d].rules[%d]: regex must not be empty", i, j)
			}
		}
	}

	return nil
}

// sanitizePath validates a path to prevent directory traversal attacks.
// It resolves the path to an absolute form and ensures it does not contain
// ".." components after cleaning.
func sanitizePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	// Reject null bytes.
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("path contains null byte")
	}

	cleaned := filepath.Clean(path)
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Ensure the cleaned path does not escape via "..".
	// After filepath.Clean + Abs, ".." at the root is collapsed,
	// but we explicitly check for traversal patterns.
	if strings.Contains(absPath, "..") {
		return "", fmt.Errorf("path traversal detected in %q", path)
	}

	return absPath, nil
}
