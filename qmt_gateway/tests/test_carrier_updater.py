# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_carrier_updater.py — 盒子侧载体更新轮询（carrier_updater.py）的行为锁。

被测的这条腿是什么（11-05 之后唯一还能自动更新的形态）：
  QMT 内置策略不能读文件、不能联网 ⇒ 它既不能更新自己、也不能回报；同机那个普通
  Python 进程可以，于是"拉清单 → 比哈希 → 取字节 → 验哈希 → 交给既有投递脚本 → 回执"
  这一段就是它的活。这一文件锁的是**这一段的判断姿势**，不是投递脚本内部
  （备份 / 三读 SHA / 版本回读 / 停更标记由 scripts/update_qmt_strategy.ps1 自己负责，
  它的行为锁在 test_update_qmt_strategy.py，这里不重抄一遍）。

八条断言（每条都对应模块 docstring 里的一条规则，也都对应本仓出过的真实缺陷形状）：
  1. 清单读不到 / ok!=true / 缺版本或缺哈希 ⇒ 什么都不做，结果点名"unknown/读不到"，
     **绝不折成"已一致"**（AGENTS 读数纪律 + report_uplink 的 missing≠empty + §STARVE 的 J0）。
  2. 清单哈希 == 已安装哈希 ⇒ 不下载、不投递（幂等：同清单跑第二次就是空转）；
     只有运营那格 request_state=pending 时才回一句"本来就是对的"。
  3. 哈希不等 ⇒ 先下到 QMT 目录之外的临时路径，验完哈希才调投递脚本，
     且 argv **逐字**断言（-Apply / -ExpectedVersion / -Source / -QmtPythonDir / -StrategyFile）。
  4. 取回来的字节哈希对不上 ⇒ 投递脚本一次都不调，回执 ok=false（未验字节零写入）。
  5. 目标目录里有 STOP_AUTO_UPDATE 牌 ⇒ 提前拒，原因说清楚（那块牌只有人工能清）。
  6. --dry-run ⇒ 不下载、不投递、不回执、状态文件一个字节都不写。
  7. 回执体字段名与契约**一字不差**（strategy_version / executor_version / ok / reason）。
  8. 钥匙绝不出现：任何打印行、状态文件、tmp 目录里本模块写出的文件逐字节搜不到它。

