# -*- coding: utf-8 -*-
"""§FLOW-MONEY 资金流中继（flow_proxy + /flow/money 路由）行为锁（2026-11-19）。

为什么单独锁这一层：这是唯一一条"盒子上会主动出网取数"的新腿，而它同时又是一个
**能被我方决策机任意调用的 HTTP 端点**。两类错都会真出钱/真出事：
  ① 参数面失控（secid/fs/fields 能被拼成任意 URL）⇒ 盒子变成通用出网跳板；
  ② 缓存被摘掉 ⇒ 广州那场"每轮几百只票各取一次"的风暴原样搬到盒子，把盒子也一起打封。
所以本文件钉四件事：
  1. secid / fs / fields / klt / lmt / pn / pz 的白名单校验（非法一律 FlowProxyError）；
  2. 上游 body **原样透传**（广州侧复用既有解析器，两边不长第二份形状）；
  3. TTL 缓存命中时不打上游（fetch_upstream 只被调用一次）；
  4. 路由映射：参数非法→400、上游不可用→502、正常→200，且该路由排在 broker 连接闸之前。
"""
import os
import sys
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import flow_proxy  # noqa: E402
import gateway  # noqa: E402

# 一条真实形状的 fflow 上游 body（节选两行，字段序与生产一致：日期+五档净额）。
_FFLOW_BODY = (
    '{"rc":0,"data":{"code":"000001","market":1,"name":"上证指数","klines":['
    '"2026-11-18,-3411456000.0,6336782336.0,-2925322240.0,-3832389632.0,420933632.0",'
    '"2026-11-19,-11811803136.0,14767779840.0,-2955976704.0,-6693154816.0,-5118648320.0"]}}'
)

# 一条真实形状的 clist 上游 body（板块列表，含成交额 f6 与主力净额 f62）。
_CLIST_BODY = (
    '{"rc":0,"data":{"total":1,"diff":[{"f12":"BK0420","f14":"汽车整车","f3":1.23,'
    '"f6":12345678900.0,"f62":-987654321.0,"f184":-2.34}]}}'
)


class _Handler:
    """最小 request_handler 桩：只提供 .path（_do_flow_money 只读这一个属性）。"""

    def __init__(self, path):
        self.path = path


def _route(query, method="GET"):
    """以桩 self 调 Gateway._do_flow_money —— 该函数不引用任何 self 属性，可安全解绑调用。"""
    stub = SimpleNamespace()
    return gateway.Gateway._do_flow_money(stub, _Handler("/flow/money?" + query))


class TestSecidWhitelist(unittest.TestCase):
    def test_accepts_index_and_sector(self):
        for good in ("1.000001", "0.399001", "1.000300", "90.BK0420", "90.BK1234"):
            self.assertTrue(flow_proxy.valid_secid(good), good)

    def test_rejects_injection_shapes(self):
        # 任何能改写路径/参数的形状都必须被拒（& / ? / 多段点 / 非数字代码）。
        for bad in ("", "000001", "1.000001&x=1", "1.000001/../x", "2.000001",
                    "1.00000", "1.0000011", "90.0420", "90.BK", "90.BK04a2",
                    "1.000001.2", None, 1000001):
            self.assertFalse(flow_proxy.valid_secid(bad), bad)


class TestUrlBuilders(unittest.TestCase):
    def test_fflow_url_keeps_secid_explicit(self):
        # 今日腿（klt=1）走 push2 分时：实测回 240 根、末根=当日累计，指数与板块都可用。
        url = flow_proxy.build_fflow_url("1.000001", 1, 0)
        self.assertIn("secid=1.000001", url)
        self.assertIn("klt=1", url)
        self.assertIn("lmt=0", url)
        self.assertTrue(url.startswith("https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?"))
        # 历史腿（klt=101）走 push2his 日K。
        daily = flow_proxy.build_fflow_url("1.000001", 101, 5)
        self.assertIn("klt=101", daily)
        self.assertIn("lmt=5", daily)
        self.assertTrue(daily.startswith("https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get?"))

    def test_fflow_rejects_bad_params(self):
        for klt in (0, 2, 60, 102, "1"):
            with self.assertRaises(flow_proxy.FlowProxyError):
                flow_proxy.build_fflow_url("1.000001", klt, 5)
        for lmt in (-1, 2001, "5"):
            with self.assertRaises(flow_proxy.FlowProxyError):
                flow_proxy.build_fflow_url("1.000001", 101, lmt)
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_fflow_url("0.000001;rm -rf", 101, 5)

    def test_clist_url_only_allowed_fields(self):
        url = flow_proxy.build_clist_url("m:90+t:2", "f12,f14,f6,f62", 1, 500)
        self.assertIn("fs=m:90+t:2", url)
        self.assertIn("fields=f12,f14,f6,f62", url)
        # 参数面必须与生产腿逐字同形：多一个 fltt=2 就会让 f3/f184 的量纲整体错 100 倍。
        for forbidden in ("fltt=", "invt=", "po=", "fid=", "np="):
            self.assertNotIn(forbidden, url)
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_clist_url("m:90+t:2", "f12,f999", 1, 500)
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_clist_url("m:0+t:6", "f12", 1, 500)   # 个股市场不在此通道
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_clist_url("m:90+t:2", "", 1, 500)
        for pn, pz in ((0, 500), (21, 500), (1, 0), (1, 501)):
            with self.assertRaises(flow_proxy.FlowProxyError):
                flow_proxy.build_clist_url("m:90+t:2", "f12", pn, pz)


