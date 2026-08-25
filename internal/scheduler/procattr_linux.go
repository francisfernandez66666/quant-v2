//go:build linux

// §S1 孤儿进程防护（Linux/首尔服务器）：子进程独立进程组 + Pdeathsig。
// researchd 被 OOM/SIGKILL 时：Pdeathsig 让内核直接杀掉研究子进程（此前孤儿继续跑，
// 重启后 ResetStaleRunningTasks 复活任务再 spawn → 同一任务双实例并发写库——2026-08-25
// discover_factors 压死整机事故的根因之一）。Setpgid 让抢占/超时能整组击杀。
package scheduler

import (
	"os/exec"
	"syscall"
)

// configureSysProcAttr 启动前设置进程组与父死信号。
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,            // 独立进程组：支持整组击杀
		Pdeathsig: syscall.SIGKILL, // 父进程死亡 → 内核杀子进程
	}
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
