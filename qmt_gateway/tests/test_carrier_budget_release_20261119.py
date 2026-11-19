# test_carrier_budget_release_20261119.py — 载体侧「撤单释放预算」与「在途单占预算」
# 的行为锁（owner 2026-11-19 报障两条）。
#
# owner 原话（逐字）：
#   "挂单不会自己撤单，手动撤单后，预算应该也没有及时恢复（因为没有新的个股挂单）。
#    我之前的设计是十分钟不成交会自动撤单，且计时结束以后资金拿最新的可用资金。"
#
# 报障拆开是三件事，这一组逐一钉：
#   A. 自动撤单腿**确实按 600 秒**判陈旧（现场烘成了 900 秒，等于 15 分钟才撤；
#      `cancel_watch` 那一行是"腿到底跑没跑"的读数，静默与"没有陈旧单"原本同形）；
#   B. 手动撤单之后，三个会话账（sent / buy_intents / buy_amounts）必须**当轮释放**，
#      否则同一只票当天再也发不出去（"没有新的个股挂单"的直接成因）；
#   C. 释放只能发生**一次**（按委托号），否则新补的那一单会被它自己之前那批已撤行
#      再释放一次 ⇒ 同一只票买两遍；
#   D. 当日已投金额 = 柜台真值（已成按成交量、在途按未成余量），会话账只补柜台还没
#      看到的那几只票——两个都算会重复计，两个都不算会让刚发出去那一笔不占预算。
#
# English: behavioural locks for the 2026-11-19 owner report — the stale-cancel leg must
# fire at 600s and say so, a manual cancel must release the same-day session journals in
# the same round, the release is one-shot per order id, and the committed-buy amount is
# counter truth (traded + unfilled remainder) with the session journal only filling in
# symbols the counter has not shown yet.
import contextlib
import importlib.util
import io
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

# 固定时刻：2026-10-28（周三）10:30，连续竞价中段。
# 不用真实钟：cancel_leg 有一条"到 close_sweep_from 就把在途单撤掉"的收摊腿，
# 真实钟跑到 14:52 之后会真撤单，断言就成了随时刻漂移的假红。
_MID_SESSION = datetime(2026, 10, 28, 10, 30)
_DAY = "20261028"


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


def row(symbol, short, price, qty, traded=0.0, terminal=False, side="buy",
        order_id="", cancel_like=None):
    """一条 `_live_orders()` 形状的委托行（键名与产物逐个对齐）。"""
    out = {"order_id": order_id, "symbol": symbol, "short_code": short, "side": side,
           "price": price, "qty": qty, "traded": traded, "terminal": terminal}
    if cancel_like is not None:
        out["cancel_like"] = cancel_like
    return out


