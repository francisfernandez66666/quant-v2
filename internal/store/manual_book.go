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
//     三态判据立刻判成新鲜。这正是 §2.12「手动读数不得进任何'必须新鲜'硬闸」禁止的形态，
//     而且它不会以报错的方式暴露，只会表现为"某天用一笔手敲进来的钱买了不该买的票"。
//   - 加列本身不被 schemaVersion 断言拦（本包先例：can_use_qty / anchor_price / anchor_source 都是
//     惰性 ALTER，schemaVersion 恒 2，见 schema_version_bump_test.go 的 E3），
//     但"带 source 列的柜台表"要求**每一个既有读者都会判这个列**——判漏一处就是假账。
//   - 新表是**结构性**满足：硬闸的取数 SQL 一个字都不用改，也不可能读到手动行。
//     代价是"同一账户两本账"，所以读数面必须把来源与时刻如实标出来（见 ManualSourceLabel / ManualAsOf）。
//
// English: a *separate* manual book rather than a source column on real_positions/real_account, because
// those two tables feed fail-closed hard gates (§M12-A cash freshness, T+1 sellable, concentration, buy
// discipline) whose readers are outside this batch's edit boundary — a manual row there would itself
// read as "just reported" and unlock the auto-buy leg with no code change at all.
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
	UserID    string  `json:"user_id,omitempty"`
}

// ManualAccount 一行手动资金（账户级：可用资金可选填、总资产可选）。
type ManualAccount struct {
	UserID        string  `json:"user_id"`
	AvailableCash float64 `json:"available_cash"` // 可用资金（元）
	TotalAsset    float64 `json:"total_asset"`    // 总资产（元；0=未填）
	Source        string  `json:"source"`         // 恒为 ManualSourceLabel
	AsOf          string  `json:"asof"`           // 用户声称的数据时刻
	UpdatedAt     string  `json:"updated_at"`     // 服务端落库时刻
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
		updated_at     TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store migrate manual_account create: %w", err)
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
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, source, asof, updated_at, user_id
		FROM manual_positions WHERE user_id = ? ORDER BY ts_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualPosition
	for rows.Next() {
		var p ManualPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Source, &p.AsOf, &p.UpdatedAt, &p.UserID); err != nil {
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
	err := d.db.QueryRow(`SELECT available_cash, total_asset, cash_filled, source, asof, updated_at
		FROM manual_account WHERE user_id = ?`, userID).
		Scan(&st.Account.AvailableCash, &st.Account.TotalAsset, &cashFilled,
			&st.Account.Source, &st.Account.AsOf, &st.Account.UpdatedAt)
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
