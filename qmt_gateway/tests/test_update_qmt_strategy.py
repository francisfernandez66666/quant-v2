# test_update_qmt_strategy.py — 内置策略投递腿（scripts/update_qmt_strategy.ps1）的行为锁（2026-11-04）。
#
# 载体定了之后，这条腿是"每日收盘后规则自动更新一次"唯一可能的形态：
# 内置策略自己不能读文件、不能联网 ⇒ 它无法更新自己、也无法回报；
# 能读能写能联网的是同机那个普通进程，投递就是它的活。
#
# 为什么必须用真 pwsh 跑（本机 /tmp/pwsh7/pwsh 在位）：
# 这条腿今天之前全仓只有静态判据，而 11-03 那次"用 pwsh 真跑一遍 -Apply"当场抓出四个
# 只有跑起来才现形的错（非法强转、漏一层目录、判据写在分支外、ABORT 后自己抛绑定错误）。
# 所以这里的每条断言都是**跑完看盘上的后态**，不是读脚本文字。
#
# 锁清单：
#   1. 缺省只预览：一个字节都不动，且打出 plan 与重启顺序锚；
#   2. -Apply 走完三段并落码：备份在位、目标字节与源一致、版本回读锚 OK；
#   3. 版本对不上 ⇒ ABORT + 还原旧文件 + 立 STOP_AUTO_UPDATE 牌（R6 那枚命门）；
#   4. 牌在位时下一次 -Apply 直接拒（不许在不明状态上继续投）；
#   5. 非 ASCII 源（GBK 终端会把策略撕碎）⇒ 进门就拒，不落半个字节；
#   6. 脚本体内**不许出现直起 QMT 客户端**的命令（10-29：会话内直起随会话被杀），
#      唯一启动路径是计划任务 schtasks /Run。
#
# English: behaviour tests for the strategy delivery leg, executed with a real pwsh
# against throwaway directories, including the rollback, the STOP marker and the
# "never launch the client from this session" negative lock.
import os
import shutil
import subprocess
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCRIPT = os.path.join(REPO, "scripts", "update_qmt_strategy.ps1")
ARTIFACT = os.path.join(REPO, "qmt_gateway", "qmt_internal_strategy.py")
PWSH = "/tmp/pwsh7/pwsh" if os.path.exists("/tmp/pwsh7/pwsh") else shutil.which("pwsh")


def _sandbox_artifact():
    """用真构建器产出一份沙箱产物（不把手抄的文件当被测对象）。"""
    out = tempfile.mkdtemp()
    proc = subprocess.run(["python3", os.path.join(REPO, "scripts", "build_local_executor.py"),
                           "--target", "sandbox", "--out-dir", out],
                          capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-500:])
    for name in os.listdir(out):
        if name.startswith("qmt_internal_strategy_"):
            return os.path.join(out, name)
    raise AssertionError("artifact not published: %s" % out)


