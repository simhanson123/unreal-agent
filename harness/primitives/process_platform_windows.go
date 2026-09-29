package primitives

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Handles never enter persisted state. The lock protects handle use against close.
var processJobs = struct {
	sync.Mutex
	jobs map[*os.Process]windows.Handle
}{jobs: make(map[*os.Process]windows.Handle)}

func configurePlatformProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

func startPlatformProcess(command *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create process job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("configure process job: %w", err)
	}
	if err := command.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}
	fail := func(err error) error {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = windows.CloseHandle(job)
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("open suspended process: %w", err))
	}
	err = windows.AssignProcessToJobObject(job, handle)
	windows.CloseHandle(handle)
	if err != nil {
		return fail(fmt.Errorf("assign suspended process to job: %w", err))
	}
	// No user code can run (and spawn untracked children) before job assignment.
	if err := resumeInitialThread(uint32(command.Process.Pid)); err != nil {
		return fail(err)
	}
	processJobs.Lock()
	processJobs.jobs[command.Process] = job
	processJobs.Unlock()
	return nil
}

func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot suspended thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("open suspended thread: %w", err)
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("find suspended thread for process %d: %w", pid, err)
}

func releasePlatformProcess(process *os.Process) {
	processJobs.Lock()
	defer processJobs.Unlock()
	if job, ok := processJobs.jobs[process]; ok {
		delete(processJobs.jobs, process)
		_ = windows.CloseHandle(job)
	}
}

func processInvocationExists(process *os.Process) (bool, error) {
	processJobs.Lock()
	defer processJobs.Unlock()
	_, exists := processJobs.jobs[process]
	return exists, nil
}

func signalProcessInvocation(process *os.Process, signal syscall.Signal) error {
	if signal != syscall.SIGTERM && signal != syscall.SIGKILL {
		return fmt.Errorf("Windows process trees support termination only, not signal %d", signal)
	}
	processJobs.Lock()
	defer processJobs.Unlock()
	job, ok := processJobs.jobs[process]
	if !ok {
		return nil
	}
	return windows.TerminateJobObject(job, 1)
}

func terminateProcess(process *os.Process, pipes processParentPipes, grace time.Duration) error {
	// Windows has no general SIGTERM equivalent. Terminate the job, not just its leader.
	killErr := signalProcessInvocation(process, syscall.SIGKILL)
	waitErr := waitForJobEmpty(process, grace)
	return errors.Join(killErr, waitErr, pipes.closeAll())
}

// Layout from JOBOBJECT_BASIC_ACCOUNTING_INFORMATION in winnt.h.
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime               int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses uint32
}

func waitForJobEmpty(process *os.Process, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		processJobs.Lock()
		job, ok := processJobs.jobs[process]
		var info jobAccounting
		var err error
		if ok {
			err = windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
				uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
		}
		processJobs.Unlock()
		if err != nil {
			return fmt.Errorf("wait for process job: %w", err)
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("process job did not finish termination before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func openPlatformCapture(path string) (*os.File, error) {
	if err := validateWindowsFilePath(path); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Add the extended prefix internally only after rejecting device-path input.
	absolute = strings.ReplaceAll(absolute, "/", `\`)
	if strings.HasPrefix(absolute, `\\`) {
		absolute = `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`)
	} else {
		absolute = `\\?\` + absolute
	}
	name, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("capture must be a regular file, not a reparse point")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func preparePlatformCapture(_ *os.File) error { return nil }

func finishPlatformOutput(_ *os.File, _ time.Time) error {
	// Job termination closes all inherited writers. Let readers drain to EOF;
	// anonymous Windows pipes do not implement read deadlines.
	return nil
}

func drainProcessOutput(_ context.Context, _ ProcessStartRequest, _ ProcessStream,
	_ *os.File, _ *int64, _ chan<- PrimitiveEvent) error {
	return errors.New("unexpected read deadline on a Windows anonymous pipe")
}
