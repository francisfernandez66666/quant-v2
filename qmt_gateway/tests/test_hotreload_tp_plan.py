# test_hotreload_tp_plan.py — §TP-LADDER 云端烘参链的行为锁（2026-11-24）。
#
# 载体没有磁盘（券商禁令）、每次投递都把内存清空、柜台 POSITION 行又不带入场战法与峰值，
# 所以"这只票的止盈梯子走到第几档"只能由云端每轮从 live.db 的成交重放出来：
#
#   开仓日/入场战法 = 净头寸从 0 变正的那一笔买单的 `buy:<码>:<战法>:<日>`；
#   Stage 1        = 成交里出现过 `sell:<码>:tp:<日>`（qmt.go 卖出补链写的）；
#   峰值           = max(real_positions.highest_price, 开仓日至今 daily.high)。
#
# 本文件锁四件事：
#   1. 重放口径：部分卖出不重置开仓日，清仓后的下一笔买入才是新一轮（Stage 回 0）；
#   2. 战法 → 止盈点：龙头 30（账上显式值优先）、双凸 15、龙回头 25；没有止盈点的战法
#      必须留 None（走峰值回撤档），**绝不**硬凑一个数；
#   3. 弃权方向：库读不到 → 空表（载体整条阶梯失效但不乱动仓），没有可归属买单 → manual；
#   4. 指纹只认静态三元组：峰值每天在变，把它算进 sha 会让每个整点都投递一次
#      ——而一次投递＝盒子客户端一次有序重启。
#
# English: behaviour locks for the cloud-side take-profit plan bake (replay, points,
# fail-closed direction, and a digest that ignores the daily-moving peak).
import importlib.util
import json
import os
import shutil
import sqlite3
import tempfile
import unittest
from datetime import datetime

HR_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "scripts",
                       "carrier_hot_reload_gz.py")
spec = importlib.util.spec_from_file_location("hotreload_tp_plan_test", HR_PATH)
hr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hr)

NOW = datetime.now().astimezone()
TODAY_DASH = NOW.strftime("%Y-%m-%d")
TODAY = NOW.strftime("%Y%m%d")
OPEN_DASH = "2026-11-20"

DRAGON = "600519"
DOUBLE_BUMP = "002415"
FAC = "000797"
MANUAL = "000680"
CLOSED = "000009"


def _live_db(path):
    """One live.db with just the three tables bake_tp_plan reads."""
    conn = sqlite3.connect(path)
    # D2（2026-11-25）起 bake_tp_plan 还要读 price 当止盈 pnl 的分母，夹具必须同形，
    # 否则整条 SELECT 抛 "no such column" 被 fail-closed 吞成空计划（假红）。
    conn.execute("CREATE TABLE fills (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT,"
                 " code TEXT, side TEXT, qty INTEGER, traded_at TEXT, signal_id TEXT,"
                 " price REAL)")
    conn.execute("CREATE TABLE real_positions (user_id TEXT, ts_code TEXT, qty INTEGER,"
                 " highest_price REAL)")
    conn.execute("CREATE TABLE daily (ts_code TEXT, trade_date TEXT, high REAL)")
    rows = [
        # 龙头：11-20 建仓，今天卖掉一半（TP）⇒ Stage 1。
        ("buy:%s:dragon:%s" % (DRAGON, OPEN_DASH), DRAGON, "买入", 1000, OPEN_DASH + " 09:35:00", 12.0),
        ("sell:%s:tp:%s" % (DRAGON, TODAY_DASH), DRAGON, "卖出", 500, TODAY_DASH + " 10:12:00", 13.0),
        # 双凸：今天新建仓，还没卖过。
        ("buy:%s:double_bump:%s" % (DOUBLE_BUMP, TODAY_DASH), DOUBLE_BUMP, "买入", 600,
         TODAY_DASH + " 09:41:00", 21.0),
        # 因子综合（没有战法止盈点）。
        ("buy:%s:fac_160:%s" % (FAC, OPEN_DASH), FAC, "买入", 1100, OPEN_DASH + " 10:02:00", 9.0),
        # 已清仓：买入 + 全部卖出，不得出现在计划里。
        ("buy:%s:fac_160:%s" % (CLOSED, OPEN_DASH), CLOSED, "买入", 800, OPEN_DASH + " 09:50:00", 8.0),
        ("sell:%s:tt:%s" % (CLOSED, TODAY_DASH), CLOSED, "卖出", 800, TODAY_DASH + " 09:31:00", 8.5),
    ]
    for signal_id, code, side, qty, at, price in rows:
        conn.execute("INSERT INTO fills (user_id, code, side, qty, traded_at, signal_id, price)"
                     " VALUES (?,?,?,?,?,?,?)", ("u_owner", code, side, qty, at, signal_id, price))
    for code, qty, high in ((DRAGON, 500, 12.5), (DOUBLE_BUMP, 600, 21.0),
                            (FAC, 1100, 9.2), (MANUAL, 2100, 8.8)):
        conn.execute("INSERT INTO real_positions VALUES (?,?,?,?)", ("u_owner", code, qty, high))
    # daily.trade_date 是**紧凑**形态（20261124）；flow 表才是带横线的。查错形状＝
    # 峰值永远只剩券商那条腿，而它只跟成交价 ⇒ 阶梯的"跟峰值"退化成"跟成交价"。
    conn.execute("INSERT INTO daily VALUES (?,?,?)", (DRAGON + ".SH", OPEN_DASH.replace("-", ""), 11.0))
    conn.execute("INSERT INTO daily VALUES (?,?,?)", (DRAGON + ".SH", TODAY, 13.4))
    conn.execute("INSERT INTO daily VALUES (?,?,?)", (DOUBLE_BUMP + ".SZ", TODAY, 22.2))
    conn.commit()
    conn.close()


