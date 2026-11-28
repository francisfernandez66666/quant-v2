# -*- coding: utf-8 -*-
"""§FLOW-SECTOR-JOIN 板块逐票连接键生产者（pregen_sector_map_gz.py）行为锁（2026-11-20）。

owner 2026-11-20 裁决 A：板块腿走"扩白名单 + 生产者"。这条生产者是唯一一处
**把东财板块名接到个股代码上**的地方，它错了不会报错，只会让逐票板块 delta
贴到错的票上（或全不命中）——属于"读数看着正常、因子全错"的那一类。

本文件钉四件事：
  1. 分页：凑不齐 `data.total` 必须抛错，绝不返回半张成分表；
  2. 连接规则：**行业优先于概念**、同档按 BK 码升序、先到先得（确定性，两处各排一遍必漂）；
  3. diff 两种形态（索引 map / 数组）解出**同一个顺序**；
  4. 覆盖率闸：残表不覆盖旧文件（退出码 1），旧文件字节不变。
"""
import importlib.util
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

SCRIPT = os.path.join(os.path.dirname(__file__), "..", "..", "scripts",
                      "pregen_sector_map_gz.py")
spec = importlib.util.spec_from_file_location("pregen_sector_map_under_test", SCRIPT)
psm = importlib.util.module_from_spec(spec)
sys.modules["pregen_sector_map_under_test"] = psm
spec.loader.exec_module(psm)


def _body(rows, total=None):
    """造一份 clist 上游 body；rows 是 dict 列表。"""
    return {"rc": 0, "data": {"total": len(rows) if total is None else total,
                              "diff": rows}}


class RowsOfTest(unittest.TestCase):
    def test_index_map_and_array_resolve_to_the_same_order(self):
        rows = [{"f12": "BK0001", "f14": "A"}, {"f12": "BK0002", "f14": "B"}]
        as_map = {"data": {"total": 2, "diff": {"0": rows[0], "1": rows[1]}}}
        as_list = {"data": {"total": 2, "diff": list(rows)}}
        self.assertEqual([r["f12"] for r in psm.rows_of(as_map)[0]], ["BK0001", "BK0002"])
        self.assertEqual([r["f12"] for r in psm.rows_of(as_list)[0]], ["BK0001", "BK0002"])
        # 索引 map 的键序在上游不保证（JSON 对象无序），必须按数值索引重排，
        # 否则同一天两次跑出来的映射顺序会不同（映射本身仍是确定的）。
        shuffled = {"data": {"total": 2, "diff": {"1": rows[1], "0": rows[0]}}}
        self.assertEqual([r["f12"] for r in psm.rows_of(shuffled)[0]], ["BK0001", "BK0002"])

    def test_total_is_parsed_and_absent_total_is_minus_one(self):
        self.assertEqual(psm.rows_of({"data": {"total": "379", "diff": []}})[1], 379)
        self.assertEqual(psm.rows_of({"data": {"diff": []}})[1], -1)
        self.assertEqual(psm.rows_of({"data": None})[1], -1)


class ClistPagesTest(unittest.TestCase):
    """分页完整性：边界 1「全量或弃权」。"""

    def test_complete_paging_stops_at_total(self):
        calls = []

        def fake_relay(base, token, params):
            calls.append(params["pn"])
            pn = int(params["pn"])
            # 第一页满 100、第二页只回 34（total=134 已凑齐）。
            if pn == 1:
                return _body([{"f12": "BK%04d" % i} for i in range(100)], total=134)
            return _body([{"f12": "BK%04d" % i} for i in range(100, 134)], total=134)

        with mock.patch.object(psm, "relay", side_effect=fake_relay), \
                mock.patch.object(psm, "PAGE", 100), mock.patch.object(psm, "SLEEP_MS", 0):
            rows = psm.clist_pages("http://x", "t", "m:90+t:2", "f12,f14")
        self.assertEqual(len(rows), 134)
        self.assertEqual(calls, ["1", "2"])

    def test_short_page_raises_instead_of_returning_a_partial_table(self):
        def fake_relay(base, token, params):
            # 说 total=500 却只回 100 且第二页空 ⇒ 必须抛，不许交半张表。
            pn = int(params["pn"])
            if pn == 1:
                return _body([{"f12": "BK%04d" % i} for i in range(100)], total=500)
            return {"rc": 0, "data": {"total": 500, "diff": []}}

        with mock.patch.object(psm, "relay", side_effect=fake_relay), \
                mock.patch.object(psm, "PAGE", 100), mock.patch.object(psm, "SLEEP_MS", 0):
            with self.assertRaises(RuntimeError):
                psm.clist_pages("http://x", "t", "m:90+t:2", "f12,f14")

    def test_total_declared_but_pages_stop_early_raises(self):
        def fake_relay(base, token, params):
            return _body([{"f12": "BK%04d" % i} for i in range(3)], total=250)

        with mock.patch.object(psm, "relay", side_effect=fake_relay), \
                mock.patch.object(psm, "PAGE", 100), mock.patch.object(psm, "SLEEP_MS", 0):
            with self.assertRaises(RuntimeError):
                psm.clist_pages("http://x", "t", "m:90+t:2", "f12,f14")


