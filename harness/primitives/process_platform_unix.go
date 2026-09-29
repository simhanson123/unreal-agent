//go:build unix

package primitives

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func configurePlatformProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startPlatformProcess(command *exec.Cmd) error { return command.Start() }
func releasePlatformProcess(_ *os.Process)         {}

func processInvocationExists(process *os.Process) (bool, error) {
	return processGroupExists(process.Pid)
}

func openPlatformCapture(path string) (*os.File, error) {
	var descriptor int
	err := retryEINTR(func() error {
		var err error
		descriptor, err = unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func preparePlatformCapture(file *os.File) error {
	return unix.SetNonblock(int(file.Fd()), false)
}

func finishPlatformOutput(file *os.File, deadline time.Time) error {
	return file.SetReadDeadline(deadline)
}

func terminateProcess(process *os.Process, pipes processParentPipes, gracePeriod time.Duration) error {
	termErr := signalProcessInvocation(process, syscall.SIGTERM)
	exited, waitErr := waitForProcessInvocation(process, gracePeriod)
	if exited && errors.Is(termErr, syscall.EPERM) {
		termErr = nil
	}
	var killErr error
	if !exited {
		killErr = signalProcessInvocation(process, syscall.SIGKILL)
	}
	return errors.Join(termErr, waitErr, killErr, pipes.closeAll())
}

func signalProcessInvocation(process *os.Process, signal syscall.Signal) error {
	err := normalizeProcessGroupSignalError(process.Pid, syscall.Kill(-process.Pid, signal))
	if err == nil || !errors.Is(err, syscall.ESRCH) {
		return err
	}
	err = process.Signal(signal)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func waitForProcessInvocation(process *os.Process, gracePeriod time.Duration) (bool, error) {
	deadline := time.Now().Add(gracePeriod)
	delay := time.Millisecond
	for {
		exists, err := processGroupExists(process.Pid)
		if err != nil {
			return false, err
		}
		if !exists && processWaitCompleted(process) {
			return true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		timer := time.NewTimer(min(delay, remaining))
		<-timer.C
		delay = min(delay*2, 50*time.Millisecond)
	}
}

func processGroupExists(id int) (bool, error) {
	err := syscall.Kill(-id, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func drainProcessOutput(ctx context.Context, request ProcessStartRequest, stream ProcessStream,
	reader *os.File, offset *int64, events chan<- PrimitiveEvent) error {
	fd := int(reader.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		return fmt.Errorf("drain process %s: set nonblocking: %w", processStreamName(stream), err)
	}
	buffer := make([]byte, ProcessOutputChunkSize)
	for {
		count, err := unix.Read(fd, buffer)
		if count > 0 {
			if !sendProcessOutput(ctx, request, stream, *offset, buffer[:count], events) {
				return nil
			}
			*offset += int64(count)
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("drain process %s: %w", processStreamName(stream), err)
		}
		if count == 0 {
			return nil
		}
	}
}
