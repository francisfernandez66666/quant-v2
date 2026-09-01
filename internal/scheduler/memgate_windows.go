//go:build windows

// Windows：调用 kernel32 GlobalMemoryStatusEx 读物理可用内存（AvailPhys，字节→MB）；
// 读取失败返回 -1（闸门放行，不因读数失败卡死队列）。与 memgate_unix.go 语义对齐。
// x/sys/windows v0.47.0 未导出 MEMORYSTATUSEX/GlobalMemoryStatusEx，故直接走 syscall。
// 文件：memgate_windows.go
// 包名：scheduler

package scheduler

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx 对应 Win32 MEMORYSTATUSEX（含 ullAvailExtendedVirtual 共 64 字节）。
type memoryStatusEx struct {
	Length               uint32 // 结构体大小（调用前需填入）
	MemoryLoad           uint32 // 当前内存使用率（0-100）
	TotalPhys            uint64 // 物理内存总量（字节）
	AvailPhys            uint64 // 可用物理内存（字节）
	TotalPageFile        uint64 // 页面文件总大小（字节）
	AvailPageFile        uint64 // 可用页面文件（字节）
	TotalVirtual         uint64 // 总虚拟地址空间（字节）
	AvailVirtual         uint64 // 可用虚拟地址空间（字节）
	AvailExtendedVirtual uint64 // 可用扩展虚拟地址空间（字节）
}

var (
	kernel32                 = syscall.MustLoadDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.MustFindProc("GlobalMemoryStatusEx")
)

// platformMemAvailableMB 系统可用物理内存（MB）。
func platformMemAvailableMB() int {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return -1
	}
	// AvailPhys 单位字节 → MB
	return int(m.AvailPhys / (1024 * 1024))
}
