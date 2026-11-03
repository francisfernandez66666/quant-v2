#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-PKG 批 6 回归锁：朋友机器一键解压安装包的**构建腿**行为。

钉住五件事（全是"歪了不会喊、只会发出一份看起来正常的包"那一类）：
  ① 清单必须描述真正压进包的那棵树：zip 里除 MANIFEST.json 外每个文件都要在清单里、
     SHA256 逐字节对得上；`__pycache__` 之类本机垃圾一旦混进去，朋友侧的逐文件校验必然假红
     （11-03 首跑就是这么抓到的——import gateway 落了 .pyc 又被自己列进清单）；
  ② 契约指纹不许在构建侧再造一把尺子：清单里那个值必须等于**网关自己**
     `contract_fingerprint()` 对包内 contract 目录算出来的值（两把尺子必然漂，
     漂开的现场形态是"装完自检说契约不符"，而代码其实是对的）；
  ③ 白名单里缺文件 ⇒ 当场拒绝构建，不发"少一个文件"的包；
  ④ 白名单里出现禁止路径（真配置 / 账本 / 凭据）⇒ **拒绝**而不是跳过：
     跳过会静默发出一份带凭证的包，而没人知道少算了什么（N2 与 B-N2 的结构性边界）；
  ⑤ `--wireguard` 指向的文件不在 ⇒ 拒绝，不"那就先不带"：静默跳过会发出一份
     "以为带了 overlay"的包，朋友装完才发现第一根腿起不来。
另外锁住三个 .bat 入口是纯 ASCII 且走 `-File`（cmd 按 OEM 码页解 .bat，中文会变 `?`；
`-Command` 会把参数面留在进程列表里）。

