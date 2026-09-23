// Package store — SQLite 历史数据存储层。
// fill_amendments.go：§FILL-AMEND（2026-09-23）历史错账的**人工逐笔勘误**存储层 +
// 读取侧唯一收敛点 `fills_effective` 视图。
//
// 为什么需要它（事故链）：2026-09-22 10:08 一笔真实卖出（603468.SH 900 股 @22.55）被记成
// 买入。方向判定的根因已由同批 §SIDE-AUTH 修好（网关未命中派发行不再采信柜台枚举猜测，
// Go 侧对 side_unverified 回报留痕拒入账本），**但已入库的历史行不会被自动改**——成交是
// 柜台证据，任何自动"纠偏"都可能把对的改错。故本文件只提供一件事：追加一条人工勘误决定，
// 并在读取侧生效。
//
// 三条设计前提（违反即返工）：
//  1. 绝不 UPDATE/DELETE fills 原始行。柜台证据不可改写，人工判断只能以"追加决定"的形式存在；
//     fills_effective 视图把两者合成一行的两个字段（side=生效方向、orig_side=原始方向），
//     原始表逐列保持落库时的样子（守恒自检的第二条不变量就是这个）。
//  2. 默认影子态。新提交的勘误 status=pending，视图只 JOIN status='applied' 的行 →
//     未批准/已撤销的勘误对所有账目数字零影响，必须显式批准（apply）才生效。
//  3. 单一收敛点。所有"这笔成交算买还是卖"的账目口径一律读 fills_effective，不允许各查各的
//     （各查各的 = 同一笔改判在不同闸口给出互相矛盾的数字，比错账更难查）。
//     已收敛的读取方见 fillEffectiveDoc 常量旁的清单。
//
// English: §FILL-AMEND — append-only human corrections for historically mis-booked fills, plus the
// single read-side convergence point (the fills_effective view). Raw broker evidence is never
// UPDATEd or DELETEd; a correction is a new row that only takes effect once explicitly applied, and
// every accounting reader resolves the direction through the same view.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"quant-trading-v2/internal/cntime"
)

// 勘误状态机（英文蛇形口径，与本仓 settlement_diff.mode 的 report_only/sync_fills 同族；
// 委托状态用中文是"柜台回报枚举"的历史包袱，勘误是本系统自有的决定记录，不混用）。
//
//	pending  → 已提交、未批准：影子态，对所有账目数字零影响
//	applied  → 已批准：fills_effective 视图据此改判方向
//	revoked  → 已撤销：回到原始方向（撤销不是删除，审计链必须留痕）
const (
	FillAmendPending = "pending"
	FillAmendApplied = "applied"
	FillAmendRevoked = "revoked"
)

// fillEffectiveDoc 记录视图定义与"必须走它"的读取方清单。为什么写成注释而不是代码：视图本体
// 在 migrateFillAmendments 里建，改判方向的影响面需要一处能读到的清单，否则下一个新增 fills
// 读取方一定会绕过它直接查 fills（本仓 §M-14 的教训形态）。
//
//	视图 fills_effective = fills LEFT JOIN（status='applied' 的勘误）
//	                     side = COALESCE(勘误方向, 原始方向)
//
// 已收敛到账目口径的读取方（全部 FROM fills_effective）：
//  1. CountBuyFilledOrdersByDay   —— 单日买入笔数纪律闸（risk.Gate checkBuyDiscipline 闸1）
//  2. SumBuyFilledAmountByDay     —— 单日买入预算「已成交」腿
//  3. SumSellFilledAmountByDay    —— 单日卖出回款（本次错账的主要受害者）
//  4. RealFills                   —— handleQMTTrades 成交簿重放（已实现盈亏/胜负/战法归因）
//  5. ListFillsByDay              —— TodayRealizedPnl（熔断闸日内已实现盈亏）+ 交割单三方对账本地腿
//  6. TodayBoughtQty              —— T+1 可卖量（持仓 − 当日买入）
//
// 有意**不**收敛的 fills 读取方（它们的语义是"柜台证据/委托进度"，不是"这笔算买还是卖"）：
//   - ApplyRealFill 的判重（trade_id / 复合键）与 FillExists：幂等锚必须锚在原始行上，
//     否则同一笔回报在勘误生效后会被判成"新成交"再入一次账（双倍记账）。
//   - SumFilledQty / SumOpenSellQty：按 signal_id/order_id 聚合委托已成交量做部成/在途扣减，
//     不带方向条件，改判方向不改变"该委托成交了多少股"。
//   - ClearRealBook / MigrateRealTablesIfEmpty：整表清理与搬运，针对原始证据。
const fillEffectiveDoc = "fills_effective"

