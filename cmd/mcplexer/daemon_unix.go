//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func daemonSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func signalTerminate(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
