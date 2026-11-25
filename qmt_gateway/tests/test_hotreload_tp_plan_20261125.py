# test_hotreload_tp_plan_20261125.py — D2（止盈阶梯的"当前这一段建仓"分母）行为锁（2026-11-25）。
#
# 为什么要有这份文件：`bake_tp_plan` 是云端唯一知道"这只票是哪天、哪个战法买的"的地方，
# 载体没有磁盘、柜台 POSITION 行又不带入场战法与峰值 ⇒ **止盈 P&L 的分母只能在这里算对**。
# 2026-11-24 现场：000797 是 10:08 买的，梯子却拿着 6 周前的 `open_date` ⇒ pnl 一天之内
# 跨过 30% ⇒ 龙头腿按"已经涨到 30%"整仓清掉。分母错一次，止损/止盈的行为就整套错。
#
# 本文件锁四件（对应 §17.1 的 ⑳㉑ 与 §17.5 的"6 例"）：
#   1. **段重置**：净头寸从 0 起来、或这一笔买入本身就是当前头寸的主体（≥50%），
#      才重写 open_date/strategy/open_price；同段内的加仓**不覆盖**分母。
#   2. **`stage == 0` 是硬前置**：卖过一半之后的大额加仓不许把阶梯打回 Stage 0，
#      否则同一只票当天会被再卖第二个半仓（重复卖＝把仓位卖光）。
#   3. **`open_price` 进载荷**：载体靠它算 pnl 分母，缺了就回落 position_cost。
#   4. **digest 含 `open_price` 但不含 `peak`**：peak 每天都动，算进 sha 会让每次定时
#      投递都"看起来变了"——**一次投递＝盒子客户端一次有序重启**。
#
# 全部用例都在临时 sqlite 上跑真 SQL，不打桩 `bake_tp_plan` 本体：这些判据的价值
# 就在"与真表形状对得上"（`daily.trade_date` 是紧凑形态 `20261125`，成交时间是带横线的）。
#
# English: behaviour locks for the ladder-plan baker — segment rebase (net<=0 or a
# dominant buy), stage==0 hard precondition, open_price in the payload, and a digest
# that includes open_price but not peak.
import importlib.util
import json
import os
import shutil
import sqlite3
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
HOTRELOAD = os.path.join(REPO, "scripts", "carrier_hot_reload_gz.py")

OWNER = "u_1785377969825355000"


def _load_hotreload(build_dir, live_db, quant_db):
    """Import a FRESH copy of the hot-reload script with the temp paths patched in.

    A fresh module per test matters: the script reads `BUILD_DIR` / `LIVE_DB` at
    import time, and a shared module would keep the first test's database.
    """
    os.environ["CARRIER_BUILD_DIR"] = build_dir
    os.environ["LIVE_DB"] = live_db
    os.environ["QUANT_DATA_DIR"] = os.path.dirname(build_dir)
    spec = importlib.util.spec_from_file_location("hotreload_tp_plan_%d" % id(live_db), HOTRELOAD)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    # HOTRELOAD_DB 是日 K 的库（trading.db），与 live.db 不是同一个文件。
    module.HOTRELOAD_DB = quant_db
    module.OWNER_UID = OWNER
    return module


def _make_dbs(root):
    """Two throwaway databases with only the columns the baker actually reads."""
    build_dir = os.path.join(root, "carrier_build")
    os.makedirs(os.path.join(build_dir, "qmt_gateway"))
    with open(os.path.join(build_dir, "qmt_gateway", "local_executor_params.json"), "w",
              encoding="utf-8") as fh:
        json.dump({"params": {}}, fh)
    live = os.path.join(root, "live.db")
    conn = sqlite3.connect(live)
    conn.executescript(
        """
        CREATE TABLE fills (id INTEGER PRIMARY KEY, user_id TEXT, code TEXT, side TEXT,
                            qty INTEGER, traded_at TEXT, signal_id TEXT, price REAL);
        CREATE TABLE real_positions (user_id TEXT, ts_code TEXT, qty INTEGER,
                                     highest_price REAL);
        """
    )
    conn.commit()
    conn.close()
    quant = os.path.join(root, "trading.db")
    conn = sqlite3.connect(quant)
    conn.executescript(
        """
        CREATE TABLE daily (ts_code TEXT, trade_date TEXT, high REAL);
        """
    )
    conn.commit()
    conn.close()
    return build_dir, live, quant


def _add_fill(live, day, code, side, qty, price, signal_id=""):
    conn = sqlite3.connect(live)
    conn.execute(
        "INSERT INTO fills (user_id, code, side, qty, traded_at, signal_id, price) "
        "VALUES (?,?,?,?,?,?,?)",
        (OWNER, code, side, qty, "%s 10:00:00" % day, signal_id, price))
    conn.commit()
    conn.close()


def _hold(live, code, qty, highest=0.0):
    conn = sqlite3.connect(live)
    conn.execute("INSERT INTO real_positions (user_id, ts_code, qty, highest_price) "
                 "VALUES (?,?,?,?)", (OWNER, code, qty, highest))
    conn.commit()
    conn.close()


def _today():
    return datetime.now().astimezone().strftime("%Y-%m-%d")


class TpPlanRebaseTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        # 用例结束即回收夹具库：这类 sqlite 里全是假代码/假成交，留着只会
        # 让下一次排查分不清是测试残留还是现场数据。
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.build_dir, self.live, self.quant = _make_dbs(self.root)
        self.mod = _load_hotreload(self.build_dir, self.live, self.quant)
        self.today = _today()

    def _plan(self, code):
        plan = self.mod.bake_tp_plan(qc={})
        day = datetime.now().astimezone().strftime("%Y%m%d")
        return (plan.get(day) or {}).get(code) or {}

    def test_open_price_is_baked_for_the_current_segment(self):
        # 正常一买：分母＝这笔成交价，不是 position_cost（对账行不带成本时它就是 0）。
        _add_fill(self.live, self.today, "000797", "买入", 1000, 12.34,
                  "buy:000797:dragon:20261125")
        _hold(self.live, "000797", 1000)
        row = self._plan("000797")
        self.assertEqual(row.get("open_price"), 12.34)
        self.assertEqual(row.get("strategy"), "dragon")
        self.assertEqual(row.get("stage"), 0)
        self.assertFalse(row.get("manual"))

    def test_add_on_does_not_move_the_denominator(self):
        # 同段加仓（100 股 vs 1100 股 ≈ 9% < 50%）：分母不动。
        # 这是"平均成本"与"段起点"的分野——用平均成本会让 pnl 随加仓漂移。
        _add_fill(self.live, self.today, "000797", "买入", 1000, 12.00,
                  "buy:000797:dragon:20261125")
        _add_fill(self.live, self.today, "000797", "买入", 100, 13.00,
                  "buy:000797:fac_160:20261125")
        _hold(self.live, "000797", 1100)
        row = self._plan("000797")
        self.assertEqual(row.get("open_price"), 12.0)

    def test_dominant_buy_rebases_the_segment(self):
        # 零头被打断后主仓才进来（100 股 → 再买 5000 股）：这一笔就是主体，
        # 分母必须是它（`q*100 >= net_after*50%` 那条判据），否则整段的 pnl 都算在零头上。
        _add_fill(self.live, self.today, "000797", "买入", 100, 12.00,
                  "buy:000797:dragon:20261125")
        _add_fill(self.live, self.today, "000797", "买入", 5000, 9.50,
                  "buy:000797:dragon:20261125")
        _hold(self.live, "000797", 5100)
        row = self._plan("000797")
        self.assertEqual(row.get("open_price"), 9.5)

    def test_stage_one_never_rebases(self):
        # 卖过一半之后再加仓：stage 必须还是 1，分母不许被这次加仓改写。
        # 反例方向：改回去 ⇒ 同一只票当天再卖一个半仓 ⇒ 等于清仓。
        _add_fill(self.live, self.today, "000797", "买入", 1000, 12.00,
                  "buy:000797:dragon:20261125")
        _add_fill(self.live, self.today, "000797", "卖出", 500, 15.00,
                  "sell:000797:tp:20261125")
        _add_fill(self.live, self.today, "000797", "买入", 900, 14.00,
                  "buy:000797:dragon:20261125")
        _hold(self.live, "000797", 1400)
        row = self._plan("000797")
        self.assertEqual(row.get("stage"), 1)
        self.assertEqual(row.get("open_price"), 12.0)

    def test_flat_then_rebuy_starts_a_new_segment(self):
        # 清仓后再买 ⇒ 新一轮建仓：分母/日期/Stage 全部从零重来。
        _add_fill(self.live, self.today, "000797", "买入", 1000, 12.00,
                  "buy:000797:dragon:20261125")
        _add_fill(self.live, self.today, "000797", "卖出", 1000, 16.00,
                  "sell:000797:tt:20261125")
        _add_fill(self.live, self.today, "000797", "买入", 800, 11.00,
                  "buy:000797:double_bump:20261125")
        _hold(self.live, "000797", 800)
        row = self._plan("000797")
        self.assertEqual(row.get("open_price"), 11.0)
        self.assertEqual(row.get("strategy"), "double_bump")
        self.assertEqual(row.get("stage"), 0)

    def test_digest_tracks_open_price_and_ignores_peak(self):
        base = {self.today.replace("-", ""): {"000797": {
            "strategy": "dragon", "open_date": self.today, "stage": 0,
            "manual": False, "tp_pct": 30.0, "open_price": 12.0, "peak": 13.0}}}
        same_but_peak = {self.today.replace("-", ""): {"000797": {
            "strategy": "dragon", "open_date": self.today, "stage": 0,
            "manual": False, "tp_pct": 30.0, "open_price": 12.0, "peak": 19.9}}}
        other_price = {self.today.replace("-", ""): {"000797": {
            "strategy": "dragon", "open_date": self.today, "stage": 0,
            "manual": False, "tp_pct": 30.0, "open_price": 9.5, "peak": 13.0}}}
        d0 = self.mod.tp_plan_digest(base)
        self.assertTrue(d0)
        # peak 每天都动：进 digest 就等于每个 tick 都投递一次（客户端重启）。
        self.assertEqual(d0, self.mod.tp_plan_digest(same_but_peak))
        # open_price 是 pnl 的分母：它变了必须重烘。
        self.assertNotEqual(d0, self.mod.tp_plan_digest(other_price))
        # "从没烘过"与"烘了但是空表"要能分开。
        self.assertEqual(self.mod.tp_plan_digest({}), "")


if __name__ == "__main__":
    unittest.main()