@unittest.skipUnless(PWSH, "no pwsh on this box -- the delivery leg is then UNVERIFIED")
class UpdateQmtStrategyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.artifact = _sandbox_artifact()
        cls.version = cls._version_of(cls.artifact)

    @staticmethod
    def _version_of(path):
        for line in open(path, encoding="utf-8"):
            if line.startswith("STRATEGY_VERSION"):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
        raise AssertionError("artifact carries no STRATEGY_VERSION")

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.installed = os.path.join(self.dir, "local_strategy.py")

    def run_ps(self, *extra):
        cmd = [PWSH, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", SCRIPT,
               "-Source", self.artifact, "-QmtPythonDir", self.dir] + list(extra)
        proc = subprocess.run(cmd, capture_output=True, text=True, cwd=self.dir)
        return proc.returncode, proc.stdout + proc.stderr

    # ---- 1. 缺省只预览 ----
    def test_default_is_preview_and_changes_nothing(self):
        rc, out = self.run_ps()
        self.assertEqual(rc, 0, out[-400:])
        self.assertIn("STRATEGY_UPDATE_RESULT=DRYRUN", out)
        self.assertIn("expected_version=%s" % self.version, out)
        self.assertFalse(os.path.exists(self.installed), "预览阶段就把策略文件写下去了")
        # 重启顺序必须在预览里就说清楚，值班的人不必猜
        self.assertIn("order=gateway_stop,ensure_stop,client_kill,task_run", out)

    # ---- 2. -Apply 三段自证 ----
    def test_apply_lands_the_file_with_all_three_stages(self):
        rc, out = self.run_ps("-Apply", "-SkipRestart")
        self.assertEqual(rc, 0, out[-500:])
        for anchor in ("STRATEGY_UPDATE_STAGE=BACKUP ok=1 path=absent_first_install",
                       "STRATEGY_UPDATE_STAGE=WRITE ok=1 sha256=",
                       "STRATEGY_UPDATE_STAGE=VERSION ok=1 version=%s" % self.version,
                       "STRATEGY_UPDATE_RESULT=OK"):
            self.assertIn(anchor, out)
        self.assertTrue(os.path.exists(self.installed))
        self.assertEqual(open(self.installed, "rb").read(), open(self.artifact, "rb").read())

    def test_second_apply_backs_up_the_previous_file(self):
        self.run_ps("-Apply", "-SkipRestart")
        rc, out = self.run_ps("-Apply", "-SkipRestart")
        self.assertEqual(rc, 0, out[-500:])
        self.assertIn("STRATEGY_UPDATE_STAGE=BACKUP ok=1 path=", out)
        self.assertNotIn("absent_first_install", out)
        backups = [n for n in os.listdir(self.dir) if ".pre-strategy-update-" in n]
        self.assertEqual(len(backups), 1, "第二次投递应留下一份旧文件：%s" % os.listdir(self.dir))

    # ---- 3+4. 版本回读失败：命门 ----
    def _tamper_to_force_version_mismatch(self):
        """先投一份，再把源里的版本号改掉投第二次 ⇒ 回读必须判红并还原。"""
        self.run_ps("-Apply", "-SkipRestart")
        broken = os.path.join(self.dir, "broken_source.py")
        text = open(self.artifact, encoding="utf-8").read().replace(
            'STRATEGY_VERSION = "%s"' % self.version, 'STRATEGY_VERSION = "v99-19700101"', 1)
        open(broken, "w", encoding="utf-8").write(text)
        cmd = [PWSH, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", SCRIPT,
               "-Source", broken, "-QmtPythonDir", self.dir, "-Apply", "-SkipRestart",
               "-ExpectedVersion", self.version]
        proc = subprocess.run(cmd, capture_output=True, text=True, cwd=self.dir)
        return proc.returncode, proc.stdout + proc.stderr

    def test_failed_readback_aborts_rolls_back_and_marks(self):
        rc, out = self._tamper_to_force_version_mismatch()
        self.assertEqual(rc, 2, "回读失败却当成功退出：%s" % out[-500:])
        self.assertIn("STRATEGY_UPDATE_RESULT=ABORT", out)
        self.assertIn("STRATEGY_STOP_AUTO_UPDATE=1", out)
        self.assertTrue(os.path.exists(os.path.join(self.dir, "STOP_AUTO_UPDATE")),
                        "版本回读失败却没有立牌：下一次自动更新会在不明状态上继续投")
        # 盘上必须还是上一版（旧文件被还原）
        self.assertIn('STRATEGY_VERSION = "%s"' % self.version,
                      open(self.installed, encoding="utf-8").read())

    def test_marker_blocks_the_next_run(self):
        self._tamper_to_force_version_mismatch()
        rc, out = self.run_ps("-Apply", "-SkipRestart")
        self.assertEqual(rc, 2, out[-300:])
        self.assertIn("stop_auto_update_marker_present", out)

    # ---- 5. 非 ASCII 源进门就拒 ----
    def test_non_ascii_source_is_refused_before_any_write(self):
        bad = os.path.join(self.dir, "cjk_source.py")
        text = open(self.artifact, encoding="utf-8").read().replace(
            "def _pick(", u"def _pick(\n    # 中文注释会撕裂 GBK 终端\n    ", 1)
        open(bad, "w", encoding="utf-8").write(text)
        cmd = [PWSH, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", SCRIPT,
               "-Source", bad, "-QmtPythonDir", self.dir, "-Apply", "-SkipRestart"]
        proc = subprocess.run(cmd, capture_output=True, text=True, cwd=self.dir)
        self.assertEqual(proc.returncode, 2, (proc.stdout + proc.stderr)[-300:])
        self.assertIn("artifact_not_pure_ascii", proc.stdout + proc.stderr)
        self.assertFalse(os.path.exists(self.installed), "被拒的源仍然落了盘")

    # ---- 6. 不许在脚本里直起客户端（10-29 教训） ----
    def test_script_never_launches_the_client_directly(self):
        body = open(SCRIPT, encoding="utf-8").read()
        lowered = body.lower()
        # 允许：停客户端进程、走计划任务；禁止：Start-Process/直接调用可执行文件
        for forbidden in ("start-process", "start-mp", "xtitclient.exe", "invoke-expression"):
            self.assertNotIn(forbidden, lowered,
                             "脚本里出现了 %r：会话内直起客户端会随会话被杀（10-29 实录）" % forbidden)
        self.assertIn("schtasks /run", lowered, "唯一合法的启动路径（计划任务）不在脚本里")
        self.assertIn("stop-process", lowered, "客户端没有先停就投新策略＝旧代码还在跑")


if __name__ == "__main__":
    unittest.main()
