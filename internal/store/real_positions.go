// Package store — SQLite 历史数据存储层。
// real_positions.go：实盘账本（AUTO_TRADING_PLAN M1）的真实持仓/委托/成交存取。
// 数据源为国内 QMT 网关回报（全量对账 positions 事件 + 增量 trade/order 事件），
// 与纸面账本（report.Report JSON）完全独立（双账本并存）。
// English: real-book persistence for AUTO_TRADING_PLAN M1 — real positions/orders/fills written from
// the domestic QMT gateway reports (full reconciliation + incremental trade/order events), fully
// independent of the paper book (report.Report JSON). Dual ledgers coexist.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/opslog"
)

// RealPosition 实盘持仓行。
// （RealPosition is one row of the live book.）
type RealPosition struct {
	TsCode       string  `json:"ts_code"`       // TS代码
	Name         string  `json:"name"`          // 名称
	Qty          int     `json:"qty"`           // 数量
	CostPrice    float64 `json:"cost_price"`    // 成本价
	Amount       float64 `json:"amount"`        // 成交额
	HighestPrice float64 `json:"highest_price"` // 持仓以来最高价（加仓/格局判定用）
	Strategy     string  `json:"strategy"`      // 战法
	SignalID     string  `json:"signal_id"`     // 信号ID
	UpdatedAt    string  `json:"updated_at"`    // 更新时间
	// BuyDate §PROD-T1（2026-09-18）：建仓交易日（YYYY-MM-DD，ApplyRealFill 买入首成交落库）。
	// 实盘建议视图 trading.execLogsFromReal 用它填 ExecLog.EntryAt，让"持仓超期离场"按真实
	// 持仓天数判定；空串=开仓日未知（如券商快照对账建的行），下游超期判定自动跳过。
	// English: §PROD-T1 — opening trade date recorded on the first buy fill; empty = unknown
	// (e.g. rows created by broker-snapshot reconcile), and timeout checks skip when unknown.
	BuyDate string `json:"buy_date,omitempty"`
	// CurPrice §前端实盘持仓现价/盈亏展示用：由 handleRealPositions 装配实时行情快照填充，
	// 网关回报本身不含实时价（仅 cost_price）。English: live price for the real-position table;
	// filled from the quote snapshot by handleRealPositions, not carried in gateway reports.
	CurPrice float64 `json:"cur_price"` // 实时现价（浮动盈亏计算用）
	// UserID §GAP1.10 多租户归属：网关回报 user_id 写入；空串=遗留全局行（对所有人可见，
	// 兼容单老板存量部署）。English: §GAP1.10 owner account; empty = legacy global row.
	UserID string `json:"user_id,omitempty"` // 归属用户
}

// RealOrder 实盘委托单行（signal_id 为幂等键）。
// （RealOrder is one live order ticket; signal_id is the idempotency key.）
type RealOrder struct {
	OrderID   string  `json:"order_id"`          // 订单ID
	SignalID  string  `json:"signal_id"`         // 信号ID
	Code      string  `json:"code"`              // 代码
	Side      string  `json:"side"`              // 方向
	Status    string  `json:"status"`            // 状态
	Price     float64 `json:"price"`             // 价格
	Qty       int     `json:"qty"`               // 数量
	CreatedAt string  `json:"created_at"`        // 创建时间
	UserID    string  `json:"user_id,omitempty"` // §W2-10 归属账号（空=遗留全局行）
}

// RealFill 实盘成交回报行。
// （RealFill is one live fill report.）
type RealFill struct {
	ID       int64   `json:"id"`        // 自增 ID
	OrderID  string  `json:"order_id"`  // 订单ID
	Code     string  `json:"code"`      // 代码
	Name     string  `json:"name"`      // 名称（成交回报携带，建仓回填）
	Side     string  `json:"side"`      // 方向
	Price    float64 `json:"price"`     // 价格
	Qty      int     `json:"qty"`       // 数量
	Amount   float64 `json:"amount"`    // 成交额
	TradedAt string  `json:"traded_at"` // 成交时间
	SignalID string  `json:"signal_id"` // 信号ID
	// TradeID §M4（2026-09-22 PM 批）券商成交编号：网关回报一直携带，旧 Go 信封没有该 tag
	// → 本地 fills 只剩复合键判重（同委托同秒同价同量的两笔真实部成会被误判为重放丢单）。
	// 空串=券商未给（旧行/交割单回灌路径），判重自动退回复合键。
	// English: §M4 — broker trade number, long emitted by the gateway but silently dropped by the
	// old Go envelope. Empty (legacy rows / settlement backfill) falls back to composite dedup.
	TradeID  string  `json:"trade_id,omitempty"`
	UserID   string  `json:"user_id,omitempty"`   // §W2-10 归属账号
	Fee      float64 `json:"fee,omitempty"`       // §WS-B 手续费（交割单回灌）
	StampTax float64 `json:"stamp_tax,omitempty"` // §WS-B 印花税（交割单回灌）
	Serial   string  `json:"serial,omitempty"`    // §WS-B 券商交割流水号（三方对账关联键）
}

// realTsCodeRe §F2（2026-09-22 修复批）：持仓对账行 ts_code 的合法格式——
// 6位数字 + .SH/.SZ/.BJ 交易所后缀（与 data.ExchangeSuffix 产出口径一致）。
// English: §F2 — the only accepted ts_code shape on reconcile rows: 6 digits + .SH/.SZ/.BJ.
var realTsCodeRe = regexp.MustCompile(`^[0-9]{6}\.(SH|SZ|BJ)$`)

// ErrInvalidPositionReport §F2：对账快照字段级校验失败的哨兵错误——任一行 ts_code 为空或
// 非法格式即整批拒收（不落库），上层（handleQMTReport）据 errors.Is 映射为 HTTP 400。
// 锤实背景（FIX_PLAN §6.2）：一行 ts_code=” 的垃圾行会永久误触发「本地有仓+空快照 409
// 守卫」，真实全平再也无法经对账通道落账；且主键 (ts_code,user_id) 下的空串行本身就是脏数据。
// English: §F2 sentinel — any invalid ts_code rejects the whole snapshot before any write;
// the HTTP layer maps it to 400 via errors.Is.
var ErrInvalidPositionReport = errors.New("positions 快照字段校验失败")

// costBasisDropped §N-6（2026-09-22 傍晚批复验，裁决 11=本地含费优先）守卫**留痕**判定：
// 本轮对账快照的 cost_price 是否会被保护丢弃。它不参与落库——保护由 upsert 里的 CASE 完成，
// 本函数只负责让保护不再"自身静默"（本批主题是静默失效）。两处口径必须逐项一致：
// 本地 >0 即保留本地，此时快照值只要与本地不同就是被丢弃的那个值。
// English: §N-6 observability predicate — whether the snapshot's fee-less cost is being dropped
// by the local-wins guard (the guard itself lives in SQL; this only makes it visible).
func costBasisDropped(localCost, snapshotCost float64) bool {
	return localCost > 0 && snapshotCost != localCost
}

// CostGuardDrops §N-6 累计计数：对账快照成本被本地含费基准丢弃的次数（进程内，重启归零）。
// 用途：排障时区分"守卫从没触发"与"守卫一直在挡"——现网若持续增长说明券商侧 open_price
// 缺失/口径不符，是数据源问题而非账本问题；测试用它断言守卫确实生效过。
func (d *DB) CostGuardDrops() int64 { return d.costGuardDrops.Load() }

// validateRealPositions §F2：全量对账入口校验。空快照（len==0，合法全平语义）直接放行；
// 任一行 ts_code 空或非法格式 → 返回包装了 ErrInvalidPositionReport 的错误（含行号与原值），
// 调用方必须整批拒收并 opslog 留痕，绝不部分落库。
// English: §F2 entry validation for full reconciliation snapshots; empty = legit flat passes,
// any malformed ts_code rejects the entire batch with row context.
func validateRealPositions(pos []RealPosition) error {
	for i := range pos {
		if !realTsCodeRe.MatchString(pos[i].TsCode) {
			return fmt.Errorf("%w: 第 %d 行 ts_code=%q 为空或非法格式（应为 6位数字+.SH/.SZ/.BJ）",
				ErrInvalidPositionReport, i, pos[i].TsCode)
		}
	}
	return nil
}

