# -*- coding: utf-8 -*-
# 盒子侧两份 PowerShell 交付物（2026-11-23 §BOX-FIX-20261123 第 4/6/7 条）的静态用例。
#
# 为什么要有这一份：`archive_exports.ps1` 与 `install_tasks.ps1` 的改动落在 Windows 计划任务
# 与文件搬运面上，本机没有 pwsh（`NO_PWSH`），仓里也没有能在 CI 里真跑 schtasks 的环境。
# 门禁 §236 用的是 shell 侧 grep 锁；这里补一层**可执行**的 Python 断言，把"必须成立的口径"
# 钉成用例——任何一次重构把近端窗口、/ET 旧形状、3 分钟节拍或归档护栏改回去，都会在这里红。
#
# 断言的都是脚本里的**判据字面量**（不是风格）：硬编码根、$keepFrom 的算法、$KeepDays 缺省、
# WhatIf 的两处守卫、T1/T8/T9 的触发器形状、Set-TaskPosture 的 StartWhenAvailable 与 BootTrigger。
# 真机执行（-WhatIf 清单 / -Apply 重注册）在盒上做，读数记在 docs/PLAN_BOX_FIX_20261123.md §11。

import io
import os
import re
import unittest

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO = os.path.dirname(os.path.dirname(_HERE))


def _read(rel):
    """读仓库里的 UTF-8 文本（两个 ps1 都带 BOM，按 utf-8-sig 剥掉）。"""
    with io.open(os.path.join(_REPO, rel), encoding="utf-8-sig") as fh:
        return fh.read()


def _job_block(src, var):
    """取出 `@{ Name = $T8; ... }` 这一段（跨行，到第一个 `}` 为止）。

    只用于静态断言"这一条任务没写 /ET、必须是 DAILY + /RI + /DU"这类形状问题；
    不解析 PowerShell 语法（那要 pwsh，本机没有）。
    """
    # 收尾锚是"行首只有 }"的那一行（`"{0}"` 里的 `}` 不是任务块的结尾）。
    m = re.search(r"@\{\s*Name\s*=\s*\$" + var + r";.*?\n\s*\}", src, re.S)
    if not m:
        raise AssertionError("找不到 %s 的任务定义块" % var)
    return m.group(0)


class ArchiveExportsScriptTests(unittest.TestCase):
    """§EXPORT-ARCHIVE：只搬旧导出、近端窗口（今天+昨天）一个都不碰。"""

    @classmethod
    def setUpClass(cls):
        cls.src = _read("deploy/qmt-win/archive_exports.ps1")

    def test_root_is_hardcoded_and_mismatch_refuses(self):
        # 护栏一：参数可以传，但与硬编码根不逐字相等就拒跑（防止被改成 C:\ 之类）。
        self.assertIn('$FixedRoot = "D:\\qmt"', self.src)
        self.assertIn('$rootFull -ne $FixedRoot', self.src)
        self.assertIn("ARCHIVE_RESULT=ABORT reason=root_mismatch", self.src)

    def test_archive_root_outside_the_box_tree(self):
        # 护栏二：归档根在 D:\qmt 内 ⇒ 下一轮发现层还会遍历它。
        self.assertIn("$archFull.StartsWith($FixedRoot + '\\')", self.src)
        self.assertIn("ARCHIVE_RESULT=ABORT reason=archive_inside_root", self.src)

    def test_keep_active_window_is_today_plus_yesterday(self):
        # 今天与昨天留在原地：上行腿的候选窗口是 uplink_export_max_age_days=2。
        self.assertIn("[int]$KeepActiveDays = 1", self.src)
        self.assertIn("$keepActive = [math]::Max(1, $KeepActiveDays)", self.src)
        self.assertIn("$keepFrom = $today.AddDays(-1 * $keepActive)", self.src)
        # 判定必须对 $keepFrom，不许写成对 $today（那会把昨天的导出也搬走）。
        self.assertIn("if ($day -ge $keepFrom)", self.src)
        self.assertNotIn("if ($day -ge $today)", self.src)

    def test_undated_file_is_left_alone(self):
        # 解析不出日期 ⇒ fail-closed 留在原地（不拿"证明不了它旧"当"它旧"）。
        self.assertIn("$skippedUndated++", self.src)
        self.assertIn("skipped_undated={6}", self.src)

    def test_keep_days_default_is_three(self):
        self.assertIn("[int]$KeepDays = 3", self.src)
        self.assertIn("if ($ageDays -le $KeepDays) { continue }", self.src)

    def test_never_moves_ledgers_or_python(self):
        # 移动面只有导出 CSV：账本/配置/策略/库一个都不许出现在脚本里。
        for banned in ("data.db", "uplink_state.json", "local_executor_params", ".py"):
            self.assertNotIn(banned, self.src, "archive_exports.ps1 不该持有 %r" % banned)

    def test_whatif_guards_both_move_and_purge(self):
        # -WhatIf 零写入：两处写动作都必须排在 WhatIf 分支之后。
        move = self.src.index("Move-Item -LiteralPath $f.FullName")
        purge = self.src.index("Remove-Item -LiteralPath $d.FullName")
        whatif_marks = [m.start() for m in re.finditer(r"(?m)^[ \t]*if \(\$WhatIf\)", self.src)]
        self.assertEqual(len(whatif_marks), 2, "WhatIf 必须同时守住搬移与清理两段")
        self.assertLess(whatif_marks[0], move)
        self.assertLess(whatif_marks[1], purge)


