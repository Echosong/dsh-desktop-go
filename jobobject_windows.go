package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle 持有本应用创建的 Job Object。
//
// 设置 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE 后，只要这个句柄被关闭
// —— 无论是应用正常退出、崩溃，还是被任务管理器/命令行强杀 ——
// 系统都会把 job 内的所有进程一并结束。
// 这样即使 OnShutdown 没机会执行，dsh 也不会残留在后台。
var jobHandle windows.Handle

// attachToJob 把指定进程（及其后续派生的子进程）纳入 Job Object。
// 失败不致命：正常的退出清理（taskkill 进程树）仍然有效，这里只是多一层保底。
func attachToJob(pid int) error {
	if jobHandle == 0 {
		handle, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return err
		}

		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

		if _, err := windows.SetInformationJobObject(
			handle,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			windows.CloseHandle(handle)
			return err
		}
		jobHandle = handle
	}

	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(proc) }()

	return windows.AssignProcessToJobObject(jobHandle, proc)
}