// UpsertRealPositions 全量对账写入：以网关推送的持仓集合为准，逐条 upsert 并移除已不在集合内的旧持仓。
// 返回替换后的持仓数量。English: full-reconciliation write — upserts every gateway position and drops
// rows absent from the push; returns the resulting position count.
//
// ⚠ 入口定位（§N-6 复验登记，2026-09-22 傍晚批）：**测试专用入口**。生产链路（Controller.Reconcile
// / MaybeReconcile / handleQMTReport 的 positions 事件）一律走账号隔离版 ReconcilePositionsForUser，
// 全仓 grep 证实本函数只有 *_test.go 调用者（多租户下它的"空快照 DELETE 全表"分支是历史清库炸弹，
// 现按 user_id 作用域收敛，但仍不该被生产复用）。函数按要求保留不删，改动与 ReconcilePositionsForUser
// 同步：列保护口径、§N-6 成本基准守卫两边一致，避免"测试路径测的是一条生产不再走的语句"。
func (d *DB) UpsertRealPositions(pos []RealPosition) (int, error) {
	// §F2（2026-09-22 修复批）：字段级校验前置——任一行 ts_code 非法即整批拒收不落库。
	if err := validateRealPositions(pos); err != nil {
		log.Printf("[store] §F2 持仓对账整批拒收: %v", err)
		opslog.Logf("quant", "持仓对账整批拒收(§F2 字段校验)：%v", err)
		return 0, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, p := range pos {
		if p.UpdatedAt == "" {
			p.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
		}
		// §P0-1 多租户：带归属账号的对账先声明遗留全局行，避免复合主键冲突产生重复行。
		if p.UserID != "" {
			if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, p.UserID, p.TsCode); err != nil {
				return 0, fmt.Errorf("claim legacy position %s: %w", p.TsCode, err)
			}
		}
		// §N-6 守卫留痕（与 ReconcilePositionsForUser 同款，测试专用入口同步）：
		// 快照成本被本地含费账丢弃时打 debug 日志 + 计数，保护不得自身静默。
		var localCost float64
		if err := tx.QueryRow(`SELECT cost_price FROM real_positions WHERE ts_code=? AND user_id=?`,
			p.TsCode, p.UserID).Scan(&localCost); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("read local cost %s: %w", p.TsCode, err)
		}
		if costBasisDropped(localCost, p.CostPrice) {
			d.costGuardDrops.Add(1)
			log.Printf("[store] §N-6 快照成本被本地含费账丢弃(裁决11，测试专用入口) %s 用户=%s：快照 %.4f → 保留本地 %.4f",
				p.TsCode, p.UserID, p.CostPrice, localCost)
		}
		_, err := tx.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code, user_id) DO UPDATE SET
				-- §M4（2026-09-22 PM 批）空名快照不得抹空已有名称：券商持仓快照常不带 name，
				-- 旧实现 excluded.name('') 直覆盖 → 实盘持仓页只剩代码。与同语句 strategy 的
				-- §M5 保护同口径（COALESCE(NULLIF(新,''),旧)）。
				-- English: §M4 — keep the stored name when the snapshot carries none (same guard as §M5).
				name=COALESCE(NULLIF(excluded.name,''), real_positions.name),
				-- qty 有意裸写（份额数以柜台为准，只增守卫会把已卖出的量永久留在账上→超卖）。
				qty=excluded.qty,
				-- §N-6（裁决 11=本地含费优先）与 ReconcilePositionsForUser 同款 CASE 保护：
				-- 券商快照的不含费 open_price（缺失时=0）绝不覆盖本地已有的非零含费成本。
				cost_price=CASE WHEN real_positions.cost_price > 0
					THEN real_positions.cost_price ELSE excluded.cost_price END,
				-- amount 与 cost_price 同源推导（选定成本 × 快照数量），杜绝 qty×cost≠amount 错配。
				amount=(CASE WHEN real_positions.cost_price > 0
					THEN real_positions.cost_price ELSE excluded.cost_price END) * excluded.qty,
				-- §M5（2026-09-22 修复批）：strategy 仅在快照携带非空值时覆盖——券商快照不带
				-- 战法归因，旧实现 excluded.strategy('') 直插会把本地战法标记洗成空串。
				-- 对照同语句 highest_price 的 CASE 保护，同函数保护口径现已补齐。
				strategy=COALESCE(NULLIF(excluded.strategy,''), real_positions.strategy),
				updated_at=excluded.updated_at,
				user_id=excluded.user_id,
				-- signal_id 有意不出现在 SET 列表：本入口无归因来源时保持旧值（与
				-- ReconcilePositionsForUser 的 COALESCE 非空才覆盖等效，见 §N-6 同族裸写盘点）。
				highest_price=CASE WHEN excluded.highest_price > real_positions.highest_price
					THEN excluded.highest_price ELSE real_positions.highest_price END`,
			p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID)
		if err != nil {
			return 0, fmt.Errorf("upsert real position %s: %w", p.TsCode, err)
		}
	}
	// 移除网关推送中已不存在的持仓（全量对账语义）。
	// §修复 R9（2026-08-29）：原 len(pos)==0 分支是 `DELETE FROM real_positions` 全表删除，
	// 多租户下任一账号推送空快照即清掉所有账号持仓（清库炸弹）。现按 user_id 作用域删除：
	// 仅当 pos 含归属账号时才限定到这些账号行；无归属账号(旧单租户/测试)保留原行为。
	userSet := make(map[string]bool)
	for _, p := range pos {
		if p.UserID != "" {
			userSet[p.UserID] = true
		}
	}
	if len(pos) == 0 {
		if len(userSet) == 0 {
			// 旧单租户 / 测试：保留原整表清空语义
			if _, err := tx.Exec(`DELETE FROM real_positions`); err != nil {
				return 0, err
			}
		} else {
			// 多租户空快照：仅清这些账号行，绝不动他人持仓
			args := make([]any, 0, len(userSet))
			ph := ""
			for u := range userSet {
				if ph != "" {
					ph += ","
				}
				ph += "?"
				args = append(args, u)
			}
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id IN (`+ph+`)`, args...); err != nil {
				return 0, err
			}
		}
	} else {
		codes := make([]any, 0, len(pos))
		placeholders := ""
		for i, p := range pos {
			codes = append(codes, p.TsCode)
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
		}
		if len(userSet) == 0 {
			// 旧单租户 / 测试：原整表 NOT IN 语义
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE ts_code NOT IN (`+placeholders+`)`, codes...); err != nil {
				return 0, err
			}
		} else {
			// 多租户：仅删这些账号、且不在推送集合内的行
			args := make([]any, 0, len(codes)+len(userSet))
			ph := ""
			for u := range userSet {
				if ph != "" {
					ph += ","
				}
				ph += "?"
				args = append(args, u)
			}
			args = append(args, codes...)
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id IN (`+ph+`) AND ts_code NOT IN (`+placeholders+`)`, args...); err != nil {
				return 0, err
			}
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM real_positions`).Scan(&n); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ReconcilePositionsForUser §R3-8 P1-G 用户隔离的全量对账写入（Controller.Reconcile 专用）：
//   - 每行打 UserID 归属后 upsert；
//   - 删除范围限定在 本账号行 ∪ 遗留全局行（user_id=”）——绝不动其他账号的 scoped 行
//     （旧 UpsertRealPositions 空集合分支是 DELETE FROM real_positions 全表，多租户下是清库炸弹）；
//   - pos 为空 = 网关全平，调用方必须已做「通道在线」守卫（网关 /state 断连时也返回空列表，
//     不可信快照禁止清账）。
//
// English: R3-8 P1-G — user-scoped full reconciliation write: stamps ownership on every row,
// deletes only this account's rows plus legacy global rows; never touches other accounts' rows.
// An empty pos means "gateway flat" — the caller must have verified the channel is connected.
func (d *DB) ReconcilePositionsForUser(userID string, pos []RealPosition) (int, error) {
	// §F2（2026-09-22 修复批）：字段级校验前置——任一行 ts_code 非法即整批拒收不落库，
	// 上层据 ErrInvalidPositionReport 回 400（网关 outbox 按 4xx 永久拒绝进死信，不无限重推）。
	if err := validateRealPositions(pos); err != nil {
		log.Printf("[store] §F2 持仓对账整批拒收(用户=%s): %v", userID, err)
		opslog.Logf("quant", "持仓对账整批拒收(§F2 字段校验) 用户=%s：%v", userID, err)
		return 0, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, p := range pos {
		if p.UpdatedAt == "" {
			p.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
		}
		if userID != "" {
			p.UserID = userID
		}
		// §P0-1 多租户主键：同一股票先声明遗留全局行，避免复合主键冲突产生重复行。
		// English: claim any legacy global row for this account before upserting.
		if userID != "" {
			if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, userID, p.TsCode); err != nil {
				return 0, fmt.Errorf("claim legacy position %s: %w", p.TsCode, err)
			}
		}
		// §N-6（2026-09-22 傍晚批复验，owner 裁决 11=本地含费优先）成本基准守卫留痕：
		// 先读本地既有成本，判断本轮快照是否会被丢弃——守卫本身若也"静默"，就还是本批要根治的
		// 那类静默失效（券商 open_price 缺失时按 0 落库、含费成本被洗掉，下游三条判定线输入消失
		// 却零日志）。故命中丢弃时打一条 debug 日志 + 递增计数（CostGuardDrops 可读）。
		var localCost float64
		if err := tx.QueryRow(`SELECT cost_price FROM real_positions WHERE ts_code=? AND user_id=?`,
			p.TsCode, p.UserID).Scan(&localCost); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("read local cost %s: %w", p.TsCode, err)
		}
		if costBasisDropped(localCost, p.CostPrice) {
			d.costGuardDrops.Add(1)
			opslog.OncePer("reconcile-cost-basis:"+p.UserID+"|"+p.TsCode, 10*time.Minute, func() {
				log.Printf("[store] §N-6 对账成本守卫生效(本地含费优先，裁决11) %s 用户=%s：券商快照 cost_price=%.4f 已丢弃，保留本地 %.4f（amount 同步按本地成本×快照数量推导）",
					p.TsCode, p.UserID, p.CostPrice, localCost)
			})
		}
		_, err := tx.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code, user_id) DO UPDATE SET
				-- §M4（2026-09-22 PM 批）与 UpsertRealPositions 同口径：空名快照不抹空已有名称。
				name=COALESCE(NULLIF(excluded.name,''), real_positions.name),
				-- qty 有意保持裸写：份额数量只有券商柜台知道（本地卖出成交虽会同步扣减，但
				-- 断连期间的成交只有快照能纠正），加"只增"守卫会把已卖出的量永久留在账上→超卖。
				qty=excluded.qty,
				-- §N-6 裁决 11=本地含费优先：cost_price/amount 是**含费**成本基准（成交回报路径
				-- ApplyRealFill 按 (旧含费账+成交额+佣金)/新量 落同一列），券商快照给的是**不含费**
				-- open_price（缺失时更是直接 or 0，与 0 同归 0）→ 旧实现裸写 excluded.* 等于每 5 分钟
				-- 对账把含费成本洗成不含费、甚至归零，且已落库、重启救不回。现只在本地为 0/缺失时
				-- 用快照兜底，已有非零含费值一律保留（与下方 highest_price 的 CASE 同款保护）。
				-- ⚠ 副作用（裁决已知并接受）：券商侧对"非零但错"的成本不再有纠正通道，人工纠偏须走
				-- 成交回报或直接改库。
				-- English: §N-6 ruling 11 — the fee-inclusive local cost basis wins; the snapshot only
				-- back-fills when the local value is 0/missing.
				cost_price=CASE WHEN real_positions.cost_price > 0
					THEN real_positions.cost_price ELSE excluded.cost_price END,
				-- amount 与 cost_price **同源推导**（=上面选定的每股成本 × 本行最终数量），
				-- 绝不允许"新快照量 × 旧成本"与"旧 amount"两套口径并存 → qty×cost≠amount 的错配
				-- 会让 ApplyRealFill 的加仓加权（以 p.Amount 为含费累计账）从错误基数起算。
				amount=(CASE WHEN real_positions.cost_price > 0
					THEN real_positions.cost_price ELSE excluded.cost_price END) * excluded.qty,
				-- §M5（2026-09-22 修复批）：strategy/signal_id 是本地战法归因链（复盘/审计/补卖
				-- 定位都靠它），券商对账快照恒不带这两字段——旧实现 excluded.* 直插等于每次对账
				-- 把归因洗成空串。仅当来源非空才覆盖（COALESCE(NULLIF(新,''),旧)），
				-- 与同语句 highest_price 的 CASE 保护同口径。
				strategy=COALESCE(NULLIF(excluded.strategy,''), real_positions.strategy),
				signal_id=COALESCE(NULLIF(excluded.signal_id,''), real_positions.signal_id),
				updated_at=excluded.updated_at, user_id=excluded.user_id,
				-- §M5 只增锚 → §N-7 起本地锚点会由 RaiseRealPositionHigh 持续抬高：这里
				-- max(本地, 快照) 的只增语义保证快照 open_price（券商口径=开仓价）绝不会把
				-- 已回写的期间最高价拉回，两条写路径（对账 upsert / 锚点 UPDATE）因此互不覆盖。
				highest_price=CASE WHEN excluded.highest_price > real_positions.highest_price
					THEN excluded.highest_price ELSE real_positions.highest_price END`,
			p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID)
		if err != nil {
			return 0, fmt.Errorf("reconcile real position %s: %w", p.TsCode, err)
		}
	}
	// 清除本账号范围内已不在网关快照中的行。
	if len(pos) == 0 {
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id = ?`, userID); err != nil {
			return 0, err
		}
	} else {
		codes := make([]any, 0, len(pos))
		placeholders := ""
		for i, p := range pos {
			codes = append(codes, p.TsCode)
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
		}
		// §修复 P2#17：非空快照分支必须连同清理不在快照中的遗留全局行（user_id=''）。
		// 旧实现只删 `user_id=?` 的作用域行，遗留全局行既未被本账号声明（claim 只对快照内的
		// ts_code 执行），又不在快照中 → 成为永驻残留：对全账号可见、Reconcile 计数虚高、
		// 前向对账后还会把这些无主行误当成真实持仓展示。真正的多租户 scoped 行不会被误删；
		// 可靠的遗留行走 claim 路径已在本事务前转成 scoped，这里删掉的只能是"无任何账号认领"
		// 的过期行。English: P2#17 — the non-empty branch must also purge legacy global rows (user_id='')
		// absent from the snapshot. The old code deleted only this account's scoped rows, so a legacy row
		// that was neither claimed by this snapshot (claim runs only for codes present in pos) nor present
		// in it lingered forever: visible to every account, inflating the reconciliation count, and
		// re-surfacing as phantom positions after a full reconcile. Real multi-tenant scoped rows are
		// untouched; a genuine legacy row gets converted to scoped earlier in this transaction via the
		// claim UPDATE, so what this deletes is only ownerless stale rows.
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE (user_id = ? OR user_id = '') AND ts_code NOT IN (`+placeholders+`)`,
			append([]any{userID}, codes...)...); err != nil {
			return 0, err
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM real_positions WHERE user_id = '' OR user_id = ?`, userID).Scan(&n); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// RealPositions 返回全部实盘持仓（含成本/最高价），供决策层读取。
// （RealPositions returns every live position for the decision layer.）
func (d *DB) RealPositions() ([]RealPosition, error) {
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions ORDER BY ts_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealPosition
	for rows.Next() {
		var p RealPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RealPositionsForUser §GAP1.10 按账号过滤实盘持仓：返回 user_id 匹配或遗留全局行（user_id=”）。
// English: §GAP1.10 — positions owned by the account plus legacy global (empty user_id) rows.
func (d *DB) RealPositionsForUser(userID string) ([]RealPosition, error) {
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,''), COALESCE(buy_date,'') FROM real_positions
		WHERE user_id = '' OR user_id = ? ORDER BY ts_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealPosition
	for rows.Next() {
		var p RealPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID, &p.BuyDate); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RealPositionByCode 返回单只实盘持仓（不存在返回 sql.ErrNoRows）。
// ⚠️ 多账号部署下应使用 RealPositionByCodeForUser；本函数保留以兼容遗留单租户调用。
// （RealPositionByCode returns one live position, sql.ErrNoRows when absent.）
func (d *DB) RealPositionByCode(code string) (RealPosition, error) {
	var p RealPosition
	err := d.db.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions WHERE ts_code=?`, code).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID)
	return p, err
}

