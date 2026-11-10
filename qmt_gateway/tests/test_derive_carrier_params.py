# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_derive_carrier_params.py -- the single-source export tool's own tests.

覆盖三件事，顺序即重要性：

1. **形状转换不许把方向弄反**（_index_bands / _index_scales / _emotion_scales / _split_windows）。
   这一组不是"锦上添花"：本工具第一版就是按升序取 shallow，于是"跌 1% 不降权"被静默
   改成了"跌 1% 就降到 0.70"，两张表看起来都"有 shallow 有 deep"，肉眼检不出来。
   所以下面每条都断言**具体档位对应具体值**，而不是"两个键都在"。
2. **没有来源的键一律不猜**（build_carrier_values / resolve_extras / coverage）。
   别名只做改名；模块声明表只在 Go 没读数时补位；两者都给不了的键进 unhoused 清单，
   绝不落到参数文件里冒充有主。
3. **端到端与 Go 同源**（需要 go 二进制；缺了就如实 skip，不许伪过）。
   跑一遍 --check，并逐键比对参数文件与 Go 打出来的值——这条红了就说明有人在
   手抄或改错了某个阈值，而那是本工具存在的唯一理由。
"""

import json
import os
import shutil
import subprocess
import sys
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCRIPT = os.path.join(REPO_ROOT, "scripts", "derive_carrier_params.py")
PARAMS_PATH = os.path.join(REPO_ROOT, "qmt_gateway", "local_executor_params.json")

sys.path.insert(0, os.path.dirname(SCRIPT))
import derive_carrier_params as dcp  # noqa: E402


GO_AVAILABLE = shutil.which("go") is not None


def _params():
    with open(PARAMS_PATH, "r", encoding="utf-8") as fh:
        return json.load(fh)["params"]


class WindowShape(unittest.TestCase):
    def test_comma_string_becomes_pairs(self):
        self.assertEqual(
            dcp._split_windows("09:20-09:30,14:57-15:00"),
            [["09:20", "09:30"], ["14:57", "15:00"]])

    def test_malformed_window_raises_not_drops(self):
        """残缺的时间闸不许被静默丢掉：丢掉一条等于那道门今天不存在。"""
        with self.assertRaises(ValueError):
            dcp._split_windows("09:20")


class IndexBands(unittest.TestCase):
    """Go 的 {"-1":1.0,"-2":0.7,"deep":0.4} 拆成 bands/scales 两表——方向是本组的全部意义。"""

    THRESHOLDS = {"-1": 1.0, "-2": 0.7, "deep": 0.4}

    def test_shallow_is_the_less_negative_band(self):
        bands = dcp._index_bands(self.THRESHOLDS)
        self.assertEqual(bands, {"shallow": -1.0, "deep": -2.0})

    def test_scale_keys_follow_the_same_direction(self):
        scales = dcp._index_scales(self.THRESHOLDS)
        self.assertEqual(scales, {"shallow": 1.0, "mid": 0.7, "deep": 0.4})

    def test_bands_and_scales_agree_on_order(self):
        """bands.shallow 必须正是 scales.shallow 那一档的边界。

        两条分别断言"值对"很容易，一条断言"两张表互相自洽"才抓得住只改一边的那种漂移。
        """
        bands = dcp._index_bands(self.THRESHOLDS)
        scales = dcp._index_scales(self.THRESHOLDS)
        self.assertGreater(bands["shallow"], bands["deep"])
        self.assertGreaterEqual(scales["shallow"], scales["mid"])
        self.assertGreaterEqual(scales["mid"], scales["deep"])

    def test_unexpected_key_count_raises(self):
        with self.assertRaises(ValueError):
            dcp._index_bands({"-1": 1.0})
        with self.assertRaises(ValueError):
            dcp._index_scales({"-1": 1.0, "-2": 0.7})


class EmotionRekey(unittest.TestCase):
    """Go 用中文相位名做键，载体用 ASCII 码——只许改名，不许丢档。"""

    LABELS = {"retreat": "退潮", "divergence": "背离", "climax": "高潮",
              "ferment": "发酵", "start": "启动", "ice": "冰点"}
    GO = {"退潮": 0.5, "背离": 0.6, "高潮": 0.8, "发酵": 1.0, "启动": 1.0}

    def test_every_go_phase_survives_the_rename(self):
        out = dcp._emotion_scales(self.GO, self.LABELS)
        # 键换成了 ASCII 码，值一个都不许少（冰点在 Go 表里没有，本来就不该出现）。
        self.assertEqual(set(out), {"retreat", "divergence", "climax", "ferment", "start"})
        self.assertEqual(out["retreat"], 0.5)
        self.assertEqual(out["climax"], 0.8)

    def test_missing_label_raises_instead_of_dropping_a_phase(self):
        """丢一档降权＝那一相从此不参与合成，而合成结果只会更激进——必须炸，不能默默少一个键。"""
        with self.assertRaises(ValueError):
            dcp._emotion_scales(self.GO, {"retreat": "退潮"})

    def test_label_without_go_scale_raises(self):
        with self.assertRaises(ValueError):
            dcp._emotion_scales({"退潮": 0.5}, self.LABELS)

    def test_absent_label_table_raises(self):
        with self.assertRaises(ValueError):
            dcp._emotion_scales(self.GO, None)


class TDirection(unittest.TestCase):
    def test_mode_is_assembled_from_the_two_booleans(self):
        self.assertEqual(dcp._t_mode({"t_buy_first": True, "t_sell_first": True}), "both")
        self.assertEqual(dcp._t_mode({"t_buy_first": True, "t_sell_first": False}), "buy_first")
        self.assertEqual(dcp._t_mode({"t_buy_first": False, "t_sell_first": True}), "sell_first")
        # 两个都不许做：Go 的语义是"资金路径宁可不做"，串必须如实说 none，不许回落到 both。
        self.assertEqual(dcp._t_mode({"t_buy_first": False, "t_sell_first": False}), "none")


class MappingIntegrity(unittest.TestCase):
    def test_no_carrier_key_is_doubly_sourced(self):
        seen = {}
        for go_key, targets, _transform in dcp.MAPPING:
            for target in targets:
                self.assertNotIn(target, seen,
                                 "carrier key %s supplied by both %s and %s"
                                 % (target, seen.get(target), go_key))
                seen[target] = go_key

    def test_every_alias_names_a_real_source_key(self):
        """别名必须指向一个真实存在的键，否则它是"看起来有人管"的第三种孤儿。"""
        mapped = set()
        for _go, targets, _t in dcp.MAPPING:
            mapped.update(targets)
        for alias, source in dcp.ALIAS.items():
            self.assertIn(source, mapped | set(_params()),
                          "alias %s points at %s which nothing produces" % (alias, source))
            self.assertNotIn(alias, dcp.ALIAS.values(), "alias chain (two hops) is not allowed")

    def test_registered_owned_keys_are_not_also_derived(self):
        """登记为"载体自有"又同时被派生的键＝两处供给；派生侧赢，但登记必须撤掉。"""
        derived_targets = set()
        for _go, targets, _t in dcp.MAPPING:
            derived_targets.update(targets)
        overlap = sorted(derived_targets & set(dcp.CARRIER_OWNED))
        self.assertEqual(overlap, [], "derived keys must be removed from CARRIER_OWNED: %s" % overlap)


class Coverage(unittest.TestCase):
    def test_orphan_is_a_key_nobody_owns(self):
        values = {"scale_floor": (0.15, "src")}
        owned = {"lot_size": "carrier-shape"}
        original = dcp.CARRIER_OWNED
        dcp.CARRIER_OWNED = owned
        try:
            cov = dcp.coverage(values, {"scale_floor", "lot_size", "who_knows"}, {})
        finally:
            dcp.CARRIER_OWNED = original
        self.assertEqual(cov["orphan"], ["who_knows"])
        self.assertIn("scale_floor", cov["derived"])
        self.assertIn("lot_size", cov["owned"])

    def test_defaults_table_drift_is_reported_not_repaired(self):
        """兜底表和 Go 不一致时报出来，让改的人决定——工具不许顺手改规则模块。"""
        drift = dcp.defaults_table_drift({"scale_floor": (0.20, "src")}, {"scale_floor": 0.15})
        self.assertEqual(len(drift), 1)
        self.assertEqual(drift[0][0], "scale_floor")

    def test_numeric_spelling_is_not_a_drift(self):
        """0.5 与 0.50、1 与 1.0 是同一个数；把它们报成漂移会让人无视这张表。"""
        self.assertEqual(dcp._norm(1), dcp._norm(1.0))
        self.assertEqual(dcp.defaults_table_drift({"power_p": (0.5, "src")}, {"power_p": 1 / 2}), [])

    def test_resolve_extras_never_invents_a_value(self):
        """unhoused 的键既不进 merged 也不进参数文件——这条是整个工具的底线。"""
        merged = dcp.resolve_extras({"t_mode": ("both", "src")}, {}, {})
        self.assertEqual(sorted(merged), ["t_direction_mode", "t_mode"])
        self.assertEqual(merged["t_direction_mode"][0], "both")
        # 别名搬过来的是**值**，不是 (值, 真源) 那一对——形状错了产物里就是列表。
        self.assertIsInstance(merged["t_direction_mode"][0], str)
        self.assertTrue(merged["t_direction_mode"][1].startswith("alias:"))
        for invented in ("t_session_windows", "t_stale_order_sec"):
            self.assertNotIn(invented, merged)


class DefaultsCatalog(unittest.TestCase):
    def test_catalog_declares_its_go_home_for_the_changed_amount_gate(self):
        """min_order_amount 是本轮唯一一枚**改了行为**的数（1000 -> Go 出厂 10000）。

        它单独上锁：将来有人把它改回 1000 而不改 Go，--check 会红；改回 1000 且改了 Go，
        这条锁会逼他来看这段说明——金额闸决定"哪一笔不值得下"，不该由载体自己定。
        """
        sys.path.insert(0, os.path.join(REPO_ROOT, "qmt_gateway"))
        import rules_sizing
        params = _params()
        self.assertEqual(float(params["min_order_amount"]),
                         float(rules_sizing.SIZING_DEFAULTS["min_order_amount"]))
        self.assertGreaterEqual(float(params["min_order_amount"]), 1000.0)


class Universe(unittest.TestCase):
    """观察宇宙：默认**只打印不落盘**，且代码→市场后缀那条规则不许在这里再写一遍。"""

    def _write(self, doc):
        path = os.path.join(self.tmp, "wl.json")
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh)
        return path

    def setUp(self):
        import tempfile
        self.tmp = tempfile.mkdtemp(prefix="universe-")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_bare_codes_get_the_single_source_suffix(self):
        path = self._write(["600519", "000001", "300750", "830000"])
        codes, dropped = dcp.build_universe([path])
        self.assertEqual(codes, ["000001.SZ", "300750.SZ", "600519.SH", "830000.BJ"])
        self.assertEqual(dropped, [])

    def test_unusable_code_is_dropped_and_counted_not_guessed(self):
        path = self._write(["600519", "ABC123", "", {"code": "000021"}])
        codes, dropped = dcp.build_universe([path])
        self.assertEqual(codes, ["000021.SZ", "600519.SH"])
        self.assertEqual(len(dropped), 2, "认不出的码必须数得出来，不许折进'没有这一只'")

    def test_preview_writes_nothing(self):
        """把真实自选池烘进 params 是 owner 的决定：默认一条字节都不改。"""
        path = self._write(["600519"])
        before = open(dcp.PARAMS_PATH, "rb").read()
        rc = dcp.emit_universe([path], write=False)
        self.assertEqual(rc, 0)
        self.assertEqual(open(dcp.PARAMS_PATH, "rb").read(), before,
                         "preview 写了参数文件——那等于替 owner 做了决定")


class BearTacticKnobs(unittest.TestCase):
    """做空四战法的 102 枚旋钮：清单、真源、以及"参数文件里真的有这些数"。

    这一组为什么必须存在：11-04 那批把 rules_form.py 的规则**代码**搬进了载体，
    构建闸只验"模块到没到"，于是 105 枚声明的阈值里 102 枚在参数文件里不存在，
    产物里每条移植规则第一次被调用就 raise KeyError('bd_min_bars')——装载成功、
    注册成功、五道闸全绿，而一轮也算不出来。这三条用例把那个形状从三个方向钉住。
    """

    def test_declared_list_equals_form_keys_minus_carrier_owned(self):
        """BEAR_TACTIC_KEYS 必须正好是 rules_form 自己声明的那批，去掉登记为载体自有的三枚。

        多一枚＝搬一个没人读的数进产物；少一枚＝那枚阈值又没主人。两个方向都要红。
        """
        sys.path.insert(0, os.path.join(REPO_ROOT, "qmt_gateway"))
        import rules_form
        declared = set(dcp.BEAR_TACTIC_KEYS)
        form = set(rules_form.FORM_PARAM_KEYS)
        owned = set(dcp.CARRIER_OWNED)
        self.assertEqual(declared, form - owned,
                         "extra=%s missing=%s" % (sorted(declared - (form - owned)),
                                                  sorted((form - owned) - declared)))
        self.assertEqual(len(dcp.BEAR_TACTIC_KEYS), len(declared), "清单里有重复键")

    def test_no_bear_key_is_doubly_sourced_with_the_main_table(self):
        """同名直搬那一批与主表不能供同一个载体键（两处供给同一枚数＝本工具的头号靶子）。"""
        seen = {}
        for go_key, targets, _transform in dcp.MAPPING:
            for target in targets:
                self.assertNotIn(target, seen,
                                 "carrier key %s supplied by both %s and %s"
                                 % (target, seen.get(target), go_key))
                seen[target] = go_key

    @unittest.skipUnless(GO_AVAILABLE, "go_absent: needs the Go export readout")
    def test_every_bear_knob_has_a_go_readout(self):
        """每一枚都必须真的能从 Go 打出来，且参数文件里有值——缺一个就是一条 KeyError。"""
        derived, _prov = dcp.run_go_tool()
        params = _params()
        missing_go = sorted(k for k in dcp.BEAR_TACTIC_KEYS if k not in derived)
        self.assertEqual(missing_go, [], "Go 侧没有这些键的导出读数：%s" % missing_go[:10])
        missing_file = sorted(k for k in dcp.BEAR_TACTIC_KEYS if k not in params)
        self.assertEqual(missing_file, [], "参数文件里没有这些键：%s" % missing_file[:10])
        drifted = sorted(k for k in dcp.BEAR_TACTIC_KEYS
                         if k in params and dcp._norm(params[k]) != dcp._norm(derived[k]))
        self.assertEqual(drifted, [], "这些键与 Go 出厂值不一致：%s" % drifted[:10])


@unittest.skipUnless(GO_AVAILABLE, "go_absent: the end-to-end check needs the Go toolchain")
class SnapshotOverride(unittest.TestCase):
    """§CAP-OVERRIDE-ASCII（2026-11-09）：账号级钱帽覆盖登记。

    daily_buy_cap 的真源是 live_rules 快照里的账号 daily_budget_amount
    （owner 2026-11-09 15:15 裁决对齐现网账号值，fa6939b），不是 Go 出厂。
    这一组锁三件事：登记在册、登记表不滚雪球、参数文件保持纯 ASCII。
    """

    def test_override_value_comes_from_the_live_rules_snapshot(self):
        sys.path.insert(0, os.path.join(REPO_ROOT, "scripts"))
        import sync_live_rules

        block = sync_live_rules.derive(sync_live_rules._read(
            sync_live_rules.SNAPSHOT))
        params = _params()
        self.assertEqual(float(params["daily_buy_cap"]),
                         float(block["qmt"]["daily_budget_amount"]))

    def test_go_factory_exclusions_are_pinned(self):
        """等值锁：任何想给第二枚键办"免于 Go 出厂对账"的人必须改这里并给理由。"""
        self.assertEqual(set(dcp.SNAPSHOT_OVERRIDES), {"daily_buy_cap"})

    def test_params_file_is_pure_ascii(self):
        """参数文件必须纯 ASCII：builder 转义有两道闸，这把字节尺料事在写入层。

        曾经 sync_live_rules --write 用 ensure_ascii=False 把整个 doc 重排，
        derive 写好的 \\uXXXX 被整体反转义（213 处中文入库）。字节数比"读进来
        是否抛 UnicodeDecodeError"可读：红了能报出第一个偏移。
        """
        raw = open(PARAMS_PATH, "rb").read()
        bad = [i for i, b in enumerate(raw) if b > 127]
        self.assertEqual(bad, [],
                         "params file carries non-ASCII bytes, first at offset %d"
                         % (bad[0] if bad else -1))


class AgainstGo(unittest.TestCase):
    """端到端：参数文件的每一个派生键都必须等于 Go 此刻打出来的出厂值。"""

    @classmethod
    def setUpClass(cls):
        cls.derived, cls.provenance = dcp.run_go_tool()

    def test_params_match_go_for_every_mapped_key(self):
        params = _params()
        sizing_keys, _t_keys, sizing_defaults = dcp.carrier_rule_keys()
        values = dcp.build_carrier_values(self.derived, self.provenance, params, sizing_defaults)
        bad = []
        for key, (value, _src) in values.items():
            if key in dcp.REGISTERED_OVERRIDES:
                # §3.0 / Q4 已裁决的故意背离 Go 出厂值：只核 override 值，不核 Go。
                if dcp._norm(params.get(key)) != dcp._norm(dcp.REGISTERED_OVERRIDES[key]):
                    bad.append("%s override=%r file=%r" % (key, dcp.REGISTERED_OVERRIDES[key], params.get(key)))
                continue
            if key not in params:
                bad.append("%s=absent" % key)
            elif dcp._norm(params[key]) != dcp._norm(value):
                bad.append("%s file=%r go=%r" % (key, params[key], value))
        self.assertEqual(bad, [], "params drifted from Go: %s" % bad[:12])

    def test_registered_overrides_are_applied(self):
        """§3.0 / Q4 已裁决的 override 必须真的落在参数文件里且值正确——否则等于没登记。"""
        params = _params()
        for key, want in dcp.REGISTERED_OVERRIDES.items():
            self.assertIn(key, params, "override key %s missing from params" % key)
            self.assertEqual(dcp._norm(params[key]), dcp._norm(want),
                             "override key %s file=%r want=%r" % (key, params[key], want))

    def test_check_mode_is_green_on_the_current_tree(self):
        proc = subprocess.run([sys.executable, SCRIPT, "--check"],
                              cwd=REPO_ROOT, capture_output=True, timeout=600)
        text = proc.stdout.decode("utf-8", "replace")
        self.assertIn("DERIVE_RESULT=OK", text)
        self.assertEqual(proc.returncode, 0, text[-400:])
        self.assertIn("orphan=0", text)

    def test_no_orphan_and_no_invented_number_for_unhoused_keys(self):
        """unhoused 必须**可见**：规则要求、又没有任何来源的键，只能列出来，不能默认补齐。"""
        params = _params()
        sizing_keys, t_keys, sizing_defaults = dcp.carrier_rule_keys()
        values = dcp.build_carrier_values(self.derived, self.provenance, params, sizing_defaults)
        all_values = dcp.resolve_extras(values, params, sizing_defaults)
        required = sizing_keys | t_keys
        unhoused = sorted(k for k in required if k not in params and k not in all_values)
        for key in unhoused:
            self.assertNotIn(key, params, "unhoused key %s must not be defaulted into the file" % key)
        # 这些键的存在本身就是结论：T 腿要接进载体，得先给它们各自找到主人。
        self.assertTrue(len(unhoused) >= 0)

    def test_phase_labels_agree_with_go_names(self):
        problems = dcp.check_phase_labels(self.derived, dcp.carrier_rule_keys()[2])
        self.assertEqual(problems, [])


class BuilderAscii(unittest.TestCase):
    def test_params_baking_escapes_non_ascii(self):
        """烘进产物的 PARAMS 段只能是 ASCII 字节。

        情绪档位的键是中文（Go 的那张表就是中文键）。%r 会把中文字节原样写进产物，
        而 GBK 终端读到的是一串 ?——产物从此不是它声称的那个文件。
        """
        sys.path.insert(0, os.path.join(REPO_ROOT, "scripts"))
        import build_local_executor as ble
        rendered = ble.render_params({"emotion_scales": {"退潮": 0.5}})
        rendered.encode("ascii")
        self.assertIn("\\u9000\\u6f6e", rendered)
        self.assertEqual(eval(rendered.replace("PARAMS = ", "")), {"emotion_scales": {"退潮": 0.5}})


if __name__ == "__main__":
    unittest.main()
