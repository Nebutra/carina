package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

func cmdRuntime(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina runtime <start|status|stop|logs|mode>")
	}
	if args[0] == "mode" {
		return cmdRuntimeMode(args[1:])
	}
	fs := flag.NewFlagSet("carina runtime "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace path")
	jsonOutput := fs.Bool("json", false, "print JSON")
	force := fs.Bool("force", false, "stop despite active obligations")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	resolution, err := localruntime.Resolve(home, *workspace, localruntime.ModeWorkspace)
	if err != nil {
		return err
	}
	spec := resolution.Spec
	switch args[0] {
	case "start":
		client, description, err := localdaemon.ConnectOrStart(spec)
		if err != nil {
			return err
		}
		_ = client.Close()
		return printRuntimeDescription(description, *jsonOutput)
	case "status":
		client, description, err := localdaemon.Connect(spec)
		if err == nil {
			_ = client.Close()
			return printRuntimeDescription(description, *jsonOutput)
		}
		if !errors.Is(err, rpc.ErrDaemonUnreachable) {
			return err
		}
		descriptor, descriptorErr := localruntime.LoadDescriptor(spec.Paths.DescriptorPath)
		if descriptorErr != nil && !errors.Is(descriptorErr, os.ErrNotExist) {
			return descriptorErr
		}
		status := map[string]any{"live": false, "workspace": spec.Workspace, "runtime_id": spec.RuntimeID, "socket_path": spec.Paths.SocketPath, "state_dir": spec.Paths.StateDir}
		if descriptorErr == nil {
			status["descriptor"] = descriptor
		}
		if *jsonOutput {
			return printJSON(status)
		}
		fmt.Printf("workspace: %s\nruntime: %s\nstatus: stopped\nsocket: %s\nstate: %s\n", spec.Workspace.CanonicalRoot, spec.RuntimeID, spec.Paths.SocketPath, spec.Paths.StateDir)
		return nil
	case "stop":
		description, err := localdaemon.StopRuntime(spec, *force)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(map[string]any{"stop_requested": true, "runtime": description})
		}
		fmt.Printf("stop requested for workspace runtime %s (pid %d)\n", description.RuntimeID, description.PID)
		return nil
	case "logs":
		return printRuntimeLogs(spec.Paths.LogPath, os.Stdout)
	default:
		return fmt.Errorf("unknown runtime subcommand %q", args[0])
	}
}

func cmdRuntimeMode(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		mode, err := localruntime.ResolveMode(home)
		if err != nil {
			return err
		}
		fmt.Printf("mode: %s\nlegacy state present: %t\n", mode, localruntime.LegacyStatePresent(home))
		return nil
	}
	mode := localruntime.Mode(strings.ToLower(strings.TrimSpace(args[0])))
	if err := localruntime.WriteMode(home, mode); err != nil {
		return err
	}
	fmt.Printf("runtime mode set to %s\n", mode)
	return nil
}

func cmdRuntimes(args []string) error {
	fs := flag.NewFlagSet("carina runtimes", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	entries, err := localruntime.ScanRegistry(home)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(entries)
	}
	if len(entries) == 0 {
		fmt.Println("no workspace runtimes")
		return nil
	}
	for _, entry := range entries {
		if entry.Descriptor != nil {
			fmt.Printf("%s\t%s\t%s\n", entry.Descriptor.Lifecycle, entry.Descriptor.Workspace.CanonicalRoot, entry.Descriptor.RuntimeID)
			continue
		}
		if entry.Spec != nil {
			fmt.Printf("unknown\t%s\t%s\n", entry.Spec.Workspace.CanonicalRoot, entry.Spec.RuntimeID)
			continue
		}
		fmt.Printf("invalid\t%s\t%s\n", entry.RuntimeDir, entry.Error)
	}
	return nil
}

func printRuntimeDescription(description localdaemon.RuntimeDescription, jsonOutput bool) error {
	if jsonOutput {
		return printJSON(description)
	}
	fmt.Printf("workspace: %s\nruntime: %s\nstatus: %s\npid: %d\nepoch: %s\nsocket: %s\nstate: %s\n", description.WorkspaceRoot, description.RuntimeID, description.Lifecycle, description.PID, description.Epoch, description.SocketPath, description.StateDir)
	return nil
}

func printRuntimeLogs(path string, output io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read runtime logs %s: %w", path, err)
	}
	defer file.Close()
	const maxBytes = 256 << 10
	info, err := file.Stat()
	if err != nil {
		return err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if start > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	_, err = output.Write(data)
	return err
}