// _ 常量占位：让 fillEffectiveDoc 出现在编译器可见的位置（文档型常量，无运行期用途）。
var _ = fillEffectiveDoc

// FillAmendment 一条人工勘误决定（append-only）。
// 锚点姿势与 fills 落库判重完全一致（见 ApplyRealFill）：券商成交编号 trade_id 为权威主锚，
// 缺失（旧行/交割单回灌）退回复合锚 order_id+code+traded_at+price+qty。
// English: one human correction decision; anchored exactly like the fills replay key.
type FillAmendment struct {
	ID       int64   `json:"id"`       // 勘误行自增 ID
	FillID   int64   `json:"fill_id"`  // 被勘误的 fills.id（原始证据行指针，仅审计/校验用）
	TradeID  string  `json:"trade_id"` // 券商成交编号（主锚，空=退回复合锚）
	OrderID  string  `json:"order_id"`
	Code     string  `json:"code"`
	TradedAt string  `json:"traded_at"`
	Price    float64 `json:"price"`
	Qty      int     `json:"qty"`
	UserID   string  `json:"user_id"` // 成交归属账号（遗留全局行为空串）

	OrigSide   string  `json:"orig_side"`   // 原始（柜台入账）方向
	NewSide    string  `json:"new_side"`    // 勘误后方向，只接受 买入/卖出
	OrigAmount float64 `json:"orig_amount"` // 原始成交金额快照（勘误只改方向，金额必须一字不变）

	Reason    string `json:"reason"`     // 必填：为什么改（审计语义全靠它）
	Operator  string `json:"operator"`   // 提交/批准人（用户名）
	Status    string `json:"status"`     // pending / applied / revoked
	CreatedAt string `json:"created_at"` // 北京时间 yyyy-MM-dd HH:mm:ss
	AppliedAt string `json:"applied_at"` // 批准时间（未批准为空）

	// AmendKey 前端与本仓读取侧共用的匹配键（Go 侧单点计算，绝不在 JS 里重算一套锚规则：
	// 浮点 price 的字符串化两边一旦不一致，前端就把勘误状态挂错行）。
	AmendKey string `json:"amend_key"`
}

// ErrFillAmendmentNotFound 勘误行不存在。
var ErrFillAmendmentNotFound = errors.New("勘误记录不存在")

// ErrFillAmendmentConflict 同一笔成交已有 pending/applied 勘误——不允许叠加两条互相打架的
// 改判（视图 JOIN 会因此变成一对多，账目数字直接失真）。
var ErrFillAmendmentConflict = errors.New("该成交已有待批准或已生效的勘误")

// ErrFillAmendmentSide 方向非法：只接受 买入/卖出，且必须与原始方向不同（同向"勘误"是空操作，
// 收下它只会污染审计链）。
var ErrFillAmendmentSide = errors.New("勘误方向非法（仅接受与原始方向不同的 买入/卖出）")

// ErrFillAmendmentNoFill 被勘误的原始成交不存在。
var ErrFillAmendmentNoFill = errors.New("待勘误的成交记录不存在")

// maxAmendReasonLen 理由长度上限（防整段日志粘贴进审计字段把列表撑爆）。
const maxAmendReasonLen = 500

// FillAmendKey 成交/勘误的匹配键：trade_id 非空 → 't:'+trade_id（券商权威锚）；
// 否则 → 'c:'+order_id|code|traded_at|price(4位小数)|qty（复合锚）。
// 为什么两边（fills 读取方与 fill_amendments 行）必须用同一个函数：前端要把"这笔已提交
// 勘误尚未批准"挂到正确的成交行上，锚规则一旦两处各写一份就会漂移。
// English: the single correlation key used by both sides; computed in Go only, never re-derived in JS.
func FillAmendKey(tradeID, orderID, code, tradedAt string, price float64, qty int) string {
	if tradeID != "" {
		return "t:" + tradeID
	}
	return fmt.Sprintf("c:%s|%s|%s|%.4f|%d", orderID, code, tradedAt, price, qty)
}

