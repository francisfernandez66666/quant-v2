// amountscale.go — §0929SCALE-⑩（FIX_PLAN_20260929 ⑩ / AUDIT P2-4）日线成交额量纲归一。
//
// 事实（本轮逐字读码锤定，非引用旧注释）：
//   - baostock 腿把 amount 原样落库，单位＝元（cmd/dataload/baostock.go:163/287）；
//   - tushare 接口文档口径 amount＝千元，而 daily/index_daily 两条写入路径同样是原样落库
//     （internal/data/tushare.go:232/262 只声明字段清单，cmd/dataload/main.go 里 grep amount 只命中 baostock 两处）；
//   - 于是「切换 --provider」这一句人手操作，会让同一张 daily 表里同时存在元与千元两套数，
//     差 1000 倍；下游三处（股池流动性质控 internal/store/quality.go、回放成本 internal/btreplay、
//     动量判据）都没有单位判定，**没有任何一条日志会报错**。
//
// 本文件只干一件事：把"单位"从注释里的口头承诺变成写侧的一次换算。
// 换算方向固定为 **归一到元**（与库里现存 785 万行、以及质控阈值 3e7 的口径一致），
// 因此历史行不需要打标、不需要迁移；读侧的逐票自校（btreplay/cost.go fixAmountScale）
// 在归一后的数据上是恒等的（中位均价 ≥1 元 ⇒ 不乘），保留作为混源兜底。
//
// English: §0929SCALE-⑩ — normalize tushare's thousand-CNY `amount` to CNY on the WRITE side,
// so the single `daily` table can never mix two calibers again.
package data

import (
	"encoding/json"
	"strconv"
	"strings"
)

// TushareAmountScale 是 tushare 日线/指数日线 amount 字段与"元"口径的倍率。
// tushare 的 daily.amount 与 index_daily.amount 单位是**千元**，本仓落库契约是**元**。
// （The multiplier between tushare's amount unit (thousand CNY) and this repo's storage caliber (CNY).）
const TushareAmountScale = 1000.0

// AmountScaledTables 列出"tushare 口径需要写侧换算 amount"的表。
// 只有日线家族在这一列语义上是千元；daily_basic 的 total_mv/circ_mv 是**万元**、
// 且下游没有按元比较的阈值，因此都不在此列——把该列清单钉成显式白名单，
// 避免以后加表时被顺手"统一乘一次"变成双重换算。
//
// §W7-D（2026-10-09 波 7）改掉的一句话：旧注释在这里写着"minute_klines 走的是另一条
// hithink/baostock 腿（元）"，把 THS 侧的口径当成已核实事实，而同一批读码发现
// ths_daily 的 amount 来自 parquet 的 turnover 列，该列在本仓两处注释里分别写着
// "换手率（%）"和"成交额/换手率"（internal/data/hithink_dump.go:89/:104，§W7-D 已改）。
// ⇒ 白名单只收"上游单位有核实记录"的表；THS 日 K（ths_daily）不进这张表，
// 它进的是 store 侧的抽检集合 AmountProbedTables——先出读数，读数说千元再谈换算。
// （The explicit whitelist of tables whose tushare `amount` is verifiably in thousand-CNY;
// a table may be probe-eligible without being conversion-eligible.)
var AmountScaledTables = map[string]bool{
	"daily":       true,
	"index_daily": true,
}

// NormalizeTushareAmount 就地把写入载荷里 amount 字段从千元换算为元，返回实际换算的行数。
//
// 约束（都是本轮读码定下的，改这段前先读）：
//  1. 只碰 AmountScaledTables 白名单里的表，其余表原样返回 0，一行都不改；
//  2. 只碰 "amount" 键——vol（手）、close（元）与复权列都不动，均价自校判据依赖它们；
//  3. 值为 nil（NULL 单元格）或不可解析为数值（防御：调用方可能给 json.Number/string）时跳过该行，
//     **不静默写 0**，写 0 会让下游把"缺数"当"零成交"从而剔票；
//  4. 幂等性由调用侧保证：本函数只在 tushare 装载腿调用一次，重复调用会得到 ×1000² 的错值，
//     所以调用点必须紧跟 InsertRows 之前且不在任何重试循环里二次进入（dataload 的断点续传按日期整批重写，
//     整批重新 fetch 再归一，天然安全）。
//
// 返回 changed 用于日志留痕（换算行数 0 而白名单表有行 ⇒ 说明上游字段名变了，要肉眼确认）。
// English: scales the `amount` cell of whitelisted tables from thousand-CNY to CNY in place,
// returning how many rows were converted (0 on a non-whitelisted table).
func NormalizeTushareAmount(table string, rows []map[string]any) int {
	if !AmountScaledTables[table] || len(rows) == 0 {
		return 0
	}
	changed := 0
	for _, r := range rows {
		v, ok := r["amount"]
		if !ok || v == nil {
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			// 解析失败保持原值：宁可让下游自校去判，也不在这里伪造数据。
			continue
		}
		r["amount"] = f * TushareAmountScale
		changed++
	}
	return changed
}

// toFloat 把 map 载荷里的数值单元格读成 float64，兼容装载路径可能出现的四种形态
// （float64 / int / json.Number / 数字字符串）。返回 ok=false 表示这不是数值，调用侧跳过。
// English: best-effort numeric coercion for payload cells; ok=false means "not a number".
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
