package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// riskLevel represents an ordered risk level.
type riskLevel int

const (
	riskLow    riskLevel = 0
	riskMedium riskLevel = 1
	riskHigh   riskLevel = 2
)

// riskPriority maps risk level strings to numeric priority.
var riskPriority = map[string]riskLevel{
	"low":    riskLow,
	"medium": riskMedium,
	"high":   riskHigh,
}

// operationPattern maps command patterns to canonical operation names
// and their inherent risk levels.
type operationPattern struct {
	pattern   string
	operation string
	risk      riskLevel
}

// knownPatterns defines recognized dangerous command patterns.
// The pattern is matched against the command name (first arg) case-insensitively.
var knownPatterns = []operationPattern{
	// File deletion — high risk
	{"rm", "file_delete", riskHigh},
	{"rmdir", "file_delete", riskHigh},
	{"del", "file_delete", riskHigh},
	{"shred", "file_delete", riskHigh},

	// Network operations — medium risk
	{"curl", "network_request", riskMedium},
	{"wget", "network_request", riskMedium},
	{"httpie", "network_request", riskMedium},

	// Package management — medium risk
	{"npm", "package_install", riskMedium},
	{"pip", "package_install", riskMedium},
	{"pip3", "package_install", riskMedium},
	{"gem", "package_install", riskMedium},
	{"cargo", "package_install", riskMedium},
	{"apt", "package_install", riskMedium},
	{"apt-get", "package_install", riskMedium},
	{"yum", "package_install", riskMedium},
	{"brew", "package_install", riskMedium},

	// System commands — high risk
	{"sudo", "system_command", riskHigh},
	{"chmod", "system_command", riskHigh},
	{"chown", "system_command", riskHigh},
	{"mount", "system_command", riskHigh},
	{"umount", "system_command", riskHigh},
	{"systemctl", "system_command", riskHigh},
	{"service", "system_command", riskHigh},

	// Code execution — medium risk
	{"python", "code_execution", riskMedium},
	{"python3", "code_execution", riskMedium},
	{"node", "code_execution", riskMedium},
	{"ruby", "code_execution", riskMedium},
	{"perl", "code_execution", riskMedium},

	// Build/test — low risk
	{"go", "code_execution", riskLow},
	{"make", "code_execution", riskLow},
	{"gcc", "code_execution", riskLow},
	{"g++", "code_execution", riskLow},
	{"javac", "code_execution", riskLow},
}

// ApprovalDecision represents the user's approval choice.
type ApprovalDecision int

const (
	// DecisionDeny rejects the operation; the command will not execute.
	DecisionDeny ApprovalDecision = iota
	// DecisionApprove allows this single operation to proceed.
	DecisionApprove
	// DecisionApproveAll allows this and all future operations at the same
	// risk level for the remainder of the session.
	DecisionApproveAll
)

// ApprovalResult is the outcome of an approval check.
type ApprovalResult struct {
	Allowed  bool             // Whether the command is allowed to proceed
	Decision ApprovalDecision // The decision that was made
	Reason   string           // Human-readable reason
}

// PromptFunc reads user input for approval prompts.
// Returns the raw line read and any error.
type PromptFunc func(prompt string) (string, error)

// ApprovalGate enforces the approval policy defined in ApprovalConfig.
type ApprovalGate struct {
	config   *ApprovalConfig
	promptFn PromptFunc

	// remembered maps risk levels that the user has approved via "approve all".
	// Only active when config.RememberDecisions is true.
	remembered map[riskLevel]bool
}

// NewApprovalGate creates a new ApprovalGate with the given config and prompt function.
// If promptFn is nil, interactive prompts are disabled (denied by default for high-risk).
func NewApprovalGate(config *ApprovalConfig, promptFn PromptFunc) *ApprovalGate {
	return &ApprovalGate{
		config:     config,
		promptFn:   promptFn,
		remembered: make(map[riskLevel]bool),
	}
}

// Check evaluates whether a command is allowed to execute.
// It returns an ApprovalResult indicating whether the command should proceed.
func (g *ApprovalGate) Check(command []string) ApprovalResult {
	if !g.config.Enabled {
		return ApprovalResult{Allowed: true, Decision: DecisionApprove, Reason: "approvals disabled"}
	}

	if len(command) == 0 {
		return ApprovalResult{Allowed: false, Decision: DecisionDeny, Reason: "empty command"}
	}

	cmdName := commandName(command)
	operation, risk := g.classifyCommand(cmdName, command)

	// Check auto_approve rules first.
	for _, rule := range g.config.Rules {
		if rule.Operation == operation && rule.AutoApprove {
			return ApprovalResult{
				Allowed:  true,
				Decision: DecisionApprove,
				Reason:   fmt.Sprintf("auto-approved by rule for operation %q", operation),
			}
		}
	}

	// Determine the effective risk threshold.
	threshold := riskPriority[g.config.RiskThreshold]
	if threshold == 0 && g.config.RiskThreshold == "" {
		threshold = riskMedium // default
	}

	// If risk is below the threshold, allow without prompting.
	if risk < threshold {
		return ApprovalResult{
			Allowed:  true,
			Decision: DecisionApprove,
			Reason:   fmt.Sprintf("risk level %s below threshold %s", riskString(risk), g.config.RiskThreshold),
		}
	}

	// Check if user previously approved all at this risk level.
	if g.config.RememberDecisions && g.remembered[risk] {
		return ApprovalResult{
			Allowed:  true,
			Decision: DecisionApproveAll,
			Reason:   fmt.Sprintf("previously approved all %s-risk operations", riskString(risk)),
		}
	}

	// Check if the risk level is "low" and auto_approve_low_risk is set.
	if risk == riskLow && g.config.AutoApproveLow {
		return ApprovalResult{
			Allowed:  true,
			Decision: DecisionApprove,
			Reason:   "auto-approved low-risk operation",
		}
	}

	// Need interactive approval.
	if g.promptFn == nil {
		// No prompt function — deny by default for operations at or above threshold.
		return ApprovalResult{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   fmt.Sprintf("no interactive prompt available; %s-risk operation %q denied", riskString(risk), operation),
		}
	}

	decision := g.promptForApproval(operation, risk, command)
	switch decision {
	case DecisionApprove:
		return ApprovalResult{
			Allowed:  true,
			Decision: DecisionApprove,
			Reason:   "user approved",
		}
	case DecisionApproveAll:
		if g.config.RememberDecisions {
			g.remembered[risk] = true
		}
		return ApprovalResult{
			Allowed:  true,
			Decision: DecisionApproveAll,
			Reason:   fmt.Sprintf("user approved all %s-risk operations", riskString(risk)),
		}
	default:
		return ApprovalResult{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   "user denied",
		}
	}
}

