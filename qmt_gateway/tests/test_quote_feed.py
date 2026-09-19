# -*- coding: utf-8 -*-
"""test_quote_feed.py — §ENH-5 批E：L1 行情 feed 回归。

锁四件事：
1. QuoteFeed 单元语义——xtdata 缺失时静默停用（connected=False）、snapshot 累积订阅池、
   tick 归一化（lastClose→prevClose、秒→毫秒、脏值容错）；
2. /quotes HTTP 契约——鉴权面（无 token 401）、缺 codes 400、feed 未接通时 200+空 ticks；
3. 交易主链路隔离铁律——active broker 断开时 /quotes 仍 200（行情不随交易通道熔断）；
4. /health 观察字段——feed_connected/feed_age_sec 存在但 ok 恒 True（不参与交易判定）。
"""
import json
import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from gateway import Gateway, _Handler  # noqa: E402
from quote_feed import QuoteFeed  # noqa: E402


class TestQuoteFeedUnit(unittest.TestCase):
    """QuoteFeed 纯单元：不依赖 xtdata（开发机本就不可导入，正是降级路径的实证）。"""

    def _feed(self, **over):
        cfg = {"quote_feed": True, "feed_poll_sec": 3}
        cfg.update(over)
        return QuoteFeed(cfg)

    def test_import_absence_degrades_quietly(self):
        """xtdata 不可导入：_ensure_xtdata 返回 False、connected False、接口不抛异常。"""
        feed = self._feed()
        self.assertFalse(feed._ensure_xtdata())
        self.assertTrue(feed._import_failed)
        self.assertFalse(feed.is_connected())
        self.assertEqual(feed.age_sec(), -1.0)
        self.assertEqual(feed.snapshot(["600519.SH"]), {})  # 未接通：空 ticks 而非报错
        # 轮询一轮：无订阅直接返回；有订阅但 xtdata 缺失也只静默标记
        feed._poll_once()
        feed._codes.add("600519.SH")
        feed._poll_once()
        self.assertFalse(feed.is_connected())

    def test_disabled_no_thread(self):
        """quote_feed=False：start 为 no-op，is_connected 恒 False。"""
        feed = self._feed(quote_feed=False)
        feed.start()
        self.assertIsNone(feed._thread)
        self.assertFalse(feed.is_connected())
        feed.stop()  # 幂等

    def test_snapshot_accumulates_pool(self):
        """/quotes 请求代码动态并入订阅池（下轮 get_full_tick 生效）。"""
        feed = self._feed()
        feed.snapshot(["600519.SH", "300750.SZ"])
        feed.snapshot(["600519.SH", "830799.BJ"])
        self.assertEqual(feed._codes, {"600519.SH", "300750.SZ", "830799.BJ"})

    def test_normalize_contract(self):
        """tick 归一：lastClose→prevClose、秒时间戳→毫秒、脏值→0、非 dict→丢弃。"""
        tk = QuoteFeed._normalize("600519.SH", {
            "time": 1770000000, "lastPrice": 1500.5, "open": 1490, "high": 1510,
            "low": 1488, "lastClose": 1495, "volume": 31000, "amount": "4.6e7",
            "junk": object()})
        self.assertEqual(tk["prevClose"], 1495.0)
        self.assertEqual(tk["tickTime"], 1770000000000)
        self.assertEqual(tk["amount"], 4.6e7)  # 字符串数值容错
        self.assertEqual(tk["lastPrice"], 1500.0 + 0.5)
        ms = QuoteFeed._normalize("x", {"time": 1770000000000, "lastPrice": 1})
        self.assertEqual(ms["tickTime"], 1770000000000)  # 毫秒口径原样
        self.assertIsNone(QuoteFeed._normalize("x", "not-a-dict"))

    def test_connected_window_after_fake_success(self):
        """伪造一轮成功轮询后 connected=True、age_sec 收敛在 0~1s。"""
        feed = self._feed()
        with feed._lock:
            feed._last_ok_at = time.time()
            feed._import_failed = False
        # _import_failed 清掉但 _xtdata 仍 None：_connected_locked 只看 enable+时间窗
        self.assertTrue(feed.is_connected())
        self.assertLessEqual(feed.age_sec(), 1.0)


class TestQuoteFeedHTTP(unittest.TestCase):
    """网关 HTTP 面：/quotes 契约 + 交易链路隔离 + /health 观察字段。"""

    def setUp(self):
        fd, dbpath = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(dbpath)
        self.gw = Gateway({
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock",
            "account": "MOCK0001", "db": dbpath, "report_url": "",
            "reconcile_sec": 0, "seed": [],
        })
        _Handler.gateway = self.gw
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.port = self.server.server_address[1]
        self.gw.start()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.gw.stop()  # 含 feed.stop()：daemon 线程必须随网关回收（§UAT-D8 同款约束）
        self.server.shutdown()
        self.server.server_close()

    def _req(self, method, path, body=None, token="tk"):
        url = "http://127.0.0.1:%d%s" % (self.port, path)
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return resp.status, json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode("utf-8"))

    def test_quotes_requires_auth(self):
        """/quotes 自动继承 Bearer 鉴权面：无 token → 401。"""
        status, body = self._req("GET", "/quotes?codes=600519.SH", token="")
        self.assertEqual(status, 401)
        self.assertFalse(body["ok"])

    def test_quotes_missing_codes_400(self):
        status, body = self._req("GET", "/quotes")
        self.assertEqual(status, 400)
        self.assertIn("codes", body["err"])

    def test_quotes_200_with_empty_ticks_when_feed_down(self):
        """xtdata 缺失环境（本测试机）：feed 未接通也必须 200+空 ticks——
        Go 侧 qmt_feed 按"无命中不注入"处理，绝不因行情通道缺失拿到非 200。"""
        status, body = self._req("GET", "/quotes?codes=600519.SH,000001.SZ")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        self.assertEqual(body["ticks"], {})
        self.assertIn("feed_connected", body)

    def test_quotes_survives_broker_disconnect(self):
        """交易主链路隔离铁律回归锁：broker 断连（/order 503）时 /quotes 仍 200。"""
        broker = self.gw.brokers["mock"]
        original = broker.is_connected
        broker.is_connected = lambda: False
        try:
            s_order, b_order = self._req("POST", "/order", {
                "signal_id": "SX", "code": "600519.SH", "side": "买入",
                "price_type": "market", "price": 10, "qty": 100, "amount": 1000,
                "created_at": "t"})
            self.assertEqual(s_order, 503)  # 交易闸如常在
            s_quotes, b_quotes = self._req("GET", "/quotes?codes=600519.SH")
            self.assertEqual(s_quotes, 200)  # 行情不受牵连
            self.assertTrue(b_quotes["ok"])
            s_health, b_health = self._req("GET", "/health", token="")
            self.assertTrue(b_health["ok"])
            self.assertFalse(b_health["broker_connected"])  # 熔断判定字段真实反映断连
        finally:
            broker.is_connected = original

    def test_health_carries_feed_observation(self):
        """/health 增观察字段但不改交易判定：ok=True、feed_connected=False（无 xtdata）。"""
        status, body = self._req("GET", "/health", token="")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        self.assertIn("feed_connected", body)
        self.assertIn("feed_age_sec", body)
        self.assertFalse(body["feed_connected"])


if __name__ == "__main__":
    unittest.main()
