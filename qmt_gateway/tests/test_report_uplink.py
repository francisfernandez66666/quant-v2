# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_report_uplink.py — 本地进程上行客户端的行为测试（§EXT-BRAIN 回报上行腿）。

覆盖 report_uplink 的四条硬规则，逐条对应模块 docstring：
  1. 信封形状对 golden（contract/report_fields.json）——发出键集必须是 Go 信封反射集的子集，
     事件类型与字段名与 handler.py 那代契约逐字同形（§M4 丢腿教训：两侧不同名即静默丢）。
  2. missing≠empty：导出文件缺席 ⇒ 该类一个事件都不发且原因落本地状态；
     空文件 ⇒ 允许发空快照；半文件（行数骤降）⇒ 不发、保留旧值。
  3. 只有 2xx 才推进"已报"基线（§FIX-20261028-F02）；瞬态失败进有界重试队列、
     永久 4xx 进死信（§P1-8 同口径）。
  4. 幂等去重：同内容哈希只发一次；心跳携带已安装策略版本 + 执行器版本/指纹。
外加一条红线测试：**钥匙字符串绝不出现**在任何日志流、标准输出/错误、或本模块写出的文件里。

测试传输一律注入假 opener（或本机 127.0.0.1 桩服务器），绝不打真实网络；
TLS 校验那条由代码审读保证（urllib 默认工厂，全模块无 unverified context）。
"""
import contextlib
import io
import json
import logging
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import unittest

import report_uplink as up

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GOLDEN_PATH = os.path.join(_ROOT, "qmt_gateway", "contract", "report_fields.json")

FAKE_TOKEN = "FAKE-SECRET-TOKEN-0123456789abcdef"
FIXED_NOW = 1793772000.0  # 钉死的 epoch 秒：去重测试要求"同输入同哈希"，时钟必须可复现


def _load_golden():
    with open(GOLDEN_PATH, encoding="utf-8") as f:
        return json.load(f)


class _Fixture:
    """临时导出根 + config.xt.json + 假策略/执行器文件的一次性夹具。"""

    def __init__(self, tmp, with_positions=True, positions_blank=False, positions_rows=6):
        self.tmp = tmp
        self.export_dir = os.path.join(tmp, "exports")
        os.makedirs(self.export_dir, exist_ok=True)
        self.strategy_file = os.path.join(tmp, "installed_strategy.py")
        self.executor_file = os.path.join(tmp, "local_executor.py")
        with open(self.strategy_file, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v02-20261104"\n')
        with open(self.executor_file, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v01-20261103"\nX = 1\n')
        if with_positions:
            body = "代码,名称,数量,可用数量\n"
            if not positions_blank:
                for i in range(positions_rows):
                    body += "600519,贵州茅台,%d,%d\n" % (100 + i, 100 + i)
            self._write("持仓.csv", body)
        self._write("委托.csv", "代码,委托号,委托状态,买卖标志,价格,数量,时间\n"
                                "600519,7701,已报,买入,10.0,100,2026-11-05 09:31:00\n"
                                "000001,7702,已成,卖出,8.5,200,2026-11-05 10:02:00\n")
        self._write("资产.csv", "总资产,可用资金,证券市值\n100000.0,40000.0,60000.0\n")
        self.config_path = os.path.join(tmp, "config.xt.json")
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump({"token": FAKE_TOKEN, "user_id": "u_leg01",
                       "report_url": "http://127.0.0.1:1",  # 缺省指向黑洞端口，测试自行注入 opener
                       "uplink_export_roots": [self.export_dir],
                       "uplink_strategy_file": self.strategy_file,
                       "uplink_executor_file": self.executor_file,
                       "uplink_state_dir": tmp}, fh)

    def _write(self, name, body):
        with open(os.path.join(self.export_dir, name), "w", encoding="utf-8") as fh:
            fh.write(body)

    def rewrite_positions(self, body):
        self._write("持仓.csv", body)

    def uplink(self, opener):
        return up.ReportUplink(self.config_path, now_fn=lambda: FIXED_NOW, opener=opener)


class _OkOpener:
    """假传输：全部回 200，并把 (url, token, body) 收集下来供形状断言。"""

    def __init__(self, status=200):
        self.status = status
        self.calls = []

    def __call__(self, url, token, body):
        self.calls.append((url, token, json.loads(body.decode("utf-8"))))
        return self.status


class TestEnvelopeShape(unittest.TestCase):
    """规则 1：发出键集对 golden 的子集锁 + 字段名逐字镜像 handler.py 契约。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmpdir.name)
        self.opener = _OkOpener()
        u = self.fx.uplink(self.opener)
        u.run_once()
        self.events = [c[2] for c in self.opener.calls]
        self.golden = _load_golden()

    def tearDown(self):
        self._tmpdir.cleanup()

    def _by_type(self, t):
        return [e for e in self.events if e.get("type") == t]

    def test_all_keys_within_envelope_golden(self):
        envelope = set(self.golden["report_event_fields"])
        for e in self.events:
            orphan = set(e.keys()) - envelope
            self.assertFalse(orphan, "事件 %s 发出了 Go 信封没有的键 %s（§M4 丢腿）"
                             % (e.get("type"), sorted(orphan)))

    def test_positions_account_order_field_names(self):
        pos = self._by_type("positions")
        self.assertEqual(len(pos), 1)
        self.assertEqual(set(pos[0].keys()),
                         {"type", "user_id", "positions", "synced", "data_ts"})
        self.assertIs(pos[0]["synced"], True)
        for row in pos[0]["positions"]:
            self.assertTrue(set(row.keys()) <= set(self.golden["positions_row_fields"]),
                            "对账行发出了 RealPosition 反射集外的键: %s" % sorted(row.keys()))
            self.assertRegex(row["ts_code"], r"^\d{6}\.(SH|SZ|BJ)$")  # Go §F2 整批拒收的红线形态
        acc = self._by_type("account")
        self.assertEqual(len(acc), 1)
        self.assertEqual(set(acc[0].keys()), {"type", "user_id", "asset", "synced", "data_ts"})
        self.assertEqual(acc[0]["asset"], {"total_asset": 100000.0, "cash": 40000.0,
                                           "market_value": 60000.0, "frozen_cash": 0.0})
        orders = self._by_type("order")
        self.assertEqual(len(orders), 2)
        want = set(self.golden["gateway_emitted_fields"]["order"])
        for o in orders:
            self.assertEqual(set(o.keys()), want, "order 事件字段名必须逐字镜像既有契约")
        self.assertEqual(orders[0]["order_id"], "7701")
        self.assertEqual(orders[1]["code"], "000001.SZ")  # 裸 6 位码归一：0 开头=深市（平安银行形态）
        self.assertEqual(orders[0]["side"], "买入")

    def test_uplink_emit_sets_match_golden_section(self):
        # golden 的 report_uplink_emitted_fields 是这条腿自己的发出集（与 handler.py 的
        # gateway_emitted_fields 并列、互不冒充）——本模块加/删键不同批改 golden 即红。
        want = self.golden["report_uplink_emitted_fields"]
        seen_types = set()
        for t, keys in want.items():
            for e in self._by_type(t):
                seen_types.add(t)
                self.assertEqual(set(e.keys()), set(keys),
                                 "事件 %s 发出键集与 golden.report_uplink_emitted_fields 不一致" % t)
        self.assertEqual(seen_types, set(want.keys()), "夹具应逐类各至少发出一条，四类都要被钉到")

    def test_positions_row_uplink_keys_within_golden(self):
        allowed = {"ts_code", "name", "qty", "user_id", "can_use_qty", "updated_at"}
        pos = self._by_type("positions")[0]
        for row in pos["positions"]:
            self.assertTrue(set(row.keys()) <= allowed,
                            "上行对账行发出了未登记的键: %s" % sorted(set(row.keys()) - allowed))

    def test_heartbeat_carries_strategy_versions(self):
        hb = self._by_type("heartbeat")
        self.assertEqual(len(hb), 1)
        # 已安装内置策略文件的 STRATEGY_VERSION 进信封新腿 strategy_version
        self.assertEqual(hb[0]["strategy_version"], "v02-20261104")
        # 执行器版本 + 指纹进 reason（ASCII 键值串，heartbeat 分支 Go 不消费、不误进账）
        self.assertIn("executor_version=v01-20261103", hb[0]["reason"])
        self.assertRegex(hb[0]["reason"], r"executor_fingerprint=[0-9a-f]{12}")
        self.assertRegex(hb[0]["reason"], r"uplink_fingerprint=[0-9a-f]{12}")

    def test_attribution_and_url_wiring(self):
        for url, token, _body in self.opener.calls:
            self.assertTrue(url.endswith("/api/qmt/report"))
            self.assertEqual(token, FAKE_TOKEN)  # 钥匙只走 Authorization 头，不进任何载荷体
            self.assertNotIn(FAKE_TOKEN, json.dumps(_body))


