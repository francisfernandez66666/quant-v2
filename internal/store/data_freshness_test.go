// data_freshness_test.go — §D-4（GAP_VERIFY_20260917_PM）行情库覆盖断言：
// 滞后 0/1 交易日边界、断供多日、无日历不误报、行情全空不误报、daily∪ths 取大。
package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newFreshDB 建带交易日历（2026-09-14~17 均开市，13 周六休市）的临时库。
func newFreshDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cal := []struct {
		d    string
		open int
	}{{"20260911", 0}, {"20260912", 0}, {"20260914", 1}, {"20260915", 1}, {"20260916", 1}, {"20260917", 1}}
	for _, c := range cal {
		if _, err := db.db.Exec(`INSERT INTO trade_cal(cal_date,is_open) VALUES(?,?)`, c.d, c.open); err != nil {
			t.Fatalf("cal: %v", err)
		}
	}
	return db
}

func TestCheckDataFreshness(t *testing.T) {
	const today = "20260917"

	t.Run("滞后一个交易日以内算新鲜", func(t *testing.T) {
		db := newFreshDB(t)
		db.db.Exec(`INSERT INTO ths_daily(ts_code,trade_date) VALUES('600000.SH','20260916')`)
		f, err := db.CheckDataFreshness(today, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !f.OK || f.LagDays != 1 || f.Latest != "20260916" || f.LastOpen != "20260917" {
			t.Fatalf("应 滞后1/新鲜, got %+v", f)
		}
	})

	t.Run("断供两日 maxLag1 判不新鲜", func(t *testing.T) {
		db := newFreshDB(t)
		db.db.Exec(`INSERT INTO daily(ts_code,trade_date) VALUES('600000.SH','20260915')`)
		f, _ := db.CheckDataFreshness(today, 1)
		if f.OK || f.LagDays != 2 {
			t.Fatalf("应判不新鲜 lag=2, got %+v", f)
		}
	})

	t.Run("daily 与 ths_daily 取最新", func(t *testing.T) {
		db := newFreshDB(t)
		db.db.Exec(`INSERT INTO daily(ts_code,trade_date) VALUES('600000.SH','20260914')`)
		db.db.Exec(`INSERT INTO ths_daily(ts_code,trade_date) VALUES('600000.SH','20260917')`)
		f, _ := db.CheckDataFreshness(today, 0)
		if !f.OK || f.Latest != "20260917" {
			t.Fatalf("应取两表大者且零滞后, got %+v", f)
		}
	})

	t.Run("无日历/行情全空不误报", func(t *testing.T) {
		db := newFreshDB(t)
		db.db.Exec(`DELETE FROM trade_cal`)
		if f, _ := db.CheckDataFreshness(today, 0); !f.OK || f.TradeCalOK {
			t.Fatalf("无日历应判无从判定不误报, got %+v", f)
		}
		db2 := newFreshDB(t)
		if f, _ := db2.CheckDataFreshness(today, 0); !f.OK || f.Latest != "" {
			t.Fatalf("行情全空（未启动采集）不应误报, got %+v", f)
		}
	})
}

// TestStoreOpenFilePermissions §D-5：Open 后库文件主文件 mode 必须 0600（含密钥 KV 快照，
// auth.json §A3 同口径）。Windows 无 POSIX mode 语义时跳过断言。
func TestStoreOpenFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode 不适用")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "trading.db")
	db, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("trading.db 权限应 0600, got %v", st.Mode().Perm())
	}
}
