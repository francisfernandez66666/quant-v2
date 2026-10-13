// data_freshness_test.go — §D-4（GAP_VERIFY_20260917_PM）行情库覆盖断言：
// 滞后 0/1 交易日边界、断供多日、无日历不误报、行情全空不误报、daily∪ths 取大；
// §CAL-COVER-GO（2026-10-12）加三条自证：日历末行不覆盖今日时「滞后 0」必须判不新鲜
// （盲窗本体，现网 10-08/09 即此型）、覆盖今日时同一把尺子必须照常出数（防止判据恒红）、
// 日历齐而行情真落后时判因必须是 lag（两型修法不同，混一条必然误归因）。
package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// addCal 按用例补日历行（key=cal_date，value=is_open）。覆盖度判据只认表里真的有哪天，
// 所以夹具必须能自己决定末行——用 map 是因为插入顺序与判定无关。
func addCal(t *testing.T, db *DB, days map[string]int) {
	t.Helper()
	for d, open := range days {
		if _, err := db.db.Exec(`INSERT INTO trade_cal(cal_date,is_open) VALUES(?,?)`, d, open); err != nil {
			t.Fatalf("cal +%s: %v", d, err)
		}
	}
}

// dropCal 删掉某日以后的全部日历行（造"日历停更"盲窗）。
func dropCal(t *testing.T, db *DB, after string) {
	t.Helper()
	if _, err := db.db.Exec(`DELETE FROM trade_cal WHERE cal_date>?`, after); err != nil {
		t.Fatalf("cal -%s: %v", after, err)
	}
}

// 三表对目标交易日的数据新鲜度检查。
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

	// §CAL-COVER-GO 盲窗态：日历末行停在 20260916（今日 20260917 没有行），行情也到 20260916。
	// 旧判据在这里读出「最近交易日 20260916、滞后 0」＝满分放行——现网 09-28~30、10-08/09
	// 两段真实交易日就是这么整段被跳过的。新判据必须判不新鲜、归因 calendar_stalled、
	// 滞后按"算不了"报（-1，且文案是 unknown 而不是 0）。
	t.Run("日历不覆盖今日：滞后0也不判到齐（盲窗本体）", func(t *testing.T) {
		db := newFreshDB(t)
		dropCal(t, db, "20260916") // 末行退到昨天＝§CAL-COVER 的停更形态
		db.db.Exec(`INSERT INTO daily(ts_code,trade_date) VALUES('600000.SH','20260916')`)
		f, err := db.CheckDataFreshness(today, 1)
		if err != nil {
			t.Fatal(err)
		}
		if f.OK || f.Reason != FreshReasonCalendarStalled || f.LagDays != -1 {
			t.Fatalf("日历停更必须判不新鲜/calendar_stalled/滞后 unknown, got %+v", f)
		}
		if f.CoversToday || f.CalMax != "20260916" {
			t.Fatalf("覆盖读数不对（CalMax 应指到停更那行）, got %+v", f)
		}
		// 告警文案两条腿都要在：指到停更位置 + 明说覆盖不到今日（值班按句子就能归因）。
		if !strings.Contains(f.String(), "covers_today=no") || !strings.Contains(f.String(), "滞后 unknown") {
			t.Fatalf("盲窗文案不对, got %q", f.String())
		}
		if !strings.Contains(f.String(), "日历末行 20260916") {
			t.Fatalf("盲窗文案缺停更位置读数, got %q", f.String())
		}
	})

	// §CAL-COVER-GO 覆盖态（反证腿）：日历按 today+45 回写后末行在未来（含 is_open=0 的周末行，
	// 覆盖度看的是"表维护到哪"，不是"哪天开市"）。判据必须回到只按行情日龄判：
	// 缺这一条就无法区分"加固生效"与"判据改成恒红"。
	t.Run("日历覆盖今日（含未来休市行）：照常按日龄判且文案出数", func(t *testing.T) {
		db := newFreshDB(t)
		addCal(t, db, map[string]int{"20260918": 1, "20260919": 0, "20260920": 0, "20261031": 1})
		db.db.Exec(`INSERT INTO daily(ts_code,trade_date) VALUES('600000.SH','20260916')`)
		f, err := db.CheckDataFreshness(today, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !f.OK || f.Reason != "" || f.LagDays != 1 {
			t.Fatalf("覆盖态应 滞后1/新鲜/无判因, got %+v", f)
		}
		if !f.CoversToday || f.CalMax != "20261031" {
			t.Fatalf("覆盖读数不对（未来休市行同样算覆盖）, got %+v", f)
		}
		if !strings.Contains(f.String(), "covers_today=yes") || strings.Contains(f.String(), "unknown") {
			t.Fatalf("覆盖态文案应出 yes 且滞后照出数字, got %q", f.String())
		}
	})

	// 覆盖到位、行情真的落后：判因必须是 lag（与日历停更分开，否则告警把人支去查错的地方）。
	t.Run("日历齐而行情落后：判因 lag 不是 calendar_stalled", func(t *testing.T) {
		db := newFreshDB(t)
		addCal(t, db, map[string]int{"20260918": 1, "20261031": 1})
		db.db.Exec(`INSERT INTO daily(ts_code,trade_date) VALUES('600000.SH','20260914')`)
		f, err := db.CheckDataFreshness(today, 1)
		if err != nil {
			t.Fatal(err)
		}
		if f.OK || f.Reason != FreshReasonLag || f.LagDays != 3 || !f.CoversToday {
			t.Fatalf("应 lag 型不新鲜（滞后3＝0915/0916/0917 三个开市日没行情），got %+v", f)
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
