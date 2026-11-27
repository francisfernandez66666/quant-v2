# test_internal_strategy_entry_guard_20261126.py — §BUY-PENDING-ENTRY（2026-11-26）。
#
# 两件今天在现场造成损失的事，各一组用例：
#   A. **确认窗按"轮数"而不是"时间跨度"**（P2）：rounds=2 / gap=60 时窗口在 60 秒就关了，
#      002009 是"第二次看见一个 40 秒前才出现的信号"就买了 14.01。
#   B. **追刀闸**（P2 另一半）：`entry_price_mode=vwap_pullback` 用 min(VWAP×0.993, last)，
#      市场已经跌到 pullback 目标**下方**时，这个 min 等于"按正在崩塌的价追进去"。
#      09:33 天奇 target=14.03 / last=14.01，买到 14.01，一小时后 13.71（-2.14%）。
#
# English: locks for the 2026-11-26 entry defects — the confirm window is a TIME
# SPAN (not a round count), a dropped signal restarts the window instead of
# counting, and a market already below the pullback target is skipped (while a
# patient limit that merely sits under the target is still allowed).
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

# 固定时刻：2026-11-26（周四）10:30，连续竞价中段（14:52 收摊腿不会插进来）。
_MID_SESSION = datetime(2026, 11, 26, 10, 30)
_DAY = "20261126"
SYM_A = "000009.SZ"
SYM_C = "600001.SH"


def build_sandbox(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def load_module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class _Row(object):
    def __init__(self, **fields):
        for k, v in fields.items():
            setattr(self, k, v)


class _Counter(object):
    def __init__(self, positions=(), orders=(), cash=100000.0, total=100000.0):
        self.positions = list(positions)
        self.orders = list(orders)
        self.cash = cash
        self.total = total

    def __call__(self, acct, kind, table):
        if table == "POSITION":
            return list(self.positions)
        if table == "ACCOUNT":
            return [_Row(m_dAvailable=self.cash, m_dBalance=self.total)]
        if table == "ORDER":
            return list(self.orders)
        return []


class _Clock(object):
    def __init__(self):
        self.now = 0.0

    def time(self):
        return self.now


class _Ctx(object):
    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, *args, **kwargs):
        return {}


def _tick(price=10.0, vwap=None):
    """lastPrice=price；vwap 给了就按 amount/volume 反算（_vwap_of 读的就是这两个）。"""
    volume = 200000
    amount = (vwap if vwap else price) * volume
    return {"lastPrice": price, "lastClose": price, "open": price, "high": price,
            "low": price, "askPrice": [price + 0.01], "bidPrice": [price],
            "volume": volume, "amount": amount}


def _pos(symbol, qty=1000, cost=10.0, mv=10000.0):
    return _Row(m_strInstrumentID=symbol, stock_code=symbol, m_nVolume=qty,
                volume=qty, m_dOpenPrice=cost, open_price=cost,
                m_nCanUseVolume=0, can_use_volume=0,
                m_dMarketValue=mv, market_value=mv)


