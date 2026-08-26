// Package fileutil — 无依赖的文件原子写原语（§W3-c）。
// 供 auth/config/data/research/scheduler 等所有持久化点统一使用：
// 同目录唯一临时文件 + fsync(文件) + rename 覆盖 + fsync(目录)。
// 设计约束：本包不得 import 项目内任何其他包（叶节点），否则会重新引入循环依赖
// （实录：config→data→config 成环，故从 data 下沉至此）。
// English: Package fileutil — dependency-free atomic file write primitive shared by every
// persistence point. Must stay a leaf package (no intra-project imports) to avoid cycles.
package fileutil

import (
	"os"
	"path/filepath"
)

// AtomicWrite 原子写：唯一临时名防双进程踩踏；写后 fsync 防掉电半截；rename 后目录 fsync。
// English: unique temp name (no cross-process .tmp stomping), fsync before rename for durability,
// directory fsync to persist the rename.
func AtomicWrite(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // rename 成功后是 no-op；失败路径清理半截文件
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best-effort：部分平台/文件系统不支持目录 fsync
		_ = d.Close()
	}
	return nil
}
