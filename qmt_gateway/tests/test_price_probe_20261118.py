# -*- coding: utf-8 -*-
"""2026-11-18 价格盲定性探针（_probe_price_channels）的行为锁。

靶子（2026-11-18 盘中实录）：
  round_skips total=859 no_price=770
  sell_blind held_without_price=2 total_held=2
⇒ 载体一只票都看不到价，持仓两行全盲，买入链死在 rule_fillable、三条卖出腿全哑。
本批只加"哪条取价通道还活着"的只读取证线，改价路径要等这条线的读数。

每条锁都配一个"故意破坏必须红"的形态：
  ① 批取价那条丢掉 -> batch 行不再出现
  ② 探针长出下单动作 -> 静态负锁（probe 函数体里不许出现 passorder/_place/cancel）
  ③ 探针不设上限 -> 单票调用次数会超过 4
  ④ 每轮都探 -> 5 轮里 summary 行数会变 5
  ⑤ 任一条通道抛异常就把整轮带走 -> broken-ctx 用例会炸

英文：behaviour locks for the read-only price probe; it must be bounded, must
never place/cancel, and must survive a client where every read shape raises.
"""
import contextlib
import importlib.util
import io
import os
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

_UNIVERSE = ["000050", "000070", "600519.SH", "000002", "600036.SH"]
_HELD = "000417"


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


class _ProbeCtx(object):
    """ContextInfo stub that records every read shape the probe uses."""

    def __init__(self, ticks=None, batch_ticks=None, raise_tick=False,
                 raise_mde=False, with_subscribe=True, mde_rows=0):
        self.ticks = ticks or {}
        self.batch = batch_ticks if batch_ticks is not None else self.ticks
        self.raise_tick = raise_tick
        self.raise_mde = raise_mde
        self.mde_rows = mde_rows
        self.single_calls = []
        self.batch_calls = []
        self.mde_calls = []
        self.quote_calls = []
        if with_subscribe:
            self.subscribe_quote = lambda code, period="": (
                self.quote_calls.append((code, period)), 21)[1]

    def get_full_tick(self, codes):
        codes = list(codes)
        if len(codes) > 1:
            self.batch_calls.append(codes)
        else:
            self.single_calls.append(codes[0])
        if self.raise_tick:
            raise RuntimeError("tick boom")
        source = self.batch if len(codes) > 1 else self.ticks
        return dict((c, source[c]) for c in codes if c in source)

    def get_market_data_ex(self, fields, codes, period="", count=1, **kw):
        self.mde_calls.append((codes[0], period))
        if self.raise_mde:
            raise TypeError("bad signature")
        rows = [{"close": 10.0 + i} for i in range(self.mde_rows)]
        return {codes[0]: {"close": [float(10 + i) for i in range(self.mde_rows)]}} if rows else {}


