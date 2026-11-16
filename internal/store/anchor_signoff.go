package store

// anchor_signoff.go —— 实盘持仓「首买锚点」签字回填（§ANCHOR-SIGNOFF 2026-11-16）。
//
// 为什么需要它
//   做 T 的 step0 开腿门要求 anchor_source ∈ {first_buy_order, first_lot}（AnchorTicket）。
//   现网存量持仓大量是券商快照建行（anchor 为空）或 W3 回填的 cost_fallback 档，
//   两者都不能开 T。要么用真实成交重建首买价，要么——当 owner 能证明该持仓自买入
//   从未加减仓、成本价即首买价时——由 owner 签字把成本价确认为首买锚点。
//
//   本文件实现的是后者：一条**显式、签字、留审计痕迹**的回填通道。
//
// 硬边界（不越过就不算「用摊薄成本冒充首买价」）
//   1. 只处理 qty>0 且当前不能开 T 的行；已有白名单档锚点一律跳过、绝不覆盖。
//   2. anchor_source 写 AnchorSourceFirstBuyOrder（唯一能开 T 的实盘档），
//      anchor_price 取 cost_price —— 但**仅当**调用方携带 owner 签字口令，
//      并逐票声明「买入后无加减仓」事实。
//   3. 每一笔写入同步落 anchor_signoff_log：谁、何时、哪只票、旧值/新值、依据原文。
//      回填可追溯、可复核、可回滚（审计表留存旧值）。
//   4. 缺签字、缺依据、qty<=0、cost<=0 的行一律跳过——宁可不做，不可伪造证据。
//
// English: signed, auditable backfill of real-position first-buy anchors for the live T gate.

import (
	"fmt"
	"time"
)

// AnchorSignoffRow 一行待回填候选（plan 输出 / apply 输入共用）。
type AnchorSignoffRow struct {
	TsCode         string  `json:"ts_code"`
	UserID         string  `json:"user_id"`
	Qty            int     `json:"qty"`
	CostPrice      float64 `json:"cost_price"`
	AnchorPrice    float64 `json:"anchor_price"`
	AnchorSource   string  `json:"anchor_source"`
	ProposedAnchor float64 `json:"proposed_anchor"` // = cost_price
	Eligible       bool    `json:"eligible"`        // qty>0 && cost>0 && 当前不可开T
	Reason         string  `json:"reason"`          // 不可开T原因 / 跳过原因
}

// AnchorSignoffPlan 是 plan/apply 的完整返回：候选 + 汇总，杜绝「只报成功不报补了几条」。
type AnchorSignoffPlan struct {
	Candidates []AnchorSignoffRow `json:"candidates"`
	Eligible   int                `json:"eligible"`   // 可回填行数
	Skipped    int                `json:"skipped"`    // 条件不满足行数
	AlreadyOK  int                `json:"already_ok"` // 已可开T、不动的行数
	Total      int                `json:"total"`      // 有持仓(qty>0)的行数
}

