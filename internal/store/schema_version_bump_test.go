// schema_version_bump_test.go — §W-05（2026-10-29 夜批）：schemaVersion 1 → 2。
//
// 背景：常量从 1 起就没动过，而迁移一直在加列 ⇒「拒绝旧代码开新库」这道守卫此前从未生效。
// 这三条断言钉住三件事：未来版本必须拒（E1）、老库必须抬到 2（E2）、常量不许手滑改回 1（E3）。
package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// E1：未来版本的库必须拒绝打开，且错误文案要能让人一眼知道是"二进制太旧"。
func TestRefusesNewerDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatalf("写未来版本: %v", err)
	}
	db.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("user_version=3 的库必须拒绝打开（否则旧二进制会静默跑新 schema）")
	}
	if !strings.Contains(err.Error(), "高于本二进制支持") {
		t.Fatalf("错误文案必须说明是版本过高，got: %v", err)
	}
}

// E2：老库（user_version=1，即本批之前的所有库）必须能打开并抬到 2。
func TestBumpsLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("降成老库: %v", err)
	}
	raw.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("老库必须能打开: %v", err)
	}
	defer db2.Close()
	if got := db2.SchemaVersion(); got != 2 {
		t.Fatalf("老库必须被抬到 2，got %d", got)
	}
}

// E3：防手滑把常量改回 1（改回去等于把本批的守卫又废掉，且所有断言仍绿）。
func TestVersionNotOne(t *testing.T) {
	if schemaVersion == 1 {
		t.Fatal("schemaVersion 不许是 1：1 表示「本批之前」，改回去等于让「拒绝旧代码开新库」再次失效")
	}
	if schemaVersion != 2 {
		t.Fatalf("本批定稿 schemaVersion=2，got %d（若确实要再加一版，改这条断言并写清原因）", schemaVersion)
	}
}
