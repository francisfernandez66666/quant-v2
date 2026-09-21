// insert_whitelist_test.go — §INSERTLOCK（2026-09-22 修复批）InsertRows 写入面校验的反例锁。
//
// 缺陷原型：InsertRows 把表名/列名 fmt.Sprintf 直拼进 SQL，cols 来自调用方（导入面
// dateload/delta import 的 JSON 列清单可被外部文件左右）——拼错列名靠 SQLite 事后报错、
// 恶意伪列名（"close) , (SELECT 1)--"）没有本地防线。
//
// 本锁钉死四件事：
//
//	① 合法路径：白名单表子集列正常写入；
//	② 白名单表未知列 → 显式报错；
//	③ 非法标识符（注入串/带空白/带括号）表名或列名 → 显式报错，且在拼语句前拦下；
//	④ 白名单外表回退运行时 schema 校验（PRAGMA table_info）：真实存在的扩展表列可写，
//	   未知列/不存在的表显式报错。
//
// English: locks the InsertRows write-surface validation — bare identifiers only, whitelist
// subset for known tables, runtime PRAGMA schema check for extension tables, explicit errors
// for unknown columns and injection-shaped names.
package store

import (
	"strings"
	"testing"
)

func TestInsertRowsWhitelistKnownTable(t *testing.T) {
	db := testDB(t)
	rows := []map[string]any{{"ts_code": "600000.SH", "trade_date": "20260922", "close": 10.5}}

	// ① 合法：白名单表的列子集正常写入
	if n, err := db.InsertRows("daily", []string{"ts_code", "trade_date", "close"}, rows); err != nil || n != 1 {
		t.Fatalf("合法列应写入成功，得 n=%d err=%v", n, err)
	}
	// ② 未知列：白名单内不存在的列必须显式报错（旧行为拼进语句后靠 SQLite 报错/写歪列）
	if _, err := db.InsertRows("daily", []string{"ts_code", "trade_date", "not_a_column"}, rows); err == nil {
		t.Fatal("未知列必须报错")
	} else if !strings.Contains(err.Error(), "not_a_column") {
		t.Fatalf("报错信息应点名未知列，得 %v", err)
	}
	// ③ 注入形列名/表名：拼语句前即拦下
	if _, err := db.InsertRows("daily", []string{"close) , (SELECT 1)--"}, rows); err == nil {
		t.Fatal("注入形列名必须报错")
	} else if !strings.Contains(err.Error(), "非法列名") {
		t.Fatalf("报错应标明非法列名，得 %v", err)
	}
	if _, err := db.InsertRows("daily; DROP TABLE stocks", []string{"ts_code"}, rows); err == nil {
		t.Fatal("非法表名必须报错")
	}
	// 表未被偷偷改掉：注入尝试后合法写入仍正常
	if _, err := db.InsertRows("daily", []string{"ts_code", "trade_date", "close"}, rows); err != nil {
		t.Fatalf("注入拦截后合法写入不应受影响: %v", err)
	}
}

func TestInsertRowsWhitelistExtensionTable(t *testing.T) {
	db := testDB(t)
	// ④ 白名单外表（migrations 扩展表场景，如 wl_extension_probe_tbl）：回退 PRAGMA 实际 schema 校验
	if _, err := db.db.Exec(`CREATE TABLE wl_extension_probe_tbl (trade_date TEXT NOT NULL, ts_code TEXT, name TEXT, PRIMARY KEY (trade_date, ts_code))`); err != nil {
		t.Fatalf("create extension table: %v", err)
	}
	rows := []map[string]any{{"trade_date": "20260922", "ts_code": "600000.SH", "name": "炸板测试"}}
	if n, err := db.InsertRows("wl_extension_probe_tbl", []string{"trade_date", "ts_code", "name"}, rows); err != nil || n != 1 {
		t.Fatalf("扩展表真实列应写入成功，得 n=%d err=%v", n, err)
	}
	if _, err := db.InsertRows("wl_extension_probe_tbl", []string{"trade_date", "ghost_col"}, rows); err == nil {
		t.Fatal("扩展表未知列必须报错")
	}
	// 不存在的表：显式拒绝（旧行为到 Prepare 才报 no such table，且列面完全没校验）
	if _, err := db.InsertRows("no_such_table_anywhere", []string{"a"}, rows); err == nil {
		t.Fatal("未知表必须显式报错")
	} else if !strings.Contains(err.Error(), "no_such_table_anywhere") {
		t.Fatalf("报错应点名表名，得 %v", err)
	}
}
