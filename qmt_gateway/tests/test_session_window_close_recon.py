# §CANCEL-STATE（2026-10-28）+ §CLOSE-RECON（2026-10-28）网关/桥侧回归测试。
#
# 覆盖两件事：
#   ① 禁撤窗口**入队侧兜底**：窗口内撤单请求不排队（交易所在集合竞价期不接撤单，
#      "已受理"会被读成"已撤"——600362 事故的根因之一）。窗口判定是北京时间、左闭右开。
#   ② 收盘对账观测位：桥在 15:01 打的收盘快照（close_snapshot=true）必须能被
#      /admin/status 看见（"最近一次收盘快照在哪天、有多少行"）。
#
# 桥侧那份实现（qmt_bridge_strategy.py，纯 ASCII 单文件沙箱）同样在这里回归：
# 它的窗口判定必须与网关侧一致（两份实现各自独立，口径必须同源）。
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import broker  # noqa: E402
import qmt_bridge_strategy as bs  # noqa: E402


class TestNoCancelWindowGateway(unittest.TestCase):
    """网关侧禁撤窗口判定（入队侧兜底）。"""

    def test_parse_windows(self):
        self.assertEqual(broker.parse_session_windows("09:20-09:30,14:57-15:00"),
                         [(560, 570), (897, 900)])
        self.assertEqual(broker.parse_session_windows(""), [])
        # 垃圾片段跳过而不是瞎猜
        self.assertEqual(broker.parse_session_windows("xx,09:20-09:30"), [(560, 570)])

    def test_window_half_open(self):
        w = "09:20-09:30,14:57-15:00"
        # 窗口内
        self.assertTrue(broker.no_cancel_window_now(w, 9 * 60 + 25)[0])
        self.assertTrue(broker.no_cancel_window_now(w, 14 * 60 + 57)[0])
        # 左闭右开：起点算窗口内，终点算窗口外
        self.assertTrue(broker.no_cancel_window_now(w, 9 * 60 + 20)[0])
        self.assertFalse(broker.no_cancel_window_now(w, 9 * 60 + 30)[0])
        self.assertFalse(broker.no_cancel_window_now(w, 15 * 60)[0])
        # 窗口外
        self.assertFalse(broker.no_cancel_window_now(w, 10 * 60)[0])
        self.assertFalse(broker.no_cancel_window_now(w, 14 * 60 + 56)[0])

    def test_window_label(self):
        blocked, label = broker.no_cancel_window_now("14:57-15:00", 14 * 60 + 58)
        self.assertTrue(blocked)
        self.assertEqual(label, "14:57-15:00")

    def test_queued_broker_refuses_cancel_in_window(self):
        """窗口内不入队：派发表里不能出现这笔撤单请求。"""
        calls = []

        class _Store:
            def dispatch_enqueue_cancel(self, *a, **k):
                calls.append(a)
                return "seq:1"

            def dispatch_get(self, oid):
                return {"order_id": "4521", "signal_id": "S1", "code": "600362.SH", "side": "买入"}

        b = broker.QueuedBroker(_Store(), account="A", no_cancel_windows="09:20-09:30")
        orig = broker._now_cn_minutes
        broker._now_cn_minutes = lambda: 9 * 60 + 25  # 窗口内（09:20-09:30）
        try:
            ok, err = b.cancel("seq:1")
        finally:
            broker._now_cn_minutes = orig
        self.assertFalse(ok)
        self.assertIn("禁撤窗口", err)
        self.assertEqual(calls, [], "窗口内不得入队")

    def test_queued_broker_enqueues_outside_window(self):
        """窗口外照常入队（兜底不能把正常撤单也拦掉）。"""
        calls = []

        class _Store:
            def dispatch_enqueue_cancel(self, *a, **k):
                calls.append(a)
                return "seq:1"

            def dispatch_get(self, oid):
                return {"order_id": "4521", "signal_id": "S1", "code": "600362.SH", "side": "买入"}

        b = broker.QueuedBroker(_Store(), account="A", no_cancel_windows="09:20-09:30")
        orig = broker._now_cn_minutes
        broker._now_cn_minutes = lambda: 10 * 60  # 窗口外
        try:
            ok, err = b.cancel("seq:1")
        finally:
            broker._now_cn_minutes = orig
        self.assertTrue(ok)
        self.assertEqual(err, "")
        self.assertEqual(len(calls), 1)


