// atomic_write.go — §GAP3.8 data 层状态文件统一原子写：先写同目录临时文件再 rename 覆盖。
// 直接 os.WriteFile 在进程被 kill/OOM 时会把 JSON 状态文件截断，且多数 store 加载失败时
// 静默清空——坏文件等于数据丢失。rename 在同一文件系统上原子生效，杜绝半截文件。
// （与 engine/atomic_write.go 同款实现；独立成包避免 data↔engine 循环依赖。）
package data

import "os"

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