// RealPositionByCodeForUser §P0-3 按账号返回单只实盘持仓；遗留全局行（user_id=”）对查询账号可见。
// English: user-scoped single position lookup; legacy global rows are visible to any caller.
func (d *DB) RealPositionByCodeForUser(userID, code string) (RealPosition, error) {
	var p RealPosition
	err := d.db.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions
		WHERE ts_code=? AND (user_id = '' OR user_id = ?)`, code, userID).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID)
	return p, err
}

// ApplyRealFill 成交回报应用到持仓：
//   - 买入：首次建仓（成本=成交价）或加仓（合并加权平均成本），并更新最高价；
//   - 卖出：减仓 qty；全部卖出后 qty<=0（下次全量对账/查询时清除）。
//
// 成交回报同时写 fills 表。English: applies a gateway fill to the book: buys open/add with weighted
// average cost, sells reduce qty (cleared when qty<=0); the fill row is also persisted.
func (d *DB) ApplyRealFill(f RealFill) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// §P0-1 多租户：成交前先声明遗留全局行，防止后续 INSERT 产生重复。
	if f.UserID != "" {
		if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, f.UserID, f.Code); err != nil {
			return fmt.Errorf("claim legacy position before fill %s: %w", f.Code, err)
		}
	}

	var p RealPosition
	err = tx.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, user_id FROM real_positions WHERE ts_code=? AND (user_id='' OR user_id=?)`, f.Code, f.UserID).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount, &p.HighestPrice, &p.Strategy, &p.SignalID, &p.UserID)
	switch {
	case err == sql.ErrNoRows:
		// 首次成交：建仓（卖出空仓视为 no-op，仅记录 fills，不建行）。
		// §实盘账户隔离：INSERT 必须写入 user_id，将该持仓归属到来源成交的账号，
		// 否则该持仓会落入 user_id='' 的遗留全局行，对所有账号可见，造成跨账号持仓泄漏。
		err = nil
		if f.Side == "买入" {
			// §修复 R8（2026-08-29）：建仓时回填成交携带的 name（此前硬编码空串，
			// 导致实盘持仓页个股名称为空）。
			// §WS-A T+1：建仓记录买入交易日 buy_date（北京时，T+1 可卖量判定用）。
			buyDate := buyDateOf(f.TradedAt)
			// §F1（2026-09-22 修复批）：佣金摊入持仓成本（每股 =(成交额+fee)/量），
			// 与 paper 口径 `p.Cost += cost + fee`（paper.go:1480）对齐——旧实现成本只含
			// 成交均价，费用腿被凭空抹掉，实盘账面系统性偏乐观、settlement_diff.fee_diff
			// 永不收敛。印花税为卖出环节税费，不摊买入成本。
			buyCostPerShare := f.Price
			amountWithFee := f.Price * float64(f.Qty)
			if f.Qty > 0 {
				amountWithFee = f.Price*float64(f.Qty) + f.Fee
				buyCostPerShare = amountWithFee / float64(f.Qty)
			}
			_, err = tx.Exec(`INSERT INTO real_positions
				(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id, buy_date)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				f.Code, f.Name, f.Qty, buyCostPerShare, amountWithFee, f.Price, "", f.SignalID,
				time.Now().Format("2006-01-02 15:04:05"), f.UserID, buyDate)
		}
	case err == nil:
		now := time.Now().Format("2006-01-02 15:04:05")
		// 统一归属到本次成交账号（遗留空行被声明后 p.UserID 已等于 f.UserID）。
		ownerID := f.UserID
		if ownerID == "" {
			ownerID = p.UserID
		}
		if f.Side == "买入" {
			newQty := p.Qty + f.Qty
			var newCost float64
			if newQty > 0 {
				// §F1：加仓加权平均同样把佣金摊入本笔成本（p.Amount 本身已是含费累计账，
				// 首仓/历史加仓都按含费口径滚动），与 paper `p.Cost += cost + fee` 同式。
				newCost = (p.Amount + f.Price*float64(f.Qty) + f.Fee) / float64(newQty)
			}
			hi := p.HighestPrice
			if f.Price > hi {
				hi = f.Price
			}
			// §实盘账户隔离：加仓时一并回写 user_id，确保归属字段不被旧行残值覆盖。
			// §修复 R8：加仓时若原持仓 name 为空，用本次成交的 name 回填。
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, cost_price=?, amount=?,
				highest_price=?, updated_at=?, user_id=?, name=CASE WHEN name='' OR name IS NULL THEN ? ELSE name END
				WHERE ts_code=? AND (user_id='' OR user_id=?)`,
				newQty, newCost, newCost*float64(newQty), hi, now, ownerID, f.Name, f.Code, f.UserID)
		} else {
			newQty := p.Qty - f.Qty
			if newQty < 0 {
				newQty = 0
			}
			// §实盘账户隔离：减仓时同样回写 user_id（减仓不改变归属，但保持写路径一致，
			// 防止历史上 user_id 为空的持仓在减仓后仍以全局行形态存在）。
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, amount=?, updated_at=?, user_id=? WHERE ts_code=? AND (user_id='' OR user_id=?)`,
				newQty, float64(newQty)*p.CostPrice, now, ownerID, f.Code, f.UserID)
		}
	}
	if err != nil {
		return fmt.Errorf("apply fill %s %s: %w", f.Code, f.Side, err)
	}
	// §WS-A A3 作用域化清仓：只删"本次成交归属账号（∪遗留全局行）"下的 qty<=0 行，
	// 绝不 `DELETE FROM real_positions WHERE qty<=0` 扫全表（多账号下会误删他人刚清仓的零仓行）。
	if f.UserID == "" {
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE qty <= 0 AND user_id=''`); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE qty <= 0 AND (user_id='' OR user_id=?)`, f.UserID); err != nil {
			return err
		}
	}
	// §WS-A A2 结构化幂等：先按复合唯一键 (order_id,traded_at,price,qty) 显式判重——
	// 网关 outbox 对同笔回报重试时，命中即视为已入账，整体事务回滚并返回幂等成功，
	// 持仓数量不再被二次累加（此前依赖 SQLite 错误文案匹配，驱动措辞变化即静默失效）。
	// §M4（2026-09-22 PM 批）判重锚优先券商成交编号：复合键把"同委托+同秒+同价+同量"的两笔
	// 真实部成也判成重放——第二笔被静默丢弃（少记成交 → 持仓虚高、卖出侧会拿不存在的量下单）。
	// trade_id 券商保证唯一，非空时以它为准；缺省时退回复合键，行为与旧版兼容。
	// English: §M4 — prefer the broker's unique trade number for replay detection; the composite
	// key could silently discard a second genuine partial fill in the same second. Fall back to
	// the composite key when the gateway reports no trade_id.
	var dup int
	if f.TradeID != "" {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM fills WHERE trade_id=?`, f.TradeID).Scan(&dup); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM fills WHERE order_id=? AND traded_at=? AND price=? AND qty=?`,
			f.OrderID, f.TradedAt, f.Price, f.Qty).Scan(&dup); err != nil {
			return err
		}
	}
	if dup > 0 {
		log.Printf("[store] fills 幂等命中(重复回报): order=%s trade_id=%s traded_at=%s qty=%d",
			f.OrderID, f.TradeID, f.TradedAt, f.Qty)
		return tx.Rollback()
	}
	if _, err := tx.Exec(`INSERT INTO fills (order_id, code, side, price, qty, amount, traded_at, signal_id, user_id, fee, stamp_tax, serial, trade_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.OrderID, f.Code, f.Side, f.Price, f.Qty, f.Amount, f.TradedAt, f.SignalID, f.UserID, f.Fee, f.StampTax, f.Serial, f.TradeID); err != nil {
		return err
	}
	return tx.Commit()
}