class _SandboxBase(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = load_module(path, "sandbox_budget_release_under_test")
        cls._params = dict(cls.mod.PARAMS)

    def setUp(self):
        # 会话账逐条复位：这些用例互相独立，不能靠"上一条刚好清干净"。
        self.mod._STATE.update({
            "sent": set(), "buy_intents": set(), "buy_amounts": {},
            "released_orders": set(), "released_day": _DAY,
            "cancel_tried": [], "ops": [], "warned": {},
            "cancel_watch_key": None, "round_moment": _MID_SESSION,
            "acct": "", "acct_ok": False, "acct_type": "",
        })
        self.mod._SANDBOX_FNS.clear()

    def tearDown(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self._params)


class SpentBuyAmountTest(_SandboxBase):
    """D：当日已投金额的口径（在途占预算 / 已撤释放）。"""

    PREFIXES = {"FC"}

    def spent(self, orders):
        return self.mod._spent_buy_amount(orders, self.PREFIXES, _DAY)

    def test_pending_remainder_is_committed_money(self):
        """在途买单按**未成余量**占预算（owner 裁决三：在途单要占用预算）。"""
        got = self.spent([row("600000.SH", "FC600000", 10.0, 1000)])
        self.assertAlmostEqual(got, 10000.0)

    def test_cancelled_row_releases_its_remainder(self):
        """已撤且零成交 ⇒ 一分钱都不再占（这就是"手动撤单后预算回来"）。"""
        got = self.spent([row("600000.SH", "FC600000", 10.0, 1000,
                              terminal=True, cancel_like=True)])
        self.assertAlmostEqual(got, 0.0)

    def test_partially_filled_cancelled_counts_only_the_fill(self):
        """部成后撤单：只有成交过的那部分算钱，余量必须放掉。"""
        got = self.spent([row("600000.SH", "FC600000", 10.0, 1000, traded=400.0,
                              terminal=True, cancel_like=False)])
        self.assertAlmostEqual(got, 4000.0)

    def test_filled_row_counts_the_traded_amount(self):
        """已成：按 成交量 × 委托价 计入（不含余量，余量本来就是 0）。"""
        got = self.spent([row("600000.SH", "FC600000", 10.0, 1000, traded=1000.0,
                              terminal=True, cancel_like=False)])
        self.assertAlmostEqual(got, 10000.0)

    def test_sell_rows_never_count_against_the_buy_budget(self):
        """卖出腿的钱不是买入预算：side=sell 一律不计。"""
        got = self.spent([row("600000.SH", "SL600000", 10.0, 1000, side="sell",
                              terminal=True, cancel_like=True)])
        self.assertAlmostEqual(got, 0.0)

    def test_side_missing_falls_back_to_the_baked_prefix_list(self):
        """老构建不暴露 side：按烘入的买入短码前缀兜底（前缀不认 ⇒ 不计）。"""
        r = row("600000.SH", "FC600000", 10.0, 1000)
        r.pop("side")
        self.assertAlmostEqual(self.spent([r]), 10000.0)
        other = row("600001.SH", "ZZ600001", 10.0, 1000)
        other.pop("side")
        self.assertAlmostEqual(self.spent([other]), 0.0)

    def test_journal_covers_symbols_the_counter_has_not_shown_yet(self):
        """柜台表慢几秒：刚发出去那一笔靠会话账占住预算（否则同一轮能再买一次）。"""
        self.mod._STATE["buy_amounts"] = {(_DAY, "600002.SH"): 8000.0}
        self.assertAlmostEqual(self.spent([]), 8000.0)

    def test_journal_is_not_double_counted_once_the_counter_shows_the_symbol(self):
        """柜台已看到该票 ⇒ 会话账不再叠加（同一笔钱只算一次）。"""
        self.mod._STATE["buy_amounts"] = {(_DAY, "600000.SH"): 10000.0}
        got = self.spent([row("600000.SH", "FC600000", 10.0, 1000)])
        self.assertAlmostEqual(got, 10000.0, msg="会话账与柜台真值被重复计入")

    def test_journal_from_another_day_is_ignored(self):
        """跨日遗留的会话账不能占今天的预算。"""
        self.mod._STATE["buy_amounts"] = {("20261027", "600003.SH"): 9000.0}
        self.assertAlmostEqual(self.spent([]), 0.0)


class ReconcileSessionIntentsTest(_SandboxBase):
    """B/C：手动撤单当轮释放，且只释放一次。"""

    def seed(self, symbol="600000.SH", short="FC600000", amount=10000.0):
        self.mod._STATE["sent"] = {(_DAY, symbol, short)}
        self.mod._STATE["buy_intents"] = {(_DAY, symbol)}
        self.mod._STATE["buy_amounts"] = {(_DAY, symbol): amount}

    def journals_empty(self):
        st = self.mod._STATE
        return (not st["sent"]) and (not st["buy_intents"]) and (not st["buy_amounts"])

    def test_manual_cancel_releases_all_three_journals(self):
        """柜台证明"该票今天全部已撤且零成交" ⇒ 三个会话账一并释放。"""
        self.seed()
        released = self.mod._reconcile_session_intents(
            [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                 cancel_like=True, order_id="OID-1")], _DAY)
        self.assertEqual(released, ["FC600000"])
        self.assertTrue(self.journals_empty(), "释放后不应还留着 %r" % (self.mod._STATE,))

    def test_no_release_without_a_counter_row(self):
        """柜台还没看到这只票 ⇒ 不许释放（表是秒级延迟的，放了就是重复下单）。"""
        self.seed()
        released = self.mod._reconcile_session_intents([], _DAY)
        self.assertEqual(released, [])
        self.assertFalse(self.journals_empty())

    def test_no_release_when_any_row_of_the_symbol_traded(self):
        """同一只票有一行成交过 ⇒ 意图不是死的，整只票的账都不许放。"""
        self.seed()
        released = self.mod._reconcile_session_intents([
            row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                cancel_like=True, order_id="OID-1"),
            row("600000.SH", "FC600000", 10.0, 500, traded=500.0, terminal=True,
                cancel_like=False, order_id="OID-2"),
        ], _DAY)
        self.assertEqual(released, [])
        self.assertFalse(self.journals_empty())

    def test_release_is_one_shot_per_order_id(self):
        """同一个已撤委托号只释放一次：新补的那一单不能被旧行再放一遍。"""
        self.seed()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                      cancel_like=True, order_id="OID-1")]
        self.assertEqual(self.mod._reconcile_session_intents(orders, _DAY), ["FC600000"])
        # 模拟"释放后同一轮/下一轮又发出去了一笔"（会话账重新记上）
        self.seed()
        self.assertEqual(self.mod._reconcile_session_intents(orders, _DAY), [],
                         "同一个已撤委托号把新补的意图又放了一次 ⇒ 同一只票会买两遍")
        self.assertFalse(self.journals_empty(), "第二轮不该再释放")

    def test_a_new_cancelled_order_releases_again(self):
        """第二次撤的是**另一张**委托 ⇒ 应当再释放一次（不是永久封锁）。"""
        self.seed()
        first = [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                     cancel_like=True, order_id="OID-1")]
        self.mod._reconcile_session_intents(first, _DAY)
        self.seed()
        second = [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                      cancel_like=True, order_id="OID-1"),
                  row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                      cancel_like=True, order_id="OID-2")]
        self.assertEqual(self.mod._reconcile_session_intents(second, _DAY), ["FC600000"])

    def test_release_set_rolls_over_with_the_trading_day(self):
        """跨日重置：昨天的委托号不能挡住今天的释放。"""
        self.seed()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                      cancel_like=True, order_id="OID-1")]
        self.mod._reconcile_session_intents(orders, _DAY)
        self.seed()
        self.mod._STATE["released_day"] = _DAY  # 仍是同一天：不释放
        self.assertEqual(self.mod._reconcile_session_intents(orders, _DAY), [])
        # 新的一天、同样的委托号：那是另一本账，必须释放
        self.mod._STATE["sent"] = {("20261029", "600000.SH", "FC600000")}
        self.mod._STATE["buy_intents"] = {("20261029", "600000.SH")}
        self.mod._STATE["buy_amounts"] = {("20261029", "600000.SH"): 10000.0}
        self.assertEqual(self.mod._reconcile_session_intents(orders, "20261029"), ["FC600000"])

    def test_idless_cancelled_rows_release_nothing(self):
        """认不出委托号的已撤行不能证明"这是新的一次撤单" ⇒ fail-closed。"""
        self.seed()
        released = self.mod._reconcile_session_intents(
            [row("600000.SH", "FC600000", 10.0, 1000, terminal=True,
                 cancel_like=True, order_id="")], _DAY)
        self.assertEqual(released, [])
        self.assertFalse(self.journals_empty())

    def test_sell_only_orders_do_not_release_a_buy_intent(self):
        """该票今天的行全是卖单时，"已撤"证明不了买单死没死（方向不匹配）。"""
        self.seed()
        released = self.mod._reconcile_session_intents(
            [row("600000.SH", "FC600000", 10.0, 1000, side="sell", terminal=True,
                 cancel_like=True, order_id="OID-1")], _DAY)
        self.assertEqual(released, [], "卖单的已撤不能释放买单账")
        self.assertFalse(self.journals_empty())