class TestRelayAndCache(unittest.TestCase):
    def setUp(self):
        flow_proxy.clear_cache()

    def test_body_passed_through_verbatim(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, _FFLOW_BODY)) as up:
            out = flow_proxy.relay_fflow("1.000001", 1, 0)
        self.assertTrue(out["ok"])
        self.assertEqual(out["upstream_body"], _FFLOW_BODY)   # 逐字节透传，不做任何再加工
        self.assertEqual(out["src"], "eastmoney_fflow")
        self.assertEqual(out["cache"], "miss")
        self.assertEqual(up.call_count, 1)
        # 广州侧靠既有解析器吃这份 body，形状必须仍是它认识的那个。
        self.assertIn("klines", flow_proxy.parse_body(out)["data"])

    def test_cache_prevents_upstream_storm(self):
        # 这条就是"别再打封一次"的结构性护栏：同一组参数第二次必须命中缓存。
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, _FFLOW_BODY)) as up:
            first = flow_proxy.relay_fflow("1.000001", 101, 5)
            second = flow_proxy.relay_fflow("1.000001", 101, 5)
            other = flow_proxy.relay_fflow("1.000300", 101, 5)   # 不同 secid 不共享缓存
        self.assertEqual(first["cache"], "miss")
        self.assertEqual(second["cache"], "hit")
        self.assertEqual(other["cache"], "miss")
        self.assertEqual(up.call_count, 2)

    def test_upstream_failure_is_not_ok(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(502, "")):
            out = flow_proxy.relay_fflow("1.000001", 1, 0)
        self.assertFalse(out["ok"])
        self.assertEqual(out["upstream_status"], 502)

    def test_intraday_falls_back_to_daily(self):
        # 盒子上 push2his 是间歇性断连 ⇒ 今日腿必须"分时失败→日K兜底"，两条都试才算失败。
        # 这里按调用顺序给：第一次 502（分时），第二次 200（日K）。
        seq = [(502, ""), (200, _FFLOW_BODY)]
        with mock.patch.object(flow_proxy, "fetch_upstream", side_effect=seq) as up:
            out = flow_proxy.relay_fflow("1.000001", 1, 0)
        self.assertTrue(out["ok"])
        self.assertEqual(out["src"], "eastmoney_fflow_daily_fallback")
        self.assertEqual(up.call_count, 2)
        # 兜底那一刻打的必须是 push2his 日K，而不是把分时再打一遍。
        self.assertIn("push2his.eastmoney.com", up.call_args_list[1][0][0])

    def test_both_fflow_upstreams_fail(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(502, "")):
            out = flow_proxy.relay_fflow("1.000001", 1, 0)
        self.assertFalse(out["ok"])
        # 两条都试过（分时 + 日K），失败原因要能看出各自状态码。
        self.assertIn("intraday=502", out.get("err", ""))

    def test_daily_null_data_is_not_ok(self):
        # 200 但 data:null ⇒ 没数据，不许当成功（否则广州会拿到一份解析不了的空 body）。
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, '{"rc":0,"data":null}')):
            out = flow_proxy.relay_fflow("1.000001", 1, 0)
        self.assertFalse(out["ok"])

    def test_clist_relay_shape(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, _CLIST_BODY)):
            out = flow_proxy.relay_clist("m:90+t:2", "f12,f14,f6,f62", 1, 500)
        self.assertTrue(out["ok"])
        diff = flow_proxy.parse_body(out)["data"]["diff"]
        self.assertEqual(diff[0]["f6"], 12345678900.0)
        self.assertEqual(diff[0]["f62"], -987654321.0)


