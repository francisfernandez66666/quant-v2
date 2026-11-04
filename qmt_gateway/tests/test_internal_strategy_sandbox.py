# test_internal_strategy_sandbox.py — QMT 内置策略产物（沙箱载体）的构建与接线锁（2026-11-04）。
#
# 背景：owner 2026-11-04 定载体——11-05 之后唯一仍在券商豁免内的自动下单通道是
# **QMT 内置策略**（信号完全在 QMT 内产生、不与外部环境交互），物理机上的普通进程退为
# 投递/回读/回报。于是同一套阈值与规则必须有**两个载体**，而两个载体绝不能各持一份：
# 构建器 scripts/build_local_executor.py --target sandbox 用 AST 从
# qmt_gateway/local_executor.py 抽出规则层、从 handler/store 生成状态词表，注进模板。
#
# 本文件锁四件事：
#   1. **注入是真替换**：模板里 XT_STATUS_CODES = {} 的占位行必须被覆盖掉。
#      今天实测过这个 bug——注入写成"在标记后追加"，占位行留在后面把词表覆盖成空，
#      产物运行时把所有委托状态都读成"未知"，撤单腿一单不撤：方向是 fail-closed，
#      形态却是"以为在跑"（本仓最贵的静默）。
#   2. **词表与规则层单源**：产物里的 XT_STATUS_CODES / TERMINAL_STATUSES 必须与
#      handler/store 的真源逐值相等；规则层函数必须真的在产物里（不是只剩模板壳）。
#   3. **闸会咬**：把词表改回空 dict、把规则层摘掉、把网络 import 塞回去，构建必须红。
#   4. **柜台表读不到 ⇒ 一轮什么都不下**（沙箱没有本地账本可兜底，这是它唯一的安全垫）。
#
# English: locks for the built-in-strategy artifact — injection really replaces the
# placeholder, the status vocabulary and rule layer come from the single sources,
# the gates bite, and an unreadable counter table places nothing.
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")


def build_sandbox(out_dir):
    """跑真构建器，返回 (rc, stdout, artifact_path)。"""
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


class SandboxArtifactTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.out = out
        cls.artifact = path
        cls.mod = load_module(path, "sandbox_artifact_under_test")

    def test_status_vocab_is_not_left_as_a_placeholder(self):
        """注入必须真的覆盖掉模板占位行。

        断言形态刻意取"运行时值"而不是 grep 源码：占位行留在注入块后面时，
        源码里看得到两份赋值，而**后一份赢**——只有读运行时值才会红。
        """
        self.assertEqual(self.mod.XT_STATUS_CODES, _source_codes(),
                         "产物里的状态词表与 handler 真源不同值（占位没被覆盖？）")
        self.assertEqual(tuple(self.mod.TERMINAL_STATUSES), _source_terminals())
        text = open(self.artifact, encoding="utf-8").read()
        self.assertEqual(text.count("\nXT_STATUS_CODES = {"), 1, "词表被赋值了两次")
        self.assertEqual(text.count("\nTERMINAL_STATUSES = ("), 1)

    def test_rule_layer_is_actually_injected(self):
        """两个载体共用一份规则实现：产物里必须真有那些函数。"""
        for name in ("rule_pullback_buy", "rule_take_profit", "rule_stop_loss",
                     "rule_breakdown_sell", "rule_no_cancel_window", "rule_ops_frequency_ok",
                     "normalize_order_status", "build_remark", "short_code_from_remark"):
            self.assertTrue(hasattr(self.mod, name), "规则层函数 %s 没进产物" % name)

    def test_remark_carries_the_baked_version(self):
        self.assertTrue(self.mod.STRATEGY_VERSION.startswith("v"))
        self.assertEqual(self.mod.build_remark("PB600000"),
                         "%s-PB600000" % self.mod.STRATEGY_VERSION)

    def test_unreadable_counter_table_places_nothing(self):
        """柜台 ORDER 表读不到 ⇒ 整轮不下单（沙箱没有本地账本可兜）。"""
        calls = []
        self.mod._SANDBOX_FNS.update({"passorder": lambda *a: calls.append(a),
                                      "cancel": lambda *a: calls.append(a),
                                      "get_trade_detail_data": lambda *a: (_ for _ in ()).throw(
                                          RuntimeError("counter down")),
                                      "get_account": lambda *a: "8001"})
        self.mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [], "cancel_tried": [],
                               "warned": {}, "last_round": 0.0})
        summary = self.mod.run_round(FakeContext({}), self.mod.now_cn())
        self.assertEqual(calls, [], "柜台表读不到却调了交易函数")
        self.assertEqual(summary["placed"], [])
        self.assertIn("unreadable", summary["reason"])

    def test_live_order_of_ours_is_not_re_sent(self):
        """柜台表里已有同短码的在途/已成 ⇒ 该意图不再发第二次（幂等只靠柜台）。"""
        class Row(object):
            pass

        row = Row()
        row.m_strOrderRemark = "%s-PB600000" % self.mod.STRATEGY_VERSION
        row.m_nOrderStatus = 50            # 已报
        row.m_strOrderSysID = "OID-A"
        row.m_strInstrumentID = "600000.SH"
        row.m_nOrderVolume = 1000
        row.m_nTradedVolume = 0
        row.m_dLimitPrice = 10.0
        row.m_nOrderTime = None
        calls = []
        self.mod._SANDBOX_FNS.update({"passorder": lambda *a: calls.append(a),
                                      "cancel": lambda *a: calls.append(a),
                                      "get_trade_detail_data": lambda acct, kind, table:
                                          [row] if table == "ORDER" else [],
                                      "get_account": lambda *a: "8001"})
        self.mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [], "cancel_tried": [],
                               "warned": {}})
        self.mod.run_round(FakeContext({"600000.SH": {
            "last_price": 9.7, "prev_close": 10.0, "volume": 100000,
            "ask1": 9.71, "bid1": 9.7}}), self.mod.now_cn())
        self.assertEqual(calls, [], "柜台里已有同一意图，仍然下单")


