# test_pregen_stock_flow_20261128.py — §REPLAY-INPUTS 2026-11-28：载体烘参的资金流双分母锁。
#
# 靶子是 owner 裁决 Q3 的落地形态：**一个键两种量纲**必须消失。
# 现状（2026-11-28 实测，方案 §0.5 探针 3）：pregen 的个股腿写的是
# `MainNet/(float_shares×close)×100`（占流通市值%），而板块兜底腿直接取
# `entry["pct"]`（占成交额%）——两条写进**同一个键** `flow_today_pct`、吃同一条 band。
# 后果与 §CONCEPT-HEAT-LINEAR 同形：band 标在错尺度上不会报错，
# 只会稳定地把高分票判成低分（反之亦然），而读数上"这条腿在工作"。
#
# 本文件钉四件事：
#   ① 库路径命中 ⇒ flow_today_pct = 上游 f57（占成交额），flow_cap_* = 原公式，两套各写各的；
#   ② 5 日档要求**连续交易日**（daily 序列），中间断一天 ⇒ 5 日键整条不写（不是拿 4 行凑）；
#   ③ 只有旧 json 缓存时 ⇒ 只写 cap 侧，且**不置 stock_flow_done**，板块兜底仍可补占成交额侧；
#   ④ 烘参表（local_executor_params.json）的 factor_defs 必须与 FACTOR_LIBRARY 同步登记
#      （§FACTOR-FIX 的命门：源码加了、烘参没加 ⇒ spec=None ⇒ 整条规则静默 input_absent），
#      而 scalar_bands 刻意**没有**这两条 —— 没标定就该弃权，不猜尺度。
#
# 全程真 SQLite + 真 bake_bakeables（不 mock 数据库：mock 会把"SQL 形状写错"这一类
# 最常见的缺陷直接掩盖掉，本仓 §P0-③ 那批就是靠真库抓出来的）。
import importlib.util
import json
import os
import shutil
import sqlite3
import sys
import tempfile
import unittest
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
SCRIPTS = os.path.join(REPO, "scripts")

BAKE_DAY = "20261127"
SYMBOL = "600004"
TS_CODE = "600004.SH"

SCHEMA = """
CREATE TABLE daily (ts_code TEXT, trade_date TEXT, close REAL);
CREATE TABLE daily_basic (
    ts_code TEXT, trade_date TEXT, pe_ttm REAL, pb REAL, ps_ttm REAL,
    pcf_ttm REAL, dv_ttm REAL, turnover_rate REAL, total_share REAL, circ_mv REAL);
CREATE TABLE fina_indicator (
    ts_code TEXT, end_date TEXT, ann_date TEXT, eps REAL, roe REAL, roe_waa REAL,
    roa REAL, grossprofit_margin REAL, netprofit_margin REAL,
    debt_to_assets REAL, yoy_or REAL);
CREATE TABLE sector_flow_daily (sector TEXT, trade_date TEXT, net_inflow REAL, amount REAL);
CREATE TABLE d1_scores (code TEXT, score REAL, date TEXT, level TEXT, created_at TEXT);
CREATE TABLE stk_limit (ts_code TEXT, trade_date TEXT, up_limit REAL);
CREATE TABLE ths_limit_up_daily (
    ts_code TEXT, trade_date TEXT, continue_cnt INTEGER,
    first_seal_time TEXT, max_seal_money REAL);
CREATE TABLE stock_moneyflow_daily (
    ts_code TEXT, trade_date TEXT, main_net REAL, main_pct REAL, amount REAL,
    small_net REAL, medium_net REAL, large_net REAL, super_large_net REAL,
    source TEXT, fetched_at TEXT, PRIMARY KEY (ts_code, trade_date)) WITHOUT ROWID;
"""


