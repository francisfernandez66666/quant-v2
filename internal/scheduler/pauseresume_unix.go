//go:build !windows

// 非 Windows（Linux/darwin）：进程暂停/恢复用 POSIX 信号 SIGSTOP/SIGCONT。
// 文件：pauseresume_unix.go
// 包名：scheduler

package scheduler

import (
	"os/exec"
	"syscall"
)

// pauseProcess 挂起子进程（暂停研究任务，便于限流/人工控速）。
func pauseProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGSTOP)
}

// resumeProcess 恢复子进程。
func resumeProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGCONT)
}
