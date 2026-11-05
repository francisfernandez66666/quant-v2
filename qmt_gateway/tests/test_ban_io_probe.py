# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_ban_io_probe.py — BAN_IO 禁令生效自证（2026-11-05 批）。

背景（现网实测，不是推测）：券商 11-03 公告"11-05 起关闭 QMT 内置策略的本地文件读写"，
但 11-05 早上 09:5x 取证时这条桥的上行文件 5 秒前还在被写、`queued_connected=True`，
而终端 exe 是 **2026-08-07 那版（FileVersion 2.1.26.0）**——限制最可能是随新版终端下发的，
不是按日历在服务端切。也就是说：**断链的触发器是一次没人盯着的客户端升级，而不是一个日期。**

于是这条探针只买一样东西：**切换那一刻的归因**。整条上行就是文件，所以
"策略写不出文件"、"策略没在跑"、"今天就是没事件"三件事从外面看**完全同一副样子**——一个静默的文件。
而这三者的修法各不相同（换通道 / 重启终端 / 什么都不用做）。

本文件锁的就是这个分辨力，四条判据都是形状而非措辞：
  1. 探针**一进程一次**：每次 handlebar 都写一行会把 5MB 轮转预算花在诊断上；
  2. 事件**能被收到＝写成功**（它只可能在写成功时存在），网关不许把它读成"未知/失败"；
  3. 写失败时**必须留一行 BAN_IO**，且这一行走 trace 那条独立通道——因为报告通道正是失败的那条；
  4. 网关读数三态互不冒充：`ok` / `denied` / `never_seen`，缺省**不判红不熔断**（观测腿不许有钱的动作）。
