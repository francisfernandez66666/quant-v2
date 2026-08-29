//go:build !windows

// 非 Windows（Linux/darwin）：读 /proc/meminfo 的 MemAvailable（KB→MB）；
// 读取失败返回 -1（闸门放行，不因读数失败卡死队列）。
// 文件：memgate_unix.go
// 包名：scheduler

package scheduler

import (
	"os"
	"strconv"
	"strings"
)

// platformMemAvailableMB 系统可用内存（MB）。
func platformMemAvailableMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(strings.TrimPrefix(line, "MemAvailable:"))
			if len(f) >= 1 {
				if kb, e := strconv.ParseInt(f[0], 10, 64); e == nil {
					return int(kb / 1024)
				}
			}
			return -1
		}
	}
	return -1
}
