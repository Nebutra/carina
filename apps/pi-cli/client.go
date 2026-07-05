package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/TsekaLuk/pi-os/go/rpc"
)

type rpcClient = rpc.Client

func dialDaemon() (*rpcClient, error) {
	socket, err := defaultSocketPath()
	if err != nil {
		return nil, err
	}
	return rpc.Dial(socket)
}

func readAllStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// execTool runs a Zig native tool, transparently passing through stdio and
// the child's exit code. pi is a native launcher here (PRD §3.1/§8.1).
func execTool(tool string, args []string) error {
	bin := tool
	if dir := toolsDir(); dir != "" {
		bin = filepath.Join(dir, tool)
	}
	c := exec.Command(bin, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err := c.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	return err
}

// toolsDir locates the Zig tools: $PI_TOOLS_DIR, next to the pi binary, or
// the in-repo build output.
func toolsDir() string {
	if d := os.Getenv("PI_TOOLS_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "pi-scan")); err == nil {
			return dir
		}
	}
	for _, c := range []string{"zig/zig-out/bin", "../zig/zig-out/bin"} {
		if _, err := os.Stat(filepath.Join(c, "pi-scan")); err == nil {
			return c
		}
	}
	return ""
}
