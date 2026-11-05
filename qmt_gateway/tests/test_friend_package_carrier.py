#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-CARRIER 发版前自证：一键包必须真带全量规则 + 沙箱策略 py。

与 test_friend_package.py（§171 批 6，锁"包本身不自欺"）分工：那份锁清单/指纹/deny/
入口 ASCII，且**用例数被 §171 钉死为 8**，所以本批新增的判据另起一份文件，不碰它。

钉住的事：
  ① 白名单（唯一真源＝构建脚本 -Preview 输出，不在测试里抄第二份）必须含五个 rules_*.py、
     参数真源、两份模板、构建器、派生器、载体更新器、回报腿、更新脚本、看门狗；
  ② 包里同时有**模板**（空规则块，供包内复现构建）与**构建期烘好的产物**（含全量规则、
     v02 烘参）；只带模板就是"清单全绿但装上一份没有规则的空壳"；
  ③ MANIFEST 逐文件 SHA256 覆盖 artifacts，且反向覆盖包内每个文件；
  ④ 沙箱策略产物走 passorder、纯 ASCII、AST import 根集合不含任何文件/网络模块；
  ⑤ --check 只读校验：原包绿、篡改一字节红；-Preview 是 dry-run、不往 --out 落盘；
  ⑥ 解压后在**包内**重跑构建器，两份产物逐字节 + SHA256 复现（全在临时目录，不碰仓库源码）；
  ⑦ install.ps1 静态锚点（STEP4B / ABORT / -Apply / BOM）、三个 .bat 纯 ASCII 走 -File、
     SETUP.txt 保留三条 11-04 更正与"未分发"且写清全量规则 + 沙箱策略 + 一键解压。

