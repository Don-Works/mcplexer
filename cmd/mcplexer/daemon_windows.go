//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func daemonSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func signalTerminate(proc *os.Process) error {
	return proc.Kill()
}

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess = kernel32.NewProc("OpenProcess")
	procCloseHandle = kernel32.NewProc("CloseHandle")
)

const processQueryLimitedInfo = 0x1000

func processAlive(pid int) bool {
	h, _, _ := procOpenProcess.Call(
		uintptr(processQueryLimitedInfo),
		0,
		uintptr(pid),
	)
	if h == 0 {
		return false
	}
	procCloseHandle.Call(h)
	return true
}