// AmendKey 本勘误行的匹配键。
func (a FillAmendment) AmendKeyOf() string {
	return FillAmendKey(a.TradeID, a.OrderID, a.Code, a.TradedAt, a.Price, a.Qty)
}

// migrateFillAmendments 建勘误表 + 部分唯一索引 + fills_effective 视图（幂等）。
//
// 为什么放在列补齐之后调用：视图定义引用 fills.trade_id，该列对旧库是靠
// `ALTER TABLE fills ADD COLUMN trade_id` 增量补上的——建视图早于补列会让 Open 直接失败、
// 服务起不来（本仓迁移的中断即起不来口径只用于资金级风险，此处没必要）。
//
// 为什么视图用 DROP+CREATE 而不是 CREATE VIEW IF NOT EXISTS：视图定义要能随勘误锚点口径的
// 演进而更新，IF NOT EXISTS 会把第一版的定义永久钉死（正是本仓 §ADJ-BASIS 那类"改前旧行
// 被当新结果"的形态）。DROP/CREATE 之间的窗口在单进程启动路径内，无并发读。
//
// 部分唯一索引是"视图不会把一行成交扇成多行"的**唯一**保障：JOIN 条件按锚点分两支
// （trade_id 命中 / 复合锚命中），每支各建一个 partial unique index ⇒ 任一成交最多匹配
// 一条 applied 勘误。少了这两个索引，两条针对同一笔的 applied 勘误会让成交在视图里出现两次
// → 买入笔数/金额全部翻倍，且 fills 原始行看起来仍然一字未动（最难查的一类污染）。
// English: creates the amendments table, the two partial unique indexes that keep the view from
// fanning one fill into several rows, and the fills_effective view (drop+create so the definition
// can evolve).
func (d *DB) migrateFillAmendments() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS fill_amendments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			fill_id INTEGER NOT NULL DEFAULT 0,      -- 被勘误的 fills.id（原始证据行指针）
			trade_id TEXT NOT NULL DEFAULT '',       -- 券商成交编号（主锚，空=退回复合锚）
			order_id TEXT NOT NULL DEFAULT '',       -- 复合锚 1
			code TEXT NOT NULL DEFAULT '',           -- 复合锚 2
			traded_at TEXT NOT NULL DEFAULT '',      -- 复合锚 3
			price REAL NOT NULL DEFAULT 0,           -- 复合锚 4
			qty INTEGER NOT NULL DEFAULT 0,          -- 复合锚 5
			user_id TEXT NOT NULL DEFAULT '',        -- 成交归属账号（展示/过滤，不参与锚）
			orig_side TEXT NOT NULL DEFAULT '',      -- 原始方向快照
			new_side TEXT NOT NULL DEFAULT '',       -- 勘误方向（仅 买入/卖出）
			orig_amount REAL NOT NULL DEFAULT 0,     -- 原始金额快照（方向改、金额不改）
			reason TEXT NOT NULL DEFAULT '',         -- 必填理由
			operator TEXT NOT NULL DEFAULT '',       -- 提交人
			status TEXT NOT NULL DEFAULT 'pending',  -- pending/applied/revoked
			created_at TEXT NOT NULL DEFAULT '',     -- 北京时间
			applied_at TEXT NOT NULL DEFAULT ''      -- 批准时间（北京时间，未批准为空）
		)`,
		// 一笔成交最多一条"活跃"（pending/applied）勘误：pending 也计入唯一性，否则同一笔会被
		// 提交两条 pending、批准第二条时第一条静默失效，操作者以为改了其实改的是另一条。
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fa_active_trade
			ON fill_amendments(trade_id) WHERE status <> 'revoked' AND trade_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fa_active_comp
			ON fill_amendments(order_id, code, traded_at, price, qty)
			WHERE status <> 'revoked' AND (trade_id = '' OR trade_id IS NULL)`,
		// 视图按 status='applied' JOIN，故 applied 侧的唯一性另建一组索引——不能复用上面两个：
		// 部分索引谓词里含 'revoked' 分支时，SQLite 无法据它保证 JOIN 至多一行。
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fa_applied_trade
			ON fill_amendments(trade_id) WHERE status = 'applied' AND trade_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fa_applied_comp
			ON fill_amendments(order_id, code, traded_at, price, qty)
			WHERE status = 'applied' AND (trade_id = '' OR trade_id IS NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_fa_status ON fill_amendments(status, id)`,
		// 读取侧唯一收敛点：side = 生效方向（勘误优先），orig_side = 柜台原始方向。
		//
		// 哪些读取口**不**接本视图（2026-09-23 全仓 `FROM fills` 逐条盘点结论）：
		// 勘误只改写 side 一列（price/qty/amount/order_id/signal_id/trade_id 全为透传），
		// 因此凡不按 side 过滤/取值的读取口，接视图与接原表在数值上恒等，无须收敛——
		// SumFilledQty / SumOpenSellQty / ResetFailedRealOrder(Exists) 三条都只按
		// user_id+signal_id / order_id 聚合 qty；ApplyRealFill 与 FillExists 的判重走的是
		// 柜台回报的原始身份（trade_id / 事实键），必须对原表，否则一笔改判会让同一条回报
		// 判不出重复而二次入账；旧库搬迁 COPY 也是原表→原表（源库未必建过视图）。
		// 反过来说：任何**新增**的按方向取数口径（买入/卖出、金额、笔数）一律读 fills_effective。
		`DROP VIEW IF EXISTS fills_effective`,
		`CREATE VIEW fills_effective AS
		SELECT
			f.id AS id,
			f.order_id AS order_id,
			f.code AS code,
			COALESCE(a.new_side, f.side) AS side,
			f.side AS orig_side,
			f.price AS price,
			f.qty AS qty,
			f.amount AS amount,
			f.traded_at AS traded_at,
			f.signal_id AS signal_id,
			f.user_id AS user_id,
			f.fee AS fee,
			f.stamp_tax AS stamp_tax,
			f.serial AS serial,
			f.trade_id AS trade_id,
			a.id AS amend_id,
			a.status AS amend_status,
			a.reason AS amend_reason,
			a.operator AS amend_operator
		FROM fills f
		LEFT JOIN fill_amendments a
			ON a.status = 'applied'
			AND (
				(COALESCE(f.trade_id,'') <> '' AND COALESCE(a.trade_id,'') <> '' AND a.trade_id = f.trade_id)
				OR (COALESCE(f.trade_id,'') = '' AND COALESCE(a.trade_id,'') = ''
					AND a.order_id = f.order_id AND a.code = f.code
					AND a.traded_at = f.traded_at AND a.price = f.price AND a.qty = f.qty)
			)`,
	}
	for _, s := range stmts {
		if _, err := d.db.Exec(s); err != nil {
			return fmt.Errorf("store migrate fill_amendments: %w\n%s", err, s)
		}
	}
	return nil
}

// CreateFillAmendment 为某笔原始成交追加一条**待批准**勘误（默认影子态，绝不动 fills 行）。
// 入参只给"改判方向 + 理由 + 操作者"，锚点字段全部从原始行读出——不能由调用方自报锚点：
// 那样提交者可以手写一个锚去命中他想改的那行，一旦写歪就是一条永久匹配不到成交的死勘误。
// English: appends a PENDING correction for one raw fill row; the anchor is read from the row
// itself (never self-declared by the caller), so a typo cannot create a permanently dangling
// amendment. Raw fills are untouched and the numbers do not move until ApplyFillAmendment.
func (d *DB) CreateFillAmendment(fillID int64, newSide, reason, operator string) (*FillAmendment, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("勘误理由必填（人工改判无留痕即无据可查）")
	}
	if len(reason) > maxAmendReasonLen {
		return nil, fmt.Errorf("勘误理由过长（%d 字符，上限 %d）", len(reason), maxAmendReasonLen)
	}
	newSide = strings.TrimSpace(newSide)
	if newSide != "买入" && newSide != "卖出" {
		return nil, ErrFillAmendmentSide
	}
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return nil, errors.New("操作者必填")
	}

	// 锚点一律由服务端从**原始 fills 行**读出（前端只提交 fill_id）：
	// 让客户端自带 trade_id/order_id 会造成"死勘误"——改判指向一个柜台不存在的锚，
	// 视图永远匹配不上却零报错。这里查不到原行即拒（ErrFillAmendmentNoFill）。
	var a FillAmendment
	err := d.db.QueryRow(`SELECT id, COALESCE(trade_id,''), COALESCE(order_id,''), code, side,
		price, qty, COALESCE(amount,0), traded_at, COALESCE(user_id,'')
		FROM fills WHERE id=?`, fillID).
		Scan(&a.FillID, &a.TradeID, &a.OrderID, &a.Code, &a.OrigSide,
			&a.Price, &a.Qty, &a.OrigAmount, &a.TradedAt, &a.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFillAmendmentNoFill
	}
	if err != nil {
		return nil, fmt.Errorf("read raw fill %d: %w", fillID, err)
	}
	if a.OrigSide == newSide {
		return nil, ErrFillAmendmentSide
	}
	a.NewSide = newSide

	// 同一笔成交只允许一条活跃勘误（SQL 侧另有 partial unique index 兜底并发下的竞态）。
	var active int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM fill_amendments WHERE status <> 'revoked' AND (
			(? <> '' AND trade_id = ?)
			OR (? = '' AND order_id = ? AND code = ? AND traded_at = ? AND price = ? AND qty = ?))`,
		a.TradeID, a.TradeID, a.TradeID, a.OrderID, a.Code, a.TradedAt, a.Price, a.Qty).Scan(&active); err != nil {
		return nil, fmt.Errorf("count active amendments: %w", err)
	}
	if active > 0 {
		return nil, ErrFillAmendmentConflict
	}

	// 新条目强制落 pending：这里绝不写 applied_at，生效只能走 ApplyFillAmendment 的显式动作，
	// 使"提交"与"改账"两步在数据上不可合并（误操作者还能靠撤销回退）。
	a.Status = FillAmendPending
	a.CreatedAt = cntime.Now().Format("2006-01-02 15:04:05")
	res, err := d.db.Exec(`INSERT INTO fill_amendments
		(fill_id, trade_id, order_id, code, traded_at, price, qty, user_id,
		 orig_side, new_side, orig_amount, reason, operator, status, created_at, applied_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'')`,
		a.FillID, a.TradeID, a.OrderID, a.Code, a.TradedAt, a.Price, a.Qty, a.UserID,
		a.OrigSide, a.NewSide, a.OrigAmount, reason, operator, a.Status, a.CreatedAt)
	if err != nil {
		if isUniqueIndexErr(err) {
			return nil, ErrFillAmendmentConflict
		}
		return nil, fmt.Errorf("insert amendment: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		a.ID = id
	}
	a.Reason = reason
	a.Operator = operator
	a.AmendKey = a.AmendKeyOf()
	return &a, nil
}

