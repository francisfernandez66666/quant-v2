#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§0926E2E-W2C（2026-09-26 二波）落盘密钥加固——load_config 权限自检行为用例。

覆盖 gateway.load_config 新增的"配置文件权限宽于 0600 → log.warning"腿：
  ① 0644（组/其他可读）必须告警，且告警里能定位是哪个文件（运维拿到就能 chmod）；
  ② 0600 必须静默（告警只吵真问题；每启动一条无害 warning 会把真告警淹死）；
  ③ 判定口径只在 POSIX 生效——Windows 的 st_mode 恒报 0o666，拿它判宽严是
     永久性假红（NTFS 收敛责任在生成端 ensure_gateway_config.ps1 的 icacls）；
  ④ 权限自检不得改变配置解析结果（合并语义/优先级与收口前一致——只加告警，
     不碰返回值，"只吵不改"设计裁决的机器锁）。
mock 默认口令告警（cmd/qmt-mock 启动日志一行）属 Go main 内联，由门禁静态锁与
人工启动目检覆盖，不在本文件。
"""
import logging
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import load_config  # noqa: E402


class TestW2cConfigPermWarning(unittest.TestCase):
    def setUp(self):
        # load_config 的 token 优先级会读 env；测试环境必须清干净，
        # 否则本机导出过 QUANT_GATEWAY_TOKEN 会让用例跟着环境漂（§探针取值链纪律）。
        for k in ("QUANT_GATEWAY_TOKEN", "QUANT_GATEWAY_REPORT_TOKEN"):
            self._old = getattr(self, "_old", {})
            self._old[k] = os.environ.pop(k, None)
        self.tmp = tempfile.TemporaryDirectory()
        # §全局静音对策（先例：test_claim_release._LogCaptureMixin 留字）：
        # test_xt_mapping / test_channel_position_fields 在 import 期执行
        # logging.disable(CRITICAL)，pytest 同进程收集全部模块 → 本文件排后时该全局开关
        # 仍生效，而 assertLogs 不会解除 disable，"网关确实告了警但抓到空"＝假红。
        # 用例期间临时解除、tearDown 还原原值：不破坏其他模块的静音意图，也不依赖执行顺序。
        self._prev_log_disable = logging.Logger.manager.disable
        logging.disable(logging.NOTSET)

    def tearDown(self):
        logging.disable(self._prev_log_disable)
        for k, v in getattr(self, "_old", {}).items():
            if v is not None:
                os.environ[k] = v
        self.tmp.cleanup()

    def _write_cfg(self, mode=None):
        path = os.path.join(self.tmp.name, "config.xt.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write('{"token": "change-me", "report_url": "http://127.0.0.1:8080"}')
        if mode is not None:
            os.chmod(path, mode)
        return path

    def test_loose_permissions_warn(self):
        if os.name != "posix":
            self.skipTest("Windows st_mode 恒 0o666，权限判定按设计只在 POSIX 生效")
        path = self._write_cfg(0o644)
        logger = logging.getLogger("qmt_gateway")
        old_level, old_prop = logger.level, logger.propagate
        records = []

        class Cap(logging.Handler):
            def emit(self, record):
                records.append(record)

        h = Cap(level=logging.WARNING)
        logger.addHandler(h)
        logger.setLevel(logging.WARNING)
        try:
            cfg = load_config(path)
        finally:
            logger.removeHandler(h)
            logger.level, logger.propagate = old_level, old_prop
        hits = [r for r in records if "宽于" in r.getMessage()]
        self.assertTrue(hits, "0644 权限必须触发『宽于 0600』告警，实得 %s" % [r.getMessage() for r in records])
        self.assertIn("config.xt.json", hits[0].getMessage(), "告警必须点名文件路径，否则运维不知道 chmod 谁")
        # ④ 只吵不改：解析结果不受权限告警影响
        self.assertEqual(cfg.get("token"), "change-me")
        self.assertEqual(cfg.get("report_url"), "http://127.0.0.1:8080")

    def test_0600_is_silent(self):
        if os.name != "posix":
            self.skipTest("同上：POSIX-only 判定")
        path = self._write_cfg(0o600)
        with self.assertLogs("qmt_gateway", level="WARNING") as cm:
            # 基准锚点：assertLogs 零产出会直接抛 AssertionError，先垫一条；
            # load_config 必须在 with 内跑才会被捕获，cm.output 成稿要等块退出。
            load_config(path)
            logging.getLogger("qmt_gateway").warning("w2c-baseline")
        perm_hits = [m for m in cm.output if "宽于" in m]
        self.assertEqual(perm_hits, [], "0600 文件不得再有权限告警（告警疲劳=真告警被淹）")

    def test_no_file_no_crash(self):
        # 配置文件不存在=纯默认值路径（开发/UAT 常用），权限自检须整体跳过而非抛错。
        cfg = load_config(os.path.join(self.tmp.name, "absent.json"))
        self.assertEqual(cfg.get("token"), "change-me")


if __name__ == "__main__":
    unittest.main()
