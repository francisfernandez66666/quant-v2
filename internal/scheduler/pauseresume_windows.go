//go:build windows

// Windows：无 POSIX 信号（SIGSTOP/SIGCONT 不可用）。
// 暂停/恢复仅 Linux 支持——Windows 分支降级为 no-op（任务继续运行），并记日志。
// 与 MIGRATION_CN_PLAN 既定口径一致。
// 文件：pauseresume_windows.go
// 包名：scheduler

package scheduler

import (
	"log"
	"os/exec"
)

// pauseProcess Windows 降级：无操作（仅记录）。
func pauseProcess(cmd *exec.Cmd) {
	log.Printf("[scheduler] 暂停不支持(Windows)：任务 %v 继续运行", cmd.Path)
}

// resumeProcess Windows 降级：无操作。
func resumeProcess(cmd *exec.Cmd) {
	log.Printf("[scheduler] 恢复不支持(Windows)：任务 %v 继续运行", cmd.Path)
}