构建清单的唯一真源是 build_friend_package.sh 自己：本测试用它的 `-Preview` 输出反推要准备哪些文件，
不在测试里再抄一份白名单——抄一份就是第二次事实源，白名单一改这里就假绿。
"""
import hashlib
import io
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
import zipfile

# 测试进程里 import gateway 不许往仓库落 __pycache__（构建脚本侧同样一条纪律，两处都写：
# 谁哪天把这条摘掉，包清单里就会长出一堆本机字节码，朋友侧校验集体假红）。
sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
# 整个测试只往这一棵临时树里写（TMPDIR 缝 + PKG_OUT 都指它），跑完即删。
_WORK = tempfile.mkdtemp(prefix="fp-work-")
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
# PKG_BUILDER 是给门禁反证留的缝：把一份"摘掉 deny 判据"的构建脚本指进来，
# 第 05 条必须转红——否则那条负锁今天到底有没有牙，没人知道（§130 同法）。
BUILDER = os.environ.get("PKG_BUILDER") or os.path.join(REPO, "scripts", "build_friend_package.sh")


def _run(args, env=None):
    """跑构建脚本，返回 (rc, 合并输出)。

    缺省**不带 PKG_OUT**（只继承 TMPDIR）——带上进程环境里那个 PKG_OUT 会让预览与构建落到
    不同目录、产物数对不上，测试就红在自己的夹具上而不是红在代码上（11-03 首跑撞到）。
    """
    e = {"TMPDIR": _WORK, "PATH": os.environ.get("PATH", ""), "HOME": os.environ.get("HOME", "")}
    if env:
        e.update(env)
    p = subprocess.run([BUILDER] + args, cwd=REPO, env=e,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return p.returncode, p.stdout.decode("utf-8", "replace")


def _planned_files():
    """从 -Preview 的输出里取白名单相对路径（清单的唯一真源在构建脚本，这里不重抄一遍）。"""
    rc, out = _run(["-Preview", "--out", os.path.join(_WORK, "preview-unused")])
    assert rc == 0, "预览构建失败：\n" + out
    rels = []
    for line in out.splitlines():
        m = re.match(r"^   (\S.+?)\s*$", line)
        if m and "/" in m.group(1) and not m.group(1).startswith("（"):
            rels.append(m.group(1))
    assert len(rels) >= 20, "预览只列出 %d 个文件，白名单不可能这么少" % len(rels)
    return rels


_FAKE_DIRS = []


def _fake_tree(with_files=True, extra=None):
    """按预览清单在临时目录里摆一棵最小源树（软链到真文件，不复制内容）。"""
    d = tempfile.mkdtemp(prefix="fp-src-")
    if with_files:
        for rel in _planned_files():
            src = os.path.join(REPO, rel)
            dst = os.path.join(d, rel)
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            os.symlink(src, dst)
    for name in (extra or []):
        p = os.path.join(d, name)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with io.open(p, "w", encoding="utf-8") as f:
            f.write("planted\n")
    _FAKE_DIRS.append(d)
    return d


class TestFriendPackage(unittest.TestCase):

    @classmethod
    def setUpClass(cls):
        cls.tmp = os.path.join(_WORK, "out")
        os.makedirs(cls.tmp, exist_ok=True)

    @classmethod

    def _build(self, extra_args=None, env=None):
        e = {"PKG_OUT": self.tmp}
        if env:
            e.update(env)
        rc, out = _run(["--commit", "abcdef1"] + (extra_args or []), env=e)
        return rc, out

    def test_01_build_writes_zip_and_manifest_hashes_match(self):
        """整包构建：每个清单文件的 SHA256 必须与 zip 里那份字节一致。"""
        rc, out = self._build()
        self.assertEqual(rc, 0, "构建失败：\n" + out)
        self.assertIn("BUILD_EXIT=0", out)
        m = re.search(r"^PKG=(.+)$", out, re.M)
        self.assertTrue(m, "没打出 PKG= 那一行：%s" % out)
        zpath = m.group(1).strip()
        self.assertTrue(os.path.isfile(zpath))
        with zipfile.ZipFile(zpath) as z:
            manifest = __import__("json").loads(z.read("payload/MANIFEST.json").decode("utf-8"))
            names = set(n for n in z.namelist() if not n.endswith("/"))
            listed = 0
            for ent in manifest["files"]:
                listed += 1
                arc = "payload/" + ent["path"]
                self.assertIn(arc, names, "清单里有但包里没有：" + ent["path"])
                self.assertEqual(hashlib.sha256(z.read(arc)).hexdigest(), ent["sha256"],
                                 "SHA256 对不上：" + ent["path"])
            # 反向：包里的每个文件（除清单自身）都必须被清单覆盖
            for n in names:
                if n.endswith("MANIFEST.json"):
                    continue
                self.assertIn(n.replace("payload/", "", 1),
                              [e["path"] for e in manifest["files"]],
                              "包里混进未登记的文件：" + n)
            self.assertGreaterEqual(listed, 20)

    def test_02_no_bytecode_junk_in_package(self):
        """__pycache__ / .pyc 不许进包（本机垃圾进包＝朋友侧校验假红的第一个来源）。"""
        rc, out = self._build()
        zpath = re.search(r"^PKG=(.+)$", out, re.M).group(1).strip()
        with zipfile.ZipFile(zpath) as z:
            junk = [n for n in z.namelist() if "__pycache__" in n or n.endswith(".pyc")]
        self.assertEqual(junk, [], "包里有字节码垃圾：%s" % junk)

    def test_03_contract_fp_uses_gateway_own_ruler(self):
        """清单里的契约指纹必须等于网关自己对包内 contract 目录算的那一把尺子。"""
        rc, out = self._build()
        zpath = re.search(r"^PKG=(.+)$", out, re.M).group(1).strip()
        manifest_fp = None
        with zipfile.ZipFile(zpath) as z:
            manifest_fp = __import__("json").loads(z.read("payload/MANIFEST.json").decode("utf-8"))["contract_fp"]
            extract = tempfile.mkdtemp(prefix="fp-x-")
            for n in z.namelist():
                if n.endswith("/"):
                    continue  # zip 的目录条目（basename 为空，写过去就是 IsADirectoryError）
                if n.startswith("payload/qmt_gateway/contract/"):
                    data = z.read(n)
                    dst = os.path.join(extract, os.path.basename(n))
                    with open(dst, "wb") as f:
                        f.write(data)
        sys.path.insert(0, GW_DIR)
        import gateway  # noqa: E402  借它的 contract_fingerprint，不另写哈希逻辑
        try:
            again = gateway.contract_fingerprint(extract)
        finally:
            shutil.rmtree(extract, ignore_errors=True)
        self.assertEqual(again, manifest_fp,
                         "构建侧与网关侧两把尺子对不上（装完必然报契约不符）")
        self.assertNotEqual(again, "unreadable")

    def test_04_missing_whitelist_file_refuses_build(self):
        rc, out = self._build(env={
            "PKG_SRC_ROOT": _fake_tree(with_files=False)})
        self.assertEqual(rc, 1)
        self.assertIn("reason=missing_files", out)

    def test_05_banned_path_in_whitelist_refuses_not_skips(self):
        """账本/真配置这类路径出现在包里 ⇒ 拒绝构建（跳过＝静默发出带凭证的包）。"""
        src = _fake_tree(extra=[os.path.join("qmt_gateway", "contract", "data.db")])
        rc, out = self._build(env={"PKG_SRC_ROOT": src})
        self.assertEqual(rc, 1, "混进 data.db 照样发出去了：\n" + out)
        self.assertIn("reason=deny_list_hit", out)
        self.assertNotIn("BUILD_EXIT=0", out)

    def test_06_wireguard_requested_but_absent_refuses(self):
        src = _fake_tree()
        rc, out = self._build(["--wireguard", "/nonexistent/wireguard-amd64.msi"],
                              env={"PKG_SRC_ROOT": src})
        self.assertEqual(rc, 1)
        self.assertIn("--wireguard", out)
        self.assertNotIn("BUILD_EXIT=0", out)

    def test_07_wireguard_embedded_shows_up_in_manifest(self):
        # 嵌了 msi 却漏进清单，朋友侧的逐文件校验会"少一个文件都没人报"——
        # 包体与清单同源这条纪律，在 overlay 这种"可选件"上最容易破。
        src = _fake_tree()
        fake_msi = os.path.join(self.tmp, "wireguard-amd64.msi")
        with open(fake_msi, "wb") as f:
            f.write(b"MSI-PLACEHOLDER-BYTES")
        rc, out = self._build(["--wireguard", fake_msi], env={"PKG_SRC_ROOT": src})
        self.assertEqual(rc, 0, out)
        self.assertIn("WIREGUARD=embedded", out)
        zpath = re.search(r"^PKG=(.+)$", out, re.M).group(1).strip()
        with zipfile.ZipFile(zpath) as z:
            names = z.namelist()
            self.assertIn("payload/overlay/wireguard-amd64.msi", names)
            manifest = __import__("json").loads(z.read("payload/MANIFEST.json").decode("utf-8"))
            self.assertEqual(manifest["wireguard"], "embedded")
            paths = [e["path"] for e in manifest["files"]]
            self.assertIn("overlay/wireguard-amd64.msi", paths,
                          "msi 进了包却没进清单＝朋友侧校验漏一个文件，永远发现不了")

    def test_08_bat_entries_are_ascii_and_use_file_not_command(self):
        """cmd 按 OEM 码页解 .bat（中文变 `?`）；-Command 会把参数面留在进程列表里。"""
        for name in ("install_entry.bat", "uninstall_entry.bat", "export_ledger.bat"):
            p = os.path.join(REPO, "deploy", "qmt-friend", name)
            self.assertTrue(os.path.isfile(p), name + " 不在位")
            with open(p, "rb") as fh:
                raw = fh.read()
            self.assertTrue(raw.startswith(b"@echo off\r\n"), name + " 必须是 CRLF 且 @echo off 打头")
            bad = [b for b in raw if b > 127]
            self.assertEqual(bad, [], name + " 含非 ASCII 字节 %r（.bat 会被码页换成 ?）" % bad[:8])
            # 负锁只看**可执行行**：REM 注释里本来就该出现"-File (not -Command)"这句解释，
            # 按整文件判会把自家说明当成下雷（本仓 §164 那条负锁同形事故）。
            body = b"\n".join(l for l in raw.splitlines() if not l.lstrip().upper().startswith(b"REM"))
            self.assertIn(b"-ExecutionPolicy Bypass -File", body, name + " 没走 -File")
            self.assertNotIn(b"-Command", body, name + " 用了 -Command（参数面会进进程列表）")


def tearDownModule():
    """全部产物都只落在 _WORK 这一棵临时树里（含构建脚本的缺省产物目录），跑完即删。"""
    shutil.rmtree(_WORK, ignore_errors=True)
    for d in _FAKE_DIRS:
        shutil.rmtree(d, ignore_errors=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