// ensureAnchorSignoffTable 建审计表（幂等）。
func (d *DB) ensureAnchorSignoffTable() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS anchor_signoff_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts_code TEXT NOT NULL,
		user_id TEXT NOT NULL,
		old_anchor_price REAL,
		old_anchor_source TEXT,
		new_anchor_price REAL NOT NULL,
		new_anchor_source TEXT NOT NULL,
		basis TEXT NOT NULL,
		signer TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`)
	return err
}

// anchorTicketEligible 该来源档位是否能开 T —— 与 AnchorTicket 判据同源（同一张白名单）。
func anchorTicketEligible(source string, price float64) bool {
	if price <= 0 {
		return false
	}
	for _, src := range anchorTicketSources {
		if source == src {
			return true
		}
	}
	return false
}

// AnchorSignoffPlanBackfill 只读规划：列出所有有持仓、但当前不能开 T 的行及其建议锚点。
// 不写任何库。
func (d *DB) AnchorSignoffPlanBackfill() (AnchorSignoffPlan, error) {
	plan := AnchorSignoffPlan{}
	if err := d.ensureAnchorPriceColumn(); err != nil {
		return plan, err
	}
	if err := d.ensureAnchorSourceColumn(); err != nil {
		return plan, err
	}
	poses, err := d.RealPositions()
	if err != nil {
		return plan, err
	}
	for _, p := range poses {
		if p.Qty <= 0 {
			continue
		}
		plan.Total++
		row := AnchorSignoffRow{
			TsCode: p.TsCode, UserID: p.UserID, Qty: p.Qty,
			CostPrice: p.CostPrice, AnchorPrice: p.AnchorPrice,
			AnchorSource: p.AnchorSource, ProposedAnchor: p.CostPrice,
		}
		if anchorTicketEligible(p.AnchorSource, p.AnchorPrice) {
			row.Eligible = false
			row.Reason = "already_t_eligible: 已有可开T锚点，不动"
			plan.AlreadyOK++
		} else if p.CostPrice <= 0 {
			row.Eligible = false
			row.Reason = "cost_nonpositive: 无价格信息，不可回填"
			plan.Skipped++
		} else {
			row.Eligible = true
			row.Reason = "当前不可开T（来源=" + dashIfEmpty(p.AnchorSource) + "），签字后以成本价确认为首买锚点"
			plan.Eligible++
		}
		plan.Candidates = append(plan.Candidates, row)
	}
	return plan, nil
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "<空>"
	}
	return s
}

// AnchorSignoffApply 执行签字回填。
//
// signer     : 签字人标识（如 operator / owner 名）
// basis      : 依据原文，必须非空（如「owner 确认 002115 买入后无加减仓，成本价即首买价」）
// onlyCodes  : 非空时只回填这些 ts_code（白名单，防止整批误伤）；为空表示回填 plan 中全部 eligible 行
//
// 返回 (回填行数, 跳过行数, error)。每一行写入前再次校验 eligibility（防 plan→apply 之间状态变化）。
func (d *DB) AnchorSignoffApply(signer, basis string, onlyCodes []string) (filled, skipped int, err error) {
	if signer == "" || basis == "" {
		return 0, 0, fmt.Errorf("signer 与 basis 均不得为空（签字留痕是硬要求）")
	}
	if err := d.ensureAnchorSignoffTable(); err != nil {
		return 0, 0, err
	}
	allow := map[string]bool{}
	for _, c := range onlyCodes {
		allow[c] = true
	}
	poses, err := d.RealPositions()
	if err != nil {
		return 0, 0, err
	}
	now := time.Now().Format(time.RFC3339)
	tx, err := d.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, p := range poses {
		if p.Qty <= 0 || p.CostPrice <= 0 {
			continue
		}
		if anchorTicketEligible(p.AnchorSource, p.AnchorPrice) {
			continue // 已有可开 T 锚点：绝不覆盖（只补不拉）
		}
		if len(allow) > 0 && !allow[p.TsCode] {
			skipped++
			continue
		}
		// 写入首买锚点：source=first_buy_order，price=cost_price（owner 已证明无加减仓）。
		if _, e := tx.Exec(`UPDATE real_positions
			SET anchor_price=?, anchor_source=?
			WHERE ts_code=? AND user_id=? AND qty>0`,
			p.CostPrice, AnchorSourceFirstBuyOrder, p.TsCode, p.UserID); e != nil {
			err = e
			return 0, 0, err
		}
		// 审计留痕：旧值、新值、依据、签字人、时间全落表。
		if _, e := tx.Exec(`INSERT INTO anchor_signoff_log
			(ts_code,user_id,old_anchor_price,old_anchor_source,new_anchor_price,new_anchor_source,basis,signer,applied_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			p.TsCode, p.UserID, p.AnchorPrice, p.AnchorSource,
			p.CostPrice, AnchorSourceFirstBuyOrder, basis, signer, now); e != nil {
			err = e
			return 0, 0, err
		}
		filled++
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return filled, skipped, nil
}

// AnchorSignoffLog 读回审计记录（复核/回滚依据）。
func (d *DB) AnchorSignoffLog() ([]map[string]any, error) {
	if err := d.ensureAnchorSignoffTable(); err != nil {
		return nil, err
	}
	return d.QueryRows(`SELECT ts_code,user_id,old_anchor_price,old_anchor_source,
		new_anchor_price,new_anchor_source,basis,signer,applied_at
		FROM anchor_signoff_log ORDER BY id DESC LIMIT 200`)
}
