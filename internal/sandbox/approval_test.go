package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

// mockPrompt returns a PromptFunc that yields the given responses in sequence.
func mockPrompt(responses ...string) PromptFunc {
	idx := 0
	return func(_ string) (string, error) {
		if idx >= len(responses) {
			return "", fmt.Errorf("no more mock responses")
		}
		resp := responses[idx]
		idx++
		return resp, nil
	}
}

// mockPromptError returns a PromptFunc that always returns an error.
func mockPromptError(err error) PromptFunc {
	return func(_ string) (string, error) {
		return "", err
	}
}

func silentStderr() {
	SetStderr(&strings.Builder{})
}

func TestApprovalGate_DisabledConfig(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: false}
	gate := NewApprovalGate(cfg, nil)

	result := gate.Check([]string{"rm", "-rf", "/"})
	if !result.Allowed {
		t.Error("expected allowed when approvals disabled")
	}
}

func TestApprovalGate_EmptyCommand(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, nil)

	result := gate.Check([]string{})
	if result.Allowed {
		t.Error("expected denied for empty command")
	}
	if result.Decision != DecisionDeny {
		t.Errorf("decision = %d, want DecisionDeny", result.Decision)
	}
}

func TestApprovalGate_LowRisk_BelowThreshold(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, nil)

	// "go build" is low risk, below medium threshold.
	result := gate.Check([]string{"go", "build", "./..."})
	if !result.Allowed {
		t.Errorf("expected allowed for low-risk command below threshold: %s", result.Reason)
	}
}

func TestApprovalGate_MediumRisk_AtThreshold(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	// "curl" is medium risk, at threshold — should prompt.
	result := gate.Check([]string{"curl", "https://example.com"})
	if !result.Allowed {
		t.Errorf("expected allowed after user approval: %s", result.Reason)
	}
	if result.Decision != DecisionApprove {
		t.Errorf("decision = %d, want DecisionApprove", result.Decision)
	}
}

func TestApprovalGate_HighRisk_PromptsAndDenies(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPrompt("n"))

	// "rm" is high risk — should prompt and deny.
	result := gate.Check([]string{"rm", "-rf", "/tmp/test"})
	if result.Allowed {
		t.Error("expected denied when user says 'n'")
	}
	if result.Decision != DecisionDeny {
		t.Errorf("decision = %d, want DecisionDeny", result.Decision)
	}
}

func TestApprovalGate_HighRisk_PromptsAndApproves(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	result := gate.Check([]string{"rm", "-rf", "/tmp/test"})
	if !result.Allowed {
		t.Errorf("expected allowed when user says 'y': %s", result.Reason)
	}
	if result.Decision != DecisionApprove {
		t.Errorf("decision = %d, want DecisionApprove", result.Decision)
	}
}

func TestApprovalGate_ApproveAll_RemembersDecision(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:           true,
		RiskThreshold:     "medium",
		RememberDecisions: true,
	}
	gate := NewApprovalGate(cfg, mockPrompt("a"))

	// First call prompts.
	result := gate.Check([]string{"rm", "-rf", "/tmp/test"})
	if !result.Allowed {
		t.Fatalf("expected allowed: %s", result.Reason)
	}
	if result.Decision != DecisionApproveAll {
		t.Errorf("decision = %d, want DecisionApproveAll", result.Decision)
	}

	// Second call should auto-approve without prompting.
	result = gate.Check([]string{"sudo", "rm", "-rf", "/"})
	if !result.Allowed {
		t.Errorf("expected auto-approved: %s", result.Reason)
	}
	if result.Decision != DecisionApproveAll {
		t.Errorf("decision = %d, want DecisionApproveAll (remembered)", result.Decision)
	}
}