class TestSectorConstituentWhitelist(unittest.TestCase):
    """§FLOW-SECTOR-JOIN（owner 2026-11-20 裁决 A）：白名单唯一新增面 `b:BKxxxx`。

    为什么这组锁不能省：这条中继是盒子上**唯一一条主动出网**的腿，fs 一旦从"两档板块列表"
    放宽成"任意字符串"，盒子立刻变成通用出网跳板（`fs=b:` 后面能接任何上游认识的选择器）。
    所以只放行 `b:BK` + 2~6 位数字，其余一律 400；字段面仍只认生产腿那九个。
    """

    def setUp(self):
        flow_proxy.clear_cache()

    def test_sector_constituents_are_allowed(self):
        for fs in ("b:BK0420", "b:BK0477", "b:BK10", "b:BK123456"):
            url = flow_proxy.build_clist_url(fs, "f12,f14", 1, 100)
            self.assertIn("fs=" + fs, url)
            self.assertIn("fields=f12,f14", url)
            # 参数面与生产腿逐字同形：多一个 np/fltt 就会把 diff 形状换掉。
            for forbidden in ("fltt=", "invt=", "po=", "fid=", "np="):
                self.assertNotIn(forbidden, url)

    def test_relay_through_the_member_selector(self):
        body = '{"rc":0,"data":{"total":1,"diff":[{"f12":"600519","f14":"贵州茅台"}]}}'
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, body)):
            out = flow_proxy.relay_clist("b:BK0477", "f12,f14", 1, 100)
        self.assertTrue(out["ok"])
        self.assertEqual(flow_proxy.parse_body(out)["data"]["diff"][0]["f12"], "600519")

    def test_everything_else_is_still_rejected(self):
        # 负锁：个股市场 / 任意 b: 选择器 / 注入形态 / 空 BK 码，全部 400。
        for fs in ("m:0+t:6", "m:1+t:2", "b:BK", "b:BKX1", "b:BK0420;rm -rf",
                   "b:BK1234567", "b:90+t:2", "b:BK-0420", "BK0420", "b:bk0420"):
            with self.assertRaises(flow_proxy.FlowProxyError, msg=repr(fs)):
                flow_proxy.build_clist_url(fs, "f12,f14", 1, 100)
            status, payload = _route("kind=clist&fs=%s&fields=f12,f14" % fs)
            self.assertEqual(status, 400, fs)
            self.assertFalse(payload["ok"], fs)

    def test_member_query_rejects_unknown_fields(self):
        # 成分查询的字段面仍只认生产腿那一组（未知字段一律拒，绝不静默丢弃后照打）。
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_clist_url("b:BK0420", "f12,f999", 1, 100)
        with self.assertRaises(flow_proxy.FlowProxyError):
            flow_proxy.build_clist_url("b:BK0420", "", 1, 100)


class TestRouteMapping(unittest.TestCase):
    def setUp(self):
        flow_proxy.clear_cache()

    def test_ok_200(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, _FFLOW_BODY)):
            status, payload = _route("kind=fflow&secid=1.000001&klt=1&lmt=0")
        self.assertEqual(status, 200)
        self.assertTrue(payload["ok"])

    def test_bad_params_400(self):
        for query in ("kind=fflow&secid=000001",           # secid 缺市场前缀
                      "kind=fflow&secid=1.000001&klt=7",
                      "kind=fflow&secid=1.000001&klt=abc",
                      "kind=clist&fs=m:0+t:6&fields=f12",
                      "kind=clist&fs=m:90+t:2&fields=f999",
                      "kind=whatever"):
            status, payload = _route(query)
            self.assertEqual(status, 400, query)
            self.assertFalse(payload["ok"], query)

    def test_upstream_down_502(self):
        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(503, "")):
            status, payload = _route("kind=fflow&secid=1.000001&klt=1&lmt=0")
        self.assertEqual(status, 502)
        self.assertFalse(payload["ok"])

    def test_route_registered_before_broker_gate(self):
        # 结构锁：/flow/money 必须排在 active_broker.is_connected() 闸之前——
        # 盘前取历史、盘后取当日汇总都发生在 broker 未连时段，排在闸后等于整天读不到。
        # 桩必须是**类**而不是 SimpleNamespace：handle 里是 self._do_flow_money(...) 调用，
        # 挂在命名空间上的裸函数不会被绑定，request_handler 会错位成 self（首版就是这样假红的）。
        class _GatewayStub:
            _do_flow_money = gateway.Gateway._do_flow_money

        with mock.patch.object(flow_proxy, "fetch_upstream", return_value=(200, _FFLOW_BODY)):
            status, _ = gateway.Gateway.handle(
                _GatewayStub(), "GET", "/flow/money", None,
                _Handler("/flow/money?kind=fflow&secid=1.000001&klt=1&lmt=0"))
        self.assertEqual(status, 200)


if __name__ == "__main__":
    unittest.main()