class TestBakeTPPlan(unittest.TestCase):
    """烘参主腿：重放口径 / 战法止盈点 / 弃权方向 / 指纹只认静态三元组。"""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        os.makedirs(os.path.join(self.dir, "qmt_gateway"))
        self.params_path = os.path.join(self.dir, "qmt_gateway",
                                        "local_executor_params.json")
        with open(self.params_path, "w", encoding="utf-8") as fh:
            json.dump({"params": {"tp_dragon_take_profit_pct": 30,
                                  "tp_double_bump_take_profit_pct": 15,
                                  "tp_dragon_return_take_profit_pct": 25}}, fh)
        self.db = os.path.join(self.dir, "live.db")
        _live_db(self.db)
        self.keep = (hr.BUILD_DIR, hr.LIVE_DB, hr.HOTRELOAD_DB, hr.OWNER_UID)
        hr.BUILD_DIR = self.dir
        hr.LIVE_DB = self.db
        hr.HOTRELOAD_DB = self.db
        hr.OWNER_UID = "u_owner"

    def tearDown(self):
        (hr.BUILD_DIR, hr.LIVE_DB, hr.HOTRELOAD_DB, hr.OWNER_UID) = self.keep

    def _plan(self, qc=None):
        plan = hr.bake_tp_plan(qc if qc is not None else {})
        self.assertEqual(list(plan.keys()), [TODAY],
                         "the plan is day-keyed (stale days must not leak into the box)")
        return plan[TODAY]

    # ---- ① 重放口径 -------------------------------------------------------
    def test_partial_sale_keeps_the_open_day_and_moves_the_stage(self):
        per = self._plan()
        row = per[DRAGON]
        self.assertEqual(row["open_date"], OPEN_DASH)
        self.assertEqual(row["strategy"], "dragon")
        self.assertEqual(row["stage"], 1, "a TP fill means the half was already sold")
        self.assertFalse(row["manual"])

    def test_closed_position_is_not_in_the_plan(self):
        per = self._plan()
        self.assertNotIn(CLOSED, per)

    def test_peak_is_the_max_of_broker_reading_and_daily_high(self):
        per = self._plan()
        self.assertAlmostEqual(per[DRAGON]["peak"], 13.4,
                               msg="today's daily high is above the broker's highest_price")
        self.assertAlmostEqual(per[DOUBLE_BUMP]["peak"], 22.2)
        self.assertAlmostEqual(per[FAC]["peak"], 9.2)

    # ---- ② 战法 → 止盈点 --------------------------------------------------
    def test_strategy_points(self):
        per = self._plan()
        self.assertEqual(per[DRAGON]["tp_pct"], 30)
        self.assertEqual(per[DOUBLE_BUMP]["tp_pct"], 15)
        self.assertIsNone(per[FAC]["tp_pct"],
                          "fac_* has no strategy take-profit point -> peak-retrace ladder")
        self.assertEqual(per[DOUBLE_BUMP]["stage"], 0, "a fresh position starts at stage 0")

    def test_explicit_account_value_wins(self):
        qc = {"rules": {"strategy": {"dragon": {"take_profit_pct": 22}}}}
        self.assertEqual(self._plan(qc)[DRAGON]["tp_pct"], 22)

    def test_dragon_return_fraction_is_converted_once(self):
        qc = {"rules": {"strategy": {"dragon_return": {"take_profit_pct": 0.25}}}}
        self.assertEqual(hr._tp_explicit_pct(qc, "dragon_return"), 25.0)

    def test_missing_knob_abstains_instead_of_guessing(self):
        with open(self.params_path, "w", encoding="utf-8") as fh:
            json.dump({"params": {}}, fh)
        per = self._plan()
        self.assertIsNone(per[DRAGON]["tp_pct"],
                          "no baked point -> the ladder must not invent one")

    # ---- ③ 弃权方向 -------------------------------------------------------
    def test_position_without_attributable_buy_is_manual(self):
        per = self._plan()
        self.assertTrue(per[MANUAL]["manual"], "no buy fill -> manual, the legs leave it alone")
        self.assertIsNone(per[MANUAL]["tp_pct"])
        self.assertEqual(per[MANUAL]["stage"], 0)

    def test_unreadable_ledger_returns_empty_plan(self):
        hr.LIVE_DB = os.path.join(self.dir, "missing.db")
        self.assertEqual(hr.bake_tp_plan({}), {},
                         "fail-closed: no ledger -> no plan -> the carrier ladder abstains")
        self.assertEqual(hr.tp_plan_digest({}), "")

    # ---- ④ 指纹只认静态三元组 ---------------------------------------------
    def test_plan_is_a_runtime_param(self):
        self.assertIn("tp_plan", hr.RUNTIME_PARAM_KEYS)

    def test_digest_ignores_the_peak(self):
        per = self._plan()
        first = hr.tp_plan_digest({TODAY: per})
        moved = {TODAY: {code: dict(row) for code, row in per.items()}}
        moved[TODAY][DRAGON]["peak"] = 99.9
        self.assertEqual(hr.tp_plan_digest(moved), first,
                         "a peak-only change must not re-bake (a re-bake restarts the client)")

    def test_digest_tracks_stage_and_presence(self):
        per = self._plan()
        first = hr.tp_plan_digest({TODAY: per})
        staged = {TODAY: {code: dict(row) for code, row in per.items()}}
        staged[TODAY][DOUBLE_BUMP]["stage"] = 1
        self.assertNotEqual(hr.tp_plan_digest(staged), first)
        without = {TODAY: {code: row for code, row in per.items() if code != DRAGON}}
        self.assertNotEqual(hr.tp_plan_digest(without), first)

    def test_digest_is_stable_across_key_order(self):
        per = self._plan()
        reordered = {TODAY: {code: per[code] for code in reversed(list(per))}}
        self.assertEqual(hr.tp_plan_digest(reordered), hr.tp_plan_digest({TODAY: per}))