测试一律注入假 transport / 假 runner：不打真实网络、不真起 PowerShell、不碰 data.db。
本机重建那条兜底腿也照同一道哈希闸验（对不上照样拒）。
"""

import contextlib
import hashlib
import io
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import carrier_updater as cu

FAKE_TOKEN = "FAKE-SECRET-TOKEN-0123456789abcdef"
BASE = "http://brain.invalid"
FIXED_NOW = 1793772000.0
CANDIDATE_NAME = "candidate_%d.py" % int(FIXED_NOW)

OLD_ART = b'STRATEGY_VERSION = "v01-20261101"\nprint("old")\n'
NEW_ART = b'STRATEGY_VERSION = "v02-20261105"\nprint("new")\n'
EVIL_ART = b'STRATEGY_VERSION = "v02-20261105"\nprint("EVIL")\n'

OK_UPDATER_OUT = "STRATEGY_UPDATE_RESULT=OK version=v02-20261105 sha256=ab backup=none\n"


def _sha(raw):
    """清单里那两枚哈希用真哈希算：被测代码比的是字节，不是长相。"""
    return hashlib.sha256(raw).hexdigest()


class _Transport:
    """假传输：按 (method, url) 回预设响应，并把每一次调用记下来供 argv/字段名断言。"""

    def __init__(self, manifest=None, manifest_status=200, artifact=None, artifact_status=200,
                 ack_status=200, raise_on=None):
        self.manifest = manifest
        self.manifest_status = manifest_status
        self.artifact = artifact
        self.artifact_status = artifact_status
        self.ack_status = ack_status
        self.raise_on = raise_on
        self.calls = []

    def __call__(self, method, url, token, body):
        # 三条 URL 各走一支：读清单 / 回执 / 其余一律当"取产物字节"。
        # raise_on 用来造真实失败形态（连接被拒、连接被重置）——异常文本里不许出现钥匙，
        # 而被测代码必须把"取不到"报成"取不到"，不是"没有要更新的"。
        self.calls.append((method, url, token, body))
        if cu.MANIFEST_PATH in url:
            if self.raise_on == "manifest":
                raise OSError("connection refused")
            if self.manifest is None:
                return self.manifest_status or 200, b"<html>not json</html>"
            return self.manifest_status, json.dumps(self.manifest).encode("utf-8")
        if cu.ACK_PATH in url:
            if self.raise_on == "ack":
                raise OSError("connection refused")
            return self.ack_status, b'{"ok":true}'
        if self.raise_on == "artifact":
            raise OSError("connection reset")
        if self.artifact is None:
            return 404, b""
        return self.artifact_status, self.artifact

    def acks(self):
        """只看回执那一腿：字段名/布尔值/原因都从这里断言。"""
        return [json.loads(c[3].decode("utf-8")) for c in self.calls if cu.ACK_PATH in c[1]]

    def artifact_fetches(self):
        """真的去取产物字节的那几次（读清单与回执都不算）。"""
        return [c for c in self.calls
                if cu.MANIFEST_PATH not in c[1] and cu.ACK_PATH not in c[1]]

    def non_protocol_calls(self):
        """除"读清单"之外的每一次出网（下载/回执）——预览与拒跑时必须为空。"""
        return [c for c in self.calls if cu.MANIFEST_PATH not in c[1]]


class _Runner:
    """假 subprocess：按 argv 分流（投递脚本 vs 本机构建命令），并记录全部 argv。

    deliver=(目标路径, 字节) 用来模拟"投递脚本真的把新字节写进去了"——被测代码判的是
    **盘上字节**，不是脚本自述，所以没有这个模拟的每一条都应当落在 post_apply_sha_mismatch 那支。
    """

    def __init__(self, rc=0, output=OK_UPDATER_OUT, build_rc=0, build_output="", deliver=None):
        self.rc = rc
        self.output = output
        self.build_rc = build_rc
        self.build_output = build_output
        self.deliver = deliver
        self.argvs = []

    def __call__(self, argv):
        self.argvs.append(list(argv))
        if any("update_qmt_strategy" in str(x) for x in argv):
            if self.rc == 0 and self.deliver and "STRATEGY_UPDATE_RESULT=OK" in self.output:
                path, raw = self.deliver
                with open(path, "wb") as fh:  # 模拟脚本落盘（测试自己的临时目录，不是被测代码写的）
                    fh.write(raw)
            return self.rc, self.output
        return self.build_rc, self.build_output

    def deliveries(self):
        """只数"真的调了投递脚本"的那几次（构建命令不该算进幂等判据）。"""
        return [a for a in self.argvs if any("update_qmt_strategy" in str(x) for x in a)]


class _Fixture:
    """一台"假装的家"：QMT python 目录 + 已装策略 + 状态目录 + 临时目录 + config.xt.json。"""

    def __init__(self, tmp, installed_bytes=OLD_ART, extra_cfg=None):
        self.tmp = tmp
        self.qmt_dir = os.path.join(tmp, "qmt", "python")
        os.makedirs(self.qmt_dir, exist_ok=True)
        # 已安装的内置策略：投递脚本写的就是这个文件，比对读的也是这一个（不许两个名字）。
        self.strategy_file = os.path.join(self.qmt_dir, "local_strategy.py")
        self.write_installed(installed_bytes)
        self.state_dir = os.path.join(tmp, "state")
        os.makedirs(self.state_dir, exist_ok=True)
        self.temp_dir = os.path.join(tmp, "tmp_artifacts")
        cfg = {
            "token": FAKE_TOKEN,
            "user_id": "u_leg01",
            "report_url": BASE,
            "carrier_strategy_file": self.strategy_file,
            "carrier_qmt_python_dir": self.qmt_dir,
            "carrier_state_dir": self.state_dir,
            "carrier_temp_dir": self.temp_dir,
            # 更新脚本与 PowerShell 用"存在的假文件"占位：闸只判在不在，真跑由注入的 runner 承担。
            "carrier_updater_script": self._touch("update_qmt_strategy.ps1"),
            "carrier_ps_exe": self._touch("powershell.exe"),
            "carrier_executor_file": self._touch("local_executor.py",
                                                 b'STRATEGY_VERSION = "v01-20261101"\n'),
        }
        cfg.update(extra_cfg or {})
        self.config_path = os.path.join(tmp, "config.xt.json")
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh)

    def _touch(self, name, raw=b"# placeholder for the existence gate\n"):
        path = os.path.join(self.tmp, name)
        with open(path, "wb") as fh:
            fh.write(raw)
        return path

    def write_installed(self, raw):
        with open(self.strategy_file, "wb") as fh:
            fh.write(raw)

    def cfg_value(self, key):
        """从**配置文件**取一个键（断言 argv 时不写第二份路径，避免夹具改了用例还绿）。"""
        with open(self.config_path, encoding="utf-8") as fh:
            return json.load(fh)[key]

    def set_cfg(self, **over):
        """改一个键再落盘（缺件/错配置那几条用它，别再造第二份夹具）。"""
        with open(self.config_path, encoding="utf-8") as fh:
            cfg = json.load(fh)
        for key, value in over.items():
            if value is None:
                cfg.pop(key, None)
            else:
                cfg[key] = value
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh)

    ARTIFACT_URL = BASE + "/static/carrier/qmt_internal_strategy.py"

    def manifest(self, strategy_sha=None, **over):
        """默认给一份"确实要更新"的清单：契约那八个键一字不差，外加一条可选的产物地址。

        `artifact_url` **不是契约里的键**（brain 侧只承诺清单八键），用例带上它是因为盒子
        总得有个字节来源才谈得上"下载后验哈希"；它没带时走 `test_no_artifact_source_...` 那条。
        """
        doc = {
            "ok": True,
            "strategy_version": "v02-20261105",
            "strategy_sha256": _sha(NEW_ART) if strategy_sha is None else strategy_sha,
            "executor_version": "v01-20261101",
            "executor_sha256": "",
            "updated_at": "2026-11-05T15:40:00+08:00",
            "request_state": "idle",
            "requested_at": None,
            "artifact_url": self.ARTIFACT_URL,
        }
        doc.update(over)
        return doc

    def artifact_gets(self, transport):
        """只数"真的去取产物字节"的那几次（回执与读清单都不算）。"""
        return [c for c in transport.calls
                if cu.MANIFEST_PATH not in c[1] and cu.ACK_PATH not in c[1]]

    def updater(self, transport=None, runner=None):
        return cu.CarrierUpdater(self.config_path, now_fn=lambda: FIXED_NOW,
                                 transport=transport or _Transport(manifest=self.manifest()),
                                 runner=runner or _Runner())


def _run(u, dry_run=False):
    """跑一轮并把 stdout 收下来：锚行是给人和门禁 grep 的，必须能拿到全文。"""
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        rep = u.run_once(dry_run=dry_run)
    return rep, buf.getvalue()


class TestManifestUnreadableIsNeverUpToDate(unittest.TestCase):
    """规则 1：读不到/不完整一律报"不知道"，绝不折成"已一致"（本仓最重复的缺陷形状）。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_transport_error_reports_unknown_and_touches_nothing(self):
        transport = _Transport(manifest=self.fx.manifest(), raise_on="manifest")
        runner = _Runner()
        rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertEqual(rep["version"], "unknown")
        self.assertEqual(rep["sha256"], "unknown")
        self.assertIn("manifest_unreadable", rep["reason"])
        self.assertIn("CARRIER_UPDATE_RESULT=REFUSED", out)
        self.assertEqual(runner.deliveries(), [])
        self.assertEqual(transport.acks(), [], "读不到清单就回执＝把不知道报成失败结论")

    def test_non_200_manifest_is_unknown_not_empty(self):
        rep, _out = _run(self.fx.updater(
            transport=_Transport(manifest=self.fx.manifest(), manifest_status=503)))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("http_status_503", rep["reason"])

    def test_ok_false_is_refused(self):
        rep, _out = _run(self.fx.updater(
            transport=_Transport(manifest=self.fx.manifest(ok=False))))
        self.assertEqual(rep["status"], "REFUSED")
        # 码侧会把报文里给的 reason 一起带出来（没有就给 no_reason_given），
        # 判据锁的是"必须以 manifest_not_ok 起头"这个类，不是某个精确串——
        # 否则将来把上游原因说得更细反而会红，那种红只会教人把断言改松。
        self.assertTrue(rep["reason"].startswith("manifest_not_ok"),
                        "reason must stay in the manifest_not_ok family, got %r" % rep["reason"])

    def test_missing_sha_is_incomplete_not_up_to_date(self):
        # 清单在、但没给哈希：与"哈希相同"是两种读数，折成一个就是"以为更新过了"。
        rep, _out = _run(self.fx.updater(
            transport=_Transport(manifest=self.fx.manifest(strategy_sha256=""))))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("manifest_incomplete", rep["reason"])

    def test_missing_version_is_incomplete(self):
        rep, _out = _run(self.fx.updater(
            transport=_Transport(manifest=self.fx.manifest(strategy_version=""))))
        self.assertIn("manifest_incomplete", rep["reason"])

    def test_non_json_manifest_is_unknown(self):
        rep, _out = _run(self.fx.updater(transport=_Transport(manifest=None)))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("not_json", rep["reason"])

    def test_installed_file_unreadable_is_cannot_tell(self):
        # 已装那份读不到（文件不在）：既不是"没装上"也不是"已一致"。
        os.remove(self.fx.strategy_file)
        rep, _out = _run(self.fx.updater(transport=_Transport(manifest=self.fx.manifest())))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("installed_strategy_unreadable", rep["reason"])


