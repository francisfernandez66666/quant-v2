// schema_version_test.go — §SCHEMA-VERSION（2026-10-26，旧审计 F10）：
// Open 必须把 PRAGMA user_version 抬到当前值；未来版本库必须拒绝打开，避免旧二进制静默跑新 schema。
package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestSchemaVersionStamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := db.SchemaVersion(); got != schemaVersion {
		t.Fatalf("新库 schema_version 应为 %d，got %d", schemaVersion, got)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer db2.Close()
	if got := db2.SchemaVersion(); got != schemaVersion {
		t.Fatalf("重开后 schema_version 应保持 %d，got %d", schemaVersion, got)
	}
}

func TestSchemaVersionNewerRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatalf("写未来版本: %v", err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("未来 schema 版本的库必须拒绝打开")
	}
}
