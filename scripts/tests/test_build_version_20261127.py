#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§BAKE-CHAIN 2026-11-27：版本宽度与失败回滚的行为腿。

热更链两条真事故：① 版本越过 v999 后构建器只认 2–3 位，每 30 秒 ABORT 一次且
版本号继续 +1；② 构建失败时版本号已经落盘，不回滚就永久漂移。这两条都是
"静态看代码全对、现场永远不生效"的形态，所以这里用真实临时文件跑行为腿。
"""

import importlib.util
import json
import os
import shutil
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

import build_local_executor as builder  # noqa: E402

_SPEC = importlib.util.spec_from_file_location(
    "hotreload_gz_20261127",
    os.path.join(REPO, "scripts", "carrier_hot_reload_gz.py"))
hotreload = importlib.util.module_from_spec(_SPEC)  # noqa: E402
_SPEC.loader.exec_module(hotreload)


class TestBuildVersionWidth(unittest.TestCase):
    """2–6 位全收、1/7 位全拒，且 4 位版本真能烘出产物。"""

    def test_four_digit_version_bakes(self):
        tmp = tempfile.mkdtemp(prefix="qmtp-bake-version-")
        self.addCleanup(shutil.rmtree, tmp)
        params_path = os.path.join(tmp, "params.json")
        source_path = os.path.join(tmp, "source.py")
        out_dir = os.path.join(tmp, "out")
        with open(builder.DEFAULT_PARAMS, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        doc["strategy_version"] = "v2072-20261127"
        with open(params_path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=2)
        with open(builder.DEFAULT_SOURCE, "r", encoding="utf-8") as src, \
                open(source_path, "w", encoding="utf-8") as dst:
            shutil.copyfileobj(src, dst)
        os.makedirs(out_dir, exist_ok=True)
        params, version, error = builder.load_params(params_path)
        self.assertEqual(error, "", "临时参数文件必须能加载")
        self.assertEqual(version, "v2072-20261127")
        with open(source_path, "r", encoding="utf-8") as fh:
            artifact = builder.bake(fh.read(), params, version)
        self.assertIn('STRATEGY_VERSION = "v2072-20261127"', artifact)
        ok, _results = builder.run_gates(artifact)
        self.assertTrue(ok, "4 位版本烘出的产物必须通过全部构建闸")

    def test_regex_width(self):
        for version in ("v99-20261127", "v999-20261127", "v2072-20261127",
                        "v100000-20261127"):
            self.assertTrue(builder.VERSION_RE.match(version), version)
        for version in ("v1-20261127", "v1000000-20261127", "v2072_20261127"):
            self.assertFalse(builder.VERSION_RE.match(version), version)


class TestVersionRollback(unittest.TestCase):
    """失败后参数文件的 strategy_version 必须逐字节变回失败前的值。"""

    def test_restore_is_byte_identical(self):
        tmp = tempfile.mkdtemp(prefix="qmtp-version-rollback-")
        self.addCleanup(shutil.rmtree, tmp)
        params_path = os.path.join(tmp, "local_executor_params.json")
        with open(builder.DEFAULT_PARAMS, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        original = "v999-20261127"
        doc["strategy_version"] = original
        with open(params_path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=2)
        before = open(params_path, "rb").read()

        # 模拟主流程：先 bump 落盘，再构建失败。
        doc["strategy_version"] = "v1000-20261127"
        with open(params_path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=2)
        self.assertNotEqual(open(params_path, "rb").read(), before)
        self.assertTrue(hotreload.restore_params_version(original, params_path))
        self.assertEqual(open(params_path, "rb").read(), before)

    def test_restore_only_touches_one_field(self):
        tmp = tempfile.mkdtemp(prefix="qmtp-version-field-")
        self.addCleanup(shutil.rmtree, tmp)
        params_path = os.path.join(tmp, "local_executor_params.json")
        with open(builder.DEFAULT_PARAMS, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        doc["strategy_version"] = "v2072-20261127"
        doc["params"]["sell_confirm_sec"] = 77
        with open(params_path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=2)
        self.assertTrue(hotreload.restore_params_version("v2071-20261127", params_path))
        with open(params_path, "r", encoding="utf-8") as fh:
            after = json.load(fh)
        self.assertEqual(after["strategy_version"], "v2071-20261127")
        # 不能把本轮已经烘进去的静态旋钮一起回滚。
        self.assertEqual(after["params"]["sell_confirm_sec"], 77)


if __name__ == "__main__":
    unittest.main()