class TestMissingNotEmpty(unittest.TestCase):
    """规则 2：missing/empty/半文件 三种读数三种处置（§MANUAL-SYNC 边界的抄法）。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_missing_file_sends_nothing_and_records_reason(self):
        fx = _Fixture(self.tmp, with_positions=False)
        opener = _OkOpener()
        fx.uplink(opener).run_once()
        types = [c[2].get("type") for c in opener.calls]
        self.assertNotIn("positions", types, "持仓导出缺席时发出了 positions 事件——missing 被当成空仓")
        with open(os.path.join(self.tmp, "uplink_state.json"), encoding="utf-8") as f:
            state = json.load(f)
        self.assertTrue([s for s in state["skips"]
                         if s["kind"] == "positions" and s["reason"] == "file_not_found"],
                        "缺席原因必须本地留痕（不发≠没发生）")

    def test_empty_file_sends_empty_snapshot(self):
        fx = _Fixture(self.tmp, positions_blank=True)
        opener = _OkOpener()
        fx.uplink(opener).run_once()
        pos = [c[2] for c in opener.calls if c[2].get("type") == "positions"]
        self.assertEqual(len(pos), 1, "文件在而零行是合法空读数，允许发空快照由 Go 守卫兜底")
        self.assertEqual(pos[0]["positions"], [])

    def test_half_written_export_sends_nothing_this_round(self):
        fx = _Fixture(self.tmp, positions_rows=6)
        opener = _OkOpener()
        u = fx.uplink(opener)
        u.run_once()  # 第一份 6 行可信读数已发
        del opener.calls[:]
        fx.rewrite_positions("代码,名称,数量,可用数量\n600519,贵州茅台,100,100\n")  # 6→1 骤降
        u.run_once()
        types = [c[2].get("type") for c in opener.calls]
        self.assertNotIn("positions", types, "行数骤降=半文件，本轮不得上送")
        skips = [s for s in u.skips if s["kind"] == "positions"]
        self.assertTrue(any(s["reason"].startswith("parse_fail:row_collapse") for s in skips))


class TestFailurePosture(unittest.TestCase):
    """规则 3/4：2xx 才推进基线、瞬态进有界重试、永久 4xx 进死信、同内容不重发。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_only_2xx_advances_baseline(self):
        fx = _Fixture(self.tmp)
        failing = _OkOpener(status=500)
        u = fx.uplink(failing)
        rep = u.run_once()
        self.assertEqual(rep["sent"], 0)
        self.assertEqual(u.sent_hashes, [], "非 2xx 之前不得推进已报基线（§FIX-20261028-F02）")
        self.assertTrue(u._retry, "瞬态失败必须留在重试队列")
        ok = _OkOpener(status=200)
        u._opener = ok
        rep2 = u.run_once()
        self.assertGreater(rep2["sent"], 0, "恢复后积压应重发")

    def test_dedupe_same_content_not_reposted(self):
        fx = _Fixture(self.tmp)
        a = _OkOpener()
        u = fx.uplink(a)
        u.run_once()
        first_n = len(a.calls)
        b = _OkOpener()
        u2 = fx.uplink(b)  # 新进程实例、同状态文件：跨轮去重靠落盘的哈希账
        rep = u2.run_once()
        self.assertEqual(len(b.calls), 0, "同一份未变的导出不得二次上送（含重启后）")
        self.assertEqual(rep["skipped_dupe"], first_n)

    def test_permanent_4xx_dead_letters(self):
        fx = _Fixture(self.tmp)
        u = fx.uplink(_OkOpener(status=400))
        rep = u.run_once()
        self.assertGreater(rep["dead"], 0)
        self.assertEqual(rep["retry"], 0, "永久拒绝不是瞬态，不许占重试队列（§P1-8 毒丸）")
        self.assertEqual(u._retry and len(u._retry) or 0, 0)

    def test_retry_queue_is_bounded(self):
        fx = _Fixture(self.tmp)
        fx.rewrite_positions("代码,名称,数量,可用数量\n" +
                             "".join("600%03d,票%d,%d,%d\n" % (i, i, i, i) for i in range(50)))
        # 把委托导出改成 60 行，制造远超队列上限的事件量
        with open(os.path.join(fx.export_dir, "委托.csv"), "w", encoding="utf-8") as fh:
            fh.write("代码,委托号,委托状态,买卖标志,价格,数量,时间\n")
            for i in range(60):
                fh.write("600%03d,9%03d,已报,买入,10.0,100,2026-11-05 09:31:00\n" % (i, i))
        u = fx.uplink(_OkOpener(status=503))
        u.queue_max = 10
        u.run_once()
        self.assertLessEqual(len(u._retry), 10)
        self.assertGreater(u.stats["dropped_queue_overflow"], 0)


