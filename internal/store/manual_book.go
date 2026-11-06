// Package store — SQLite 历史数据存储层。
// manual_book.go：§2.12（2026-11-04 外脑化）「手动维护账」——桥 11-05 断后持仓/可用资金不再由柜台
// 自动上报，owner 在量仔里手工维护这一本账，量仔据此出可执行的买卖信号卡。
//
// 为什么是**新表**而不是给 real_positions / real_account 加一个 source 列（本批现场读码后的结论）：
//   - 这两张表是**硬闸的输入**，而读它们的代码不在本批改动边界内：
//     `internal/trading/controller.go` 的 `AvailableCash`（§M12-A 资金三态：按 real_account.updated_at
//     与 30 分钟窗口判新鲜，过期即 fail-close 不自动买）、`internal/risk/gate.go`（T+1 可卖量、
//     集中度、买入纪律的可用资金）、以及打分循环/收盘对账的持仓取数。
//     手动行一旦落进这两张表，**不必改任何调用方就已经把钱放了**：updated_at 就是"刚写的"，
//     三态判据立刻判成新鲜——而那个"新鲜"其实是一个人敲的数字，不是券商回报。
//     这条与 owner 2026-11-06 裁决 1（手动账喂买卖硬闸）**不冲突**：进闸走 resolver 显式选源并带
//     asof/接管两条可用性判据，靠的是"读数知道自己是谁给的"，不是靠伪造柜台回报时间戳。
//   - 加列本身不被 schemaVersion 断言拦（本包先例：can_use_qty / anchor_price / anchor_source 都是
//     惰性 ALTER，schemaVersion 恒 2，见 schema_version_bump_test.go 的 E3），
//     但"带 source 列的柜台表"要求**每一个既有读者都会判这个列**——判漏一处就是假账。
//   - 新表是**结构性**满足：硬闸的取数 SQL 一个字都不改，也不可能**读到**手动行——
//     手动值参与硬闸走的是 `ManualBookViewFor` + 上层 resolver 的**显式选源**（owner 2026-11-06 裁决 1），
//     不是靠把行灌进柜台输入表。代价是"同一账户两本账"，所以读数面必须把来源与时刻如实标出来
//     （见 ManualSourceLabel / ManualAsOf / ManualBookView）。
//   - 为什么"经 resolver 进闸"仍然要求"永不写进 real_*"：柜台为准这条判据的前提是柜台账干净。
//     手动行一旦混进去，`updated_at` 就同时可能是"刚回报"和"刚手敲"，两把尺（§M12-A 30 分钟、
//     §A1-FRESH 当前交易日）都会把后者读成前者，于是"柜台恢复后自动让位"这件事失去依据。
//
// English: a *separate* manual book rather than a source column on real_positions/real_account.
// Hard gates may still consume manual values, but only through the explicit source-selection in
// ManualBookViewFor and the engine resolver — never by writing rows into the counter tables,
// which is what keeps "counter wins" decidable at all.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

// ManualSourceLabel 手动账的来源标记：落库与读数面共用同一个字面量。
// 为什么不给 UI 拼字符串：这五个字是判据——前端按它决定要不要挂"柜台真值"的样式，
// 运维也按它在库里区分两本账。
const ManualSourceLabel = "manual"

// ErrInvalidManualPosition 手动行字段级校验失败的哨兵错误：**整批拒收**，语义与 §F2 的柜台快照同源。
// 为什么手动面也要整批拒收而不是跳过坏行：手敲清单出错（多一位、后缀写错）时，
// "坏行被跳过"会让页面显示成"这份账本来就少一只"，而 owner 以为已经录进去了——
// 两本账对不上时必须有一边响亮地失败。
var ErrInvalidManualPosition = errors.New("手动持仓行校验失败")

