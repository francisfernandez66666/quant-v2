// concepts_test.go — §C1-V2 概念库落库/覆盖率锁。
package store

import (
	"path/filepath"
	"testing"
)

func TestConceptReplaceAndCoverage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "concept.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.ReplaceConceptMembers("308001", "AI", "ths", []string{"600000.SH", "600001.SH"}, "2026-10-22T15:00:00+08:00"); err != nil {
		t.Fatalf("ReplaceConceptMembers: %v", err)
	}
	if err := db.UpsertConceptSyncState("308001", "AI", "2026-10-22T15:00:00+08:00", 2, "ok", ""); err != nil {
		t.Fatalf("UpsertConceptSyncState: %v", err)
	}
	if err := db.UpsertConceptSyncState("308002", "机器人", "", 0, "error", "timeout"); err != nil {
		t.Fatalf("UpsertConceptSyncState: %v", err)
	}
	synced, total, cov, last, err := db.ConceptCoverage()
	if err != nil {
		t.Fatalf("ConceptCoverage: %v", err)
	}
	if synced != 1 || total != 2 || cov != 50 || last == "" {
		t.Fatalf("覆盖率读数不符: synced=%d total=%d cov=%v last=%q", synced, total, cov, last)
	}
	codes, err := db.ConceptMembersForTsCode("600000.SH")
	if err != nil || len(codes) != 1 || codes[0] != "308001" {
		t.Fatalf("反查概念失败: %v %v", codes, err)
	}
	// 全量替换：第二次只留一只，旧成员必须消失。
	if err := db.ReplaceConceptMembers("308001", "AI", "ths", []string{"600001.SH"}, "2026-10-22T16:00:00+08:00"); err != nil {
		t.Fatalf("ReplaceConceptMembers(2): %v", err)
	}
	if codes, _ := db.ConceptMembersForTsCode("600000.SH"); len(codes) != 0 {
		t.Fatalf("旧成员残留: %v", codes)
	}
}