class FakeContext(object):
    """最小可用的 ContextInfo 替身：只给行情快照，历史一律取不到（规则应弃权而不是造数）。"""

    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, *args, **kwargs):
        return {}


def _source_codes():
    sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))
    import handler
    return handler.XT_STATUS_CODES


def _source_terminals():
    import store
    return store.TERMINAL_STATUSES


class SandboxGatesBiteTest(unittest.TestCase):
    """反证：三处注入被破坏时，构建必须红（不红就是摆设）。"""

    def _build_with(self, mutate, which="qmt_internal_strategy.py"):
        import glob
        import shutil
        work = tempfile.mkdtemp()
        # 构建器会读四件：模板 / 规则源 / 参数 / 状态词表真源（handler+store）。
        # 所以反证用的副本树必须把 qmt_gateway 的 py 一起带过去，否则三条反证都会
        # 因为"词表源不可用"而红在同一处——那种红证明不了任何一把锁有牙。
        os.makedirs(os.path.join(work, "qmt_gateway"), exist_ok=True)
        for src in glob.glob(os.path.join(REPO, "qmt_gateway", "*.py")):
            shutil.copyfile(src, os.path.join(work, "qmt_gateway", os.path.basename(src)))
        shutil.copyfile(os.path.join(REPO, "qmt_gateway", "local_executor_params.json"),
                        os.path.join(work, "qmt_gateway", "local_executor_params.json"))
        src = os.path.join(REPO, "qmt_gateway", which)
        dst = os.path.join(work, "qmt_gateway", which)
        open(dst, "w", encoding="utf-8").write(mutate(open(src, encoding="utf-8").read()))
        builder_dst = os.path.join(work, "scripts", "build_local_executor.py")
        os.makedirs(os.path.dirname(builder_dst), exist_ok=True)
        import shutil
        shutil.copyfile(BUILDER, builder_dst)
        proc = subprocess.run([sys.executable, builder_dst, "--target", "sandbox",
                               "--out-dir", tempfile.mkdtemp()],
                              capture_output=True, text=True, cwd=work)
        return proc.returncode, proc.stdout + proc.stderr

    def test_empty_status_placeholder_would_be_caught(self):
        """把模板里的词表标记摘掉 ⇒ 组装失败（而不是带着空词表发布）。"""
        rc, out = self._build_with(lambda t: t.replace("# ==== STATUS VOCAB END ====",
                                                       "# STATUS VOCAB END MOVED"))
        self.assertNotEqual(rc, 0, "标记被挪走却仍然构建成功＝这把锁没有牙：%s" % out[-400:])
        self.assertIn("sandbox_assemble_failed", out)

    def test_network_import_is_rejected(self):
        rc, out = self._build_with(lambda t: t.replace("def _pick(*values):",
                                                       "import socket\n\n\ndef _pick(*values):"))
        self.assertNotEqual(rc, 0, "网络 import 混进沙箱产物却构建成功：%s" % out[-400:])
        self.assertIn("sandbox_imports_not_allowed", out)

    def test_missing_rule_source_is_not_silently_skipped(self):
        rc, out = self._build_with(lambda t: t.replace("# ==== RULE LAYER BEGIN ====",
                                                       "# RULE LAYER BEGIN MOVED"),
                                   which="local_executor.py")
        self.assertNotEqual(rc, 0, "规则层标记丢失应当拒建：%s" % out[-400:])


    def test_missing_clock_import_is_caught_by_the_load_gate(self):
        """g7 装载自证的牙：把顶层 datetime import 摘掉 ⇒ 构建必须红。

        这正是今天真实踩过一次的形态：五道闸全绿，产物却在第一次 handlebar 上
        NameError——"部署了但什么都不报"。语法解析看不见未导入的名字，只有装载看得见。
        """
        rc, out = self._build_with(lambda t: t.replace("from datetime import datetime, timedelta, timezone\n", ""))
        self.assertNotEqual(rc, 0, "摘掉时钟 import 仍构建成功＝g7 是摆设：%s" % out[-400:])
        self.assertIn("sandbox_smoke_call_failed", out)


if __name__ == "__main__":
    unittest.main()
