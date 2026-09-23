// Package store — SQLite 历史数据存储层。
// fill_conservation.go：§FILL-AMEND（2026-09-23）**账本守恒自检**——只读、只报数，绝不动账。
//
// 为什么必须有它：人工勘误改的是"读取侧方向"，而 real_positions / real_account 两本账是当年
// ApplyRealFill 按**错误方向**写进去的（勘误不重放历史写账，那是自动改账，误伤风险远大于收益，
// 明确不在本项范围）。于是必然出现一种状态：成交簿重放出来的净持仓/净现金流与账本行**对不上**。
// 这个自检就是把这个"对不上"变成可读、可复核、可回归的数字，而不是等它以下游异常形态暴露
// （预算被占满、已实现盈亏恒 0 就是这次的暴露方式）。
//
// 两条不变量（判据见下面两个方法头的注释原文）：
//  1. 持仓数量不变量：按 fills（含已生效勘误）重放的净持仓 = 实盘持仓账（real_positions）；
//     对不上时输出**逐笔**差异线索（代码/重放量/账本量/差额/线索说明），不只报一个布尔。
//  2. 现金不变量：期初资金 − 累计买入(含佣金) + 累计卖出(扣佣金与印花税) = 现金账
//     （real_account.available_cash 券商回报口径）；偏差连同各条腿一起列出。
//
// 范围取向（写死在这里，避免后来者"顺手让它自动修"）：本文件**只 SELECT**。任何 UPDATE/DELETE
// 都不允许出现在这里。
//
// English: §FILL-AMEND read-only conservation self-check. Human amendments rewrite the *read*
// direction only, so the position/cash books (written back then under the wrong direction) can
// legitimately disagree with a replay of the fill ledger. This file surfaces that disagreement as
// per-code lines and cash legs; it never mutates any ledger.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// ConservationPositionLine 持仓不变量的逐笔差异线索。
// English: one per-code line for the position invariant.
type ConservationPositionLine struct {
	Code        string `json:"code"`         // 代码
	ReplayedQty int    `json:"replayed_qty"` // 按成交簿（含生效勘误）重放的净持仓
	BookQty     int    `json:"book_qty"`     // 实盘持仓账 real_positions 的量
	Diff        int    `json:"diff"`         // book − replayed（正=账上多，负=账上少）
	Note        string `json:"note"`         // 差异线索（成因分类，见 classifyPositionDiff）
}

// ConservationCash 现金不变量的各条腿与偏差。
// English: the cash invariant's legs and the resulting deviation.
type ConservationCash struct {
	// Checked=false 时本条**未参与**总判定（数据缺口不假装通过，也不误报失败）：
	// 期初资金未配置或券商可用资金从未回报时，"期望现金"没有可比基准。
	Checked        bool    `json:"checked"`
	SkipReason     string  `json:"skip_reason,omitempty"` // 未检查的原因（如实说明缺哪条腿）
	InitialCapital float64 `json:"initial_capital"`       // 期初资金（配置项，非账本行）
	BuyAmount      float64 `json:"buy_amount"`            // 累计买入成交额（生效方向）
	SellAmount     float64 `json:"sell_amount"`           // 累计卖出成交额（生效方向）
	FeeTotal       float64 `json:"fee_total"`             // 累计佣金（买卖两侧）
	StampTaxTotal  float64 `json:"stamp_tax_total"`       // 累计印花税（卖出腿）
	ExpectedCash   float64 `json:"expected_cash"`         // 期初 − 买入 − 佣金 + 卖出 − 印花税
	BookCash       float64 `json:"book_cash"`             // real_account.available_cash（券商回报口径）
	Diff           float64 `json:"diff"`                  // book − expected（绝对值越大越可疑）
	NetSpend       float64 `json:"net_spend"`             // 买入 − (卖出 − 费用/印花税腿)：净占用现金
}

// ConservationReport 一次守恒自检的完整结果（HTTP 端点直接序列化返回）。
// English: one full conservation check, serialized straight to the read-only endpoint.
type ConservationReport struct {
	UserID string `json:"user_id"`
	Day    string `json:"day"` // 复核截止日（含当日）

	// 生效勘误计数：报告必须自带"这次结论里有多少人工改判"，否则读报表的人无从判断
	// 差异是原始错账还是勘误带来的口径位移。
	AppliedAmendments int `json:"applied_amendments"`

	PositionsChecked int                        `json:"positions_checked"` // 参与比对的去重代码数
	PositionsOK      bool                       `json:"positions_ok"`
	PositionLines    []ConservationPositionLine `json:"position_lines"`

	Cash ConservationCash `json:"cash"`

	OK bool `json:"ok"` // 两条不变量都成立（未检查项按"不失败"处理，见 Cash.Checked）
}