English: locks for the BAN_IO self-check — one-shot, receipt proves writability, failure leaves a
trace line on the other channel, and the three readout states never substitute for one another.
"""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

import gateway as gw_mod  # noqa: E402
import qmt_bridge_strategy as bs  # noqa: E402


def _reset_probe():
    """把模块级的一次性状态清掉（测试之间必须互不污染，否则"一进程一次"测不出来）。"""
    bs.IO_SELF_CHECK.update({"done": False, "report_ok": None, "trace_ok": None,
                             "reason": "", "at": ""})


class TestBridgeProbe(unittest.TestCase):
    """1/2/3：桥侧探针的写入形状与失败留痕。"""

    def setUp(self):
        _reset_probe()
        # conftest.py 已把 QMT_BRIDGE_DIR 指到临时目录；这里只保证路径就是那两个文件
        self.report = bs.REPORT_PATH
        self.trace = bs.TRACE_PATH
        for p in (self.report, self.trace):
            if os.path.exists(p):
                os.remove(p)

    def _report_lines(self):
        if not os.path.exists(self.report):
            return []
        with open(self.report, "rb") as fh:
            return [l for l in fh.read().decode("utf-8", "replace").splitlines() if l.strip()]

    def _trace_text(self):
        if not os.path.exists(self.trace):
            return ""
        with open(self.trace, "rb") as fh:
            return fh.read().decode("utf-8", "replace")

    def test_probe_writes_one_parseable_event(self):
        out = bs.probe_strategy_io()
        lines = self._report_lines()
        self.assertEqual(len(lines), 1, "探针应当且只应当写一行上报")
        ev = json.loads(lines[0])
        self.assertEqual(ev["type"], "io_selfcheck")
        self.assertTrue(ev["day"].startswith("20"), "day 必须是日期形态（网关按它归日）")
        self.assertEqual(ev["src"], "qmt_bridge_strategy")
        self.assertIs(out["report_ok"], True)
        # 上报行必须纯 ASCII：沙箱默认码页是 GBK，非 ASCII 字节会被换成 '?'（既有那条事故）
        with open(self.report, "rb") as fh:
            self.assertTrue(all(b < 128 for b in fh.read()), "上报行含非 ASCII 字节")

    def test_probe_is_one_shot_per_process(self):
        bs.probe_strategy_io()
        bs.probe_strategy_io()
        bs.probe_strategy_io()
        self.assertEqual(len(self._report_lines()), 1,
                         "探针按进程只跑一次；每次都写＝诊断把上行文件吃掉")

    def test_write_denied_still_leaves_a_trace_line(self):
        """报告通道失败时，归因必须从**另一条通道**出来——否则禁令生效当天我们什么也看不见。"""
        real_report = bs._report
        bs._report = lambda payload: False
        try:
            out = bs.probe_strategy_io()
        finally:
            bs._report = real_report
        self.assertIs(out["report_ok"], False)
        self.assertEqual(self._report_lines(), [], "写被拒却落下了上报行")
        trace = self._trace_text()
        self.assertIn("BAN_IO reason=", trace,
                      "写失败必须打 BAN_IO——这是整条链上唯一会说「策略丢了磁盘」的一行")
        self.assertIn("report_ok=False", trace)

    def test_trace_failure_is_reasoned_not_swallowed(self):
        """两条通道分别探：trace 写不出去也要有原因串（"trace denied 而 report 通"是另一种执法形态）。"""
        real_trace = bs._trace

        def boom(msg):
            raise OSError("trace denied")
        bs._trace = boom
        try:
            out = bs.probe_strategy_io()
        finally:
            bs._trace = real_trace
        self.assertIs(out["trace_ok"], False)
        self.assertIn("trace:OSError", out["reason"])


class TestGatewayReadout(unittest.TestCase):
    """2/4：网关收下这枚事件、三态读数，以及"不放松未知类型"。"""

    def setUp(self):
        """一个 mock 柜台网关实例：库落在临时目录，绝不连真实柜台，也绝不发任何委托。"""
        fd, dbpath = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(dbpath)
        self.dbpath = dbpath
        self.gw = gw_mod.Gateway({"listen": "127.0.0.1:0", "token": "tk-ban-io",
                                  "broker": "mock", "account": "M", "db": dbpath,
                                  "report_url": "", "reconcile_sec": 0})
        self.gw._io_selfcheck = {"at": "", "day": "", "ts": 0.0, "report_ok": None,
                                 "trace_ok": None, "reason": "", "n": 0}

    def tearDown(self):
        try:
            self.gw.store.close()
        except Exception:
            pass
        for suffix in ("", "-wal", "-shm"):
            try:
                os.remove(self.dbpath + suffix)
            except OSError:
                pass

    def _rows(self):
        """账本里"钱相关"三张表的行数合计——观测事件一行都不许碰它们。"""
        total = 0
        for table in ("orders", "fills", "dispatch"):
            cur = self.gw.store._conn.execute("SELECT COUNT(*) FROM %s" % table)
            total += int(cur.fetchone()[0])
        return total

    def test_never_seen_is_not_ok(self):
        r = self.gw._io_selfcheck_readout()
        self.assertEqual(r["state"], "never_seen",
                         "从没收到自证必须是独立一态：旧载体与真被禁都不是「正常」")
        self.assertIsNone(r["report_ok"])
        self.assertEqual(r["probes"], 0)

    def test_received_event_reads_as_ok(self):
        code, resp = self.gw._do_dispatch_result({"type": "io_selfcheck", "day": "2026-11-05",
                                            "at": "2026-11-05 09:55:12", "src": "x"})
        self.assertEqual(code, 200, resp)
        r = self.gw._io_selfcheck_readout()
        self.assertEqual(r["state"], "ok")
        self.assertIs(r["report_ok"], True, "收到即证明写得到——不许读成 None")
        self.assertEqual(r["probes"], 1)
        self.assertEqual(r["day"], "2026-11-05")

    def test_explicit_denial_reads_as_denied(self):
        self.gw._note_io_selfcheck({"at": "2026-11-05 10:00:00", "day": "2026-11-05",
                              "report_ok": False, "trace_ok": True, "reason": "report:OSError"})
        r = self.gw._io_selfcheck_readout()
        self.assertEqual(r["state"], "denied")
        self.assertIn("OSError", r["reason"])

    def test_unknown_type_is_still_refused(self):
        """加了 io_selfcheck 不等于把未知类型放开了——那条 400 是"桥升级要被发现"的锁。"""
        code, resp = self.gw._do_dispatch_result({"type": "brand_new_type_nobody_declared"})
        self.assertEqual(code, 400)
        self.assertIn("unknown dispatch result type", str(resp))

    def test_probe_event_touches_no_ledger(self):
        """观测腿不许有钱的动作（§OBS-GUARD）：收下这枚事件不得写委托/成交/持仓任何一张表。"""
        before = self._rows()
        code, _resp = self.gw._do_dispatch_result({"type": "io_selfcheck", "day": "2026-11-05",
                                                   "at": "2026-11-05 09:55:12"})
        self.assertEqual(code, 200)
        self.assertEqual(self._rows(), before, "观测事件改变了账本计数")
        self.assertEqual(self.gw._io_selfcheck_readout()["state"], "ok")


class TestDeployedStrategyStaysAscii(unittest.TestCase):
    """qmt_bridge_strategy.py 必须继续逐字节 ASCII（客户端按 GBK 解源码，一个中文字节就能让加载失败）。"""

    def test_strategy_file_is_pure_ascii(self):
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..",
                            "qmt_bridge_strategy.py")
        with open(path, "rb") as fh:
            raw = fh.read()
        bad = [i for i, b in enumerate(raw) if b > 127]
        self.assertEqual(bad, [], "策略文件里有 %d 个非 ASCII 字节，首个偏移 %s"
                         % (len(bad), bad[:1]))

    def test_gateway_still_carries_chinese_comments(self):
        """反向对照：只有**沙箱里那份**受 GBK 限制；网关是普通进程，中文注释照旧（别改错方向）。"""
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "gateway.py")
        with open(path, "rb") as fh:
            self.assertTrue(any(b > 127 for b in fh.read()))


if __name__ == "__main__":
    unittest.main()
