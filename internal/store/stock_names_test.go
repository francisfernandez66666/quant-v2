// §0929FILL-NAME 代码→股票名旁证（FIX_PLAN_20260929 ⑦ P2-2）：store 侧三组锁。
//
// 锁什么：
//
//	S1 命中/缺失的真实形状——查得到的进 map、查不到与空名行**少键**（前端据此显示「—」，
//	   一旦这里改成"给空串占位"，流水页就会把空名字当成系统藏数据）；
//	S2 入参归一与上限——大小写/空白去重后只问一次，超限显式 ErrTooManyCodes（防无界 IN）；
//	S3 负锁（本批承诺的那条）——成交簿的身份锚定义里**永远不许出现 name**：
//	   幂等唯一索引与判重 SQL 只能落在 (order_id,traded_at,price,qty) 与 trade_id 上。
//	   名称一旦能当锚，同一家公司改名/两处名字不一致就会让同一笔成交判不出重复（§M4 形态）。
//
// English: store-side locks for the read-only code→name channel — hit/miss shape, input
// normalization and the per-query cap, plus the promised negative lock that broker names
// never take part in the fills idempotency anchor.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStockNamesByCodesHitAndMiss S1：命中给名字，缺失与空名行都必须是「少键」而不是空串键。
func TestStockNamesByCodesHitAndMiss(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO stocks(ts_code, name) VALUES('600000.SH','浦发银行')`)
	mustExec(t, db, `INSERT INTO stocks(ts_code, name) VALUES('603468.SH','')`) // 空名壳行
	mustExec(t, db, `INSERT INTO stocks(ts_code, name) VALUES('000001.SZ','平安银行')`)

	got, err := db.StockNamesByCodes([]string{"600000.SH", "603468.SH", "000001.SZ", "999999.SZ"})
	if err != nil {
		t.Fatalf("§0929FILL-NAME：旁证查询报错 %v", err)
	}
	if got["600000.SH"] != "浦发银行" || got["000001.SZ"] != "平安银行" {
		t.Fatalf("§0929FILL-NAME：命中的代码没给出名字（%v）", got)
	}
	// 空名行与未知代码都必须**没有键**——前端靠「键缺失」显示「—」，靠空串区分不出两种缺失。
	if _, ok := got["603468.SH"]; ok {
		t.Fatalf("§0929FILL-NAME：空名行不得进 map（got %#v）——空串会被前端当成名字渲染", got["603468.SH"])
	}
	if _, ok := got["999999.SZ"]; ok {
		t.Fatalf("§0929FILL-NAME：未知代码不得进 map")
	}
	if len(got) != 2 {
		t.Fatalf("§0929FILL-NAME：map 应只含两个命中键，got %d（%v）", len(got), got)
	}
}

// TestStockNamesByCodesNormalizeAndCap S2：入参大写/去空白/去重，超限显式报错，空入参给空 map。
func TestStockNamesByCodesNormalizeAndCap(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO stocks(ts_code, name) VALUES('600000.SH','浦发银行')`)

	// 同一代码写三种形态：只应问一次，命中一个键。
	got, err := db.StockNamesByCodes([]string{"600000.sh", " 600000.SH ", "600000.SH"})
	if err != nil || len(got) != 1 || got["600000.SH"] != "浦发银行" {
		t.Fatalf("§0929FILL-NAME：大小写/空白/重复未归一（len=%d err=%v）", len(got), err)
	}
	// 空入参（含全空白项）→ 空 map 且**不报错**：前端首屏无成交时不该为此看到红色错误。
	empty, err := db.StockNamesByCodes([]string{"", "   "})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("§0929FILL-NAME：空入参应给非 nil 空 map，got %#v err=%v", empty, err)
	}
	if nilMap, err := db.StockNamesByCodes(nil); err != nil || nilMap == nil {
		t.Fatalf("§0929FILL-NAME：nil 入参也必须给非 nil map（调用侧无需判空）")
	}
	// 超限：显式哨兵错误，而不是拼一条无界 IN 语句。
	over := make([]string, 0, MaxStockNamesPerQuery+1)
	for i := 0; i <= MaxStockNamesPerQuery; i++ {
		over = append(over, fmt.Sprintf("9%05d.SZ", i))
	}
	if _, err := db.StockNamesByCodes(over); !errors.Is(err, ErrTooManyCodes) {
		t.Fatalf("§0929FILL-NAME：超过 %d 个代码应回 ErrTooManyCodes，got %v", MaxStockNamesPerQuery, err)
	}
	// 边界：正好等于上限必须放行（否则前端按 200 拼批会结构性必红）。
	if _, err := db.StockNamesByCodes(over[:MaxStockNamesPerQuery]); err != nil {
		t.Fatalf("§0929FILL-NAME：恰好 %d 个代码应放行，got %v", MaxStockNamesPerQuery, err)
	}
}

