// Package store — SQLite 历史数据存储层。
// manual_adjust.go：§MANUAL-BOOK-UNIFY（2026-11-06，owner 裁决 2/3）「增量改账」的存储层写面。
//
// 一句话：把"手动维护"从一张要整张重交的清单，改成四个能点着的动作
// （加仓/减仓/止盈/清仓）+ 一个改账新增 + 一个直接改资金，让实仓 tab 成为唯一出入口。
//
// 四条不许被"顺手优化"掉的边界：
//  1. **只写 manual_*，永不写 real_***：柜台为准这条判据的前提是柜台账只由回报与真实成交推进
//     （见 manual_book.go 文件头）。改账值参与硬闸走读侧 resolver 的显式选源，不走灌表。
//  2. **单事务 read-modify-write**：增量语义下"读当前值→算新值→写回"三步拆开，
//     并发两次改账会丢一笔量（后写的按它读到的旧值覆盖），而丢的那一笔正好是钱。
//  3. **基线从哪本来必须点名**（seeded_from）：柜台账有行而手动账没行时，第一笔改账拿柜台行做基线；
//     资金基线同理。不点名的话，事后没人能解释"这个数是人敲的还是继承昨天柜台的"。
//  4. **卖出侧沿用柜台那两条纪律**：qty<100 且不是全卖 ⇒ 拒（与 §F16"卖出不支持零股"同口径）。
//     改账不是"随便写数字"的口子——它记的是人在券商真实做过的那一笔，形状必须和真单一致。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 改账动作名（写面 body 与流水表 op 列共用同一组字面量）。
const (
	ManualOpNew     = "new"      // 改账·新增：柜台从没回报过的一码，手工记进账
	ManualOpAdd     = "add"      // 加仓
	ManualOpReduce  = "reduce"   // 减仓
	ManualOpTp      = "tp"       // 止盈（卖出方向，与 reduce 同腿，动作名留给读数面分形）
	ManualOpClose   = "close"    // 清仓（整行删除）
	ManualOpSetCash = "set_cash" // 直接改可用资金（手续费/分红/出入金的兜平口）
)

// 基线来源（流水 seeded_from 列与响应共用）。
const (
	ManualSeedCounter = "counter" // 从 real_positions / real_account 继承
	ManualSeedManual  = "manual"  // 手动账已有行
	ManualSeedNone    = "none"    // 全新建行（new）
)

// ErrInvalidManualAdjust 增量改账参数校验失败的哨兵错误：整笔拒收，一条都不写。
// 为什么整笔拒而不是跳过坏 op：一次改账同时动持仓与资金两本值，写一半留一半就是自相矛盾的账。
var ErrInvalidManualAdjust = errors.New("改账参数校验失败")

// ManualAdjustReq 一次增量改账（server 层已完成 asof 归一与归属判定）。
type ManualAdjustReq struct {
	Op     string   // ManualOp* 之一
	TsCode string   // 持仓类动作必填（set_cash 忽略）
	Qty    int      // 本笔股数
	Price  float64  // 本笔成交价（持仓类动作必填）
	Cash   *float64 // 仅 set_cash：nil＝本次不改资金
	AsOf   string   // 已归一的"这份账是几点"
	User   string   // 操作者（审计留痕用，与 user_id 同值时才有意义）
}

// ManualAdjustResult 改账后的读数回显（响应层直接透传，让录入人当场看见服务端认下来的值）。
type ManualAdjustResult struct {
	Op           string  `json:"op"`
	TsCode       string  `json:"ts_code"`
	Qty          int     `json:"qty"`           // 该码改后持仓量（已删行则 0）
	DealQty      int     `json:"deal_qty"`      // 本笔数量（清仓时＝账上现量，不是请求量）
	Price        float64 `json:"price"`         // 本笔成交价
	CostPrice    float64 `json:"cost_price"`    // 该码改后成本（加权）
	Deleted      bool    `json:"deleted"`       // 清仓/减仓归零 ⇒ 行已删除
	Cash         float64 `json:"cash"`          // 账户改前可用资金
	CashAfter    float64 `json:"cash_after"`    // 账户改后可用资金
	SeededFrom   string  `json:"seeded_from"`   // 持仓基线来源
	CashSeeded   string  `json:"cash_seeded"`   // 资金基线来源
	AmountDelta  float64 `json:"amount_delta"`  // 本笔对资金的影响
	PositionRows int     `json:"position_rows"` // 该账号手动持仓总行数（改后）
}

