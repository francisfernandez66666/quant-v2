# ── §BRIDGE-PATH（2026-09-24，任务 #43 根因用例）桥文件路径跨平台落点测试 ──
# 现象：仓库根反复长出一个字面带反斜杠的文件 `C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log`，
#       每次跑 qmt_gateway 测试就再生一次（09-23/09-24 各清理过一次，又回来了）。
# 根因：桥策略模块把 Windows 目录常量用**字面反斜杠**拼接（BRIDGE_DIR + "\\bridge_boot.log"）。
#       在 POSIX 上反斜杠不是路径分隔符，`open("C:\\...\\x.log","ab")` 不但不会失败，
#       还会在当前工作目录创建一个"名字里带反斜杠"的文件 ⇒ 测试一导入就污染工作树，
#       而且 trace 落在没人看的地方（等于诊断失明）。
# 为什么不能直接把默认值改成相对路径/POSIX 路径：广州那台 QMT 机器就靠这个绝对目录做文件桥
#       （bridge_report.jsonl / bridge_cmd.json / bridge_seen.jsonl 是真实通道，不是日志），
#       改默认值＝改生产接线，且判重 seen 文件一旦换地方就是"重启后可重放同一笔委托"的资金风险。
# 修法前置：① 目录可由 QMT_BRIDGE_DIR 覆盖（广州机器上没有这个变量 ⇒ 取值与改前逐字相同），
#           测试会话由 conftest.py 指到临时目录；
#           ② 拼接一律走 os.path.join（Windows 下与旧的字符串相加结果完全一致）；
#           ③ 写盘前过 _dir_ready()：路径在本机根本不构成"目录/文件"两段时**拒写**，
#              而不是在 CWD 造文件——trace 属诊断可丢，report/seen 走既有 False 分支（fail-closed）。
# English: regression for the bridge path plumbing — the Windows default must survive verbatim
# (it is the real transport directory), while a POSIX host must refuse to write instead of
# creating a backslash-named junk file in the repository root.
import importlib
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
import qmt_bridge_strategy as bs  # noqa: E402

STRATEGY_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "qmt_bridge_strategy.py")
WIN_DEFAULT = r"C:\qmt\quant-trading-v2\qmt_gateway"


class TestDirReady(unittest.TestCase):
    """_dir_ready 的三态：真目录可写 / Windows 路径落在 POSIX 上拒写 / 目录不存在拒写。"""

    def test_real_directory_is_ready(self):
        with tempfile.TemporaryDirectory() as td:
            self.assertTrue(bs._dir_ready(os.path.join(td, "bridge_boot.log")),
                            "目录存在时必须可写（生产路径就是这一态）")

    def test_windows_path_on_posix_is_not_ready(self):
        # POSIX 下 dirname 为空、basename 是整串——只查 isdir 会漏，必须查分隔符
        self.assertFalse(bs._dir_ready(WIN_DEFAULT + "\\bridge_boot.log"))

    def test_missing_directory_is_not_ready(self):
        self.assertFalse(bs._dir_ready("/tmp/definitely-not-here-xyz-qmt/bridge_boot.log"))