func TestApprovalGate_ApproveAll_RemembersByRiskLevel(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:           true,
		RiskThreshold:     "medium",
		RememberDecisions: true,
	}
	// Approve all for the first high-risk command.
	gate := NewApprovalGate(cfg, mockPrompt("a"))

	gate.Check([]string{"rm", "-rf", "/tmp"})

	// High-risk should be remembered.
	result := gate.Check([]string{"sudo", "chmod", "777", "/etc"})
	if !result.Allowed {
		t.Errorf("high-risk should be remembered: %s", result.Reason)
	}

	// Medium-risk should NOT be remembered (different risk level).
	result = gate.Check([]string{"curl", "https://example.com"})
	// Since there's no prompt function left (mockPrompt exhausted), it should deny.
	if result.Allowed {
		t.Error("medium-risk should not be auto-approved by high-risk remember")
	}
}

func TestApprovalGate_RememberDecisions_Disabled(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:           true,
		RiskThreshold:     "medium",
		RememberDecisions: false,
	}
	gate := NewApprovalGate(cfg, mockPrompt("a", "n"))

	// First call: user says "approve all".
	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if !result.Allowed {
		t.Fatalf("expected allowed: %s", result.Reason)
	}

	// Second call: should NOT be remembered, should prompt again.
	result = gate.Check([]string{"rm", "-rf", "/tmp2"})
	if result.Allowed {
		t.Error("expected denied when remember_decisions=false and user says 'n'")
	}
}

func TestApprovalGate_AutoApproveRule(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:       true,
		RiskThreshold: "medium",
		Rules: []ApprovalRule{
			{Operation: "file_delete", RiskLevel: "high", AutoApprove: true},
		},
	}
	gate := NewApprovalGate(cfg, nil)

	// "rm" triggers file_delete, which has auto_approve=true.
	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if !result.Allowed {
		t.Errorf("expected auto-approved by rule: %s", result.Reason)
	}
}

func TestApprovalGate_AutoApproveLowRisk(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:        true,
		RiskThreshold:  "high", // only high-risk needs approval
		AutoApproveLow: true,
	}
	gate := NewApprovalGate(cfg, nil)

	// "go" is low risk, auto_approve_low_risk=true, threshold=high.
	result := gate.Check([]string{"go", "build", "./..."})
	if !result.Allowed {
		t.Errorf("expected allowed: %s", result.Reason)
	}
}

func TestApprovalGate_NoPromptFunc_DeniesHighRisk(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, nil) // nil prompt function

	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if result.Allowed {
		t.Error("expected denied when no prompt function for high-risk command")
	}
}

func TestApprovalGate_NoPromptFunc_AllowsLowRisk(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "high"}
	gate := NewApprovalGate(cfg, nil)

	// "go" is low risk, below high threshold.
	result := gate.Check([]string{"go", "test", "./..."})
	if !result.Allowed {
		t.Errorf("expected allowed for low-risk: %s", result.Reason)
	}
}

func TestApprovalGate_UnknownCommand_LowRisk(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, nil)

	// "myapp" is not in known patterns — classified as low risk.
	result := gate.Check([]string{"myapp", "--flag"})
	if !result.Allowed {
		t.Errorf("expected allowed for unknown command: %s", result.Reason)
	}
}

func TestApprovalGate_CommandWithPath(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	// Command with path prefix should still match.
	result := gate.Check([]string{"/usr/bin/rm", "-rf", "/tmp"})
	if !result.Allowed {
		t.Errorf("expected allowed: %s", result.Reason)
	}
}

func TestApprovalGate_WindowsPathCommand(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	result := gate.Check([]string{`C:\Windows\System32\cmd.exe`, "/c", "del", "file.txt"})
	if !result.Allowed {
		t.Errorf("expected allowed: %s", result.Reason)
	}
}

func TestApprovalGate_PromptError_DeniesCommand(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, mockPromptError(fmt.Errorf("input error")))

	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if result.Allowed {
		t.Error("expected denied when prompt returns error")
	}
}

func TestApprovalGate_PromptEOF_DeniesCommand(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	// Empty string simulates EOF-like behavior.
	gate := NewApprovalGate(cfg, mockPrompt(""))

	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if result.Allowed {
		t.Error("expected denied on empty input")
	}
}

