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
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(processHandle)
	if err = windows.AssignProcessToJobObject(job, processHandle); err != nil {
		closeJob()
		return nil, err
	}
	return closeJob, nil
}
