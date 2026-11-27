# 2026-11-27 P3（PLAN_FACTOR_FIX_P0_20261126 §4.2）pregen 侧行为锁。
#
# 靶子：四条"算得出来但从来没人烘"的腿，逐条给出真实 SQLite + 真实 bake_bakeables：
#   ① fin_pit 的 ann_date 前视闸（D3：ann_date 空 ⇒ end_date+120，不丢行）
#   ② fin_series（最近 6 个已披露报告期的 roe，最旧在前）
#   ③ total_share_series 改用流通股本（daily_basic.total_share 现网全 NULL）
#   ④ stom_21 / cmra（盒内拿不到的换手累计与趋势跨度）
#   ⑤ 资金流双源（个股 json 新鲜优先，否则板块净流入率代理 + flow_resid）
#   ⑥ sector_map 超龄整表弃权（不拿旧成分冒充今天）
#
# 夹具纪律：临时库 + 临时 BUILD_DIR/FLOW_DIR 一律 addCleanup(shutil.rmtree) 回收，
# 绝不写 /var/lib/quant-trading-v2（旧夹具漏回收曾把测试库留在现网路径上）。

import importlib.util
import json
import math
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
SYMBOL = "600001"
TS_CODE = "600001.SH"

SCHEMA = """
CREATE TABLE daily_basic (
    ts_code TEXT, trade_date TEXT, pe_ttm REAL, pb REAL, ps_ttm REAL,
    pcf_ttm REAL, dv_ttm REAL, turnover_rate REAL, total_share REAL, circ_mv REAL);
CREATE TABLE fina_indicator (
    ts_code TEXT, end_date TEXT, ann_date TEXT, eps REAL, roe REAL, roe_waa REAL,
    roa REAL, grossprofit_margin REAL, netprofit_margin REAL,
    debt_to_assets REAL, yoy_or REAL);
CREATE TABLE daily (ts_code TEXT, trade_date TEXT, close REAL);
CREATE TABLE sector_flow_daily (
    sector TEXT, trade_date TEXT, net_inflow REAL, amount REAL);
CREATE TABLE d1_scores (
    code TEXT, score REAL, date TEXT, level TEXT, created_at TEXT);
CREATE TABLE stk_limit (ts_code TEXT, trade_date TEXT, up_limit REAL);
CREATE TABLE ths_limit_up_daily (
    ts_code TEXT, trade_date TEXT, continue_cnt INTEGER,
    first_seal_time TEXT, max_seal_money REAL);
"""


