//go:build windows

package stt

import (
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// superviseProcessTree places a model server in a Windows Job Object configured
// with KILL_ON_JOB_CLOSE. Windows closes Parley's job handle on graceful exit,
// forced termination, or a crash, which also terminates descendants the Python
// sidecar may have created.
func superviseProcessTree(process *os.Process) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := sync.OnceFunc(func() { _ = windows.CloseHandle(job) })
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		closeJob()
		return nil, err
	}
	var assignErr error
	if err = process.WithHandle(func(handle uintptr) {
		assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle))
	}); err != nil {
		closeJob()
		return nil, err
	}
	if assignErr != nil {
		closeJob()
		return nil, assignErr
	}
	return closeJob, nil
}
