//go:build !linux

// 非 Linux（开发机 darwin）：仅设独立进程组；Pdeathsig 为 Linux 专有不可用。
package scheduler

import (
	"os/exec"
	"syscall"
)

// configureSysProcAttr 启动前设置进程组。
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 整组击杀（含孙进程）；回退单杀。调用方保证 cmd.Process 已 Start。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
