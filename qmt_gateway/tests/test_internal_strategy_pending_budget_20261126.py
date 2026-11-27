# test_internal_strategy_pending_budget_20261126.py — §BUY-PENDING-ENTRY（2026-11-26）。
#
# 现场（盒上 XtClient_FormulaOutput_20261126.log）：
#   09:32:07  000009 宝安 6.12×3200 = 19,584 发出（柜台行已出现，尺寸字段还是空的）
#   09:33:05  002009 天奇 14.01×1400 = 19,614 又发一笔，cash_probe 打
#             `target_room=21173` —— 上一笔的 19,584 一分钱都没被扣掉
#   09:36:47  起 room=0，全天 `hard_cap_below_floor`
# 两笔合计 39,198，是动态目标 21,173 的 1.85 倍。
#
# 三条缺陷（各一组用例）：
#   A. ORDER 行"尺寸还没回填"被当成"0 股 0 价"直接丢掉（P0-a）；
#   B. 尺寸读不到的**在途买单**因此不占预算（P0-b）——这一条是当天超额的直接成因；
#   C. 进程内账与柜台行是**同一张单**，只能取 max，相加就把一笔算两遍（P1-c）。
# 另加一条"释放必须有柜台证明"（P1-d，不许在下单后头两秒把预占放掉）。
#
# English: locks for the 2026-11-26 overshoot — unreadable order sizes are marked,
# an unreadable working buy still reserves the single-order cap, the process journal
# and the counter describe ONE ticket (max, never sum), a reservation is released
# only on counter proof, and the live two-order sequence is replayed.
import contextlib
import importlib.util
import io
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

# 固定时刻：2026-11-26（周四）10:30，连续竞价中段。
# 不用真实钟：cancel_leg 有 14:52 收摊腿，真实钟跑过 14:52 会真撤单（假红）。
_MID_SESSION = datetime(2026, 11, 26, 10, 30)
_DAY = "20261126"

SYM_A = "000009.SZ"
SYM_B = "002009.SZ"
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
    """ACCOUNT / POSITION / ORDER / DEAL 四张表的最小桩。"""

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


def _tick(price=10.0):
    return {"lastPrice": price, "lastClose": price, "open": price, "high": price,
            "low": price, "askPrice": [price + 0.01], "bidPrice": [price],
            "volume": 200000, "amount": price * 200000}


def _pos(symbol, qty=1000, cost=10.0, mv=10000.0):
    return _Row(m_strInstrumentID=symbol, stock_code=symbol, m_nVolume=qty,
                volume=qty, m_dOpenPrice=cost, open_price=cost,
                m_nCanUseVolume=0, can_use_volume=0,
                m_dMarketValue=mv, market_value=mv)


def _order_row(symbol, short, qty=None, price=None, status=50):
    """柜台 ORDER 原始对象：qty/price 传 None ⇒ 尺寸字段根本没回填（今天的形状）。"""
    row = _Row(m_strRemark="%s-%s" % ("VERSION", short),
               m_nOrderStatus=status, m_nOffsetFlag=48,
               m_strOrderSysID="OID-%s" % short, m_strInstrumentID=symbol.split(".")[0])
    if qty is not None:
        row.m_nOrderVolume = qty
        row.m_nVolumeTraded = 0
    if price is not None:
        row.m_dOrderPrice = price
    return row