// ManualPosition 一行手动持仓（用户声称，不是柜台证据）。
type ManualPosition struct {
	TsCode    string  `json:"ts_code"`    // 6 位数字 + .SH/.SZ/.BJ（与柜台持仓同一格式判据）
	Name      string  `json:"name"`       // 股票名（可空，读数面按代码兜底）
	Qty       int     `json:"qty"`        // 持有股数（必须 >0；要清仓请把这一行从清单里删掉）
	CostPrice float64 `json:"cost_price"` // 成本价（可选，0=未填；不参与任何判定）
	Source    string  `json:"source"`     // 恒为 ManualSourceLabel：由本包写入，调用方无权改写
	AsOf      string  `json:"asof"`       // 用户声称"这份账是几点的数据"
	UpdatedAt string  `json:"updated_at"` // 服务端落库时刻（北京时间墙钟）
	// TakenOverAt §MANUAL-BOOK-UNIFY（2026-11-06 裁决 5）：柜台恢复回报的时刻。
	// 非空且 asof ≤ 它 ⇒ 这本账已被柜台接管过，下次桥断**不许**自动复活（人敲的账停在接管那一刻，
	// 而柜台已经证明自己能报数了）；行本身保留，作为"我改过哪些"的证据。
	TakenOverAt string `json:"taken_over_at,omitempty"`
	UserID      string `json:"user_id,omitempty"`
}

// ManualAccount 一行手动资金（账户级：可用资金可选填、总资产可选）。
type ManualAccount struct {
	UserID        string  `json:"user_id"`
	AvailableCash float64 `json:"available_cash"` // 可用资金（元）
	TotalAsset    float64 `json:"total_asset"`    // 总资产（元；0=未填）
	Source        string  `json:"source"`         // 恒为 ManualSourceLabel
	AsOf          string  `json:"asof"`           // 用户声称的数据时刻
	UpdatedAt     string  `json:"updated_at"`     // 服务端落库时刻
	// TakenOverAt 同 ManualPosition：柜台恢复回报的时刻（§MANUAL-BOOK-UNIFY 裁决 5）。
	TakenOverAt string `json:"taken_over_at,omitempty"`
}

// ManualFlow 一行改账流水（§MANUAL-BOOK-UNIFY 2026-11-06）。
//
// 为什么增量改账要另记流水，而不是只 UPDATE 当前值：
//  1. T+1 本地推算要扣"当日买入"，而桥断期间人工在券商 App 下的单不进 orders 表——
//     改账买入是这条链上唯一有据可查的当日增量，没有它，手动态的可卖量会算成"昨天全量"（偏松）；
//  2. 资金联动（买扣卖加）必须能从"当前值"回看到"是怎么变成这个数的"，
//     否则一次手滑改错资金只能靠猜，而两本账对不上时最需要的是过程；
//  3. 柜台接管后人工核对"我改过哪些"靠它。
//
// 按北京时间交易日 day-keyed：新交易日不复活、不累计（与 signal_decisions / t_rounds 同纪律）。
type ManualFlow struct {
	ID          int64   `json:"id"`
	UserID      string  `json:"user_id"`
	Day         string  `json:"day"`          // 北京交易日 YYYY-MM-DD
	TsCode      string  `json:"ts_code"`      // set_cash 时为空串
	Op          string  `json:"op"`           // new|add|reduce|tp|close|set_cash
	Side        string  `json:"side"`         // 买入|卖出|资金
	Qty         int     `json:"qty"`          // 本笔数量（资金笔为 0）
	Price       float64 `json:"price"`        // 本笔成交价（资金笔为 0）
	AmountDelta float64 `json:"amount_delta"` // 对可用资金的影响（买为负、卖为正）
	CashAfter   float64 `json:"cash_after"`   // 本笔之后的可用资金读数
	SeededFrom  string  `json:"seeded_from"`  // counter|manual|none：本笔基线从哪本账来
	AsOf        string  `json:"asof"`         // 用户声称时刻（缺省＝服务端当前时刻）
	CreatedAt   string  `json:"created_at"`   // 服务端落库时刻
}

// ManualAccountState 手动资金读数（带"有没有这一行"的三态）。
// 为什么不能只返回结构体：零值 ManualAccount{AvailableCash:0} 与"从没手动过资金"在两本账里
// 长得一模一样，而下游要区分"owner 说我真的没钱"（Filled=true，0 就是真值）与
// "owner 还没录"（Present=false，保持今天的柜台口径不变）——与 §M12-A「真 0 与不可得不得混写」同一条纪律。
type ManualAccountState struct {
	Account ManualAccount `json:"account"`
	Present bool          `json:"present"` // false=没有任何手动资金行
	Filled  bool          `json:"filled"`  // true=显式填过可用资金（含真 0）
}

