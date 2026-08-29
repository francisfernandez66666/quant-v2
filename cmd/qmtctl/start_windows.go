//go:build windows

// Windows：以 DETACHED_PROCESS 启动 MiniQMT，使其脱离 qmtctl 控制台常驻
// （qmtctl 退出后 MiniQMT 继续运行，由 QMT-Daily-Restart/看门狗看护）。
package main

import (
	"os/exec"
	"syscall"
)

// setDetached 以 DETACHED_PROCESS（0x00000008）标志启动子进程，使 MiniQMT 脱离 qmtctl 控制台
// 常驻运行（qmtctl 退出后 MiniQMT 继续存活，由 QMT-Daily-Restart/看门狗看护）。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
}