class PriceProbe20261118(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        out = tempfile.mkdtemp()
        rc, log, path = _build_sandbox(out)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % log[-800:])
        spec = importlib.util.spec_from_file_location("probe_artifact", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def setUp(self):
        self.mod.PARAMS["symbols"] = list(_UNIVERSE)
        self.mod._STATE["probe_seq"] = 0
        self.mod._STATE.pop("probe_subbed", None)
        # the 1m ledger is per-day/idempotent: a sibling test that already
        # subscribed turns the next call into a cache hit, so the assertion
        # would fail on a correct implementation.
        self.mod._STATE.pop("probe_1m", None)

    # ---- helper: run the probe and capture the lines it printed --------------
    def _probe(self, ctx, positions=None):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.mod._probe_price_channels(
                ctx, positions if positions is not None else {_HELD: {"qty": 1000.0}})
        return [ln for ln in buf.getvalue().splitlines() if "tick_probe" in ln]

    def _rounds(self, ctx, n):
        """Call the probe n times (the cadence is internal, so drive it directly)."""
        lines = []
        for _ in range(n):
            lines.extend(self._probe(ctx))
        return lines

    # ---- 1. batch shape: one call for the whole pool, its hit count printed ----
    def test_batch_line_reports_how_many_of_the_pool_came_back(self):
        ctx = _ProbeCtx(ticks={_HELD: _tick()},
                        batch_ticks={_UNIVERSE[0]: _tick(), _UNIVERSE[2]: _tick()})
        lines = self._probe(ctx)
        batch = [ln for ln in lines if " batch " in ln]
        self.assertEqual(len(batch), 1, "the batched read must produce exactly one receipt line")
        self.assertIn("asked=%d" % len(_UNIVERSE), batch[0])
        self.assertIn("got=2", batch[0])
        self.assertIn("sample=", batch[0],
                      "the batch line must name a few returned keys: that is the format receipt")
        self.assertEqual(len(ctx.batch_calls), 1, "the pool must be asked in ONE call, not per symbol")
        self.assertEqual(len(ctx.batch_calls[0]), len(_UNIVERSE))

    # ---- 2. per-symbol shapes + a subscription probe ---------------------------
    def test_per_symbol_line_names_every_shape_it_tried(self):
        ctx = _ProbeCtx(ticks={_HELD: _tick()}, batch_ticks={})
        lines = self._probe(ctx)
        # The client truncates a printed line at ~150 chars, so the shapes are
        # split over exactly two lines; join them before checking every key.
        rows = [ln for ln in lines if " sym=%s " % _HELD in ln]
        self.assertEqual(len(rows), 2, "two lines per symbol: price shapes, then bar shapes")
        row = [" ".join(rows)]
        for piece in ("one=1", "one_sfx=", "sub=", "sfx=", "sub_tick=", "sfx_tick=",
                      "mde_tick=", "mde_1d=", "d1d_sfx=", "mde_1m=", "sub1m=",
                      "mde_1m_after="):
            self.assertIn(piece, row[0])
        # the tick subscription is what the evidence pointed at: period must be tick
        self.assertIn((_HELD, "tick"), ctx.quote_calls,
                      "the probe must ask for TICK quotes, not the 1m shape used by _subscribe_held")
        # ...and it must ask for the SUFFIXED code too: the pool is bare 6-digit.
        self.assertIn((_HELD + ".SZ", "tick"), ctx.quote_calls,
                      "the probe must also subscribe the CODE.SZ form -- the pool carries bare codes")

    # ---- 2a-bis. the MINUTE leg: subscribe 1m, let the client aggregate -------
    def test_minute_period_is_subscribed_so_the_client_can_aggregate(self):
        """2026-11-18: the local 1m store is empty and this build has no
        downloader, but 1m bars never needed one -- the client aggregates the
        ticks it pushes. The suffixed "1m" subscribe has never been exercised
        (11-18 made "tick" primary; the old bare-code "1m" returned id 0). This
        lock exists so the reading cannot be dropped before the minute legs
        (afternoon reseal / T) are settled either way.
        """
        ctx = _ProbeCtx(ticks={_HELD + ".SZ": _tick()}, batch_ticks={})
        lines = self._probe(ctx)
        row = " ".join([ln for ln in lines if " sym=%s " % _HELD in ln])
        self.assertIn((_HELD + ".SZ", "1m"), ctx.quote_calls,
                      "the probe must subscribe the suffixed 1m period")
        self.assertIn("sub1m=", row)
        self.assertIn("mde_1m=", row)

    # ---- 2b. THE SUSPECT: only the dotted form answers ------------------------
    def test_suffixed_variant_is_the_one_that_answers(self):
        """2026-11-18: pool=1000 codes, only 52 carry a dot, batch got=48.

        Shape the box showed: bare read 0, CODE.SZ read 1. This lock exists so
        the "codes are missing a market suffix" diagnosis cannot be quietly
        forgotten before the real fix lands.
        """
        ctx = _ProbeCtx(ticks={_HELD + ".SZ": _tick()}, batch_ticks={})
        lines = self._probe(ctx)
        row = " ".join([ln for ln in lines if " sym=%s " % _HELD in ln])
        self.assertIn("one=0", row)
        self.assertIn("one_sfx=1", row)
        self.assertIn("priced=1", [ln for ln in lines if "summary" in ln][0])

    # ---- 3. bounded: never more than four single-symbol reads per probe -------
    def test_probe_is_bounded_to_four_symbols(self):
        ctx = _ProbeCtx(ticks={_HELD: _tick()}, batch_ticks={})
        self.mod.PARAMS["symbols"] = list(_UNIVERSE) + ["601988.SH", "000651.SZ"]
        lines = self._probe(ctx)
        self.assertLessEqual(len(ctx.single_calls), 4 * 4,
                             "4 symbols x (bare+dotted) x (before/after subscribe); a "
                             "bigger number means the probe can slow the round down")
        named = set(ln.split(" sym=")[1].split()[0] for ln in lines if " sym=" in ln)
        self.assertIn(_HELD, named, "held rows are probed first")

    # ---- 4. cadence: one probe every _PROBE_ROUND_EVERY rounds ---------------
    def test_probe_runs_once_every_fifth_round(self):
        ctx = _ProbeCtx(ticks={_HELD: _tick()}, batch_ticks={})
        lines = self._rounds(ctx, 5)
        summaries = [ln for ln in lines if "summary" in ln]
        self.assertEqual(len(summaries), 1, "probing every round would bury the log")
        lines = self._rounds(ctx, 5)
        self.assertEqual(len([ln for ln in lines if "summary" in ln]), 1,
                         "the sixth round starts the next probe")

    # ---- 5. a client where every shape raises must not take the round down ---
    def test_broken_client_is_reported_not_raised(self):
        ctx = _ProbeCtx(ticks={}, batch_ticks={}, raise_tick=True,
                        raise_mde=True, with_subscribe=False)
        lines = self._probe(ctx)
        self.assertTrue(any("batch raised=" in ln for ln in lines),
                        "a raising batch read must be named, not swallowed")
        row = " ".join([ln for ln in lines if " sym=%s " % _HELD in ln])
        self.assertTrue(row)
        self.assertIn("mde_tick=-1", row, "a rejected signature is the answer: print -1")
        self.assertIn("sub=api_absent", row, "no subscribe fn must read as api_absent")

    # ---- 6. static negative lock: the probe can never trade ------------------
    def test_probe_source_contains_no_trade_call(self):
        text = open(self.mod.__file__, encoding="utf-8", errors="replace").read()
        start = text.index("_PROBE_ROUND_EVERY = ")
        end = text.index("def _download_day_bars(")
        body = text[start:end]
        for banned in ("passorder", "_place(", "cancel(", "_STATE[\"sent\"]",
                       "buy_intents", "buy_amounts"):
            self.assertNotIn(banned, body,
                             "probe body must stay read-only; found %r" % (banned,))

    # ---- 7. an unpriced universe reports priced=0 (today's live shape) ------
    def test_all_blind_universe_reports_zero_priced(self):
        ctx = _ProbeCtx(ticks={}, batch_ticks={})
        lines = self._probe(ctx)
        summary = [ln for ln in lines if "summary" in ln]
        self.assertEqual(len(summary), 1)
        self.assertIn("priced=0", summary[0],
                      "today's live state: nothing priced, and the line must say so")


if __name__ == "__main__":
    unittest.main()