class TestNeverLeakTheKey(unittest.TestCase):
    """红线：钥匙字符串绝不进日志/stdout/stderr/任何本模块写出的文件（B-N2 / §TOKEN-PERAACCT）。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_token_absent_from_every_output_channel(self):
        fx = _Fixture(self.tmp)
        # 通道一：真实失败路径（抛异常）——异常文本最容易顺手 str(e) 进日志
        def boom(url, token, body):
            raise OSError("connection refused")
        log_stream = io.StringIO()
        handler = logging.StreamHandler(log_stream)
        lg = logging.getLogger("qmt_gateway.report_uplink")
        lg.addHandler(handler)
        old_stdout, old_stderr = sys.stdout, sys.stderr
        out_buf, err_buf = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stdout(out_buf), contextlib.redirect_stderr(err_buf):
                u = fx.uplink(boom)
                u.run_once()
                # 通道二：成功路径 + 状态落盘
                u2 = fx.uplink(_OkOpener())
                u2.run_once()
        finally:
            sys.stdout, sys.stderr = old_stdout, old_stderr
            lg.removeHandler(handler)
        sinks = {"log": log_stream.getvalue(), "stdout": out_buf.getvalue(),
                 "stderr": err_buf.getvalue()}
        for name, text in sinks.items():
            self.assertNotIn(FAKE_TOKEN, text, "钥匙泄漏进 %s" % name)
        # 通道三：tmp 目录里本模块写出的每一个文件逐字节扫描（config.xt.json 是钥匙的**源**，排除）
        for root, _dirs, files in os.walk(self.tmp):
            for fn in files:
                p = os.path.join(root, fn)
                if os.path.abspath(p) == os.path.abspath(fx.config_path):
                    continue
                with open(p, "rb") as fh:
                    self.assertNotIn(FAKE_TOKEN.encode(), fh.read(), "钥匙泄漏进文件 %s" % p)

    def test_config_error_message_carries_no_content(self):
        bad = os.path.join(self.tmp, "broken.json")
        with open(bad, "w", encoding="utf-8") as fh:
            fh.write("{not json " + FAKE_TOKEN)
        with self.assertRaises(ValueError) as ctx:
            up.load_xt_config(bad)
        self.assertNotIn(FAKE_TOKEN, str(ctx.exception))


class TestStubServerEndToEnd(unittest.TestCase):
    """本机 127.0.0.1 桩服务器跑一次真 http.client 传输（urllib 真实腿，假地址）：
    验证 Bearer 头接线与 JSON 上线体，不留任何真实网络依赖。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        received = []
        self.received = received

        class _Stub(BaseHTTPRequestHandler):
            def do_POST(self):
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n)
                # 闭包引用外层 setUp 的 received 列表：BaseHTTPRequestHandler 每个请求新建
                # 实例，绝不能在 do_POST 里用 self.received（那指的是 handler 自己）。
                received.append((self.headers.get("Authorization"),
                                 json.loads(body.decode("utf-8"))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"ok":"1"}')

            def log_message(self, *a):  # 桩服务器闭嘴：测试输出里不该有访问日志
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Stub)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.fx = _Fixture(self._tmpdir.name)
        with open(self.fx.config_path, "r", encoding="utf-8") as fh:
            cfg = json.load(fh)
        port = self.server.server_address[1]
        cfg["report_url"] = "http://127.0.0.1:%d" % port
        with open(self.fx.config_path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self._tmpdir.cleanup()

    def test_real_urllib_posts_envelopes(self):
        u = up.ReportUplink(self.fx.config_path, now_fn=lambda: FIXED_NOW)
        rep = u.run_once()
        self.assertGreater(rep["sent"], 0)
        self.assertTrue(self.received, "桩服务器没有收到任何上报")
        for authh, body in self.received:
            self.assertEqual(authh, "Bearer " + FAKE_TOKEN)
            self.assertNotIn(FAKE_TOKEN, json.dumps(body))
        types = sorted({b.get("type") for _a, b in self.received})
        self.assertIn("positions", types)
        self.assertIn("account", types)
        self.assertIn("heartbeat", types)


if __name__ == "__main__":
    unittest.main()