class TestNoJunkFileInCwd(unittest.TestCase):
    """负锁本体：拿不存在的 Windows 路径去 trace/report/record_seen，工作目录必须一个文件都不长。"""

    def test_refused_writes_create_nothing(self):
        with tempfile.TemporaryDirectory() as cwd:
            old = (bs.TRACE_PATH, bs.REPORT_PATH, bs.SEEN_PATH)
            try:
                bad = WIN_DEFAULT + "\\bridge_boot.log"
                bs.TRACE_PATH, bs.REPORT_PATH, bs.SEEN_PATH = bad, bad, bad
                cwd_before = set(os.listdir(cwd))
                bs._trace("must not create a file")
                # report/seen 走既有的 False 分支：调用方据此拒单/重试，绝不静默当成功
                self.assertFalse(bs._report({"k": 1}), "report 写不下去必须返回 False")
                self.assertFalse(bs._record_seen("SEQ-NOJUNK"), "判重记录写不下去必须 fail-closed")
                self.assertNotIn("SEQ-NOJUNK", bs._load_seen(),
                                 "被拒的 seq 不得出现在判重集合里（否则重启后就会重放这一笔）")
                self.assertEqual(set(os.listdir(cwd)), cwd_before, "工作目录不得长出任何新文件")
            finally:
                bs.TRACE_PATH, bs.REPORT_PATH, bs.SEEN_PATH = old

    def test_repo_root_stays_clean_after_import(self):
        """整仓根目录不得出现"名字里带反斜杠"的桥文件（改前每次 pytest 都会生成一个）。"""
        root = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
        junk = [n for n in os.listdir(root) if "\\" in n or (len(n) > 1 and n[1] == ":")]
        self.assertEqual(junk, [], "仓库根有 Windows 形态的怪文件：%s" % junk)


class TestBridgeDirOverride(unittest.TestCase):
    """QMT_BRIDGE_DIR 覆盖生效；未设置时默认值必须与改前**逐字相同**（生产接线不许动）。"""

    def _reload(self, envval):
        prev = os.environ.get("QMT_BRIDGE_DIR")
        if envval is None:
            os.environ.pop("QMT_BRIDGE_DIR", None)
        else:
            os.environ["QMT_BRIDGE_DIR"] = envval
        try:
            m = importlib.reload(bs)
            # 取值必须在**还原 env 之前**：reload 会把五个常量重算一遍，晚读就读回 conftest 的目录了
            return m.BRIDGE_DIR, m.TRACE_PATH, {k: getattr(m, k) for k in
                                                ("REPORT_PATH", "CFG_PATH", "CMD_PATH", "SEEN_PATH")}
        finally:
            if prev is None:
                os.environ.pop("QMT_BRIDGE_DIR", None)
            else:
                os.environ["QMT_BRIDGE_DIR"] = prev
            importlib.reload(bs)  # 复位：其它用例看到的仍是 conftest 的临时目录

    def test_unset_keeps_verbatim_windows_default(self):
        d, trace, _ = self._reload(None)
        self.assertEqual(d, WIN_DEFAULT)
        self.assertEqual(trace, os.path.join(WIN_DEFAULT, "bridge_boot.log"))
        # os.path.join 在 Windows 上给出反斜杠串；在 POSIX 上只是拼接（本机不会真的写，见 _dir_ready）
        self.assertTrue(trace.endswith("bridge_boot.log"))

    def test_env_override_redirects_all_five_paths(self):
        with tempfile.TemporaryDirectory() as td:
            d, trace, others = self._reload(td)
            self.assertEqual(d, td)
            self.assertEqual(trace, os.path.join(td, "bridge_boot.log"))
            for name in ("REPORT_PATH", "CFG_PATH", "CMD_PATH", "SEEN_PATH"):
                v = others[name]
                self.assertTrue(v.startswith(td + os.sep), "%s 没跟着目录走：%s" % (name, v))
                self.assertNotIn("\\", os.path.basename(v))


class TestJoinFormIsSingleSource(unittest.TestCase):
    """写法层负锁：五个常量一律经 _p()（os.path.join）拼出来，旧的 `BRIDGE_DIR + "\\..."` 不得复活。"""

    def test_no_manual_backslash_join(self):
        src = open(STRATEGY_FILE, "rb").read().decode("ascii")  # 非 ASCII 直接抛：GBK 沙箱禁忌
        for line in src.splitlines():
            if "BRIDGE_DIR +" in line:
                self.fail("仍在用字符串相加拼桥路径（POSIX 上会产生反斜杠文件名）：%s" % line)
        self.assertGreaterEqual(src.count("= _p("), 5, "五个桥文件常量必须都走 _p()")


if __name__ == "__main__":
    unittest.main()