class StaleCancelTest(_SandboxBase):
    """A：十分钟自动撤单 + 一行可判读的读数。"""

    def setUp(self):
        base = _SandboxBase.setUp
        base(self)
        self.mod._SANDBOX_FNS.update({
            "get_trade_detail_data": lambda acct, kind, table:
                [object()] if table == "ACCOUNT" else [],
            "cancel": lambda *args: None,
        })
        self.mod.PARAMS["account"] = "2069008957"
        self.mod._STATE["round_moment"] = _MID_SESSION

    def run_cancel(self, orders):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            done = self.mod.cancel_leg(orders, _MID_SESSION)
        return done, buf.getvalue()

    def test_threshold_is_ten_minutes_not_fifteen(self):
        """烘入的陈旧阈值必须是 600 秒（现场曾错烘成 900 ⇒ 15 分钟才撤）。"""
        self.assertEqual(float(self.mod.PARAMS["stale_order_sec"]), 600.0,
                         "陈旧撤单阈值不是 10 分钟（烘参错档会让挂单一直不撤）")

    def test_open_order_under_threshold_is_kept_and_reported(self):
        """未到阈值：不撤，但**必须**留下一行读数（静默与"没有陈旧单"原本同形）。"""
        now = self.mod._now_ts()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, order_id="OID-1")]
        orders[0]["order_time"] = now - 100.0
        done, out = self.run_cancel(orders)
        self.assertEqual(done, [])
        self.assertIn("cancel_watch open=1", out)
        self.assertIn("threshold=600", out)
        self.assertIn("oldest_age=", out)

    def test_order_older_than_ten_minutes_is_cancelled(self):
        """超过 600 秒的在途单 ⇒ 真发撤单（owner 设计的"十分钟不成交自动撤"）。"""
        now = self.mod._now_ts()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, order_id="OID-1")]
        orders[0]["order_time"] = now - 601.0
        done, out = self.run_cancel(orders)
        self.assertEqual(done, ["OID-1"], "超过 10 分钟仍不撤 ⇒ 这条腿没牙：%s" % out)
        self.assertIn("OID-1", self.mod._STATE["cancel_tried"])

    def test_unknown_age_is_never_guessed_stale(self):
        """没有委托时间的行＝年龄未知，绝不能当成"够旧了"去撤。"""
        orders = [row("600000.SH", "FC600000", 10.0, 1000, order_id="OID-1")]
        orders[0]["order_time"] = None
        done, out = self.run_cancel(orders)
        self.assertEqual(done, [], "年龄未知却撤了单（fail-open）: %s" % out)

    def test_watch_line_is_one_per_hour(self):
        """每小时只打一行：这是读数，不是每轮刷屏。"""
        now = self.mod._now_ts()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, order_id="OID-1")]
        orders[0]["order_time"] = now - 100.0
        _, first = self.run_cancel(orders)
        _, second = self.run_cancel(orders)
        self.assertIn("cancel_watch", first)
        self.assertNotIn("cancel_watch", second, "同一小时内重复打读数行")

    def test_cancelled_journal_is_dropped_on_auto_cancel(self):
        """自动撤单同样要放开该意图（否则撤了也补不回来）。"""
        self.mod._STATE["sent"] = {(_DAY, "600000.SH", "FC600000")}
        now = self.mod._now_ts()
        orders = [row("600000.SH", "FC600000", 10.0, 1000, order_id="OID-1")]
        orders[0]["order_time"] = now - 700.0
        done, _ = self.run_cancel(orders)
        self.assertEqual(done, ["OID-1"])
        self.assertEqual(self.mod._STATE["sent"], set())


if __name__ == "__main__":
    unittest.main()