class TestRetiredParamKeys(unittest.TestCase):
    """`take_profit_ratio` 必须真的从盒子上消失（不是"没人读"）。"""

    def _doc(self, params):
        return {"strategy_version": "v168-20261124", "params": dict(params)}

    def test_retired_key_in_runtime_is_dropped_not_kept(self):
        base = self._doc({"stale_order_sec": 600})
        runtime = self._doc({"stale_order_sec": 600, "take_profit_ratio": 1.05})
        merged, patched = hr.merge_static_params(runtime, base)
        self.assertNotIn("take_profit_ratio", merged["params"],
                         "a retired knob must not survive the merge")
        self.assertNotIn("take_profit_ratio", [k for k, _old, _new in patched])

    def test_retired_key_in_base_is_dropped_too(self):
        base = self._doc({"take_profit_ratio": 1.05, "stale_order_sec": 600})
        merged, _ = hr.merge_static_params(self._doc({}), base)
        self.assertNotIn("take_profit_ratio", merged["params"])

    def test_retirement_does_not_widen_into_a_wildcard(self):
        """只有点名退役的键被删；base 不认识的新运行态键照旧保留（11-12 池子教训）。"""
        base = self._doc({"stale_order_sec": 600})
        runtime = self._doc({"brand_new_runtime": {"x": 1}})
        merged, _ = hr.merge_static_params(runtime, base)
        self.assertEqual(merged["params"]["brand_new_runtime"], {"x": 1})


if __name__ == "__main__":
    unittest.main()
