//go:build !linux && !windows

// 非 Linux/Windows（开发机 darwin 等）：仅设独立进程组；Pdeathsig 为 Linux 专有不可用。
// 文件：procattr_other.go
// 包名：scheduler
// 所属模块：「任务调度与后台 worker 管理」
// 模块职责：本文件属于 任务调度与后台 worker 管理，负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

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