func TestApprovalGate_YesVariants(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}

	for _, input := range []string{"y", "Y", "yes", "YES", "Yes"} {
		gate := NewApprovalGate(cfg, mockPrompt(input))
		result := gate.Check([]string{"rm", "-rf", "/tmp"})
		if !result.Allowed {
			t.Errorf("input %q: expected allowed", input)
		}
	}
}

func TestApprovalGate_AllVariants(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:           true,
		RiskThreshold:     "medium",
		RememberDecisions: true,
	}

	for _, input := range []string{"a", "A", "all", "ALL"} {
		gate := NewApprovalGate(cfg, mockPrompt(input))
		result := gate.Check([]string{"rm", "-rf", "/tmp"})
		if !result.Allowed {
			t.Errorf("input %q: expected allowed", input)
		}
		// Verify it was remembered.
		result = gate.Check([]string{"sudo", "rm", "-rf", "/"})
		if !result.Allowed {
			t.Errorf("input %q: expected remembered approval", input)
		}
	}
}

func TestApprovalGate_NoVariants(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}

	for _, input := range []string{"n", "N", "no", "NO", "No", "anything", ""} {
		gate := NewApprovalGate(cfg, mockPrompt(input))
		result := gate.Check([]string{"rm", "-rf", "/tmp"})
		if result.Allowed {
			t.Errorf("input %q: expected denied", input)
		}
	}
}

func TestApprovalGate_MultipleSequentialCommands(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{
		Enabled:           true,
		RiskThreshold:     "medium",
		RememberDecisions: true,
	}
	gate := NewApprovalGate(cfg, mockPrompt("y", "n", "a", "y"))

	// 1. go build — low risk, no prompt needed.
	r := gate.Check([]string{"go", "build", "./..."})
	if !r.Allowed {
		t.Fatalf("go build: %s", r.Reason)
	}

	// 2. rm — high risk, user says "y".
	r = gate.Check([]string{"rm", "-rf", "/tmp/a"})
	if !r.Allowed {
		t.Fatalf("rm: %s", r.Reason)
	}

	// 3. sudo — high risk, user says "n".
	r = gate.Check([]string{"sudo", "reboot"})
	if r.Allowed {
		t.Fatal("sudo: expected denied")
	}

	// 4. curl — medium risk, user says "a".
	r = gate.Check([]string{"curl", "https://example.com"})
	if !r.Allowed {
		t.Fatalf("curl: %s", r.Reason)
	}

	// 5. wget — medium risk, should be auto-approved (remembered).
	r = gate.Check([]string{"wget", "https://example.com"})
	if !r.Allowed {
		t.Fatalf("wget: expected auto-approved: %s", r.Reason)
	}
}

func TestApprovalGate_HighRiskThreshold(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "high"}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	// Medium-risk should pass without prompt.
	r := gate.Check([]string{"curl", "https://example.com"})
	if !r.Allowed {
		t.Errorf("medium below high threshold: %s", r.Reason)
	}

	// High-risk should prompt.
	r = gate.Check([]string{"rm", "-rf", "/tmp"})
	if !r.Allowed {
		t.Errorf("high at threshold: %s", r.Reason)
	}
}

func TestApprovalGate_LowRiskThreshold(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "low"}
	gate := NewApprovalGate(cfg, mockPrompt("y", "y", "y"))

	// Low-risk should prompt.
	r := gate.Check([]string{"go", "build", "./..."})
	if !r.Allowed {
		t.Errorf("low at threshold: %s", r.Reason)
	}

	// Medium-risk should prompt.
	r = gate.Check([]string{"curl", "https://example.com"})
	if !r.Allowed {
		t.Errorf("medium above threshold: %s", r.Reason)
	}

	// High-risk should prompt.
	r = gate.Check([]string{"rm", "-rf", "/tmp"})
	if !r.Allowed {
		t.Errorf("high above threshold: %s", r.Reason)
	}
}