// CheckBookConservation §FILL-AMEND 守恒自检主入口（只读）。
//
// 判据原文（两条不变量）：
//
//	① 持仓数量不变量
//	   replayed(code) = Σ生效买入量 − Σ生效卖出量（卖出侧下限钳 0，与 ApplyRealFill 的
//	   `if newQty < 0 { newQty = 0 }` 同式——不钳就会出现负持仓，比对结果凭空多出假差异）
//	   对每个出现在成交簿或持仓账里的代码，要求 replayed(code) == book(code)
//	② 现金不变量
//	   expected = 期初资金 − Σ买入成交额 − Σ佣金 + Σ卖出成交额 − Σ印花税
//	   要求 expected == real_account.available_cash（券商回报口径）
//
// 范围与账号可见性：本自检刻意比风控闸**更宽**——成交与持仓都按 (user_id = ? OR user_id = ”)
// 取（遗留全局行对所有人可见，与 RealPositionsForUser/GetRealAccount 同口径）。理由：这是排障
// 工具，把遗留全局行藏在视野外会让"账上有仓、成交簿无凭"这一类真实缺陷正好查不出来；
// 风控闸走严格 user_id=? 是权限隔离要求，两者目的不同，不视为口径不一致。
//
// English: read-only conservation check — replays the (amendment-resolved) fill ledger against the
// position book and the cash book, reporting per-code lines and every cash leg.
func (d *DB) CheckBookConservation(userID, day string, initialCapital float64) (*ConservationReport, error) {
	rep := &ConservationReport{UserID: userID, Day: day, PositionLines: []ConservationPositionLine{}}

	// 生效勘误计数（纯上下文信息，不参与判定）。
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM fill_amendments WHERE status='applied'`).Scan(&rep.AppliedAmendments); err != nil {
		return nil, fmt.Errorf("count applied amendments: %w", err)
	}

	// ① 持仓数量不变量：按成交簿重放（截至 day，含当日；日期串取 traded_at 前 10 位，
	// 与全仓 substr(traded_at,1,10) 的日界口径一致）。
	replayed := map[string]int{}
	rows, err := d.db.Query(`SELECT code, side, qty FROM fills_effective
		WHERE (user_id = ? OR user_id = '') AND substr(traded_at,1,10) <= ?
		ORDER BY traded_at ASC, id ASC`, userID, day)
	if err != nil {
		return nil, fmt.Errorf("replay fills: %w", err)
	}
	codes := map[string]bool{}
	for rows.Next() {
		var code, side string
		var qty int
		if err := rows.Scan(&code, &side, &qty); err != nil {
			rows.Close()
			return nil, fmt.Errorf("replay fills row: %w", err)
		}
		codes[code] = true
		switch side {
		case "买入":
			replayed[code] += qty
		case "卖出":
			n := replayed[code] - qty
			if n < 0 {
				n = 0 // 与 ApplyRealFill 的减仓下限同式：不许出现负持仓
			}
			replayed[code] = n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("replay fills cursor: %w", err)
	}
	rows.Close()

	// 实盘持仓账（real_positions）同口径读出，逐代码比对。
	type bookRow struct {
		qty  int
		name string
	}
	book := map[string]bookRow{}
	prows, err := d.db.Query(`SELECT ts_code, qty, COALESCE(name,'') FROM real_positions
		WHERE user_id = ? OR user_id = ''`, userID)
	if err != nil {
		return nil, fmt.Errorf("read position book: %w", err)
	}
	for prows.Next() {
		var code string
		var b bookRow
		if err := prows.Scan(&code, &b.qty, &b.name); err != nil {
			prows.Close()
			return nil, fmt.Errorf("read position book row: %w", err)
		}
		// 同代码同时存在遗留全局行与归属行时取并集（理论上 claim 迁移后不会并存，
		// 但自检宁可把两行相加也不静默丢弃其中一行——丢行本身就可能是要查的问题）。
		b.qty += book[code].qty
		book[code] = b
		codes[code] = true
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, fmt.Errorf("read position book cursor: %w", err)
	}
	prows.Close()

	rep.PositionsChecked = len(codes)
	rep.PositionsOK = true
	for _, code := range sortedKeys(codes) {
		rq, bk := replayed[code], book[code]
		if rq == bk.qty {
			continue
		}
		rep.PositionsOK = false
		rep.PositionLines = append(rep.PositionLines, ConservationPositionLine{
			Code: code, ReplayedQty: rq, BookQty: bk.qty, Diff: bk.qty - rq,
			Note: classifyPositionDiff(rq, bk.qty),
		})
	}

	// ② 现金不变量。金额腿与纪律账同式（amount>0 用落库成交额，旧格式回报回落 price×qty）。
	var buyAmt, sellAmt, feeAmt, stampAmt sql.NullFloat64
	if err := d.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN side='买入'  THEN (CASE WHEN amount>0 THEN amount ELSE price*qty END) ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN side='卖出'  THEN (CASE WHEN amount>0 THEN amount ELSE price*qty END) ELSE 0 END),0),
			COALESCE(SUM(COALESCE(fee,0)),0),
			COALESCE(SUM(COALESCE(stamp_tax,0)),0)
		FROM fills_effective WHERE (user_id = ? OR user_id = '') AND substr(traded_at,1,10) <= ?`,
		userID, day).Scan(&buyAmt, &sellAmt, &feeAmt, &stampAmt); err != nil {
		return nil, fmt.Errorf("sum cash legs: %w", err)
	}
	cash := ConservationCash{
		InitialCapital: initialCapital,
		BuyAmount:      valueOr(buyAmt),
		SellAmount:     valueOr(sellAmt),
		FeeTotal:       valueOr(feeAmt),
		StampTaxTotal:  valueOr(stampAmt),
	}
	// 净占用现金：买入 − (卖出 − 费用/印花税腿)。指令钉的就是这个口径，单独回显便于人工复核。
	cash.NetSpend = cash.BuyAmount - (cash.SellAmount - cash.FeeTotal - cash.StampTaxTotal)
	cash.ExpectedCash = initialCapital - cash.BuyAmount - cash.FeeTotal + cash.SellAmount - cash.StampTaxTotal
	acc, err := d.GetRealAccount(userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read cash book: %w", err)
	}
	cash.BookCash = acc.AvailableCash
	cash.Diff = acc.AvailableCash - cash.ExpectedCash
	switch {
	case initialCapital <= 0:
		cash.SkipReason = "期初资金未配置（qmt.initial_capital≤0）→ 期望现金无基准"
	case acc.AvailableCash <= 0:
		cash.SkipReason = "券商可用资金未回报（real_account.available_cash≤0）→ 无现金账可比"
	default:
		cash.Checked = true
	}
	rep.Cash = cash

	// 总判定：持仓条必须成立；现金条**只在检查过**时参与判定（缺腿是数据缺口，不是账错，
	// 如实以 Checked=false + SkipReason 标出，绝不折叠成"通过"或"失败"）。
	rep.OK = rep.PositionsOK && (!cash.Checked || absF(cash.Diff) <= cashToleranceYuan)
	return rep, nil
}