def _load_pregen():
    sys.path.insert(0, GATEWAY)
    spec = importlib.util.spec_from_file_location(
        "pregen_flow_under_test",
        os.path.join(SCRIPTS, "pregen_carrier_factors_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _days_back(count, end=BAKE_DAY):
    base = datetime.strptime(end, "%Y%m%d")
    return [(base - timedelta(days=count - 1 - i)).strftime("%Y%m%d") for i in range(count)]


class PregenStockFlow20261128(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="pregen_flow_")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.db = os.path.join(self.tmp, "trading.db")
        conn = sqlite3.connect(self.db)
        conn.executescript(SCHEMA)
        conn.commit()
        conn.close()
        self.flow_dir = os.path.join(self.tmp, "moneyflow")
        os.makedirs(self.flow_dir)
        os.makedirs(os.path.join(self.tmp, "qmt_gateway"))
        m = self.mod
        self._orig = (m.LOCAL_DB, m.FLOW_DIR, m.BUILD_DIR)
        m.LOCAL_DB = self.db
        m.FLOW_DIR = self.flow_dir
        m.BUILD_DIR = self.tmp
        self.addCleanup(self._restore)

    def _restore(self):
        m = self.mod
        m.LOCAL_DB, m.FLOW_DIR, m.BUILD_DIR = self._orig

    def _exec(self, sql, rows=()):
        conn = sqlite3.connect(self.db)
        if rows:
            conn.executemany(sql, rows)
        else:
            conn.execute(sql)
        conn.commit()
        conn.close()

    def _bake(self, **kwargs):
        return self.mod.bake_bakeables(
            SYMBOL, {"hot_pool": {"bullish": {}}},
            kwargs.pop("lu_boards", {}), kwargs.pop("market", {}), **kwargs)

    def _seed_daily(self, days):
        self._exec("INSERT INTO daily VALUES (?,?,?)",
                   [(TS_CODE, d, 12.0) for d in days])

    def _seed_flow(self, rows):
        # rows: (trade_date, main_net, main_pct, amount)
        # 显式列名（不写 VALUES(...) 的位置式）：列序错一位会把 amount 写进 small_net，
        # 表现是"5 日档算不出来"而夹具本身看不出问题。
        self._exec("INSERT INTO stock_moneyflow_daily "
                   "(ts_code, trade_date, main_net, main_pct, amount, source, fetched_at) "
                   "VALUES (?,?,?,?,?,?,?)",
                   [(TS_CODE, d, net, pct, amt, "eastmoney_fflow_relay_seed", "2026-11-27 15:40:00")
                    for d, net, pct, amt in rows])

    # ---- ① 库路径：双分母各写各的 ---------------------------------------
    def test_db_path_writes_both_denominators(self):
        days = _days_back(5)  # 新→旧：days[4] 是 BAKE_DAY
        self._seed_daily(days)
        self._seed_flow([(d, 1000.0 * (i + 1), 2.0 + i, 50000.0) for i, d in enumerate(days)])
        out = self._bake(float_shares=1e9, day=BAKE_DAY)
        # 占成交额 = 上游 f57 原值（最后一天 pct=6.0）
        self.assertEqual(out["flow_today_pct"], 6.0)
        # 5 日累计：Σ净额 / Σ成交额 = (1000+..+5000)/(5*50000) = 15000/250000 = 6%
        self.assertAlmostEqual(out["flow_5d_pct"], 6.0, places=6)
        # 占流通市值 = 净额 / (股本×收盘) ×100 = 5000/(1e9*12)*100 = 4.1667e-5
        # 期望值按同一精度（8 位）算，否则比的是"烘参的取整方式"而不是算术本身
        self.assertAlmostEqual(out["flow_cap_today_pct"], round(5000.0 / 12e9 * 100.0, 8), places=10)
        self.assertAlmostEqual(out["flow_cap_5d_pct"], round(15000.0 / 12e9 * 100.0, 8), places=10)
        # 精度锁：cap 量纲在 4 位小数下会被压成 0.0（旧写法的真缺陷，见 pregen 写点注释）
        self.assertNotEqual(out["flow_cap_today_pct"], 0.0,
                            "占流通市值档必须留到 8 位精度，否则烘出来恒为 0、band 读成常数")
        self.assertEqual(out["flow_src"], "stock_db")
        self.assertEqual(out["flow_src_day"], days[4])
        # 两键绝不能同值（同值＝其中一条被另一条顶班，正是本批要消灭的形态）
        self.assertNotEqual(out["flow_today_pct"], out["flow_cap_today_pct"])

    # ---- ② 断一日 ⇒ 5 日档整条不写（不许拿 4 行凑）-----------------------
    def test_five_day_window_requires_continuous_sessions(self):
        days = _days_back(6)
        self._seed_daily(days)  # 行情有 6 个交易日
        # 资金流缺第 5 新那天（days[4]），只到 days[3] 及更早
        rows = [(d, 1000.0, 2.0, 50000.0) for d in days[:4]]
        self._seed_flow(rows)
        out = self._bake(float_shares=1e9, day=BAKE_DAY)
        self.assertNotIn("flow_5d_pct", out)
        self.assertNotIn("flow_cap_5d_pct", out)

    def test_single_day_still_writes_today_only(self):
        # 只有当日一行（更早的还没采）⇒ 当日键可写、5 日键必须缺席
        days = _days_back(5)
        self._seed_daily(days)
        self._seed_flow([(days[4], 900.0, 1.5, 60000.0)])
        out = self._bake(float_shares=1e9, day=BAKE_DAY)
        self.assertEqual(out["flow_today_pct"], 1.5)
        self.assertNotIn("flow_5d_pct", out)

    # ---- 主键/日尺：库里是紧凑 YYYYMMDD，日线也是紧凑；写错形状＝永远查不到 ----
    def test_dashed_dates_in_flow_table_are_not_matched(self):
        days = _days_back(5)
        self._seed_daily(days)
        self._seed_flow([(f"{d[:4]}-{d[4:6]}-{d[6:]}", 1000.0, 2.0, 50000.0) for d in days])
        out = self._bake(float_shares=1e9, day=BAKE_DAY)
        # 库里混进横线日期 ⇒ 库路径查不中（这是**期望行为**：一种表只留一种形状）。
        # 若这里能读到值，说明 pregen 在两种日尺之间来回猜——那正是本仓栽过三次的形状。
        self.assertNotEqual(out.get("flow_src"), "stock_db")

    # ---- ③ 只有旧 json 缓存时：只写 cap 侧，且不锁死板块兜底 --------------
    def test_json_fallback_writes_cap_side_only(self):
        days = _days_back(5)
        self._seed_daily(days)
        doc = {
            "saved_at": "2026-11-27T15:00:00",
            "days": [
                {"Date": "2026-11-23", "MainNet": 100.0, "SmallNet": 0, "MediumNet": 0,
                 "LargeNet": 0, "SuperLargeNet": 0},
                {"Date": "2026-11-24", "MainNet": 200.0, "SmallNet": 0, "MediumNet": 0,
                 "LargeNet": 0, "SuperLargeNet": 0},
            ],
        }
        with open(os.path.join(self.flow_dir, SYMBOL + ".json"), "w", encoding="utf-8") as fh:
            json.dump(doc, fh)
        out = self._bake(float_shares=1e9, day=BAKE_DAY)
        self.assertIn("flow_cap_today_pct", out)      # 原公式照旧可算
        self.assertNotIn("flow_today_pct", out)        # 占成交额侧 json 没有 ⇒ 绝不冒充
        self.assertEqual(out["flow_src"], "stock_json_cap_only")
        # 板块兜底仍可补 flow_today_pct（同一量纲，正是拆键后该发生的事）
        market = {
            "sector_map": {SYMBOL: "芯片"},
            "sector_flow": {"boards": {"芯片": {"pct": 3.3, "pct5": 2.2, "day": "20261127", "z": 0.4}}},
        }
        out2 = self._bake(float_shares=1e9, market=market, day=BAKE_DAY)
        self.assertEqual(out2["flow_today_pct"], 3.3)
        self.assertEqual(out2["flow_5d_pct"], 2.2)
        self.assertEqual(out2["flow_src"], "sector")
        # cap 侧没被板块腿覆盖（板块腿根本不该写 cap 键）
        self.assertIn("flow_cap_today_pct", out2)
        self.assertNotEqual(out2["flow_cap_today_pct"], 3.3)

    def test_db_hit_blocks_sector_fallback_from_overwriting(self):
        # 库里有数 ⇒ 板块兜底不许再把 flow_today_pct 换成板块值（两本账互相覆盖的老形状）
        days = _days_back(5)
        self._seed_daily(days)
        self._seed_flow([(d, 1000.0, 6.6, 50000.0) for d in days])
        market = {
            "sector_map": {SYMBOL: "芯片"},
            "sector_flow": {"boards": {"芯片": {"pct": 1.1, "pct5": 1.2, "day": "20261127", "z": 0.1}}},
        }
        out = self._bake(float_shares=1e9, market=market, day=BAKE_DAY)
        self.assertEqual(out["flow_today_pct"], 6.6)
        self.assertEqual(out["flow_src"], "stock_db")

    # ---- ④ 两处同写：烘参表必须与源码 FACTOR_LIBRARY 同步登记 -------------
    def test_baked_params_register_both_new_ids(self):
        sys.path.insert(0, GATEWAY)
        import rules_factor as rf
        with open(os.path.join(GATEWAY, "local_executor_params.json"), encoding="utf-8") as fh:
            baked = (json.load(fh).get("params") or {}).get("factor_defs") or {}
        for fid in ("FlowCapToday", "FlowCap5d"):
            self.assertIn(fid, rf.FACTOR_LIBRARY, "源码 FACTOR_LIBRARY 缺 %s" % fid)
            self.assertIn(fid, baked, "烘参 factor_defs 缺 %s（derive 走 kept=file，源码加了烘参不加"
                                      "就静默 input_absent —— 这是 §FACTOR-FIX 抓过的同一颗雷）" % fid)
            self.assertEqual(baked[fid]["input"], rf.FACTOR_LIBRARY[fid]["input"], "%s input 漂移" % fid)
            self.assertEqual(baked[fid]["kind"], rf.FACTOR_LIBRARY[fid]["kind"], "%s kind 漂移" % fid)
        # 新列必须在 CTX_KEYS 里（它是从 FACTOR_LIBRARY 自动推的，漏装配=整条腿永不参与）
        self.assertIn("flow_cap_today_pct", rf.CTX_KEYS)
        self.assertIn("flow_cap_5d_pct", rf.CTX_KEYS)

    def test_no_band_registered_for_uncalibrated_keys(self):
        """未标定的两键**故意没有 band**：无带即弃权，不许猜一个尺度打分。"""
        sys.path.insert(0, GATEWAY)
        with open(os.path.join(GATEWAY, "local_executor_params.json"), encoding="utf-8") as fh:
            bands = (json.load(fh).get("params") or {}).get("scalar_bands") or {}
        for fid in ("FlowCapToday", "FlowCap5d"):
            self.assertNotIn(fid, bands, "%s 已写进 band 表——owner 未签字的尺度不该进代码" % fid)

    def test_unbanded_factor_declines_not_zero(self):
        """无带 ⇒ 规则 declined（不是给 0 分参与复合）。"""
        sys.path.insert(0, GATEWAY)
        import rules_factor as rf
        rule = {"id": "fac_cap_test", "name": "T", "factors": ["FlowCapToday"],
                "weights": {"FlowCapToday": 1.0}, "directions": {"FlowCapToday": 1},
                "buy_threshold": 70.0}
        params = dict(rf.PARAMS_TEMPLATE)
        params["factor_defs"] = {"FlowCapToday": {"kind": "scalar_proxy", "windows": [],
                                                  "input": "flow_cap_today_pct", "direct": True}}
        params["scalar_bands"] = {}
        # 参数顺序是 (params, rule, ctx)——写反了会以 no_factors/no_params 之类的
        # 形状失败，看起来像"功能没实现"，实际是用例自己的姿势错。
        res = rf.score_factor_rule(params, rule, {"flow_cap_today_pct": 9.9})
        self.assertTrue(res.get("declined"), "无带的标量必须弃权，得 %s" % res)

    def test_band_added_later_scores_linear(self):
        """owner 签字后补上 linear band ⇒ 同一条腿立刻按尺度打分（标定路径本身要可用）。"""
        sys.path.insert(0, GATEWAY)
        import rules_factor as rf
        rule = {"id": "fac_cap_test", "name": "T", "factors": ["FlowCapToday"],
                "weights": {"FlowCapToday": 1.0}, "directions": {"FlowCapToday": 1},
                "buy_threshold": 70.0}
        params = dict(rf.PARAMS_TEMPLATE)
        params["factor_defs"] = {"FlowCapToday": {"kind": "scalar_proxy", "windows": [],
                                                  "input": "flow_cap_today_pct", "direct": True}}
        params["scalar_bands"] = {"FlowCapToday": {"min": 0.0, "max": 4.0, "mode": "linear"}}
        res = rf.score_factor_rule(params, rule, {"flow_cap_today_pct": 2.0})
        self.assertFalse(res.get("declined"), res)
        # 2.0 在 0..4 的线性中点 = 0.5 ⇒ 复合分 (0.5+1)/2*100 = 75
        # （与 Go 侧 internal/strategies/factor/factor_scalar_test.go 的同一组手算期望对齐）
        self.assertAlmostEqual(res["score"], 75.0, places=6)


if __name__ == "__main__":
    unittest.main()
