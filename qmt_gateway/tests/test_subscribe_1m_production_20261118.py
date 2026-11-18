# -*- coding: utf-8 -*-
"""2026-11-18 A-ROUTE 生产 1m 订阅的行为锁（_subscribe_1m_held）。

背景：分钟腿（afternoon_reseal 断板反包保护、做 T）读 get_market_data_ex(...,"1m",...)，
但本地 1m 仓几乎空、此 build 无下载器。11-18 探针 _probe_subscribe_1m 实证：
带后缀 "1m" 订阅后 mde_1m=5>0 —— 客户端 bar 引擎会聚合并存 1m bar。
本批把该姿势扶正到生产：新增 _subscribe_1m_held，对持仓票订阅 1m，与 tick 并存。

现有 _subscribe_held 的 shape 循环 (tick->tick->1m) 成功即 break，1m 永远轮不到；
且 §204 门禁锁死那一行（价格盲锚点），故不碰它，改为独立函数。

每条锁配塞雷反证：摘订阅/改裸码/去掉按日账本/放行下单，对应断言必红。
"""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")


def _build_sandbox(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                           "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, (proc.stdout + proc.stderr), path


class _SubCtx(object):
    """ContextInfo stub recording subscribe_quote calls; no trading methods."""

    def __init__(self, fail=False, ret=21):
        self.quote_calls = []
        self.fail = fail

    def subscribe_quote(self, code, period=""):
        self.quote_calls.append((code, period))
        if self.fail:
            raise RuntimeError("subscribe boom")
        return 21


class Subscribe1MProduction(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        out = tempfile.mkdtemp()
        rc, log, path = _build_sandbox(out)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % log[-800:])
        spec = importlib.util.spec_from_file_location("sub1m_artifact", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def setUp(self):
        self.mod._STATE.pop("subbed_1m_today", None)

    def _positions(self, codes=("000417", "600580")):
        return dict((c, {"qty": 1000.0}) for c in codes)

    # ---- 1. subscribes the SUFFIXED 1m period for each held name -------------
    def test_1m_subscribe_uses_suffixed_code_and_minute_period(self):
        ctx = _SubCtx()
        self.mod._subscribe_1m_held(ctx, self._positions())
        self.assertIn(("000417.SZ", "1m"), ctx.quote_calls,
                      "must subscribe the suffixed code on the 1m period")
        self.assertIn(("600580.SH", "1m"), ctx.quote_calls)
        self.assertTrue(all(per == "1m" for _c, per in ctx.quote_calls),
                        "no tick period here -- tick is the other function's job")

    # ---- 2. zero-qty rows are skipped (sell legs only care about holdings) ---
    def test_zero_qty_rows_are_not_subscribed(self):
        ctx = _SubCtx()
        self.mod._subscribe_1m_held(ctx, {"000417": {"qty": 0.0},
                                          "600580": {"qty": 500.0}})
        self.assertFalse(any(c == "000417.SZ" for c, _p in ctx.quote_calls))
        self.assertIn(("600580.SH", "1m"), ctx.quote_calls)

    # ---- 3. idempotent per day: a second call makes no new subscribe --------
    def test_second_call_same_day_is_a_no_op(self):
        ctx = _SubCtx()
        self.mod._subscribe_1m_held(ctx, self._positions())
        first = list(ctx.quote_calls)
        self.mod._subscribe_1m_held(ctx, self._positions())
        self.assertEqual(ctx.quote_calls, first,
                         "ledger must prevent a per-round subscribe storm")

    # ---- 4. empty/None positions is a no-op ---------------------------------
    def test_empty_positions_is_no_op(self):
        for pos in (None, {}, {"000417": None}):
            ctx = _SubCtx()
            self.mod._subscribe_1m_held(ctx, pos)
            self.assertEqual(ctx.quote_calls, [])

    # ---- 5. a subscribe API that raises must not take down the round --------
    def test_subscribe_exception_is_swallowed(self):
        ctx = _SubCtx(fail=True)
        self.mod._subscribe_1m_held(ctx, self._positions())  # must not raise

    # ---- 6. ledger keys stay BARE (joins must keep working) ------------------
    def test_ledger_keys_are_bare_symbols(self):
        self.mod._subscribe_1m_held(_SubCtx(), self._positions())
        ledger = self.mod._STATE.get("subbed_1m_today", {})
        self.assertIn("000417", ledger)
        self.assertIn("600580", ledger)
        self.assertNotIn("000417.SZ", ledger,
                         "suffixed key would break pos_view/de-dup joins")

    # ---- 7. read-only: no trading method touched, source has no order call --
    def test_source_has_no_order_call(self):
        import inspect
        src = inspect.getsource(self.mod._subscribe_1m_held)
        for bad in ("passorder", "cancel", "_place", "order_share", "m_dAvailable"):
            self.assertNotIn(bad, src, "subscribe fn must never carry a trade call")

    # ---- 8. run_round actually calls it (not a dead helper) ------------------
    def test_run_round_wires_the_1m_subscribe(self):
        import inspect
        src = inspect.getsource(self.mod.run_round)
        self.assertIn("_subscribe_1m_held(ContextInfo, positions)", src,
                      "the new subscribe must be called from run_round")


if __name__ == "__main__":
    unittest.main(verbosity=2)
