package sandbox

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// NativeLauncher runs commands directly on the host without sandboxing.
// Used when sandbox.type = "none" in configuration.
type NativeLauncher struct{}

// NewNativeLauncher returns a new NativeLauncher.
func NewNativeLauncher() *NativeLauncher {
	return &NativeLauncher{}
}

// Run executes the command directly on the host.
func (l *NativeLauncher) Run(ctx context.Context, cfg RunConfig, output io.Writer) (int, error) {
	if len(cfg.Command) == 0 {
		return -1, fmt.Errorf("empty command")
	}

	var cmd *exec.Cmd
	if len(cfg.Command) == 1 {
		cmd = exec.CommandContext(ctx, cfg.Command[0])
	} else {
		cmd = exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	}

	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	// Build environment.
	if len(cfg.Env) > 0 {
		cmd.Env = make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), err
		}
		return -1, err
	}
	return 0, nil
}

// Close is a no-op for the native launcher.
func (l *NativeLauncher) Close() error {
	return nil
}