// ApplyFillAmendment 批准一条待批准勘误（唯一让账目数字变动的动作）。
// 只接受 pending → applied：revoked 是终态（撤销后重新提交一条，审计链才看得见"改过又撤回
// 再改"的完整过程）；applied 重复批准按幂等成功返回（前端重试不该报错）。
// English: approves one pending amendment — the only action that moves any number.
func (d *DB) ApplyFillAmendment(id int64, operator string) (*FillAmendment, error) {
	a, err := d.FillAmendmentByID(id)
	if err != nil {
		return nil, err
	}
	if a.Status == FillAmendApplied {
		return a, nil // 幂等：重复批准不覆盖首次生效时间
	}
	if a.Status != FillAmendPending {
		return nil, fmt.Errorf("勘误 %d 状态=%s，仅待批准（pending）可批准", id, a.Status)
	}
	now := cntime.Now().Format("2006-01-02 15:04:05")
	res, err := d.db.Exec(`UPDATE fill_amendments SET status='applied', applied_at=?
		WHERE id=? AND status='pending'`, now, id)
	if err != nil {
		if isUniqueIndexErr(err) {
			return nil, ErrFillAmendmentConflict
		}
		return nil, fmt.Errorf("apply amendment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("勘误 %d 已被他人处置（并发批准/撤销），请刷新后重试", id)
	}
	// 批准人落审计：operator 列记提交人，批准动作用 opslog 在 HTTP 层留痕（含业务字段）。
	_ = operator
	a.Status = FillAmendApplied
	a.AppliedAt = now
	return a, nil
}

// RevokeFillAmendment 撤销一条勘误（pending/applied → revoked）：账目数字立刻回到原始方向。
// 不删行——"曾经批准又撤销"是后续排查必须能看到的事实。
// English: revokes an amendment (numbers snap back to the raw direction); the row is kept.
func (d *DB) RevokeFillAmendment(id int64) (*FillAmendment, error) {
	a, err := d.FillAmendmentByID(id)
	if err != nil {
		return nil, err
	}
	if a.Status == FillAmendRevoked {
		return a, nil
	}
	res, err := d.db.Exec(`UPDATE fill_amendments SET status='revoked' WHERE id=? AND status<>'revoked'`, id)
	if err != nil {
		return nil, fmt.Errorf("revoke amendment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("勘误 %d 已被他人处置，请刷新后重试", id)
	}
	a.Status = FillAmendRevoked
	return a, nil
}

// FillAmendmentByID 读一条勘误。
func (d *DB) FillAmendmentByID(id int64) (*FillAmendment, error) {
	row := d.db.QueryRow(amendSelect+` WHERE id=?`, id)
	a, err := scanAmendment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFillAmendmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ListFillAmendments 按状态（空=全部）倒序列出勘误，供前端把"已提交待批准/已生效"挂到成交行上。
// English: lists amendments (all statuses when status=”) newest first.
func (d *DB) ListFillAmendments(status string, limit int) ([]FillAmendment, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := amendSelect
	args := []any{}
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FillAmendment
	for rows.Next() {
		a, err := scanAmendment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// amendSelect 勘误行的统一列清单（单点定义，防列表/详情两处列序漂移）。
const amendSelect = `SELECT id, fill_id, COALESCE(trade_id,''), COALESCE(order_id,''), code,
	COALESCE(traded_at,''), price, qty, COALESCE(user_id,''), orig_side, new_side, orig_amount,
	COALESCE(reason,''), COALESCE(operator,''), status, COALESCE(created_at,''), COALESCE(applied_at,'')
	FROM fill_amendments`

// scanAmendment 把一行勘误扫成结构体并顺手算好匹配键（前端挂状态用）。
// 参数用匿名接口而非 *sql.Row：列表路径拿的是 *sql.Rows，两处共用同一份列序（同 scanCandidate 姿势）。
func scanAmendment(sc interface{ Scan(...any) error }) (*FillAmendment, error) {
	var a FillAmendment
	if err := sc.Scan(&a.ID, &a.FillID, &a.TradeID, &a.OrderID, &a.Code, &a.TradedAt,
		&a.Price, &a.Qty, &a.UserID, &a.OrigSide, &a.NewSide, &a.OrigAmount,
		&a.Reason, &a.Operator, &a.Status, &a.CreatedAt, &a.AppliedAt); err != nil {
		return nil, err
	}
	a.AmendKey = a.AmendKeyOf()
	return &a, nil
}

// RawFillForUser 按 ID 读**原始**成交行（不走视图），并套用与成交簿展示一致的可见性规则：
// 归属本账号的行 + 遗留全局行（user_id=”）。
// 为什么 HTTP 层需要它：勘误入口拿的是 fills.id，若直接按 id 建勘误，多账号部署下任一管理员
// 都能改到别人账上的成交（越权面）。这里先把"这笔是否可见"钉住，再谈勘误。
// English: reads the RAW fill row by id with the same visibility rule the trade ledger uses
// (own rows plus legacy global rows) — the amendment endpoint must not let one account's admin
// re-book another account's evidence.
func (d *DB) RawFillForUser(userID string, fillID int64) (RealFill, error) {
	var f RealFill
	err := d.db.QueryRow(`SELECT id, COALESCE(order_id,''), code, side, price, qty, COALESCE(amount,0),
		traded_at, COALESCE(signal_id,''), COALESCE(user_id,''), COALESCE(fee,0), COALESCE(stamp_tax,0),
		COALESCE(serial,''), COALESCE(trade_id,'')
		FROM fills WHERE id=? AND (user_id=? OR user_id='')`, fillID, userID).
		Scan(&f.ID, &f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount,
			&f.TradedAt, &f.SignalID, &f.UserID, &f.Fee, &f.StampTax, &f.Serial, &f.TradeID)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrFillAmendmentNoFill
	}
	if err != nil {
		return f, err
	}
	f.OrigSide = f.Side
	f.AmendKey = FillAmendKey(f.TradeID, f.OrderID, f.Code, f.TradedAt, f.Price, f.Qty)
	return f, nil
}

// CountAppliedAmendmentsForFill 某原始行当前生效勘误数（HTTP 层回显"这笔已生效"用）。
func (d *DB) CountAppliedAmendmentsForFill(fillID int64) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM fill_amendments WHERE fill_id=? AND status='applied'`, fillID).Scan(&n)
	return n, err
}

// isUniqueIndexErr 判断是否唯一约束冲突（两个 partial index 的报错文案都以 UNIQUE constraint
// 收尾；驱动为 modernc.org/sqlite，措辞与官方 SQLite 一致）。
func isUniqueIndexErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