def _load_pregen():
    sys.path.insert(0, GATEWAY)  # pregen 间接 import report_uplink 等
    spec = importlib.util.spec_from_file_location(
        "pregen_new_series_under_test",
        os.path.join(SCRIPTS, "pregen_carrier_factors_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class PregenNewSeries20261127(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="pregen_p3_")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.db = os.path.join(self.tmp, "trading.db")
        conn = sqlite3.connect(self.db)
        conn.executescript(SCHEMA)
        conn.commit()
        conn.close()
        self.flow_dir = os.path.join(self.tmp, "moneyflow")
        os.makedirs(self.flow_dir, exist_ok=True)
        os.makedirs(os.path.join(self.tmp, "qmt_gateway"), exist_ok=True)
        m = self.mod
        self._orig = (m.LOCAL_DB, m.FLOW_DIR, m.BUILD_DIR)
        m.LOCAL_DB = self.db
        m.FLOW_DIR = self.flow_dir
        m.BUILD_DIR = self.tmp
        self.addCleanup(self._restore)

    def _restore(self):
        m = self.mod
        m.LOCAL_DB, m.FLOW_DIR, m.BUILD_DIR = self._orig

    # ---- 夹具小工具 -------------------------------------------------------
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

    @staticmethod
    def _days(count, start="20250101"):
        base = datetime.strptime(start, "%Y%m%d")
        return [(base + timedelta(days=i)).strftime("%Y%m%d") for i in range(count)]

    def _basic_row(self, day, turnover=1.0, total_share=None, circ_mv=None):
        return (TS_CODE, day, 10.0, 1.2, 2.0, 30.0, 2.5, turnover, total_share, circ_mv)

    def _fin_row(self, end_date, ann_date, roe, eps=0.5):
        return (TS_CODE, end_date, ann_date, eps, roe, roe, 6.0, 30.0, 8.0, 40.0, 5.0)

    # ---- ① ann_date 前视闸 ------------------------------------------------
    def test_ann_fallback_keeps_undisclosed_out(self):
        # 20260930 + 120 天 = 20270128 > 20261127 ⇒ 尚未披露，不许算进来；
        # 20260630 + 120 天 = 20261028 ≤ 20261127 ⇒ 已披露，采纳。
        self._exec("INSERT INTO fina_indicator VALUES (?,?,?,?,?,?,?,?,?,?,?)", [
            self._fin_row("20260930", None, 9.0),          # 未披露（回退日仍在未来）
            self._fin_row("20260630", None, 8.0),          # 回退日已过 ⇒ 最新可采纳
            self._fin_row("20260331", "20260428", 7.0),    # 显式公告日
        ])
        out = self._bake(day=BAKE_DAY)
        self.assertEqual(out["fin_pit"]["roe"], 8.0,
                         "未披露的报告期必须被前视闸挡住（拿未来数据打分是前视偏差）")
        self.assertEqual(out["fin_series"], [7.0, 8.0],
                         "fin_series 最旧在前，且只含已披露报告期")

    def test_ann_date_with_dashes_is_normalized(self):
        # tushare 两种日期形态混排（20261031 / 2026-10-31），切片前必须先归一。
        self._exec("INSERT INTO fina_indicator VALUES (?,?,?,?,?,?,?,?,?,?,?)", [
            self._fin_row("2026-09-30", "2026-10-31", 6.5),
        ])
        out = self._bake(day=BAKE_DAY)
        self.assertEqual(out["fin_pit"]["roe"], 6.5)

    # ---- ② total_share_series 用流通股本 -----------------------------------
    def test_total_share_series_falls_back_to_float_shares(self):
        self._exec("INSERT INTO daily_basic VALUES (?,?,?,?,?,?,?,?,?,?)",
                   [self._basic_row("20261126", total_share=None)])
        out = self._bake(day=BAKE_DAY, float_shares=123456.0)
        self.assertEqual(out["total_share_series"], [123456.0] * self.mod.BAKE_SERIES_N,
                         "total_share 全 NULL 时必须用流通股本烘同值序列（单元素会被判样本不足）")

    def test_total_share_series_prefers_real_column_when_present(self):
        self._exec("INSERT INTO daily_basic VALUES (?,?,?,?,?,?,?,?,?,?)", [
            self._basic_row("20261126", total_share=999.0),
            self._basic_row("20261125", total_share=998.0),
        ])
        out = self._bake(day=BAKE_DAY, float_shares=123456.0)
        self.assertEqual(out["total_share_series"], [999.0, 998.0],
                         "真列有值时以真列为准（流通股本是回退腿，不许反过来盖掉真值）")

    # ---- ③ stom_21 / cmra --------------------------------------------------
    def test_stom_21_and_cmra(self):
        days21 = self._days(21, "20251001")
        rows = [self._basic_row(day) for day in days21]
        self._exec("INSERT INTO daily_basic VALUES (?,?,?,?,?,?,?,?,?,?)", rows)
        # 253 根收盘，降序入库（与 pregen 的 ORDER BY trade_date DESC 一致）
        closes = [10.0 + 0.1 * i for i in range(253)]
        days253 = self._days(253, "20250101")
        daily_rows = [(TS_CODE, day, value)
                      for day, value in zip(reversed(days253), closes)]
        self._exec("INSERT INTO daily VALUES (?,?,?)", daily_rows)
        out = self._bake(day=BAKE_DAY)
        expected_stom = round(21 * math.log(1.01), 6)
        self.assertAlmostEqual(out["stom_21"], expected_stom, places=6)
        base = closes[-1]  # 最旧一根 = 累计收益的基准
        acc, cum = 0.0, []
        for value in reversed(closes):
            acc += math.log(value / base)
            cum.append(acc)
        self.assertAlmostEqual(out["cmra"], round(max(cum) - min(cum), 6), places=6)

    def test_cmra_needs_full_window(self):
        days = self._days(252, "20250101")
        self._exec("INSERT INTO daily VALUES (?,?,?)",
                   [(TS_CODE, day, 10.0 + i) for i, day in enumerate(days)])
        out = self._bake(day=BAKE_DAY)
        self.assertNotIn("cmra", out, "不足 253 根就不烘（缺读数 ≠ 0 读数）")

    # ---- ④ 资金流双源 ------------------------------------------------------
    def _write_flow(self, last_date, mains):
        doc = {"days": [{"Date": last_date, "MainNet": value} for value in mains]}
        with open(os.path.join(self.flow_dir, SYMBOL + ".json"), "w", encoding="utf-8") as fh:
            json.dump(doc, fh)

    def test_stock_flow_wins_when_fresh(self):
        self._exec("INSERT INTO daily_basic VALUES (?,?,?,?,?,?,?,?,?,?)",
                   [self._basic_row("20261126", total_share=None, circ_mv=None)])
        self._exec("INSERT INTO daily VALUES (?,?,?)", [(TS_CODE, "20261126", 10.0)])
        self._write_flow("2026-11-26", [1e7, 2e7, 3e7, 4e7, 5e7])
        out = self._bake(day=BAKE_DAY, float_shares=1e8)
        # cap = 流通股本 1e8 × 收盘 10 = 1e9；末根 5e7 ⇒ 5%；五日 1.5e8 ⇒ 15%
        self.assertEqual(out["flow_src"], "stock")
        self.assertAlmostEqual(out["flow_today_pct"], 5.0, places=4)
        self.assertAlmostEqual(out["flow_5d_pct"], 15.0, places=4)
        self.assertEqual(out["flow_src_day"], "20261126")

    def test_sector_proxy_when_stock_flow_is_stale(self):
        self._exec("INSERT INTO daily VALUES (?,?,?)", [(TS_CODE, "20261126", 10.0)])
        self._write_flow("2026-11-10", [1e7])  # 超过 FLOW_FRESH_DAYS ⇒ 不新鲜
        market = {"sector_map": {SYMBOL: "BK0100"},
                  "sector_flow": {"boards": {"BK0100": {
                      "pct": 1.25, "pct5": 3.5, "z": -1.8, "day": "20261126"}}}}
        out = self._bake(day=BAKE_DAY, float_shares=1e8, market=market)
        self.assertEqual(out["flow_src"], "sector")
        self.assertAlmostEqual(out["flow_today_pct"], 1.25, places=6)
        self.assertAlmostEqual(out["flow_5d_pct"], 3.5, places=6)
        self.assertAlmostEqual(out["flow_resid"], -1.8, places=6)

    def test_no_flow_source_bakes_nothing(self):
        # 两源都缺 ⇒ 键不烘。绝不用 0 顶班（0 是"资金中性"，与"没读数"两回事）。
        self._exec("INSERT INTO daily VALUES (?,?,?)", [(TS_CODE, "20261126", 10.0)])
        out = self._bake(day=BAKE_DAY, float_shares=1e8,
                         market={"sector_map": {}, "sector_flow": {"boards": {}}})
        self.assertNotIn("flow_today_pct", out)
        self.assertNotIn("flow_5d_pct", out)
        self.assertNotIn("flow_resid", out)

    # ---- ⑤ sector_map 超龄弃权 --------------------------------------------
    def _write_sector_map(self, day):
        doc = {"day": day, "sector_map": {SYMBOL: "BK0100"}}
        path = os.path.join(self.tmp, "qmt_gateway", "local_executor_sector_map.json")
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh)

    def test_sector_map_fresh_ok_stale_discarded(self):
        today = datetime.now().astimezone()
        self._write_sector_map((today - timedelta(days=1)).strftime("%Y%m%d"))
        mapping, day = self.mod.load_sector_map()
        self.assertEqual(mapping.get(SYMBOL), "BK0100")
        self.assertEqual(len(day), 8)
        self._write_sector_map((today - timedelta(days=30)).strftime("%Y%m%d"))
        mapping, day = self.mod.load_sector_map()
        self.assertEqual(mapping, {}, "超龄的板块表必须整表弃权，不拿旧成分冒充今天")
        self.assertIsNone(day)

    def test_sector_flow_residual_shape(self):
        days = self._days(25, "20261001")
        rows = []
        for i, day in enumerate(days):
            net = 1e8 * (1.0 + 0.1 * (i % 3))
            rows.append(("BK0100", day, net, 1e10))
        self._exec("INSERT INTO sector_flow_daily VALUES (?,?,?,?)", rows)
        boards = self.mod.fetch_sector_flow_map(BAKE_DAY)
        self.assertIn("BK0100", boards)
        entry = boards["BK0100"]
        self.assertIn("pct", entry)
        self.assertIn("z", entry, "样本 ≥ 8 时必须给残差 z（flow_resid 靠它）")
        self.assertEqual(entry["day"], days[-1])


if __name__ == "__main__":
    unittest.main()
