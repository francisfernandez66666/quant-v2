// Package store — §0929FILL-NAME：代码→股票名的只读旁证查询。
//
// 背景（FIX_PLAN_20260929 ⑦ P2-2）：实盘成交簿 fills 表根本没有 name 列（网关回报虽带 name，
// 但只用于建仓时回填 real_positions.name，成交行永远是空），所以「交易流水」页只能看代码、
// 人工核对柜台回单时要拿代码去反查名字。
//
// 本文件提供的是**只读旁证通道**：名称取自本地 stocks 表（tushare stock_basic 落库），
// 零网络、零写入、零迁移。刻意**不给 fills 加 name 列**——名称是券商侧自由文本，
// 一旦进账本就会有人把它当身份锚（本仓 §M4 锚点教训形态：幂等键必须是
// (order_id,traded_at,price,qty) + trade_id，见 store.go 的 idx_fills_idem 系列）。
// 幂等锚里出现 name 由 stock_names_test.go 的负锁钉红。
//
// English: a read-only code→name side-evidence channel backed by the local stocks table
// (tushare stock_basic). It deliberately does **not** add a name column to fills: broker names
// are free text and must never become an identity anchor (the idempotency keys stay
// (order_id,traded_at,price,qty) + trade_id, pinned by a negative lock in the test file).
package store

import (
	"errors"
	"strings"
)

// MaxStockNamesPerQuery 单次代码→名称旁证查询的代码数上限。
// 上限存在的理由：调用方是「成交流水最近 100 笔去重后的代码集」，正常远小于此值；
// 超过即视为调用异常（前端失控/拼参数出错），显式报错而不是把 SQL 拼成无界大 IN。
// English: per-query cap on the code list; exceeding it is a caller bug and errors out
// instead of building an unbounded IN clause.
const MaxStockNamesPerQuery = 200

// ErrTooManyCodes 代码数超过 MaxStockNamesPerQuery 时返回的哨兵错误。
var ErrTooManyCodes = errors.New("stock names: too many codes in one query")

// StockNamesByCodes 批量返回 ts_code→股票名（只读、零网络、查不到的代码不进 map）。
//
// 语义约定（前端按这套口径渲染「—」，不许自己猜）：
//
//	① 入参去空白、去重、大写归一（账本代码本就大写，归一只是防大小写错配）；
//	② 名称为空串或 NULL 的行情壳行跳过——「没有名字」和「查到了但名字是空」都归为缺失；
//	③ 返回的 map 恒非 nil，缺码即少键，绝不返回与请求等长的空值 map（那会让前端把空串当名字显示）。
//
// English: returns ts_code→name for the requested codes; blank names and unknown codes are
// simply absent from the (always non-nil) result map.
func (d *DB) StockNamesByCodes(codes []string) (map[string]string, error) {
	// 归一 + 去重：保持顺序无关，只做集合。
	set := make(map[string]bool, len(codes))
	uniq := make([]string, 0, len(codes))
	for _, c := range codes {
		// 大写归一后去空白；空串跳过（防调用方把 undefined 拼进逗号串）。
		code := strings.ToUpper(strings.TrimSpace(c))
		if code == "" || set[code] {
			continue
		}
		set[code] = true
		uniq = append(uniq, code)
	}
	out := make(map[string]string)
	if len(uniq) == 0 {
		return out, nil
	}
	if len(uniq) > MaxStockNamesPerQuery {
		return nil, ErrTooManyCodes
	}
	// 参数化 IN 子句：代码来自 HTTP 查询串，绝不做字符串直拼（§INSERTLOCK 同族纪律）。
	ph := make([]string, len(uniq))
	args := make([]interface{}, len(uniq))
	for i, c := range uniq {
		ph[i] = "?"
		args[i] = c
	}
	q := `SELECT ts_code, name FROM stocks WHERE ts_code IN (` + strings.Join(ph, ",") + `)`
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(name) == "" {
			continue // 空名行不入图：缺失与空串在调用侧同义
		}
		out[code] = name
	}
	return out, rows.Err()
}
