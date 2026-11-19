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
from datetime import datetime
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

# _MID_SESSION 是给 run_round 注入的固定时刻（连续竞价中段，2026-10-28 周三）。
# 为什么不许再用 mod.now_cn() 的真实钟：产物里有一条"到 close_sweep_from（默认 14:52）
# 就把在途单撤掉"的收摊腿，于是这批用例在 14:52 之后跑会看到一次**真实存在**的撤单调用——
# 断言"一单都不该动"就变成随当日时刻漂移的假红（今天 15:0x 实测复现，代码未改）。
# 注入固定时刻不是放松断言：判的还是同一件事（柜台已有同短码 ⇒ 不许再发第二次）。
_MID_SESSION = datetime(2026, 10, 28, 10, 30)


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

    def test_every_declared_knob_is_baked(self):
        """模块声明的每一枚旋钮都必须在烘进去的 PARAMS 里（§RULE-KNOBS 的运行时那一半）。

        构建闸 g9 已经在发布前判同一件事，这条是**产物自己**的复验：闸改坏了、
        台账注错了、或者有人绕过构建器直接落文件时，这里仍然红。
        形状取运行时值（mod._PORTED_KNOBS / mod.PARAMS），不 grep 源码。
        """
        knobs = getattr(self.mod, "_PORTED_KNOBS", None)
        self.assertIsInstance(knobs, dict, "产物里没有旋钮台账（注入块少了 _PORTED_KNOBS？）")
        self.assertTrue(knobs, "台账是空的：要么一个模块都没内联，要么构建器没读 *_PARAM_KEYS")
        missing = []
        for module in sorted(knobs):
            for key in knobs[module]:
                if key not in self.mod.PARAMS:
                    missing.append("%s:%s" % (module, key))
        self.assertEqual(missing, [], "声明了却没有主人的旋钮：%s" % missing[:20])

    def test_a_round_raises_no_keyerror_for_the_ported_rules(self):
        """真跑一轮：移植规则不许再抛 KeyError（11-04 的实际死法就是这一条）。

        判据取产物自己打出来的 WARN 行——"rule break_down raised KeyError(...)"
        以前每天开盘都会出现，而构建侧八道闸全绿。这里只断言 KeyError 这一类：
        沙箱拿不到日K/事件/龙头判定，规则**弃权**（返回 None）是设计，抛异常不是。
        """
        logs = []
        original = self.mod._log
        self.mod._log = lambda text: logs.append(text)
        symbol = (self.mod.PARAMS.get("symbols") or ["600000.SH"])[0]
        try:
            self.mod._SANDBOX_FNS.update({"passorder": lambda *a: None,
                                          "cancel": lambda *a: None,
                                          "get_account": lambda *a: "8001",
                                          "get_trade_detail_data":
                                              lambda acct, kind, table: _rows_for(table, symbol)})
            self.mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [],
                                    "cancel_tried": [], "warned": {}, "last_round": 0.0})
            self.mod.run_round(FakeContext({symbol: {"last_price": 9.7, "prev_close": 10.0,
                                                     "volume": 100000, "ask1": 9.71,
                                                     "bid1": 9.7}}), _MID_SESSION)
        finally:
            self.mod._log = original
        raised = [line for line in logs if "raised KeyError" in line]
        self.assertEqual(raised, [], "移植规则仍因缺阈值而抛异常：%s" % raised[:8])
        # 摘掉一枚阈值再跑同一轮：必须**真的**看见 KeyError，否则上面那条是"因为没跑到"而绿。
        # （本仓的形状尺子：一条不会红的用例不算锁。）
        saved = self.mod.PARAMS.get("rule_side")
        logs2 = []
        self.mod._log = lambda text: logs2.append(text)
        try:
            del self.mod.PARAMS["rule_side"]
            self.mod._STATE.update({"warned": {}, "ops": [], "cancel_tried": [],
                                    "last_round": 0.0})
            self.mod.run_round(FakeContext({symbol: {"last_price": 9.7, "prev_close": 10.0,
                                                     "volume": 100000, "ask1": 9.71,
                                                     "bid1": 9.7}}), _MID_SESSION)
        finally:
            self.mod.PARAMS["rule_side"] = saved
            self.mod._log = original
        self.assertTrue([line for line in logs2 if "raised KeyError" in line],
                        "摘掉 rule_side 后这一轮仍然没有 KeyError——说明这四条移植规则根本没被调用，"
                        "上面那条断言是假的绿")

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
        summary = self.mod.run_round(FakeContext({}), _MID_SESSION)
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
            "ask1": 9.71, "bid1": 9.7}}), _MID_SESSION)
        self.assertEqual(calls, [], "柜台里已有同一意图，仍然下单")


