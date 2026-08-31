// Package engine 核心引擎：信号生产、打分池、板块传播、持仓退出、通知推送与 QMT 自动交易的 orchestration。
// 本文件提供原子写盘工具函数，确保关键状态文件（信号/墓碑/打分等）在进程 crash/OOM 时不会被截断。
// 采用"先写临时文件再 rename"的策略，利用文件系统 rename 的原子性保证数据完整性。
package engine

import (
	"log"
	"os"
	"path/filepath"
)

// atomicWrite §E3 原子写盘：先写同目录临时文件再 rename 覆盖。
// 直接 os.WriteFile 在进程被 kill/OOM（自述 1.6GiB 服务器发生过 global_oom）时会把
// signals_today.json 等状态文件截断——重启后当日固化信号/墓碑丢失，"假信号复活"可再次
// 触发 autoPlace 下单。rename 在同一文件系统上原子生效，杜绝半截文件。
// English: E3 atomic persistence — write to a sibling temp file then rename over the target, so a
// crash/OOM mid-write can no longer truncate state files (lost tombstones previously let dead
// signals resurrect and re-trigger auto placement).
func atomicWrite(path string, raw []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// mustAtomicWrite atomicWrite 的日志包装：失败仅记错误不中断调用方（与原 WriteFile 行为一致，
// 但不再静默）。English: logs write failures instead of silently swallowing them.
func mustAtomicWrite(tag, path string, raw []byte) {
	if err := atomicWrite(path, raw, 0644); err != nil {
		log.Printf("[engine] %s 原子写入失败: %v", tag, err)
	}
}

// ensureParentDir 确保目标文件的父目录存在（首次落盘前调用）。
// 若目录已存在则 MkdirAll 静默返回 nil；路径为空或 "." 时跳过。
func ensureParentDir(path string) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
}
