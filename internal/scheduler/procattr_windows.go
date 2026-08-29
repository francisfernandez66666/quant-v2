//go:build windows

// Windows 进程组管理：Windows 无 POSIX 进程组/Kill 原语。
// 用 CREATE_NEW_PROCESS_GROUP 让子进程独立（便于整组回收），击杀用 taskkill /T /F
// 树杀（含孙进程，research/discover 等子任务可能再 spawn 孙进程）。
// 文件：procattr_windows.go
// 包名：scheduler
// 所属模块：「任务调度与后台 worker 管理」

package scheduler

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureSysProcAttr 启动前设置独立进程组（Windows 用 CreationFlags）。
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcessGroup 整组击杀（含孙进程）；回退单杀。调用方保证 cmd.Process 已 Start。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// 树杀：Windows 无 POSIX kill，用 taskkill /T /F 杀掉整个进程树（含孙进程）。
	out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).CombinedOutput()
	if err != nil {
		// 回退：直接单杀当前进程
		if kerr := cmd.Process.Kill(); kerr != nil {
			log.Printf("[scheduler] taskkill 失败且单杀亦失败: %v (%s)", kerr, string(out))
		} else {
			log.Printf("[scheduler] taskkill 失败, 已回退单杀: %v (%s)", err, string(out))
		}
		return
	}
	_ = fmt.Sprint(string(out))
}
