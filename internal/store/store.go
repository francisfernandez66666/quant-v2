// Package store — SQLite 历史数据存储层（B 阶段数据地基）。
// 基于纯 Go 驱动 modernc.org/sqlite（无 cgo），存放 Tushare 历史数据（日线/复权因子/
// 每日指标/涨跌停/财务指标/利润表/现金流/指数）与研究产物。
// 仅服务离线研究链路（dataload/回测/因子/自动研究），交易时段的实时数据仍走内存 JSON。
// （Package store is the SQLite historical-data persistence layer for the Phase-B data foundation,
// built on the pure-Go modernc.org/sqlite driver (no cgo). It holds Tushare history used only by the
// offline research chain; realtime trading data keeps flowing through in-memory JSON as before.）
package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（driver 名 "sqlite"）
)

// DB 历史数据存储句柄。
// （DB wraps the research database handle.）
type DB struct {
	db *sql.DB // SQLite 数据库连接
	// costGuardDrops §N-6（2026-09-22 傍晚批复验）：实盘对账 upsert 里「本地含费成本优先」守卫
	// 丢弃快照值的累计次数（进程内计数，重启归零）。放在实例上而非包级全局：计数与库连接同
	// 生命周期，测试可对各自 DB 独立断言。本批主题是「静默失效」——保护本身也不许静默生效。
	costGuardDrops atomic.Int64
	// eventBasisAborted §ADJ-BASIS（2026-09-23）：backtest_event_results 的口径位主键重建被
	// 「行数守恒」守卫中止（或服务启动时读到未知表形态）时置真。真 = 本库该表**没有**可用的
	// adj_basis 键列，此时读写侧一律走保守路径（见 backtest_jobs.go / emotion_matrix.go）：
	// 读恒判未命中、写直接拒绝并留痕——宁可整轮重算，也绝不把改前旧行当新结果报出去。
	// 中止只影响回测缓存命中率，不影响交易路径，故 Open 不返回错误（应用照常启动）。
	// English: set when the basis-in-PK rebuild was aborted by the row-conservation guard. Readers
	// then always miss and writers refuse, so a half-migrated cache fails toward recomputation
	// instead of serving pre-fix rows as fresh results; the app still starts.
	eventBasisAborted bool
	// eventBasisReason 中止原因（仅 eventBasisAborted 为真时有意义，日志/错误信息用）。
	eventBasisReason string
	// eventBasisWarn 口径位相关告警的日志去重（读/写/情绪矩阵各一次，防逐事件、逐请求刷屏）。
	eventBasisWarnRead   sync.Once
	eventBasisWarnWrite  sync.Once
	eventBasisWarnMatrix sync.Once
}

// Open 打开（必要时创建）研究数据库并初始化表结构。
// （Open opens (creating if needed) the research DB and initializes the schema.）
func Open(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("store mkdir: %v", err)
	}
	// busy_timeout 避免多进程（dataload 与回测）并发写时报 database is locked；
	// WAL 提升并发读写吞吐。English: busy_timeout avoids "database is locked" across the
	// dataload/backtest processes; WAL boosts concurrent read/write throughput.
	// §W4-c journal_size_limit：WAL 收尾后自动截断到 64MB，防夜间大批量装载后 -wal 滞留膨胀
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=journal_size_limit(67108864)")
	if err != nil {
		return nil, fmt.Errorf("store open %s: %v", dbPath, err)
	}
	// 连接池收敛为少量连接：SQLite 单写者模型下并发连接反而放大锁竞争。
	db.SetMaxOpenConns(4)
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	// §D-5（GAP_VERIFY_20260917_PM）库文件权限 0600：trading.db/live.db 含 LLM 密钥池与 QMT token
	// 的 KV 快照，默认 0644 在共享主机/备份外泄面过大。auth.json 0600 先例（§A3）同口径收口。
	// -wal/-shm 旁文件同样处理（WAL 下常驻）。个别平台 chmod 不支持时静默跳过。
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		_ = os.Chmod(p, 0o600)
	}
	return d, nil
}

// Close 关闭数据库。
// （Close closes the database.）
func (d *DB) Close() error { return d.db.Close() }

