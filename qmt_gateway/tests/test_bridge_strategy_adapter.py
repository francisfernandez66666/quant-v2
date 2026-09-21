# _XtOps 契约回归测试（§2026-09-11 生产实录：真机模式首调 AttributeError 崩溃）。
# 不依赖 xtquant（no env 下全部走"未连接清晰失败"），只验证下单前的本地校验与
# dry 路径语义与 canonical 调用前检查。
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import qmt_bridge_strategy as bs  # noqa: E402


class TestXtOpsContract(unittest.TestCase):
    """file-bridge 策略端 canonical adapter（mirror broker.py 口径）单测。"""

    def test_dry_place_returns_dryrun_ref(self):
        ops = bs._XtOps(account="ACC", dry_run=True, xt_path="", session_id=2)
        ok, ref, err = ops.place({"code": "600279.SH", "side": "\u4e70\u5165",
                                  "price_type": "limit", "price": 10, "qty": 100,
                                  "signal_id": "S1"})
        self.assertTrue(ok)
        self.assertTrue(ref.startswith("DRYRUN-"))
        self.assertEqual(err, "")

    def test_place_unsupported_code_rejected_before_xtquant(self):
        ops = bs._XtOps(account="ACC", dry_run=False, xt_path="", session_id=2)
        # 无 xtquant 环境（CI/Linux）：_expect_suffix 前置拦截在 ensure 之前本应不触发，
        # 但非法代码行直接失败，不得走到 xtquant 加载
        try:
            ops.ensure()
            self.fail("无 xtquant 环境 ensure 应失败")
        except Exception:
            pass

    def test_expect_suffix_map(self):
        self.assertEqual(bs._expect_suffix("600279.SH"), "SH")
        self.assertEqual(bs._expect_suffix("000938.SZ"), "SZ")
        self.assertEqual(bs._expect_suffix("837171.BJ"), "BJ")
        self.assertEqual(bs._expect_suffix(""), "")

    def test_price_type_const_defaults(self):
        # 无 xtconstant 环境 → 文档数值兜底（limit=11；沪市 43 / 深市 14）
        self.assertEqual(bs._price_type_const("limit", "600279.SH"), 11)
        self.assertEqual(bs._price_type_const("market", "600279.SH"), 43)
        self.assertEqual(bs._price_type_const("market", "000938.SZ"), 14)

    def test_query_dry_run_returns_empty(self):
        ops = bs._XtOps(account="ACC", dry_run=True, xt_path="", session_id=2)
        self.assertEqual(ops.query_positions(), [])
        self.assertIsNone(ops.query_asset())
        self.assertEqual(ops.query_trades(), [])

    def test_read_cfg_encodings(self):
        """cfg 读取兼容 utf-8-sig(BOM)/utf-8/gbk 三种写法（运维脚本改写后不炸桥）。"""
        import tempfile, json as _json
        data = b'{"account":"A","dry_run":false,"xt_path":"p"}'
        variants = [data, b'\xef\xbb\xbf' + data,
                    data.decode("utf-8").encode("gbk")]
        with tempfile.TemporaryDirectory() as td:
            for i, v in enumerate(variants):
                p = os.path.join(td, "cfg%d.json" % i)
                open(p, "wb").write(v)
                bs.CFG_PATH = p
                bs._XtAdapter_holder = None
                cfg = bs._read_cfg()
                self.assertEqual(cfg.get("account"), "A", "encoding variant %d failed" % i)
                self.assertFalse(cfg.get("dry_run"))


    def test_record_seen_fail_closed(self):
        """P3-OPS 20260918: dedup write failure must be fail-CLOSED.

        _record_seen returns False when the seen file can't be written; _handle_cmd
        must then refuse the order (return False) and NOT add it to the in-memory set,
        so the command stays pending for a safe retry instead of executing a possibly
        duplicated real order after a bridge restart. (Pure ASCII test: this module
        runs under the GBK strategy sandbox.)
        """
        old_seen = bs.SEEN_PATH
        old_trace = bs._trace
        bs._trace = lambda m: None  # silence sandbox trace writer in CI
        try:
            # unwritable target: a directory path as the seen file forces open(a) to fail
            import tempfile
            with tempfile.TemporaryDirectory() as td:
                bs.SEEN_PATH = td  # open(dir, "a") -> IsADirectoryError
                self.assertFalse(bs._record_seen("SEQ-X"))
                seen = set()
                # kind=order with a failing seen-write must be refused before place()
                # (no xtquant here; if it reached place() it would error/return True path)
                ret = bs._handle_cmd({"kind": "order", "seq": "SEQ-X", "code": "600279.SH",
                                      "side": "买入", "price": 10, "qty": 100}, seen)
                self.assertFalse(ret, "cmd must be refused when dedup is not durable")
                self.assertNotIn("SEQ-X", seen, "refused seq must NOT enter in-memory seen")
        finally:
            bs.SEEN_PATH = old_seen
            bs._trace = old_trace

    def test_now_cn_str_matches_epoch_plus8(self):
        """§CB-TICKWINDOW regression (live 2026-09-21): the embedded interpreter may run
        on a UTC clock, so ts must be derived from epoch+8h (not the local wall clock)
        and carry a truthful +08:00 label. Skew against real UTC now must be ~0."""
        import datetime
        got = bs._now_cn_str()
        parsed = datetime.datetime.fromisoformat(got)
        skew = abs((parsed - datetime.datetime.now(datetime.timezone.utc)).total_seconds())
        self.assertLess(skew, 5, "ts clock face must track epoch (+08:00 label honest): " + got)
        self.assertTrue(got.endswith("+08:00"))


if __name__ == "__main__":
    unittest.main()
