# test_side_policy_20261124.py - SIDE-POLICY built-artifact behavior locks.
import datetime
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
DAY = "20261124"
NOW = 1800000000.0


# build_module 构建真实沙箱产物，确保行为测试覆盖烘参后的规则层。
def build_module(cls):
    out = tempfile.mkdtemp()
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out],
                          capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError((proc.stdout + proc.stderr)[-2000:])
    path = ""
    for token in (proc.stdout + proc.stderr).replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    spec = importlib.util.spec_from_file_location("side_policy_artifact_%s" % cls.__name__, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    cls.artifact_path = path
    cls.mod = mod


def snapshot(**over):
    base = {"day": DAY, "asof_ts": NOW, "mode": "bear", "level": 1,
            "allow_new": False, "allow_add": False, "target_multiplier": 0.85,
            "left_weight": 1.0, "right_weight": 0.8, "latch_until_ts": 0.0}
    base.update(over)
    return base


class SidePolicyArtifactTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        build_module(cls)

    def setUp(self):
        self.mod._STATE["side_left_seen"] = {}
        self.mod._STATE["side_policy_last"] = {}

    def ctx(self, **over):
        base = {"day": DAY, "now_ts": NOW, "symbol": "600000.SH", "trading_day": True}
        base.update(over)
        return base

    def params(self, **over):
        p = dict(self.mod.PARAMS)
        p.update(over)
        return p

    # test_go_json_without_latch_decodes_in_python 验证无 latch 的真实 Go JSON 不会被误判损坏。
    def test_go_json_without_latch_decodes_in_python(self):
        env = dict(os.environ, SIDE_POLICY_JSON_FOR_PYTHON="1")
        proc = subprocess.run(["go", "test", "./internal/signalctl", "-run",
                               "^TestSidePolicyJSONForPython$", "-count=1", "-v"],
                              capture_output=True, text=True, cwd=REPO, env=env)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        lines = [line.split("SIDE_POLICY_JSON=", 1)[1] for line in proc.stdout.splitlines()
                 if "SIDE_POLICY_JSON=" in line]
        self.assertEqual(len(lines), 1, proc.stdout)
        raw = json.loads(lines[0])
        self.assertIn("latch_until_ts", raw)
        self.assertEqual(raw["latch_until_ts"], 0)
        self.assertNotIsInstance(raw["latch_until_ts"], bool)
        p = self.params(side_policy_enabled=True, side_policy=raw)
        st = self.mod.side_policy_state(p, self.ctx(day=raw["day"], now_ts=raw["asof_ts"]))
        self.assertEqual(st["mode"], raw["mode"])
        self.assertFalse(st["latch_active"])
        self.assertNotEqual(st["reason"], "side_policy_bad_value")

    def test_disabled_and_missing_state(self):
        st = self.mod.side_policy_state(self.params(), self.ctx())
        self.assertEqual(st["mode"], "legacy")
        st = self.mod.side_policy_state(self.params(side_policy_enabled=True), self.ctx())
        self.assertEqual(st["mode"], "bear")
        self.assertFalse(st["allow_new"])
        self.assertTrue(st["allow_add"])

    def test_gate_matrix_and_strong_left(self):
        p = self.params(side_policy_enabled=True, side_policy=snapshot())
        sig = {"entry_style": "left", "score": 85.0, "threshold": 70.0, "rule_id": "fac_118"}
        self.assertFalse(self.mod.side_buy_allowed(p, self.ctx(), sig, "new", 0, False, False, True)[0])
        self.assertFalse(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, False, False, False)[0])
        self.assertTrue(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, False, False, True)[0])
        self.assertEqual(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, True, False, True)[1],
                         "tp_rebuy_block")
        self.assertEqual(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, False, True, True)[1],
                         "same_symbol_day")
        self.assertTrue(self.mod.side_buy_allowed(p, self.ctx(), sig, "t_leg", 100, True, True, False)[0])

    def test_real_fac118_rule_reaches_left_exception(self):
        rule = next(r for r in self.mod.PARAMS["factor_rules"] if r.get("id") == "fac_118")
        self.assertEqual(rule.get("entry_style"), "left")
        sig = {"entry_style": rule["entry_style"], "score": 90.0,
               "threshold": rule["buy_threshold"], "rule_id": rule["id"]}
        p = self.params(side_policy_enabled=True, side_policy=snapshot())
        self.assertTrue(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, False, False, True)[0])
        sig["entry_style"] = "unclassified"
        self.assertFalse(self.mod.side_buy_allowed(p, self.ctx(), sig, "add", 100, False, False, True)[0])

    def left_signal(self, score=90.0, threshold=70.0):
        return {"entry_style": "left", "score": score, "threshold": threshold,
                "rule_id": "fac_118", "symbol": "600000.SH"}

    def stable_params(self):
        keys = ("side_left_stable_rounds", "side_left_stable_gap_sec",
                "side_left_stable_max_drop", "side_left_score_floor",
                "side_left_score_margin", "side_freshness_heartbeat_sec")
        return {key: self.mod.PARAMS[key] for key in keys}

    def with_stable_params(self, **over):
        params = self.stable_params()
        params.update(over)
        old = {key: self.mod.PARAMS.get(key) for key in params}
        self.mod.PARAMS.update(params)
        return old

    def restore_params(self, old):
        for key, value in old.items():
            if value is None:
                self.mod.PARAMS.pop(key, None)
            else:
                self.mod.PARAMS[key] = value

    def test_left_stability_rejects_historical_low_score(self):
        old = self.with_stable_params()
        try:
            sig = self.left_signal(0.0)
            self.assertFalse(self.mod._side_left_stable(sig, self.ctx(), NOW - 60))
            self.assertFalse(self.mod._side_left_stable(self.left_signal(90.0), self.ctx(), NOW))
        finally:
            self.restore_params(old)

    def test_left_stability_rejects_stale_observations(self):
        old = self.with_stable_params(side_freshness_heartbeat_sec=1200)
        try:
            sig = self.left_signal()
            self.mod._STATE["side_left_seen"][(DAY, "600000.SH", "fac_118")] = [
                (NOW - 1260, 90.0), (NOW - 1201, 90.0)]
            self.assertFalse(self.mod._side_left_stable(sig, self.ctx(), NOW))
        finally:
            self.restore_params(old)

    def test_left_stability_current_pass_and_repeat_cannot_bypass(self):
        old = self.with_stable_params()
        try:
            sig = self.left_signal()
            self.assertFalse(self.mod._side_left_stable(sig, self.ctx(), NOW - 60))
            self.assertTrue(self.mod._side_left_stable(sig, self.ctx(), NOW))
            # A same-gap round is not a new observation and a weak current
            # score must not reuse the previous round's strong score.
            self.assertFalse(self.mod._side_left_stable(self.left_signal(0.0), self.ctx(), NOW + 1))
            self.assertFalse(self.mod._side_left_stable(self.left_signal(87.0), self.ctx(), NOW + 2))
            self.assertTrue(self.mod._side_left_stable(sig, self.ctx(), NOW + 60))
        finally:
            self.restore_params(old)

    def test_left_stability_missing_knobs_fail_closed(self):
        old = self.with_stable_params()
        try:
            for key in tuple(old):
                value = self.mod.PARAMS.pop(key)
                try:
                    self.assertFalse(self.mod._side_left_stable(self.left_signal(), self.ctx(), NOW), key)
                finally:
                    self.mod.PARAMS[key] = value
        finally:
            self.restore_params(old)

    def test_target_multiplier_and_style_weight(self):
        p = self.params(side_policy_enabled=True, side_policy=snapshot())
        self.assertEqual(self.mod.side_target_multiplier(p, self.ctx(), "new"), 0.85)
        self.assertEqual(self.mod.side_target_multiplier(p, self.ctx(), "t_leg"), 1.0)
        self.assertEqual(self.mod.side_style_weight(p, self.ctx(), "right"), 0.8)
        self.assertEqual(self.mod.combine_side_weight(p, 1.2, 1.2), 1.2)

    def test_latch_and_aggressive_ttl(self):
        p = self.params(side_policy_enabled=True,
                        side_policy=snapshot(mode="bull", allow_new=True, target_multiplier=1.1,
                                             latch_until_ts=NOW + 60))
        st = self.mod.side_policy_state(p, self.ctx())
        self.assertFalse(st["allow_new"])
        self.assertTrue(st["latch_active"])
        p["side_policy"] = snapshot(mode="bull", allow_new=True, target_multiplier=1.1,
                                    asof_ts=NOW - 4000)
        st = self.mod.side_policy_state(p, self.ctx())
        self.assertEqual(st["mode"], "bear")
        self.assertFalse(st["allow_new"])

    def test_t_bear_entry_and_accel_are_synchronized(self):
        p = self.params(side_policy_enabled=True, side_policy=snapshot(), t_round_stop_pct=0.0)
        c = self.ctx(market_state="bear", market_state_age_sec=60.0)
        blocked, reason = self.mod.t_regime_gate(p, c)
        self.assertFalse(blocked, reason)
        existing = {"direction": "buy_first", "opened_at": NOW - 1000}
        legs = {"first_filled": 100, "first_price": 10.0, "pair_filled": 0, "pair_price": 0.0}
        self.assertEqual(self.mod.t_accel_reason(p, c, existing, legs, 10.0), "")
        c["market_state"] = "bull"
        self.assertEqual(self.mod.t_accel_reason(p, c, existing, legs, 10.0), "regime")

    def test_round_prepare_contains_decode_exception(self):
        original = self.mod.side_policy_state
        def broken(*_args):
            raise ValueError("malformed params")
        self.mod.side_policy_state = broken
        try:
            st = self.mod._side_round_prepare(datetime.datetime(2026, 11, 24, 10, 0))
        finally:
            self.mod.side_policy_state = original
        self.assertFalse(st["allow_new"])
        self.assertEqual(st["reason"], "side_policy_decode_failed")
        self.assertEqual(self.mod._STATE["side_policy_last"], st)

    def test_gate_order_and_readout_are_wired(self):
        src = open(self.artifact_path, encoding="utf-8").read()
        body = src[src.index("def run_round("):src.index("def cancel_leg(")]
        anchors = ["same_symbol_day", "tp_rebuy_block", "flow_quadrant_block",
                   "side_buy_allowed(", "sizing_cap(ctx, positions, intent"]
        pos = [body.index(a) for a in anchors]
        self.assertEqual(pos, sorted(pos))
        self.assertIn("side mode=%s level=%s slope=%s gap=%s tgt=%s", src)


if __name__ == "__main__":
    unittest.main()