// cashToleranceYuan 现金不变量的判定容差（元）：券商可用资金回报是四舍五入到分的口径，
// 且本自检的期望值不含隔夜利息/红利/过户费等未入账腿——留出 1 元容差，超过即如实列出。
// 注意容差只影响 OK 判定，Diff 与各腿数值一律原样输出（自检的职责是报数，不是判"差不多"）。
const cashToleranceYuan = 1.0

// absF 浮点绝对值。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// classifyPositionDiff 给差异配一句可操作的成因线索（自检的输出必须能直接指导下一步动作，
// 只报布尔值等于没做）。
// English: turns a mismatch into an actionable clue instead of a bare boolean.
func classifyPositionDiff(replayed, book int) string {
	switch {
	case replayed == 0 && book > 0:
		return "账上有仓、成交簿无凭：券商对账快照建的持仓（成交从未回报），或该笔成交被勘误改判后成交簿已不再解释这份持仓"
	case replayed > 0 && book == 0:
		return "成交簿有净买入、账上无仓：持仓行被 qty<=0 清仓分支删除，或对账快照把它清空——勘误生效后这是最常见形态（当年的错误买入曾把量加进账，后又卖出）"
	case book > replayed:
		return fmt.Sprintf("账比成交簿多 %d 股：成交簿少记（漏回报/交割单未补记）或卖出被误记成买入后已勘误（历史写账未回改）", book-replayed)
	default:
		return fmt.Sprintf("账比成交簿少 %d 股：成交簿多记（重复回报）或买入被误记成卖出后已勘误（历史写账未回改）", replayed-book)
	}
}

// sortedKeys map 键升序（报告必须可 diff：同一份数据两次自检的输出要逐字一致）。
// English: deterministic ordering so two runs on the same data produce identical output.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// valueOr NullFloat64 → float64（NULL 按 0 计，与 SQL 侧 COALESCE 一致）。
func valueOr(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}
