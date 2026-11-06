#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§STRATEGY-SWITCH-SYNC / §PARTIAL-FILL-1106 行为锁（2026-11-06）。

owner 裁决：「今晚有我更新的批准战法，启用的战法口径以量化交易 tab 里的战法开关为准」。
本文件锁三件会静默骗人的事：
  1. 载体准入判据与 Go signalctl.admit 两支语义一致（空白名单≠全部放行、非空白名单必须显式命中）；
  2. params 的开关与因子规则表必须由快照派生，手改一处 --check 必须真红；
  3. 部成数量来自柜台成交表，成交表读不到才回退委托表副本且必须点名来源。

English: behaviour locks for the live strategy-switch whitelist, snapshot drift gate and
partial-fill truth from the counter trade table.
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
sys.path.insert(0, GW_DIR)

import local_executor as lex  # noqa: E402

PY = sys.executable


def params(**overrides):
    out = dict(lex.PARAMS)
    out.update(overrides)
    return out


# ---------------------------------------------------------------------------
# (1) 准入判据：Go signalctl.go:admit 的两支，一支都不能漂
# ---------------------------------------------------------------------------
class TestStrategyAdmit(unittest.TestCase):
    def _p(self, allow):
        return params(
            enabled_strategies=allow,
            default_allowed_strategies=["dragon", "double_bump", "n_shape",
                                        "dragon_return", "factor", "pattern"],
            factor_id_prefix="fac_", pattern_id_prefix="pat_")

    def test_empty_whitelist_allows_builtins_and_library_rules_only(self):
        p = self._p([])
        for key in ["dragon", "double_bump", "n_shape", "dragon_return",
                    "factor", "pattern", "fac_118", "pat_2"]:
            self.assertTrue(lex.rule_strategy_admit(p, key), key)
        for key in ["momentum", "unknown_rule", ""]:
            self.assertFalse(lex.rule_strategy_admit(p, key), key)

    def test_nonempty_whitelist_is_explicit_membership_only(self):
        p = self._p(["dragon", "fac_118"])
        self.assertTrue(lex.rule_strategy_admit(p, "dragon"))
        self.assertTrue(lex.rule_strategy_admit(p, "fac_118"))
        self.assertFalse(lex.rule_strategy_admit(p, "n_shape"))
        self.assertFalse(lex.rule_strategy_admit(p, "fac_160"))
        self.assertFalse(lex.rule_strategy_admit(p, ""))

    def test_empty_key_is_never_live_in_either_branch(self):
        for allow in ([], ["dragon"]):
            self.assertFalse(lex.rule_strategy_admit(self._p(allow), ""))

    def test_baked_live_snapshot_covers_every_allowlisted_library_rule(self):
        p = lex.PARAMS
        allow = p["enabled_strategies"]
        ids = set(r["id"] for r in p["factor_rules"])
        library = [k for k in allow if k.startswith(("fac_", "pat_"))]
        self.assertTrue(library, "现网快照里没有库规则，判据恒真")
        self.assertEqual(set(library) - ids, set(),
                         "开关说开了、载体却没有可跑的规则表（降级报成功）")
        self.assertIn("n_shape", p["unmapped_strategies"],
                      "n_shape 未移植必须如实登记，不许当成已启用")


# ---------------------------------------------------------------------------
# (2) 快照派生链：手改 params 必须被 --check 抓到（变异反证）
# ---------------------------------------------------------------------------
class TestSnapshotSyncGate(unittest.TestCase):
    SCRIPT = os.path.join(REPO, "scripts", "sync_strategy_switches.py")
    PARAMS = os.path.join(GW_DIR, "local_executor_params.json")
    SNAPSHOT = os.path.join(GW_DIR, "strategy_switches_snapshot.json")

    def test_check_is_green_on_the_baked_params(self):
        r = subprocess.run([PY, self.SCRIPT, "--check"], cwd=REPO,
                           capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("SYNC_RESULT=OK", r.stdout)

    def test_hand_edited_params_turns_the_gate_red(self):
        original = open(self.PARAMS, "r", encoding="ascii").read()
        doc = json.loads(original)
        doc["params"]["enabled_strategies"] = ["dragon"]
        try:
            with open(self.PARAMS, "w", encoding="ascii", newline="\n") as fh:
                fh.write(json.dumps(doc, ensure_ascii=True, indent=2) + "\n")
            r = subprocess.run([PY, self.SCRIPT, "--check"], cwd=REPO,
                               capture_output=True, text=True, timeout=60)
            self.assertNotEqual(r.returncode, 0, "手改开关竟然没被对账抓到")
            self.assertIn("SYNC_RESULT=DRIFT", r.stdout)
        finally:
            with open(self.PARAMS, "w", encoding="ascii", newline="\n") as fh:
                fh.write(original)

    def test_snapshot_is_a_readable_auditable_baseline(self):
        snap = json.load(open(self.SNAPSHOT, encoding="utf-8"))
        self.assertTrue(snap["enabled_strategies"])
        self.assertTrue(snap["factor_rules"])
        self.assertTrue(snap.get("source"))


# ---------------------------------------------------------------------------
# (3) 部成：成交表是真值，委托表只是副本
# ---------------------------------------------------------------------------
class _Fill(object):
    def __init__(self, oid, qty):
        self.order_id = oid
        self.traded_volume = qty


class _FakeTrader(object):
    def __init__(self, fills=None, fail=False):
        self.fills = fills or []
        self.fail = fail

    def query_stock_trades(self, acc):
        if self.fail:
            raise RuntimeError("counter trade table down")
        return list(self.fills)


class _FakeSelf(object):
    def __init__(self, fills=None, fail=False):
        self._trader = _FakeTrader(fills, fail)
        self._acc = "acct"

    def is_connected(self):
        return True


class TestPartialFillTruth(unittest.TestCase):
    def test_fills_are_grouped_by_order_id(self):
        got = lex.XtBroker.trades_by_order(_FakeSelf([
            _Fill("A", 100), _Fill("A", 50), _Fill("B", 200)]))
        self.assertEqual(got, {"A": 150.0, "B": 200.0})

    def test_unreadable_trade_table_yields_empty_not_guessed_numbers(self):
        got = lex.XtBroker.trades_by_order(_FakeSelf(fail=True))
        self.assertEqual(got, {})

    def test_builder_whitelist_allows_the_trade_table_call(self):
        # 静态闸 g2 不许产物调未经登记的 xt 方法；方法名漏登记＝上线后 ImportError 之外
        # 更隐蔽的「部成永远走副本」形状。
        sys.path.insert(0, os.path.join(REPO, "scripts"))
        import build_local_executor as builder  # noqa: E402
        self.assertIn("query_stock_trades", builder.ALLOWED_XT_METHODS)


if __name__ == "__main__":
    unittest.main()
