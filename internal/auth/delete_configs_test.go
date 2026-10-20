// §F-15 删号数据清理：配置行先备份再删、空 userID 拒绝。
package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteConfigsBackupThenRemove(t *testing.T) {
	m := newTestMgr(t)
	code, _ := m.CreateInvite()
	u, err := m.Register("deltest", "secret123", code)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.SetConfig(u.ID, "strategy", `{"n":1}`); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := m.SetConfig(u.ID, "d1", `{"threshold":60}`); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// 他人的配置行必须原样保留
	if err := m.SetConfig("u_other", "strategy", `{"keep":true}`); err != nil {
		t.Fatalf("SetConfig other: %v", err)
	}

	n, err := m.DeleteConfigs(u.ID)
	if err != nil {
		t.Fatalf("DeleteConfigs: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted=%d want=2", n)
	}
	if _, ok := m.GetConfig(u.ID, "strategy"); ok {
		t.Fatalf("被删账号配置仍可读")
	}
	if v, ok := m.GetConfig("u_other", "strategy"); !ok || !strings.Contains(v, "keep") {
		t.Fatalf("他人配置被误删: %q %v", v, ok)
	}
	backs, _ := filepath.Glob(filepath.Join(m.dataDir, "deleted_configs_*.json"))
	if len(backs) != 1 {
		t.Fatalf("备份件数=%d want=1", len(backs))
	}
	raw, err := os.ReadFile(backs[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	for _, want := range []string{u.ID, "strategy", "d1", "threshold"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("备份缺字段 %s: %s", want, raw)
		}
	}
}

func TestDeleteConfigsRejectsEmptyUserID(t *testing.T) {
	m := newTestMgr(t)
	if _, err := m.DeleteConfigs(""); err == nil {
		t.Fatalf("空 userID 必须拒绝（会误删平台级行）")
	}
}
