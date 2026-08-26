// atomic_write.go — §GAP3.8 data 层状态文件统一原子写：先写同目录临时文件再 rename 覆盖。
// 直接 os.WriteFile 在进程被 kill/OOM 时会把 JSON 状态文件截断，且多数 store 加载失败时
// 静默清空——坏文件等于数据丢失。rename 在同一文件系统上原子生效，杜绝半截文件。
// （与 engine/atomic_write.go 同款实现；独立成包避免 data↔engine 循环依赖。）
package data

import (
	"os"

	"quant-trading-v2/internal/fileutil"
)

// AtomicWrite §W3-c 统一原子写（导出）：同目录唯一临时文件 + 写后 fsync + rename 覆盖 + 目录 fsync。
// 相比旧版（固定 .tmp 名、无 fsync）修复两点：
//  1. 固定临时名在双进程（quant 与 researchd 共享 config.json）并发写时互相踩踏，
//     rename 出交错内容——改为 CreateTemp 唯一名；
//  2. 掉电/强杀场景 rename 后内容可能未落盘——写入后 fsync 文件，rename 后 fsync 目录。
//
// English: §W3-c unified atomic write: unique temp file in the same dir + fsync(file) before rename
// + fsync(dir) after — fixes cross-process .tmp stomping and post-crash durability.
func AtomicWrite(path string, raw []byte, perm os.FileMode) error {
	return fileutil.AtomicWrite(path, raw, perm)
}

// atomicWrite 包内既有调用方兼容包装。
func atomicWrite(path string, raw []byte, perm os.FileMode) error {
	return AtomicWrite(path, raw, perm)
}