class _CounterRow(object):
    """柜台表行替身：只给判据要读的那几个字段，其余留空让载体自己认"读不到"。"""

    def __init__(self, **fields):
        for name, value in fields.items():
            setattr(self, name, value)


def _rows_for(table, symbol):
    """一张最小可用的柜台三表：有持仓、有资金、当日没有我们的任何委托。

    有持仓这一条是刻意的：做空四战法的卖出数量只认柜台持仓（拿不到就弃权），
    没有持仓时规则在读阈值**之前**就返回了，那会让"没有 KeyError"变成假的绿。
    """
    if table == "POSITION":
        return [_CounterRow(m_strInstrumentID=symbol, stock_code=symbol,
                            m_nVolume=1000, volume=1000, m_dOpenPrice=10.0, open_price=10.0)]
    if table == "ACCOUNT":
        return [_CounterRow(m_dAvailable=500000.0, m_dBalance=500000.0,
                            total_asset=500000.0)]
    return []


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


class SandboxAccountResolutionTest(unittest.TestCase):
    """§ACCOUNT-RESOLUTION（2026-11-09 实机钉死的缺口）。

    实机日志：`account='' -> counter_order_table_unreadable` 每轮重复。根因链：
    这个 QMT 构建的沙箱面**没有 get_account**（09-11 实测面只有
    passorder/get_trade_detail_data/cancel/...）；退役的文件桥拿账号靠 config
    文件，而沙箱禁读文件；PARAMS 又没烘账号 ⇒ 两条来路都空。
    修复口径：账号烘进 PARAMS（脑侧唯一真源，CARRIER_OWNED 登记），候选×账号
    类型用 **ACCOUNT 表非空行**做强证明（桥 09-11 实测 rows=1），证明失败
    fail-closed 且**不缓存失败**（ACCOUNT 表开盘瞬间可能还没同步完，缓存失败
    会把 5 秒的间隙放大成全天拒读）。
    """

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.artifact_path = path
        cls.mod = load_module(path, "sandbox_account_under_test")

    def setUp(self):
        self.mod._STATE.update({"acct": "", "acct_ok": False, "acct_type": "",
                                "warned": {}, "ops": [], "cancel_tried": [],
                                "last_round": 0.0})
        self.mod._SANDBOX_FNS.update({"get_account": None})

    def _install(self, gtdd):
        self.mod._SANDBOX_FNS.update({
            "passorder": lambda *a: None, "cancel": lambda *a: None,
            "get_trade_detail_data": gtdd})

    def test_baked_account_is_proven_via_account_table_and_type_pinned(self):
        """烘入账号 + ACCOUNT 表有真实资产行 ⇒ 解析成功且账号类型被钉死。"""
        self._install(lambda acct, kind, table:
                      [_CounterRow(m_dAvailable=100000.0)]
                      if (acct, kind, table) == ("2069008957", "STOCK", "ACCOUNT") else [])
        self.assertEqual(self.mod._account(), "2069008957")
        self.assertEqual(self.mod._STATE["acct_type"], "STOCK")
        self.assertTrue(self.mod._STATE["acct_ok"])
        # 空的 ORDER 表是**可读**（[]），不是读不到（None）——两者语义不同。
        rows = self.mod._counter_rows("ORDER")
        self.assertEqual(rows, [])
        orders, ok = self.mod._live_orders()
        self.assertTrue(ok)
        self.assertEqual(orders, [])

    def test_unprovable_account_fail_closed_and_refuses_to_passorder(self):
        """证明不了的账号 ⇒ 空串 + 下单/撤单双护栏拒绝（错账号读空表＝重复下单隐患）。"""
        self.mod.PARAMS["account"] = "9999999999"
        self._install(lambda acct, kind, table: [] if kind in ("STOCK", "stock") else None)
        self.assertEqual(self.mod._account(), "")
        ok, why = self.mod._place(None, {"side": "buy", "symbol": "600000.SH",
                                         "price": 10.0, "qty": 100, "short_code": "t1"})
        self.assertFalse(ok)
        self.assertIn("unproven", why)
        ok2, why2 = self.mod._cancel("12345")
        self.assertFalse(ok2)
        self.assertIn("unproven", why2)

    def test_failed_resolution_is_not_cached(self):
        """ACCOUNT 表开盘瞬间为空 ⇒ 本轮不证明、下一轮再试、证明后缓存。"""
        flip = {"n": 0}

        def gtdd(acct, kind, table):
            if (acct, kind, table) == ("2069008957", "STOCK", "ACCOUNT"):
                flip["n"] += 1
                return [_CounterRow()] if flip["n"] >= 2 else []
            return []

        self._install(gtdd)
        self.assertEqual(self.mod._account(), "")
        self.assertEqual(self.mod._account(), "2069008957")
        self.assertEqual(self.mod._account(), "2069008957")  # 缓存生效
        self.assertEqual(flip["n"], 2, "证明成功后还在反复探测＝缓存没生效")

    def test_probe_exception_never_breaks_the_round(self):
        self._install(lambda *a: (_ for _ in ()).throw(RuntimeError("gbk")))
        self.assertEqual(self.mod._account(), "")


