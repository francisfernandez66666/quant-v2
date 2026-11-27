# 2026-11-27 P5（PLAN_FACTOR_FIX_P0_20261126 §6 / owner D7）行为锁。
#
# 靶子：战法库的 `enabled` 开关以前只管研究引擎与回测，**没有**同步进载体实盘白名单派生链。
# 修法落在两处**快照重建**（不是 sync_strategy_switches.derive——那里 ② 要求"规则表里的每条
# 都在白名单"，只滤规则不滤白名单会自相矛盾）：
#   ① scripts/carrier_hot_reload_gz.py::rebuild_snapshots
#   ② scripts/carrier_hot_reload.sh 远端拉取段（Mac 备用链）
#
# 两枚行为腿：停用条目必须从 factor_rules 与 enabled_strategies 同时消失；重新启用后恢复。
# 一枚静态锁：Mac 备用链的远端 python 段必须带 enabled 过滤（那条链本机不跑，只能静态钉）。
# 夹具纪律：临时 BUILD_DIR，addCleanup(shutil.rmtree) 回收。

import importlib.util
import json
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
SCRIPTS = os.path.join(REPO, "scripts")


def _load_gz_reloader():
    sys.path.insert(0, GATEWAY)
    spec = importlib.util.spec_from_file_location(
        "carrier_hot_reload_gz_under_test",
        os.path.join(SCRIPTS, "carrier_hot_reload_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class CarrierEnabledFilter20261127(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_gz_reloader()

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="carrier_p5_")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.qg = os.path.join(self.tmp, "qmt_gateway")
        os.makedirs(self.qg, exist_ok=True)
        with open(os.path.join(self.qg, "strategy_switches_snapshot.json"), "w",
                  encoding="utf-8") as fh:
            json.dump({"enabled_strategies": [], "factor_rules": []}, fh)
        self._orig = self.mod.BUILD_DIR
        self.mod.BUILD_DIR = self.tmp
        self.addCleanup(self._restore)

    def _restore(self):
        self.mod.BUILD_DIR = self._orig

    def _rules(self, disabled):
        return [
            {"id": "fac_172", "factors": ["Mom10"], "weights": {"Mom10": 1.0},
             "directions": {"Mom10": -1}, "buy_threshold": 70, "enabled": not disabled},
            {"id": "fac_173", "factors": ["Mom20"], "weights": {"Mom20": 1.0},
             "directions": {"Mom20": -1}, "buy_threshold": 70},
        ]

    def _snapshot(self):
        with open(os.path.join(self.qg, "strategy_switches_snapshot.json"),
                  encoding="utf-8") as fh:
            return json.load(fh)

    def test_disabled_rule_leaves_both_legs(self):
        wl = ["fac_172", "fac_173", "dragon"]
        sel = self.mod.rebuild_snapshots({}, self._rules(disabled=True), wl)
        ids = [r["id"] for r in sel]
        self.assertNotIn("fac_172", ids, "停用的战法必须从 factor_rules 里摘掉")
        self.assertIn("fac_173", ids, "enabled 缺省＝启用（老条目没有该字段）")
        snap = self._snapshot()
        self.assertNotIn("fac_172", snap["enabled_strategies"],
                         "白名单同一腿也要摘——只摘规则会撞 derive 的 rule_missing")
        self.assertIn("fac_173", snap["enabled_strategies"])
        self.assertIn("dragon", snap["enabled_strategies"], "内置战法不受 applied enabled 影响")

    def test_reenabled_rule_comes_back(self):
        wl = ["fac_172", "fac_173"]
        sel = self.mod.rebuild_snapshots({}, self._rules(disabled=False), wl)
        self.assertEqual(sorted(r["id"] for r in sel), ["fac_172", "fac_173"])
        self.assertIn("fac_172", self._snapshot()["enabled_strategies"])

    def test_mac_fallback_filters_enabled(self):
        # Mac 备用链本机不跑（需要 ssh 到 gz），只能静态钉：远端 python 段必须算 off 集合并滤 wl。
        # 注意：那段 python 写在双引号 ssh 实参里，文件中的引号是 `\"` 形态，断言要照抄。
        path = os.path.join(SCRIPTS, "carrier_hot_reload.sh")
        with open(path, encoding="utf-8") as fh:
            body = fh.read()
        self.assertIn('r.get(\\"enabled\\",True) is False', body,
                      "Mac 备用链远端拉取段必须按 enabled 过滤（停用战法不许仍参与实盘竞争）")
        self.assertIn('wl=[i for i in wl if i not in off]', body,
                      "白名单同腿必须一起滤，否则 derive 报 rule_missing")


if __name__ == "__main__":
    unittest.main()