class _Base(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = load_module(path, "sandbox_entry_guard_under_test")
        cls._params = dict(cls.mod.PARAMS)
        # staticmethod：普通函数放在类属性上会被绑成实例方法（多收一个 self）。
        cls._orig_now = staticmethod(cls.mod._now_ts)
        cls._orig_total = staticmethod(cls.mod._account_total_asset)

    @classmethod
    # 测试夹具回收：build_sandbox 造出的临时目录必须整棵删掉，避免 /tmp 里堆出孤儿库
    # （本项目多次出现过临时库残留导致后续测试串味；shutil.rmtree 走 Python 而非 shell）。
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    # 每条用例都从一份干净的 PARAMS 起跑：入口闸/额度闸的键逐条覆盖，
    # 免得上一条用例写进去的开关漏到下一条（PARAMS 是模块级字典，共享即串味）。
    def setUp(self):
        mod = self.mod
        mod.PARAMS.clear()
        mod.PARAMS.update(self._params)
        mod.PARAMS.update({
            "sizing_enabled": True,
            "order_amount_cap": 20000.0,
            "min_order_amount": 10000.0,
            "daily_buy_cap": 150000.0,
            "per_trade_position_pct": 0.5,
            "max_total_position_pct": 1.0,
            "missing_key_target_cap": 1.0,
            "min_cash_reserve": 0.0,
            "lot_size": 100.0,
            "side_policy_enabled": False,
            "open_buys_grace_sec": 120.0,
            # P2 三枚 + 追刀闸
            "buy_confirm_rounds": 2,
            "buy_confirm_min_gap_sec": 60.0,
            "buy_confirm_min_span_sec": 180.0,
            "buy_confirm_reset_sec": 300.0,
            "entry_pullback_chase_guard": True,
            "vwap_pullback_pct": 0.7,
            "entry_price_mode": "vwap_pullback",
        })
        mod._STATE.update({
            "open_buys": {}, "open_buys_day": _DAY,
            "sent": set(), "buy_intents": set(), "buy_amounts": {},
            "buy_seen": {}, "buy_tries": {}, "buy_tries_day": "",
            "basis": {}, "basis_day": "", "sell_obs": {}, "sell_obs_day": "",
            "tp_fired": set(), "tp_stage_mem": {},
            "ops": [], "cancel_tried": [], "warned": {}, "last_round": 0.0,
            "acct": "8001", "acct_type": "", "round_moment": _MID_SESSION,
            "cash_probe_done": False, "cash_probe_positions": {},
        })
        mod._SANDBOX_FNS.clear()

    def tearDown(self):
        self.mod._now_ts = self._orig_now
        self.mod._account_total_asset = self._orig_total
        self.mod._SANDBOX_FNS.clear()


class ConfirmSpanTest(_Base):
    """P2-a：确认窗是**时间跨度**，不是轮数。"""

    def test_span_not_rounds(self):
        mod = self.mod
        t0 = 1_000_000.0
        ok, why = mod._buy_confirmed(SYM_A, t0)
        self.assertFalse(ok)
        self.assertEqual(why, "confirm_rounds_1_of_2")
        # 轮数够了（第 2 次观察）但跨度只有 70 秒 ⇒ 仍然不许买。
        ok, why = mod._buy_confirmed(SYM_A, t0 + 70.0)
        self.assertFalse(ok, "两轮就放行：确认窗退回了 60 秒的轮数窗口（今天 14.01 的成因）")
        self.assertEqual(why, "confirm_span_60_of_180",
                         "跨度不足必须报 confirm_span_<桶>_of_<span>，实得 %s" % why)
        # 30 秒一次连看两轮：连 gap 都不到，更不该算"持续观察"。
        mod._STATE["buy_seen"] = {}
        mod._buy_confirmed(SYM_A, t0)
        ok, why = mod._buy_confirmed(SYM_A, t0 + 30.0)
        self.assertFalse(ok)
        self.assertEqual(why, "confirm_gap_pending")
        # 跨度真的到了 180 秒 ⇒ 放行（不是"永远不放行"）。
        mod._STATE["buy_seen"] = {}
        mod._buy_confirmed(SYM_A, t0)
        mod._buy_confirmed(SYM_A, t0 + 70.0)
        ok, why = mod._buy_confirmed(SYM_A, t0 + 185.0)
        self.assertTrue(ok, "跨度 185 秒仍然拒绝：窗被写成了永远关不上: %s" % why)

    def test_stale_signal_restarts(self):
        """P2-b：信号消失超过 reset ⇒ 旧的观察作废，重新从 1 数起。"""
        mod = self.mod
        t0 = 1_000_000.0
        mod._buy_confirmed(SYM_A, t0)
        ok, why = mod._buy_confirmed(SYM_A, t0 + 301.0)
        self.assertFalse(ok)
        self.assertEqual(why, "confirm_restart",
                         "断档超过 reset 必须先报 confirm_restart（不是 gap_pending），实得 %s" % why)
        self.assertEqual(mod._STATE["buy_seen"][SYM_A], [], "重启后旧观察必须清空")
        # 重启后第一次观察只算 1，不许接着昨天的数继续。
        ok, why = mod._buy_confirmed(SYM_A, t0 + 370.0)
        self.assertFalse(ok)
        self.assertEqual(why, "confirm_rounds_1_of_2",
                         "重启后把旧观察又算进去了: %s" % why)

    def test_span_missing_refuses(self):
        """P2-c：跨度/重置两枚旋钮缺失 ⇒ 拒绝买入并点名（不许退回 60 秒窗口）。"""
        mod = self.mod
        mod.PARAMS.pop("buy_confirm_min_span_sec", None)
        ok, why = mod._buy_confirmed(SYM_A, 1_000_000.0)
        self.assertFalse(ok)
        self.assertEqual(why, "buy_confirm_span_missing")


class ChaseGuardTest(_Base):
    """P2-d：市场已经在 pullback 目标下方 ⇒ 这一轮跳过（不许按崩塌价追）。"""

    def _reset(self, tick, signal_price=14.01):
        mod = self.mod
        self.placed = []
        counter = _Counter(positions=[_pos(SYM_C, qty=1000, cost=78.827, mv=78827.0)])
        mod._SANDBOX_FNS.update({
            "passorder": lambda *a: self.placed.append(a),
            "cancel": lambda *a: None,
            "get_account": lambda *a: "8001",
            "get_trade_detail_data": counter})
        mod._STATE.update({
            "sent": set(), "buy_intents": set(), "buy_amounts": {},
            "buy_seen": {}, "buy_tries": {}, "buy_tries_day": "",
            "open_buys": {}, "open_buys_day": _DAY,
            "basis": {}, "basis_day": "", "sell_obs": {}, "sell_obs_day": "",
            "tp_fired": set(), "tp_stage_mem": {},
            "ops": [], "cancel_tried": [], "warned": {}, "last_round": 0.0,
            "acct": "8001", "acct_type": "",
            "cash_probe_done": False, "cash_probe_positions": {},
        })
        mod.PARAMS["symbols"] = [SYM_A]
        mod.PARAMS["industry_map"] = {SYM_A: "BANK", SYM_C: "ENERGY"}
        mod.PARAMS["concept_map"] = {SYM_A: "CSI", SYM_C: "OTHER"}
        mod.PARAMS["emergency_stop"] = False
        mod.PARAMS["rule_arm"] = {}
        mod.PARAMS["_PORTED_RULES_FORCED_OFF"] = True
        mod._PORTED_RULES.clear()

        def one_signal(params, ctx):
            if ctx.get("symbol") != SYM_A:
                return None
            return {"side": "buy", "short_code": "FC000009", "qty": 1400,
                    "price": signal_price, "amount": round(signal_price * 1400, 2),
                    "symbol": SYM_A}
        mod.rule_pullback_buy = one_signal
        mod._account_total_asset = lambda: 100000.0
        self.clock = _Clock()
        mod.time = self.clock
        self.ctx = _Ctx({SYM_A: tick})
        return counter

    def _round(self, when):
        self.clock.now = when.timestamp()
        return self.mod.run_round(self.ctx, when)

    def test_below_target_is_skipped(self):
        """VWAP 14.13 ⇒ target 14.03；市场 14.01 已在目标下方 ⇒ 跳过。

        这正是 09:33 天奇的形状：min(target, last) 把"等回抽"变成"追下跌"。
        """
        tick = _tick(14.01, vwap=14.13)
        self._reset(tick)
        out = self._round(datetime(2026, 11, 26, 10, 30, 0))
        self.assertEqual(len(self.placed), 0, "市场已在目标下方还买进去了: %s" % out)
        reasons = [s["reason"] for s in out["skipped"]]
        self.assertIn("vwap_pullback_passed", reasons,
                      "拒因必须点名 vwap_pullback_passed，实得 %s" % reasons)

    def test_patient_limit_still_allowed(self):
        """反证：市场**高于**目标（14.10 > 14.03）时闸不许误杀。

        信号限价 14.01 本来就低于目标，那是"耐心限价单"，不是追刀 ——
        闸比的是市场价 last_price，不是 signal 的限价（拿限价比会把这单误杀）。
        """
        tick = _tick(14.10, vwap=14.13)
        self._reset(tick, signal_price=14.01)
        out = self._round(datetime(2026, 11, 26, 10, 30, 0))
        reasons = [s["reason"] for s in out["skipped"]]
        self.assertNotIn("vwap_pullback_passed", reasons,
                         "市场在目标上方却被追刀闸拦掉（误杀耐心限价单）: %s" % reasons)
        # 它应当正常走到确认窗（第一次观察）。
        self.assertIn("confirm_rounds_1_of_2", reasons)

    def test_above_target_reprice(self):
        """市场在目标上方且信号限价更高 ⇒ 按 target 改价（改价腿没被闸吃掉）。"""
        tick = _tick(14.10, vwap=14.13)
        self._reset(tick, signal_price=14.20)
        out = self._round(datetime(2026, 11, 26, 10, 30, 0))
        reasons = [s["reason"] for s in out["skipped"]]
        self.assertNotIn("vwap_pullback_passed", reasons, "改价腿被追刀闸吃掉了: %s" % reasons)
        priced = out.get("priced") or []
        self.assertTrue(priced, "没有改价记录（vwap_pullback 腿没跑）: %s" % out)
        self.assertAlmostEqual(float(priced[0]["price"]), 14.03, places=2)

    def test_guard_off_restores_old_cap(self):
        """显式关掉闸 ⇒ 回到 min(target, last) 的旧行为（可回退，不是硬编码）。"""
        self.mod.PARAMS["entry_pullback_chase_guard"] = False
        tick = _tick(14.01, vwap=14.13)
        self._reset(tick)
        self.mod.PARAMS["entry_pullback_chase_guard"] = False
        out = self._round(datetime(2026, 11, 26, 10, 30, 0))
        reasons = [s["reason"] for s in out["skipped"]]
        self.assertNotIn("vwap_pullback_passed", reasons,
                         "闸已经显式关掉却还在拦: %s" % reasons)


if __name__ == "__main__":
    unittest.main()
