package sandbox

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// SandboxLauncher runs commands in a sandboxed environment.
type SandboxLauncher interface {
	// Run executes the command in the sandbox and streams output to the writer.
	// Returns the exit code and any error.
	Run(ctx context.Context, cfg RunConfig, output io.Writer) (int, error)

	// Close releases resources held by the launcher.
	Close() error
}

// RunConfig holds parameters for a single sandbox execution.
type RunConfig struct {
	Command []string          // Command and arguments to execute
	WorkDir string            // Host working directory (mounted read-only into container)
	Env     map[string]string // Additional environment variables
	Image   string            // Docker image override (empty = use config default)
}

// sanitize validates and cleans the run configuration.
func (rc RunConfig) sanitize() (RunConfig, error) {
	if len(rc.Command) == 0 {
		return rc, fmt.Errorf("empty command")
	}

	sanitized := RunConfig{
		Command: rc.Command,
		Env:     rc.Env,
		Image:   rc.Image,
	}

	if rc.WorkDir != "" {
		cleaned, err := sanitizePath(rc.WorkDir)
		if err != nil {
			return rc, fmt.Errorf("invalid work directory: %w", err)
		}
		sanitized.WorkDir = cleaned
	}

	// Validate environment variable keys — reject shell metacharacters.
	for key := range rc.Env {
		if strings.ContainsAny(key, "=;\n\r$`\\'\"") {
			return rc, fmt.Errorf("invalid environment variable key: %q", key)
		}
	}

	return sanitized, nil
}

// DockerLauncher implements SandboxLauncher using the Docker CLI.
// Security measures enforced via Docker CLI flags:
//   - Read-only bind mount (--read-only + volume :ro)
//   - Network disabled (--network=none)
//   - All capabilities dropped (--cap-drop=ALL)
//   - Memory and CPU limits (--memory, --cpus)
//   - Paths sanitized to prevent traversal
type DockerLauncher struct {
	config      *SandboxConfig
	imagePulled bool
}

// NewDockerLauncher creates a new Docker-based sandbox launcher.
func NewDockerLauncher(cfg *SandboxConfig) (*DockerLauncher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sandbox config is required")
	}
	return &DockerLauncher{config: cfg}, nil
}

// ensureImage pulls the Docker image if not already done.
func (l *DockerLauncher) ensureImage(ctx context.Context, img string) error {
	if l.imagePulled {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "pull", img)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pull image %s: %w\n%s", img, err, string(output))
	}

	l.imagePulled = true
	return nil
}

// Run executes the command inside a Docker container.
func (l *DockerLauncher) Run(ctx context.Context, runCfg RunConfig, output io.Writer) (int, error) {
	runCfg, err := runCfg.sanitize()
	if err != nil {
		return -1, fmt.Errorf("invalid run config: %w", err)
	}

	img := runCfg.Image
	if img == "" {
		img = l.config.Image
	}

	if err := l.ensureImage(ctx, img); err != nil {
		return -1, err
	}

	// Build docker run arguments.
	// Security: --read-only, --network=none, --cap-drop=ALL
	args := []string{
		"run",
		"--rm",           // Remove container on exit
		"--read-only",    // Read-only root filesystem
		"--network=none", // Disable network access
		"--cap-drop=ALL", // Drop all Linux capabilities
	}

	// Resource limits.
	if l.config.MemoryLimit > 0 {
		args = append(args, fmt.Sprintf("--memory=%dm", l.config.MemoryLimit))
	}
	if l.config.CPULimit > 0 {
		args = append(args, fmt.Sprintf("--cpus=%.1f", l.config.CPULimit))
	}

	// Environment variables.
	for k, v := range l.config.Environment {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range runCfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Bind mount working directory as read-only.
	if runCfg.WorkDir != "" {
		cWorkDir := containerPath(runCfg.WorkDir)
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro", runCfg.WorkDir, cWorkDir))
		args = append(args, "-w", cWorkDir)
	}

	// Image and command.
	args = append(args, img)
	args = append(args, runCfg.Command...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), err
		}
		return -1, fmt.Errorf("docker run: %w", err)
	}

	return 0, nil
}

// Close is a no-op for the CLI-based launcher.
func (l *DockerLauncher) Close() error {
	return nil
}

// NewSandboxLauncher creates a SandboxLauncher based on the configuration.
func NewSandboxLauncher(cfg *SandboxConfig) (SandboxLauncher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sandbox config is required")
	}

	switch cfg.Type {
	case "docker":
		return NewDockerLauncher(cfg)
	case "none":
		return NewNativeLauncher(), nil
	default:
		return nil, fmt.Errorf("unsupported sandbox type: %s", cfg.Type)
	}
}
