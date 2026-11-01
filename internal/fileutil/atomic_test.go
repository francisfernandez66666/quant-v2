// atomic_test.go — §W3-c 原子写原语的行为锁（2026-11-01 补测）。
//
// 为什么这个包值得单独测：auth/config/data/research/scheduler 的**所有**持久化点都走它，
// 而它此前零测试。原子写的三条保证——不产生半截文件、权限位真的生效、失败路径不留垃圾——
// 任何一条破了，表现都是"偶发配置丢失/账号被清空"，而不是一条能定位的报错。
//
// English: locks the three guarantees of the shared atomic-write primitive — no half-written file,
// the requested mode really applies, and failure paths leave no debris.
package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ① 写入内容与权限位都按参数落地。
func TestAtomicWriteWritesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := AtomicWrite(p, []byte(`{"k":1}`), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if string(b) != `{"k":1}` {
		t.Fatalf("内容不符: %q", b)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("权限应为 0600，实得 %o（密钥文件权限放宽是安全问题）", got)
	}
}

// ② 覆盖写：旧内容必须被整体替换，不能出现"新内容 + 旧尾巴"。
func TestAtomicWriteReplacesOlderLongerContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	long := strings.Repeat("x", 4096)
	if err := AtomicWrite(p, []byte(long), 0o644); err != nil {
		t.Fatalf("首次写: %v", err)
	}
	if err := AtomicWrite(p, []byte("short"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "short" {
		t.Fatalf("覆盖后应为 %q，实得 %d 字节（长度 %d）", "short", len(b), len(b))
	}
}

// ③ 成功路径不留临时文件（半截 .tmp 堆积会污染目录扫描与备份）。
func TestAtomicWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := AtomicWrite(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("残留临时文件 %s —— 失败/成功路径都该清干净", e.Name())
		}
	}
	if len(ents) != 1 || ents[0].Name() != "a.json" {
		t.Fatalf("目录内应只有目标文件，实得 %v", ents)
	}
}

// ④ 失败路径：目录不存在 ⇒ 返回错误，且**不留下**任何半截文件。
// 反证靶心：把 CreateTemp 的目录换成 path 所在目录是设计要点——若改成 os.TempDir()，
// rename 会跨盘失败，这条用例会以"文件不存在"的形式先红。
func TestAtomicWriteFailsCleanlyOnMissingDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nope", "a.json")
	if err := AtomicWrite(p, []byte("x"), 0o644); err == nil {
		t.Fatal("目录不存在时应返回错误，不能静默成功")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("失败后不应产生目标文件，stat err=%v", err)
	}
	// 父目录里也不该出现临时文件
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("失败路径不应留下任何文件，实得 %v", ents)
	}
}

// ⑤ 空内容也必须写成 0 字节文件（而不是"没写"）——清空配置是合法语义。
func TestAtomicWriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.json")
	if err := AtomicWrite(p, nil, 0o644); err != nil {
		t.Fatalf("AtomicWrite(nil): %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("空内容也应落盘: %v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("应为 0 字节，实得 %d", st.Size())
	}
}