// manualBaseline 一次改账读到的基线（持仓行 + 资金）。
type manualBaseline struct {
	pos          ManualPosition
	posFound     bool
	posSeeded    string // manual|counter|none
	cash         float64
	cashFilled   bool
	cashSeeded   string // manual|counter|none
	posRowsAfter int    // 该账号手动持仓行数（进事务时读一次，set_cash 分支直接回显）
}

// AdjustManualBook 在**一个事务**里完成：读基线 → 校验 → 写持仓/资金 → 插流水。
//
// day 由调用方给（与 risk/engine 的交易日口径同源，测试可注入）；本函数不读墙钟，
// 免得"今天"这件事在存储层再定义一次。
func (d *DB) AdjustManualBook(userID string, req ManualAdjustReq, day string) (ManualAdjustResult, error) {
	if err := d.ensureManualBook(); err != nil {
		return ManualAdjustResult{}, err
	}
	// real_account 是惰性建表（首次柜台上报才创建，见 real_account.go ensureRealAccountTable）。
	// 改账要读它做资金基线：表不存在时必须先建，否则"no such table"会被当成读失败，
	// 把一次合法改账拒成"存储坏了"——而真实原因只是这台机器从没收到过柜台资金回报。
	if err := d.ensureRealAccountTable(); err != nil {
		return ManualAdjustResult{}, err
	}
	if strings.TrimSpace(day) == "" {
		return ManualAdjustResult{}, fmt.Errorf("%w: day 为空（改账流水必须落在具体交易日）", ErrInvalidManualAdjust)
	}
	switch req.Op {
	case ManualOpNew, ManualOpAdd, ManualOpReduce, ManualOpTp, ManualOpClose, ManualOpSetCash:
	default:
		return ManualAdjustResult{}, fmt.Errorf("%w: 未知动作 op=%q", ErrInvalidManualAdjust, req.Op)
	}
	code := strings.TrimSpace(req.TsCode)
	if req.Op != ManualOpSetCash {
		if !validManualTsCode(code) {
			return ManualAdjustResult{}, fmt.Errorf("%w: ts_code=%q 为空或非法格式（应为 6位数字+.SH/.SZ/.BJ）",
				ErrInvalidManualAdjust, req.TsCode)
		}
		if req.Qty <= 0 {
			return ManualAdjustResult{}, fmt.Errorf("%w: %s 的 qty=%d 必须为正（清仓请用清仓动作，不是填 0）",
				ErrInvalidManualAdjust, code, req.Qty)
		}
		if req.Price <= 0 {
			return ManualAdjustResult{}, fmt.Errorf("%w: %s 的成交价 %.2f 非法（改账要记真实成交价）",
				ErrInvalidManualAdjust, code, req.Price)
		}
	} else if req.Cash == nil {
		return ManualAdjustResult{}, fmt.Errorf("%w: set_cash 必须带 available_cash", ErrInvalidManualAdjust)
	} else if *req.Cash < 0 {
		return ManualAdjustResult{}, fmt.Errorf("%w: 可用资金为负（%.2f）", ErrInvalidManualAdjust, *req.Cash)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return ManualAdjustResult{}, err
	}
	defer tx.Rollback()

	base, err := d.readManualBaseline(tx, userID, code)
	if err != nil {
		return ManualAdjustResult{}, err
	}

	res := ManualAdjustResult{Op: req.Op, TsCode: code, SeededFrom: base.posSeeded, CashSeeded: base.cashSeeded}

	if req.Op == ManualOpSetCash {
		// 直接改资金：不动持仓。资金基线在这里没有"继承柜台"的问题——owner 就是在给一个终值。
		res.Cash = base.cash
		res.CashAfter = *req.Cash
		res.AmountDelta = *req.Cash - base.cash
		if err := d.writeManualCash(tx, userID, *req.Cash, base, req.AsOf); err != nil {
			return res, err
		}
		if err := insertManualFlow(tx, userID, day, "", req.Op, "资金", 0, 0, res.AmountDelta, res.CashAfter,
			base.cashSeeded, req.AsOf); err != nil {
			return res, err
		}
		res.PositionRows = base.posRowsAfter
		if err := tx.Commit(); err != nil {
			return res, err
		}
		return res, nil
	}

	// ── 持仓类动作 ──
	// 资金基线：既无手动行又无柜台读数时**拒**，不许从 0 起算。
	// 为什么：从 0 起算会让"卖出一笔"凭空造出现金，而"买出一笔"直接判负数——
	// 两个方向都是假账，且都朝着放钱的方向错。给一句可操作的拒绝，比给一个能算的错数有用。
	if base.cashSeeded == ManualSeedNone {
		return res, fmt.Errorf("%w: 可用资金既没有手动行也读不到柜台值，无法联动；请先用「直接改资金」录入基线",
			ErrInvalidManualAdjust)
	}
	newQty := base.pos.Qty
	newCost := base.pos.CostPrice
	deleted := false
	var delta float64
	var side string

	switch req.Op {
	case ManualOpNew:
		if base.posFound && base.posSeeded == ManualSeedManual {
			return res, fmt.Errorf("%w: %s 已在手动持仓里，加仓请用「加仓」而不是「新增」", ErrInvalidManualAdjust, code)
		}
		if base.posFound {
			// 柜台账里有这一码 ⇒ "新增"其实是在给已有仓位记一笔，走 add 才是加权语义。
			return res, fmt.Errorf("%w: %s 柜台账里已有持仓（%d 股），请用「加仓」改账（新增只用于柜台从未回报过的票）",
				ErrInvalidManualAdjust, code, base.pos.Qty)
		}
		side = "买入"
		newQty = req.Qty
		newCost = req.Price
		delta = -float64(req.Qty) * req.Price
	case ManualOpAdd:
		if !base.posFound {
			return res, fmt.Errorf("%w: %s 既不在手动账也不在柜台账，请用「改账·新增」建仓", ErrInvalidManualAdjust, code)
		}
		side = "买入"
		// 加权成本：旧成本为 0（没填）时不能参与加权，否则会把新价稀释成 0。
		oldCost := base.pos.CostPrice
		if oldCost <= 0 {
			newCost = req.Price
		} else {
			newCost = (float64(base.pos.Qty)*oldCost + float64(req.Qty)*req.Price) / float64(base.pos.Qty+req.Qty)
		}
		newQty = base.pos.Qty + req.Qty
		delta = -float64(req.Qty) * req.Price
	case ManualOpReduce, ManualOpTp:
		if !base.posFound {
			return res, fmt.Errorf("%w: %s 不在账上，减不了", ErrInvalidManualAdjust, code)
		}
		side = "卖出"
		if req.Qty > base.pos.Qty {
			return res, fmt.Errorf("%w: %s 减仓 %d 股超过持仓 %d 股", ErrInvalidManualAdjust, code, req.Qty, base.pos.Qty)
		}
		// §F16 同口径：卖出必须整手，除非这一笔就是把整行卖光（残股清仓走 close）。
		if req.Qty < 100 && req.Qty != base.pos.Qty {
			return res, fmt.Errorf("%w: %s 卖出 %d 股不足一手；要清残股请用「清仓」", ErrInvalidManualAdjust, code, req.Qty)
		}
		newQty = base.pos.Qty - req.Qty
		delta = float64(req.Qty) * req.Price
		if newQty == 0 {
			deleted = true
		}
	case ManualOpClose:
		if !base.posFound {
			return res, fmt.Errorf("%w: %s 不在账上，无需清仓", ErrInvalidManualAdjust, code)
		}
		side = "卖出"
		// 清仓量一律取**账上现量**，不取请求量：请求量与现量不一致说明两端读到的账不同步，
		// 此时按请求量算钱会把一笔说不清的差额加进现金。
		newQty = 0
		deleted = true
		delta = float64(base.pos.Qty) * req.Price
		req.Qty = base.pos.Qty
	}

	res.Deleted = deleted
	res.Qty = newQty
	res.CostPrice = newCost
	res.Price = req.Price
	res.DealQty = req.Qty
	res.Cash = base.cash
	res.CashAfter = base.cash + delta
	res.AmountDelta = delta

	// 三写一事务：持仓行 → 资金行 → 当日改账流水。拆开任何一步都会留下
	// 「持仓变了、资金没变」或「账变了、流水查不到」的半态；提交失败整笔回滚。
	// English: position, cash and the day-keyed flow row are written in ONE transaction.
	if err := d.writeManualPosition(tx, userID, code, base, newQty, newCost, deleted, req.AsOf); err != nil {
		return res, err
	}
	if err := d.writeManualCash(tx, userID, res.CashAfter, base, req.AsOf); err != nil {
		return res, err
	}
	if err := insertManualFlow(tx, userID, day, code, req.Op, side, req.Qty, req.Price, delta, res.CashAfter,
		base.posSeeded, req.AsOf); err != nil {
		return res, err
	}
	rows, err := countManualPositions(tx, userID)
	if err != nil {
		return res, err
	}
	res.PositionRows = rows
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// readManualBaseline 在同一事务里读持仓基线与资金基线，并点名各自从哪本来。
// 柜台账兜底只在手动账**没有**该码行时启用：柜台为准的前提是读侧选源，写侧继承一次基线
// 不等于此后一直跟柜台走（下一次改账仍先读手动行）。
func (d *DB) readManualBaseline(tx *sql.Tx, userID, code string) (manualBaseline, error) {
	var b manualBaseline
	// 手动持仓行数（改后回显用，先读一次当前值即可，写完后另有 count 复核）
	var err error
	if b.posRowsAfter, err = countManualPositions(tx, userID); err != nil {
		return b, err
	}
	var qty int
	var cost float64
	var name, asof string
	err = tx.QueryRow(`SELECT name, qty, cost_price, asof FROM manual_positions WHERE user_id = ? AND ts_code = ?`,
		userID, code).Scan(&name, &qty, &cost, &asof)
	switch {
	case err == nil:
		b.pos = ManualPosition{TsCode: code, Name: name, Qty: qty, CostPrice: cost}
		b.posFound = true
		b.posSeeded = ManualSeedManual
	case errors.Is(err, sql.ErrNoRows):
		// 手动账没这一码 ⇒ 试柜台账（桥断时 real_positions 停在最后一次回报，正是断链当天的真实持仓）。
		var rqty int
		var rcost float64
		rerr := tx.QueryRow(`SELECT name, qty, cost_price FROM real_positions WHERE user_id = ? AND ts_code = ?`,
			userID, code).Scan(&name, &rqty, &rcost)
		if rerr == nil && rqty > 0 {
			b.pos = ManualPosition{TsCode: code, Name: name, Qty: rqty, CostPrice: rcost}
			b.posFound = true
			b.posSeeded = ManualSeedCounter
		} else {
			b.pos = ManualPosition{TsCode: code}
			b.posSeeded = ManualSeedNone
		}
	default:
		return b, fmt.Errorf("read manual position baseline: %w", err)
	}

	// 资金基线同理：无手动行时从柜台 real_account 种子，且 cash_filled=false 不冒充
	// 「用户确认过余额」；种子来源随流水落库，事后能对清这段空白是谁给的。
	// English: cash baseline is seeded from the counter row when absent, never faked as confirmed.
	var cash float64
	var filled int
	accErr := tx.QueryRow(`SELECT available_cash, cash_filled FROM manual_account WHERE user_id = ?`, userID).
		Scan(&cash, &filled)
	switch {
	case accErr == nil:
		b.cash = cash
		b.cashFilled = filled != 0
		if b.cashFilled {
			b.cashSeeded = ManualSeedManual
		}
	case !errors.Is(accErr, sql.ErrNoRows):
		return b, fmt.Errorf("read manual cash baseline: %w", accErr)
	}
	if !b.cashFilled {
		// 手动资金不可用（没行 / 有行但没填）⇒ 从柜台资金继承一次基线。
		var counterCash float64
		cErr := tx.QueryRow(`SELECT available_cash FROM real_account WHERE user_id = ?`, userID).Scan(&counterCash)
		if cErr == nil {
			b.cash = counterCash
			b.cashSeeded = ManualSeedCounter
		} else if !errors.Is(cErr, sql.ErrNoRows) {
			return b, fmt.Errorf("read counter cash baseline: %w", cErr)
		} else {
			b.cashSeeded = ManualSeedNone
		}
	}
	return b, nil
}

// writeManualPosition 写回该码的持仓行；deleted=true 时删行（清单里不留 0 股行，与全量语义同纪律）。
func (d *DB) writeManualPosition(tx *sql.Tx, userID, code string, base manualBaseline, qty int, cost float64, deleted bool, asof string) error {
	if deleted {
		_, err := tx.Exec(`DELETE FROM manual_positions WHERE user_id = ? AND ts_code = ?`, userID, code)
		if err != nil {
			return fmt.Errorf("delete manual position %s: %w", code, err)
		}
		return nil
	}
	_, err := tx.Exec(`INSERT INTO manual_positions (ts_code, name, qty, cost_price, source, asof, updated_at, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ts_code, user_id) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), manual_positions.name),
			qty=excluded.qty,
			cost_price=excluded.cost_price,
			asof=excluded.asof,
			updated_at=excluded.updated_at`,
		code, strings.TrimSpace(base.pos.Name), qty, cost, ManualSourceLabel, asof, ManualNow(), userID)
	if err != nil {
		return fmt.Errorf("upsert manual position %s: %w", code, err)
	}
	return nil
}

// writeManualCash 写回账户资金。
// total_asset 刻意**不**在这里推算：它是"现金+市值"的合计，改账只动了现金与若干持仓行，
// 拿不到实时价就算不出市值，凭空写一个合计会让下游展示读到一个自相矛盾的数
// （与 manual_book.go 里"只带总资产会被拒"同取向：宁可不动，不许猜）。
func (d *DB) writeManualCash(tx *sql.Tx, userID string, cash float64, base manualBaseline, asof string) error {
	_, err := tx.Exec(`INSERT INTO manual_account
		(user_id, available_cash, total_asset, cash_filled, source, asof, updated_at)
		VALUES (?, ?, 0, 1, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			available_cash=excluded.available_cash,
			cash_filled=1,
			asof=excluded.asof,
			updated_at=excluded.updated_at`,
		userID, cash, ManualSourceLabel, asof, ManualNow())
	if err != nil {
		return fmt.Errorf("upsert manual cash: %w", err)
	}
	return nil
}

// insertManualFlow 落一行改账流水（当日 T+1 扣减与资金联动的唯一凭据）。
func insertManualFlow(tx *sql.Tx, userID, day, code, op, side string, qty int, price, delta, cashAfter float64, seededFrom, asof string) error {
	_, err := tx.Exec(`INSERT INTO manual_flows
		(user_id, day, ts_code, op, side, qty, price, amount_delta, cash_after, seeded_from, asof, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, day, code, op, side, qty, price, delta, cashAfter, seededFrom, asof, ManualNow())
	if err != nil {
		return fmt.Errorf("insert manual flow: %w", err)
	}
	return nil
}

// countManualPositions 该账号手动持仓行数（改后回显）。
func countManualPositions(tx *sql.Tx, userID string) (int, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM manual_positions WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count manual positions: %w", err)
	}
	return n, nil
}

// ManualFlowsForUser 读某账号某交易日之后的改账流水（读数面与人工对账用，倒序）。
// day 为空＝不限日。上限 500 行：这是"一个人改过哪些"的回看面，不是导出面；
// 超限时调用方必须显式翻页或按日收窄，静默截断会让人把"没显示"读成"没改过"。
func (d *DB) ManualFlowsForUser(userID, day string) ([]ManualFlow, error) {
	if err := d.ensureManualBook(); err != nil {
		return nil, err
	}
	q := `SELECT id, user_id, day, ts_code, op, side, qty, price, amount_delta, cash_after, seeded_from, asof, created_at
		FROM manual_flows WHERE user_id = ?`
	args := []any{userID}
	if day != "" {
		q += ` AND day = ?`
		args = append(args, day)
	}
	q += ` ORDER BY id DESC LIMIT 500`
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualFlow
	for rows.Next() {
		var f ManualFlow
		if err := rows.Scan(&f.ID, &f.UserID, &f.Day, &f.TsCode, &f.Op, &f.Side, &f.Qty, &f.Price,
			&f.AmountDelta, &f.CashAfter, &f.SeededFrom, &f.AsOf, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