class _Base(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = load_module(path, "sandbox_pending_budget_under_test")
        cls._params = dict(cls.mod.PARAMS)
        # staticmethod: a plain function stored as a CLASS attribute would be
        # bound to the test instance on `self._orig_now` and then called with an
        # extra argument (TypeError: _now_ts() takes 0 positional arguments).
        cls._orig_now = staticmethod(cls.mod._now_ts)
        cls._orig_total = staticmethod(cls.mod._account_total_asset)

    @classmethod
    # 测试夹具回收：build_sandbox 造出的临时目录必须整棵删掉，避免 /tmp 里堆出孤儿库
    # （本项目多次出现过临时库残留导致后续测试串味；shutil.rmtree 走 Python 而非 shell）。
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    # 每条用例都从一份干净的 PARAMS 起跑：在途买单/预算相关的键逐条覆盖，
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
        mod._now_ts = self._orig_now

    def tearDown(self):
        self.mod._now_ts = self._orig_now
        self.mod._account_total_asset = self._orig_total
        self.mod._SANDBOX_FNS.clear()


class UnreadableSizeTest(_Base):
    """P0-a：读不到尺寸与"真的是 0"必须可区分。"""

    def test_size_unknown_is_marked(self):
        mod = self.mod
        # 尺寸字段完全没回填（今天 09:32:44 的 000009 就是这一形状）。
        blank = _order_row(SYM_A, "FC000009")
        blank.m_strRemark = "%s-FC000009" % mod.STRATEGY_VERSION
        sized = _order_row(SYM_B, "FC002009", qty=1400, price=14.01)
        sized.m_strRemark = "%s-FC002009" % mod.STRATEGY_VERSION
        saved = mod._counter_rows
        mod._counter_rows = lambda table: [blank, sized] if table == "ORDER" else []
        try:
            rows, ok = mod._live_orders()
        finally:
            mod._counter_rows = saved
        self.assertTrue(ok)
        by_code = {r["short_code"]: r for r in rows}
        self.assertTrue(by_code["FC000009"]["size_unknown"],
                        "尺寸没回填的行必须标 size_unknown（否则会被当成 0 股丢掉）")
        self.assertFalse(by_code["FC002009"]["size_unknown"],
                         "尺寸读得到的行不许标 size_unknown")
        self.assertFalse(by_code["FC000009"]["terminal"],
                         "工作单不许被读成终态（夹具前提：状态 50 非终态）")

    def test_unreadable_size_reserves_the_cap(self):
        """P0-b：尺寸读不到的在途买单按单笔帽预占——不许丢掉。"""
        mod = self.mod
        rows = [{"symbol": "000009", "short_code": "FC000009", "side": "buy",
                 "terminal": False, "qty": 0.0, "traded": 0.0, "price": 0.0,
                 "size_unknown": True}]
        pending = mod._pending_buy_rows(rows)
        self.assertEqual(len(pending), 1, "尺寸读不到的在途买单被丢掉了（当天超额的直接成因）")
        self.assertEqual(pending[0]["remain"], 20000.0, "预占额必须等于 order_amount_cap")
        self.assertEqual(mod._pending_amount_for(SYM_A, pending), 20000.0)
        # 真成交完的行（qty>0, price>0, 余量 0）是"确实没有钱了"，不预占。
        filled = [{"symbol": "000009", "short_code": "FC000009", "side": "buy",
                   "terminal": False, "qty": 3200, "traded": 3200, "price": 6.12,
                   "size_unknown": False}]
        self.assertEqual(mod._pending_buy_rows(filled), [])

    def test_size_unknown_without_cap_still_reserves_floor(self):
        """P0-b 退路：连 order_amount_cap 都缺时按 min_order_amount 预占，绝不当 0 丢。"""
        mod = self.mod
        saved = mod.PARAMS.get("order_amount_cap")
        mod.PARAMS["order_amount_cap"] = 0.0
        try:
            rows = [{"symbol": "000009", "short_code": "FC000009", "side": "buy",
                     "terminal": False, "qty": 0.0, "traded": 0.0, "price": 0.0,
                     "size_unknown": True}]
            pending = mod._pending_buy_rows(rows)
            self.assertEqual(len(pending), 1, "缺 cap 时把读不到尺寸的买单丢了")
            self.assertEqual(pending[0]["remain"], mod.PARAMS["min_order_amount"],
                             "缺 cap 时没有回落到 min_order_amount")
        finally:
            if saved is None:
                mod.PARAMS.pop("order_amount_cap", None)
            else:
                mod.PARAMS["order_amount_cap"] = saved

    def test_cash_probe_reports_pending(self):
        """P0-d：`pend=` 必须是真值（它长期打 0，把人引到"没有在途单"上）。"""
        mod = self.mod
        mod._account_total_asset = lambda: 100000.0
        mod._STATE["cash_probe_positions"] = {SYM_C: {"qty": 1000, "cost": 10.0}}
        lines = []
        saved_log = mod._log
        mod._log = lambda text: lines.append(text)
        try:
            mod._probe_cash_line("FC002009", 20000.0, "", "", {
                "symbol": SYM_B, "last_price": 14.01, "available_cash": 100000.0,
                "total_asset": 100000.0, "hhmm": "10:30", "day": _DAY,
                "market_scale": 1.0, "index_change_pct": 0.5,
            }, mod.PARAMS, [{"symbol": "000009", "qty": 3200, "remain": 3200,
                             "price": 6.12}])
        finally:
            mod._log = saved_log
        self.assertEqual(len(lines), 1, "cash_probe 必须打一行")
        self.assertIn("pend=19584", lines[0],
                      "cash_probe 的 pend= 仍不是真值（读的人会以为没有在途单）: %s" % lines[0])


class JournalMergeTest(_Base):
    """P1-c：进程内账与柜台行是同一张单 —— 取 max，永不求和。"""

    def test_journal_and_counter_take_max(self):
        mod = self.mod
        counter = [{"symbol": "000009", "qty": 3200, "remain": 3200, "price": 6.12}]
        mod._STATE["open_buys"] = {
            (_DAY, "000009"): {"qty": 3200, "price": 6.12, "short": "FC000009", "at": 0.0}}
        merged = mod._open_buys_merge(_DAY, counter)
        self.assertEqual(len(merged), 1, "同一只票必须只有一行")
        amount = float(merged[0]["remain"]) * float(merged[0]["price"])
        self.assertAlmostEqual(amount, 3200 * 6.12,
                               msg="柜台+进程内账相加了：同一张单被算两遍")
        # 进程内账**更大**（柜台只回填了一部分尺寸）⇒ 取 max，仍然不许相加。
        # 这条是上面那条的反面：两边相等时被 `amount <= current` 短路挡掉，
        # 只有"进程内账严格更大"才走得到那条赋值 —— 把 MAX 改成 SUM 只有这里会红。
        mod._STATE["open_buys"] = {
            (_DAY, "000009"): {"qty": 5000, "price": 6.12, "short": "FC000009", "at": 0.0}}
        merged = mod._open_buys_merge(_DAY, counter)
        self.assertEqual(len(merged), 1)
        self.assertAlmostEqual(float(merged[0]["remain"]) * float(merged[0]["price"]),
                               5000 * 6.12,
                               msg="进程内账更大时被相加了（19,584 + 30,600 那种形状）")
        # 进程内账更大（柜台还没回填尺寸）⇒ 进程内账那笔赢。
        mod._STATE["open_buys"] = {
            (_DAY, "000009"): {"qty": 3200, "price": 6.12, "short": "FC000009", "at": 0.0}}
        merged = mod._open_buys_merge(_DAY, [])
        self.assertEqual(len(merged), 1)
        self.assertAlmostEqual(float(merged[0]["remain"]) * float(merged[0]["price"]),
                               3200 * 6.12)
        # 跨日的账不许漏进今天。
        mod._STATE["open_buys"] = {
            ("20261125", "000009"): {"qty": 3200, "price": 6.12, "short": "FC000009", "at": 0.0}}
        self.assertEqual(mod._open_buys_merge(_DAY, []), [])

    def test_settle_needs_counter_proof(self):
        """P1-d：没有柜台行不是证据；宽限期内必须继续占着。"""
        mod = self.mod
        now = 1_000_000.0
        mod._now_ts = lambda: now
        mod._STATE["open_buys"] = {
            (_DAY, "000009"): {"qty": 3200, "price": 6.12, "short": "FC000009",
                               "at": now - 10.0}}
        mod._open_buys_settle(_DAY, [])
        self.assertIn((_DAY, "000009"), mod._STATE["open_buys"],
                      "下单后 10 秒（宽限期内）就把预占放掉了")
        # 过了宽限期仍然没有行 ⇒ 这张单没落到柜台上，释放。
        mod._now_ts = lambda: now + 300.0
        mod._open_buys_settle(_DAY, [])
        self.assertNotIn((_DAY, "000009"), mod._STATE["open_buys"])
        # 有行且仍在途 ⇒ 哪怕过了宽限期也继续占着。
        mod._now_ts = lambda: now
        mod._STATE["open_buys"] = {
            (_DAY, "000009"): {"qty": 3200, "price": 6.12, "short": "FC000009",
                               "at": now - 10.0}}
        mod._now_ts = lambda: now + 300.0
        mod._open_buys_settle(_DAY, [{"symbol": "000009", "terminal": False}])
        self.assertIn((_DAY, "000009"), mod._STATE["open_buys"])
        # 有行但已终态 ⇒ 证明这张单死了，立即释放。
        mod._open_buys_settle(_DAY, [{"symbol": "000009", "terminal": True}])
        self.assertNotIn((_DAY, "000009"), mod._STATE["open_buys"])


class Replay20261126Test(_Base):
    """§243 ⑮：复现今天那两笔——第二笔必须被拒。"""

    def _reset(self, counter):
        mod = self.mod
        self.placed = []
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
        mod.PARAMS["symbols"] = [SYM_A, SYM_B]
        # 持仓那只是 ENERGY，两只候选分属 BANK/TECH：集中度三顶帽不许在这条
        # 用例里先把它拦掉（今天现场 000009 也是在新行业里开的仓）。
        mod.PARAMS["industry_map"] = {SYM_A: "BANK", SYM_B: "TECH", SYM_C: "ENERGY"}
        mod.PARAMS["concept_map"] = {SYM_A: "CSI", SYM_B: "CSI", SYM_C: "OTHER"}
        mod.PARAMS["emergency_stop"] = False
        mod.PARAMS["rule_arm"] = {}
        mod.PARAMS["_PORTED_RULES_FORCED_OFF"] = True
        mod.PARAMS["buy_confirm_rounds"] = 2
        mod.PARAMS["buy_confirm_min_gap_sec"] = 60.0
        mod.PARAMS["buy_confirm_min_span_sec"] = 180.0
        mod.PARAMS["buy_confirm_reset_sec"] = 300.0
        mod.PARAMS["entry_price_mode"] = "signal"
        mod._PORTED_RULES.clear()
        # 今天两笔的先后顺序：先 000009（09:32:07），再 002009（09:33:05）。
        # `active` 控制本轮哪些票出现在候选里，这样两次买入落在两轮而不是同一轮
        # （同一轮内 pending_rows 会立刻预占，测的就不是跨轮那个缺口了）。
        self.active = set([SYM_A])

        def one_signal(params, ctx):
            symbol = ctx.get("symbol")
            if symbol not in self.active:
                return None
            return {"side": "buy", "short_code": "FC%s" % symbol[:6], "qty": 1400,
                    "price": 14.01, "amount": round(14.01 * 1400, 2), "symbol": symbol}
        mod.rule_pullback_buy = one_signal
        mod._account_total_asset = lambda: 100000.0
        self.counter = counter
        self.clock = _Clock()
        mod.time = self.clock

    def _round(self, when):
        self.clock.now = when.timestamp()
        return self.mod.run_round(_Ctx({SYM_A: _tick(14.01), SYM_B: _tick(14.01)}), when)

    def test_replay_20261126_second_order_refused(self):
        """target=96,608（基准 100,000 × ms 0.9661）/ held=78,827 / room=17,781
        / cap=20,000 / floor=10,000。

        09:30:00  A 第一次被看见（confirm 1/2）
        09:33:00  A 第二次，跨度 180 s ⇒ 买 17,781（room 把单笔帽压下来）
        09:33:30  B 第一次被看见——此刻柜台**已有 A 的行，但尺寸字段是空的**
        09:36:30  B 第二次，跨度 180 s ⇒ room 只剩 0 < 地板 ⇒ hard_cap_below_floor

        修之前那一行会被丢掉，B 照样拿满 20,000（当天 39,198 的成因）。
        """
        mod = self.mod
        held = _pos(SYM_C, qty=1000, cost=78.827, mv=78827.0)
        self._reset(_Counter(positions=[held]))
        seed = self._round(datetime(2026, 11, 26, 9, 30, 0))
        self.assertEqual(len(self.placed), 0, "确认窗还没满就下单了: %s" % seed)
        self.assertIn("confirm_rounds_1_of_2", [s["reason"] for s in seed["skipped"]])
        first = self._round(datetime(2026, 11, 26, 9, 33, 0))
        self.assertEqual(len(self.placed), 1, "第一笔应当发出: %s" % first)
        # 柜台已经能看到 A 的行，但尺寸字段没回填（09:32:44 的现场形状）。
        blank = _order_row(SYM_A, "FC000009")
        blank.m_strRemark = "%s-FC000009" % mod.STRATEGY_VERSION
        self.counter.orders = [blank]
        self.active = set([SYM_B])
        self._round(datetime(2026, 11, 26, 9, 33, 30))
        second = self._round(datetime(2026, 11, 26, 9, 36, 30))
        self.assertEqual(len(self.placed), 1,
                         "第二笔又发出去了（尺寸读不到的在途单没有占住额度）")
        reasons = [s["reason"] for s in second["skipped"]]
        self.assertIn("hard_cap_below_floor", reasons,
                      "第二笔的拒因必须是 hard_cap_below_floor，实得 %s" % reasons)


if __name__ == "__main__":
    unittest.main()
