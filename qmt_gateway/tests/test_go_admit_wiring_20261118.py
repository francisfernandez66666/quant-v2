# -*- coding: utf-8 -*-
"""2026-11-18 GO-ADMIT-WIRING 运行时白名单接线的行为锁（方案 A）。

审计发现：_go_admitted 自 875e1a6 引入起 0 调用点，docstring 却声称缺陷已关
（AST LOAD 计数=[]实锤）。本批把它真正接进 run_round 的移植规则循环：
rule_id 优先、kind 兜底映射、live 买入无法归属即降档 switch_off（算但不花）、
卖出豁免（Go 白名单只管买入 dispatch，scoring_loop.go dispatchLive 只收 buys）。

影响面（当前烘参）：因子表已按白名单烘焙过滤、dragon∈白名单、做空是 sell
⇒ 接线=纯防御，零行为变化；只在"rule_arm=live 但白名单没开"的烘参漂移场景生效。
每条锁配塞雷反证。
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


def _sig(side="buy", rule_id="", kind_hint=None):
    s = {"side": side, "symbol": "000417.SZ", "qty": 100, "price": 10.0,
         "amount": 1000.0, "short_code": "000417-x", "kind": kind_hint or "k"}
    if rule_id:
        s["rule_id"] = rule_id
    return s


class GoAdmitWiring20261118(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        out = tempfile.mkdtemp()
        rc, log, path = _build_sandbox(out)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % log[-800:])
        spec = importlib.util.spec_from_file_location("admit_artifact", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)
        # 真实烘参快照（与 v97 产物一致），供影响面对照
        cls.REAL_ALLOW = list(cls.mod.PARAMS.get("enabled_strategies") or [])

    def setUp(self):
        self.mod.PARAMS["enabled_strategies"] = list(self.REAL_ALLOW)
        self.mod._PORTED_RULES.pop("_wiring_probe", None)

    def tearDown(self):
        self.mod._PORTED_RULES.pop("_wiring_probe", None)
        self.mod.PARAMS["enabled_strategies"] = list(self.REAL_ALLOW)

    def _register(self, side, rule_id):
        sig = _sig(side=side, rule_id=rule_id)
        self.mod._PORTED_RULES["_wiring_probe"] = (
            (lambda params, ctx, **kw: dict(sig)), "test")

    # ---- 1. factor buy with an in-whitelist rule_id stays live ---------------
    def test_factor_buy_in_whitelist_stays_live(self):
        fac = next((s for s in self.REAL_ALLOW if s.startswith("fac_")), None)
        self.assertIsNotNone(fac, "real baked whitelist must contain a fac_* id")
        self._register("buy", fac)
        mode = self.mod.rule_arm_mode(self.mod.PARAMS, "_wiring_probe")
        # the loop body: replicate the wired check exactly via _go_admitted
        admitted = self.mod._go_admitted("_wiring_probe", rule_id=fac)
        self.assertTrue(admitted, "in-whitelist factor id must admit")

    # ---- 2. factor buy with an OUT-of-whitelist rule_id must refuse ----------
    def test_factor_buy_outside_whitelist_refused(self):
        self.assertFalse(self.mod._go_admitted("_wiring_probe", rule_id="fac_999999"),
                         "out-of-whitelist factor id must refuse")

    # ---- 3. dragon kind fallback maps and admits -----------------------------
    def test_dragon_kind_fallback_admits(self):
        self.assertTrue(self.mod._go_admitted("dragon"),
                        "dragon is in the baked map AND the whitelist")

    # ---- 4. unattributable live BUY is refused (fail-closed) -----------------
    def test_unattributable_kind_refused(self):
        self.assertFalse(self.mod._go_admitted("_wiring_probe"),
                         "no rule_id, no map entry, no prefix => refuse")

    # ---- 5. SELL is exempt: a whitelist-unknown sell kind still admits -------
    def test_sell_kinds_are_not_gated(self):
        # break_down has NO map entry and is not in the whitelist, yet its live
        # sell leg must keep working -- the caller skips the check for sells.
        # The lock: _go_admitted itself would refuse it, proving the exemption
        # lives in the CALLER (side=="buy" guard), not inside the helper.
        self.assertFalse(self.mod._go_admitted("break_down"),
                         "helper refuses unattributed kinds (by design)")
        import inspect
        src = inspect.getsource(self.mod.run_round)
        self.assertIn('== "buy"', src,
                      "caller must gate live BUYS only (sell exemption)")

    # ---- 6. SABOTAGE: dragon removed from the whitelist => refused -----------
    def test_sabotage_dragon_out_of_whitelist(self):
        saved = list(self.mod.PARAMS["enabled_strategies"])
        try:
            self.mod.PARAMS["enabled_strategies"] = [
                s for s in saved if s != "dragon"]
            self.assertFalse(self.mod._go_admitted("dragon"),
                             "removing dragon from the whitelist must refuse it")
        finally:
            self.mod.PARAMS["enabled_strategies"] = saved

    # ---- 7. run_round really calls _go_admitted (dead-code stays dead-NO) ----
    def test_run_round_wires_go_admitted(self):
        import inspect
        src = inspect.getsource(self.mod.run_round)
        self.assertIn("_go_admitted(kind, rule_id=hit.get(\"rule_id\"))", src,
                      "the 0-call-site defect must stay closed for real now")

    # ---- 8. helper signature stays backward compatible (old tests, 1 arg) ----
    def test_one_arg_call_still_works(self):
        self.assertTrue(self.mod._go_admitted("dragon"))
        self.assertFalse(self.mod._go_admitted("unknown_kind"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