// buyDateOf 从成交时间串提取买入交易日（北京时 yyyy-MM-dd）；解析失败回退到 TradedAt 前 10 字符
// （TradedAt 通常为 RFC3339 或 "yyyy-MM-dd HH:mm:ss"，前缀即日期）。空串返回 ""。
// English: extracts the buy trading-day from a fill's traded-at string (Beijing date); falls back to
// the first 10 chars (RFC3339 / SQL datetime prefix). Empty when the input is empty.
func buyDateOf(tradedAt string) string {
	if tradedAt == "" {
		return ""
	}
	if len(tradedAt) >= 10 {
		return tradedAt[:10]
	}
	return tradedAt
}

// TodayBoughtQty §WS-A T+1 可卖量辅助：统计某账号某代码在指定北京交易日的累计买入量
// （含部成，不含撤单——fills 只记真实成交）。供卖出侧做 T+1 前置校验：
// 可卖量 = 持仓量 − 当日买入量（当日买入的份额 T+1 才能卖）。
// English: WS-A T+1 sell-availability helper — sums a code's today-bought qty per user/day,
// so sellable = held − todayBought (same-day buys are T+1 locked).
func (d *DB) TodayBoughtQty(userID, tsCode, day string) int {
	var n int
	if err := d.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM fills
		WHERE user_id=? AND code=? AND side='买入' AND substr(traded_at,1,10)=?`,
		userID, tsCode, day).Scan(&n); err != nil {
		return 0
	}
	return n
}

// BuyableQtyForUserSell §WS-A T+1：返回某账号某代码"可立即卖出"的数量（持仓 − 当日买入）。
// 遗留全局行（user_id=”）对该账号可见。English: WS-A T+1 sellable qty = held − todayBought.
func (d *DB) BuyableQtyForUserSell(userID, tsCode, day string) int {
	p, err := d.RealPositionByCodeForUser(userID, tsCode)
	if err != nil {
		return 0
	}
	bought := d.TodayBoughtQty(userID, tsCode, day)
	q := p.Qty - bought
	if q < 0 {
		q = 0
	}
	return q
}

// LocalBuyFrozen §WS-M 本地在途冻结金额：某账号当日"已报/部成"买单中仍未成交部分的金额。
//   - 已报：全额计入（amount = price×qty）；
//   - 部成：按 amount − 已成交额（SumFilledQty 按 signal_id 前缀聚合）计入；
//   - 发送失败/已撤/已成/废单：不计。
//
// 用途：券商口径不可信（过期/缺失）时，用本地账本显式计冻结，防并发信号超买。
// §C1（2026-09-22 修复批）：day 参数（北京日 2006-01-02）此前声明而未使用——历史停在
// 已报/部成 的买单会跨日永久占用当日冻结额，静默锁死买入预算（调用方 gate.go 一直按
// 「撤单即消失」的当日语义使用）。现按 substr(created_at,1,10)=day 过滤（RFC3339 与
// "2006-01-02 15:04:05" 两种存量格式前 10 位均为北京日，一并覆盖）；created_at 为空的
// 行不计入冻结（与旧口径差异仅限异常行）。查询错误改为上抛而非吞成 0——旧 fail-open
// 会让 DB 故障期间的冻结直接归零放水。
// English: §C1 — the day argument is now enforced (Beijing-date prefix match on created_at) so
// cross-day stale 已报/部成 buys no longer freeze the daily budget forever; query errors surface
// to the caller (fail-closed) instead of silently returning 0.
func (d *DB) LocalBuyFrozen(userID, day string) (float64, error) {
	rows, err := d.db.Query(`SELECT signal_id, status, price, qty FROM orders
		WHERE user_id=? AND side='买入' AND (status='已报' OR status='部成')
		AND substr(created_at,1,10)=?`, userID, day)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	frozen := 0.0
	for rows.Next() {
		var sid, status string
		var price float64
		var qty int
		if err := rows.Scan(&sid, &status, &price, &qty); err != nil {
			continue
		}
		filled := d.SumFilledQty(userID, sid)
		remain := qty - filled
		if remain < 0 {
			remain = 0
		}
		frozen += price * float64(remain)
	}
	// 游标迭代错误同样上抛（旧实现静默丢弃半截结果当全额有效）。
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return frozen, nil
}

// SweepStaleBuyOrders §C1b（2026-09-22 修复批）跨日陈旧买单无条件降级：
// created_at 早于 beforeDay（北京日，不含）且仍停在 已报/部成 的买单一律置为 废单终态留痕。
// A 股委托当日收盘即失效，跨日挂单永不可能是活单；旧行为下这些行的解冻完全依赖
// SweepOrders 的网关撤单路（!Enabled/Tripped/cancel_stale_sec=-1 时整体跳过），
// 网关崩溃/断连留下的 已报 行会长期以僵尸单形态滞留委托簿。本方法是纯本地账操作，
// 不触网关、不受熔断管辖，作为 C1 日期过滤之外的终态兜底。
// English: §C1b — unconditional local demotion of cross-day 已报/部成 buys to terminal 废单;
// A-share orders die at close, and this path must not depend on gateway reachability.
func (d *DB) SweepStaleBuyOrders(userID, beforeDay string) (int64, error) {
	if beforeDay == "" {
		return 0, nil
	}
	res, err := d.db.Exec(`UPDATE orders SET status='废单'
		WHERE user_id=? AND side='买入' AND status IN ('已报','部成')
		AND substr(created_at,1,10) <> '' AND substr(created_at,1,10) < ?`,
		userID, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpsertRealOrder 写入/更新委托单；signal_id 冲突时返回已存在（幂等，不重复下单）。
// 返回 (是否已存在, 错误)。English: upserts an order; a signal_id conflict means the order already
// exists (idempotent — never double-sends). Returns (alreadyExisted, err).
func (d *DB) UpsertRealOrder(o RealOrder) (bool, error) {
	// §W2-10 租户列：委托行打归属账号（存量行为空串=遗留全局）
	res, err := d.db.Exec(`INSERT OR IGNORE INTO orders
		(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt, o.UserID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return true, nil
	}
	return false, nil
}