// ensureManualBook 幂等建表（首次使用即创建）。姿势与本包 ensureRealAccountTable 同族：
// 惰性 DDL、不进集中迁移清单、不动 schemaVersion（版本号只用来拒绝"旧二进制开新库"，
// 新增一张**独立**表不改变任何既有行的读法，因此不需要抬版本）。
func (d *DB) ensureManualBook() error {
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS manual_positions (
		ts_code     TEXT NOT NULL,
		name        TEXT NOT NULL DEFAULT '',
		qty         INTEGER NOT NULL,
		cost_price  REAL NOT NULL DEFAULT 0,
		source      TEXT NOT NULL DEFAULT 'manual',
		asof        TEXT NOT NULL DEFAULT '',
		updated_at  TEXT NOT NULL,
		user_id     TEXT NOT NULL DEFAULT '',
		taken_over_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (ts_code, user_id)
	)`); err != nil {
		return fmt.Errorf("store migrate manual_positions create: %w", err)
	}
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS manual_account (
		user_id        TEXT PRIMARY KEY,
		available_cash REAL NOT NULL DEFAULT 0,
		total_asset    REAL NOT NULL DEFAULT 0,
		cash_filled    INTEGER NOT NULL DEFAULT 0,
		source         TEXT NOT NULL DEFAULT 'manual',
		asof           TEXT NOT NULL DEFAULT '',
		updated_at     TEXT NOT NULL,
		taken_over_at  TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("store migrate manual_account create: %w", err)
	}
	// §MANUAL-BOOK-UNIFY（2026-11-06）接管指针列：存量库惰性补列（姿势与本包 can_use_qty 同族，
	// 不进集中迁移清单、不动 schemaVersion——新增列不改变任何既有行的读法）。
	for _, col := range []struct{ table, name string }{
		{"manual_positions", "taken_over_at"},
		{"manual_account", "taken_over_at"},
	} {
		has, err := d.hasColumn(col.table, col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := d.db.Exec(`ALTER TABLE ` + col.table + ` ADD COLUMN ` + col.name + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("store migrate %s add %s: %w", col.table, col.name, err)
			}
		}
	}
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS manual_flows (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id      TEXT NOT NULL,
		day          TEXT NOT NULL,
		ts_code      TEXT NOT NULL DEFAULT '',
		op           TEXT NOT NULL,
		side         TEXT NOT NULL DEFAULT '',
		qty          INTEGER NOT NULL DEFAULT 0,
		price        REAL NOT NULL DEFAULT 0,
		amount_delta REAL NOT NULL DEFAULT 0,
		cash_after   REAL NOT NULL DEFAULT 0,
		seeded_from  TEXT NOT NULL DEFAULT '',
		asof         TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store migrate manual_flows create: %w", err)
	}
	if _, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_manual_flows_day ON manual_flows(user_id, day, ts_code)`); err != nil {
		return fmt.Errorf("store migrate manual_flows index: %w", err)
	}
	return nil
}

// validManualTsCode 手动行的代码判据：**复用**柜台持仓那条正则 realTsCodeRe（§F2）。
// 为什么复用而不是"手动面宽松一点"：同一份代码格式在两本账里必须同判据，否则手动账能写
// `600000`（无后缀）而柜台账永远写不进，两边对账时永远配不上——那正是 §BUG-INDEX 那族坑的形状。
func validManualTsCode(tsCode string) bool {
	return realTsCodeRe.MatchString(strings.TrimSpace(tsCode))
}

