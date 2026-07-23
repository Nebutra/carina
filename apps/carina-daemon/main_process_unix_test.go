//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

func TestSIGTERMPersistsStoppedRuntimeDescriptor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping real daemon process test")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target", "release", "carina-kernel-service"),
		filepath.Join(repoRoot, "target", "debug", "carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}

	binDir := t.TempDir()
	daemonBin := filepath.Join(binDir, "carina-daemon")
	build := exec.Command("go", "build", "-o", daemonBin, "./apps/carina-daemon")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build carina-daemon: %v\n%s", err, output)
	}

	testRoot, err := os.MkdirTemp("/tmp", "carina-sigterm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	home := filepath.Join(testRoot, "home")
	workspaceRoot := filepath.Join(testRoot, "workspace")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	resolution, err := localruntime.ResolveWithManaged(
		home,
		workspaceRoot,
		localruntime.ModeWorkspace,
		filepath.Join(home, "missing-managed.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	cmd := exec.Command(daemonBin,
		"-runtime-spec", resolution.Spec.Paths.SpecPath,
		"-kernel", kernelBin,
		"-offline",
	)
	cmd.Dir = workspaceRoot
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitForRuntimeSocket(t, resolution.Spec.Paths.SocketPath, &logs)
	firstRunning, err := localruntime.LoadDescriptor(resolution.Spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatalf("load first running descriptor: %v\n%s", err, logs.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal daemon: %v\n%s", err, logs.String())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait daemon: %v\n%s", err, logs.String())
	}
	waited = true

	descriptor, err := localruntime.LoadDescriptor(resolution.Spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatalf("load stopped descriptor: %v\n%s", err, logs.String())
	}
	if descriptor.Lifecycle != localruntime.LifecycleStopped || descriptor.StoppedAt == nil {
		t.Fatalf("descriptor after SIGTERM = %+v\n%s", descriptor, logs.String())
	}

	var restartLogs bytes.Buffer
	restarted := exec.Command(daemonBin,
		"-runtime-spec", resolution.Spec.Paths.SpecPath,
		"-kernel", kernelBin,
		"-offline",
	)
	restarted.Dir = workspaceRoot
	restarted.Env = append(os.Environ(), "HOME="+home)
	restarted.Stdout = &restartLogs
	restarted.Stderr = &restartLogs
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	restartWaited := false
	t.Cleanup(func() {
		if restartWaited {
			return
		}
		_ = restarted.Process.Kill()
		_, _ = restarted.Process.Wait()
	})
	waitForRuntimeSocket(t, resolution.Spec.Paths.SocketPath, &restartLogs)
	secondRunning, err := localruntime.LoadDescriptor(resolution.Spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatalf("load restarted descriptor: %v\n%s", err, restartLogs.String())
	}
	if secondRunning.RuntimeID != firstRunning.RuntimeID {
		t.Fatalf("runtime ID changed across restart: %s -> %s", firstRunning.RuntimeID, secondRunning.RuntimeID)
	}
	if secondRunning.Epoch == "" || secondRunning.Epoch == firstRunning.Epoch {
		t.Fatalf("process epoch did not change across restart: %q -> %q", firstRunning.Epoch, secondRunning.Epoch)
	}
	if err := restarted.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal restarted daemon: %v\n%s", err, restartLogs.String())
	}
	if err := restarted.Wait(); err != nil {
		t.Fatalf("wait restarted daemon: %v\n%s", err, restartLogs.String())
	}
	restartWaited = true
	secondStopped, err := localruntime.LoadDescriptor(resolution.Spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatalf("load restarted stopped descriptor: %v\n%s", err, restartLogs.String())
	}
	if secondStopped.Lifecycle != localruntime.LifecycleStopped || secondStopped.StoppedAt == nil {
		t.Fatalf("restarted descriptor after SIGTERM = %+v\n%s", secondStopped, restartLogs.String())
	}
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if info, err := os.Stat(absolute); err == nil && !info.IsDir() {
			return absolute
		}
	}
	return ""
}

func waitForRuntimeSocket(t *testing.T, path string, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := rpc.Dial(path)
		if err == nil {
			_ = client.Close()
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("runtime socket did not become reachable: %v\n%s", lastErr, logs.String())
}