// UpdateRealOrderStatus §WS-A A4 收权：无用户作用域版本。历史裸 UPDATE 可回退委托真实进度，
// 现已统一委托给带单调秩守卫的实现（UpdateRealOrderStatusMonotonic），杜绝状态倒卷。
// 保留签名兼容遗留调用；全仓只有定义无调用（grep 证实），可安全保持为薄包装。
// English: §WS-A A4 — legacy no-scope status update now routes through the monotonic rank-guarded
// implementation so order progress can never regress. Kept as a thin wrapper for legacy callers.
func (d *DB) UpdateRealOrderStatus(orderID, status string) error {
	_, err := d.UpdateRealOrderStatusMonotonic("", orderID, status)
	return err
}

// SumFilledQty 汇总某账号某 signal_id 前缀的累计成交数量（按 (user_id, signal_id) 过滤）。
// §修复 R6（2026-08-29）：自动卖出为日级幂等键，若首笔卖单仅部成（部分成交），同日同键重试会被
// 唯一键判 duplicate 而剩余仓位不再卖出。此处累计已成交数量，供补卖逻辑计算剩余可卖量。
// §修复 FIX#1（2026-09-04）：卖出单 signal_id 是 base（sell:<码>:<类>:<日>）追加 :r<剩余量> 后缀
// 的完整键——网关回报的 fills 恒带该后缀，按 base 精确匹配恒为 0，导致补卖逻辑把"已成交量"看成 0、
// 对尚未成交的旧挂单叠加发单（卖出敞口超额）。改为按 base 前缀聚合，base 与所有 :rN 桶的成交都计入。
// English: §FIX#1 — sell fills carry the full signal id (base + ":r<remaining>" suffix), so an exact
// match on base always returned 0 and re-sells overlapped pending old orders (oversold exposure).
// Aggregate by signal_id prefix to count fills across the base and every :rN bucket.
func (d *DB) SumFilledQty(userID, signalID string) int {
	var total int
	if err := d.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM fills WHERE user_id=? AND signal_id LIKE ?||'%'`,
		userID, signalID).Scan(&total); err != nil {
		return 0
	}
	return total
}

// SumOpenSellQty §P0-2（2026-09-15）+ §N-3（2026-09-22 傍晚批复验）汇总某账号某代码
// 「当日仍在途卖单的**未成交余量**」股数——状态 ∈ {已报,部成,已报待撤,部成待撤}（可能继续成交），
// 不含已成/已撤/部撤/废单终态，也不含「发送失败」占位行（从未到达券商，重试前不该占额度）。
// 用途算式（三项扣减，调用方 scoring_loop.realSoldOrOpenQtyToday 逐项对应）：
//
//	卖出剩余量 = 持仓量 − Σ已成交(SumFilledQty) − Σ在途(SumOpenSellQty，本函数=未成交余量)
//
// 只统计「卖出」方向且当日（created_at 北京时前缀）的委托，买入在途不受影响。
//
// §N-3 口径改写（资金缺陷）：旧实现对 `status='部成'` 的委托按**整笔委托 qty** 计入在途，
// 而调用方 `scoring_loop.realSoldOrOpenQtyToday` 的算式是「Σ已成交 + Σ在途」，其中 Σ已成交
// （SumFilledQty）已经把该单的成交部分数过一次 → **同一笔成交被扣两次**。
// 数值反例（持仓 2000 / 挂单 1000 / 部成 500，成交回报已把账面持仓降到 1500）：
//   - 旧：remaining = 1500 −(500+1000) = 0 → 剩余量恒为 0，当天**永不补卖**（少卖=敞口留过夜）；
//   - 新：remaining = 1500 −(500+500) = 500 → 以 500 补卖；再部成 300 后以 200 补；全成后归 0 不再发单。
//
// 标准来源（**买卖两侧「部成」定义必须同源**）：docs/BUGFIX_BUDGET_FREEZE_LEDGER_20260918.md
// §BUDGET_FREEZE 在买入侧钉死的「部成 = 未成交余量、一笔在途单不得被扣两次」，与本函数净额口径
// 逐字一致（同族实现见 LocalBuyFrozen 的 remain = qty − 已成交）。
// 成交归属按 order_id **或** signal_id 双键匹配：下单回填（UpdateRealOrderBySignalID）失败时本地行
// order_id 仍停留 `pend:` 前缀，只按 order_id 关联会恒得 0 成交 → 退化成整笔占额，正是本条要根除的
// 双扣形态；signal_id 在 orders 上有 UNIQUE(user_id,signal_id)，一单一行不会串到别的委托。
//
// 剩余量下限钳 0：成交理论上不会超过委托量，但网关重放/交割单回灌可造成 filled>qty 的异常行，
// 此时按 0 计——宁可少占额度，也不许出现「负在途量」把剩余量抬高造成超卖。
//
// English: §N-3 — sums today's still-open sell tickets at their **unfilled remainder**
// (qty minus that ticket's fills), the same definition the buy-side §BUDGET_FREEZE ledger pins for 部成.
// The old whole-order qty double-counted the filled part against the caller's Σfilled term, so a
// partially filled sell could never be topped up during the rest of the day.
func (d *DB) SumOpenSellQty(userID, tsCode, day string) int {
	rows, err := d.db.Query(`SELECT o.qty,
		COALESCE((SELECT SUM(f.qty) FROM fills f
			WHERE f.order_id = o.order_id
			   OR (o.signal_id <> '' AND f.signal_id = o.signal_id)), 0)
		FROM orders o
		WHERE (o.user_id = '' OR o.user_id = ?) AND o.code = ? AND o.side = '卖出'
		  AND o.created_at LIKE ?||'%'
		  AND o.status IN ('已报','部成','已报待撤','部成待撤')`,
		userID, tsCode, day)
	if err != nil {
		// 与旧实现同为 fail-open（回 0 = 不占额度），但不再静默：本函数是「卖出剩余量」与
		// 「T+1 可卖量」两道闸的共同输入，查询失败会让两道闸同时失明，必须留下可见痕迹。
		log.Printf("[store] §N-3 在途卖量查询失败(按 0 计，不占额度) user=%s code=%s: %v", userID, tsCode, err)
		return 0
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var qty, filled int
		if err := rows.Scan(&qty, &filled); err != nil {
			log.Printf("[store] §N-3 在途卖量行读取失败(跳过该行): %v", err)
			continue
		}
		if remain := qty - filled; remain > 0 {
			total += remain
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[store] §N-3 在途卖量游标错误(已累计 %d 股，按不完整值计): %v", total, err)
	}
	return total
}

// RaiseRealPositionHigh §N-7（2026-09-22 傍晚批复验）把运行期抬高的**持仓期最高价**回写实盘账本，
// 语义与对账 upsert 里 highest_price 的 CASE 只增完全一致（单调不降）。
//   - 为什么需要它：卖出裁决内核（signalctl.sellProbe）的锚点是「播种 + 每轮自抬」，live 侧的播种值
//     就取自本列（engine/sell_shadow.go 探针 HighPrice）。此前该列只有三个写入源（券商快照
//     open_price、成交回报成交价、CASE 只增的比较），**期间最高价从无写入路径** → 进程重启即回落到
//     建仓价，涨过 15% 再回落的仓位移动止盈永不触发（§LIVEANCHOR）。
//   - 为什么不学 paper 新增 json：live 的账本就是 sqlite，再开一份文件会让同一规则有两份真相
//     （§M10 的 json 持久化按设计只覆盖 paper 一本）。
//   - WHERE 上的 `highest_price < ?` 就是只增守卫本体（并发/重放下都不会把高点拉回），返回值表示
//     是否真的写了一行——调用方据此决定留痕，不做无变更写盘。
//
// English: §N-7 — raises real_positions.highest_price with the in-process trailing-stop anchor
// (monotonic; the WHERE clause is the same only-up rule the reconcile CASE applies). The boolean
// reports whether a row actually changed.
func (d *DB) RaiseRealPositionHigh(userID, tsCode string, high float64) (bool, error) {
	if high <= 0 {
		return false, nil // 无有效锚点绝不写 0：0 会让下游 DrawdownPct 与移动止盈整条线失明
	}
	res, err := d.db.Exec(`UPDATE real_positions
		SET highest_price = CASE WHEN highest_price < ? THEN ? ELSE highest_price END, updated_at = ?
		WHERE ts_code = ? AND (user_id = '' OR user_id = ?) AND highest_price < ?`,
		high, high, time.Now().Format("2006-01-02 15:04:05"), tsCode, userID, high)
	if err != nil {
		return false, fmt.Errorf("raise real position high %s: %w", tsCode, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("raise real position high rows affected %s: %w", tsCode, err)
	}
	return n > 0, nil
}

// UpdateRealOrderBySignalID 下单回填：把 pend:<signal_id> 占位行的 order_id 替换为网关真实委托号并更新状态。
// §GAP 修复：此前占位行 order_id 恒为空串，与 order_id 主键冲突导致第二笔起的新单被
// INSERT OR IGNORE 误判为重复（静默不下单），且按网关单号 UPDATE 也永不命中。
// §P0-2/P0-3 userID 为空时仅操作遗留全局行；非空时严格限定本账号，防止跨账号误更新。
// §修复 FIX#2（2026-09-04）：加单调状态守卫——重试成功后该行可能已被回报线程推进到
// 已成/部分成交（超时但券商实际受理），旧实现直接覆盖为"已报"会回退真实进度；现仅当
// 新状态秩不低于当前秩时才回填（对齐 §R4-4 单调状态机口径）。
// §修复 U-1（2026-09-14 像素级 UAT 实录）：守卫由"秩更高才回填（<= 跳过）"改为"仅严格更低才跳过（< 跳过）"。
// 旧实现占位行以"已报"落库、首次回填状态同为"已报"，秩相等被 `<=` 误杀 → 网关真实单号永不回填，
// 本地委托行 order_id 恒停留 `pend:` 前缀；后续一切按网关单号的推进/撤单/对账 UPDATE 永不命中，
// 委托生命周期在本端整体失真（成交回报靠 signal_id 记账故持仓/资金未受损）。等秩回填的语义恰恰是
// "只换单号、状态原地"，不存在回退风险，真正要拦的仍是"高秩已被回报推进、低秩回填晚到"。
// English: backfills the pending ticket (keyed by signal_id) with the gateway-assigned order id.
// English: §GAP fix — placeholder rows previously stored an empty order_id, colliding on the primary
// key so every later new order was swallowed as a "duplicate", and the status update by gateway id
// never matched. §FIX#2 — a monotonic guard skips the backfill when the row already advanced
// past 已报 (e.g. a late fill landed), so real progress is never rolled back. §U-1 — the guard was
// off-by-one strict: skipping equal ranks also killed the very first backfill (placeholder row and
// backfill both carry 已报, rank 1 <= rank 1), so the gateway order id NEVER landed on the local row
// and every later by-order-id update missed. Equal rank now backfills (it only swaps the id, the
// status stays); strictly-lower ranks are still refused.
func (d *DB) UpdateRealOrderBySignalID(userID, signalID, orderID, status string) error {
	var cur string
	var err error
	if userID == "" {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=''`, signalID).Scan(&cur)
	} else {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=?`, signalID, userID).Scan(&cur)
	}
	if err == sql.ErrNoRows {
		return nil // 无占位行（例如回填前已被清仓清理）：回填无可定位目标，静默跳过
	}
	if err != nil {
		return err
	}
	// §U-1：仅"严格更低秩"才视为回退并跳过；等秩（占位行与回填同为"已报"）放行——
	// 这正是首次网关单号回填的唯一命中路径，旧 `<=` 把它误杀导致 order_id 永驻 pend:。
	if orderStatusRank(status) < orderStatusRank(cur) {
		return nil // 低秩回报晚到（已成/部成后被晚到的"已报"回填）：绝不回退真实进度
	}
	if userID == "" {
		_, err = d.db.Exec(`UPDATE orders SET order_id=?, status=? WHERE signal_id=? AND user_id=''`, orderID, status, signalID)
	} else {
		_, err = d.db.Exec(`UPDATE orders SET order_id=?, status=? WHERE signal_id=? AND user_id=?`, orderID, status, signalID, userID)
	}
	return err
}

// MarkRealOrderSendFailed §GAP2-W1 占位行降级：下单请求发送失败（网关超时/5xx/断连）时把占位行
// 从"已报"改为"发送失败"。带 status='已报' 守卫——若回报线程已把该单推进到 部分成交/已成 等状态
// （客户端超时但券商实际受理的场景），绝不回退真实进度，只降级仍停留在"已报"假象的行。
// 效果：①买入纪律统计不再把幽灵单计入当日预算/笔数；②同一 signal_id 重试可经
// ResetFailedRealOrder 放行，止损类自动单不会因首次网络抖动被封死一整天。
// §P0-3 userID 限定本账号作用域。
// English: §GAP2-W1 demotes a placeholder order from 已报 to 发送失败 after a send failure
// (gateway timeout / 5xx / disconnect). Guarded on status='已报' so fills that already arrived via
// report callbacks (broker accepted despite our timeout) are never rolled back. Effects: failed sends
// no longer pollute daily budget/count, and retrying the same signal_id is allowed via
// ResetFailedRealOrder — auto stop-losses can no longer be bricked for the whole day by one network blip.
func (d *DB) MarkRealOrderSendFailed(userID, signalID string) error {
	var res sql.Result
	var err error
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status='发送失败' WHERE signal_id=? AND status='已报' AND user_id=''`, signalID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status='发送失败' WHERE signal_id=? AND status='已报' AND user_id=?`, signalID, userID)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[store] 委托 %s 发送失败，占位行已降级（可重试）", signalID)
	}
	return nil
}

// ResetFailedRealOrder §GAP2-W1 失败重试放行：把指定 signal_id 的"发送失败"行重置为"已报"。
// 仅当行存在且状态恰为"发送失败"、或「**已撤且零成交**」时生效并返回 true；其余状态
// （已报/部分成交/已成/已撤有成交）原样保留并返回 false——即真正的重复下单仍然被唯一键拦截，
// 只有确认「从未有效占用过额度」的单才允许再发一次。
// §H1-MG（2026-09-22 PM 修复批）：放行集合并入「已撤+无成交 fills」——保护性卖单被撤
// （人工/网关撤单/收盘失效）且一股未卖时，旧口径下同幂等键整天 duplicate，止损卖出当日猝死；
// 与 §M16「发送失败可重放」同语义：撤单零成交=这笔目标根本没达成，同键重放不是重复下单。
// 已撤但**有成交**（部撤入账）仍不可重放——那笔的剩余仓位已无当日在途卖单，由下一轮评分
// 以新幂等键自然接管。秩守卫不打架：重置只发生在下单重试路径（本函数内 WHERE 带状态复核），
// 不走回报推进（回报按 order_id 反查，行已换新 pend 占位号后旧单号的迟到回报自然 miss）。
// English: §H1-MG — the replay-eligible set grows from "send-failed" to also include
// "cancelled with zero fills": a cancelled protective sell that never filled must not kill the
// same-key retry for the rest of the day. Cancelled-with-fills rows stay non-replayable.
// §P0-3 userID 限定本账号作用域。
// §修复 FIX#2（2026-09-04）：重试同时换新占位单号 `pend:<signal_id>:<attempt>`（attempt 自增）。
// 旧实现只改状态、占位单号保持首次 `pend:<sid>`——一旦重试实际到券商（响应丢失）而网关单号
// 回填前崩溃，SweepOrders 见 pend: 前缀行即按"从未到达网关"再降级，同 signal_id 反复真报单。
// 每次重试换唯一占位单号后，真实委托的回填（UpdateRealOrderBySignalID）只会命中本轮单号，
// 历史 pend 行不再被误判为幽灵单。
func (d *DB) ResetFailedRealOrder(userID, signalID string) (bool, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// §H1-MG 放行条件的单一事实源（SELECT/UPDATE 两处共用，防语句漂移）：
	// 发送失败=从未到券商；已撤+零 fills=到过券商但一股未成交——两者都不构成"目标已达成"。
	const eligibleWhere = `signal_id=? AND user_id=? AND (status='发送失败'
		OR (status='已撤' AND NOT EXISTS (
			SELECT 1 FROM fills f WHERE f.user_id=orders.user_id AND f.signal_id LIKE orders.signal_id||'%'))) `
	var cur string
	err = tx.QueryRow(`SELECT order_id FROM orders WHERE `+eligibleWhere, signalID, userID).Scan(&cur)
	if err == sql.ErrNoRows {
		return false, nil // 行不存在或状态不可重放（含"已撤但有成交"）
	}
	if err != nil {
		return false, err
	}
	// 自增 attempt：旧式 pend:<sid> 视为第 1 次；解析失败/非 pend 前缀时从 1 重新计
	// （§H1-MG：被重置的"已撤"行带的是网关真实单号，非 pend 前缀 → 从 attempt 1 起新占位号，
	// 真实旧单号被替换后，该旧单号的迟到回报反查不到行，不会污染重放轮次的账）。
	attempt := 1
	prefix := "pend:" + signalID + ":"
	if strings.HasPrefix(cur, prefix) {
		if n, perr := strconv.Atoi(strings.TrimPrefix(cur, prefix)); perr == nil && n > 0 {
			attempt = n + 1
		}
	}
	newID := fmt.Sprintf("pend:%s:%d", signalID, attempt)
	// 可重放行重置为"已报"并换新占位单号（供重试队列再次投递）。UPDATE 复用同一 WHERE：
	// 事务窗口内若有回报把行推进（如撤单回报带成交落地），条件不再成立则零行受影响→不可重试。
	res, err := tx.Exec(`UPDATE orders SET status='已报', order_id=? WHERE `+eligibleWhere, newID, signalID, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	// 无行受影响（已被其他重试处理）→ 视为不可重试。
	if n == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RealOrders 返回全部实盘委托单（倒序）。
// （RealOrders returns all live orders, newest first.）
func (d *DB) RealOrders() ([]RealOrder, error) {
	rows, err := d.db.Query(`SELECT order_id, signal_id, code, side, status, price, qty, created_at,
		COALESCE(user_id,'') FROM orders ORDER BY created_at DESC, order_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealOrder
	for rows.Next() {
		var o RealOrder
		if err := rows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RealOrdersForUser §P0-3 按账号返回实盘委托单；遗留全局行（user_id=”）对查询账号可见。
// English: user-scoped live orders; legacy global rows are visible to any caller.
func (d *DB) RealOrdersForUser(userID string) ([]RealOrder, error) {
	rows, err := d.db.Query(`SELECT order_id, signal_id, code, side, status, price, qty, created_at,
		COALESCE(user_id,'') FROM orders WHERE user_id = '' OR user_id = ?
		ORDER BY created_at DESC, order_id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealOrder
	for rows.Next() {
		var o RealOrder
		if err := rows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// orderStatusRank §R4-4 委托状态单调推进秩：回报乱序/重放时绝不回退真实进度。
// 终态（已成/已撤/部撤/废单）秩最高；未报/待报视同初始已报。
// English: §R4-4 monotonic rank for order-report statuses so out-of-order/replayed reports
// never roll real progress back; terminal states rank highest.
func orderStatusRank(status string) int {
	switch status {
	case "已成", "已撤", "部撤", "废单":
		return 6
	case "部成待撤":
		return 4
	case "部成":
		return 3
	case "已报待撤":
		return 2
	case "已报":
		return 1
	default: // 未报/待报/未知 → 初始档
		return 1
	}
}

// AdvanceRealOrderStatus §R4-4 带单调守卫的委托状态推进（按 signal_id 定位）：
// 仅当回报状态的秩高于本地当前秩时才更新——修掉"回报 部成/已成/已撤 被 INSERT OR IGNORE
// 静默吞掉、本地永远停留已报"的状态机断链（该断链会让自动撤单误撤已成交单，资损级前置）。
// §P0-3 userID 限定本账号作用域；userID 为空时仅操作遗留全局行。
// 返回 (是否实际更新, 错误)。
// English: §R4-4 guarded status advance keyed by signal_id — updates only when the reported
// status outranks the local one, fixing the silent-swallow hole where 部成/已成/已撤 reports
// never landed (a prerequisite before any auto-cancel can be trusted).
func (d *DB) AdvanceRealOrderStatus(userID, signalID, status string) (bool, error) {
	var cur string
	var err error
	if userID == "" {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=''`, signalID).Scan(&cur)
	} else {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=?`, signalID, userID).Scan(&cur)
	}
	if err == sql.ErrNoRows {
		return false, nil // 本地无此单（如网侧重放的历史单）：交由调用方决定是否补插
	}
	if err != nil {
		return false, err
	}
	if orderStatusRank(status) <= orderStatusRank(cur) {
		return false, nil // 乱序/重放/回退：忽略
	}
	var res sql.Result
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=''`, status, signalID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=?`, status, signalID, userID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// §A4（20260918 全栈审计批）委托回报（order 事件）处理动作枚举：ApplyOrderReportTx 返回值。
// English: §A4 outcome vocabulary for ApplyOrderReportTx.
const (
	OrderReportAdvanced = "advanced" // 本地已有单且秩推进成功（updated）
	OrderReportInserted = "inserted" // 本地无单，补插完整委托行（inserted）
	OrderReportIgnored  = "ignored"  // 乱序/重放/回退或唯一键冲突，未写任何数据（no-op）
)

// ApplyOrderReportTx §A4（20260918 全栈审计批）委托状态回报的单事务原子落库：
// 把旧 server/qmt.go "case order" 路径的两步独立写——AdvanceRealOrderStatus（按 signal_id
// 单调推进）→ 未命中再 UpsertRealOrder（补插）——合并为一个事务内的 SELECT+UPDATE/INSERT。
// 旧实现的竞态窗口：进程在"推进失败"与"补插"两步之间崩溃或被并发插入打断时，
// 回报可能整体丢失（outbox 重放前本地状态空洞）；合并后要么推进、要么补插、要么
// 明确 no-op，三步判定共享同一事务快照。语义与两步版严格一致：
//   - 本地有行且回报秩 > 当前秩 → UPDATE status，返回 advanced；
//   - 本地有行且秩不升 → 返回 ignored（绝不回退真实进度，§R4-4 单调守卫）；
//   - 本地无行 → INSERT OR IGNORE 完整委托行，插入生效返回 inserted、
//     唯一键 (user_id, signal_id) 冲突返回 ignored。
//
// userID 作用域口径与 AdvanceRealOrderStatus 相同：空=仅遗留全局行，非空=严格本账号。
// English: §A4 — folds the order-report path (guarded advance, then insert-if-absent) into a
// single transaction so a crash or a concurrent insert between the two former statements can no
// longer drop a report. Monotonic rank guard, signal_id scoping and INSERT OR IGNORE idempotency
// are preserved verbatim; returns one of advanced/inserted/ignored.
func (d *DB) ApplyOrderReportTx(o RealOrder) (string, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// 先按 user_id 作用域读出本地状态：空账号只匹配遗留的全局行，
	// 拿到 cur 才能在下面做「只升不降」的秩比较。
	var cur string
	if o.UserID == "" {
		err = tx.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=''`, o.SignalID).Scan(&cur)
	} else {
		err = tx.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=?`, o.SignalID, o.UserID).Scan(&cur)
	}
	switch err {
	case nil:
		// 本地有行：仅高秩才推进（与 AdvanceRealOrderStatus 同守卫）。
		if orderStatusRank(o.Status) <= orderStatusRank(cur) {
			return OrderReportIgnored, nil // defer Rollback：无写入
		}
		if o.UserID == "" {
			_, err = tx.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=''`, o.Status, o.SignalID)
		} else {
			_, err = tx.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=?`, o.Status, o.SignalID, o.UserID)
		}
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return OrderReportAdvanced, nil
	case sql.ErrNoRows:
		// 本地无此单（网侧重放/回报先于下单回填到达）：补插完整委托行。
		res, err := tx.Exec(`INSERT OR IGNORE INTO orders
			(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt, o.UserID)
		if err != nil {
			return "", err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		if n > 0 {
			return OrderReportInserted, nil
		}
		return OrderReportIgnored, nil // 同事务内唯一键冲突（并发对手机已插入）：幂等 no-op
	default:
		return "", err
	}
}