// migrate 建表（幂等，IF NOT EXISTS）。
// 所有表均为历史研究数据，主键即 Tushare 主键，便于 INSERT OR REPLACE 断点续传。
// （migrate creates the schema idempotently; primary keys match Tushare's for resumable upserts.）
func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS stocks (
			ts_code TEXT PRIMARY KEY, name TEXT, area TEXT, industry TEXT,
			market TEXT, list_date TEXT, delist_date TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS trade_cal (
			cal_date TEXT PRIMARY KEY, is_open INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS daily (
			ts_code TEXT NOT NULL, trade_date TEXT NOT NULL,
			open REAL, high REAL, low REAL, close REAL, pre_close REAL,
			change REAL, pct_chg REAL, vol REAL, amount REAL,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE TABLE IF NOT EXISTS adj_factor (
			ts_code TEXT NOT NULL, trade_date TEXT NOT NULL, adj_factor REAL,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE TABLE IF NOT EXISTS daily_basic (
			ts_code TEXT NOT NULL, trade_date TEXT NOT NULL,
			turnover_rate REAL, turnover_rate_f REAL, volume_ratio REAL,
			pe REAL, pe_ttm REAL, pb REAL, ps REAL, ps_ttm REAL, pcf_ttm REAL,
			dv_ratio REAL, dv_ttm REAL, total_share REAL, float_share REAL,
			free_share REAL, total_mv REAL, circ_mv REAL, is_st INTEGER,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE TABLE IF NOT EXISTS stk_limit (
			ts_code TEXT NOT NULL, trade_date TEXT NOT NULL,
			up_limit REAL, down_limit REAL,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE TABLE IF NOT EXISTS index_daily (
			ts_code TEXT NOT NULL, trade_date TEXT NOT NULL,
			open REAL, high REAL, low REAL, close REAL, pre_close REAL,
			change REAL, pct_chg REAL, vol REAL, amount REAL,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE TABLE IF NOT EXISTS fina_indicator (
			ts_code TEXT NOT NULL, end_date TEXT NOT NULL, ann_date TEXT,
			eps REAL, roe REAL, roe_waa REAL, roa REAL, roe_dt REAL,
			grossprofit_margin REAL, netprofit_margin REAL, debt_to_assets REAL,
			yoy_or REAL, yoy_net_profit REAL, or_yoy REAL, netprofit_yoy REAL,
			PRIMARY KEY (ts_code, end_date)
		)`,
		`CREATE TABLE IF NOT EXISTS income (
			ts_code TEXT NOT NULL, end_date TEXT NOT NULL,
			n_income_attr_p REAL, revenue REAL, total_revenue REAL,
			PRIMARY KEY (ts_code, end_date)
		)`,
		`CREATE TABLE IF NOT EXISTS cashflow (
			ts_code TEXT NOT NULL, end_date TEXT NOT NULL,
			n_cashflow_act REAL, n_cashflow_inv_act REAL, n_cashflow_fnc_act REAL,
			PRIMARY KEY (ts_code, end_date)
		)`,
		// 研究候选库（B5 自动研究闭环：优化器产出 → 人工审批 → 应用）
		`CREATE TABLE IF NOT EXISTS research_candidates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'proposed',
			factors TEXT,
			weights TEXT,
			metric REAL,
			ic_mean REAL,
			ir REAL,
			avg_excess REAL,
			horizon INTEGER,
			reason TEXT,
			guard TEXT DEFAULT 'standard',
			params TEXT DEFAULT ''
		)`,
		// 参数扫参结果（§P2-c）：optimize 任务 TOP-N 排名，审批后转规则级参数覆盖。
		// English: parameter-sweep rankings per task; approvals become rule-level overrides.
		`CREATE TABLE IF NOT EXISTS optimization_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL,
			rank INTEGER NOT NULL,
			strategy TEXT NOT NULL,
			strategy_kind TEXT DEFAULT '',
			params TEXT NOT NULL,
			objective TEXT DEFAULT '',
			win_rate REAL DEFAULT 0,
			profit_factor REAL DEFAULT 0,
			win INTEGER DEFAULT 0,
			loss INTEGER DEFAULT 0,
			avg_win_pct REAL DEFAULT 0,
			avg_loss_pct REAL DEFAULT 0,
			expectancy REAL DEFAULT 0,
			stop_loss REAL DEFAULT 0,
			avg_hold_days REAL DEFAULT 0,
			trigger_count INTEGER DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)`,
		// §D1 各战法独立寻优参数池：四维步进搜索空间（未配置走代码内置默认池）
		`CREATE TABLE IF NOT EXISTS sweep_pool_configs (
			strategy TEXT PRIMARY KEY,
			tp_from REAL, tp_to REAL, tp_step REAL,
			sl_from REAL, sl_to REAL, sl_step REAL,
			hold_from INTEGER, hold_to INTEGER, hold_step INTEGER,
			score_from REAL, score_to REAL, score_step REAL,
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)`,
		// §回测自动增强 A0：回测引擎增强配置单行 JSON 载体（无记录=不启用=旧行为；
		// 类型与校验归 config 包，store 只存原文，见 backtest_settings.go）。
		// English: single-row JSON settings for the backtest enhancement (absent = disabled).
		`CREATE TABLE IF NOT EXISTS backtest_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			config_json TEXT NOT NULL,
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)`,
		// 同花顺（新）日K（§HITHINK_DATA_SOURCE_PLAN）：主源回测行情，与旧 daily 物理分离。
		`CREATE TABLE IF NOT EXISTS ths_daily (
			ts_code TEXT NOT NULL,
			trade_date TEXT NOT NULL,
			open REAL DEFAULT 0,
			high REAL DEFAULT 0,
			low REAL DEFAULT 0,
			close REAL DEFAULT 0,
			vol REAL DEFAULT 0,
			amount REAL DEFAULT 0,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		// 板块历史（E5）：按行业聚合的板块日线（离线重建），供形态战法回测与因子环境分组。
		// English: sector daily history (E5) — per-industry aggregated board daily bars, rebuilt offline
		// from daily+stk_limit, used for pattern backtests and factor environment grouping.
		`CREATE TABLE IF NOT EXISTS sector_history (
			trade_date TEXT NOT NULL,
			industry TEXT NOT NULL,
			limitup_cnt INTEGER DEFAULT 0,
			change_pct REAL DEFAULT 0,
			member_count INTEGER DEFAULT 0,
			top_stocks TEXT,
			PRIMARY KEY (trade_date, industry)
		)`,
		// 回测任务中心：job 持久化（单候选 + 夜间全量都记录），quant 重启后可查/可恢复/可续跑。
		// kind='candidate' 单候选回测（candidate_id 对应候选）；kind='nightly' 夜间全量回测（candidate_id=0）。
		// UNIQUE(kind,candidate_id) 保证同一候选的任务只有一条，重跑覆盖。
		// English: backtest task center — jobs are persisted (both per-candidate and nightly runs), so they
		// survive restarts and can be resumed. kind='candidate' maps candidate_id to a candidate; 'nightly'
		// uses candidate_id=0. UNIQUE(kind,candidate_id) keeps one row per candidate; reruns overwrite it.
		`CREATE TABLE IF NOT EXISTS backtest_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL,
			candidate_id INTEGER DEFAULT 0,
			status TEXT NOT NULL,
			progress TEXT DEFAULT '',
			avg_excess REAL,
			error TEXT,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE(kind, candidate_id)
		)`,
		// 回测断点缓存：按候选 + 事件唯一键（事件日+行业+**复权口径位**）存完整 EventResult JSON，
		// 中断/重启后续跑只重算未缓存的事件；同一候选重跑覆盖（INSERT OR REPLACE 语义）。
		// adj_basis §ADJ-BASIS（2026-09-23）：数值口径进主键。研究断点键早已带口径位
		// （internal/research.AdjBaselineVersion），本表却漏了——§ADJ(P0-A 复权因子前向填充) 这种
		// "入口不变、数值全变"的修复不会让本表缓存失效，离线重放同一候选会直接把改前的行当成
		// 新结果报出来（"数据是旧的 / 流水线是绿的"同时成立）。空串 '' 是**改前旧证据行**的哨兵值，
		// 不代表任何当前口径，读写侧一律排除（见 backtest_jobs.go）。
		// English: backtest checkpoint cache — full EventResult JSON per (candidate, event-date,
		// industry, adjustment basis); a resumed run only recomputes uncached events. '' in adj_basis
		// is the sentinel for pre-basis evidence rows and is never presented as a current result.
		`CREATE TABLE IF NOT EXISTS backtest_event_results (
			candidate_id INTEGER NOT NULL,
			event_date TEXT NOT NULL,
			industry TEXT NOT NULL,
			adj_basis TEXT NOT NULL DEFAULT '',
			result_json TEXT NOT NULL,
			PRIMARY KEY (candidate_id, event_date, industry, adj_basis)
		)`,
		// 研究任务队列（子系统统一改造一期）：quant(API) 与 researchd 夜间作业都只入队，
		// 唯一消费者是 researchd worker（盘后门控 + 优先级 + kill 抢占）。
		// 详见 docs/RESEARCH_TASK_QUEUE_PLAN.md §4。
		// English: research task queue (unified-subsystem phase 1) — both quant(API) and the researchd
		// nightly chain only enqueue; the single consumer is the researchd worker (after-hours gate +
		// priority + kill-preemption). See docs/RESEARCH_TASK_QUEUE_PLAN.md §4.
		`CREATE TABLE IF NOT EXISTS research_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			ref_id INTEGER DEFAULT 0,
			priority TEXT NOT NULL DEFAULT 'low',
			status TEXT NOT NULL DEFAULT 'queued',
			progress TEXT DEFAULT '',
			result_num REAL DEFAULT 0,
			result_text TEXT DEFAULT '',
			error TEXT DEFAULT '',
			payload TEXT NOT NULL DEFAULT '{}',
			chain_day TEXT DEFAULT '',
			chain_seq INTEGER DEFAULT 0,
			control TEXT DEFAULT '',
			retry_count INTEGER NOT NULL DEFAULT 0,
			fail_fp TEXT NOT NULL DEFAULT '',
			fail_streak INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			started_at TEXT DEFAULT '',
			finished_at TEXT DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// D1 评分历史（§历史D1方案B）：盘中 LLM 打标的 (日期,股票,评分) 落库，
		// 攒够数据后 N 形回放按触发日 JOIN 当日真实 D1 分，替代固定规则分近似。
		// English: D1 score history — intraday LLM scores persisted per (date, code) so N-shape
		// replay can JOIN the real score of the trigger day instead of a fixed rule-score proxy.
		`CREATE TABLE IF NOT EXISTS d1_scores (
			date TEXT NOT NULL,
			code TEXT NOT NULL,
			score REAL NOT NULL DEFAULT 0,
			blocked INTEGER NOT NULL DEFAULT 0,
			reason TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (date, code)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_d1_date ON d1_scores(date)`,
		// §MARKET_RISK_GATE P8 市场风险档日级留痕：每日一条，记录当日情绪相位/市场状态/合成风险档 + 关键
		// 输入（上涨占比/炸板率/涨停数/连板高度），供分组回测（按风险档切样本看信号胜率/收益差）与复盘。
		// up_ratio/break_rate 等缺失时存 NULL（NaN 弃权），聚合查询按 NULL 跳过，绝不当 0 参与。
		// English: P8 daily risk-tier record — one row/day capturing emotion/market-state/synthesized tier
		// plus key inputs (up-ratio/break-rate/limit-up count/ladder) for tier-grouped backtests and review;
		// missing inputs stored as NULL (NaN abstain), never a fake 0.
		`CREATE TABLE IF NOT EXISTS market_risk_daily (
			trade_date TEXT PRIMARY KEY,
			emotion TEXT DEFAULT '',
			market_state TEXT DEFAULT '',
			risk_tier TEXT DEFAULT '',
			reasons TEXT DEFAULT '',
			up_ratio REAL,
			break_rate REAL,
			max_pos_pct REAL,
			limit_up_count INTEGER DEFAULT 0,
			ladder_height INTEGER DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rtask_state ON research_tasks(status, priority)`,
		`CREATE INDEX IF NOT EXISTS idx_rtask_chain ON research_tasks(chain_day)`,
		// 研究窗口级断点（二期）：discover-factors 各阶段按窗口缓存装配产物（IC 行等），
		// 被抢占/中断后续跑跳过已算窗口；resume_key 含区间+参数哈希，参数变更自动失效。
		// English: window-level checkpoints (phase 2) — per-window artifacts (IC rows) cached per stage
		// so a preempted discovery resumes skipping finished windows; resume_key embeds range+params so
		// parameter changes invalidate automatically.
		`CREATE TABLE IF NOT EXISTS research_ckpts (
			resume_key TEXT NOT NULL,
			stage TEXT NOT NULL,
			win_start TEXT NOT NULL,
			win_end TEXT NOT NULL,
			payload TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (resume_key, stage, win_start, win_end)
		)`,
		// 模拟盘研究落库：盘中模拟盘只在交易时段运行（省内存），盘后把当日成交与每日快照
		// 导出到研究库，供自动研究（夜间 scheduler / research CLI）读取做信号质量与绩效研究。
		// English: paper-to-research export — the paper book only runs during trading hours (memory
		// friendly); after the close its day's fills and daily snapshot are exported into the research DB
		// for auto-research (nightly scheduler / research CLI) to study signal quality and performance.
		`CREATE TABLE IF NOT EXISTS paper_trades (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			code TEXT NOT NULL,
			name TEXT DEFAULT '',
			strategy TEXT DEFAULT '',
			strategy_type TEXT DEFAULT '',
			side TEXT NOT NULL,
			price REAL NOT NULL,
			signal_price REAL DEFAULT 0,
			latency_sec REAL DEFAULT 0,
			qty INTEGER NOT NULL,
			amount REAL NOT NULL,
			filled_at TEXT NOT NULL,
			reason TEXT DEFAULT '',
			UNIQUE(user_id, code, side, filled_at)
		)`,
		// 模拟盘每日快照：每交易日盘后导出一条（现金/市值/净值/已实现/持仓数），按账号+日期唯一。
		// English: paper daily snapshot — one row per trading day after the close (cash/market value/
		// equity/realized/positions), unique per account + date.
		`CREATE TABLE IF NOT EXISTS paper_daily (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			date TEXT NOT NULL,
			cash REAL NOT NULL,
			market_value REAL NOT NULL,
			total_value REAL NOT NULL,
			realized REAL NOT NULL,
			positions INTEGER NOT NULL,
			UNIQUE(user_id, date)
		)`,
		// 模拟盘研究报告摘要：夜间 paper-research 步骤把信号质量与绩效报告落库（按日期+账号 UPSERT），
		// 研究侧可直接查询历史报告。
		// English: paper-research report summary — the nightly paper-research step saves its signal-quality
		// & performance report here (UPSERT per date + account) for queryable research history.
		`CREATE TABLE IF NOT EXISTS paper_research_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date TEXT NOT NULL,
			user_id TEXT NOT NULL,
			summary_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(date, user_id)
		)`,
		// 实盘持仓（AUTO_TRADING_PLAN 真实账本主源）：由国内 QMT 网关全量对账/成交回报驱动，
		// 与纸面 report.Report 完全独立（双账本并存）。ts_code 为持仓唯一键，signal_id 关联开仓信号。
		// English: real book positions (AUTO_TRADING_PLAN live ledger source) — driven by the domestic QMT
		// gateway's reconciliation/fill reports, fully independent of the paper report.Report (dual ledgers).
		`CREATE TABLE IF NOT EXISTS real_positions (
			ts_code TEXT NOT NULL,
			name TEXT DEFAULT '',
			qty INTEGER NOT NULL DEFAULT 0,
			cost_price REAL NOT NULL DEFAULT 0,
			amount REAL NOT NULL DEFAULT 0,
			highest_price REAL NOT NULL DEFAULT 0,
			strategy TEXT DEFAULT '',
			signal_id TEXT DEFAULT '',
			updated_at TEXT NOT NULL,
			user_id TEXT DEFAULT '',
			PRIMARY KEY (ts_code, user_id)
		)`,
		// 实盘委托单：order_id 为网关返回的单号，signal_id 唯一（幂等，防重复下单）。
		// English: real order tickets — order_id from the gateway, signal_id unique (idempotency key).
		`CREATE TABLE IF NOT EXISTS orders (
			order_id TEXT PRIMARY KEY,
			signal_id TEXT NOT NULL,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			status TEXT NOT NULL,
			price REAL,
			qty INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			user_id TEXT DEFAULT '',
			UNIQUE (user_id, signal_id)
		)`,

		// 实盘成交回报：网关成交事件逐条落库（对账/研究用）。
		// English: real fill reports — one row per gateway trade event (reconciliation/research).
		`CREATE TABLE IF NOT EXISTS fills (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id TEXT NOT NULL,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			price REAL NOT NULL,
			qty INTEGER NOT NULL,
			amount REAL NOT NULL,
			traded_at TEXT NOT NULL,
			signal_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			fee REAL DEFAULT 0,
			stamp_tax REAL DEFAULT 0,
			serial TEXT DEFAULT '',
			-- §M4（2026-09-22 PM 批）券商成交编号：网关一直发 trade_id，旧 Go 信封没有这个 tag
			-- → 字段被静默丢弃，本地 fills 只剩 (order_id,traded_at,price,qty) 复合键这一把身份锚。
			-- English: broker trade number — the gateway always sent it, the old Go envelope had no
			-- tag, so it was silently dropped and fills had no exact identity anchor.
			trade_id TEXT DEFAULT ''
		)`,
		// §W3-b 成交回报幂等唯一键：同一委托+同一回报时间戳+同价同量只入账一次，
		// 根除 outbox 重试遇响应丢失时的双倍记账（首尔侧此前零幂等）。
		// ⚠️ 建唯一索引前必须先去重历史行——生产 fills 已有 outbox 重试造成的重复记录，
		// 直接建索引会因冲突失败导致迁移中断、服务起不来。保留每组最早一条（MIN(rowid)）。
		`DELETE FROM fills WHERE rowid NOT IN (
			SELECT MIN(rowid) FROM fills GROUP BY order_id, traded_at, price, qty)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_idem ON fills(order_id, traded_at, price, qty)`,
		// §WS-A T+1/交割单对账查询加速：按交易日聚合成交/费用
		`CREATE INDEX IF NOT EXISTS idx_fills_traded_at ON fills(traded_at)`,
		// §WS-B 券商交割单三方对账结果：每日一条对账差异快照（report_only 落账 / sync_fills 纠偏留痕）。
		`CREATE TABLE IF NOT EXISTS settlement_diff (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT DEFAULT '',
			day TEXT NOT NULL,
			diff_json TEXT NOT NULL,
			fee_diff REAL DEFAULT 0,
			cash_diff REAL DEFAULT 0,
			mode TEXT DEFAULT 'report_only',
			created_at TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_settle_day ON settlement_diff(day)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_settle_user_day ON settlement_diff(user_id, day)`,
		// §WS-C 风控闸口命中计数与每日汇总：供 SLO/审计/UI 卡片（每 user+日+闸 一行，命中自增）。
		`CREATE TABLE IF NOT EXISTS risk_gates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT DEFAULT '',
			trade_date TEXT NOT NULL,
			gate TEXT NOT NULL,
			hits INTEGER NOT NULL DEFAULT 0,
			last_reason TEXT DEFAULT '',
			updated_at TEXT DEFAULT '',
			UNIQUE(user_id, trade_date, gate)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_risk_gates_day ON risk_gates(trade_date)`,
		// §WS-G Shadow 执行器落账：staging 影子引擎只记录决策不真下（signal_id 幂等，同键去重）。
		`CREATE TABLE IF NOT EXISTS shadow_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT DEFAULT '',
			signal_id TEXT NOT NULL,
			code TEXT DEFAULT '',
			name TEXT DEFAULT '',
			strategy TEXT DEFAULT '',
			strategy_id TEXT DEFAULT '',
			side TEXT DEFAULT '',
			price REAL DEFAULT 0,
			qty INTEGER DEFAULT 0,
			amount REAL DEFAULT 0,
			created_at TEXT DEFAULT '',
			UNIQUE(signal_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_shadow_day ON shadow_orders(created_at)`,
		// 常用查询索引（主键外的补充加速）
		`CREATE INDEX IF NOT EXISTS idx_daily_date ON daily(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_db_date ON daily_basic(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_adj_date ON adj_factor(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_stklimit_date ON stk_limit(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_fina_code ON fina_indicator(ts_code)`,
		`CREATE INDEX IF NOT EXISTS idx_sector_date ON sector_history(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_optres_task ON optimization_results(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ths_daily_date ON ths_daily(trade_date)`,
		// 同花顺（新）盘口特色数据（§P1 盘口升级）：涨停/跌停/炸板三池。
		`CREATE TABLE IF NOT EXISTS ths_limit_up_daily (
			trade_date TEXT NOT NULL,
			ts_code TEXT NOT NULL,
			name TEXT DEFAULT '',
			is_st INTEGER DEFAULT 0,
			is_new INTEGER DEFAULT 0,
			price REAL DEFAULT 0,
			pct_chg REAL DEFAULT 0,
			first_seal_time TEXT DEFAULT '',
			continue_cnt INTEGER DEFAULT 0,
			continue_text TEXT DEFAULT '',
			limit_reason TEXT DEFAULT '',
			seal_money REAL DEFAULT 0,
			max_seal_money REAL DEFAULT 0,
			PRIMARY KEY (trade_date, ts_code)
		)`,
		`CREATE TABLE IF NOT EXISTS ths_limit_down_daily (
			trade_date TEXT NOT NULL,
			ts_code TEXT NOT NULL,
			name TEXT DEFAULT '',
			price REAL DEFAULT 0,
			pct_chg REAL DEFAULT 0,
			first_limit_time TEXT DEFAULT '',
			last_limit_time TEXT DEFAULT '',
			turnover_ratio_pct REAL DEFAULT 0,
			open_times INTEGER DEFAULT 0,
			turnover REAL DEFAULT 0,
			PRIMARY KEY (trade_date, ts_code)
		)`,
		`CREATE TABLE IF NOT EXISTS ths_break_pool_daily (
			trade_date TEXT NOT NULL,
			ts_code TEXT NOT NULL,
			name TEXT DEFAULT '',
			price REAL DEFAULT 0,
			pct_chg REAL DEFAULT 0,
			open_times INTEGER DEFAULT 0,
			turnover_ratio_pct REAL DEFAULT 0,
			turnover REAL DEFAULT 0,
			PRIMARY KEY (trade_date, ts_code)
		)`,
		// 连板天梯逐日切片（board_num=连板数；seal_nextday 空=未知）
		`CREATE TABLE IF NOT EXISTS ths_ladder_daily (
			trade_date TEXT NOT NULL,
			board_num INTEGER NOT NULL,
			ts_code TEXT NOT NULL,
			name TEXT DEFAULT '',
			seal_nextday INTEGER,
			sign_level INTEGER DEFAULT 0,
			PRIMARY KEY (trade_date, board_num, ts_code)
		)`,
		// 个股异动原因（当日批查落库，供 D1 辅证与消息推送）
		`CREATE TABLE IF NOT EXISTS ths_anomaly_daily (
			trade_date TEXT NOT NULL,
			ts_code TEXT NOT NULL,
			tag_name TEXT DEFAULT '',
			name TEXT DEFAULT '',
			analysis_content TEXT DEFAULT '',
			keywords TEXT DEFAULT '[]',
			PRIMARY KEY (trade_date, ts_code, tag_name)
		)`,
		// 同花顺（新）估值快照（五项指标，夜间批量入表）
		`CREATE TABLE IF NOT EXISTS ths_valuations_daily (
			trade_date TEXT NOT NULL,
			ts_code TEXT NOT NULL,
			pe_ttm REAL,
			pe_mrq REAL,
			pb_mrq REAL,
			ps_ttm REAL,
			pcf_ttm REAL,
			PRIMARY KEY (trade_date, ts_code)
		)`,
		// 同花顺（新）财务指标五类24项（ability 区分维度，index_id 为具名字段标识）
		`CREATE TABLE IF NOT EXISTS ths_fin_indicators (
			ts_code TEXT NOT NULL,
			report TEXT NOT NULL,
			ability TEXT NOT NULL,
			index_id TEXT NOT NULL,
			value TEXT,
			PRIMARY KEY (ts_code, report, ability, index_id)
		)`,
		// 同花顺（新）累计后复权因子（事件换算生成，锚定衔接旧表基线）。
		`CREATE TABLE IF NOT EXISTS ths_adj_factor (
			ts_code TEXT NOT NULL,
			trade_date TEXT NOT NULL,
			factor REAL NOT NULL,
			PRIMARY KEY (ts_code, trade_date)
		)`,
		// §ENH-A 单票涨停微结构因子面板装配：逐股 (ts_code, trade_date) 索引 seek，
		// 免全表扫（夜间逐窗 5000 股装配；置于三池建表之后，fresh DB 顺序安全）。
		`CREATE INDEX IF NOT EXISTS idx_ths_lu_code ON ths_limit_up_daily(ts_code, trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_ths_bk_code ON ths_break_pool_daily(ts_code, trade_date)`,
	}
	for _, s := range stmts {
		if _, err := d.db.Exec(s); err != nil {
			return fmt.Errorf("store migrate: %w\n%s", err, s)
		}
	}
	// 旧库增量迁移：为已存在的表补新列（幂等）。
	// （Incremental migration: add new columns to tables created by older schema versions.）
	for _, mig := range []struct{ table, column, ddl string }{
		{"daily_basic", "pcf_ttm", "ALTER TABLE daily_basic ADD COLUMN pcf_ttm REAL"},
		{"daily_basic", "is_st", "ALTER TABLE daily_basic ADD COLUMN is_st INTEGER"},
		// 阶段3.4 战法库回测：done 任务的汇总报告文本（胜率/盈亏比等，前端直接展示）
		{"backtest_jobs", "result_text", "ALTER TABLE backtest_jobs ADD COLUMN result_text TEXT DEFAULT ''"},
		// §P2 过程数据：扫参排名行的胜/负/平均盈亏明细（详情展开展示）
		{"optimization_results", "win", "ALTER TABLE optimization_results ADD COLUMN win INTEGER DEFAULT 0"},
		{"optimization_results", "loss", "ALTER TABLE optimization_results ADD COLUMN loss INTEGER DEFAULT 0"},
		{"optimization_results", "avg_win_pct", "ALTER TABLE optimization_results ADD COLUMN avg_win_pct REAL DEFAULT 0"},
		{"optimization_results", "avg_loss_pct", "ALTER TABLE optimization_results ADD COLUMN avg_loss_pct REAL DEFAULT 0"},
		{"optimization_results", "expectancy", "ALTER TABLE optimization_results ADD COLUMN expectancy REAL DEFAULT 0"},
		{"optimization_results", "stop_loss", "ALTER TABLE optimization_results ADD COLUMN stop_loss REAL DEFAULT 0"},
		// §D 热力网格：每战法冠军行携带 止盈×止损 最优期望压缩网格（JSON，前端渲染用）
		{"optimization_results", "grid_json", "ALTER TABLE optimization_results ADD COLUMN grid_json TEXT DEFAULT ''"},
		// §GAP1.10 实盘账本多租户：持仓行归属账号（网关回报 user_id 写入；空串=遗留全局行，所有人可见）
		{"real_positions", "user_id", "ALTER TABLE real_positions ADD COLUMN user_id TEXT DEFAULT ''"},
		// §W2-10 委托/成交流水补租户列：回报写入时打归属账号；存量行空串=遗留全局，读侧兼容
		{"orders", "user_id", "ALTER TABLE orders ADD COLUMN user_id TEXT DEFAULT ''"},
		{"fills", "user_id", "ALTER TABLE fills ADD COLUMN user_id TEXT DEFAULT ''"},
		// §GAP 二.3#5 回测断点缓存规则指纹：改参后旧缓存自动失效
		{"backtest_event_results", "rule_fp", "ALTER TABLE backtest_event_results ADD COLUMN rule_fp TEXT DEFAULT ''"},
		// §GAP4.5 寻优排名风险调整指标：夏普/最大回撤/年化/卡玛
		{"optimization_results", "sharpe", "ALTER TABLE optimization_results ADD COLUMN sharpe REAL DEFAULT 0"},
		{"optimization_results", "max_drawdown_pct", "ALTER TABLE optimization_results ADD COLUMN max_drawdown_pct REAL DEFAULT 0"},
		{"optimization_results", "annual_return_pct", "ALTER TABLE optimization_results ADD COLUMN annual_return_pct REAL DEFAULT 0"},
		{"optimization_results", "calmar", "ALTER TABLE optimization_results ADD COLUMN calmar REAL DEFAULT 0"},
		// §Phase3 ATR 动态止损维：ATR×mult 作为动态止损距离（0 档=禁用、回退固定百分比止损）
		{"sweep_pool_configs", "atr_from", "ALTER TABLE sweep_pool_configs ADD COLUMN atr_from REAL DEFAULT 0"},
		{"sweep_pool_configs", "atr_to", "ALTER TABLE sweep_pool_configs ADD COLUMN atr_to REAL DEFAULT 0"},
		{"sweep_pool_configs", "atr_step", "ALTER TABLE sweep_pool_configs ADD COLUMN atr_step REAL DEFAULT 1"},
		// §Phase3 情绪相位分参回测：扫参排名行记录当日情绪阶段（无情绪数据为空串）
		{"optimization_results", "emotion_phase", "ALTER TABLE optimization_results ADD COLUMN emotion_phase TEXT DEFAULT ''"},
		// §2026-09-05 多轮发现/护栏分级：候选护栏档位 + 参数快照（精确复现审批时的战法）
		{"research_candidates", "guard", "ALTER TABLE research_candidates ADD COLUMN guard TEXT DEFAULT 'standard'"},
		{"research_candidates", "params", "ALTER TABLE research_candidates ADD COLUMN params TEXT DEFAULT ''"},
		// §WS-A/WS-B 实盘账本扩充：
		//  fills 手续费/印花税/交割流水号（券商交割单三方对账 + 盈亏含成本口径）
		{"fills", "fee", "ALTER TABLE fills ADD COLUMN fee REAL DEFAULT 0"},
		{"fills", "stamp_tax", "ALTER TABLE fills ADD COLUMN stamp_tax REAL DEFAULT 0"},
		{"fills", "serial", "ALTER TABLE fills ADD COLUMN serial TEXT DEFAULT ''"},
		//  real_positions.buy_date：买入交易日（T+1 可卖量判定 + 日终对账关联）
		{"real_positions", "buy_date", "ALTER TABLE real_positions ADD COLUMN buy_date TEXT DEFAULT ''"},
		// §M4 券商成交编号列（成交回报最精确的身份锚；旧库回填为空串=未知）
		{"fills", "trade_id", "ALTER TABLE fills ADD COLUMN trade_id TEXT DEFAULT ''"},
	} {
		has, err := d.hasColumn(mig.table, mig.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := d.db.Exec(mig.ddl); err != nil {
				return fmt.Errorf("store migrate add column: %w", err)
			}
		}
	}
	// 一次性迁移：backtest_jobs → research_tasks（子系统统一改造，详见
	// docs/RESEARCH_TASK_QUEUE_PLAN.md §9）。仅当队列表为空且旧表有数据时执行，
	// 幂等安全：research_tasks 一旦有行（含新写入）绝不回填。
	// English: one-shot backtest_jobs → research_tasks migration; runs only when the queue table is
	// empty and legacy rows exist, so it can never clobber live queue data.
	if err := d.migrateBacktestJobsToTasks(); err != nil {
		return fmt.Errorf("store migrate backtest_jobs→research_tasks: %w", err)
	}
	// §失败重排队：requeue_seq 单调尾键列（旧库增量迁移，幂等）。
	// English: failure-requeue tail-key column, added to pre-existing DBs idempotently.
	if ok, err := d.hasColumn("research_tasks", "requeue_seq"); err == nil && !ok {
		if _, err := d.db.Exec(`ALTER TABLE research_tasks ADD COLUMN requeue_seq INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store migrate research_tasks.requeue_seq: %w", err)
		}
	}
	// §P1-11 失败重试计数上限列（旧库增量迁移，幂等）。
	// English: P1-11 failure retry counter column, added to pre-existing DBs idempotently.
	if ok, err := d.hasColumn("research_tasks", "retry_count"); err == nil && !ok {
		if _, err := d.db.Exec(`ALTER TABLE research_tasks ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store migrate research_tasks.retry_count: %w", err)
		}
	}
	// §M14 同因连败熔断（2026-09-22）：fail_fp=最近一次失败指纹（error 前 200 字），
	// fail_streak=同指纹连败计数（换因即从 1 重计，成功/人工 Requeue 清零）。旧库增量迁移，幂等。
	// English: M14 same-cause breaker columns — last error fingerprint and its consecutive-failure
	// streak; added to pre-existing DBs idempotently.
	if ok, err := d.hasColumn("research_tasks", "fail_fp"); err == nil && !ok {
		if _, err := d.db.Exec(`ALTER TABLE research_tasks ADD COLUMN fail_fp TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("store migrate research_tasks.fail_fp: %w", err)
		}
	}
	if ok, err := d.hasColumn("research_tasks", "fail_streak"); err == nil && !ok {
		if _, err := d.db.Exec(`ALTER TABLE research_tasks ADD COLUMN fail_streak INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store migrate research_tasks.fail_streak: %w", err)
		}
	}
	// §P0-1 real_positions 主键迁移为 (ts_code, user_id)：旧库单主键重建。
	// English: P0-1 migrate real_positions primary key to (ts_code, user_id) for multi-tenant isolation.
	if err := d.migrateRealPositionsPK(); err != nil {
		return fmt.Errorf("store migrate real_positions pk: %w", err)
	}
	// §P0-2 orders.signal_id 唯一约束迁移为 (user_id, signal_id)：旧库单唯一索引重建。
	// English: P0-2 migrate orders signal_id uniqueness to (user_id, signal_id).
	if err := d.migrateOrdersSignalUnique(); err != nil {
		return fmt.Errorf("store migrate orders signal unique: %w", err)
	}
	// §ADJ-BASIS（2026-09-23）backtest_event_results 主键加复权口径位：旧库四列重建。
	// 本迁移**刻意不把 Open 变成硬失败**——守卫中止时只置降级标志 + ERROR 日志，交易路径
	// 不受影响（详见 migrateBacktestEventResultsAdjBasis 的注释）。
	// English: rebuild the event-result PK with the adjustment-basis column; an aborted rebuild
	// degrades (loud log + readers/writers fail toward recomputation) rather than failing Open.
	if err := d.migrateBacktestEventResultsAdjBasis(); err != nil {
		log.Printf("[store] ERROR §ADJ-BASIS backtest_event_results 口径位主键重建异常（本库回测缓存按保守路径处理：读恒未命中、写拒绝）: %v", err)
		d.markEventBasisDegraded("重建异常: " + err.Error())
	}
	// §M4（2026-09-22 PM 批）fills 判重键升级：成交编号优先、无编号退回复合键。
	// 旧复合唯一索引 (order_id,traded_at,price,qty) 把"同委托同秒同价同量的两笔真实部成"
	// 也判成重放——第二笔直接被唯一约束拒绝（回报 500 → 网关 outbox 无限重推 → 死信），
	// 券商侧两笔的 trade_id 本来就不同。现拆成两段部分索引：
	//   ① trade_id 非空 → 按 trade_id 唯一（券商成交编号是权威身份锚）；
	//   ② trade_id 为空（旧行/交割单回灌）→ 沿用复合键唯一。
	// 先建新索引再删旧索引：新索引建失败时旧保护仍在，不会留下无幂等保护的窗口。
	// English: §M4 — split the fills replay key: broker trade_id wins when present, the old
	// composite key only guards rows without one. Build before drop, so protection never lapses.
	if ok, err := d.hasColumn("fills", "trade_id"); err != nil {
		return err
	} else if ok {
		for _, s := range []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_trade ON fills(trade_id) WHERE trade_id <> ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_idem_notid ON fills(order_id, traded_at, price, qty) WHERE trade_id = '' OR trade_id IS NULL`,
			`DROP INDEX IF EXISTS idx_fills_idem`,
		} {
			if _, err := d.db.Exec(s); err != nil {
				return fmt.Errorf("store migrate fills trade_id index: %w\n%s", err, s)
			}
		}
	}
	return nil
}

// hasColumn 判断表是否已含某列（用于旧库增量迁移幂等）。
// （hasColumn reports whether a table already has a column, for idempotent migration.）
func (d *DB) hasColumn(table, column string) (bool, error) {
	// 通过 PRAGMA table_info 查询列定义，逐列比对列名。
	rows, err := d.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// tableHasPKColumns 返回指定表当前主键包含的列名（按 PRAGMA table_info 的 pk 顺序）。
// 表不存在或无法解析时返回空切片与 nil 错误。
// English: returns the columns that make up the current primary key of the given table.
func (d *DB) tableHasPKColumns(table string) ([]string, error) {
	rows, err := d.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// colInfo 表列信息：列名 + 是否主键。
	type colInfo struct {
		name string
		pk   int
	}
	// 遍历 PRAGMA table_info 行：记录每列的列名与主键序号（pk>0 表示复合主键成员）。
	var infos []colInfo
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		infos = append(infos, colInfo{name: name, pk: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 取主键序号最大值；无主键时返回 nil。
	maxPK := 0
	for _, info := range infos {
		if info.pk > maxPK {
			maxPK = info.pk
		}
	}
	if maxPK == 0 {
		return nil, nil
	}
	// 按主键序号 1..maxPK 依次找列名，得到复合主键列的有序列表。
	cols := make([]string, 0, maxPK)
	for i := 1; i <= maxPK; i++ {
		for _, info := range infos {
			if info.pk == i {
				cols = append(cols, info.name)
				break
			}
		}
	}
	return cols, nil
}

// indexKeyColumns 返回指定索引中 key=1 的列名（使用 PRAGMA index_xinfo，兼容 modernc.org/sqlite
// 下 index_info 的 name/cid 为空的问题）。
func (d *DB) indexKeyColumns(indexName string) ([]string, error) {
	rows, err := d.db.Query("PRAGMA index_xinfo(" + indexName + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var names []string
	// 逐行读取索引键信息；key=1 的列即索引键成员，取其 name（name 为空时跳过）。
	for rows.Next() {
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = new(any)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		// 按列名取值的辅助闭包（兼容 index_xinfo 顺序与缺失列）。
		get := func(key string) any {
			for i, c := range cols {
				if c == key {
					return *(dest[i].(*any))
				}
			}
			return nil
		}
		key, _ := get("key").(int64)
		if key != 1 {
			continue
		}
		if v, ok := get("name").(string); ok && v != "" {
			names = append(names, v)
		}
	}
	return names, rows.Err()
}

// tableHasUniqueIndex 判断表是否存在仅由给定单列组成的唯一索引/约束。
// English: reports whether the table has a unique index/constraint consisting solely of the given single column.
func (d *DB) tableHasUniqueIndex(table, column string) (bool, error) {
	idxRows, err := d.db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		return false, err
	}
	defer idxRows.Close()
	// 遍历所有索引：unique=1 且键恰好等于指定单列 → 判定命中。
	for idxRows.Next() {
		cols, err := idxRows.Columns()
		if err != nil {
			return false, err
		}
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = new(any)
		}
		if err := idxRows.Scan(dest...); err != nil {
			return false, err
		}
		// 兼容性取值：string 原样，int64 的 1 归一为 "1"。
		get := func(key string) string {
			for i, c := range cols {
				if c == key {
					switch v := (*(dest[i].(*any))).(type) {
					case string:
						return v
					case int64:
						if v == 1 {
							return "1"
						}
						return "0"
					}
				}
			}
			return ""
		}
		if get("unique") != "1" {
			continue
		}
		name := get("name")
		if name == "" {
			continue
		}
		// 取该唯一索引的键列，验证是否单列且为目标列。
		colNames, err := d.indexKeyColumns(name)
		if err != nil {
			return false, err
		}
		if len(colNames) == 1 && colNames[0] == column {
			return true, nil
		}
	}
	return false, idxRows.Err()
}

// migrateRealPositionsPK 把 real_positions 主键从单 ts_code 重建为 (ts_code, user_id)。
// 幂等：已符合目标结构时不操作。
// English: rebuilds real_positions primary key from single ts_code to (ts_code, user_id).
func (d *DB) migrateRealPositionsPK() error {
	cols, err := d.tableHasPKColumns("real_positions")
	if err != nil {
		return err
	}
	if len(cols) == 2 && cols[0] == "ts_code" && cols[1] == "user_id" {
		return nil // 已迁移
	}
	if len(cols) == 1 && cols[0] == "ts_code" {
		log.Printf("[store] migrate real_positions PK: (ts_code) -> (ts_code, user_id)")
		// 重建表：旧数据整体搬运（user_id 缺省补空串），主键升级为 (ts_code, user_id)。
		_, err := d.db.Exec(`
			CREATE TABLE real_positions_new (
				ts_code TEXT NOT NULL,
				name TEXT DEFAULT '',
				qty INTEGER NOT NULL DEFAULT 0,
				cost_price REAL NOT NULL DEFAULT 0,
				amount REAL NOT NULL DEFAULT 0,
				highest_price REAL NOT NULL DEFAULT 0,
				strategy TEXT DEFAULT '',
				signal_id TEXT DEFAULT '',
				updated_at TEXT NOT NULL,
				user_id TEXT DEFAULT '',
				PRIMARY KEY (ts_code, user_id)
			);
			INSERT OR REPLACE INTO real_positions_new
				(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			SELECT ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, COALESCE(user_id, '')
			FROM real_positions;
			DROP TABLE real_positions;
			ALTER TABLE real_positions_new RENAME TO real_positions;
		`)
		return err
	}
	return nil
}

// migrateOrdersSignalUnique 把 orders.signal_id 单唯一索引重建为 (user_id, signal_id)。
// 幂等：已符合目标结构时不操作。
// English: rebuilds orders uniqueness from single signal_id to (user_id, signal_id).
func (d *DB) migrateOrdersSignalUnique() error {
	has, err := d.tableHasUniqueIndex("orders", "signal_id")
	if err != nil {
		return err
	}
	if !has {
		// 已无单 signal_id 唯一索引，再检查是否有 (user_id, signal_id) 即可
		idxRows, err := d.db.Query("PRAGMA index_list(orders)")
		if err != nil {
			return err
		}
		defer idxRows.Close()
		// 遍历 orders 全部索引：确认 (user_id, signal_id) 唯一键已存在则迁移完成。
		for idxRows.Next() {
			idxCols, err := idxRows.Columns()
			if err != nil {
				return err
			}
			dest := make([]any, len(idxCols))
			for i := range dest {
				dest[i] = new(any)
			}
			if err := idxRows.Scan(dest...); err != nil {
				return err
			}
			// 按列名取值（仅处理 string 类型字段）。
			get := func(key string) string {
				for i, c := range idxCols {
					if c == key {
						if v, ok := (*(dest[i].(*any))).(string); ok {
							return v
						}
					}
				}
				return ""
			}
			if get("unique") != "1" {
				continue
			}
			name := get("name")
			if name == "" {
				continue
			}
			cols, err := d.indexKeyColumns(name)
			if err != nil {
				return err
			}
			if len(cols) == 2 && cols[0] == "user_id" && cols[1] == "signal_id" {
				return nil
			}
		}
		return nil
	}
	log.Printf("[store] migrate orders unique: signal_id -> (user_id, signal_id)")
	// 重建 orders 表：旧数据整体搬运，唯一键升级为 (user_id, signal_id)。
	_, err = d.db.Exec(`
		CREATE TABLE orders_new (
			order_id TEXT PRIMARY KEY,
			signal_id TEXT NOT NULL,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			status TEXT NOT NULL,
			price REAL,
			qty INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			user_id TEXT DEFAULT '',
			UNIQUE (user_id, signal_id)
		);
		INSERT OR REPLACE INTO orders_new
			(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
		SELECT order_id, signal_id, code, side, status, price, qty, created_at, COALESCE(user_id, '')
		FROM orders;
		DROP TABLE orders;
		ALTER TABLE orders_new RENAME TO orders;
	`)
	return err
}

// eventResultsPKTarget / eventResultsPKLegacy backtest_event_results 的目标 / 待迁移主键列序。
var (
	eventResultsPKTarget = []string{"candidate_id", "event_date", "industry", "adj_basis"}
	eventResultsPKLegacy = []string{"candidate_id", "event_date", "industry"}
)

// pkColumnsEqual 主键列序逐位比对（顺序即语义，ON CONFLICT 目标按列序匹配）。
func pkColumnsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !strings.EqualFold(got[i], want[i]) {
			return false
		}
	}
	return true
}

// markEventBasisDegraded 置降级标志并打 ERROR（幂等：只打第一次，重复调用只留首条日志）。
// 由 migrate() 在 Open 返回前调用，故读取侧无需加锁（发布发生在任何并发使用之前）。
// English: flags the degraded mode once with a loud ERROR log; written before Open returns.
func (d *DB) markEventBasisDegraded(reason string) {
	if d.eventBasisAborted {
		return
	}
	d.eventBasisAborted = true
	d.eventBasisReason = reason
	log.Printf("[store] ERROR §ADJ-BASIS backtest_event_results 复权口径位主键未生效（%s）："+
		"原表保持未迁移形态、不删任何行；本进程回测断点缓存转入保守模式——读恒判未命中、写直接拒绝，"+
		"整轮重算而不是把改前旧行当新结果。请人工核对该表后重启服务。", reason)
}

// EventBasisDegraded 报告本库 backtest_event_results 是否处于「口径位主键未生效」的保守模式。
// English: reports whether event-result caching is in the degraded (basis-PK not in place) mode.
func (d *DB) EventBasisDegraded() bool { return d.eventBasisAborted }

// migrateBacktestEventResultsAdjBasis 把 backtest_event_results 主键从
// (candidate_id, event_date, industry) 重建为 (candidate_id, event_date, industry, adj_basis)。
// 幂等：已符合目标结构时不操作。
//
// 为什么必须重建表而不是加列（§ADJ-BASIS，2026-09-23）：adj_basis 只有**进主键**，才能同时做到
// 「改前旧行原样留着当证据」和「同一三元组在当前口径下另起一行」。实测：只 ALTER 加列 + 建四列
// UNIQUE 索引时，旧三元组上的表级 PRIMARY KEY 仍然拦新口径行，插入报
// `UNIQUE constraint failed: backtest_event_results.candidate_id, event_date, industry (1555)`。
// SQLite 不支持改主键，因此照本仓既有重建迁移（migrateRealPositionsPK /
// migrateOrdersSignalUnique）的形态走：建 *_new → 整表搬运 → drop 旧表 → 改名回来，
// 且额外套一层事务（本迁移会 drop 表，中途失败必须能回到原样）。
//
// 行数守恒守卫（任一条不满足 → ROLLBACK 中止，旧表原封不动，只置降级标志 + ERROR 日志）：
//  1. 预检：按投影键（四列，NULL 先归一空串）去重后的行数必须等于 COUNT(*)——不相等说明搬运
//     会把多行塌成一行（旧库被手工改过 / 键列含 NULL 互相撞键），删证据不可接受；
//  2. 搬运用普通 INSERT（**不是** INSERT OR REPLACE）且新表键列 NOT NULL：任何塌行或 NULL 键
//     都直接撞约束中止；
//  3. 复检：事务内比对新旧两表——总 COUNT(*) 相等，且按**旧三元组**分组的
//     (行数, result_json 字节合计) 摘要双向 EXCEPT 为空。既证「一行不丢」也证「没一行被改」，
//     含 adj_basis 值逐行核对（旧行必须全为空串）。
//
// 旧行一律带空串 adj_basis 过表：空串就是「改前证据行」哨兵，永不参与当前口径的读取（见
// backtest_jobs.go 读写侧与 emotion_matrix.go）。
//
// English: rebuilds the event-result primary key to carry the adjustment-basis column. Adding a
// column plus a 4-column UNIQUE index is provably not enough — the table-level PRIMARY KEY on the
// old triple still rejects a new-basis row for an existing triple (SQLite error 1555). Following
// this repo's other rebuild migrations we create *_new, copy, drop and rename, wrapped in a
// transaction, and carry every pre-basis row over with an empty adj_basis (the sentinel readers
// never surface). Three count-conservation guards abort the whole thing and leave the original
// table untouched; an abort degrades caching (recompute, never reuse) instead of failing Open.
func (d *DB) migrateBacktestEventResultsAdjBasis() error {
	cols, err := d.tableHasPKColumns("backtest_event_results")
	if err != nil {
		return err
	}
	if pkColumnsEqual(cols, eventResultsPKTarget) {
		return nil // 已迁移（新建库建表语句本身就是目标形态）→ 幂等空转
	}
	if !pkColumnsEqual(cols, eventResultsPKLegacy) {
		d.markEventBasisDegraded(fmt.Sprintf("主键形态非预期 (%s)，拒绝重建", strings.Join(cols, ",")))
		return nil
	}
	// 旧库可能已被"只加列"的实验改过（列在，键不在）：有则原样带值搬，无则统一回填 ''。
	hasBasisCol, err := d.hasColumn("backtest_event_results", "adj_basis")
	if err != nil {
		return err
	}
	basisSel := "''"
	if hasBasisCol {
		basisSel = "COALESCE(adj_basis, '')"
	}
	log.Printf("[store] migrate backtest_event_results PK: (candidate_id, event_date, industry) -> " +
		"(candidate_id, event_date, industry, adj_basis)，旧行 adj_basis 回填 ''（改前证据行）")

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // commit 之后是 no-op：任何守卫失败都回到原样

	// 守卫 1（预检）：投影键去重数 == 总行数，否则搬运必塌行。
	// GROUP BY 把 NULL 归入同一组，与复制语句里 COALESCE(adj_basis,'') 的归一口径一致。
	var total, distinct int
	if err := tx.QueryRow(`SELECT
			(SELECT COUNT(*) FROM backtest_event_results),
			(SELECT COUNT(*) FROM (SELECT 1 FROM backtest_event_results
				GROUP BY candidate_id, event_date, industry, `+basisSel+`))`).Scan(&total, &distinct); err != nil {
		return fmt.Errorf("行数预检失败: %w", err)
	}
	if total != distinct {
		return fmt.Errorf("行数守恒预检不通过：COUNT(*)=%d 但投影键去重后=%d（搬运会把 %d 行塌成一行），中止迁移、旧表不动",
			total, distinct, total-distinct)
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS backtest_event_results_new`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE backtest_event_results_new (
		candidate_id INTEGER NOT NULL,
		event_date TEXT NOT NULL,
		industry TEXT NOT NULL,
		adj_basis TEXT NOT NULL DEFAULT '',
		result_json TEXT NOT NULL,
		rule_fp TEXT DEFAULT '',
		PRIMARY KEY (candidate_id, event_date, industry, adj_basis)
	)`); err != nil {
		return err
	}
	// 守卫 2：普通 INSERT + NOT NULL 键列——塌行/NULL 键在这里撞约束。
	if _, err := tx.Exec(`INSERT INTO backtest_event_results_new
			(candidate_id, event_date, industry, adj_basis, result_json, rule_fp)
		SELECT candidate_id, event_date, industry, ` + basisSel + `, result_json, COALESCE(rule_fp, '')
		FROM backtest_event_results`); err != nil {
		return fmt.Errorf("整表搬运失败（守卫：禁止塌行/NULL 键）: %w", err)
	}
	// 守卫 3（复检）：总行数相等 + 按旧三元组分组的 (行数, json 字节合计) 摘要双向一致。
	var newTotal int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM backtest_event_results_new`).Scan(&newTotal); err != nil {
		return err
	}
	if newTotal != total {
		return fmt.Errorf("行数守恒复检不通过：迁移前 %d 行，搬运后 %d 行，中止迁移、旧表不动", total, newTotal)
	}
	// 逐组摘要：行数 + result_json 字节合计（LENGTH 对 NULL 返回 NULL，故 COALESCE 成 -1 让
	// 「整组内容丢失」与「行数为 0」这两种异常都能显形）。两个方向各一次 EXCEPT，
	// 合并计数必须为 0——既证明一行不丢，也证明没一行被改。
	const groupDigest = `SELECT candidate_id, event_date, industry, COUNT(*), COALESCE(SUM(LENGTH(result_json)),-1) FROM %s GROUP BY candidate_id, event_date, industry`
	var mismatch int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM (
			SELECT * FROM (` + fmt.Sprintf(groupDigest, "backtest_event_results") + `)
			EXCEPT
			SELECT * FROM (` + fmt.Sprintf(groupDigest, "backtest_event_results_new") + `)
			UNION ALL
			SELECT * FROM (` + fmt.Sprintf(groupDigest, "backtest_event_results_new") + `)
			EXCEPT
			SELECT * FROM (` + fmt.Sprintf(groupDigest, "backtest_event_results") + `)
		)`).Scan(&mismatch); err != nil {
		return fmt.Errorf("逐组摘要比对失败: %w", err)
	}
	if mismatch != 0 {
		return fmt.Errorf("行数守恒复检不通过：%d 组 (candidate_id,event_date,industry) 的行数/内容摘要在搬运前后不一致，中止迁移、旧表不动", mismatch)
	}
	// 三道守卫全过：换表（旧行已在新表里，adj_basis=''）。
	if _, err := tx.Exec(`DROP TABLE backtest_event_results`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE backtest_event_results_new RENAME TO backtest_event_results`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[store] migrate backtest_event_results PK 完成：%d 行全部带 adj_basis='' 落新表（旧证据行不参与当前口径读取）", total)
	return nil
}

// QueryRows 执行只读查询，返回 列名→值 的行切片（TEXT 以 string 返回，其余按驱动原生类型）。
// 供增量导出（dataload export-delta）等通用读取场景；仅限 SELECT。
// English: runs a read-only query returning rows as column→value maps (TEXT as string, other types
// native). For generic reads like the delta export; SELECT only.
func (d *DB) QueryRows(query string, args ...any) ([]map[string]any, error) {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	// 逐行读取：每列用指针接收原生值，组装为列名→值 的 map。
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InsertRows 批量 INSERT OR REPLACE（单事务），用于各表的断点续传式装载。
// cols 为与 Tushare 返回字段一致的小写列名；值为 nil 的单元格写入 NULL。
// §INSERTLOCK（2026-09-22 修复批）：表名/列名直进 fmt.Sprintf 拼语句，写入前强制做
// 「裸标识符 + 真实 schema」双层校验（见 validateInsertSurface）——未知列/非法标识符
// 显式报错，杜绝导入面（delta import/dataload）被伪列名注入或写错列静默失败。
// （InsertRows bulk-upserts rows in one transaction per call, for resumable loading.
// cols are lowercase column names matching Tushare's returned fields; nil cells become NULL.）
func (d *DB) InsertRows(table string, cols []string, rows []map[string]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	// §INSERTLOCK 写入面校验：非法标识符 / 白名单外表列 / 实际 schema 缺列 → 显式报错。
	if err := d.validateInsertSurface(table, cols); err != nil {
		return 0, err
	}
	// 生成占位符与 INSERT OR REPLACE 语句（列名来自调用方，与表结构对齐）。
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ","), placeholders)
	// 单事务批量写入：失败整体回滚，保证断点续传一致性。
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(query)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	// 逐行按列取值：值为 nil 的单元格写 NULL。
	for _, r := range rows {
		args := make([]any, 0, len(cols))
		for _, c := range cols {
			if v, ok := r[c]; ok && v != nil {
				args = append(args, v)
			} else {
				args = append(args, nil)
			}
		}
		if _, err := stmt.Exec(args...); err != nil {
			return 0, fmt.Errorf("store insert %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

// TableColumns 各表写入时的列清单（与 migrate 的建表列一致）。
// （TableColumns returns each table's writable column list, aligned with the schema.）
func TableColumns(table string) []string {
	switch table {
	case "stocks":
		return []string{"ts_code", "name", "area", "industry", "market", "list_date", "delist_date"}
	case "trade_cal":
		return []string{"cal_date", "is_open"}
	case "daily", "index_daily":
		return []string{"ts_code", "trade_date", "open", "high", "low", "close", "pre_close", "change", "pct_chg", "vol", "amount"}
	case "adj_factor":
		return []string{"ts_code", "trade_date", "adj_factor"}
	case "daily_basic":
		return []string{"ts_code", "trade_date", "turnover_rate", "turnover_rate_f", "volume_ratio", "pe", "pe_ttm", "pb", "ps", "ps_ttm", "pcf_ttm", "dv_ratio", "dv_ttm", "total_share", "float_share", "free_share", "total_mv", "circ_mv", "is_st"}
	case "stk_limit":
		return []string{"ts_code", "trade_date", "up_limit", "down_limit"}
	case "fina_indicator":
		return []string{"ts_code", "end_date", "ann_date", "eps", "roe", "roe_waa", "roa", "roe_dt", "grossprofit_margin", "netprofit_margin", "debt_to_assets", "yoy_or", "yoy_net_profit", "or_yoy", "netprofit_yoy"}
	case "income":
		return []string{"ts_code", "end_date", "n_income_attr_p", "revenue", "total_revenue"}
	case "cashflow":
		return []string{"ts_code", "end_date", "n_cashflow_act", "n_cashflow_inv_act", "n_cashflow_fnc_act"}
	case "sector_history":
		return []string{"trade_date", "industry", "limitup_cnt", "change_pct", "member_count", "top_stocks"}
	}
	return nil
}

// isBareInsertIdent §INSERTLOCK 第一层防线：仅接受裸 SQL 标识符（字母/下划线开头，
// 其后字母/数字/下划线，长度 ≤ 64）——引号/反引号/空白/括号/逗号/分号/连字符一律拒绝。
// 表名列名以 fmt.Sprintf 直进语句文本，本校验把「列名位注入」（如 "a) , (SELECT ..."）
// 挡在拼语句之前。
func isBareInsertIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validateInsertSurface §INSERTLOCK 写入面校验（InsertRows 前置闸）：
//  1. 表名与每个列名必须是裸标识符（isBareInsertIdent，防语句拼接注入）；
//  2. 表在 TableColumns 白名单内 → 列必须全部 ∈ 白名单（大小写不敏感，SQLite 列名不区分），
//     未知列显式报错；
//  3. 表不在白名单（migrations 扩展表/研究临时表，如 ths_break_pool_daily）→ 回退运行时
//     PRAGMA table_info 按实际 schema 校验：表不存在（schema 为空）或列缺失都显式报错。
//
// 拒绝而非静默容忍的取向：拼错列名旧行为依赖 SQLite 报错（信息含原始语句）或干脆写歪数据，
// 本闸在本地给出精确缺列信息，防导入脚本带错列继续跑。
// English: pre-flight write-surface validation — bare identifiers only, subset of the
// TableColumns whitelist when the table is known, runtime PRAGMA schema check otherwise;
// unknown columns error explicitly.
func (d *DB) validateInsertSurface(table string, cols []string) error {
	if !isBareInsertIdent(table) {
		return fmt.Errorf("store insert %q: 非法表名（仅允许裸标识符，禁止引号/空白/括号等）", table)
	}
	if len(cols) == 0 {
		return fmt.Errorf("store insert %s: 列清单为空", table)
	}
	for _, c := range cols {
		if !isBareInsertIdent(c) {
			return fmt.Errorf("store insert %s: 非法列名 %q（仅允许裸标识符）", table, c)
		}
	}
	allow := make(map[string]bool)
	if known := TableColumns(table); known != nil {
		for _, c := range known {
			allow[strings.ToLower(c)] = true
		}
	} else {
		// 白名单外表：以数据库实际 schema 为准（表不存在时 allow 为空 → 显式拒绝）。
		rows, err := d.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return fmt.Errorf("store insert %s: 查询表结构失败: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("store insert %s: 读取表结构失败: %w", table, err)
			}
			allow[strings.ToLower(name)] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store insert %s: 读取表结构失败: %w", table, err)
		}
		if len(allow) == 0 {
			return fmt.Errorf("store insert %s: 表不存在或无列结构（写入面白名单拒绝未知表）", table)
		}
	}
	for _, c := range cols {
		if !allow[strings.ToLower(c)] {
			return fmt.Errorf("store insert %s: 未知列 %q（不在该表写入白名单/实际结构中）", table, c)
		}
	}
	return nil
}

// MaxTradeDate 返回某张行情表（daily/daily_basic/...）中指定股票的最近交易日，无数据返回空串。
// 用于逐票断点续传：从 max+1 日开始拉取。
// （MaxTradeDate returns a stock's latest loaded trade date in a bar table ("" if none),
// enabling per-stock resume from the next trading day.）
func (d *DB) MaxTradeDate(table, tsCode string) (string, error) {
	// trade_date 为 YYYYMMDD 字符串，字典序即时间序
	query := fmt.Sprintf("SELECT MAX(trade_date) FROM %s WHERE ts_code=?", table)
	var v sql.NullString
	if err := d.db.QueryRow(query, tsCode).Scan(&v); err != nil {
		return "", err
	}
	return v.String, nil
}

// MaxTradeDateAll 返回某行情表全局最近交易日（全部股票），无数据返回空串。
// （MaxTradeDateAll returns the latest trade date across all stocks in a bar table.）
func (d *DB) MaxTradeDateAll(table string) (string, error) {
	query := fmt.Sprintf("SELECT MAX(trade_date) FROM %s", table)
	var v sql.NullString
	if err := d.db.QueryRow(query).Scan(&v); err != nil {
		return "", err
	}
	return v.String, nil
}

// MaxEndDate 返回财务类表（fina_indicator/income/cashflow）中某股票的最新报告期。
// （MaxEndDate returns a stock's latest report end_date in a financial table.）
func (d *DB) MaxEndDate(table, tsCode string) (string, error) {
	query := fmt.Sprintf("SELECT MAX(end_date) FROM %s WHERE ts_code=?", table)
	var v sql.NullString
	if err := d.db.QueryRow(query, tsCode).Scan(&v); err != nil {
		return "", err
	}
	return v.String, nil
}

// Count 返回表的行数（可选按日期过滤）。
// （Count returns a table's row count, optionally filtered by a date column lower bound.）
func (d *DB) Count(table string, fromDate string) (int, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	var args []any
	// 财务类用 end_date、行情类用 trade_date 作为通用"日期列"（按表内嵌的判断）
	if fromDate != "" {
		if table == "fina_indicator" || table == "income" || table == "cashflow" {
			query += " WHERE end_date >= ?"
		} else {
			query += " WHERE trade_date >= ?"
		}
		args = append(args, fromDate)
	}
	var n int
	if err := d.db.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// TradeDates 返回 [from,to]（含）内全部交易日（升序）。
// （TradeDates returns the ascending trade dates within [from,to] inclusive.）
func (d *DB) TradeDates(from, to string) ([]string, error) {
	// 查询开市日历（is_open=1）并升序返回。
	rows, err := d.db.Query("SELECT cal_date FROM trade_cal WHERE is_open=1 AND cal_date>=? AND cal_date<=? ORDER BY cal_date", from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StockCodes 返回全部股票代码（含已退市）。
// （StockCodes returns all stock codes, delisted included.）
func (d *DB) StockCodes() ([]string, error) {
	// 全量代码清单，按代码升序。
	rows, err := d.db.Query("SELECT ts_code FROM stocks ORDER BY ts_code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UniverseAt §WS-D D-2 时点股票池（消除幸存者偏差）：返回截至 date 已上市且未退市的股票
// （list_date ≤ date < delist_date；delist_date 空 = 仍未退市）。date 格式 YYYYMMDD。
// English: §WS-D D-2 point-in-time universe (survivorship-bias-free): stocks listed on or before date
// and not yet delisted (list_date ≤ date < delist_date; empty delist_date = still listed). Date format
// is YYYYMMDD.
func (d *DB) UniverseAt(date string) ([]string, error) {
	rows, err := d.db.Query(`SELECT ts_code FROM stocks
		WHERE list_date != '' AND list_date <= ?
		  AND (delist_date = '' OR delist_date > ?)
		ORDER BY ts_code`, date, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ReadyStockCount 返回近一年（约 244 个交易日）内有日线数据的股票数。
// 作为"研究池就绪"的代理：B3/B5 因子装配与盘口扫描依赖近一年有行情的股票。
// （ReadyStockCount returns how many stocks have daily bars within the last year
// (~244 trading days), the proxy for "research-ready": B3/B5 factor assembly and
// depth scanning depend on stocks with recent daily data.）
func (d *DB) ReadyStockCount() (int, error) {
	cutoff := time.Now().AddDate(-1, 0, 0).Format("20060102")
	var n int
	err := d.db.QueryRow(`SELECT COUNT(DISTINCT ts_code) FROM daily WHERE trade_date >= ?`, cutoff).Scan(&n)
	return n, err
}

// HfqBars 读取某股票 hfq 后复权日线（升序）。
// 数据源路由开关（PrimarySourceThsDaily / ThsFactorsReady）已收为包内私有，
// 唯一写入口见 source_routing.go 的 ConfigureSource / ConfigureSourceFromFile；
// 本文件只经 useThsDaily()/useThsHfq() 读取。
// 注意：门禁未过时 hfq 仍走旧表——ths 复权因子尚在草稿态（对账门禁未过，见
// docs/HITHINK_DATA_SOURCE_PLAN.md §6.3）。
//
// 换算：hfq_close = close * adj_factor（基座因子在收益率/动量等比例型因子里自然抵消；
// 价格类因子如 MA/52周高距在同一基准下自洽，不影响相对结论）。
// （HfqBars reads a stock's hfq back-adjusted daily bars (ascending). hfq_close = close * adj_factor;
// the base factor cancels in ratio-based factors and stays self-consistent for price factors.）
func (d *DB) HfqBars(tsCode, start, end string) ([]Bar, error) {
	// §数据源路由：主源=hithink 且复权门禁通过 → ths 双表 join；
	// 否则走旧表（baostock）——因子口径未定稿前绝不混用两套复权体系。
	if useThsHfq() {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM ths_adj_factor WHERE ts_code=?`,
			tsCode).Scan(&n); err == nil && n > 0 {
			return d.thsHfqBars(tsCode, start, end)
		}
	}
	// 主源路径：日线 × 复权因子（缺因子按 1 兜底），后复权口径计算。
	//
	// §ADJ(P0-A 20260922)：adj_factor 是【事件稀疏点】表——写入口 cmd/dataload/baostock.go
	// （bsLoadStockTables 的 adjRows 构造处、bsLoadAdjFactor 专项补齐处）存的 trade_date =
	// normDate(dividoperatedate)（分红实施日），一只票一年通常只有 0~3 行，而不是每个交易日一行。
	// 因此因子必须**前向填充**：取"不晚于该交易日的最近一个事件日"的因子
	// （与同包 ths_tables.go 的 LegacyAdjFactorAt 语义严格一致）。
	// 若写成等值 JOIN（因子日 == 行情日），非除权日全部落空 → COALESCE 兜成 1 →
	// **后复权价退化为不复权价**，回测/因子研究/图表复权口径全链路失真——这正是本处缺陷。
	// 允许例外：allow-legacy-adj-join-eq —— 本函数为前向填充的合法实现点，注释中出现的
	// "因子日 == 行情日" 仅为反面说明，SQL 内不含等值 JOIN 形态。
	query := `SELECT d.trade_date,
		COALESCE(d.open,0), COALESCE(d.high,0), COALESCE(d.low,0), COALESCE(d.close,0),
		COALESCE(d.vol,0), COALESCE(d.amount,0),
		COALESCE((SELECT a.adj_factor FROM adj_factor a
		          WHERE a.ts_code=d.ts_code AND a.trade_date<=d.trade_date
		          ORDER BY a.trade_date DESC LIMIT 1), 1) AS adj
		FROM daily d
		WHERE d.ts_code=? AND d.trade_date>=? AND d.trade_date<=?
		ORDER BY d.trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	// 逐行按复权因子换算：hfc_close = close × adj_factor（量能不换算）。
	for rows.Next() {
		var b Bar
		var adj float64
		if err := rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount, &adj); err != nil {
			return nil, err
		}
		// hfq 换算：价格类按复权因子等比缩放；量能不需复权
		b.Open *= adj
		b.High *= adj
		b.Low *= adj
		b.Close *= adj
		out = append(out, b)
	}
	return out, rows.Err()
}

// RawBars 读取某股票未复权日线（升序），供回测按真实成交价撮合。
// （RawBars reads a stock's unadjusted daily bars for realistic backtest fills.）
func (d *DB) RawBars(tsCode, start, end string) ([]Bar, error) {
	// §数据源路由：主源=同花顺（新）且该股有 ths 数据 → 读 ths_daily；
	// 无数据回退旧 daily 表（缺口登记重试队列的 provenance 机制随 Phase E 补齐）。
	if useThsDaily() {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM ths_daily WHERE ts_code=? AND trade_date<=?`,
			tsCode, end).Scan(&n); err == nil && n > 0 {
			return d.thsRawBars(tsCode, start, end)
		}
	}
	// 旧表路径：直读 daily 未复权行情（供回测真实成交价撮合）。
	query := `SELECT trade_date,
		COALESCE(open,0), COALESCE(high,0), COALESCE(low,0), COALESCE(close,0),
		COALESCE(vol,0), COALESCE(amount,0)
		FROM daily WHERE ts_code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	for rows.Next() {
		var b Bar
		if err := rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// thsRawBars 读同花顺（新）日K（升序），形状与 RawBars 一致。
func (d *DB) thsRawBars(tsCode, start, end string) ([]Bar, error) {
	// 直读 ths_daily 未复权行情（升序）。
	query := `SELECT trade_date,
		COALESCE(open,0), COALESCE(high,0), COALESCE(low,0), COALESCE(close,0),
		COALESCE(vol,0), COALESCE(amount,0)
		FROM ths_daily WHERE ts_code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	for rows.Next() {
		var b Bar
		if err := rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// thsHfqBars 读同花顺（新）日K×因子的后复权序列（hfq_close = close × factor）。
//
// §ADJ 两源同核对（P0-A 20260922 施工核实，以代码为准不看文档）：
// ths_adj_factor 的**唯一写入口** cmd/dataload/hithink_sync.go 的 cmdHithinkSyncAdjFactors：
//   - 先 `db.ThsDatesSince(code, since)` 取该标的窗口内**全部交易日**；
//   - 再对每个交易日 `batch = append(batch, ThsAdjFactorRow{TsCode, TradeDate: dt, Factor: cur})`
//     逐日物化**累计**因子（cur 只在跨过除权事件时乘一次乘数，之后原样续写到下一个事件日）。
//
// 即 ths 侧是【日粒度全覆盖的累计值】，与 baostock 侧的【事件稀疏点】形态根本不同 ⇒
// 这里的等值 JOIN 语义**正确**，不需要前向填充（若强行改成子查询反而掩盖"因子未按日物化"
// 的数据缺陷）。允许例外：allow-legacy-adj-join-eq —— 等值 JOIN 作用于 ths_adj_factor
// （日累计全覆盖表），不是事件稀疏的 adj_factor。
// 已知覆盖缺口（门禁放行前须补，本次登记不修）：hithink_sync 只为"窗口内有事件"的标的展开
// （无事件即 continue，与同文件"无事件也物化恒等基线行"的注释不符），且只覆盖 since 之后
// 的日期 —— 门禁切换后，跨 since 之前的区间会被这条内连接丢行。
func (d *DB) thsHfqBars(tsCode, start, end string) ([]Bar, error) {
	// ths_daily JOIN ths_adj_factor：价格类在 SQL 层直接乘因子做后复权（量能不换算）。
	query := `SELECT b.trade_date,
		COALESCE(b.open,0)*f.factor, COALESCE(b.high,0)*f.factor,
		COALESCE(b.low,0)*f.factor, COALESCE(b.close,0)*f.factor,
		COALESCE(b.vol,0), COALESCE(b.amount,0)
		FROM ths_daily b JOIN ths_adj_factor f ON f.ts_code=b.ts_code AND f.trade_date=b.trade_date
		WHERE b.ts_code=? AND b.trade_date>=? AND b.trade_date<=? ORDER BY b.trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	for rows.Next() {
		var b Bar
		if err := rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DailyBasicRange 读取某股票一段区间的每日指标（升序），供估值/流动性类因子。
// （DailyBasicRange reads a stock's per-day indicators over a range for valuation/liquidity factors.）
func (d *DB) DailyBasicRange(tsCode, start, end string) ([]DailyBasic, error) {
	// 查询区间内每日估值/流动性指标（缺失字段按 0 兜底）。
	query := `SELECT trade_date,
		COALESCE(turnover_rate,0), COALESCE(volume_ratio,0), COALESCE(pe_ttm,0),
		COALESCE(pb,0), COALESCE(ps_ttm,0), COALESCE(pcf_ttm,0), COALESCE(dv_ttm,0),
		COALESCE(total_share,0), COALESCE(total_mv,0), COALESCE(circ_mv,0), COALESCE(is_st,0)
		FROM daily_basic WHERE ts_code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyBasic
	for rows.Next() {
		var b DailyBasic
		if err := rows.Scan(&b.Date, &b.TurnoverRate, &b.VolumeRatio, &b.PETTM, &b.PB, &b.PSTTM,
			&b.PcfTTM, &b.DVTTM, &b.TotalShare, &b.TotalMV, &b.CircMV, &b.IsST); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// IncomeHistory 读取某股票全部利润表快照（按报告期升序），供 SUE 等单季净利因子。
// （IncomeHistory reads a stock's income-statement snapshots for single-quarter factors.）
func (d *DB) IncomeHistory(tsCode string) ([]IncomeRow, error) {
	query := `SELECT end_date, COALESCE(n_income_attr_p,0), COALESCE(revenue,0)
		FROM income WHERE ts_code=? ORDER BY end_date`
	rows, err := d.db.Query(query, tsCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IncomeRow
	for rows.Next() {
		var r IncomeRow
		if err := rows.Scan(&r.EndDate, &r.NIncomeAttrP, &r.Revenue); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinaHistory 读取某股票全部财务指标快照（按报告期升序），供成长/质量因子。
// 含 ann_date（公告日），供回测做点对时（point-in-time）过滤、避免未来函数。
// §N-5（2026-09-22 PM 批）：ann_date 在旧装载行里可以是 NULL（baostock 某些期不给披露日），
// 而 Scan 进 string 会直接报错 → 整次查询失败 → 上游 fina_cache 把 err 吞掉返回 nil，
// 表现是"这只票没有财务因子"（七项指标静默全 0），实际只是披露日缺失。现 COALESCE 成空串，
// 缺失=不可知（下游新鲜度判定据此处理），不再连带丢掉整行数据。
// English: §N-5 — ann_date may be NULL on legacy rows; scanning NULL into a string failed the whole
// query, and the caller swallowed the error, so the stock silently lost all seven financial factors.
// COALESCE to "" now: unknown announcement date, but the row still counts.
func (d *DB) FinaHistory(tsCode string) ([]FinaRow, error) {
	query := `SELECT end_date, COALESCE(ann_date,''),
		COALESCE(eps,0), COALESCE(roe,0), COALESCE(roa,0), COALESCE(grossprofit_margin,0),
		COALESCE(netprofit_margin,0), COALESCE(debt_to_assets,0), COALESCE(yoy_or,0),
		COALESCE(yoy_net_profit,0)
		FROM fina_indicator WHERE ts_code=? ORDER BY end_date`
	rows, err := d.db.Query(query, tsCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FinaRow
	for rows.Next() {
		var f FinaRow
		if err := rows.Scan(&f.EndDate, &f.AnnDate, &f.EPS, &f.ROE, &f.ROA, &f.GrossMargin,
			&f.NetMargin, &f.DebtToAssets, &f.YoyOR, &f.YoyNetProfit); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// IndexBars 读取指数日线（如沪深300，升序），供超额收益基准。
// （IndexBars reads index daily bars (e.g. CSI300) as the excess-return benchmark.）
func (d *DB) IndexBars(tsCode, start, end string) ([]Bar, error) {
	query := `SELECT trade_date,
		COALESCE(open,0), COALESCE(high,0), COALESCE(low,0), COALESCE(close,0),
		COALESCE(vol,0), COALESCE(amount,0)
		FROM index_daily WHERE ts_code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	for rows.Next() {
		var b Bar
		if err := rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LimitRange 读取某股票一段区间的涨跌停价（升序），供回测 T+1 与涨跌停约束。
// （LimitRange reads a stock's limit-up/down prices over a range for backtest constraints.）
func (d *DB) LimitRange(tsCode, start, end string) ([]LimitRow, error) {
	query := `SELECT trade_date, COALESCE(up_limit,0), COALESCE(down_limit,0) FROM stk_limit
		WHERE ts_code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LimitRow
	for rows.Next() {
		var l LimitRow
		if err := rows.Scan(&l.Date, &l.Up, &l.Down); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Bar 日线（未复权原始价 + 成交量/额），研究读取的基本单元。
// （Bar is one daily bar with raw prices, the basic read unit for research.）
type Bar struct {
	Date   string  // 交易日 YYYYMMDD
	Open   float64 // 开盘
	High   float64 // 最高
	Low    float64 // 最低
	Close  float64 // 收盘
	Vol    float64 // 成交量（手）
	Amount float64 // 成交额（元）
}

// DailyBasic 每日指标行。
// （DailyBasic is one row of per-day market indicators.）
type DailyBasic struct {
	Date         string  // 日期
	TurnoverRate float64 // 换手率(%)
	VolumeRatio  float64 // 量比
	PETTM        float64 // 市盈率 TTM
	PB           float64 // 市净率
	PSTTM        float64 // 市销率 TTM
	PcfTTM       float64 // 市现率 TTM
	DVTTM        float64 // 股息率 TTM(%)
	TotalShare   float64 // 总股本(股)
	TotalMV      float64 // 总市值(万元)
	CircMV       float64 // 流通市值(万元)
	IsST         int     // 是否 ST（1=是）
}

// IncomeRow 利润表快照行（SUE 单季净利因子来源）。
// （IncomeRow is one income-statement snapshot for single-quarter factors.）
type IncomeRow struct {
	EndDate      string  // 报告期 YYYYMMDD
	NIncomeAttrP float64 // 归母净利润（累计值）
	Revenue      float64 // 营业收入（累计值）
}

// FinaRow 财务指标快照行（质量/成长因子来源）。
// （FinaRow is one financial-indicator snapshot for quality/growth factors.）
type FinaRow struct {
	EndDate      string  // 报告期 YYYYMMDD
	AnnDate      string  // 公告日 YYYYMMDD（点对时过滤用）
	EPS          float64 // 每股收益
	ROE          float64 // 净资产收益率
	ROA          float64 // 总资产收益率
	GrossMargin  float64 // 毛利率(%)
	NetMargin    float64 // 净利率(%)
	DebtToAssets float64 // 资产负债率(%)
	YoyOR        float64 // 营收同比增长(%)
	YoyNetProfit float64 // 净利同比增长(%)
}

// LimitRow 涨跌停价行。
// （LimitRow is one row of limit-up/down prices.）
type LimitRow struct {
	Date string  // 日期
	Up   float64 // 涨停价
	Down float64 // 跌停价
}

// DebugCount 输出各表行数（dataload verify 用）。
// （DebugCount logs row counts per table for dataload verify.）
func (d *DB) DebugCount() {
	for _, t := range []string{"stocks", "trade_cal", "daily", "adj_factor", "daily_basic", "stk_limit", "index_daily", "fina_indicator", "income", "cashflow"} {
		n, err := d.Count(t, "")
		if err != nil {
			log.Printf("[store] %s count err: %v", t, err)
			continue
		}
		log.Printf("[store] %s: %d 行", t, n)
	}
}

// Checkpoint §W4-c WAL 例行收口：TRUNCATE 模式把 -wal 文件清零并回主库。
// 夜间链/批量装载结束后调用一次，防 -wal 长期膨胀挤占小盘 VPS 磁盘；
// 失败仅记日志不中断调用方。English: truncates the WAL after heavy batch writes.
func (d *DB) Checkpoint() error {
	var ign, logN, ckpt int
	if err := d.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&ign, &logN, &ckpt); err != nil {
		return err
	}
	log.Printf("[store] wal_checkpoint(TRUNCATE): frames=%d→%d", logN, ckpt)
	return nil
}
