#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Builder and updater regression locks (SFRIEND-LOCAL batch 7, 2026-11-03).

The local executor is delivered as a BAKED artifact: a JSON parameter
file is rendered into the executor source, and the result is published
outside the repository working tree. Two properties have to hold, and
neither is covered by any other gate in this repo:

  BUILD SIDE
  (1) the five static gates actually BITE. A gate that never turns red is
     indistinguishable from a gate that is not there (S130's rule: a
     negative lock must be tested by planting a mine). So every gate is
     checked by injecting the exact violation it exists to catch.
  (2) a failing build leaves NO half-written artifact behind. A stale
     artifact on disk is worse than no artifact: the next deploy picks
     it up happily.
  (3) the artifact lands OUTSIDE the repository (the repo has a
     worktree-drift gate; writing a build product into the tree turns
     that gate red for a reason that has nothing to do with the change).
  (4) the artifact is pure ASCII and has no f-string, because it runs on a
     GBK console and crosses a PowerShell pipe.
  (5) the version constant reaches the order remark, because the remark is
     what the idempotency reconstruction is built from.

  UPDATE SIDE
  (6) the updater's three stages are all mandatory: backup, write (with
     the SHA self-proof), version read-back. A stage that can be
     skipped is a stage that will be skipped.
  (7) the updater is dry-run by default and only acts with -Apply.
  (8) a failed version read-back goes RED, writes the STOP_AUTO_UPDATE
     marker (so the automatic update path stops), and rolls back to the
     backup -- "we think we updated but nothing changed" is the failure
     mode this script exists to prevent.
  (9) the updater restarts ONLY the local executor process and never
     touches the QMT trading client.

The PowerShell assertions run only when pwsh is available; the
STRUCTURAL assertions (the three stages exist, the marker exists, the
"never touch QMT" rule) always run, because they are what a reviewer
reads. The structural check is the same one the runtime behaviour
depends on, so it is not a second ruler for the same judgement: the
runtime test proves the behaviour, the structural test proves the
stages cannot be quietly deleted.

(English: builder gate and updater stage regression locks.)
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
UPDATER = os.path.join(REPO, "scripts", "update_local_executor.ps1")
sys.path.insert(0, os.path.join(REPO, "scripts"))

import build_local_executor as builder  # noqa: E402


def _pwsh():
    """The best available PowerShell, or None.

    The gate in this repo looks for pwsh the same way (verify_changes.sh
    S171 (8)); when it is absent the behavioural assertions skip and the
    structural ones still run.
    """
    for cand in ("pwsh", "/tmp/pwsh7/pwsh", "/opt/homebrew/bin/pwsh",
                 "/usr/local/bin/pwsh"):
        found = shutil.which(cand) if os.sep not in cand else (
            cand if os.path.isfile(cand) else None)
        if found:
            return found
    return None


