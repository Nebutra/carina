//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Assign the child to a Job during CreateProcess, eliminating the escape
// window inherent in assigning an already-running process.
const procThreadAttributeJobList = 0x0002000d

type windowsExitError struct{ code int }

func (e windowsExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e windowsExitError) ExitCode() int { return e.code }

func runExecutorCommand(ctx context.Context, program string, args, env []string, stdin []byte, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executable, err := exec.LookPath(program)
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	executable16, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	commandLine16, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args...)))
	if err != nil {
		return err
	}
	envBlock, err := windowsEnvironmentBlock(env)
	if err != nil {
		return err
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return err
	}
	closeAll := func() {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
	}

	childHandles := []windows.Handle{windows.Handle(stdinR.Fd()), windows.Handle(stdoutW.Fd()), windows.Handle(stderrW.Fd())}
	for _, handle := range childHandles {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			closeAll()
			return err
		}
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		closeAll()
		return err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		closeAll()
		return err
	}

	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		windows.CloseHandle(job)
		closeAll()
		return err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&childHandles[0]), uintptr(len(childHandles))*unsafe.Sizeof(childHandles[0])); err != nil {
		windows.CloseHandle(job)
		closeAll()
		return err
	}
	jobs := []windows.Handle{job}
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		windows.CloseHandle(job)
		closeAll()
		return err
	}

	si := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  childHandles[0],
			StdOutput: childHandles[1],
			StdErr:    childHandles[2],
		},
		ProcThreadAttributeList: attributes.List(),
	}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_DEFAULT_ERROR_MODE | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(executable16, commandLine16, nil, nil, true, flags, &envBlock[0], nil, &si.StartupInfo, &process); err != nil {
		windows.CloseHandle(job)
		closeAll()
		return err
	}
	windows.CloseHandle(process.Thread)
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()

	var ioWG sync.WaitGroup
	ioWG.Add(3)
	go func() {
		defer ioWG.Done()
		_, _ = stdinW.Write(stdin)
		_ = stdinW.Close()
	}()
	go func() {
		defer ioWG.Done()
		_, _ = io.Copy(stdout, stdoutR)
		_ = stdoutR.Close()
	}()
	go func() {
		defer ioWG.Done()
		_, _ = io.Copy(stderr, stderrR)
		_ = stderrR.Close()
	}()

	waited := make(chan error, 1)
	go func() {
		result, waitErr := windows.WaitForSingleObject(process.Process, windows.INFINITE)
		if waitErr == nil && result != windows.WAIT_OBJECT_0 {
			waitErr = fmt.Errorf("unexpected process wait result %#x", result)
		}
		waited <- waitErr
	}()

	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		windows.CloseHandle(job)
		job = 0
		waitErr = <-waited
	}
	if job != 0 {
		windows.CloseHandle(job)
	}
	ioWG.Wait()
	if waitErr != nil {
		windows.CloseHandle(process.Process)
		return waitErr
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
		windows.CloseHandle(process.Process)
		return err
	}
	windows.CloseHandle(process.Process)
	if err := ctx.Err(); err != nil {
		return err
	}
	if exitCode != 0 {
		return windowsExitError{code: int(exitCode)}
	}
	return nil
}

func windowsEnvironmentBlock(env []string) ([]uint16, error) {
	copyEnv := append([]string(nil), env...)
	for _, entry := range copyEnv {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, fmt.Errorf("environment entry contains NUL")
		}
	}
	sort.Slice(copyEnv, func(i, j int) bool { return strings.ToLower(copyEnv[i]) < strings.ToLower(copyEnv[j]) })
	return utf16.Encode([]rune(strings.Join(copyEnv, "\x00") + "\x00\x00")), nil
}
