#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""test_live_t_enable_20261116.py — 生产 QMT 做 T 接线的行为锁（2026-11-16）。

为什么这些锁必须存在
  做 T 的 T 状态机（rules_t.py）虽然内联进产物，但此前从主循环调用次数为 0——
  "代码在、腿不走"是本仓反复点名的最贵形态（声称已做）。本文件锁的是：
  1. run_round 真的调用了 _run_t（接线在位，不是死代码）；
  2. 双闸（t_enabled + owner 装填的短码）任一关闭，T 层 no-op，绝不碰柜台；
  3. 缺可证锚点（外脑没烘 t_anchor_book）时 no-op（step0 锚点门 fail-closed）；
  4. 宏观读数过期时 no-op（不用旧行情做 T）；
  5. T 状态机符号齐全、_vwap_of 命名冲突已消除（T 用 _t_vwap_of 标量、战略用 _vwap_of tuple）；
  6. 产物 g7 可 load（缺 import/名字会在第一根 bar 崩）。

English: behaviour locks for wiring the live T layer into the QMT sandbox round.
"""

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
sys.path.insert(0, GW_DIR)

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")


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


class FakeContext(object):
    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return {s: dict(self._ticks.get(s, {})) for s in symbols}

    def get_market_data_ex(self, *a, **k):
        return {}


class LiveTWiringTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: " + out[-1000:])
        cls.artifact = path
        cls.mod = load_module(path, "live_t_artifact_under_test")
        cls.src = open(path, encoding="utf-8").read()

    def test_01_run_round_calls_run_t(self):
        """接线锁：run_round 的函数体里必须真的调用 _run_t。"""
        import ast
        tree = ast.parse(self.src)
        fn = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "run_round")
        called = {n.func.id for n in ast.walk(fn)
                  if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)}
        self.assertIn("_run_t", called, "run_round 没有调用 _run_t —— T 层又是死代码")

    def test_02_t_state_machine_symbols_inlined(self):
        for name in ("_run_t", "_apply_t_action", "TRoundBook", "t_round_state",
                     "t_gate_open", "t_open_qty", "_t_vwap_of"):
            self.assertIn(name, self.mod.__dict__, "产物缺少符号 " + name)

    def test_03_vwap_name_collision_removed(self):
        """T 用 _t_vwap_of(ctx) 标量；战略腿用 _vwap_of(tick) tuple。两者不得混用。"""
        src = self.src
        # t_gate_open 内不得再出现 _vwap_of(ctx)
        import ast
        tree = ast.parse(src)
        fn = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "t_gate_open")
        seg = ast.get_source_segment(src, fn) or ""
        # 用 AST 精确找调用名，避免子串误判（_t_vwap_of 含 _vwap_of）
        called = {n.func.id for n in ast.walk(fn)
                  if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)}
        self.assertNotIn("_vwap_of", called, "t_gate_open 仍在调用会返回 tuple 的 _vwap_of")
        self.assertIn("_t_vwap_of", called, "t_gate_open 应改用标量版 _t_vwap_of")

    def test_04_double_gate_noop_when_disabled(self):
        """t_enabled=false 时 _run_t 直接返回，不产生任何 t_round 日志。"""
        mod = self.mod
        old = mod.PARAMS.get("t_enabled")
        mod.PARAMS["t_enabled"] = False
        summary = {"placed": [], "cancelled": [], "skipped": [], "reason": ""}
        try:
            mod._run_t(FakeContext({}), datetime(2026, 11, 16, 10, 30),
                       [], {}, [], 0.0, 0.0, "20261116", summary)
            self.assertEqual(summary["placed"], [])
            self.assertEqual(summary["cancelled"], [])
        finally:
            mod.PARAMS["t_enabled"] = old

    def test_05_noop_when_no_anchor_book(self):
        """外脑没烘锚点 -> 持仓无可证 anchor -> no-op（fail-closed）。"""
        mod = self.mod
        old_a, old_s = mod.PARAMS.get("t_anchor_book"), mod.PARAMS.get("t_signal_codes")
        mod.PARAMS["t_enabled"] = True
        mod.PARAMS["t_signal_codes"] = {"t_buy_first": "TB"}
        mod.PARAMS["t_anchor_book"] = {}  # 空表
        summary = {"placed": [], "cancelled": [], "skipped": [], "reason": ""}
        try:
            mod._run_t(FakeContext({}), datetime(2026, 11, 16, 10, 30),
                       [], {"600000.SH": {"qty": 1000.0, "cost": 10.0}},
                       [], 100000.0, 100000.0, "20261116", summary)
            self.assertEqual(summary["placed"], [], "无锚点却下了 T 单")
        finally:
            mod.PARAMS["t_anchor_book"] = old_a
            mod.PARAMS["t_signal_codes"] = old_s

    def test_06_noop_when_macro_stale(self):
        """宏观读数过期 -> no-op（不用旧行情做 T）。"""
        mod = self.mod
        old_a, old_m, old_s = (mod.PARAMS.get("t_anchor_book"),
                              mod.PARAMS.get("t_macro"),
                              mod.PARAMS.get("t_signal_codes"))
        mod.PARAMS["t_enabled"] = True
        mod.PARAMS["t_signal_codes"] = {"t_buy_first": "TB"}
        mod.PARAMS["t_anchor_book"] = {"600000": {"anchor_price": 10.0, "anchor_source": "first_buy_order"}}
        mod.PARAMS["t_macro"] = {"market_scale": 0.7, "asof_age_sec": 99999}  # 过期
        summary = {"placed": [], "cancelled": [], "skipped": [], "reason": ""}
        try:
            mod._run_t(FakeContext({}), datetime(2026, 11, 16, 10, 30),
                       [], {"600000.SH": {"qty": 1000.0, "cost": 10.0}},
                       [], 100000.0, 100000.0, "20261116", summary)
            self.assertEqual(summary["placed"], [], "过期宏观却下了 T 单")
        finally:
            mod.PARAMS["t_anchor_book"] = old_a
            mod.PARAMS["t_macro"] = old_m
            mod.PARAMS["t_signal_codes"] = old_s

    def test_07_armed_with_anchor_and_macro_does_not_crash(self):
        """锚点+宏观+持仓齐备时，T 层可跑完整判据链并 no-op/决策，绝不抛异常。"""
        mod = self.mod
        old_a, old_m, old_s = (mod.PARAMS.get("t_anchor_book"),
                              mod.PARAMS.get("t_macro"),
                              mod.PARAMS.get("t_signal_codes"))
        mod.PARAMS["t_enabled"] = True
        mod.PARAMS["t_signal_codes"] = {"t_buy_first": "TB", "t_sell_first": "XB",
                                        "t_buy_first_pair": "TS", "t_sell_pair": "XS",
                                        "t_close_out": "TC"}
        mod.PARAMS["t_anchor_book"] = {"600000": {"anchor_price": 10.0, "anchor_source": "first_buy_order"}}
        mod.PARAMS["t_macro"] = {"market_scale": 0.7, "index_change_pct": 0.3,
                                 "market_state": "range", "asof_age_sec": 10}
        tick = {"600000.SH": {"last_price": 10.0, "prev_close": 10.0, "volume": 100000.0,
                              "amount": 1000000.0, "askPrice": [10.0], "bidPrice": [10.0]}}
        summary = {"placed": [], "cancelled": [], "skipped": [], "reason": ""}
        try:
            # 必须不抛（run_round 会 try 包住，但这里直接验函数本身稳健）
            mod._run_t(FakeContext(tick), datetime(2026, 11, 16, 10, 30),
                       [], {"600000.SH": {"qty": 1000.0, "cost": 10.0, "can_use": 1000.0}},
                       [], 100000.0, 100000.0, "20261116", summary)
        except Exception as exc:  # noqa: BLE001
            self.fail("T layer raised with full inputs: " + repr(exc))
        finally:
            mod.PARAMS["t_anchor_book"] = old_a
            mod.PARAMS["t_macro"] = old_m
            mod.PARAMS["t_signal_codes"] = old_s


if __name__ == "__main__":
    unittest.main()