class BuilderFixture(unittest.TestCase):
    """Shared fixtures: the baked artifact and a scratch output dir."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.out = os.path.join(self.tmp.name, "out")
        self.params, self.version, error = builder.load_params(builder.DEFAULT_PARAMS)
        self.assertEqual(error, "")
        with open(builder.DEFAULT_SOURCE, "r", encoding="utf-8") as fh:
            self.source = fh.read()
        self.artifact = builder.bake(self.source, self.params, self.version)

    def tearDown(self):
        self.tmp.cleanup()

    def injected(self, old, new):
        """The artifact with one substitution applied (asserted to land)."""
        self.assertIn(old, self.artifact, "injection anchor not found: %r" % old)
        return self.artifact.replace(old, new, 1)

    def fired_gates(self, artifact):
        """Which gates refuse this artifact."""
        _ok, results = builder.run_gates(artifact)
        return [name for name, ok, _detail in results if not ok]


# ---------------------------------------------------------------------------
# (1) every static gate bites
# ---------------------------------------------------------------------------

class TestBuilderGatesBite(BuilderFixture):
    """A gate that never turns red is indistinguishable from no gate.

    Each test injects the exact violation one gate exists to catch and
    asserts that gate -- and only that gate -- turns red.
    """

    def test_g1_non_ascii_byte_is_refused(self):
        bad = self.injected("# ==== RULE LAYER BEGIN ====",
                            "# ==== RULE LAYER BEGIN ==== \u4e2d\u6587\u6ce8\u91ca")
        self.assertIn("g1_ascii", self.fired_gates(bad))

    def test_g2_network_import_is_refused(self):
        bad = self.injected("import json", "import json\nimport requests")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_network_call_is_refused(self):
        bad = self.injected("import json",
                            "import json\nimport requests\nrequests.get('http://x')")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_socket_is_refused(self):
        bad = self.injected("import json",
                            "import json\nimport socket\nsocket.socket()")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_process_spawning_is_refused(self):
        bad = self.injected("import json",
                            "import json\nimport subprocess\nsubprocess.run(['x'])")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_dynamic_code_execution_is_refused(self):
        bad = self.injected("def now_cn():", "def now_cn():\n    eval('1')")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_privileged_os_call_is_refused(self):
        bad = self.injected("os.makedirs(directory)",
                            "os.makedirs(directory)\n    os.system('x')")
        self.assertIn("g2_privileged_calls", self.fired_gates(bad))

    def test_g2_privileged_sqlite_shape_is_refused(self):
        """sqlite3 is allowed, but only the pinned shapes."""
        bad = self.injected("sqlite3.connect(self.path)",
                            "sqlite3.connect(self.path)\n    sqlite3.Connection.__init__()")
        gates = self.fired_gates(bad)
        self.assertIn("g2_privileged_calls", gates)

    def test_g3_fstring_is_refused(self):
        bad = self.injected(
            'print("EXEC_SELFTEST version=%s fingerprint=%s params_sha256=%s"',
            'print(f"EXEC_SELFTEST version={version}")')
        self.assertIn("g3_no_fstring", self.fired_gates(bad))

    def test_g4_missing_version_constant_is_refused(self):
        bad = self.injected('STRATEGY_VERSION = "%s"' % self.version,
                            "STRATEGY_VERSION = 1")
        self.assertIn("g4_version_in_remark", self.fired_gates(bad))

    def test_g4_bad_version_format_is_refused(self):
        bad = self.injected('STRATEGY_VERSION = "%s"' % self.version,
                            'STRATEGY_VERSION = "latest"')
        self.assertIn("g4_version_in_remark", self.fired_gates(bad))

    def test_g4_version_not_wired_into_the_remark_is_refused(self):
        """A version constant that no remark carries is decoration: the
        broker table could not be rebuilt from the remarks."""
        bad = self.injected('return "%s-%s" % (STRATEGY_VERSION, short_code)',
                            'return "%s-%s" % ("x", short_code)')
        self.assertIn("g4_version_in_remark", self.fired_gates(bad))

    def test_g5_bare_number_in_a_rule_function_is_refused(self):
        bad = self.injected('    if last > ref * params["pullback_ratio"]:',
                            '    if last > ref * params["pullback_ratio"] and 42 > 0:')
        self.assertIn("g5_no_bare_numbers", self.fired_gates(bad))

    def test_g5_bare_number_in_a_threshold_is_refused(self):
        bad = self.injected('    return recent < params["max_ops_per_min"]',
                            '    return recent < params["max_ops_per_min"] and 6 > 0')
        self.assertIn("g5_no_bare_numbers", self.fired_gates(bad))

    def test_the_clean_artifact_passes_every_gate(self):
        """The counterpart of the tests above: without an injection the
        artifact must build, or the gates would be useless."""
        self.assertEqual(self.fired_gates(self.artifact), [])

    def test_every_gate_is_registered(self):
        """All five gates must actually run (a gate that is defined but
        never called is a gate that never fires)."""
        names = [name for name, _func, _why in builder.GATES]
        self.assertEqual(len(names), 5)
        self.assertEqual(len(set(names)), 5)
        for expected in ("g1_ascii", "g2_privileged_calls", "g3_no_fstring",
                         "g4_version_in_remark", "g5_no_bare_numbers"):
            self.assertIn(expected, names)


# ---------------------------------------------------------------------------
# (2) a failing build leaves no half-written artifact
# ---------------------------------------------------------------------------

class TestRuleKnobGate(BuilderFixture):
    """g9 -- every knob a ported module DECLARES must be present in the baked PARAMS.

    这道闸存在的全部理由是一个已经发生过的形态：11-04 把 rules_form.py 的规则代码内联进
    两个载体，八道闸全绿、装载自证也绿（模块注册成功了），而它按 params["bd_min_bars"]
    读的 102 枚阈值**在参数文件里一枚都不存在**——第一轮真的调用规则就 raise KeyError。
    "模块到没到"与"它读的数有没有主人"是两件事，前者从来没人验过。

    判据取模块自己声明的那张表（FORM_PARAM_KEYS 等），不是这里再列一份清单：
    再列一份就是第二本账，而第二本账正是这条闸要消灭的东西。
    """

    def _injected(self, params=None):
        params = self.params if params is None else params
        return builder.inject_pported(builder.bake(self.source, params, self.version), params)

    def _module_source(self, name):
        with open(os.path.join(REPO, "qmt_gateway", name + ".py"), "r", encoding="utf-8") as fh:
            return fh.read()

    def test_declared_knobs_are_read_from_the_module_itself(self):
        """台账由模块自己报：rules_form 声明什么，构建器就读到什么（不多不少）。"""
        import rules_form
        keys = builder.ported_param_keys(self._module_source("rules_form"), "rules_form")
        self.assertEqual(keys, sorted(set(rules_form.FORM_PARAM_KEYS)),
                         "构建器读到的旋钮清单与模块声明的不一致（声明方式改了？）")
        self.assertIn("bd_min_bars", keys)
        # 兜底表那一形（SIZING_PARAM_KEYS = sorted(SIZING_DEFAULTS)）也必须认得，
        # 否则那个模块会"没有声明"，闸门对它是瞎的。
        sizing = builder.ported_param_keys(self._module_source("rules_sizing"), "rules_sizing")
        self.assertTrue(sizing, "rules_sizing 的声明读不出来：那道模块的旋钮从此没人管")

    def test_a_declaration_the_builder_cannot_read_is_not_skipped(self):
        """读不出清单的模块必须炸，而不是"当它没有旋钮"混过去。"""
        with self.assertRaises(ValueError):
            builder.ported_param_keys('RULES = {}\nMY_PARAM_KEYS = compute(1)\n', "rules_ghost")

    def test_rules_form_knobs_are_all_baked(self):
        """rules_form 声明的每一枚都必须在烘好的 PARAMS 里（本批交付的那一半）。"""
        _ok, detail = builder.gate_ported_rule_knobs(self._injected())
        form_missing = [token for token in str(detail).split(",")
                        if token.startswith("rules_form:")]
        self.assertEqual(form_missing, [], "rules_form 有声明了却没主人的旋钮：%s" % form_missing[:10])

    def test_a_missing_knob_fires_the_gate_and_names_it(self):
        """反证：往台账里加一枚 PARAMS 没有的键，闸必须红并**点名**是哪一枚。"""
        artifact = (self._injected() +
                    '\n_PORTED_KNOBS = {"rules_form": ["knob_nobody_baked_it"]}\n')
        ok, detail = builder.gate_ported_rule_knobs(artifact)
        self.assertFalse(ok)
        self.assertIn("knob_nobody_baked_it", detail)
        self.assertIn("rules_form", detail)

    def test_a_missing_ledger_is_itself_a_failure(self):
        """没有台账＝没验过，不是"通过"。（g7 的装载自证同取向：不报"没跑"当"跑了没问题"。）"""
        ok, detail = builder.gate_ported_rule_knobs(self.artifact)
        self.assertFalse(ok)
        self.assertIn("knob_table_absent", detail)

    def test_the_build_aborts_under_its_own_reason(self):
        """端到端：参数文件里少一枚阈值 ⇒ ABORT reason=rule_knobs_missing 且不留半份产物。

        用 sandbox 目标跑真构建器（executor 目标会先撞上漂移闸——改了 params 副本
        就与仓内烘好的副本不一致，那是另一道闸的正确行为，不该在这里混判）。
        """
        params = dict(self.params)
        params.pop("bd_min_bars", None)
        path = os.path.join(self.tmp.name, "knobs.json")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(__import__("json").dumps({"strategy_version": self.version,
                                               "params": params}))
        out_dir = os.path.join(self.tmp.name, "knob-out")
        proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                               "--params", path, "--out-dir", out_dir],
                              cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        text = proc.stdout.decode("utf-8", "replace")
        self.assertNotEqual(proc.returncode, 0, text[-600:])
        self.assertIn("BUILD_RESULT=ABORT reason=rule_knobs_missing", text)
        self.assertIn("rules_form:bd_min_bars", text)
        self.assertFalse(os.path.isdir(out_dir) and os.listdir(out_dir),
                         "拒建却把产物留在盘上：下一轮部署会把它当好文件捡起来")


# ---------------------------------------------------------------------------
# (2b) the knob gate is part of both carriers' published gate set
# ---------------------------------------------------------------------------

class TestKnobGateIsRegistered(unittest.TestCase):
    """闸定义了但没接进任何一条构建路径＝没有闸（§173 反证同族：恒绿的锁不算锁）。"""

    def test_both_targets_run_the_knob_gate(self):
        source = open(BUILDER, "r", encoding="utf-8").read()
        self.assertIn('("g9_rule_knobs_baked", gate_ported_rule_knobs', source)
        self.assertEqual(source.count("g9_rule_knobs_baked"), 2,
                         "g9 应当既在闸清单里、又在中止原因的分支里——少一处就是它不咬人")


# ---------------------------------------------------------------------------
# (2c) a failing build leaves no half-written artifact
# ---------------------------------------------------------------------------

class TestBuilderLeavesNoHalfProduct(BuilderFixture):
    """A stale artifact on disk is worse than no artifact: the next
    deploy picks it up happily."""

    def _run(self, params_path, out_dir):
        proc = subprocess.run(
            [sys.executable, BUILDER, "--params", params_path, "--out-dir", out_dir],
            cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return proc.returncode, proc.stdout.decode("utf-8", "replace")

    def test_a_bad_params_file_builds_nothing(self):
        bad = os.path.join(self.tmp.name, "bad.json")
        with open(bad, "w", encoding="utf-8") as fh:
            fh.write('{"params": {"a": 1}}')  # no strategy_version
        rc, out = self._run(bad, self.out)
        self.assertNotEqual(rc, 0)
        self.assertIn("BUILD_RESULT=ABORT", out)
        self.assertFalse(os.path.isdir(self.out) and os.listdir(self.out),
                         "a failed build left something behind")

    def test_a_params_file_that_is_not_json_builds_nothing(self):
        bad = os.path.join(self.tmp.name, "bad.json")
        with open(bad, "w", encoding="utf-8") as fh:
            fh.write("{not json")
        rc, out = self._run(bad, self.out)
        self.assertNotEqual(rc, 0)
        self.assertIn("BUILD_RESULT=ABORT", out)
        self.assertFalse(os.path.isdir(self.out) and os.listdir(self.out))

    def test_a_source_that_cannot_be_baked_builds_nothing(self):
        """A source without the baked-params markers cannot be baked."""
        broken = os.path.join(self.tmp.name, "broken.py")
        with open(broken, "w", encoding="utf-8") as fh:
            fh.write("print('no markers here')\n")
        rc, out = self._run(builder.DEFAULT_PARAMS, self.out)
        self.assertEqual(rc, 0)  # the default source is fine
        # now with the broken source
        proc = subprocess.run(
            [sys.executable, BUILDER, "--params", builder.DEFAULT_PARAMS,
             "--source", broken, "--out-dir", self.out],
            cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("BUILD_RESULT=ABORT", proc.stdout.decode("utf-8", "replace"))

    def test_a_successful_build_writes_the_artifact_and_its_digest(self):
        """A successful build publishes exactly one artifact, and the
        digest it reports must be the digest of that file.

        The digest is what the updater compares against, so a builder
        that reported a digest of something else would hand the updater
        a checksum that can never match.
        """
        rc, out = self._run(builder.DEFAULT_PARAMS, self.out)
        self.assertEqual(rc, 0, out)
        self.assertIn("BUILD_RESULT=OK", out)
        # 闸数从 6 到 7：新增的 g7e_executor_loads 是把产物**真的 import 一遍**。
        # 它之所以必须存在：源文件解析正常、六道静态闸全绿，烘出来的产物却在装载时
        # NameError（内联块里的注册循环排在它调用的函数定义之前）。一个装不起来的载体
        # 不是"算错"，是"什么都不说"——那正是本仓说过的"声称已做"形状。
        # 闸数从 7 到 8：再加 g9_rule_knobs_baked（移植规则声明的每枚旋钮都必须烘进 PARAMS）。
        # 11-04 的实际形态就是"模块到了、注册成功、七道闸全绿，而它读的 102 枚阈值一枚都没有"，
        # 装载自证看不见这件事——只有第一轮真的调用规则才 raise KeyError。
        # 数量之外还按名字断言，摘掉任意一道闸都会红，而不是只红在计数上。
        self.assertIn("BUILD_GATES gates=8 all_passed=1", out)
        self.assertTrue(hasattr(builder, "gate_executor_loads"),
                        "executor load gate removed: a non-importable artifact would pass again")
        self.assertTrue(hasattr(builder, "gate_ported_rule_knobs"),
                        "knob gate removed: a rule whose thresholds were never baked would pass again")
        names = os.listdir(self.out)
        self.assertEqual(len(names), 1)
        artifact = os.path.join(self.out, names[0])
        with open(artifact, "rb") as fh:
            data = fh.read()
        self.assertEqual(sum(1 for c in data if c > 127), 0)
        import hashlib
        digest = hashlib.sha256(data).hexdigest()
        match = re.search(r"sha256=(\w+)", out)
        self.assertIsNotNone(match)
        self.assertEqual(match.group(1), digest)
        self.assertIn(self.version, names[0])

    def test_the_artifact_name_carries_the_version(self):
        rc, out = self._run(builder.DEFAULT_PARAMS, self.out)
        self.assertEqual(rc, 0, out)
        name = os.listdir(self.out)[0]
        self.assertIn(self.version, name)


# ---------------------------------------------------------------------------
# (3) the artifact lands outside the repository
# ---------------------------------------------------------------------------

class TestBuilderPublishesOutsideTheRepo(BuilderFixture):
    """The repo has a worktree-drift gate: a build product inside the
    working tree turns that gate red for a reason that has nothing to do
    with the change under review."""

    def test_the_builder_refuses_to_publish_into_the_repository(self):
        inside = os.path.join(REPO, "build_out_should_not_exist")
        proc = subprocess.run(
            [sys.executable, BUILDER, "--params", builder.DEFAULT_PARAMS,
             "--out-dir", inside],
            cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        out = proc.stdout.decode("utf-8", "replace")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("out_dir_inside_repository", out)
        self.assertFalse(os.path.isdir(inside), "the builder wrote into the repo")

    def test_the_default_out_dir_is_outside_the_repository(self):
        default = os.path.abspath(builder.DEFAULT_OUT_DIR)
        self.assertFalse(default.startswith(os.path.abspath(REPO) + os.sep),
                         "the default output directory is inside the repository")
        self.assertFalse(default == os.path.abspath(REPO))

    def test_the_out_dir_can_be_overridden_by_environment(self):
        """The env var is the documented escape hatch for a caller that
        wants the artifact somewhere else."""
        target = os.path.join(self.tmp.name, "env-out")
        env = dict(os.environ)
        env["LOCAL_EXECUTOR_OUT_DIR"] = target
        proc = subprocess.run(
            [sys.executable, BUILDER, "--params", builder.DEFAULT_PARAMS],
            cwd=REPO, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        out = proc.stdout.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 0, out)
        self.assertTrue(os.path.isdir(target))
        self.assertTrue(os.listdir(target))


# ---------------------------------------------------------------------------
# (4) + (5) artifact shape
# ---------------------------------------------------------------------------

class TestArtifactShape(BuilderFixture):
    """The artifact is pure ASCII, has no f-string, and carries the
    version in the remark."""

    def test_the_artifact_is_pure_ascii(self):
        data = self.artifact.encode("utf-8")
        self.assertEqual(sum(1 for c in data if c > 127), 0)

    def test_the_artifact_has_no_fstring(self):
        import ast
        tree = ast.parse(self.artifact)
        for node in ast.walk(tree):
            self.assertNotIsInstance(node, ast.JoinedStr,
                                     "the artifact contains an f-string")

    def test_the_repository_executor_source_is_pure_ascii(self):
        """The source that gets baked must be ASCII too: it is the same
        file, and the artifact inherits whatever it contains."""
        with open(builder.DEFAULT_SOURCE, "rb") as fh:
            data = fh.read()
        self.assertEqual(sum(1 for c in data if c > 127), 0)

    def test_the_params_file_is_the_single_source_of_the_baked_table(self):
        """Baking the JSON must reproduce the table in the artifact --
        the parameters live in one place, not two."""
        table = re.search(r"^PARAMS = \{$.*?^\}$", self.artifact,
                          re.M | re.S)
        self.assertIsNotNone(table)
        import ast
        baked = ast.literal_eval(table.group(0).split("=", 1)[1].strip())
        self.assertEqual(baked, self.params)

    def test_the_version_reaches_the_remark(self):
        self.assertIn('STRATEGY_VERSION = "%s"' % self.version, self.artifact)
        # the remark builder must USE the constant, not merely sit near it
        body = self.artifact.split("def build_remark(", 1)[1].split("\ndef ", 1)[0]
        self.assertIn("STRATEGY_VERSION", body,
                      "build_remark does not use the version constant")

    def test_the_params_json_is_valid_and_complete(self):
        """The parameter file must carry BOTH the thresholds and the
        version: the version is what the order remark carries."""
        import json
        with open(builder.DEFAULT_PARAMS, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        self.assertIn("params", doc)
        self.assertTrue(doc["params"])
        self.assertIn("strategy_version", doc)
        self.assertTrue(re.match(r"^v\d{2}-\d{8}$", doc["strategy_version"]))
        self.assertEqual(doc["params"], self.params)
        self.assertEqual(doc["strategy_version"], self.version)


# ---------------------------------------------------------------------------
# (6) + (7) + (9) the updater's structure (always checked)
# ---------------------------------------------------------------------------

class TestUpdaterStructure(unittest.TestCase):
    """The three stages are mandatory. These assertions run everywhere
    (no PowerShell needed), because they are what a reviewer reads when
    checking that a stage was not quietly deleted."""

    @classmethod
    def setUpClass(cls):
        with open(UPDATER, "r", encoding="utf-8") as fh:
            cls.text = fh.read()

    def test_the_three_stages_are_present(self):
        for stage in ("UPDATE_STAGE=BACKUP", "UPDATE_STAGE=WRITE",
                      "UPDATE_STAGE=VERSION"):
            self.assertIn(stage, self.text, "the updater lost the %s stage" % stage)

    def test_the_stages_run_in_the_mandatory_order(self):
        """Backup, then write, then version read-back.

        The order is the whole point: a version read-back before the
        write would read the OLD file and call the update a success.

        The indices are taken from the EXECUTABLE lines (the Write-Host
        calls), not from the header comment, which names the same
        anchors in prose and would make this test prove nothing.
        """
        backup = self.text.index('Write-Host ("UPDATE_STAGE=BACKUP')
        write = self.text.index('Write-Host ("UPDATE_STAGE=WRITE')
        version = self.text.index('Write-Host ("UPDATE_STAGE=VERSION')
        self.assertLess(backup, write)
        self.assertLess(write, version)

    def test_the_sha_is_proved_three_ways(self):
        """Source bytes, written bytes, and bytes re-read from disk.

        Two readings can agree by accident (same buffer); three cannot.
        """
        self.assertIn("$sourceShaHex", self.text)
        self.assertIn("$writtenShaHex", self.text)
        self.assertIn("$rereadShaHex", self.text)
        self.assertIn("sha_mismatch_source_vs_written", self.text)
        self.assertIn("sha_mismatch_written_vs_reread", self.text)

    def test_a_backup_is_taken_before_the_write(self):
        """The backup copy must happen BEFORE the new file is written.

        The indices are taken from the executable lines, not from the
        header comment (which names the anchors in prose).
        """
        backup_at = self.text.index("Copy-Item $installed $backup")
        write_at = self.text.index("[System.IO.File]::WriteAllBytes($installed")
        self.assertLess(backup_at, write_at,
                        "the write happens before the backup: a failed "
                        "write would leave no artifact to roll back to")

    def test_the_version_readback_writes_the_stop_marker(self):
        """A failed read-back must stop the automatic updates too, not
        just this one run."""
        self.assertIn("UPDATE_STOP_AUTO_UPDATE=1", self.text)
        self.assertIn("STOP_AUTO_UPDATE", self.text)
        self.assertIn("Stop-AutoUpdate", self.text)

    def test_the_marker_blocks_later_updates(self):
        self.assertIn("stop_auto_update_marker_present", self.text)

    def test_a_failed_stage_rolls_back(self):
        """Every abort path after the backup restores the backup, so the
        box is never left half-updated."""
        self.assertIn("if ($backup) { Copy-Item $backup $installed -Force }",
                      self.text)

    def test_the_updater_is_dry_run_by_default(self):
        """No -Apply, no write."""
        self.assertIn("[switch]$Apply", self.text)
        dry = self.text.index("if (-not $Apply)")
        write = self.text.index("[System.IO.File]::WriteAllBytes($installed")
        self.assertLess(dry, write)

    def test_the_updater_never_touches_the_qmt_client(self):
        """The QMT trading client belongs to another leg.

        This is the "scope discipline" lock: the updater may restart the
        LOCAL EXECUTOR and nothing else. No client task, no client
        process, no GUI automation.
        """
        lowered = self.text.lower()
        for banned in ("xtitclient", "qmtf-client", "qmtf-gateway-ensure",
                       "sendkeys", "uiautomation"):
            self.assertNotIn(banned, lowered,
                             "the updater references %r" % banned)

    def test_the_updater_restarts_only_the_local_executor(self):
        self.assertIn("scope=local_executor_only", self.text)
        self.assertIn("$ProcessName", self.text)

    def test_the_abort_anchor_carries_step_and_reason(self):
        self.assertIn("UPDATE_RESULT=ABORT step={0} reason={1}", self.text)

    def test_the_usage_documents_the_rollback_path(self):
        """The first question an operator asks is "how do I undo it"."""
        self.assertIn("ROLLBACK PATH", self.text)
        self.assertIn("pre-update", self.text)

    def test_the_updater_source_is_pure_ascii(self):
        """It runs on a box whose console may be a GBK code page."""
        with open(UPDATER, "rb") as fh:
            data = fh.read()
        self.assertEqual(sum(1 for c in data if c > 127), 0)


# ---------------------------------------------------------------------------
# (8) the updater's behaviour (needs PowerShell)
# ---------------------------------------------------------------------------

class TestUpdaterBehaviour(unittest.TestCase):
    """The updater's real behaviour, exercised with pwsh.

    When pwsh is absent these skip (the repo's own gate takes the same
    position: "the green of this run does not include a PowerShell
    judgement"). The structural assertions above still run everywhere.
    """

    @classmethod
    def setUpClass(cls):
        cls.pwsh = _pwsh()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.target = os.path.join(self.tmp.name, "target")
        os.makedirs(self.target)
        self.source = os.path.join(self.tmp.name, "new.py")
        with open(self.source, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v02-20261104"\nX = 1\n')
        self.installed = os.path.join(self.target, "local_executor.py")
        with open(self.installed, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v01-20261103"\nOLD = "keep"\n')

    def tearDown(self):
        self.tmp.cleanup()

    def _run(self, args):
        proc = subprocess.run(
            [self.pwsh, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
             UPDATER, "-Source", self.source, "-Target", self.target] + args,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return proc.returncode, proc.stdout.decode("utf-8", "replace")

    def _require_pwsh(self):
        if not self.pwsh:
            self.skipTest("pwsh is not available on this machine")

    def test_the_dry_run_changes_nothing(self):
        self._require_pwsh()
        before = open(self.installed, "rb").read()
        rc, out = self._run([])
        self.assertEqual(rc, 0, out)
        self.assertIn("UPDATE_RESULT=DRYRUN", out)
        self.assertEqual(open(self.installed, "rb").read(), before)
        self.assertEqual(os.listdir(self.target), ["local_executor.py"])

    def test_the_apply_path_runs_all_three_stages(self):
        self._require_pwsh()
        rc, out = self._run(["-Apply", "-SkipRestart"])
        self.assertEqual(rc, 0, out)
        self.assertIn("UPDATE_STAGE=BACKUP ok=1", out)
        self.assertIn("UPDATE_STAGE=WRITE ok=1", out)
        self.assertIn("UPDATE_STAGE=VERSION ok=1", out)
        self.assertIn("UPDATE_RESULT=OK", out)

    def test_the_apply_path_installs_the_new_version(self):
        self._require_pwsh()
        rc, out = self._run(["-Apply", "-SkipRestart"])
        self.assertEqual(rc, 0, out)
        self.assertIn('STRATEGY_VERSION = "v02-20261104"',
                      open(self.installed, "r", encoding="utf-8").read())

    def test_the_apply_path_leaves_a_backup(self):
        self._require_pwsh()
        rc, out = self._run(["-Apply", "-SkipRestart"])
        self.assertEqual(rc, 0, out)
        backups = [n for n in os.listdir(self.target) if ".pre-update-" in n]
        self.assertEqual(len(backups), 1, "no backup was kept: %r" % os.listdir(self.target))
        self.assertIn('STRATEGY_VERSION = "v01-20261103"',
                      open(os.path.join(self.target, backups[0]), "r",
                           encoding="utf-8").read())

    def test_a_version_mismatch_rolls_back_and_goes_red(self):
        """The pinned version is what the operator says should be running.

        A file that says something else is not that build -- even when
        its bytes hashed correctly.
        """
        self._require_pwsh()
        rc, out = self._run(["-ExpectedVersion", "v07-20260707", "-Apply",
                             "-SkipRestart"])
        self.assertNotEqual(rc, 0, out)
        self.assertIn("UPDATE_RESULT=ABORT", out)
        self.assertIn("version_mismatch", out)
        # rolled back: the old file is back in place
        self.assertIn('STRATEGY_VERSION = "v01-20261103"',
                      open(self.installed, "r", encoding="utf-8").read())

    def test_a_version_mismatch_writes_the_stop_marker(self):
        """Not just this run: the automatic updates stop too."""
        self._require_pwsh()
        rc, out = self._run(["-ExpectedVersion", "v07-20260707", "-Apply",
                             "-SkipRestart"])
        self.assertNotEqual(rc, 0, out)
        self.assertIn("UPDATE_STOP_AUTO_UPDATE=1", out)
        marker = os.path.join(self.target, "STOP_AUTO_UPDATE")
        self.assertTrue(os.path.isfile(marker), "the stop marker was not written")

    def test_the_stop_marker_blocks_the_next_update(self):
        self._require_pwsh()
        marker = os.path.join(self.target, "STOP_AUTO_UPDATE")
        with open(marker, "w", encoding="utf-8") as fh:
            fh.write("reason=test\n")
        rc, out = self._run(["-Apply", "-SkipRestart"])
        self.assertNotEqual(rc, 0, out)
        self.assertIn("stop_auto_update_marker_present", out)
        # and the installed file was NOT touched
        self.assertIn('STRATEGY_VERSION = "v01-20261103"',
                      open(self.installed, "r", encoding="utf-8").read())

    def test_a_source_without_a_version_is_refused(self):
        self._require_pwsh()
        broken = os.path.join(self.tmp.name, "broken.py")
        with open(broken, "w", encoding="utf-8") as fh:
            fh.write("X = 1\n")
        proc = subprocess.run(
            [self.pwsh, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
             UPDATER, "-Source", broken, "-Target", self.target, "-Apply",
             "-SkipRestart"],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        out = proc.stdout.decode("utf-8", "replace")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("source_version_unreadable", out)
        # nothing was written
        self.assertIn('STRATEGY_VERSION = "v01-20261103"',
                      open(self.installed, "r", encoding="utf-8").read())

    def test_a_missing_source_is_refused(self):
        self._require_pwsh()
        proc = subprocess.run(
            [self.pwsh, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
             UPDATER, "-Source", os.path.join(self.tmp.name, "nope.py"),
             "-Target", self.target, "-Apply", "-SkipRestart"],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        out = proc.stdout.decode("utf-8", "replace")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("source_absent", out)


# ---------------------------------------------------------------------------
# the deploy package carries the new files
# ---------------------------------------------------------------------------

class TestDeployPackageCarriesTheExecutor(unittest.TestCase):
    """The one-click package must ship the executor, its parameters, its
    builder and its updater -- otherwise the friend installs a package
    that cannot run the leg it is supposed to install."""

    def test_the_whitelist_lists_the_executor_files(self):
        with open(os.path.join(REPO, "scripts", "build_friend_package.sh"),
                  "r", encoding="utf-8") as fh:
            text = fh.read()
        for name in ("local_executor.py", "local_executor_params.json",
                     "install_local_executor.ps1", "build_local_executor.py",
                     "update_local_executor.ps1"):
            self.assertIn(name, text, "the package whitelist lost %r" % name)

    def test_the_executor_installer_exists_and_is_parseable(self):
        path = os.path.join(REPO, "deploy", "qmt-friend",
                            "install_local_executor.ps1")
        self.assertTrue(os.path.isfile(path))
        with open(path, "rb") as fh:
            data = fh.read()
        self.assertTrue(data.startswith(b"\xef\xbb\xbf"),
                        "the installer needs the UTF-8 BOM (PS 5.1 reads it as ANSI otherwise)")

    def test_the_installer_is_dry_run_by_default(self):
        path = os.path.join(REPO, "deploy", "qmt-friend",
                            "install_local_executor.ps1")
        with open(path, "r", encoding="utf-8-sig") as fh:
            text = fh.read()
        self.assertIn("[switch]$Apply", text)
        self.assertIn("LOCALEXEC_RESULT=DRYRUN", text)
        self.assertIn("LOCALEXEC_RESULT=OK", text)
        self.assertIn("LOCALEXEC_RESULT=ABORT step={0} reason={1}", text)

    def test_the_installer_reuses_the_existing_login_path(self):
        """No new server route: the account comes from the config that
        the existing enrol flow already wrote."""
        path = os.path.join(REPO, "deploy", "qmt-friend",
                            "install_local_executor.ps1")
        with open(path, "r", encoding="utf-8-sig") as fh:
            text = fh.read()
        self.assertIn("config.xt.json", text)
        self.assertIn("account_tokens", text)
        for banned in ("/api/auth/enroll", "/api/auth/register", "Invoke-RestMethod"):
            self.assertNotIn(banned, text,
                             "the installer grew a new server call: %r" % banned)

    def test_the_uninstaller_keeps_the_executor_ledger(self):
        """The executor's own state db is the evidence of what it sent."""
        path = os.path.join(REPO, "deploy", "qmt-friend", "uninstall.ps1")
        with open(path, "r", encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("executor_state.db", text)
        self.assertIn("executor_kept_ledger", text)
        # The PURGE block (the one behind -PurgeLedger) must NOT touch the
        # executor's ledger: the executor's state db is evidence of what
        # it sent, exactly like the gateway's data.db. Only the gateway
        # ledger may ever be purged, and only behind an explicit confirm.
        purge_block = text.split('if ($PurgeLedger) {', 1)[1]
        purge_block = purge_block.split("UNINSTALL_LEDGER_PURGED", 1)[0]
        self.assertNotIn("executor_state.db", purge_block,
                         "the executor's ledger must not be purgeable")
        self.assertIn("data.db", purge_block,
                      "the gateway ledger purge path is gone")


if __name__ == "__main__":
    unittest.main()