class TestNoCancelWindowBridge(unittest.TestCase):
    """桥侧窗口判定必须与网关侧同口径（两份实现各自独立，改一处要改另一处）。"""

    def test_parse_windows(self):
        self.assertEqual(bs._parse_windows("09:20-09:30,14:57-15:00"),
                         [(560, 570), (897, 900)])
        self.assertEqual(bs._parse_windows("bad"), [])

    def test_window_matches_gateway(self):
        """两侧窗口判定必须逐点一致（两份实现各自独立，口径必须同源）。"""
        w = "09:20-09:30,14:57-15:00"
        for minutes, want in ((9 * 60 + 20, True), (9 * 60 + 25, True), (9 * 60 + 30, False),
                              (14 * 60 + 56, False), (14 * 60 + 57, True), (15 * 60, False),
                              (10 * 60, False)):
            self.assertEqual(broker.no_cancel_window_now(w, minutes)[0], want,
                             "网关侧在 %d 分钟的判定" % minutes)
            self.assertEqual(bs._no_cancel_window_at(w, minutes)[0], want,
                             "桥侧与网关侧口径必须一致（%d 分钟）" % minutes)

    def test_default_windows_match(self):
        """两侧出厂默认值必须相同（否则兜底会与 Go 侧闸门打架）。"""
        self.assertEqual(bs.DEFAULT_NO_CANCEL_WINDOWS, broker.DEFAULT_NO_CANCEL_WINDOWS)
        self.assertEqual(bs.DEFAULT_NO_CANCEL_WINDOWS, "09:20-09:30,14:57-15:00")


    def test_bridge_refuses_cancel_in_window(self):
        """窗口内的撤单命令**不得**下发到柜台（桥执行侧的最后一道闸）。

        为什么单独锁这条：命令可能在窗口开启前就已入队（网关侧那时还没拦），
        执行时窗口已经开始——这时只有桥侧能挡。挡不住就会拿到一次"受理成功"的假回执，
        上游读成"已撤"，而交易所根本没撤（600362 事故的同一形状）。
        """
        import tempfile
        reports = []
        called = []

        class _Adapter:
            def cancel(self, seq, order_id, code=""):
                called.append((seq, order_id))
                return True, ""

        # 桩掉四个全局：回报（收集）、柜台适配器（记录是否真的下发）、窗口判定（强制在窗口内）、
        # 去重文件路径（指向临时目录，否则 _record_seen 因默认 Windows 路径写不进而拒单）。
        orig_report = bs._report
        orig_xt = bs._xt
        orig_window = bs._no_cancel_window
        orig_seen = bs.SEEN_PATH
        with tempfile.TemporaryDirectory() as td:
            bs.SEEN_PATH = os.path.join(td, "bridge_seen.jsonl")
            bs._xt = lambda: _Adapter()
            bs._report = lambda payload: reports.append(payload)
            bs._no_cancel_window = lambda cfg=None: (True, "14:57-15:00")
            try:
                handled = bs._handle_cmd({"kind": "cancel", "seq": "S-CANCEL-1",
                                          "order_id": "4521"}, set())
            finally:
                bs._report = orig_report
                bs._xt = orig_xt
                bs._no_cancel_window = orig_window
                bs.SEEN_PATH = orig_seen
        self.assertTrue(handled, "命令必须被处理掉（留 pending 会一直重放）")
        self.assertEqual(called, [], "窗口内不得下发撤单到柜台")
        self.assertTrue(reports, "必须回报一条 cancel_result")
        self.assertFalse(reports[0].get("ok"))
        self.assertIn("no-cancel window", str(reports[0].get("err", "")))

    def test_bridge_cancel_passes_outside_window(self):
        """窗口外照常下发（兜底不能把正常撤单也拦掉）。"""
        import tempfile
        reports = []
        called = []

        class _Adapter:
            def cancel(self, seq, order_id, code=""):
                called.append((seq, order_id))
                return True, ""

        # 桩法同上：把窗口判定改成"不在窗口内"，其余全局同样要复原。
        orig_report = bs._report
        orig_xt = bs._xt
        orig_window = bs._no_cancel_window
        orig_seen = bs.SEEN_PATH
        with tempfile.TemporaryDirectory() as td:
            bs.SEEN_PATH = os.path.join(td, "bridge_seen.jsonl")
            bs._xt = lambda: _Adapter()
            bs._report = lambda payload: reports.append(payload)
            bs._no_cancel_window = lambda cfg=None: (False, "")
            try:
                bs._handle_cmd({"kind": "cancel", "seq": "S-CANCEL-2",
                                "order_id": "4521"}, set())
            finally:
                bs._report = orig_report
                bs._xt = orig_xt
                bs._no_cancel_window = orig_window
                bs.SEEN_PATH = orig_seen
        self.assertEqual(len(called), 1, "窗口外必须照常下发撤单")


