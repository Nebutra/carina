//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Use a distinctive exit code so a CLI that naturally exits with status 1 is
// never mistaken for Carina's post-result Job termination.
const grokWindowsCarinaExitCode uint32 = 0xC0DECAFE

var grokWindowsJobs sync.Map // map[*exec.Cmd]windows.Handle

func startGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("nil Grok Build command")
	}
	job, err := createGrokWindowsJob()
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// The process cannot spawn an uncontained descendant before it enters the Job.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.Cancel = func() error { return terminateGrokCLIReasonerCommand(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		abortUncontainedGrokWindowsCommand(cmd, job)
		return fmt.Errorf("contain Grok Build process: %w", err)
	}

	grokWindowsJobs.Store(cmd, job)
	if err := resumeGrokWindowsProcess(uint32(cmd.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, grokWindowsCarinaExitCode)
		_ = cmd.Wait()
		releaseGrokCLIReasonerCommand(cmd)
		return fmt.Errorf("resume contained Grok Build process: %w", err)
	}
	return nil
}

func createGrokWindowsJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func resumeGrokWindowsProcess(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := false
	for {
		if entry.OwnerProcessID == processID {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			resumed = true
		}
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return err
		}
	}
	if !resumed {
		return errors.New("Grok Build process has no resumable thread")
	}
	return nil
}

func abortUncontainedGrokWindowsCommand(cmd *exec.Cmd, job windows.Handle) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	windows.CloseHandle(job)
}

func terminateGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	if job, ok := grokWindowsJobs.Load(cmd); ok {
		return windows.TerminateJobObject(job.(windows.Handle), grokWindowsCarinaExitCode)
	}
	return killCLIReasonerCommand(cmd)
}

func releaseGrokCLIReasonerCommand(cmd *exec.Cmd) {
	if job, ok := grokWindowsJobs.LoadAndDelete(cmd); ok {
		windows.CloseHandle(job.(windows.Handle))
	}
}

func grokProcessStateMatchesCarinaKill(state *os.ProcessState) bool {
	return uint32(state.ExitCode()) == grokWindowsCarinaExitCode
}