class TestIdempotency(unittest.TestCase):
    """规则 2：同清单第二次跑必须空转；空转本身不回执，但运营点过就要回话。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_same_sha_means_no_download_no_apply_no_ack(self):
        transport = _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART)))
        runner = _Runner()
        rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual((rep["status"], rep["reason"]), ("OK", "up_to_date"))
        self.assertEqual(runner.deliveries(), [], "哈希一致还投＝每天重启一次交易客户端")
        self.assertEqual(transport.acks(), [])
        self.assertEqual(transport.non_protocol_calls(), [])
        self.assertNotIn("CARRIER_DOWNLOAD", out)

    def test_pending_request_is_acked_even_when_already_current(self):
        # 运营点了一下而盒子本来就是对的：动作可以没有，回话必须有（否则点击悬在那儿）。
        transport = _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART),
                                                         request_state="pending"))
        runner = _Runner()
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "OK")
        self.assertIn("ack_requested", rep["reason"])
        self.assertEqual(runner.deliveries(), [])
        self.assertEqual(len(transport.acks()), 1)
        self.assertTrue(transport.acks()[0]["ok"])

    def test_second_run_after_apply_is_a_no_op(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner(deliver=(self.fx.strategy_file, NEW_ART))
        u = self.fx.updater(transport=transport, runner=runner)
        _run(u)
        rep2, _out2 = _run(u)
        self.assertEqual(rep2["reason"], "up_to_date")
        self.assertEqual(len(runner.deliveries()), 1, "第二轮又投了一次＝不幂等")


class TestDownloadThenApply(unittest.TestCase):
    """规则 3/4/7：取字节 → 验哈希 → 逐字 argv 投递 → 回执四键；错字节一步都不走。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_apply_invokes_the_updater_with_the_exact_argv(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        # deliver=模拟"脚本真的把新字节写进活路径"：被测代码判的是盘上字节，不是脚本自述。
        runner = _Runner(deliver=(self.fx.strategy_file, NEW_ART))
        u = self.fx.updater(transport=transport, runner=runner)
        rep, out = _run(u)
        self.assertEqual(rep["status"], "OK", out[-400:])
        self.assertEqual(len(runner.deliveries()), 1)
        argv = runner.deliveries()[0]
        # 逐字断言这六件（-Apply 与 -ExpectedVersion 是这条腿的两条命门）
        self.assertEqual(argv[argv.index("-Source") + 1], os.path.join(self.fx.temp_dir, CANDIDATE_NAME))
        self.assertEqual(argv[argv.index("-QmtPythonDir") + 1], self.fx.qmt_dir)
        self.assertEqual(argv[argv.index("-StrategyFile") + 1], "local_strategy.py")
        self.assertEqual(argv[argv.index("-ExpectedVersion") + 1], "v02-20261105")
        self.assertIn("-Apply", argv)
        self.assertEqual(argv[argv.index("-File") + 1], self.fx.cfg_value("carrier_updater_script"))
        self.assertEqual(argv[:5], [self.fx.cfg_value("carrier_ps_exe"), "-NoProfile",
                                   "-ExecutionPolicy", "Bypass", "-File"])
        # 下载先于投递，且临时件落在 QMT 目录之外
        self.assertEqual([c[1] for c in transport.artifact_fetches()], [self.fx.ARTIFACT_URL])
        self.assertNotIn(self.fx.qmt_dir, argv[argv.index("-Source") + 1])
        self.assertIn("CARRIER_DOWNLOAD", out)
        # 验完盘才回执：回执报的是这台机器上真装着什么
        self.assertEqual(transport.acks()[0]["strategy_version"], "v02-20261105")
        self.assertTrue(transport.acks()[0]["ok"])
        self.assertEqual(rep["sha256"], _sha(NEW_ART))

    def test_updater_claiming_ok_but_disk_unchanged_is_not_success(self):
        # 脚本自述 OK 而盘上字节还是旧的＝"以为跑的是 v12 实际是 v07"，这一支必须判红。
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner()  # 没有 deliver：脚本"成功"但什么都没改
        rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "FAILED")
        self.assertIn("post_apply_sha_mismatch", rep["reason"])
        self.assertFalse(transport.acks()[0]["ok"])
        self.assertIn("STRATEGY_UPDATE_RESULT=OK", out)  # 脚本那行原样可见，只是不认它

    def test_two_digests_are_logged_so_a_human_can_compare(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        _rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertIn("CARRIER_MANIFEST ok=1 strategy_version=v02-20261105 strategy_sha256=%s"
                      % _sha(NEW_ART), out)
        self.assertIn("CARRIER_INSTALLED sha256=%s" % _sha(OLD_ART), out)

    def test_sha_mismatch_never_reaches_the_updater_but_acks_false(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=EVIL_ART)
        runner = _Runner()
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "FAILED")
        self.assertIn("downloaded_sha_mismatch", rep["reason"])
        self.assertEqual(runner.deliveries(), [], "未验字节交给投递脚本＝把假件投进活路径")
        self.assertEqual(len(transport.acks()), 1)
        self.assertFalse(transport.acks()[0]["ok"])
        self.assertIn("downloaded_sha_mismatch", transport.acks()[0]["reason"])
        self.assertEqual(open(self.fx.strategy_file, "rb").read(), OLD_ART)
        self.assertFalse(os.path.exists(os.path.join(self.fx.temp_dir, CANDIDATE_NAME)),
                         "验失败的临时件留在盘上，下次可能被当成候选")

    def test_download_failure_is_named_not_silenced(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=None, artifact_status=404)
        runner = _Runner()
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "FAILED")
        self.assertIn("download_failed_status_404", rep["reason"])
        self.assertEqual(runner.deliveries(), [])
        self.assertFalse(transport.acks()[0]["ok"])

    def test_no_artifact_source_is_named_as_unavailable(self):
        # 清单没给地址 + 这台机器明确不重烘（carrier_local_build_cmd="off"）：
        # 这是"取不到字节"，不是"没有要更新的东西"——两种读数必须能分开说。
        self.fx.set_cfg(carrier_local_build_cmd="off")
        transport = _Transport(manifest=self.fx.manifest(artifact_url=None), artifact=None)
        rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertEqual((rep["status"], rep["reason"]), ("FAILED", "artifact_bytes_unavailable"))
        self.assertFalse(transport.acks()[0]["ok"])
        self.assertIn("CARRIER_LOCAL_BUILD source=disabled_by_config", out)

    def test_default_byte_source_is_the_local_rebake(self):
        # brain 本批不供字节（carrierManifestPayload 里 has_artifact_url=false 那行注释就是它对
        # 这条腿的交代），所以**默认**就该走本机重烘，而不是要运营再配一条命令。
        transport = _Transport(manifest=self.fx.manifest(artifact_url=None), artifact=None)
        runner = _Runner(build_rc=1, build_output="BUILD_BOOM\n")
        _rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertIn("CARRIER_LOCAL_BUILD source=default_builder", out)
        self.assertEqual(len(runner.argvs), 1, "默认腿没被调用＝这条链在没有配置时根本不动")
        self.assertEqual(runner.argvs[0][0], sys.executable,
                         "重烘必须用运行本模块的这个解释器，不假设机器上还留着别的 Python")
        self.assertIn("--target", runner.argvs[0])
        self.assertIn("sandbox", runner.argvs[0])
        self.assertEqual(transport.artifact_fetches(), [], "清单没给地址就不该去出网取字节")

    def test_local_build_fallback_still_passes_the_sha_gate(self):
        built = os.path.join(self.fx.tmp, "built_artifact.py")
        with open(built, "wb") as fh:
            fh.write(NEW_ART)
        self.fx.set_cfg(carrier_local_build_cmd="python3 fake_builder.py --target sandbox")
        runner = _Runner(build_rc=0, build_output="BUILD_RESULT=OK artifact=%s\n" % built,
                         deliver=(self.fx.strategy_file, NEW_ART))
        transport = _Transport(manifest=self.fx.manifest(artifact_url=None))  # 清单没给产物地址
        rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "OK", out[-300:])
        self.assertTrue(any("fake_builder" in " ".join(a) for a in runner.argvs), "没跑构建命令")
        self.assertEqual(transport.artifact_fetches(), [], "清单没给地址却还去出网取字节")
        delivery = runner.deliveries()[0]
        self.assertTrue(delivery[delivery.index("-Source") + 1].startswith(self.fx.temp_dir),
                        "构建产物必须复制进临时目录再验再投，不许直接把仓库路径交给投递脚本")

    def test_local_build_with_wrong_bytes_is_still_refused(self):
        built = os.path.join(self.fx.tmp, "bad_built.py")
        with open(built, "wb") as fh:
            fh.write(EVIL_ART)
        self.fx.set_cfg(carrier_local_build_cmd="python3 fake_builder.py --target sandbox")
        runner = _Runner(build_rc=0, build_output="BUILD_RESULT=OK artifact=%s\n" % built)
        rep, _out = _run(self.fx.updater(transport=_Transport(manifest=self.fx.manifest(artifact_url=None)),
                                          runner=runner))
        self.assertEqual(rep["status"], "FAILED")
        self.assertIn("downloaded_sha_mismatch", rep["reason"])
        self.assertEqual(runner.deliveries(), [])

    def test_ack_body_field_names_match_the_contract_exactly(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        _run(self.fx.updater(transport=transport, runner=_Runner()))
        acks = [c for c in transport.calls if cu.ACK_PATH in c[1]]
        self.assertEqual(len(acks), 1)
        body = acks[0][3].decode("utf-8")
        doc = json.loads(body)
        self.assertEqual(set(doc.keys()), {"strategy_version", "executor_version", "ok", "reason"})
        self.assertIsInstance(doc["ok"], bool)
        self.assertEqual(acks[0][0], "POST")
        self.assertEqual(acks[0][1], BASE + cu.ACK_PATH)
        self.assertEqual(acks[0][2], FAKE_TOKEN, "Bearer 腿的钥匙就是配置里那一把（不打印，只上头）")

    def test_updater_abort_is_failed_and_acks_false_without_extra_rollback(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner(rc=2, output="STRATEGY_UPDATE_RESULT=ABORT step=4 reason=version_mismatch\n")
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual((rep["status"], rep["reason"]), ("FAILED", "updater_abort"))
        self.assertEqual(len(transport.acks()), 1)
        self.assertFalse(transport.acks()[0]["ok"])
        self.assertEqual(len(runner.deliveries()), 1, "脚本自己已经回滚并立牌，这里绝不补第二刀")
        self.assertEqual(open(self.fx.strategy_file, "rb").read(), OLD_ART)

    def test_updater_rc_zero_without_ok_anchor_is_not_success(self):
        # 退出码 0 但没有 STRATEGY_UPDATE_RESULT=OK 锚＝不认（锚才是这条腿的自证）。
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner(rc=0, output="some noise only\n")
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "FAILED")
        self.assertIn("updater_rc_0", rep["reason"])


class TestEnsureTaskNamePassthrough(unittest.TestCase):
    """2026-11-09 切换实录：投递脚本缺省任务名是广州机的 QMT-Ensure-Running，
    朋友机上是 QMTF-Client-Ensure。不显式传就会在有序重启里 disable/run 一个不存在的
    任务，网关与客户端被杀掉却没人拉起。这一支锁住「配置里写什么就传什么」。
    """

    def _argv_with(self, extra_cfg):
        tmp = tempfile.TemporaryDirectory()
        try:
            fx = _Fixture(tmp.name, extra_cfg=extra_cfg)
            u = fx.updater(transport=_Transport(manifest=fx.manifest()),
                           runner=_Runner())
            return u.updater_argv(os.path.join(tmp.name, "candidate.py"),
                                 {"strategy_version": "v02-20261105"})
        finally:
            tmp.cleanup()

    def test_configured_task_name_is_passed_verbatim(self):
        argv = self._argv_with({"carrier_ensure_task": "QMTF-Client-Ensure"})
        self.assertIn("-EnsureTaskName", argv)
        self.assertEqual(argv[argv.index("-EnsureTaskName") + 1], "QMTF-Client-Ensure")

    def test_no_guangzhou_task_name_leaks_into_friend_box(self):
        argv = self._argv_with({})
        self.assertNotIn("QMT-Ensure-Running", argv)
        self.assertNotIn("-EnsureTaskName", argv)


class TestStopMarker(unittest.TestCase):
    """规则 5：目标目录里有 STOP_AUTO_UPDATE 牌 ⇒ 提前拒（那块牌只有人工能清），拒也要回话。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)
        with open(os.path.join(self.fx.qmt_dir, cu.STOP_MARKER_NAME), "w", encoding="ascii") as fh:
            fh.write("reason=version_mismatch\n")

    def tearDown(self):
        self._tmp.cleanup()

    def test_marker_blocks_before_any_download_or_apply(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner()
        rep, out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("stop_auto_update_marker_present", rep["reason"])
        self.assertEqual(runner.deliveries(), [], "牌还在就继续投＝在不明状态上覆盖别人的载体")
        self.assertEqual(transport.non_protocol_calls(),
                         [c for c in transport.non_protocol_calls() if cu.ACK_PATH in c[1]],
                         "被牌拦下的这一轮除了回执不该再出网")
        self.assertIn("CARRIER_UPDATE_RESULT=REFUSED", out)
        self.assertEqual(len(transport.acks()), 1)
        self.assertFalse(transport.acks()[0]["ok"])

    def test_dry_run_with_marker_sends_nothing(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        rep, _out = _run(self.fx.updater(transport=transport, runner=_Runner()), dry_run=True)
        self.assertEqual(rep["status"], "REFUSED")
        self.assertEqual(transport.acks(), [], "预览不许把别人那格 pending 吃掉")


class TestDryRun(unittest.TestCase):
    """规则 6：--dry-run 零副作用——不下载、不投递、不回执、状态文件不写。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_dry_run_does_nothing_but_report_the_plan(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner()
        u = self.fx.updater(transport=transport, runner=runner)
        rep, out = _run(u, dry_run=True)
        self.assertEqual(rep["status"], "DRYRUN")
        self.assertIn("update_needed", rep["reason"])
        self.assertEqual(runner.deliveries(), [])
        self.assertEqual(transport.acks(), [])
        self.assertEqual(transport.artifact_fetches(), [], "预览阶段去下载＝把没批准的字节放上这台机器")
        self.assertEqual(transport.non_protocol_calls(), [], "预览只许读清单")
        self.assertFalse(os.path.exists(u.state_path), "预览写状态＝把今天的空转记成一次真更新")
        self.assertIn("CARRIER_UPDATE_RESULT=DRYRUN", out)

    def test_dry_run_when_up_to_date_is_still_a_no_op(self):
        transport = _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART),
                                                         request_state="pending"))
        rep, _out = _run(self.fx.updater(transport=transport, runner=_Runner()), dry_run=True)
        self.assertEqual(transport.acks(), [], "预览不许把别人那格 pending 吃掉")
        self.assertEqual(transport.artifact_fetches(), [])
        self.assertNotIn("download", rep["reason"])

    def test_dry_run_does_not_create_the_temp_dir(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        _run(self.fx.updater(transport=transport, runner=_Runner()), dry_run=True)
        self.assertFalse(os.path.isdir(self.fx.temp_dir))


class TestNeverPrintTheKey(unittest.TestCase):
    """规则 8：钥匙不在任何打印行、状态文件、tmp 目录里本模块写出的文件（B-N2 / §TOKEN-PERAACCT）。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_token_absent_from_stdout_state_and_every_written_file(self):
        u = self.fx.updater(transport=_Transport(manifest=self.fx.manifest(), artifact=NEW_ART),
                            runner=_Runner())
        _rep, out = _run(u)
        self.assertNotIn(FAKE_TOKEN, out, "钥匙出现在标准输出里")
        # 失败路径同样扫一遍（异常文本最容易顺手 str(e) 带出东西）
        u2 = self.fx.updater(transport=_Transport(manifest=self.fx.manifest(), raise_on="manifest"),
                             runner=_Runner())
        _rep2, out2 = _run(u2)
        self.assertNotIn(FAKE_TOKEN, out2)
        # tmp 目录里逐字节扫（config.xt.json 是钥匙的**源**，排除它自己）
        hits = []
        for root, _dirs, files in os.walk(self.fx.tmp):
            for name in files:
                path = os.path.join(root, name)
                if os.path.abspath(path) == os.path.abspath(self.fx.config_path):
                    continue
                with open(path, "rb") as fh:
                    if FAKE_TOKEN.encode() in fh.read():
                        hits.append(path)
        self.assertEqual(hits, [])
        self.assertTrue(os.path.exists(u.state_path))

    def test_state_file_structure_has_no_credential_keys(self):
        u = self.fx.updater(transport=_Transport(manifest=self.fx.manifest(), artifact=NEW_ART),
                            runner=_Runner())
        _run(u)
        with open(u.state_path, encoding="utf-8") as fh:
            doc = json.load(fh)
        flat = json.dumps(doc, sort_keys=True).lower()
        self.assertNotIn("token", flat)
        self.assertNotIn("authorization", flat)
        for row in doc.get("runs") or []:
            self.assertEqual(set(row.keys()),
                             {"status", "version", "sha256", "reason", "at", "acked"})

    def test_config_header_line_only_reports_token_presence(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            cu.main(["--config", self.fx.config_path])
        out = buf.getvalue()
        self.assertIn("token_present=1", out)
        self.assertNotIn(FAKE_TOKEN, out)


class TestConfigAndCli(unittest.TestCase):
    """配置缺件与命令行边界：地址/钥匙/脚本缺→点名拒；没给模式→退出码 2。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_no_base_url_refuses_with_named_reason(self):
        self.fx.set_cfg(report_url=None)
        transport = _Transport(manifest=self.fx.manifest())
        rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("base_url_absent", out)
        self.assertEqual(transport.calls, [], "没地址还去出网＝把读数泼到默认路由上")

    def test_no_token_refuses_and_says_token_absent(self):
        self.fx.set_cfg(token=None)
        rep, out = _run(self.fx.updater(transport=_Transport(manifest=self.fx.manifest()),
                                        runner=_Runner()))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("token_absent", out)

    def test_missing_updater_script_refuses_before_download(self):
        self.fx.set_cfg(carrier_updater_script=os.path.join(self.fx.tmp, "nope_absent.ps1"))
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner()
        rep, _out = _run(self.fx.updater(transport=transport, runner=runner))
        self.assertEqual((rep["status"], rep["reason"]), ("REFUSED", "updater_script_absent"))
        self.assertEqual(runner.deliveries(), [])
        self.assertFalse(transport.acks()[0]["ok"])

    def test_missing_qmt_python_dir_refuses(self):
        self.fx.set_cfg(carrier_qmt_python_dir=self.fx.qmt_dir + "_absent",
                        carrier_strategy_file=os.path.join(self.fx.qmt_dir + "_absent",
                                                           "local_strategy.py"))
        transport = _Transport(manifest=self.fx.manifest())
        rep, _out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertEqual(rep["status"], "REFUSED")
        self.assertIn("installed_strategy_unreadable", rep["reason"])

    def test_temp_dir_inside_qmt_dir_is_refused(self):
        # 临时件落在 QMT 目录里＝客户端可能把它当策略加载；这是结构性错误，不是偏好。
        self.fx.set_cfg(carrier_temp_dir=os.path.join(self.fx.qmt_dir, "tmp"))
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        rep, _out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertEqual((rep["status"], rep["reason"]), ("REFUSED", "temp_dir_inside_qmt_dir"))
        self.assertEqual(transport.acks()[0]["ok"], False)

    def test_cli_without_mode_exits_two(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = cu.main(["--config", self.fx.config_path])
        self.assertEqual(rc, 2, "没给旗标就动手＝误跑一次要重启别人的交易客户端")
        self.assertIn("no_mode_given_use_once", buf.getvalue())

    def test_cli_dry_run_writes_no_state_and_sends_nothing(self):
        transport = _Transport(manifest=self.fx.manifest(), artifact=NEW_ART)
        runner = _Runner()
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = cu._run_cli(["--config", self.fx.config_path, "--once", "--dry-run"],
                             transport=transport, runner=runner)
        self.assertEqual(rc, 0, buf.getvalue()[-300:])
        self.assertIn("CARRIER_UPDATE_RESULT=DRYRUN", buf.getvalue())
        self.assertEqual(transport.acks(), [])
        self.assertEqual(runner.deliveries(), [])
        self.assertNotIn(FAKE_TOKEN, buf.getvalue())

    def test_cli_once_round_trips_through_the_real_default_entry(self):
        # 走真 main：只把两条默认腿换掉（模块级），其余（参数解析/退出码/锚行）全部真跑。
        real_transport, real_runner = cu._urllib_transport, cu._subprocess_runner
        seen = []

        def fake(method, url, token, body):
            seen.append((method, url))
            if cu.MANIFEST_PATH in url:
                return 200, json.dumps(self.fx.manifest(artifact_url=BASE + "/a.py")).encode()
            if cu.ACK_PATH in url:
                return 200, b'{"ok":true}'
            return 200, NEW_ART  # 产物字节与清单哈希一致，才允许走到投递

        def fake_runner(argv):
            # 模拟投递脚本落盘（这条用例验的是 main 的接线与退出码，不是脚本自己）
            with open(self.fx.strategy_file, "wb") as fh:
                fh.write(NEW_ART)
            return 0, OK_UPDATER_OUT

        cu._urllib_transport = fake
        cu._subprocess_runner = fake_runner
        try:
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = cu.main(["--config", self.fx.config_path, "--once"])
        finally:
            cu._urllib_transport, cu._subprocess_runner = real_transport, real_runner
        self.assertEqual(rc, 0, buf.getvalue()[-300:])
        self.assertIn("CARRIER_UPDATE_RESULT=OK", buf.getvalue())
        self.assertNotIn(FAKE_TOKEN, buf.getvalue())


class TestResultLineShape(unittest.TestCase):
    """最后那一行人机共读：状态只可能是四种，且一定带 version/sha256/reason，且全 ASCII。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_every_path_prints_exactly_one_result_line(self):
        cases = [
            _Transport(manifest=self.fx.manifest(), raise_on="manifest"),
            _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART))),
            _Transport(manifest=self.fx.manifest(), artifact=NEW_ART),
            _Transport(manifest=self.fx.manifest(), artifact=EVIL_ART),
        ]
        for transport in cases:
            _rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
            lines = [ln for ln in out.splitlines() if ln.startswith("CARRIER_UPDATE_RESULT=")]
            self.assertEqual(len(lines), 1, "每一轮恰好一行结论，多了就没法 grep：%s" % out)
            status = lines[0].split(" ", 1)[0].split("=", 1)[1]
            self.assertIn(status, ("OK", "DRYRUN", "REFUSED", "FAILED"))
            for key in (" version=", " sha256=", " reason="):
                self.assertIn(key, lines[0])

    def test_every_printed_line_is_pure_ascii(self):
        # 锚行必须 ASCII：GBK 控制台上中文锚点"看着在、grep 永远不中"（11-02 现网两次假象同源）。
        transport = _Transport(manifest=self.fx.manifest(executor_version=u"贵州茅台"), artifact=NEW_ART)
        _rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertTrue(out, "一行都没打＝这一轮无法取证")
        for ln in out.splitlines():
            self.assertTrue(all(ord(ch) < 128 for ch in ln), "锚行出现非 ASCII：%r" % ln)


class TestExecutorLegIsReportOnly(unittest.TestCase):
    """执行器那条腿只报不投：清单说要的那份与本机装的不一样时点名，绝不折进"已一致"。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_executor_diff_is_named_but_not_applied(self):
        transport = _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART),
                                                         executor_sha256="deadbeef" * 8),
                               artifact=NEW_ART)
        _rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertIn("CARRIER_EXECUTOR state=differs_not_applied", out)
        self.assertEqual([c for c in transport.calls if cu.MANIFEST_PATH not in c[1]], [],
                         "执行器不一致时不该投递，也不该回执（这一轮没事可做）")

    def test_executor_sha_absent_is_reported_as_such(self):
        transport = _Transport(manifest=self.fx.manifest(strategy_sha256=_sha(OLD_ART),
                                                         executor_sha256=""))
        _rep, out = _run(self.fx.updater(transport=transport, runner=_Runner()))
        self.assertIn("CARRIER_EXECUTOR reason=manifest_has_no_executor_sha", out)


if __name__ == "__main__":
    unittest.main()
