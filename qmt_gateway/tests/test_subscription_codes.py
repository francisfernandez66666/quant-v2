#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§SUBSCRIPTION（2026-11-01）申购类委托识别的网关侧单测。

覆盖四件事（对应 docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md B1/B2 的断言表）：
  ① 段表与 contract/subscription_codes.json 双向等值（Go 侧有一份同款锁，防两端漂移）；
  ② 开关打开 ⇒ 申购行**照常上报**（裁决 7 要能看见）、状态原样透传**不造中文语义**、
     且按 oid 去重（委托表每 10s 重放，计数不许按轮次虚增）；
  ③ 开关关着 ⇒ 行为与今天**逐字节一致**（仍被"未知状态"那条分支吞掉、仍计 unknown），
     但 subscription_would_label 在累加（追加裁决 15：关≠不存在）；
  ④ 网关不判定"与正股同段"（00/30）——它没有 IPO 日历，按裁决 8 留在不判定一侧。

不依赖 Windows/xtquant：直接构造 Gateway 并喂委托表快照行。
"""
import json
import os
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
from gateway import Gateway  # noqa: E402

GOLDEN = (pathlib.Path(__file__).resolve().parents[1] / "contract" / "subscription_codes.json")


def new_db_path():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class _SnapCase(unittest.TestCase):
    """公共工装：起一个 mock 网关，把 handler.on_order 换成记录桩。"""

    recognize = False

    def setUp(self):
        cfg = {
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "T0001",
            "db": new_db_path(), "report_url": "", "report_token": "", "reconcile_sec": 0,
            "failover_enable": False, "user_id": "uA",
            "subscription_recognize": self.recognize,
        }
        self.gw = Gateway(cfg)
        self.events = []
        # 桩：记录所有上报事件，让"是否上报"成为可直接断言的事实而不是查库推断
        self.gw.handler.on_order = lambda ev: self.events.append(ev)

    def tearDown(self):
        try:
            self.gw.stop()
        except Exception:
            pass

    def _today(self):
        return gw_mod._now_cn()[:10]

    def _row(self, **kw):
        row = {"order_id": "S1", "code": "370181.SZ", "side": "买入", "status": "未知(86)",
               "qty": 0, "filled_qty": 0, "price": 0, "remark": "",
               "created_at": self._today() + " 09:31:00"}
        row.update(kw)
        return row

    def _snap(self, rows):
        return self.gw._apply_orders_snapshot(
            {"type": "orders_snapshot", "orders": rows, "synced": True,
             "data_ts": gw_mod._now_cn()})

    def _sync(self):
        return self.gw._do_admin_status()[1]["orders_sync"]


class TestGoldenTable(unittest.TestCase):
    """① 段表与 golden 双向等值（多余/缺失都红）。"""

    @classmethod
    def setUpClass(cls):
        cls.g = json.loads(GOLDEN.read_text(encoding="utf-8"))

    def test_only_segments_equal(self):
        self.assertEqual(sorted(self.g["only_segments"]), sorted(gw_mod.SUBSCRIPTION_ONLY_PREFIXES),
                         "网关段表与 golden 漂移：两端不一致＝网关跳了、Go 落库了")

    def test_shared_segments_equal(self):
        self.assertEqual(sorted(self.g["shared_segments"]), sorted(gw_mod.SUBSCRIPTION_SHARED_PREFIXES))

    def test_calibration_flag(self):
        self.assertIn(self.g["calibration"], ("pending", "fixed"))

    def test_unverified_segments_not_in_table(self):
        # 校准前的段绝不允许已经被塞进判据（北交所 4/8/920 段是真持仓代码）
        for seg in self.g["unverified_segments"]:
            for p in gw_mod.SUBSCRIPTION_ONLY_PREFIXES:
                self.assertFalse(str(seg).startswith(p),
                                 "未校准段 %s 与判据段 %s 重叠（裁决 9 要求先校准再固化）" % (seg, p))

    def test_bare_code_shapes(self):
        cases = {"370181.SZ": "370181", "600519.SH": "600519", "sh730899": "730899",
                 "SZ002961": "002961", "  600000 ": "600000", "60000": "", "abc123": "", "": ""}
        for raw, want in cases.items():
            self.assertEqual(gw_mod._bare_code(raw), want, "裸码归一形状错: %r" % raw)

    def test_golden_samples(self):
        for s in self.g["samples"]:
            got = gw_mod.subscription_only_segment(s["code"])
            want_sub = bool(s["expect_subscription"]) and s["expect_reason"] == "only-segment"
            self.assertEqual(got, want_sub,
                             "golden 样本 %s 段判定=%s 期望=%s" % (s["code"], got, want_sub))


class TestRecognizeOff(_SnapCase):
    """③ 开关关着 ⇒ 行为与今天逐字节一致，但留痕计数在走（追加裁决 15）。"""

    recognize = False

    def test_row_still_swallowed_as_today(self):
        self._snap([self._row()])
        self.assertEqual(self.events, [], "开关关着时申购行不得上报（必须等价今天的字节行为）")
        sync = self._sync()
        self.assertEqual(sync["unknown_status_skipped"], 1, "关状态须与今天一致：仍计 unknown")
        self.assertEqual(sync["subscription_rows"], 0)

    def test_off_state_still_counts_would_label(self):
        self._snap([self._row()])
        sync = self._sync()
        self.assertEqual(sync["subscription_would_label"], 1,
                         "关≠不存在：本应标注的申购行必须留痕（这段计数也是段表校准的取证源）")
        self.assertFalse(sync["subscription_recognize"])

    def test_would_label_deduped_by_oid(self):
        # 委托表每 10s 重放同一行 ⇒ 计数按 oid 去重，不许按轮次虚增
        self._snap([self._row()])
        self._snap([self._row()])
        self._snap([self._row()])
        self.assertEqual(self._sync()["subscription_would_label"], 1,
                         "同一 oid 被计了三次：计数按 oid 去重，否则运维对不上当日笔数")

    def test_non_subscription_unknown_unchanged(self):
        self._snap([self._row(code="600000.SH", status="未知(255)", qty=100, price=10)])
        sync = self._sync()
        self.assertEqual(sync["unknown_status_skipped"], 1)
        self.assertEqual(sync["subscription_would_label"], 0)


class TestRecognizeOn(_SnapCase):
    """②④ 开关打开 ⇒ 申购行照常可见、状态不造假、同段不判定。"""

    recognize = True

    def test_subscription_row_is_reported_and_counted(self):
        self._snap([self._row()])
        self.assertEqual(len(self.events), 1, "申购行必须照常上报（裁决 7：委托列表要能看见）")
        self.assertEqual(self.events[0]["code"], "370181.SZ")
        sync = self._sync()
        self.assertEqual(sync["subscription_rows"], 1)
        self.assertEqual(sync["unknown_status_skipped"], 0,
                         "识别后不得再注水 unknown——这个告警位从此只表示真未知码")

    def test_never_fakes_status_label(self):
        # owner 裁定：不给 86 造中文语义。事件体里的 status 必须还是原始那个串。
        self._snap([self._row(status="未知(86)")])
        self.assertEqual(self.events[0]["status"], "未知(86)",
                         "状态被改写了——绝不给未知码硬造中文态")

    def test_rows_deduped_across_replays(self):
        self._snap([self._row()])
        self._snap([self._row()])
        self.assertEqual(self._sync()["subscription_rows"], 1)

    def test_real_unknown_code_still_skipped(self):
        # 本批不许把"未知"整体改成"照收"：非申购段的未知仍旧吞行 + 计 unknown
        self._snap([self._row(code="600000.SH", order_id="R1", status="未知(255)", qty=100, price=10)])
        self.assertEqual(self.events, [])
        sync = self._sync()
        self.assertEqual(sync["unknown_status_skipped"], 1)
        self.assertEqual(sync["subscription_rows"], 0)

    def test_shared_segment_not_decided_in_gateway(self):
        # 深市新股与正股同段（00/30）：网关没有 IPO 日历 ⇒ 一律不判定，留在不判定一侧
        self._snap([self._row(code="002961.SZ", order_id="X1", status="未知(86)", qty=0, price=0)])
        self.assertEqual(self.events, [], "网关不得凭 00/30 段判定申购（无上市证据）")
        self.assertEqual(self._sync()["unknown_status_skipped"], 1)

    def test_known_status_subscription_still_reported_once(self):
        # 哪天柜台给申购行一个可读状态（已报），它也只落一条：证明"锚不依赖 86 读不懂"
        self._snap([self._row(code="730899.SH", order_id="K1", status="已报", qty=1000, price=50)])
        self.assertEqual(len(self.events), 1)
        self.assertEqual(self.events[0]["status"], "已报")


if __name__ == "__main__":
    unittest.main()
