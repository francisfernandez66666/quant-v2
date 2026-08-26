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
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（driver 名 "sqlite"）
)

// DB 历史数据存储句柄。
// （DB wraps the research database handle.）
type DB struct {
	db *sql.DB
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
			reason TEXT
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
		// 回测断点缓存：按候选 + 事件唯一键（事件日+行业）存完整 EventResult JSON，
		// 中断/重启后续跑只重算未缓存的事件；同一候选重跑覆盖（INSERT OR REPLACE 语义）。
		// English: backtest checkpoint cache — full EventResult JSON per (candidate, event-date, industry);
		// a resumed run only recomputes uncached events; reruns overwrite (INSERT OR REPLACE semantics).
		`CREATE TABLE IF NOT EXISTS backtest_event_results (
			candidate_id INTEGER NOT NULL,
			event_date TEXT NOT NULL,
			industry TEXT NOT NULL,
			result_json TEXT NOT NULL,
			PRIMARY KEY (candidate_id, event_date, industry)
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
			ts_code TEXT PRIMARY KEY,
			name TEXT DEFAULT '',
			qty INTEGER NOT NULL DEFAULT 0,
			cost_price REAL NOT NULL DEFAULT 0,
			amount REAL NOT NULL DEFAULT 0,
			highest_price REAL NOT NULL DEFAULT 0,
			strategy TEXT DEFAULT '',
			signal_id TEXT DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// 实盘委托单：order_id 为网关返回的单号，signal_id 唯一（幂等，防重复下单）。
		// English: real order tickets — order_id from the gateway, signal_id unique (idempotency key).
		`CREATE TABLE IF NOT EXISTS orders (
			order_id TEXT PRIMARY KEY,
			signal_id TEXT UNIQUE,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			status TEXT NOT NULL,
			price REAL,
			qty INTEGER NOT NULL,
			created_at TEXT NOT NULL
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
			user_id TEXT DEFAULT ''
		)`,
		// §W3-b 成交回报幂等唯一键：同一委托+同一回报时间戳+同价同量只入账一次，
		// 根除 outbox 重试遇响应丢失时的双倍记账（首尔侧此前零幂等）。
		// ⚠️ 建唯一索引前必须先去重历史行——生产 fills 已有 outbox 重试造成的重复记录，
		// 直接建索引会因冲突失败导致迁移中断、服务起不来。保留每组最早一条（MIN(rowid)）。
		`DELETE FROM fills WHERE rowid NOT IN (
			SELECT MIN(rowid) FROM fills GROUP BY order_id, traded_at, price, qty)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_idem ON fills(order_id, traded_at, price, qty)`,
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
	return nil
}

// hasColumn 判断表是否已含某列（用于旧库增量迁移幂等）。
// （hasColumn reports whether a table already has a column, for idempotent migration.）
func (d *DB) hasColumn(table, column string) (bool, error) {
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
// （InsertRows bulk-upserts rows in one transaction per call, for resumable loading.
// cols are lowercase column names matching Tushare's returned fields; nil cells become NULL.）
func (d *DB) InsertRows(table string, cols []string, rows []map[string]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ","), placeholders)
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
// PrimarySourceThsDaily 数据源路由开关：true 时 RawBars 优先读 ths_daily（同花顺（新）），
// 该股无数据回退旧 daily 表。由 config data.primary_source 在启动时装配。
// 注意：HfqBars 不受此开关影响——ths 复权因子尚在草稿态（对账门禁未过，见
// docs/HITHINK_DATA_SOURCE_PLAN.md §6.3），hfq 仍走旧表直到门禁放行。
var PrimarySourceThsDaily = false

// ThsFactorsReady 同花顺复权因子对账门禁：true 时 HfqBars 走 ths_daily×ths_adj_factor；
// false（默认）时 hfq 仍走旧表——门禁未过前禁止消费（docs/HITHINK_DATA_SOURCE_PLAN §6.3）。
var ThsFactorsReady = false

// 换算：hfq_close = close * adj_factor（基座因子在收益率/动量等比例型因子里自然抵消；
// 价格类因子如 MA/52周高距在同一基准下自洽，不影响相对结论）。
// （HfqBars reads a stock's hfq back-adjusted daily bars (ascending). hfq_close = close * adj_factor;
// the base factor cancels in ratio-based factors and stays self-consistent for price factors.）
func (d *DB) HfqBars(tsCode, start, end string) ([]Bar, error) {
	// §数据源路由：主源=hithink 且复权门禁通过 → ths 双表 join；
	// 否则走旧表（baostock）——因子口径未定稿前绝不混用两套复权体系。
	if PrimarySourceThsDaily && ThsFactorsReady {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM ths_adj_factor WHERE ts_code=?`,
			tsCode).Scan(&n); err == nil && n > 0 {
			return d.thsHfqBars(tsCode, start, end)
		}
	}
	query := `SELECT d.trade_date,
		COALESCE(d.open,0), COALESCE(d.high,0), COALESCE(d.low,0), COALESCE(d.close,0),
		COALESCE(d.vol,0), COALESCE(d.amount,0), COALESCE(a.adj_factor,1) AS adj
		FROM daily d LEFT JOIN adj_factor a ON a.ts_code=d.ts_code AND a.trade_date=d.trade_date
		WHERE d.ts_code=? AND d.trade_date>=? AND d.trade_date<=?
		ORDER BY d.trade_date`
	rows, err := d.db.Query(query, tsCode, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
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
	if PrimarySourceThsDaily {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM ths_daily WHERE ts_code=? AND trade_date<=?`,
			tsCode, end).Scan(&n); err == nil && n > 0 {
			return d.thsRawBars(tsCode, start, end)
		}
	}
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
func (d *DB) thsHfqBars(tsCode, start, end string) ([]Bar, error) {
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
// （FinaHistory reads a stock's financial-indicator snapshots for quality/growth factors.
// ann_date (announcement date) enables point-in-time filtering to avoid lookahead bias.）
func (d *DB) FinaHistory(tsCode string) ([]FinaRow, error) {
	query := `SELECT end_date, ann_date,
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
	Date         string
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
	Date string
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
