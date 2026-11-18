# -*- coding: utf-8 -*-
"""2026-11-18 裸码→带后缀（_qmt_code）的行为锁。

根因实录（v79 探针在盒子上的读数，逐字）：
  tick_probe batch asked=1000 got=48 sample=000417.SZ|000520.SZ|000530.SZ|000533.SZ
  tick_probe sym=000417 one=0 one_sfx=1 sub=0 sfx=186 sub_tick=0 sfx_tick=1
  tick_probe summary asked=4 priced=4
⇒ 池 1000 只里 948 只是裸 6 位码，客户端一条都不回；带 `CODE.SZ` 立刻有价。
   两只持仓也是裸码 ⇒ `sell_blind held_without_price=2 total_held=2`（三条卖出腿全盲），
   买入侧 `no_price=770` 死在 rule_fillable —— 今天一笔都做不出去的全部原因。

每条锁都配一个"破坏必须红"的形态：
  ① 后缀映射退化成原样返回 -> 单票读价用例与整轮买入用例同时红
  ② _tick_of 只认裸键 -> "客户端回显带后缀键"的用例红（真实客户端的回法）
  ③ 订阅还用裸码 -> subscribe 断言红
  ④ 读历史不 re-key -> 历史序列读成空（同样的盲，换了个位置）
  ⑤ 键被顺手改带后缀 -> pos_view/去重集合的键必须仍是裸码（负锁）

英文：behaviour locks for the 2026-11-18 root cause -- the client resolves only
'CODE.SH/.SZ/.BJ'; the pool and the counter rows carry bare 6-digit codes.
"""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

_MOMENT = datetime(2026, 11, 18, 10, 30, 0)
_HELD = "000417"
_UNIVERSE = ["000417", "000050", "600519.SH"]