// UpdateRealOrderStatusMonotonic §P0-4 撤单路径单调状态机：仅当目标状态秩高于当前秩时才更新。
// 防止"网关撤单响应晚于成交回报"时把 已成/已撤 回退为 已撤，或把部成回退为已报。
// userID 为空时仅操作遗留全局行。
// English: P0-4 monotonic cancel path — only updates when the target status outranks the current one,
// preventing a late cancel response from downgrading a filled/cancelled order.
func (d *DB) UpdateRealOrderStatusMonotonic(userID, orderID, status string) (bool, error) {
	var cur string
	var err error
	if userID == "" {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE order_id=? AND user_id=''`, orderID).Scan(&cur)
	} else {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE order_id=? AND user_id=?`, orderID, userID).Scan(&cur)
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if orderStatusRank(status) <= orderStatusRank(cur) {
		return false, nil
	}
	var res sql.Result
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE order_id=? AND user_id=''`, status, orderID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE order_id=? AND user_id=?`, status, orderID, userID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RealFills 返回全部实盘成交回报（倒序）。
// §F1（2026-09-22 修复批）：SELECT 补齐 user_id/fee/stamp_tax 三列——此前读出恒为
// 零值，导致 ① /api/qmt/trades 的账号归属过滤形同虚设（f.UserID 恒空按遗留全局放行），
// ② 盈亏重放拿不到费用腿。COALESCE 兜底 ALTER 前的历史 NULL。
// （RealFills returns all live fills, newest first; now carrying fee/stamp_tax/user legs.）
func (d *DB) RealFills() ([]RealFill, error) {
	rows, err := d.db.Query(`SELECT id, order_id, code, side, price, qty, amount, traded_at, signal_id,
		COALESCE(user_id,''), COALESCE(fee,0), COALESCE(stamp_tax,0), COALESCE(trade_id,'')
		FROM fills ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealFill
	for rows.Next() {
		var f RealFill
		if err := rows.Scan(&f.ID, &f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount, &f.TradedAt, &f.SignalID,
			&f.UserID, &f.Fee, &f.StampTax, &f.TradeID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ClearRealBook 清空实盘账本（对账重建/演示用）。
// （ClearRealBook wipes the live book.）
func (d *DB) ClearRealBook() error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"real_positions", "orders", "fills"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MigrateRealTablesIfEmpty 仅在 dst 实盘账本为空时，从 src（通常是旧 trading.db）整表拷贝
// real_positions / orders / fills / real_account，避免拆分 live.db 后存量实盘数据丢失。
// 幂等：dst 已有数据则直接跳过，返回 (false, nil)。English: one-time copy of the live book
// from src into dst when dst is empty (idempotent; skips when dst already holds rows).
func MigrateRealTablesIfEmpty(dst, src *DB) (bool, error) {
	if dst == nil || src == nil {
		return false, nil
	}
	var n int
	if err := dst.db.QueryRow(`SELECT COUNT(*) FROM real_positions`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}

	// 搬迁整体在 dst 的一个事务里完成：任何一张表出错都整体回滚，避免出现半本实盘账。
	tx, err := dst.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	// 持仓表逐行搬：INSERT OR REPLACE 以主键覆盖，重复迁移不会把数量翻倍。
	// real_positions
	if rows, rerr := src.db.Query(`SELECT ts_code,name,qty,cost_price,amount,highest_price,strategy,signal_id,updated_at,user_id FROM real_positions`); rerr == nil {
		for rows.Next() {
			var p RealPosition
			if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount, &p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID); err != nil {
				rows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO real_positions(ts_code,name,qty,cost_price,amount,highest_price,strategy,signal_id,updated_at,user_id) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID); err != nil {
				rows.Close()
				return false, err
			}
		}
		rows.Close()
	}

	// 委托单同样按 order_id 覆盖式搬迁，迁移中断后重跑不会造出重复委托。
	// orders
	if oRows, oerr := src.db.Query(`SELECT order_id,signal_id,code,side,status,price,qty,created_at,user_id FROM orders`); oerr == nil {
		for oRows.Next() {
			var o RealOrder
			if err := oRows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
				oRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO orders(order_id,signal_id,code,side,status,price,qty,created_at,user_id) VALUES(?,?,?,?,?,?,?,?,?)`,
				o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt, o.UserID); err != nil {
				oRows.Close()
				return false, err
			}
		}
		oRows.Close()
	}

	// 成交簿是只增流水，这里用普通 INSERT 追加（重复迁移的可能已由「dst 非空即跳过」拦住）。
	// fills
	if fRows, ferr := src.db.Query(`SELECT order_id,code,side,price,qty,amount,traded_at,signal_id,user_id FROM fills`); ferr == nil {
		for fRows.Next() {
			var f RealFill
			if err := fRows.Scan(&f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount, &f.TradedAt, &f.SignalID, &f.UserID); err != nil {
				fRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT INTO fills(order_id,code,side,price,qty,amount,traded_at,signal_id,user_id) VALUES(?,?,?,?,?,?,?,?,?)`,
				f.OrderID, f.Code, f.Side, f.Price, f.Qty, f.Amount, f.TradedAt, f.SignalID, f.UserID); err != nil {
				fRows.Close()
				return false, err
			}
		}
		fRows.Close()
	}

	// real_account（惰性建表，一并迁移）
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS real_account (
		user_id TEXT PRIMARY KEY, available_cash REAL NOT NULL DEFAULT 0,
		frozen_cash REAL NOT NULL DEFAULT 0, total_asset REAL NOT NULL DEFAULT 0,
		market_value REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`); err != nil {
		return false, err
	}
	if aRows, aerr := src.db.Query(`SELECT user_id,available_cash,frozen_cash,total_asset,market_value,updated_at FROM real_account`); aerr == nil {
		for aRows.Next() {
			var a RealAccount
			if err := aRows.Scan(&a.UserID, &a.AvailableCash, &a.FrozenCash, &a.TotalAsset, &a.MarketValue, &a.UpdatedAt); err != nil {
				aRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO real_account(user_id,available_cash,frozen_cash,total_asset,market_value,updated_at) VALUES(?,?,?,?,?,?)`,
				a.UserID, a.AvailableCash, a.FrozenCash, a.TotalAsset, a.MarketValue, a.UpdatedAt); err != nil {
				aRows.Close()
				return false, err
			}
		}
		aRows.Close()
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	tx = nil
	return true, nil
}