func TestClassifyCommand_KnownPatterns(t *testing.T) {
	silentStderr()
	cfg := &ApprovalConfig{Enabled: true, RiskThreshold: "medium"}
	gate := NewApprovalGate(cfg, nil)

	tests := []struct {
		command  []string
		wantOp   string
		wantRisk riskLevel
	}{
		{[]string{"rm", "-rf", "/tmp"}, "file_delete", riskHigh},
		{[]string{"curl", "url"}, "network_request", riskMedium},
		{[]string{"sudo", "apt", "install"}, "system_command", riskHigh},
		{[]string{"go", "build"}, "code_execution", riskLow},
		{[]string{"npm", "install"}, "package_install", riskMedium},
		{[]string{"python3", "script.py"}, "code_execution", riskMedium},
		{[]string{"unknown_cmd"}, "unknown", riskLow},
	}

	for _, tt := range tests {
		op, risk := gate.classifyCommand(commandName(tt.command), tt.command)
		if op != tt.wantOp {
			t.Errorf("classifyCommand(%v): operation = %q, want %q", tt.command, op, tt.wantOp)
		}
		if risk != tt.wantRisk {
			t.Errorf("classifyCommand(%v): risk = %d, want %d", tt.command, risk, tt.wantRisk)
		}
	}
}

func TestCommandName(t *testing.T) {
	tests := []struct {
		command []string
		want    string
	}{
		{[]string{"rm", "-rf", "/tmp"}, "rm"},
		{[]string{"/usr/bin/rm", "-rf"}, "rm"},
		{[]string{`C:\Windows\cmd.exe`, "/c"}, "cmd.exe"},
		{[]string{}, ""},
		{[]string{"go"}, "go"},
	}
	for _, tt := range tests {
		got := commandName(tt.command)
		if got != tt.want {
			t.Errorf("commandName(%v) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestRiskString(t *testing.T) {
	if riskString(riskLow) != "low" {
		t.Errorf("riskString(low) = %q", riskString(riskLow))
	}
	if riskString(riskMedium) != "medium" {
		t.Errorf("riskString(medium) = %q", riskString(riskMedium))
	}
	if riskString(riskHigh) != "high" {
		t.Errorf("riskString(high) = %q", riskString(riskHigh))
	}
	if riskString(riskLevel(99)) != "unknown" {
		t.Errorf("riskString(99) = %q", riskString(riskLevel(99)))
	}
}

func TestConfigRuleOverridesPatternRisk(t *testing.T) {
	silentStderr()
	// Config says file_delete is "medium", but the pattern says "high".
	// Config risk is used when it's higher than pattern risk.
	cfg := &ApprovalConfig{
		Enabled:       true,
		RiskThreshold: "medium",
		Rules: []ApprovalRule{
			{Operation: "file_delete", RiskLevel: "high", AutoApprove: false},
		},
	}
	gate := NewApprovalGate(cfg, mockPrompt("y"))

	// "rm" triggers file_delete. Config says high, pattern says high → high.
	result := gate.Check([]string{"rm", "-rf", "/tmp"})
	if !result.Allowed {
		t.Errorf("expected allowed: %s", result.Reason)
	}
}

func TestNewConsolePrompt(t *testing.T) {
	input := "y\nn\na\n"
	reader := strings.NewReader(input)
	prompt := NewConsolePrompt(reader)

	val, err := prompt("")
	if err != nil || val != "y" {
		t.Errorf("first read: got %q, err=%v", val, err)
	}

	val, err = prompt("")
	if err != nil || val != "n" {
		t.Errorf("second read: got %q, err=%v", val, err)
	}

	val, err = prompt("")
	if err != nil || val != "a" {
		t.Errorf("third read: got %q, err=%v", val, err)
	}

	// EOF
	_, err = prompt("")
	if err == nil {
		t.Error("expected EOF error")
	}
}

func TestMatchesPattern(t *testing.T) {
	if !matchesPattern("rm", "rm") {
		t.Error("expected exact match")
	}
	if matchesPattern("rmdir", "rm") {
		t.Error("expected no partial match")
	}
}