class BoardRowsTest(unittest.TestCase):
    def test_non_bk_rows_are_dropped_and_order_is_by_code(self):
        rows = [{"f12": "BK0477", "f14": "酿酒行业"},
                {"f12": "BK0420", "f14": "汽车整车"},
                {"f12": "600519", "f14": "贵州茅台"},     # 不是板块码
                {"f12": "BK", "f14": "空码"},
                {"f12": "BK0477", "f14": ""}]            # 无名字
        with mock.patch.object(psm, "clist_pages", return_value=rows):
            out = psm.board_rows("http://x", "t", "m:90+t:2")
        self.assertEqual(out, [("BK0420", "汽车整车"), ("BK0477", "酿酒行业")])


class BuildMapTest(unittest.TestCase):
    """连接规则：行业优先、同档按码升序、一只票只认第一个命中的板块。"""

    def test_industry_wins_over_concept_for_the_same_stock(self):
        members = {"BK1000": ["600519", "000001"],   # 行业
                   "BK2000": ["600519"]}             # 概念（也含茅台）
        with mock.patch.object(psm, "members_of", side_effect=lambda b, t, c: members.get(c, [])):
            mapping, stats = psm.build_map("http://x", "t",
                                           [("BK1000", "酿酒行业")],
                                           [("BK2000", "白酒概念")])
        self.assertEqual(mapping["600519"], "酿酒行业")
        self.assertEqual(mapping["000001"], "酿酒行业")
        self.assertEqual(stats["industry_boards"], 1)
        self.assertEqual(stats["concept_boards"], 1)  # 概念板块仍然抓了，只是没抢到已占的票

    def test_first_board_wins_within_a_tier_and_order_is_by_code(self):
        # 同档内先到先得：调用顺序由 board_rows 的 BK 码升序决定。
        members = {"BK0002": ["600000"], "BK0001": ["600000", "600001"]}
        with mock.patch.object(psm, "members_of", side_effect=lambda b, t, c: members.get(c, [])):
            mapping, _ = psm.build_map("http://x", "t",
                                       [("BK0001", "先"), ("BK0002", "后")], [])
        self.assertEqual(mapping["600000"], "先")

    def test_a_failing_board_is_counted_not_silently_dropped(self):
        calls = {"n": 0}

        def fake_members(base, token, code):
            calls["n"] += 1
            if code == "BK9":
                raise RuntimeError("upstream reset")
            return ["600519"]

        with mock.patch.object(psm, "members_of", side_effect=fake_members):
            mapping, stats = psm.build_map("http://x", "t",
                                           [("BK1", "A"), ("BK9", "坏")], [])
        self.assertEqual(mapping, {"600519": "A"})
        self.assertEqual(stats["boards_ok"], 1)
        self.assertEqual(stats["boards_failed"], 1)
        self.assertTrue(stats["failed_codes"])
        self.assertIn("BK9", stats["failed_codes"][0])

    def test_empty_member_list_counts_as_failed(self):
        with mock.patch.object(psm, "members_of", return_value=[]):
            _, stats = psm.build_map("http://x", "t", [("BK1", "A")], [])
        self.assertEqual(stats["boards_ok"], 0)
        self.assertEqual(stats["boards_failed"], 1)