// PutManualPositions 账号级**全量** upsert（H2）：按 ts_code 幂等写入本批清单，
// 并删掉该账号手动账里不在清单中的行（"全量"的字面语义——手工维护的是一张清单，不是流水）。
//   - rows 为 nil 表示"本次不动持仓"（只改资金），与"空清单＝清仓"区分开：
//     只改可用资金的那次提交不该把持仓抹平（这是本函数唯一会删行的两个分支之一）。
//   - 空清单（非 nil）＝该账号当前没有手动持仓，全删。
//   - 返回 (写入行数, 删除行数)。
//
// English: account-level full upsert keyed by ts_code; nil = leave positions untouched,
// empty slice = clear them (a cash-only edit must not flatten the holdings).
func (d *DB) PutManualPositions(userID string, rows []ManualPosition, asof string) (int, int, error) {
	if err := d.ensureManualBook(); err != nil {
		return 0, 0, err
	}
	if rows == nil {
		return 0, 0, nil
	}
	// 整批校验前置（格式 / 重复代码 / 数量 / 成本）：任一坏行 ⇒ 一条都不写。
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		code := strings.TrimSpace(rows[i].TsCode)
		if !validManualTsCode(code) {
			return 0, 0, fmt.Errorf("%w: 第 %d 行 ts_code=%q 为空或非法格式（应为 6位数字+.SH/.SZ/.BJ）",
				ErrInvalidManualPosition, i, rows[i].TsCode)
		}
		if seen[code] {
			return 0, 0, fmt.Errorf("%w: 第 %d 行 %s 在同一份清单里出现两次（手动账是清单不是流水，请合并成一行）",
				ErrInvalidManualPosition, i, code)
		}
		seen[code] = true
		if rows[i].Qty <= 0 {
			return 0, 0, fmt.Errorf("%w: %s 的 qty=%d 必须为正整数（清仓请把这一行从清单里删除）",
				ErrInvalidManualPosition, code, rows[i].Qty)
		}
		if rows[i].CostPrice < 0 {
			return 0, 0, fmt.Errorf("%w: %s 的成本价为负（未填请留空）", ErrInvalidManualPosition, code)
		}
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	now := ManualNow()
	written := 0
	for _, p := range rows {
		code := strings.TrimSpace(p.TsCode)
		// 幂等主体：ON CONFLICT(ts_code,user_id) DO UPDATE——同一份清单提交两次，
		// 第二次的读数与第一次逐字节相同（重放既不长行，也不把 asof 洗成空）。
		_, err := tx.Exec(`INSERT INTO manual_positions
			(ts_code, name, qty, cost_price, source, asof, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code, user_id) DO UPDATE SET
				name=COALESCE(NULLIF(excluded.name,''), manual_positions.name),
				qty=excluded.qty,
				cost_price=excluded.cost_price,
				-- source 有意**不**出现在 SET 列表：这列的值由这张表本身定义（手动账只有 manual 这一档），
				-- 允许它被覆盖等于给"把手动行标成柜台真值"留一个写入口。
				asof=excluded.asof,
				updated_at=excluded.updated_at`,
			code, strings.TrimSpace(p.Name), p.Qty, p.CostPrice, ManualSourceLabel, asof, now, userID)
		if err != nil {
			return 0, 0, fmt.Errorf("upsert manual position %s: %w", code, err)
		}
		written++
	}
	// 全量语义的删腿：只删该账号、且不在本次清单里的行。
	// 这里不需要柜台那套"空快照不推进/通道在线才清账"的守卫（那是 ReconcilePositionsForUser
	// 防假快照的边界）：手动账不存在"通道断了返回空列表"，空清单只可能是 owner 亲手提交的清仓。
	deleted := 0
	if len(rows) == 0 {
		res, err := tx.Exec(`DELETE FROM manual_positions WHERE user_id = ?`, userID)
		if err != nil {
			return 0, 0, err
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
	} else {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(rows)), ",")
		args := make([]any, 0, len(rows)+1)
		args = append(args, userID)
		for _, p := range rows {
			args = append(args, strings.TrimSpace(p.TsCode))
		}
		res, err := tx.Exec(`DELETE FROM manual_positions WHERE user_id = ? AND ts_code NOT IN (`+ph+`)`, args...)
		if err != nil {
			return 0, 0, err
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return written, deleted, nil
}

// PutManualAccount 账号级手动资金 upsert（按 user_id 幂等）。
// cashFilled=false 表示这份清单**没填**可用资金（区别于"填了 0"），见 ManualAccountState 的三态。
func (d *DB) PutManualAccount(userID string, cash float64, cashFilled bool, totalAsset float64, asof string) error {
	if err := d.ensureManualBook(); err != nil {
		return err
	}
	if cash < 0 {
		return fmt.Errorf("%w: 可用资金为负（%.2f）", ErrInvalidManualPosition, cash)
	}
	if totalAsset < 0 {
		return fmt.Errorf("%w: 总资产为负（%.2f）", ErrInvalidManualPosition, totalAsset)
	}
	_, err := d.db.Exec(`INSERT INTO manual_account
		(user_id, available_cash, total_asset, cash_filled, source, asof, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			available_cash=excluded.available_cash,
			total_asset=excluded.total_asset,
			cash_filled=excluded.cash_filled,
			-- 与 manual_positions 同一条纪律：source 不进 SET（这张表里没有第二档来源）。
			asof=excluded.asof,
			updated_at=excluded.updated_at`,
		userID, cash, totalAsset, b2i(cashFilled), ManualSourceLabel, asof, ManualNow())
	return err
}

// ManualPositionsForUser 读某账号的手动持仓清单（无行=空表，不报错）。
// 作用域刻意**不**含 user_id=”（与柜台账的遗留全局兜底相反）：
// 手动账是"某个人手敲的声称"，把它当公共行让别的账号看见，等于让 A 的声称冒充 B 的持仓。
func (d *DB) ManualPositionsForUser(userID string) ([]ManualPosition, error) {
	if err := d.ensureManualBook(); err != nil {
		return nil, err
	}
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, source, asof, updated_at, user_id, taken_over_at
		FROM manual_positions WHERE user_id = ? ORDER BY ts_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualPosition
	for rows.Next() {
		var p ManualPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Source, &p.AsOf, &p.UpdatedAt, &p.UserID, &p.TakenOverAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ManualAccountForUser 读某账号的手动资金行（见 ManualAccountState 的三态说明）。
func (d *DB) ManualAccountForUser(userID string) (ManualAccountState, error) {
	var st ManualAccountState
	if err := d.ensureManualBook(); err != nil {
		return st, err
	}
	var cashFilled int
	err := d.db.QueryRow(`SELECT available_cash, total_asset, cash_filled, source, asof, updated_at, taken_over_at
		FROM manual_account WHERE user_id = ?`, userID).
		Scan(&st.Account.AvailableCash, &st.Account.TotalAsset, &cashFilled,
			&st.Account.Source, &st.Account.AsOf, &st.Account.UpdatedAt, &st.Account.TakenOverAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil // 没有手动行：Present=false（不等于"金额为 0"）
	}
	if err != nil {
		return st, err
	}
	st.Account.UserID = userID
	st.Present = true
	st.Filled = cashFilled != 0
	return st, nil
}

// ManualAsOf 返回该账号手动账的最新时刻（持仓行与资金行取较新者）；没有任何行时返回空串。
// 读数面用它拼「手动维护，更新于 <asof>」——这句话的数据源只有一个，不在各组件里各算一遍
// （两处算法必然漂，漂成"页面说更新于 10:00、库里有行是 09:30"就是撒谎）。
func (d *DB) ManualAsOf(userID string) (string, error) {
	if err := d.ensureManualBook(); err != nil {
		return "", err
	}
	var asof string
	err := d.db.QueryRow(`SELECT COALESCE(MAX(asof),'') FROM (
		SELECT asof FROM manual_positions WHERE user_id = ?
		UNION ALL
		SELECT asof FROM manual_account WHERE user_id = ?
	)`, userID, userID).Scan(&asof)
	if err != nil {
		// 读数失败按"没有手动账"处理（调用方据此不渲染标注），但绝不留痕为零：
		// 本函数唯一的产品后果是"这行字要不要出现"，静默失败＝手动账冒充柜台账。
		log.Printf("[store] 手动账时刻读取失败（按无手动账处理，不冒充柜台）user=%s: %v", userID, err)
		return "", nil
	}
	return asof, nil
}

// ManualNow 当前北京时间墙钟（手动账 asof 的缺省值）。
// 单独一个函数是为了让"这份账按哪个钟记"只有一个口径（cntime.Loc 是全系统唯一时区源）；
// 测试造数也走它，避免用例里各写一遍 layout。
func ManualNow() string { return cntime.Now().Format("2006-01-02 15:04:05") }

// manualAsofLayouts 手动账 asof 允许的两种既有形态（与 orders.created_at 的存量口径一致：
// 前 10 位都是北京日，字典序即时间序）。
var manualAsofLayouts = []string{"2006-01-02 15:04:05", time.RFC3339}

// NormalizeManualAsOf 把用户提交的时刻归一成 "2006-01-02 15:04:05"（北京墙钟）。
// 空串＝缺省为服务端当前时刻（H2：asof 取系统时间）。
// 无法解析的一律拒收，**不**退回当前时刻：把打错的时刻悄悄换成"现在"就是伪造新鲜度，
// 而这正是手动账唯一还能伤到人的地方。
func NormalizeManualAsOf(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ManualNow(), nil
	}
	for _, layout := range manualAsofLayouts {
		if t, err := time.ParseInLocation(layout, s, cntime.Loc); err == nil {
			return t.Format("2006-01-02 15:04:05"), nil
		}
	}
	return "", fmt.Errorf("%w: asof=%q 无法识别（期望 2006-01-02 15:04:05 或 RFC3339）", ErrInvalidManualPosition, s)
}

// SnapshotBelongsToDay §MANUAL-BOOK-UNIFY（2026-11-06）：**全仓唯一**的"快照是否属于当前交易日"判据。
//
// 为什么收在 store 而不是留在 risk 包：同一把尺现在有三处要吃——
// risk.Gate 的 T+1 柜台可卖量信任、引擎 resolver 的持仓维度选源、trading 控制器的资金维度选源。
// 三处各抄一份必然漂（§165 段序单点那条教训同形：按开关推算的那份会漏掉一段）。
// 本包不 import risk，risk import store，所以真源只能落在这里。
//
// 语义与改前的 risk.positionSnapshotFresh 逐字一致：空串/短串/无法解析一律返回 true
// （保守维持"可信"，不扩大回归面）；早于 day 返回 false。
// English: the single canonical "does this snapshot belong to today" test, moved down to store
// so the risk gate, the engine resolver and the controller all read one ruler instead of three copies.
func SnapshotBelongsToDay(updatedAt, day string) bool {
	if updatedAt == "" {
		return true
	}
	ds := updatedAt
	if len(ds) > 10 {
		ds = ds[:10]
	}
	if len(ds) != 10 {
		return true
	}
	pt, err1 := time.Parse("2006-01-02", ds)
	tt, err2 := time.Parse("2006-01-02", day)
	if err1 != nil || err2 != nil {
		return true
	}
	return !pt.Before(tt)
}

// manualAsofUsable 手动读数今天还能不能当输入用（owner 2026-11-06 裁决 1 的两条前置判据）。
//
// 三条都不满足即"读不到"，调用方照旧 fail-closed：
//  1. asof 非空且**日期正好等于**当前交易日——几天前手敲的那本账不许在下次桥断时自动顶上
//     （"以手工为准"的前提是这份手工读数本身就是按今天维护的）；
//  2. 未来时刻同样拒（人敲错一个日期就解开资金闸，方向是放钱，必须响亮地拒）；
//  3. 已被柜台接管（taken_over_at 非空）且 asof 不晚于接管时刻 ⇒ 这本账停在被接管那一刻，不许复活。
//     接管之后 owner 又改了一笔（asof 更新）自然重新可用——判据只看时刻先后，不需要额外状态位。
func manualAsofUsable(asof, takenOverAt, day string) bool {
	if asof == "" || day == "" {
		return false
	}
	if len(asof) < 10 || asof[:10] != day {
		return false
	}
	if takenOverAt != "" && asof <= takenOverAt {
		return false
	}
	return true
}

// ManualBookView 手动账在某个交易日的可用性读数（两维独立）。
//
// 为什么分两维而不是一个"手动模式"总开关：柜台可能资金回报新鲜而持仓快照停在昨天（或反过来），
// 一个全局开关会把"哪个数是谁给的"重新糊掉——而这两个数一个决定买多少、一个决定能不能卖。
type ManualBookView struct {
	Day             string
	Cash            ManualAccountState
	CashUsable      bool
	Positions       []ManualPosition // 只含"可用"的行（逐行判 asof）
	PositionsUsable bool
	PositionsAsOf   string // 可用行里最新的 asof
	CashAsOf        string
	TakenOverAt     string // 持仓行与资金行里最新的接管时刻（读数面显示"已被柜台接管"）
}

// ManualBookViewFor 装配该账号在 day 这一天的手动账可用性。
// 读失败一律把错误原样带回（调用方 fail-closed）——把手动账读不到折成"没有手动账"，
// 与折成"零仓位"是同一族错误：三个不同的事实（读不到 / 没录 / 录了但过期）在下游必须可分。
func (d *DB) ManualBookViewFor(userID, day string) (ManualBookView, error) {
	var v ManualBookView
	v.Day = day
	if err := d.ensureManualBook(); err != nil {
		return v, err
	}
	rows, err := d.ManualPositionsForUser(userID)
	if err != nil {
		return v, err
	}
	acc, err := d.ManualAccountForUser(userID)
	if err != nil {
		return v, err
	}
	v.Cash = acc
	v.CashAsOf = acc.Account.AsOf
	v.CashUsable = acc.Present && acc.Filled && manualAsofUsable(acc.Account.AsOf, acc.Account.TakenOverAt, day)
	if acc.Account.TakenOverAt != "" {
		v.TakenOverAt = acc.Account.TakenOverAt
	}
	for _, p := range rows {
		if !manualAsofUsable(p.AsOf, p.TakenOverAt, day) {
			continue
		}
		v.Positions = append(v.Positions, p)
		if p.AsOf > v.PositionsAsOf {
			v.PositionsAsOf = p.AsOf
		}
		if p.TakenOverAt > v.TakenOverAt {
			v.TakenOverAt = p.TakenOverAt
		}
	}
	v.PositionsUsable = len(v.Positions) > 0
	return v, nil
}

// MarkManualTakenOver 柜台恢复回报后给该账号的手动账打接管指针（owner 2026-11-06 裁决 5）。
//
// 三条边界：
//  1. **只在柜台真的落库之后调用**（调用点在 server 的 positions/account 回报成功分支）——
//     回报失败也打标记，等于把"没回来的账"说成"回来了"，手动账会当场变成读不到而没人报警；
//  2. **不删任何行**：手动账与 manual_flows 都是"我改过哪些"的证据；
//  3. 幂等：同一时刻重放结果逐字节相同；更晚的接管时刻覆盖更早的（语义＝最近一次柜台自证）。
//
// 返回被打标的行数（持仓行 + 资金行合计），调用方据此留痕。
func (d *DB) MarkManualTakenOver(userID, at string) (int, error) {
	if err := d.ensureManualBook(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(at) == "" {
		return 0, errors.New("store: MarkManualTakenOver 需要非空接管时刻")
	}
	total := 0
	res, err := d.db.Exec(`UPDATE manual_positions SET taken_over_at = ?
		WHERE user_id = ? AND taken_over_at <> ?`, at, userID, at)
	if err != nil {
		return 0, fmt.Errorf("mark manual positions taken over: %w", err)
	}
	n, _ := res.RowsAffected()
	total += int(n)
	res2, err := d.db.Exec(`UPDATE manual_account SET taken_over_at = ?
		WHERE user_id = ? AND taken_over_at <> ?`, at, userID, at)
	if err != nil {
		return total, fmt.Errorf("mark manual account taken over: %w", err)
	}
	n2, _ := res2.RowsAffected()
	total += int(n2)
	return total, nil
}

// ManualTodayBoughtQty §MANUAL-BOOK-UNIFY：该码当日通过改账买入的股数（T+1 本地推算的第三项扣减）。
//
// 为什么必须有这条腿：桥断期间人工在券商 App 下的买单不进 orders 表，而 T+1 闸的"当日买入"
// 原本只数 orders——看不见就会把"昨天 1000 股 + 今天刚买 300 股"算成可卖 1000 股（偏松方向）。
// 改账是唯一有据可查的当日增量，所以手动态的可卖量扣减只能靠它；没走改账入口的那笔仍然看不见，
// 这是裁决 1 的固有代价，已写进方案 §9.1，不在这里假装解决。
func (d *DB) ManualTodayBoughtQty(userID, tsCode, day string) (int, error) {
	if err := d.ensureManualBook(); err != nil {
		return 0, err
	}
	var n int
	err := d.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM manual_flows
		WHERE user_id = ? AND day = ? AND ts_code = ? AND side = '买入'`, userID, day, tsCode).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}