def _build_sandbox(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                           "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, (proc.stdout + proc.stderr), path


def _tick(price=10.0):
    return {"lastPrice": price, "lastClose": price, "open": price,
            "high": price, "low": price, "volume": 100000,
            "amount": price * 100000.0,
            "askPrice": [price + 0.01], "bidPrice": [price]}


class _SuffixOnlyCtx(object):
    """ContextInfo stub that answers ONLY the dotted codes (the real behaviour)."""

    def __init__(self, ticks=None, echo_bare=False, minute_rows=0):
        self.ticks = ticks if ticks is not None else {_HELD + ".SZ": _tick()}
        self.echo_bare = echo_bare
        self.minute_rows = minute_rows
        self.tick_calls = []
        self.sub_calls = []
        self.mde_calls = []
        self.subscribe_quote = lambda code, *a: (self.sub_calls.append(code), 186)[1]

    def get_full_tick(self, codes):
        self.tick_calls.append(list(codes))
        out = {}
        for code in codes:
            if code in self.ticks:
                key = code.split(".")[0] if self.echo_bare else code
                out[key] = self.ticks[code]
        return out

    def get_market_data_ex(self, fields, codes, period="", count=1, **kw):
        code = codes[0]
        self.mde_calls.append(code)
        if code not in self.ticks:
            return {}
        closes = [float(10 + i) for i in range(self.minute_rows)]
        return {code: {"close": closes, "volume": [100.0] * len(closes)}}


class _Row(object):
    def __init__(self, **fields):
        for k, v in fields.items():
            setattr(self, k, v)


class PriceCodeSuffix20261118(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        out = tempfile.mkdtemp()
        rc, log, path = _build_sandbox(out)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % log[-800:])
        spec = importlib.util.spec_from_file_location("sfx_artifact", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def setUp(self):
        self.mod.PARAMS["symbols"] = list(_UNIVERSE)
        self.mod._STATE["probe_seq"] = 0
        self.mod._STATE.pop("subbed_today", None)
        self.mod._STATE["warned"] = {}

    # ---- ① the mapping itself ------------------------------------------------
    def test_bare_code_gets_its_market_suffix(self):
        code = self.mod._qmt_code
        self.assertEqual(code("000417"), "000417.SZ")
        self.assertEqual(code("300750"), "300750.SZ")
        self.assertEqual(code("600519"), "600519.SH")
        self.assertEqual(code("900901"), "900901.SH")
        self.assertEqual(code("830799"), "830799.BJ")
        self.assertEqual(code("000417.SZ"), "000417.SZ", "an already-suffixed code is untouched")
        self.assertEqual(code(""), "")

    # ---- ② the price read: the defect this batch fixes -----------------------
    def test_tick_of_reads_the_suffixed_code_and_costs_nothing_else(self):
        ctx = _SuffixOnlyCtx()
        tick = self.mod._tick_of(ctx, _HELD)
        self.assertAlmostEqual(tick["last_price"], 10.0)
        self.assertEqual(ctx.tick_calls[0], [_HELD + ".SZ"],
                         "the client must be asked with the dotted code")
        self.assertEqual(tick["symbol"], _HELD,
                         "the tick's identity key stays BARE (joins depend on it)")

    def test_tick_of_accepts_a_client_that_echoes_the_bare_key(self):
        ctx = _SuffixOnlyCtx(echo_bare=True)
        tick = self.mod._tick_of(ctx, _HELD)
        self.assertAlmostEqual(tick["last_price"], 10.0,
                              "a build that normalises keys back must still be read")

    # ---- ③ the subscription: suffixed code, tick period ----------------------
    def test_subscribe_held_uses_the_suffixed_code(self):
        ctx = _SuffixOnlyCtx()
        self.mod._subscribe_held(ctx, {_HELD: {"qty": 1000.0}})
        self.assertIn(_HELD + ".SZ", ctx.sub_calls,
                      "subscribing the bare code returns id 0 and delivers nothing")
        self.assertEqual(self.mod._STATE["subbed_today"].get(_HELD), "186",
                         "the day-dedup ledger stays keyed by the BARE symbol")

    # ---- ④ history payloads are re-keyed to bare ----------------------------
    def test_market_data_payload_is_rekeyed_to_the_bare_symbol(self):
        ctx = _SuffixOnlyCtx(minute_rows=3)
        payload = self.mod._hist_bars(ctx, _HELD)
        self.assertIn(_HELD, payload, "callers look the payload up by the bare code")
        series = self.mod._series(payload, "close", _HELD)
        self.assertEqual(len(series), 3, "a re-key miss reads as 'no history'")
        self.assertEqual(ctx.mde_calls[0], _HELD + ".SZ")

    # ---- ④b acceptance: the exact 11-18 shape now reaches the buy chain -----
    def test_build_context_prices_the_pool_symbol_and_passes_fillable(self):
        """Before the fix this ctx carried last_price=None -> `no_price`, 770 times."""
        ctx = _SuffixOnlyCtx(ticks={_HELD + ".SZ": _tick()})
        held = {_HELD: {"qty": 1000.0, "cost": 10.0, "can_use": 1000.0,
                        "market_value": 10000.0}}
        built = self.mod.build_context(ctx, _HELD, _MOMENT, [], held, 100000.0)
        self.assertAlmostEqual(built["last_price"], 10.0,
                               "build_context must see the price the client has")
        ok, why = self.mod.rule_fillable(self.mod.PARAMS, built, "buy")
        self.assertTrue(ok, "rule_fillable refused a priced symbol: %s" % (why,))

    # ---- ⑤ negative: the booking keys must NOT gain a suffix ----------------
    def test_position_keys_stay_bare(self):
        def counter(acct, kind, table):
            if table == "POSITION":
                return [_Row(m_strInstrumentID=_HELD, m_nVolume=1000, m_dOpenPrice=10.0,
                             m_nCanUseVolume=1000, m_dMarketValue=10000.0)]
            if table == "ACCOUNT":
                return [_Row(m_dAvailable=100000.0, m_dBalance=200000.0)]
            return []

        self.mod._SANDBOX_FNS.update({
            "get_trade_detail_data": counter,
            "get_account": lambda *a: "8001",
            "passorder": lambda *a: None,
            "cancel": lambda *a: None})
        self.mod._STATE.update({"acct": "8001", "acct_type": ""})
        rows = self.mod._positions()
        self.assertIn(_HELD, rows, "pos_view keys are bare; suffixing them breaks every join")
        self.assertNotIn(_HELD + ".SZ", rows)


if __name__ == "__main__":
    unittest.main()