本机 NO_PWSH：PowerShell 只做静态文本自证，不真跑安装。
"""
import ast
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
import zipfile

sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
BUILDER = os.path.join(REPO, "scripts", "build_friend_package.sh")
FRIEND_DIR = os.path.join(REPO, "deploy", "qmt-friend")
_WORK = tempfile.mkdtemp(prefix="fpc-work-")
_PKG_OUT = os.path.join(_WORK, "out")
_ZIP = None
_BUILD_OUT = ""

RULE_MODULES = ("rules_form", "rules_ladder", "rules_factor", "rules_t", "rules_sizing")
SANDBOX_IMPORT_ALLOW = {"datetime", "hashlib", "time"}


def _env(extra=None):
    e = {
        "TMPDIR": _WORK,
        "PATH": os.environ.get("PATH", ""),
        "HOME": os.environ.get("HOME", ""),
        "PYTHONDONTWRITEBYTECODE": "1",
    }
    if extra:
        e.update(extra)
    return e


def _run(args, env):
    p = subprocess.run([BUILDER] + args, cwd=REPO, env=env,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return p.returncode, p.stdout.decode("utf-8", "replace")


def _planned_files():
    """白名单唯一真源＝构建脚本 -Preview 输出；这里不重抄一份。"""
    rc, out = _run(["-Preview"], _env())
    assert rc == 0, "预览构建失败：\n" + out
    rels = []
    for line in out.splitlines():
        m = re.match(r"^   (\S.+?)\s*$", line)
        if m and "/" in m.group(1) and not m.group(1).startswith("（"):
            rels.append(m.group(1))
    assert len(rels) >= 20, "预览只列出 %d 个文件，白名单不可能这么少" % len(rels)
    return rels


def _artifact_bytes():
    with zipfile.ZipFile(_ZIP) as z:
        return {os.path.basename(n): z.read(n)
                for n in z.namelist()
                if n.startswith("payload/artifacts/") and n.endswith(".py")}


class TestFriendPackageCarrier(unittest.TestCase):

    @classmethod
    def setUpClass(cls):
        global _ZIP, _BUILD_OUT
        os.makedirs(_PKG_OUT, exist_ok=True)
        rc, out = _run(["--commit", "carrierchk"], _env({"PKG_OUT": _PKG_OUT}))
        if rc != 0:
            raise AssertionError("整包构建失败：\n" + out)
        m = re.search(r"^PKG=(.+)$", out, re.M)
        if not m:
            raise AssertionError("没打出 PKG= 那一行：\n" + out)
        _BUILD_OUT = out
        _ZIP = m.group(1).strip()
        if not os.path.isfile(_ZIP):
            raise AssertionError("PKG= 指向的文件不存在：" + _ZIP)

    def test_01_whitelist_has_full_rule_and_carrier_set(self):
        """owner 清单逐项在 -Preview 里点名；缺一件就不算"全量规则 + 沙箱策略 py"。"""
        rels = _planned_files()
        want = ["qmt_gateway/%s.py" % n for n in RULE_MODULES] + [
            "qmt_gateway/local_executor_params.json",
            "qmt_gateway/qmt_internal_strategy.py",
            "qmt_gateway/local_executor.py",
            "qmt_gateway/carrier_updater.py",
            "qmt_gateway/report_uplink.py",
            "scripts/build_local_executor.py",
            "scripts/derive_carrier_params.py",
            "scripts/update_local_executor.ps1",
            "scripts/update_qmt_strategy.ps1",
            "scripts/update_local_executor.bat",
            "deploy/qmt-friend/watchdog.ps1",
            "deploy/qmt-friend/install_local_executor.ps1",
        ]
        for w in want:
            self.assertIn(w, rels, "白名单漏了：" + w)

    def test_02_build_reports_repro_and_two_artifacts(self):
        """构建期两条读数都得在：产物=2、解压后包内重跑逐字节复现=1。"""
        self.assertIn("ARTIFACTS=2", _BUILD_OUT)
        self.assertIn("REPRO_BYTE_SAME", _BUILD_OUT)
        self.assertIn("REPRO_OK files=2 bytesame=1", _BUILD_OUT)
        self.assertIn("REPRO_OK=1", _BUILD_OUT)
        self.assertIn("BUILD_EXIT=0", _BUILD_OUT)

    def test_03_zip_has_both_templates_and_both_artifacts(self):
        """模板（空规则块）与产物（全量规则）各归各位；产物顶替模板会让复现腿变红。"""
        with zipfile.ZipFile(_ZIP) as z:
            names = z.namelist()
            arts = [n for n in names if n.startswith("payload/artifacts/") and n.endswith(".py")]
            self.assertEqual(len(arts), 2, "包内产物不是两份：" + repr(arts))
            for n in ("payload/qmt_gateway/qmt_internal_strategy.py",
                      "payload/qmt_gateway/local_executor.py",
                      "payload/tools/build_local_executor.py",
                      "payload/tools/derive_carrier_params.py",
                      "payload/qmt_gateway/carrier_updater.py",
                      "payload/MANIFEST.json"):
                self.assertIn(n, names, "包内缺文件：" + n)
            tpl = z.read("payload/qmt_gateway/qmt_internal_strategy.py").decode("utf-8")
            self.assertIn('STRATEGY_VERSION = "v00-00000000"', tpl)
            self.assertIn("_PORTED_INLINE = {}", tpl)

    def test_04_manifest_covers_artifacts_with_sha256(self):
        """MANIFEST 逐文件 SHA256 与 zip 字节一致，且反向覆盖包内每个文件。"""
        with zipfile.ZipFile(_ZIP) as z:
            manifest = json.loads(z.read("payload/MANIFEST.json").decode("utf-8"))
            paths = [e["path"] for e in manifest["files"]]
            for e in manifest["files"]:
                data = z.read("payload/" + e["path"])
                self.assertEqual(hashlib.sha256(data).hexdigest(), e["sha256"],
                                 "SHA256 对不上：" + e["path"])
                self.assertEqual(len(data), e["bytes"], "字节数对不上：" + e["path"])
            arts = [p for p in paths if p.startswith("artifacts/") and p.endswith(".py")]
            self.assertEqual(len(arts), 2, "清单里产物不是两份：" + repr(arts))
            for n in z.namelist():
                if n.endswith("/") or n.endswith("MANIFEST.json"):
                    continue
                self.assertIn(n.replace("payload/", "", 1), paths,
                              "包里混进未登记文件：" + n)

    def test_05_artifacts_carry_all_five_rule_modules(self):
        """两份产物都把五个规则模块逐字内联、版本烘成 v02（不是 v00 空模板）。"""
        arts = _artifact_bytes()
        self.assertEqual(len(arts), 2)
        for name, raw in arts.items():
            text = raw.decode("ascii")  # 非 ASCII 会直接抛，顺带验纯 ASCII
            self.assertGreater(len(text), 300000,
                               "产物只有 %d 字节＝模板没被烘成全量规则：" % len(text) + name)
            for mod in RULE_MODULES:
                self.assertIn(mod, text, "%s 里缺规则模块 %s" % (name, mod))
            self.assertIn("_PORTED_INLINE = {", text, name + " 没有内联注册表")
            self.assertRegex(text, r'STRATEGY_VERSION = "v\d\d-\d{8}"')
            self.assertNotIn('STRATEGY_VERSION = "v00-00000000"', text)

    def test_06_sandbox_artifact_is_pure_ascii_and_no_io_network(self):
        """沙箱策略走 builtins 的 passorder，AST import 集合里不许有文件/网络模块。

        字符串匹配会把注释里"no socket / no requests"的说明误判成真 import，所以走 AST。
        """
        arts = _artifact_bytes()
        cand = [n for n in arts if n.startswith("qmt_internal_strategy_")]
        self.assertEqual(len(cand), 1, "沙箱产物不是一份：" + repr(cand))
        raw = arts[cand[0]]
        self.assertEqual([b for b in raw if b > 127], [], "沙箱产物含非 ASCII 字节")
        text = raw.decode("ascii")
        self.assertIn("passorder", text, "沙箱策略没走 passorder")
        roots = set()
        for node in ast.walk(ast.parse(text)):
            if isinstance(node, ast.Import):
                for alias in node.names:
                    roots.add(alias.name.split(".")[0])
            elif isinstance(node, ast.ImportFrom) and node.module:
                roots.add(node.module.split(".")[0])
        self.assertTrue(roots, "沙箱产物一个 import 都没有＝不像能跑")
        self.assertTrue(roots.issubset(SANDBOX_IMPORT_ALLOW),
                        "沙箱产物出现文件/网络模块：%s" % sorted(roots - SANDBOX_IMPORT_ALLOW))

    def test_07_check_ok_on_pristine_package(self):
        """--check 只读、不落盘：原包打 CHECK_OK 且报 artifacts=2。"""
        rc, out = _run(["--check", _ZIP], _env())
        self.assertEqual(rc, 0, out)
        self.assertIn("CHECK_OK", out)
        self.assertIn("artifacts=2", out)

    def test_08_check_fails_on_tampered_package(self):
        """改一个字节 ⇒ CHECK_FAIL 且退出码 1（清单逐文件核对不是摆设）。"""
        tampered = os.path.join(_WORK, "tampered.zip")
        with zipfile.ZipFile(_ZIP) as src, zipfile.ZipFile(tampered, "w") as dst:
            for info in src.infolist():
                data = src.read(info.filename)
                if info.filename == "payload/qmt_gateway/rules_form.py":
                    data += b"\n"
                dst.writestr(info, data)
        rc, out = _run(["--check", tampered], _env())
        self.assertEqual(rc, 1, "篡改一个字节居然还绿：\n" + out)
        self.assertIn("CHECK_FAIL", out)
        self.assertIn("sha_mismatch", out)

    def test_09_preview_is_dry_run(self):
        """-Preview 不往 --out 落任何东西，只列计划。"""
        prev = tempfile.mkdtemp(prefix="fpc-prev-", dir=_WORK)
        rc, out = _run(["-Preview", "--out", prev], _env())
        self.assertEqual(rc, 0, out)
        self.assertEqual(os.listdir(prev), [], "-Preview 往 --out 落盘了")
        self.assertIn("ARTIFACTS_PLAN=2", out)
        self.assertIn("BUILD_EXIT=0", out)

    def test_10_unzip_rerun_builder_reproduces_byte_identical(self):
        """解压后在**包内**重跑构建器，两份产物逐字节 + SHA256 复现。

        独立复现一遍构建脚本内建的那条自证：全程在临时目录，不读也不写仓库源码。
        """
        d = tempfile.mkdtemp(prefix="fpc-repro-", dir=_WORK)
        with zipfile.ZipFile(_ZIP) as z:
            z.extractall(d)
        out_dir = os.path.join(d, "out")
        os.makedirs(out_dir)
        builder = os.path.join(d, "payload", "tools", "build_local_executor.py")
        for target in ("executor", "sandbox"):
            p = subprocess.run(
                [sys.executable, builder, "--out-dir", out_dir, "--target", target],
                cwd=d, env=_env(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            text = p.stdout.decode("utf-8", "replace")
            self.assertEqual(p.returncode, 0, "包内重跑 %s 失败：\n%s" % (target, text))
            self.assertIn("BUILD_RESULT=OK", text)
        arts = _artifact_bytes()
        for name, raw in arts.items():
            rebuilt = os.path.join(out_dir, name)
            self.assertTrue(os.path.isfile(rebuilt), "重跑没产出：" + name)
            with open(rebuilt, "rb") as f:
                again = f.read()
            self.assertEqual(raw, again, "逐字节不复现：" + name)
            self.assertEqual(hashlib.sha256(raw).hexdigest(),
                             hashlib.sha256(again).hexdigest(), "SHA 不复现：" + name)

    def test_11_install_ps1_static_anchors(self):
        """NO_PWSH：安装器只做静态文本自证（STEP4B / ABORT / -Apply / BOM）。"""
        with open(os.path.join(FRIEND_DIR, "install.ps1"), "rb") as fh:
            raw = fh.read()
        self.assertEqual(raw[:3], b"\xef\xbb\xbf", "install.ps1 丢了 UTF-8 BOM")
        text = raw.decode("utf-8-sig")
        for k in ("STEP1_MANIFEST", "STEP4_LAYOUT", "STEP4B_ARTIFACTS",
                  "STEP5_BRIDGE", "LEG_INSTALL=ABORT", "LEG_INSTALL=PASS",
                  "[switch]$Apply", "artifact_copy_sha_mismatch",
                  "qmt_internal_strategy.py", "Get-FileHash", "SecureString"):
            self.assertIn(k, text, "install.ps1 缺锚点：" + k)
        code = "\n".join(l for l in text.splitlines() if not l.lstrip().startswith("#"))
        self.assertNotIn('token = "', code, "钥匙字面量进了安装器")
        self.assertNotIn("preserved_config", code, "退回恒 0 的假锁")

    def test_12_bat_entries_ascii_and_setup_txt_states(self):
        """三个 .bat 纯 ASCII 走 -File；SETUP.txt 保留三条更正与"未分发"。"""
        for name in ("install_entry.bat", "uninstall_entry.bat", "export_ledger.bat"):
            with open(os.path.join(FRIEND_DIR, name), "rb") as fh:
                raw = fh.read()
            self.assertTrue(raw.startswith(b"@echo off\r\n"), name + " 不是 CRLF 打头")
            self.assertEqual([b for b in raw if b > 127], [], name + " 含非 ASCII 字节")
            body = b"\n".join(l for l in raw.splitlines()
                              if not l.lstrip().upper().startswith(b"REM"))
            self.assertIn(b"-ExecutionPolicy Bypass -File", body, name + " 没走 -File")
            self.assertNotIn(b"-Command", body, name + " 用了 -Command")
        with io.open(os.path.join(FRIEND_DIR, "SETUP.txt"), encoding="utf-8-sig") as fh:
            setup = fh.read()
        for k in ("全量量化交易规则", "沙箱策略 py", "一键解压", "未分发",
                  "2026-11-04 的三条更正", "不要发给朋友、不要装",
                  "derive_carrier_params.py", "--check", "ABORT", "artifacts"):
            self.assertIn(k, setup, "SETUP.txt 缺口径：" + k)


def tearDownModule():
    """所有产物只落在这棵临时树里，跑完即删，仓库里不留残留。"""
    shutil.rmtree(_WORK, ignore_errors=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
