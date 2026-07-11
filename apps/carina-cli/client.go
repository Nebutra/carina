package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Nebutra/carina/go/rpc"
)

type rpcClient = rpc.Client

// dialHook lets tests observe/replace the actual dial without touching a
// real socket. Production code always goes through initGate, the single
// seam allowed to call dialHook (P1.8 startup discipline).
var dialHook = func() (*rpcClient, error) {
	socket, err := defaultSocketPath()
	if err != nil {
		return nil, err
	}
	c, err := rpc.Dial(socket)
	if err != nil {
		return nil, err
	}
	var initialized map[string]any
	if err := c.Call("runtime.initialize", map[string]any{"protocol_version": "1.2.0", "schema_version": "1.2.0", "client_name": "carina-cli", "client_version": cliVersion}, &initialized); err != nil {
		var rpcErr *rpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeMethodNotFound {
			_ = c.Close()
			return nil, fmt.Errorf("runtime initialize: %w", err)
		}
	} else if err := validateCLIEventSchema(initialized); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func validateCLIEventSchema(info map[string]any) error {
	caps, _ := info["capabilities"].(map[string]any)
	v, _ := caps["event_schema_version"].(string)
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 || parts[0] != "0" || parts[1] != "3" {
		return fmt.Errorf("runtime initialize: incompatible event schema %q; require 0.3.x", v)
	}
	return nil
}

// dialDaemon is kept as a direct dial path for callers outside run()'s
// governed-command switch (e.g. future standalone tooling); run() itself
// goes through initGate so ungated commands never reach a dial.
func dialDaemon() (*rpcClient, error) { return dialHook() }

// ungatedCommands is the explicit allowlist of carina subcommands that must
// never touch the daemon socket, config, or kernel (P1.8 startup
// discipline): help/version/completion and the <100ms native passthrough.
// This is deliberately an explicit allowlist rather than a set of ad hoc
// early returns in run(), so it is impossible to add a new governed
// subcommand without deciding whether it belongs here.
var ungatedCommands = map[string]bool{
	"version": true, "--version": true, "-v": true,
	"help": true, "-h": true, "--help": true,
	"completion": true, "daemon": true,
	"scan": true, "grep": true, "diff": true, "pty": true,
	"run-native": true, "patch-native": true,
	"auth": true, "providers": true,
}

// initGate is the shared startup init-gate (P1.8 startup discipline): every
// governed subcommand's startup I/O joins here, and it is the ONLY place
// that may call dialHook. Commands in ungatedCommands return immediately
// without ever touching the socket, config, or kernel — help/version/
// completion and the <100ms native passthrough stay fast and offline.
//
// For governed commands, startup I/O fires as goroutines at the gate and is
// joined here rather than sequentially blocking one after another —
// goroutine pipelining (explicitly not the JS module-order side-effect
// hack, plan §5.9). Today the only real startup I/O is the daemon dial;
// this is the single seam a future policy-snapshot read or resume-file read
// joins by adding another goroutine + channel below, instead of being
// bolted on ad hoc inside an individual command handler.
func initGate(cmd string) (*rpcClient, error) {
	if ungatedCommands[cmd] {
		return nil, nil
	}
	// carina doctor's own kill-switch (P1.6): CARINA_DOCTOR_DISABLE must stop
	// doctor before it ever dials the daemon, not just suppress its probes
	// server-side — a locked-down deployment that sets this env wants zero
	// socket traffic from doctor, including the dial itself.
	if cmd == "doctor" && doctorDisabled(os.Getenv) {
		return nil, nil
	}

	type dialResult struct {
		client *rpcClient
		err    error
	}
	dialCh := make(chan dialResult, 1)
	go func() {
		c, err := dialHook()
		dialCh <- dialResult{client: c, err: err}
	}()

	// Additional startup I/O (policy snapshot, resume-file read) would fire
	// as its own goroutine here and be joined below, alongside dialCh, so
	// every governed subcommand pays for them concurrently rather than in
	// sequence.
	res := <-dialCh
	if res.err != nil {
		return nil, res.err
	}
	return res.client, nil
}

func readAllStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// execTool runs a Zig native tool, transparently passing through stdio and
// the child's exit code. carina is a native launcher here (PRD §3.1/§8.1).
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

// toolsDir locates the Zig tools: $CARINA_TOOLS_DIR, next to the carina binary,
// or the in-repo build output.
func toolsDir() string {
	if d := os.Getenv("CARINA_TOOLS_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "carina-scan")); err == nil {
			return dir
		}
	}
	for _, c := range []string{"zig/zig-out/bin", "../zig/zig-out/bin"} {
		if _, err := os.Stat(filepath.Join(c, "carina-scan")); err == nil {
			return c
		}
	}
	return ""
}