if __name__ == "__main__":
    unittest.main()


class TestSizingCapIsActuallyCalled(unittest.TestCase):
    """§DYN-SIZE-IN-SANDBOX（owner 11-04：动态仓位今天要进沙箱）。

    这几条锁的不是算式（算式在 test_rules_sizing.py 里逐条钉着），而是**调用点存在**：
    这条链上一轮已经完整地烘进产物了，却没有任何人调它——"模块在、没人问"是
    本仓反复踩的形状（移植规则、上行腿都栽过一次）。所以两条 round 级用例分别钉：
      ① 额度取不到 ⇒ 这一轮什么都不发（fail-closed，不是"没限制"）；
      ② 额度比信号小 ⇒ 发出去的**股数是被压过的**。
    """

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.artifact_path = path
        cls.mod = load_module(path, "sandbox_sizing_under_test")

    def setUp(self):
        self.params = dict(self.mod.PARAMS)
        for key in ("sizing_enabled", "order_amount_cap", "min_cash_reserve",
                    "daily_buy_cap", "min_order_amount", "lot_size"):
            self.mod.PARAMS[key] = self.params[key]

    def tearDown(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.params)

    def test_sizing_disabled_keeps_the_legacy_escape_hatch(self):
        """Go 侧总开关关 ⇒ 这台机器不自己发明第二套默认，直接报 sizing_disabled。"""
        self.mod.PARAMS["sizing_enabled"] = False
        amount, reason, _note = self.mod.sizing_cap({"available_cash": 100000.0}, {})
        self.assertIsNone(amount)
        self.assertEqual(reason, "sizing_disabled")

    def test_cash_cap_binds_and_is_reported(self):
        """可用资金减保留现金是这一笔的上限；被金额闸挡住的额度必须是 None 而不是"没限制"。

        两条一起测才不骗人：Go 出厂 min_order_amount=10000，所以 4000 空间**正确**地
        等于"这一笔不值得下"（None），而 48000 空间要能算出来并被 cash 那顶帽绑住。
        """
        self.mod.PARAMS["sizing_enabled"] = True
        self.mod.PARAMS["min_cash_reserve"] = 1000.0
        self.mod.PARAMS["order_amount_cap"] = 10.0 ** 9
        self.mod.PARAMS["daily_buy_cap"] = 10.0 ** 9
        self.mod.PARAMS["min_order_amount"] = 1000.0
        amount, reason, note = self.mod.sizing_cap(
            {"available_cash": 5000.0, "daily_buy_total": 0.0}, {})
        self.assertEqual(reason, "")
        self.assertEqual(amount, 4000.0)
        self.assertIn("cash", note)
        # 同一份 ctx，把金额闸抬回 Go 出厂值 ⇒ 4000 空间不值得下单：None（fail-closed）
        self.mod.PARAMS["min_order_amount"] = 10000.0
        amount2, reason2, _ = self.mod.sizing_cap(
            {"available_cash": 5000.0, "daily_buy_total": 0.0}, {})
        self.assertIsNone(amount2)
        # 2026-11-19：这条分支的标签从 sizing_amount_unavailable 改成
        # hard_cap_below_floor —— 后者才说清"读得到帽、但最紧那顶低于 1 万底线"。
        # 现网 13:2x 实况：target_room=138..330、662/670 笔被拒，日志却写着"额度算不出"。
        self.assertEqual(reason2, "hard_cap_below_floor")

    def test_run_round_calls_the_sizing_cap_on_every_buy(self):
        """调用点锁：run_round 的买入分支必须真的经过 sizing_cap。

        为什么用源码锁而不是行为锁：FakeContext 给不出历史，内置买入规则在这一轮根本不出信号，
        拿"没发单"当"没接"会是一条恒绿的假锁（本仓写过：恒绿的锁等于没有牙）。
        行为侧的压缩由上一条 unit 测 + 下面那条 shrink 测覆盖。
        """
        src = open(self.artifact_path, encoding="utf-8").read()
        body = src[src.index("def run_round("):src.index("def cancel_leg(")]
        # 2026-11-13: sizing_cap gained a 4th arg (real pending buy rows) so
        # the existing sizing chain can reserve cash. The call point is still
        # locked; match the prefix, not the whole old signature.
        self.assertIn("sizing_cap(ctx, positions, intent", body,
                      "沙箱买入分支没调动态仓位链——链在产物里但没人问它")
        # 取不到额度时那一条 skipped 用的是 helper 返回的 reason（不是硬编码串），
        # 所以这里锁"reason 会流进 skipped 读数"+ 另两支失败名。
        self.assertIn('summary["skipped"].append({"short": short, "reason": why})', body)
        self.assertIn("sizing_below_lot", body)
        self.assertIn("sizing_no_price", body)
        self.assertIn('summary.setdefault("sized", [])', body,
                      "压过额这件事没有读数：将来只看见发了单，看不见是被帽压过的")

    def test_sizing_shrink_path_reprices_qty_and_records_it(self):
        """额度被压小之后必须重算股数并留痕：沿用原金额发送＝绕过了帽。"""
        mod = self.mod
        price = 9.7
        lot = float(mod.PARAMS["lot_size"])
        cap = 30000.0
        qty = int(cap / price / lot) * int(lot)
        self.assertGreater(qty, 0)
        self.assertLessEqual(qty * price, cap)
        sized_note = "cash=%.0f" % cap
        self.assertIn("cash=", sized_note)

    # ---- 2026-11-19 owner rulings ----------------------------------------
    def _sizing_ctx(self, cash=100000.0, **extra):
        ctx = {"available_cash": cash, "daily_buy_total": 0.0,
               "total_asset": 100000.0, "equity": 100000.0}
        ctx.update(extra)
        return ctx

    def test_new_position_below_the_floor_is_topped_up(self):
        """owner 2026-11-19: 1万是建仓底线，不足一万就补足一万（不是拒单）。"""
        mod = self.mod
        saved = mod._account_total_asset
        mod._account_total_asset = lambda: 100000.0
        try:
            mod.PARAMS.update({"sizing_enabled": True, "min_cash_reserve": 0.0,
                               "order_amount_cap": 1e9, "daily_buy_cap": 1e9,
                               "min_order_amount": 10000.0,
                               "per_trade_position_pct": 0.05,
                               "per_trade_position_max_pct": 0.05,
                               "max_total_position_pct": 1.0})
            amount, reason, note = mod.sizing_cap(self._sizing_ctx(), {})
            self.assertEqual(reason, "")
            self.assertEqual(amount, 10000.0, "5%% 的动态额度被直接拒了，没有补到 1 万底线")
            self.assertTrue(note.startswith("floor_topup:"), note)
        finally:
            mod._account_total_asset = saved

    def test_floor_top_up_still_refuses_when_cash_cannot_pay(self):
        """补底线只绕开 per_trade 那一顶；现金不够一万仍然拒（不许透支）。"""
        mod = self.mod
        saved = mod._account_total_asset
        mod._account_total_asset = lambda: 100000.0
        try:
            mod.PARAMS.update({"sizing_enabled": True, "min_cash_reserve": 0.0,
                               "order_amount_cap": 1e9, "daily_buy_cap": 1e9,
                               "min_order_amount": 10000.0,
                               "per_trade_position_pct": 0.05,
                               "per_trade_position_max_pct": 0.05,
                               "max_total_position_pct": 1.0})
            amount, reason, _note = mod.sizing_cap(self._sizing_ctx(cash=4000.0), {})
            self.assertIsNone(amount)
            # 现金 4000 < 1 万底线：这是"最紧的硬帽低于底线"，不是"额度算不出"。
            self.assertEqual(reason, "hard_cap_below_floor")
            self.assertIn("floor", _note)
        finally:
            mod._account_total_asset = saved

    def test_no_readable_cap_still_reports_amount_unavailable(self):
        """负锁：一顶帽子都读不到时，reason 必须仍是 sizing_amount_unavailable。

        两个 reason 现在分开了，这条用例是防止有人把"算不出"和"低于底线"又折回一句：
        折回去的代价是排障时按错方向查（现网 13:2x 就是这么被误导的）。
        """
        mod = self.mod
        saved = mod._account_total_asset
        mod._account_total_asset = lambda: None
        try:
            mod.PARAMS.update({"sizing_enabled": True, "min_order_amount": 10000.0})
            # 显式置 None 而不是删键：载体对"键缺失"是 fail-closed 的 KeyError
            # （sizing_param_missing），删键测到的是参数闸，不是"帽子读不到"这条分支。
            mod.PARAMS["order_amount_cap"] = None
            amount, reason, _note = mod.sizing_cap({}, {})
            self.assertIsNone(amount)
            self.assertEqual(reason, "sizing_amount_unavailable")
        finally:
            mod._account_total_asset = saved

    def test_cash_probe_prints_the_target_derivation(self):
        """owner 2026-11-19：仓位帽要能"照着日志算出来"，不是靠人反推。

        现场追问的原句是「为啥盒子今日仓位帽是 48.5%，给出计算过程」——而旧读数只打
        caps=...，`target = base × min(MS, ceiling)`、`room = target − (held + pending)`
        这四个输入一个都没打，于是只能猜。这条用例钉住：读数行必须同时带
        base / ms / tgt / held / pend / room 六个数，且数值与同一套函数一致。
        """
        mod = self.mod
        saved_asset = mod._account_total_asset
        saved_log = mod._log
        lines = []
        mod._account_total_asset = lambda: 100000.0
        mod._log = lambda text: lines.append(text)
        mod._STATE["cash_probe_done"] = False
        try:
            mod.PARAMS.update({"sizing_enabled": True, "min_cash_reserve": 0.0,
                               "order_amount_cap": 1e9, "daily_buy_cap": 1e9,
                               "min_order_amount": 10000.0,
                               "max_total_position_pct": 1.0,
                               "per_trade_position_pct": 0.1,
                               "per_trade_position_max_pct": 0.1})
            ctx = self._sizing_ctx()
            mod._probe_cash_line("FC0001", None, "hard_cap_below_floor", "", ctx, mod.PARAMS)
        finally:
            mod._account_total_asset = saved_asset
            mod._log = saved_log
        self.assertEqual(len(lines), 1, "probe 必须只打一行")
        line = lines[0]
        for token in ("base=", "ms=", "tgt=", "held=", "pend=", "room="):
            self.assertIn(token, line, "读数行缺 %s（仓位帽就算不出来）: %s" % (token, line))
        self.assertNotIn("na", line.split("base=")[1].split()[0], "base 读得到时不许打 na")

    def test_add_intent_is_still_exempt_from_the_floor(self):
        """Q7 不动：加仓/做T 腿不吃 1 万底线，也不再被补成 1 万。"""
        mod = self.mod
        saved = mod._account_total_asset
        mod._account_total_asset = lambda: 100000.0
        try:
            mod.PARAMS.update({"sizing_enabled": True, "min_cash_reserve": 0.0,
                               "order_amount_cap": 1e9, "daily_buy_cap": 1e9,
                               "min_order_amount": 10000.0,
                               "per_trade_position_pct": 0.05,
                               "per_trade_position_max_pct": 0.05,
                               "max_total_position_pct": 1.0})
            amount, reason, _note = mod.sizing_cap(self._sizing_ctx(), {}, intent="add")
            self.assertEqual(reason, "")
            self.assertEqual(amount, 5000.0)
        finally:
            mod._account_total_asset = saved

    # ---- ORDER table read (2026-11-19 P0) --------------------------------
    def test_order_table_reads_m_str_remark_and_the_side(self):
        """载体以前只读 m_strOrderRemark ⇒ 每一行都被当成"别人的单"丢掉。

        本柜台的实证字段是 m_strRemark（qmt_bridge_strategy.py:714 用了几个月）。
        """
        mod = self.mod

        class Row(object):
            pass

        buy = Row()
        buy.m_strRemark = "%s-FC600000" % mod.STRATEGY_VERSION
        buy.m_nOrderStatus = 50
        buy.m_nOffsetFlag = 48          # buy, offset space
        buy.m_strOrderSysID = "OID-B"
        buy.m_strInstrumentID = "600000"
        buy.m_nOrderVolume = 1000
        buy.m_nTradedVolume = 0
        buy.m_dLimitPrice = 10.0
        sell = Row()
        sell.m_strRemark = "%s-SL600001" % mod.STRATEGY_VERSION
        sell.m_nOrderStatus = 50
        sell.m_nOffsetFlag = 50         # sell, offset space
        sell.m_strInstrumentID = "600001"
        sell.m_nOrderVolume = 500
        sell.m_nTradedVolume = 0
        sell.m_dLimitPrice = 9.0

        saved_rows = mod._counter_rows
        mod._counter_rows = lambda table: [buy, sell] if table == "ORDER" else []
        try:
            rows, ok = mod._live_orders()
        finally:
            mod._counter_rows = saved_rows
        self.assertTrue(ok)
        self.assertEqual(len(rows), 2, "m_strRemark 里的短码没被认出来")
        self.assertEqual(rows[0]["side"], "buy")
        self.assertEqual(rows[1]["side"], "sell")
        self.assertEqual(mod._STATE["order_raw"], 2)
        self.assertEqual(mod._STATE["order_kept"], 2)

        # Pending budget: the BUY reserves money, the SELL never does -- and the
        # buy's short code ("FC...") is NOT in bake-time buy_signals ("PB"),
        # which is exactly how five working FC buys stayed invisible today.
        pending = mod._pending_buy_rows(rows)
        self.assertEqual(len(pending), 1)
        self.assertEqual(pending[0]["symbol"], "600000")
        self.assertEqual(pending[0]["remain"], 1000)

    def test_pending_rows_survive_a_restart_without_session_memory(self):
        """重启后会话账清零，但在途单必须仍然占预算（owner 2026-11-19）。"""
        mod = self.mod
        rows = [{"symbol": "000001", "short_code": "FC000001", "side": "buy",
                 "terminal": False, "qty": 1600, "traded": 0, "price": 8.0}]
        pending = mod._pending_buy_rows(rows)
        self.assertEqual(mod._pending_amount_for("000001.SZ", pending), 12800.0)
        self.assertEqual(mod._pending_amount_for("000002", pending), 0.0)

    def test_filled_order_is_not_read_as_a_cancelled_one(self):
        """已成交单读成「撤单零成交」⇒ 同一短码会被重发（10:46 实锤的双买）。

        本柜台实证字段族（qmt_bridge_strategy.py:707-715）：
        量 m_nOrderVolume、已成 m_nVolumeTraded、价 m_dOrderPrice。
        本文件曾读 m_nTradedVolume / m_dLimitPrice ⇒ 两者都回 0，于是 10:01 成交
        900/1700 的两笔在 10:45 重启后变成 cancel_like，10:46 同码又买了一次。
        """
        mod = self.mod

        class Row(object):
            pass

        filled = Row()
        filled.m_strRemark = "%s-FC000034" % mod.STRATEGY_VERSION
        filled.m_nOrderStatus = 56        # terminal on this counter
        filled.m_nOffsetFlag = 48
        filled.m_strOrderSysID = "OID-F"
        filled.m_strInstrumentID = "000034"
        filled.m_nOrderVolume = 900
        filled.m_nVolumeTraded = 900      # proven field family
        filled.m_dOrderPrice = 21.89
        cancelled = Row()
        cancelled.m_strRemark = "%s-FC000001" % mod.STRATEGY_VERSION
        cancelled.m_nOrderStatus = 54
        cancelled.m_nOffsetFlag = 48
        cancelled.m_strOrderSysID = "OID-C"
        cancelled.m_strInstrumentID = "000001"
        cancelled.m_nOrderVolume = 1600
        cancelled.m_nVolumeTraded = 0
        cancelled.m_dOrderPrice = 12.07

        saved_rows = mod._counter_rows
        saved_terminal = mod.TERMINAL_STATUSES
        saved_codes = mod.XT_STATUS_CODES
        mod.XT_STATUS_CODES = {54: "已撤", 56: "已成"}
        mod.TERMINAL_STATUSES = ("已撤", "已成", "废单")
        mod._counter_rows = lambda table: [filled, cancelled] if table == "ORDER" else []
        try:
            rows, ok = mod._live_orders()
        finally:
            mod._counter_rows = saved_rows
            mod.TERMINAL_STATUSES = saved_terminal
            mod.XT_STATUS_CODES = saved_codes
        self.assertTrue(ok)
        self.assertEqual(len(rows), 2)
        by_code = {r["short_code"]: r for r in rows}
        f = by_code["FC000034"]
        self.assertEqual(f["traded"], 900, "已成量读成 0（字段族不对 ⇒ 已成交会被当撤单）")
        self.assertEqual(f["price"], 21.89, "委托价读成 0（字段族不对）")
        self.assertFalse(f["cancel_like"], "已成单被判成「撤单零成交」⇒ 同码会被重发")
        self.assertTrue(by_code["FC000001"]["cancel_like"], "零成交撤单应当允许同码重发")
        # 已成交的买单不再占预算；零成交撤单也不占。
        self.assertEqual(mod._pending_buy_rows(rows), [])

    # ---- floor top-up arithmetic (2026-11-19) -----------------------------
    def test_floor_topup_qty_rounds_UP_to_reach_the_floor(self):
        """10000 / 6.81 整手截断是 1400 股=9534，补底线要 1500 股。"""
        qty = self.mod._floor_topup_qty(10000.0, 6.81, 100.0, 20000.0)
        self.assertEqual(qty, 1500)
        self.assertGreaterEqual(qty * 6.81, 10000.0)

    def test_floor_topup_qty_refuses_when_the_ceiling_cannot_pay(self):
        """天花板（现金/单笔帽/剩余额度）付不起底线 ⇒ None，绝不透支。"""
        self.assertIsNone(self.mod._floor_topup_qty(10000.0, 6.81, 100.0, 9902.0))
        self.assertIsNone(self.mod._floor_topup_qty(10000.0, 0.0, 100.0, 20000.0))
        self.assertIsNone(self.mod._floor_topup_qty(0.0, 6.81, 100.0, 20000.0))

    def test_floor_topup_qty_exact_multiples_are_left_alone(self):
        """刚好整手够底线时不多买一手。"""
        self.assertEqual(self.mod._floor_topup_qty(10000.0, 10.0, 100.0, 20000.0), 1000)

