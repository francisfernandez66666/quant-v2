#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§LEG-CONTRACT-GATE（批 4）回归锁：契约指纹不符时"只报不接单"。

钉住四件事：
  ① **缺省不比对**：没配 `expected_contract_fp` ⇒ 一条判断都不加，现网那台行为逐字节不变；
  ② 比对不过 ⇒ 只有 /order 与 /cancel 被拒（503 + code=contract_mismatch），
     /health、/state、/orders、/settlement、/quotes 一律照常——观测面不能被版本问题一起关掉，
     否则排障时既看不到腿的账、也看不到腿的版本；
  ③ **拒单不进健康线**：`ok` 与 `live_ready` 不因契约不符而变化（"版本旧"要升版、"通道坏"要查链路，
     折成一个布尔就把处置方向指错，这也是 §STARVE-STALE"只读不进数据面"的同族纪律）；
  ④ 契约目录读不到 ⇒ 判"unreadable" 而不是空串：空串会被当成"没配期望值"而放行，那是最坏的反面。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
from tests.test_manual_sync import new_db_path  # noqa: E402


def build_gateway(**over):
    cfg = dict(gw_mod.DEFAULT_CONFIG)
    cfg.update({"broker": "mock", "db": new_db_path(), "report_url": "",
                "user_id": "u_friend", "owner_user_id": "u_friend",
                "token": "tk-friend"})
    cfg.update(over)
    return gw_mod.Gateway(cfg)


class TestDefaultIsToday(unittest.TestCase):
    def test_no_expected_fp_means_no_check(self):
        gw = build_gateway()
        self.assertFalse(gw._contract_checked)
        self.assertTrue(gw._contract_ok)
        self.assertIsNone(gw._contract_guard())

    def test_fingerprint_is_stable_and_not_empty(self):
        # 目录里真有一堆契约文件 ⇒ 必须给出 16 位十六进制，绝不能是空串/unreadable
        fp = gw_mod.contract_fingerprint(os.path.join(
            os.path.dirname(os.path.abspath(gw_mod.__file__)), "contract"))
        self.assertEqual(len(fp), 16)
        self.assertNotEqual(fp, "unreadable")


class TestMismatchRefusesOrders(unittest.TestCase):
    def test_mismatch_returns_503_with_code(self):
        gw = build_gateway(expected_contract_fp="0123456789abcdef")
        status, payload = gw._contract_guard()
        self.assertEqual(status, 503)
        self.assertEqual(payload["code"], "contract_mismatch")
        self.assertIn("contract_fp", payload)

    def test_handle_blocks_order_and_cancel_only(self):
        gw = build_gateway(expected_contract_fp="0123456789abcdef")
        for path in ("/order", "/cancel"):
            status, payload = gw.handle("POST", path, {"signal_id": "s1"}, None)
            self.assertEqual(status, 503, path)
            self.assertEqual(payload.get("code"), "contract_mismatch", path)
        # 只读面必须照常：/health 的 ok 仍是 True（进程活着这件事没被版本问题污染）
        h = gw._health_payload()
        self.assertTrue(h["ok"])
        self.assertFalse(h["contract_ok"])
        self.assertTrue(h["contract_checked"])

    def test_matching_fp_allows(self):
        real = gw_mod.contract_fingerprint(os.path.join(
            os.path.dirname(os.path.abspath(gw_mod.__file__)), "contract"))
        gw = build_gateway(expected_contract_fp=real)
        self.assertTrue(gw._contract_ok)
        self.assertTrue(gw._contract_checked)
        self.assertIsNone(gw._contract_guard())


class TestUnreadableIsFailClosed(unittest.TestCase):
    def test_missing_contract_dir_is_unreadable_not_empty(self):
        fp = gw_mod.contract_fingerprint(os.path.join(os.path.dirname(
            os.path.abspath(gw_mod.__file__)), "nope_dir"))
        self.assertEqual(fp, "unreadable")

    def test_unreadable_blocks_when_expected_set(self):
        gw = build_gateway(expected_contract_fp="0123456789abcdef")
        gw._contract_fp = "unreadable"  # 模拟契约目录被删/权限坏了
        status, payload = gw._contract_guard()
        self.assertEqual(status, 503)
        self.assertEqual(payload["contract_fp"], "unreadable")

    def test_health_and_ok_are_untouched_by_mismatch(self):
        # 负锁的等价物：契约不符时 ok/live_ready 的取值只由通道决定——
        # 这两个字段是熔断与看门狗的输入，一旦被版本判断污染，"该升版"就会读成"通道坏了"
        gw = build_gateway(expected_contract_fp="0000000000000000")
        h = gw._health_payload()
        self.assertTrue(h["ok"])
        self.assertEqual(h["live_ready"], gw.active_broker.is_connected())


if __name__ == "__main__":
    unittest.main()