// TestFillsNameNeverAnchorsIdempotency S3 负锁：成交簿幂等锚定义里出现 name 即红。
//
// 为什么单独钉这一条：本批选择「前端映射 + 只读旁证」而**不给 fills 加 name 列**，
// 就是防后人把券商自由文本当身份锚用（§M4：网关回报的 name 曾只用于回填持仓名）。
// 判据按真值形态写：fills 相关的所有 UNIQUE 索引定义 + 两条判重 SQL 里，
// 都不允许在锚点列清单中出现 name。
func TestFillsNameNeverAnchorsIdempotency(t *testing.T) {
	storeSrc := readRepoFile(t, "store.go")
	posSrc := readRepoFile(t, "real_positions.go")
	settleSrc := readRepoFile(t, "settlement.go")

	// ① 幂等/唯一锚索引：CREATE UNIQUE INDEX ... ON fills(...) 的每一条都不许带 name。
	idxRe := regexp.MustCompile(`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_[a-z_]* ON fills\(([^)]*)\)`)
	legs := idxRe.FindAllStringSubmatch(storeSrc, -1)
	if len(legs) < 3 {
		t.Fatalf("§0929FILL-NAME 负锁读不到幂等锚定义（只匹配到 %d 条 idx_fills_* 唯一索引）——判据本身已失效", len(legs))
	}
	for _, m := range legs {
		if strings.Contains(m[1], "name") {
			t.Fatalf("§0929FILL-NAME 负锁：成交锚 %q 里出现了 name——券商名称是自由文本，绝不参与去重", m[1])
		}
		// 正向半锁：锚列必须仍是 order_id/traded_at/price/qty 或 trade_id 这两族之一。
		if !strings.Contains(m[1], "trade_id") && m[1] != "order_id, traded_at, price, qty" {
			t.Fatalf("§0929FILL-NAME：成交唯一锚形状变了（%q），本锁需要按新锚形重读后再改", m[1])
		}
	}
	// ② 判重 SQL 仍只读原始 fills 的事实键，且不含 name。
	for _, pair := range []struct {
		file string
		src  string
		want string
	}{
		{"real_positions.go", posSrc, "SELECT COUNT(*) FROM fills WHERE trade_id=?"},
		{"settlement.go", settleSrc, "SELECT COUNT(*) FROM fills WHERE order_id=? AND traded_at=? AND price=? AND qty=?"},
	} {
		if !strings.Contains(pair.src, pair.want) {
			t.Fatalf("§0929FILL-NAME：%s 的判重 SQL 变了（找不到 %q）", pair.file, pair.want)
		}
	}
	// ③ fills 表定义本身没有 name 列（本批刻意不加；真加列时必须连同旁证语义一起改锁）。
	if re := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS fills \(([\s\S]*?)\)\x60`); re.MatchString(storeSrc) {
		body := re.FindStringSubmatch(storeSrc)[1]
		for _, line := range strings.Split(body, "\n") {
			// 逐列取首词：先去注释前缀再切空格；列名恰为 name 即视为账本长出名称列。
			trimmed := strings.TrimSpace(line)
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "--"))
			field := strings.SplitN(trimmed, " ", 2)[0]
			if field == "name" || strings.HasPrefix(field, "name,") {
				t.Fatalf("§0929FILL-NAME：fills 表又长出 name 列（%q）——须先按账本迁移口径重裁这条锁", line)
			}
		}
	} else {
		t.Fatal("§0929FILL-NAME 负锁读不到 fills 建表语句——判据本身已失效")
	}
}

// mustExec 用例内直接执行建数据语句（stocks 表由 migrate 建好，这里只插测试行）。
func mustExec(t *testing.T, db *DB, q string) {
	t.Helper()
	if _, err := db.db.Exec(q); err != nil {
		t.Fatalf("插入测试行失败: %v\nSQL: %s", err, q)
	}
}

// readRepoFile 从测试工作目录（本包）相对路径读仓库源文件，供静态负锁使用。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.FromSlash(rel)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return string(b)
}
