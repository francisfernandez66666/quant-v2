# -*- coding: utf-8 -*-
"""2026-11-18 整池批取价（A-ROUTE 快路径）的行为锁。

批取价把每轮 ~1000 次逐票 get_full_tick（~45s）压成一次批取（~1s）。探针实证：
裸码 universe 批取只回 48 只（带点的），必须喂全后缀码、再 rekey 回裸码。

fail-closed 是本批的命门：批取失败/空/异常时快照为空，_tick_of 对每只票回退到
原有逐票读（§204 锁死那行），价格链绝不比批取之前更差。每条锁配塞雷反证。
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


def _raw_tick(price=10.0):
    return {"lastPrice": price, "lastClose": price, "open": price,
            "high": price, "low": price, "volume": 1000, "amount": price * 1000}


class _BatchCtx(object):
    """ContextInfo stub. A batch is any call asking for >1 code (the real
    universe is ~1000); a fallback read asks for exactly one. Tests that need a
    1-symbol batch pass a 2-name universe so the batch call stays >1."""

    def __init__(self, batch=None, fail=False, calls=None):
        self.batch = batch if batch is not None else {}
        self.fail = fail
        self.calls = calls  # shared list to count per-symbol fallback calls

    def get_full_tick(self, codes):
        codes = list(codes)
        if len(codes) > 1:
            if self.fail:
                raise RuntimeError("batch boom")
            return dict((c, self.batch[c]) for c in codes if c in self.batch)
        # single-symbol fallback path: only 000417 answers, at price 9.0
        if self.calls is not None:
            self.calls.append(codes[0])
        return {"000417.SZ": _raw_tick(9.0)} if codes[0] in ("000417", "000417.SZ") else {}


class BatchTicks20261118(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        out = tempfile.mkdtemp()
        rc, log, path = _build_sandbox(out)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % log[-800:])
        spec = importlib.util.spec_from_file_location("batch_artifact", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def setUp(self):
        self.mod._STATE.pop("tick_snapshot", None)
        self.mod._STATE.pop("tick_snapshot_round", None)
        self.mod._STATE["round_moment"] = "R1"

    # ---- 1. suffixed batch, rekeyed back to BARE symbol ----------------------
    def test_batch_feeds_suffixed_codes_and_rekeys_to_bare(self):
        ctx = _BatchCtx(batch={"000417.SZ": _raw_tick(11.0),
                               "600580.SH": _raw_tick(22.0)})
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        snap = self.mod._STATE.get("tick_snapshot", {})
        self.assertIn("000417", snap, "snapshot must be keyed by BARE symbol")
        self.assertIn("600580", snap)
        self.assertNotIn("000417.SZ", snap, "suffixed key would break ctx joins")
        self.assertEqual(snap["000417"]["last_price"], 11.0)

    # ---- 2. snapshot hit avoids a per-symbol read ----------------------------
    def test_snapshot_hit_short_circuits_per_symbol_read(self):
        calls = []
        ctx = _BatchCtx(batch={"000417.SZ": _raw_tick(11.0)}, calls=calls)
        # 2 names -> batch call is >1 code; only 000417 answers in the batch
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        tick = self.mod._tick_of(ctx, "000417")
        self.assertEqual(tick["last_price"], 11.0)
        self.assertEqual(calls, [], "no per-symbol fallback when snapshot hits")

    # ---- 3. FAIL-CLOSED: batch fails -> empty snapshot -> per-symbol fallback -
    def test_batch_failure_falls_back_to_per_symbol(self):
        calls = []
        ctx = _BatchCtx(fail=True, calls=calls)
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        self.assertFalse(self.mod._STATE.get("tick_snapshot"),
                         "failed batch must leave no usable snapshot")
        tick = self.mod._tick_of(ctx, "000417")  # falls back to per-symbol
        self.assertEqual(tick["last_price"], 9.0)
        self.assertIn("000417.SZ", calls)  # per-symbol path uses SUFFIXED code

    # ---- 4. FAIL-CLOSED: empty batch payload -> snapshot empty ---------------
    def test_empty_batch_payload_leaves_snapshot_empty(self):
        ctx = _BatchCtx(batch={})
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        self.assertFalse(self.mod._STATE.get("tick_snapshot"))

    # ---- 5. partial batch: miss falls through to per-symbol ------------------
    def test_partial_batch_miss_uses_per_symbol(self):
        calls = []
        ctx = _BatchCtx(batch={"000417.SZ": _raw_tick(11.0)}, calls=calls)
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        self.assertIn("000417", self.mod._STATE["tick_snapshot"])
        self.assertNotIn("600580", self.mod._STATE["tick_snapshot"])
        self.mod._tick_of(ctx, "600580")  # miss -> per-symbol (suffixed code)
        self.assertIn("600580.SH", calls)

    # ---- 6. stale snapshot from a prior round is ignored ---------------------
    def test_stale_round_snapshot_is_not_served(self):
        calls = []
        ctx = _BatchCtx(batch={"000417.SZ": _raw_tick(11.0)}, calls=calls)
        self.mod._batch_ticks(ctx, ["000417", "600580"], {})
        self.mod._STATE["round_moment"] = "R2"  # new round, no re-batch
        tick = self.mod._tick_of(ctx, "000417")
        self.assertEqual(tick["last_price"], 9.0, "must not serve R1's batch in R2")
        self.assertIn("000417.SZ", calls)

    # ---- 7. no symbols/positions -> no batch, no crash ----------------------
    def test_empty_universe_is_no_op(self):
        ctx = _BatchCtx(batch={"000417.SZ": _raw_tick(11.0)})
        self.mod._batch_ticks(ctx, [], {})
        self.assertNotIn("tick_snapshot", self.mod._STATE)

    # ---- 8. read-only: no order call in either new function ------------------
    def test_source_has_no_order_call(self):
        import inspect
        for name in ("_batch_ticks",):
            src = inspect.getsource(getattr(self.mod, name))
            for bad in ("passorder", "_place(", "order_share", "cancel_leg"):
                self.assertNotIn(bad, src)

    # ---- 9. run_round actually primes the batch ------------------------------
    def test_run_round_primes_batch(self):
        import inspect
        src = inspect.getsource(self.mod.run_round)
        self.assertIn("_batch_ticks(ContextInfo, symbols, positions)", src)


if __name__ == "__main__":
    unittest.main(verbosity=2)