// promptForApproval displays the interactive approval prompt and reads the user's choice.
func (g *ApprovalGate) promptForApproval(operation string, risk riskLevel, command []string) ApprovalDecision {
	cmdStr := strings.Join(command, " ")
	fmt.Fprintf(osStderr(), "\n\033[41m\033[37m ⚠ 检测到高危操作 \033[0m\n")
	fmt.Fprintf(osStderr(), "\033[31m┌──────────────────────────────────────────────┐\033[0m\n")
	fmt.Fprintf(osStderr(), "\033[31m│\033[0m 操作类型: \033[1m%s\033[0m\n", operation)
	fmt.Fprintf(osStderr(), "\033[31m│\033[0m 风险等级: \033[1m%s\033[0m\n", riskColored(risk))
	fmt.Fprintf(osStderr(), "\033[31m│\033[0m 待执行命令: \033[33m%s\033[0m\n", cmdStr)
	fmt.Fprintf(osStderr(), "\033[31m└──────────────────────────────────────────────┘\033[0m\n")
	fmt.Fprintf(osStderr(), "\n  \033[1m[y]\033[0m 批准执行  \033[1m[n]\033[0m 拒绝执行  \033[1m[a]\033[0m 全部批准（同级风险）\n")
	fmt.Fprintf(osStderr(), "  您的选择: ")

	input, err := g.promptFn("")
	if err != nil {
		fmt.Fprintf(osStderr(), "\n\033[31m读取输入失败: %v，拒绝执行\033[0m\n", err)
		return DecisionDeny
	}

	switch strings.TrimSpace(strings.ToLower(input)) {
	case "y", "yes":
		return DecisionApprove
	case "a", "all":
		return DecisionApproveAll
	default:
		return DecisionDeny
	}
}

// classifyCommand determines the operation name and risk level for a command.
// It first checks explicit rules in the config, then falls back to known patterns.
func (g *ApprovalGate) classifyCommand(cmdName string, command []string) (string, riskLevel) {
	lowerCmd := strings.ToLower(cmdName)

	// First pass: check if any config rule's operation matches a known pattern
	// that this command triggers.
	for _, rule := range g.config.Rules {
		for _, pat := range knownPatterns {
			if pat.operation == rule.Operation && matchesPattern(lowerCmd, pat.pattern) {
				// Use the risk level from the config rule if it's higher.
				configRisk := riskPriority[rule.RiskLevel]
				if configRisk > pat.risk {
					return rule.Operation, configRisk
				}
				return rule.Operation, pat.risk
			}
		}
	}

	// Second pass: match against known patterns directly.
	for _, pat := range knownPatterns {
		if matchesPattern(lowerCmd, pat.pattern) {
			return pat.operation, pat.risk
		}
	}

	// Unknown command — classify as low risk.
	return "unknown", riskLow
}

// matchesPattern checks if a command name matches a pattern.
func matchesPattern(cmdName, pattern string) bool {
	return cmdName == pattern
}

// commandName extracts the base command name from a command slice.
func commandName(command []string) string {
	if len(command) == 0 {
		return ""
	}
	cmd := command[0]
	// Strip path prefix (e.g., /usr/bin/rm → rm).
	if idx := strings.LastIndexAny(cmd, "/\\"); idx >= 0 {
		cmd = cmd[idx+1:]
	}
	return cmd
}

// riskString returns a human-readable risk level string.
func riskString(r riskLevel) string {
	switch r {
	case riskLow:
		return "low"
	case riskMedium:
		return "medium"
	case riskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// riskColored returns a colored risk level string for terminal output.
func riskColored(r riskLevel) string {
	switch r {
	case riskHigh:
		return "\033[31m高危 (HIGH)\033[0m"
	case riskMedium:
		return "\033[33m中危 (MEDIUM)\033[0m"
	case riskLow:
		return "\033[32m低危 (LOW)\033[0m"
	default:
		return "未知"
	}
}

// osStderr returns stderr for output. Extracted for testability.
var osStderr = func() io.Writer {
	return io.Discard // default: silent (overridden in main and tests)
}

// SetStderr sets the writer used for approval prompts.
func SetStderr(w io.Writer) {
	osStderr = func() io.Writer { return w }
}

// NewConsolePrompt returns a PromptFunc that reads from the given reader.
func NewConsolePrompt(r io.Reader) PromptFunc {
	scanner := bufio.NewScanner(r)
	return func(_ string) (string, error) {
		if !scanner.Scan() {
			return "", fmt.Errorf("EOF")
		}
		return scanner.Text(), nil
	}
}
