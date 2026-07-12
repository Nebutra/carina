//go:build !darwin && !linux && !windows

package main

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

func runExecutorCommand(ctx context.Context, program string, args, env []string, stdin []byte, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