class TestCloseReconBridge(unittest.TestCase):
    """§CLOSE-RECON 收盘对账：能力探针 + 收盘快照 + 幂等。"""

    def test_bridge_file_is_pure_ascii(self):
        """桥文件在 QMT 的 GBK 沙箱里加载，非 ASCII 字节会被吃掉——必须逐字节验。"""
        path = os.path.join(os.path.dirname(__file__), "..", "qmt_bridge_strategy.py")
        with open(path, "rb") as f:
            raw = f.read()
        self.assertEqual(sum(1 for b in raw if b > 127), 0, "桥文件必须纯 ASCII")

    def test_close_state_roundtrip(self):
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({"snapshot_day": "2026-10-28", "probe_usable": True})
            state = bs._read_close_state()
            self.assertEqual(state.get("snapshot_day"), "2026-10-28")
            self.assertTrue(state.get("probe_usable"))

    def test_probe_skips_when_snapshot_already_done(self):
        """同一天只做一次收盘快照（幂等）。"""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({"snapshot_day": bs._now_cn_str("%Y-%m-%d")})
            called = []
            bs._close_snapshot = lambda day: called.append(day)
            bs.close_snapshot_probe(None)
            self.assertEqual(called, [], "当天已做过收盘快照，不得重复")

    def test_probe_unusable_falls_back(self):
        """读不到表 ⇒ 不发空快照（会触发清仓守卫），改为登记待回补并告警。"""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({})
            bs._MAIN_THREAD["ident"] = -999  # 假装不是主线程
            called = []
            bs._close_snapshot = lambda day: called.append(day)

            class _Adapter:
                def _gtdd(self, table):
                    return []

            bs._xt = lambda: _Adapter()
            bs.close_snapshot_probe(None)
            self.assertEqual(called, [], "探针不可用时不得发收盘快照")
            state = bs._read_close_state()
            self.assertFalse(state.get("probe_usable"))
            self.assertTrue(state.get("pending_recon_day"), "必须登记待回补日")

    def test_probe_skips_on_non_trading_day(self):
        """非交易日（当天没有桥 tick）⇒ 不登记待回补（否则周末的探针会让周一早盘误报）。"""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({})
            bs._MAIN_THREAD["ident"] = -999  # 假装不是主线程
            orig_tick_day = bs._TICK.get("last_tick_day", "")
            bs._TICK["last_tick_day"] = "1999-01-01"  # 最后一次 tick 是另一个日子
            called = []

            class _Adapter:
                def _gtdd(self, table):
                    return []

            bs._xt = lambda: _Adapter()
            bs._close_snapshot = lambda day: called.append(day)
            try:
                bs.close_snapshot_probe(None)
            finally:
                bs._TICK["last_tick_day"] = orig_tick_day
            self.assertEqual(called, [], "非交易日不得发收盘快照")
            state = bs._read_close_state()
            self.assertFalse(state.get("pending_recon_day"),
                             "非交易日不得登记待回补（周一早盘会误报）")

    def test_probe_usable_snapshots(self):
        """主线程（或能读到行）⇒ 发一次收盘快照。"""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({})
            called = []

            class _Adapter:
                def _gtdd(self, table):
                    return [object()]

            bs._xt = lambda: _Adapter()
            bs._close_snapshot = lambda day: called.append(day)
            bs.close_snapshot_probe(None)
            self.assertEqual(len(called), 1, "可用时必须发一次收盘快照")

    def test_morning_alert_once_per_day(self):
        """待回补日的告警每天只说一次（否则早盘刷屏）。"""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            bs.CLOSE_STATE_PATH = os.path.join(td, "bridge_close_state.json")
            bs._write_close_state({"pending_recon_day": "2026-10-27"})
            bs._close_recon_morning_alert()
            state = bs._read_close_state()
            self.assertTrue(state.get("alert_day"), "首次必须登记告警日")
            day1 = state.get("alert_day")
            bs._close_recon_morning_alert()
            self.assertEqual(bs._read_close_state().get("alert_day"), day1)


class TestCloseReconGateway(unittest.TestCase):
    """网关侧收盘快照观测位。"""

    def test_close_snapshot_observed(self):
        import gateway
        gw = gateway.Gateway.__new__(gateway.Gateway)
        gw._close_snap = {"at": "", "day": "", "ts": 0.0, "n": 0, "positions": 0, "trades": 0}
        gw._note_close_snapshot("orders_snapshot",
                                {"data_ts": "2026-10-28T15:01:03+08:00",
                                 "orders": [{"order_id": "1"}, {"order_id": "2"}]})
        gw._note_close_snapshot("positions",
                                {"data_ts": "2026-10-28T15:01:03+08:00",
                                 "positions": [{"code": "600362.SH"}]})
        gw._note_close_snapshot("trade",
                                {"data_ts": "2026-10-28T15:01:03+08:00"})
        snap = gw._close_snap
        self.assertEqual(snap["day"], "2026-10-28")
        self.assertEqual(snap["n"], 2)
        self.assertEqual(snap["positions"], 1)
        self.assertEqual(snap["trades"], 1)


if __name__ == "__main__":
    unittest.main()
