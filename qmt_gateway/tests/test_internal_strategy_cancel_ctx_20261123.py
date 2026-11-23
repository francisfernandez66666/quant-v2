# test_internal_strategy_cancel_ctx_20261123.py — 撤单 4 参形态必须带 ContextInfo（2026-11-23）。
#
# 现场：盒上一天 24 次撤单全败，日志里只有
#   cancel failed id='1397' ... ArgumentError('Python argument types in None.None(str, ...')
# 根因：_cancel 把前两条形状标成 "4s"/"4l"，实际只传了 3 个参数。这个 QMT build 的
# 文档形态是 cancel(order_id, account_id, account_type, ContextInfo) —— 少了 ContextInfo
# 就落到 None.None，任何 3 参/2 参形态都不成立。
#
# 本文件锁三件事：
#   1. 带 ContextInfo 时，第一条尝试就是 4 参（"4s"），且确实把 ContextInfo 传进去；
#   2. 4 参失败后仍按 3 参/2 参降级，最后才是数值 "2i"（且 int(oid) 在循环之外）；
#   3. ContextInfo 缺失时**跳过** 4 参并留一行 cancel_ctx_missing，不许静默。
#
# English: locks for the 4-argument cancel shape — the fix for the day when every
# cancel failed because the context object was never passed through.
import importlib.util
import os
import sys
import unittest

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, GW_DIR)

_SPEC = importlib.util.spec_from_file_location(
    "qmt_strategy_cancel_ctx", os.path.join(GW_DIR, "qmt_internal_strategy.py"))
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class _Ctx(object):
    """Opaque ContextInfo stand-in: the ladder only forwards it, never reads it."""


class CancelShapeTest(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.logs = []
        self.orig_log = mod._log
        mod._log = lambda text: self.logs.append(text)
        mod._STATE.update({"acct": "8001", "acct_ok": True, "ops": [], "warned": {}})
        mod._trade_session_ok = lambda *a, **k: (True, "")
        mod._SANDBOX_FNS.update({"cancel": lambda *a: self.calls.append(a)})

    def tearDown(self):
        mod._log = self.orig_log
        mod._SANDBOX_FNS.pop("cancel", None)

    def test_first_shape_passes_context_info(self):
        ctx = _Ctx()
        ok, why = mod._cancel(ctx, "1397")
        self.assertTrue(ok, why)
        self.assertEqual(self.calls[0], ("1397", "8001", "STOCK", ctx),
                         "第一条尝试必须是 4 参且带 ContextInfo")
        self.assertIn("cancelled via 4s", "\n".join(self.logs))

    def test_falls_back_through_three_then_two_then_numeric(self):
        ctx = _Ctx()
        seen = []

        def cancel(*args):
            seen.append(args)
            if len(args) == 4:
                raise TypeError("Python argument types in None.None(str, ...")
            if args == ("1397", "8001", "STOCK"):
                raise TypeError("still wrong")
            if args == ("1397", "8001"):
                raise TypeError("still wrong")
            if args == (1397, "8001"):
                return None
            raise AssertionError("unexpected shape %r" % (args,))

        mod._SANDBOX_FNS.update({"cancel": cancel})
        ok, why = mod._cancel(ctx, "1397")
        self.assertTrue(ok, why)
        self.assertEqual(seen[-1], (1397, "8001"), "数值 2i 必须是最后一条")
        # 4s / 4l / 3 / 2s / 2i 全部试过，且没有在构建 attempts 时 ValueError。
        self.assertEqual(len(seen), 5, seen)

    def test_missing_context_is_logged_not_silent(self):
        ok, why = mod._cancel(None, "1397")
        self.assertTrue(ok, why)
        self.assertEqual(self.calls[0], ("1397", "8001", "STOCK"),
                         "没有 ContextInfo 时第一条应是 3 参（4 参被跳过）")
        self.assertTrue([l for l in self.logs if "cancel_ctx_missing" in l],
                        "跳过 4 参必须留痕")

    def test_every_leg_error_is_kept(self):
        def cancel(*args):
            raise TypeError("ArgumentError('Python argument types in None.None(str, ...")

        mod._SANDBOX_FNS.update({"cancel": cancel})
        ok, why = mod._cancel(_Ctx(), "1397")
        self.assertFalse(ok)
        for tag in ("4s", "4l", "3", "2s", "2i"):
            self.assertIn(tag + ":", why, "缺了 %s 那一腿的错误：%s" % (tag, why))


if __name__ == "__main__":
    unittest.main()