class CoverageGateTest(unittest.TestCase):
    """边界 2：残表不覆盖旧文件（宁可今天弃权，也不写一份看起来正常的残表）。"""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.out = os.path.join(self.dir, "local_executor_sector_map.json")
        with open(self.out, "w", encoding="ascii") as fh:
            json.dump({"day": "20261119", "sector_map": {"600519": "旧表"}}, fh)
        # ENGINE_OUT 也必须指到临时目录：它是云端引擎那份（<DataDir>/sector_map.json），
        # 用例如果打真实路径就会往机器上写一份假表（本机是权限失败，容器里可能真写进 /var/lib）。
        self.engine_out = os.path.join(self.dir, "data", "sector_map.json")
        self.saved = (psm.OUT_PATH, psm.ENGINE_OUT, psm.MIN_BOARD_OK_PCT,
                      psm.MIN_STOCKS, psm.SLEEP_MS)
        psm.OUT_PATH = self.out
        psm.ENGINE_OUT = self.engine_out
        psm.SLEEP_MS = 0

    def tearDown(self):
        (psm.OUT_PATH, psm.ENGINE_OUT, psm.MIN_BOARD_OK_PCT,
         psm.MIN_STOCKS, psm.SLEEP_MS) = self.saved

    def test_low_coverage_aborts_and_leaves_the_old_file_untouched(self):
        before = open(self.out, "rb").read()
        psm.MIN_BOARD_OK_PCT = 60.0
        psm.MIN_STOCKS = 3000
        with mock.patch.object(psm, "load_endpoint", return_value=("http://x", "t")), \
                mock.patch.object(psm, "board_rows", return_value=[("BK1", "A")]), \
                mock.patch.object(psm, "build_map", return_value=({"600519": "A"}, {
                    "boards_ok": 1, "boards_failed": 40, "failed_codes": []})):
            with self.assertRaises(SystemExit) as ctx:
                psm.main()
        self.assertEqual(ctx.exception.code, 1)
        self.assertEqual(open(self.out, "rb").read(), before)

    def test_healthy_run_writes_a_day_keyed_file(self):
        psm.MIN_BOARD_OK_PCT = 60.0
        psm.MIN_STOCKS = 2
        mapping = {"600519": "酿酒行业", "000001": "银行"}
        with mock.patch.object(psm, "load_endpoint", return_value=("http://x", "t")), \
                mock.patch.object(psm, "board_rows", return_value=[("BK1", "酿酒行业")]), \
                mock.patch.object(psm, "build_map", return_value=(mapping, {
                    "boards_ok": 10, "boards_failed": 0, "failed_codes": [],
                    "industry_boards": 1})):
            self.assertEqual(psm.main(), 0)
        doc = json.load(open(self.out, encoding="ascii"))
        self.assertEqual(len(doc["day"]), 8)          # YYYYMMDD，与 precalc 同一日期口径
        self.assertEqual(doc["sector_map"], mapping)
        self.assertEqual(doc["count"], 2)
        # §FLOW-SECTOR-JOIN 云端侧：同一份表必须同时落到 <DataDir>/sector_map.json，
        # 否则云端引擎那条腿仍读不到（载体接上、云端没接＝半条腿）。
        self.assertTrue(os.path.exists(self.engine_out))
        self.assertEqual(json.load(open(self.engine_out, encoding="ascii"))["sector_map"], mapping)

    def test_empty_board_list_aborts(self):
        with mock.patch.object(psm, "load_endpoint", return_value=("http://x", "t")), \
                mock.patch.object(psm, "board_rows", return_value=[]):
            with self.assertRaises(SystemExit) as ctx:
                psm.main()
        self.assertEqual(ctx.exception.code, 1)


class FailureReadoutTest(unittest.TestCase):
    """边界 5（2026-11-28 收口批）：失败读数必须带 HTTP 状态码，且瞬时失败要重试。

    现场：11-27 08:20 定时器 `SECTOR_MAP_RESULT=ABORT reason=board_list_failed:HTTPError`
    1 秒内失败，而当天手工重跑是 `OK boards_ok=1057 stocks=5662` —— 是瞬时抖动。
    旧读数把 `code` 吞掉，"钥匙被拒 / 路径不存在 / 上游 503"三种处置完全不同却同形；
    旧 unit 又写死 `Restart=no` ⇒ 一次抖动等于整天没有连接键、`ExecStartPost` 的存档步也不跑。
    """

    def test_describe_keeps_http_status_code(self):
        import urllib.error
        exc = urllib.error.HTTPError("http://x", 503, "Service Unavailable", None, None)
        self.assertIn("503", psm.describe(exc))
        self.assertIn("HTTPError", psm.describe(exc))

    def test_describe_without_code_stays_short(self):
        self.assertEqual(psm.describe(RuntimeError("boom")), "RuntimeError")

    def test_with_retry_retries_then_succeeds(self):
        # 前两次抛 URLError（模拟中继抖动），第三次成功 ⇒ 必须重试到成功，且退避走注入的 no-op sleep。
        import urllib.error
        calls = []
        saved = psm.time.sleep
        psm.time.sleep = lambda *_: None
        try:
            def fn():
                calls.append(1)
                if len(calls) < 3:
                    raise urllib.error.URLError("flaky")
                return "ok"
            self.assertEqual(psm.with_retry("t", fn, attempts=3, base_sleep=0), "ok")
        finally:
            psm.time.sleep = saved
        self.assertEqual(len(calls), 3)

    def test_with_retry_raises_the_last_error(self):
        saved = psm.time.sleep
        psm.time.sleep = lambda *_: None
        try:
            def fn():
                raise ValueError("permanent")
            with self.assertRaises(ValueError):
                psm.with_retry("t", fn, attempts=2, base_sleep=0)
        finally:
            psm.time.sleep = saved


if __name__ == "__main__":
    unittest.main()