class InstallTasksShapeTests(unittest.TestCase):
    """§UPLINK-CADENCE / §CLIENT-REVIVE-BOOT：任务触发器与设置补丁的形状。"""

    @classmethod
    def setUpClass(cls):
        cls.src = _read("deploy/qmt-friend/install_tasks.ps1")

    def test_client_ensure_runs_every_three_minutes(self):
        # owner 2026-11-23 裁决：10 → 3 分钟（每轮只是 Get-Process 一次）。
        block = _job_block(self.src, "T1")
        self.assertIn('Sc = "MINUTE"; Mn = 3; St = "08:25"', block)

    def test_client_ensure_has_boot_trigger(self):
        # 11-23 09:55 开机后没人拉起客户端：开机 90 秒补一跳，且只给保活腿。
        self.assertIn('$boot.Delay = "PT90S"', self.src)
        self.assertIn("@{ N = $T1; Boot = $true }", self.src)
        others = re.findall(r"@\{ N = \$T(\d+); Boot = \$true \}", self.src)
        self.assertEqual(others, ["1"], "BootTrigger 只给 T1（保活腿）")

    def test_uplink_is_daily_repetition_not_end_boundary(self):
        # /ET 会把 EndBoundary 钉在注册当天（T7 于 11-18 踩过），T8 必须 DAILY + /RI + /DU。
        block = _job_block(self.src, "T8")
        self.assertIn('Sc = "DAILY"', block)
        self.assertIn("Ri = 1; Du = \"06:50\"", block)
        self.assertNotIn("Et =", block)
        # 旧形状（MINUTE 每分钟 + /ET）不许回潮。
        self.assertNotIn('@{ Name = $T8; Sc = "MINUTE"', self.src)

    def test_export_archive_task_at_0710(self):
        # 07:10 早于 08:35 pregen、晚于收盘导出全部落盘。
        block = _job_block(self.src, "T9")
        self.assertIn('Sc = "DAILY"; St = "07:10"', block)
        self.assertIn("$ArchiveScript", block)
        self.assertIn('$T9 = "QMTF-Export-Archive"', self.src)

    def test_every_task_gets_start_when_available_and_ignore_new(self):
        # 重启窗口里错过的一跳必须补跑；重叠轮次仍按 IgnoreNew（互斥另有 .uplink.lock）。
        self.assertIn("$settings.StartWhenAvailable = $true", self.src)
        self.assertIn('$settings.MultipleInstances = "IgnoreNew"', self.src)
        self.assertIn("Set-ScheduledTask -TaskName $Name -Settings $settings -Trigger $triggers", self.src)
        # 九条任务全部进姿态补丁表（少一条＝那条的 StartWhenAvailable 还是 false）。
        posture_names = re.findall(r"@\{ N = \$T(\d+); Boot = \$", self.src)
        self.assertEqual(sorted(posture_names), ["1", "2", "3", "4", "5", "6", "7", "8", "9"])


if __name__ == "__main__":
    unittest.main()
