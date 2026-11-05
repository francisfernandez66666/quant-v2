#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.carrier_updater -- the BOX-SIDE carrier update poller.

Why this module exists (broker notice 2026-11-03, effective 2026-11-05):
  * a QMT BUILT-IN strategy may no longer read or write local files and may no
    longer reach the network at all, so the artifact that carries the rules
    (qmt_gateway/qmt_internal_strategy.py, baked by scripts/build_local_executor.py)
    can neither update itself nor report what it is running;
  * the ordinary local Python process on the SAME physical box is outside that
    exemption and may use files and the network -- so the update has to be done
    by that process. This file is that job: pull the manifest from the decision
    brain, compare it with what is installed, hand the verified bytes to the
    existing delivery script, then ack.

Division of labour (do not blur it):
    BRAIN side (owned by another agent, internal/server + web/src):
        GET  /api/qmt/carrier/manifest   -- what SHOULD be running
        POST /api/qmt/carrier/ack        -- what the box actually did
        the operator click in the UI     -- request_state = "pending"
    BOX side (this module):
        poll -> compare -> fetch -> verify -> apply -> ack, one round per call,
        driven by a scheduled task after the close (15:40, owner ruling 2 of
        docs/IMPL_QMT_INTERNAL_LOOP_20261103.md §9) or by a manual --once.

Four rules this module is built around (each one is a bug we have already been
bitten by, so each one has a test in tests/test_carrier_updater.py):
  1. an ABSENT reading is never folded into "nothing to do" (AGENTS 读数纪律,
     report_uplink's missing!=empty, external_brain.go's UNKNOWN, the J0
     "zero replies is not health" fix): if the manifest cannot be read, or it
     carries no version/sha, the round reports "unknown" and touches nothing.
  2. only verified bytes reach the live path: the artifact lands in a temp
     directory OUTSIDE the QMT directory, its SHA256 is compared with the
     manifest's, and only then is scripts/update_qmt_strategy.ps1 invoked.
  3. the delivery script keeps every stage it already has (BACKUP -> WRITE with
     three SHA readings -> VERSION read-back -> restart in the documented order
     -> STOP_AUTO_UPDATE marker on failure). This module does not duplicate,
     bypass, or re-implement any of it, and it never launches XtItClient itself:
     a client started inside an SSH/agent session dies with the session
     (docs/POSTMORTEM_QMT_LINK_20261102.md, the 10-29 outage).
  4. the key never leaves memory: it is resolved by report_uplink.resolve_token
     from config.xt.json (one source of truth, §TOKEN-PERAACCT/B-N2) and is
     never printed, never logged, never written to the state file.

Everything this module prints is pure ASCII on purpose: a GBK console on the box
turns CJK into '?' and has already produced two false readings here (AGENTS,
2026-11-02 运维口径), and the machine-readable anchors are exactly what a human
or a gate greps for.

English summary: box-side poller that pulls the per-account carrier manifest
from the brain, compares it against the installed built-in strategy, downloads
the artifact to a temp dir, verifies its SHA256, applies it through the existing
update_qmt_strategy.ps1 (never bypassing it), acks the outcome, and prints one
machine-readable CARRIER_UPDATE_RESULT line. ASCII output, key never printed.
"""

import hashlib
import json
import os
import subprocess
import sys
import time

import report_uplink

# 本模块自身版本（进状态文件，排查时要知道是哪一版的轮询逻辑在跑）。
UPDATER_VERSION = "carrier-updater-v01-20261105"

# 决策机两个端点的名字（与 brain 侧同一组字符串，任何一方改名都要同批改这里）。
MANIFEST_PATH = "/api/qmt/carrier/manifest"
ACK_PATH = "/api/qmt/carrier/ack"

# 载体的停更标记：update_qmt_strategy.ps1 自己在版本回读失败时立这块牌，
# 而它自己也会拒——这里提前拒是为了把原因说清楚（不是绕过去，是同一块牌的同一语义）。
STOP_MARKER_NAME = "STOP_AUTO_UPDATE"

# 更新脚本的默认位置（仓库里 scripts/，朋友包里落进 tools/；两处都试，取第一个存在的，
# 而不是在载体侧硬编码第三种布局）。
UPDATER_CANDIDATES = ("scripts/update_qmt_strategy.ps1", "tools/update_qmt_strategy.ps1")

# 沙箱构建器的默认位置（同上两种布局）：brain 本批不供产物字节，盒子默认走"本机重烘"那一支。
BUILDER_CANDIDATES = ("scripts/build_local_executor.py", "tools/build_local_executor.py")

# 清单里可能携带产物字节地址的键（按顺序取第一个非空串）。
ARTIFACT_URL_KEYS = ("artifact_url", "strategy_artifact_url", "strategy_url")

# 状态文件里保留的历史条数（只为"看得见"，不参与任何判定）。
STATE_KEEP = 20


def _ascii_text(text):
    """任何要打印的东西先过这道：非 ASCII 字节一律换成 '?'。

    为什么必须换而不是留着：这台机器的控制台码页是 GBK，中文锚点看起来在、
    grep 永远不中（11-02 现网两次假象同源）；换掉是"如实的降级"，留着是"假绿"。
    """
    return str(text or "").encode("ascii", "replace").decode("ascii")


def _sha256_file(path):
    """文件全量 SHA256（读不到返回空串，由调用方按"取不到"处理，绝不折成"相等"）。"""
    try:
        h = hashlib.sha256()
        with open(path, "rb") as fh:  # 只读打开（§F18：产物是证据，不许写）
            for chunk in iter(lambda: fh.read(65536), b""):
                h.update(chunk)
        return h.hexdigest()
    except OSError:
        return ""


def _repo_root():
    """本文件所在 qmt_gateway 的上一级＝仓库根/落位根（更新器与构建器都在它下面）。"""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _default_ps_exe():
    """Windows PowerShell，与 install_tasks.ps1 用的是同一个 $env:WINDIR 形态（不自造第三种）。"""
    windir = os.environ.get("WINDIR") or "C:\\Windows"
    return windir + "\\System32\\WindowsPowerShell\\v1.0\\powershell.exe"


def _url_or_none(value):
    """只在 manifest 里出现非空字符串时才认它，其它形状（dict/数字）一律当"没有这个键"。"""
    text = str(value or "").strip()
    return text if text else None


def _is_within(child, parent):
    """child 是否落在 parent 目录里（realpath + 大小写无关，Windows 的路径比较必须这样）。"""
    try:
        c = os.path.realpath(child).lower().rstrip(os.sep)
        p = os.path.realpath(parent).lower().rstrip(os.sep)
    except OSError:
        return False
    return c == p or c.startswith(p + os.sep.lower())


def _urllib_transport(method, url, token, body):
    """真实传输腿：标准库 urllib + 默认 TLS 校验，返回 (状态码, 响应字节)。

    与 report_uplink._urllib_opener 同一姿势（不建 unverified context）；异常文本
    结构上不含 Authorization 头，所以上层直接把异常类名打进日志也不会有凭证外泄。
    """
    import urllib.error
    import urllib.request
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return int(resp.status), resp.read()
    except urllib.error.HTTPError as exc:
        return int(exc.code), exc.read()


class CarrierUpdater:
    """一轮完整的"拉清单 → 比 → 取字节 → 验 → 投 → 回执"，跑完就退出（非常驻）。

    为什么是单轮而非常驻：与 report_uplink 同一取向（可 cron 可手敲），常驻循环
    会把 §OBS-GUARD 那类"循环静默停摆但进程还活着"的形态再制造一遍。
    """

    def __init__(self, config_path, now_fn=time.time, transport=None, runner=None):
        # 配置与钥匙：全部借 report_uplink 的那两条腿（同一份真源，不开第二个口子）。
        self.config_path = os.path.abspath(config_path)
        self.cfg = report_uplink.load_xt_config(self.config_path)
        self.base_url = str(self.cfg.get("carrier_base_url") or self.cfg.get("report_url") or "").strip()
        self.token = report_uplink.resolve_token(self.cfg)
        self.now_fn = now_fn
        # transport(method, url, token, body) -> (status:int, body:bytes)；测试注入假腿。
        self._transport = transport or _urllib_transport
        # runner(argv) -> (rc:int, output:str)：更新脚本与本地构建都走这一条注入腿。
        self._runner = runner or _subprocess_runner

        # 已安装的内置策略文件：优先用 carrier 专属键，缺省沿用 report_uplink 那一个
        # （它本来就指向"这台机器上装好的那份"，再开第二个真源就会两套账）。
        self.strategy_file = str(self.cfg.get("carrier_strategy_file")
                                 or self.cfg.get("uplink_strategy_file") or "").strip()
        # 执行器文件（只报不投：更新执行器是 update_local_executor.ps1 那条腿的事）。
        self.executor_file = str(self.cfg.get("carrier_executor_file")
                                 or self.cfg.get("uplink_executor_file") or "").strip()
        # QMT 的 python 目录：显式键优先，否则由已安装策略文件所在目录推出
        # ——那块牌（STOP_AUTO_UPDATE）与投递脚本的 -QmtPythonDir 都认这个目录。
        self.qmt_python_dir = str(self.cfg.get("carrier_qmt_python_dir") or "").strip()
        if not self.qmt_python_dir and self.strategy_file:
            self.qmt_python_dir = os.path.dirname(os.path.abspath(self.strategy_file))

        # 投递脚本（唯一合法的写盘路径）与它的 PowerShell。
        self.ps_exe = str(self.cfg.get("carrier_ps_exe") or _default_ps_exe()).strip()
        self.updater_script = str(self.cfg.get("carrier_updater_script") or "").strip()
        if not self.updater_script:
            root = _repo_root()
            for rel in UPDATER_CANDIDATES:
                cand = os.path.join(root, *rel.split("/"))
                if os.path.exists(cand):
                    self.updater_script = cand
                    break

        # 状态与临时目录：状态只写哈希/版本/原因；临时产物落仓库外的可配目录。
        cfg_dir = os.path.dirname(self.config_path)
        self.state_dir = str(self.cfg.get("carrier_state_dir")
                             or self.cfg.get("uplink_state_dir") or cfg_dir).strip() or cfg_dir
        self.state_path = os.path.join(self.state_dir, "carrier_state.json")
        self.temp_dir = str(self.cfg.get("carrier_temp_dir") or os.path.join(self.state_dir, "carrier_tmp")).strip()
        # 本机重烘那条腿（brain 本批不供字节 ⇒ 它是默认的一支）：
        #   键没写过       = 自动找构建器（scripts/ 或 tools/ 两处之一）并重烘一份；
        #   键写成 "off"   = 这台机器明确不重烘 ⇒ 没有字节来源时如实报 artifact_bytes_unavailable
        #                    （那是"取不到"，绝不是"没有要更新的"）；
        #   键写成一条命令 = 用那条命令（别的 venv、别的输出布局都在这儿说，不散到别处）。
        self.local_build_cmd = str(self.cfg.get("carrier_local_build_cmd") or "").strip()
        self.local_build_disabled = self.local_build_cmd.lower() in ("off", "none", "disabled")
        # 显式配置的产物地址（清单没带 URL 键时的兜底；两者都没有就是"取不到字节"）。
        self.configured_artifact_url = str(self.cfg.get("carrier_artifact_url") or "").strip()

        self.history = []
        self.manifest_error = ""
        self._load_state()

    # ---------------- 状态文件（结构与凭证无关：只写哈希/版本/ASCII 原因） ----------------
    def _load_state(self):
        """读回上一轮结果；坏/缺按空态起步（读侧 fail-open，写侧从不因此放宽）。"""
        try:
            with open(self.state_path, "rb") as fh:
                doc = json.loads(fh.read().decode("utf-8", "replace"))
            rows = doc.get("runs") or []
            self.history = [r for r in rows if isinstance(r, dict)][-STATE_KEEP:]
        except OSError:
            return  # 首跑没有状态文件：合法空态
        except Exception:  # noqa: BLE001 -- 状态坏了不炸轮询，重头记就行
            self.history = []

    def _save_state(self, row):
        """原子落盘（tmp+rename）并只保留最近 STATE_KEEP 条——**里面没有钥匙**（用例逐字节钉死）。"""
        self.history = (self.history + [row])[-STATE_KEEP:]
        doc = {"updater_version": UPDATER_VERSION, "runs": self.history}
        tmp = self.state_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=1, sort_keys=True)
        os.replace(tmp, self.state_path)

    # ---------------- 出网（GET 清单 / GET 产物 / POST 回执） ----------------
    def _request(self, method, url, body=None):
        """统一走注入腿；任何异常都收敛成 (0, b"")，由调用方报"取不到"而不是"没有"。"""
        try:
            status, payload = self._transport(method, url, self.token, body)
            return int(status), (payload or b"")
        except Exception as exc:  # noqa: BLE001 -- 传输坏了不许把整轮崩掉，只报原因
            self._log("CARRIER_HTTP method=%s err=%s" % (method, exc.__class__.__name__))
            return 0, b""

    def fetch_manifest(self):
        """GET 清单，返回 dict；读不到/不是 2xx/不是 JSON/缺必备键 ⇒ None（绝不当成"已一致"）。

        失败原因一律留进 `self.manifest_error`，让最后那行结论能点名是**哪一段**读不到
        （"取不到"与"没有要更新的"必须能分开说；折成一句就回到 J0 那批的假绿形态）。
        """
        self.manifest_error = ""
        if not self.base_url:
            return self._manifest_fail("base_url_absent")
        if not self.token:
            return self._manifest_fail("token_absent")
        status, raw = self._request("GET", self.base_url.rstrip("/") + MANIFEST_PATH)
        if status != 200:
            return self._manifest_fail("http_status_%d" % status)
        try:
            doc = json.loads(raw.decode("utf-8-sig", "replace"))
        except ValueError:
            return self._manifest_fail("not_json")
        if not isinstance(doc, dict):
            return self._manifest_fail("not_object")
        return doc

    def _manifest_fail(self, reason):
        """读清单失败的唯一出口：打一行原因（不含响应体，响应体可能带别的东西）并记下来。"""
        self.manifest_error = reason
        self._log("CARRIER_MANIFEST_READ reason=%s" % reason)
        return None

    # ---------------- 已安装侧读数（只读，读不到一律返回空串） ----------------
    def installed_sha256(self):
        return _sha256_file(self.strategy_file) if self.strategy_file else ""

    def installed_version(self):
        return report_uplink.read_version_constant(self.strategy_file) if self.strategy_file else ""

    def marker_present(self):
        """停更标记在不在（判的是投递脚本自己那块牌，不另造第二块）。"""
        if not self.qmt_python_dir:
            return False
        return os.path.exists(os.path.join(self.qmt_python_dir, STOP_MARKER_NAME))

    # ---------------- 取产物字节（temp 目录，永远在 QMT 目录之外） ----------------
    def _artifact_url(self, manifest):
        for key in ARTIFACT_URL_KEYS:
            url = _url_or_none(manifest.get(key))
            if url:
                return url
        return self.configured_artifact_url or None

    def _local_build_argv(self):
        """本机重烘那条兜底腿的命令行（brain 侧清单明说"本批不供字节"，靠的就是这一条）。

        默认命令在朋友包/仓库两种布局下都指向构建器本身（tools/ 与 scripts/ 两处之一），
        用**运行本模块的这个解释器**去跑——不假设机器上还留着 11-03 那个临时 Python 3.12 副本
        （install_local_executor.ps1 文件头记过那条漂移）。显式配置了 carrier_local_build_cmd
        就以它为准（运营要用别的 venv 时就在那儿写，不散到别处）。
        """
        if self.local_build_disabled:
            return [], "disabled_by_config"
        if self.local_build_cmd:
            return self.local_build_cmd.split(), "configured"
        root = _repo_root()
        for rel in BUILDER_CANDIDATES:
            path = os.path.join(root, *rel.split("/"))
            if os.path.exists(path):
                return [sys.executable, path, "--target", "sandbox"], "default_builder"
        return [], "no_builder_found"

    def _stage_from_url(self, url):
        """下载到临时文件并返回 (path, reason)——先落临时路径，绝不直写活路径。"""
        status, raw = self._request("GET", url)
        if status != 200 or not raw:
            self._log("CARRIER_DOWNLOAD reason=http_status_%d bytes=%d" % (status, len(raw)))
            return "", "download_failed_status_%d" % status
        try:
            os.makedirs(self.temp_dir, exist_ok=True)
            path = os.path.join(self.temp_dir, "candidate_%d.py" % int(self.now_fn()))
            with open(path, "wb") as fh:
                fh.write(raw)
        except OSError as exc:
            return "", "temp_write_failed:%s" % exc.__class__.__name__
        return path, ""

    def _stage_from_local_build(self, argv, source):
        """本机重烘那条兜底腿：跑构建命令，从输出里取 `artifact=<path>`，再复制进临时目录。

        为什么这是**默认**而不是可选项：brain 侧清单自己写了
        `has_artifact_url=false // 本批不供字节；盒子据此走本机重烘那条兜底腿`
        （internal/server/carrier.go 的 carrierManifestPayload）。
        为什么它仍然安全：构建器是确定性的（HOME_TEST_KIT §4b 实测"解压后在包内重烘＝逐字节相同"），
        而烘出来的那份**照样要和清单里的 SHA256 比过**才允许交给投递脚本——
        这台机器的树比期望版旧时，重烘出来的是旧字节、哈希必然对不上，于是这一轮如实失败，
        绝不会把旧载体投上去（宁可不更新，也不"看着像更新了"）。
        """
        rc, out = self._runner(argv)
        path = ""
        for line in _ascii_text(out).splitlines():
            if "artifact=" in line:
                path = line.split("artifact=", 1)[1].strip().split()[0]
        if rc != 0 or not path or not os.path.exists(path):
            self._log("CARRIER_LOCAL_BUILD source=%s rc=%d artifact_present=%d"
                      % (source, rc, 1 if path else 0))
            return "", "local_build_failed_rc_%d" % rc
        self._log("CARRIER_LOCAL_BUILD source=%s rc=0 artifact=found" % source)
        try:
            os.makedirs(self.temp_dir, exist_ok=True)
            staged = os.path.join(self.temp_dir, "candidate_%d.py" % int(self.now_fn()))
            with open(path, "rb") as src, open(staged, "wb") as dst:
                dst.write(src.read())
        except OSError as exc:
            return "", "local_build_stage_failed:%s" % exc.__class__.__name__
        return staged, ""

    def stage_artifact(self, manifest):
        """按可用性依次尝试远程下载 / 本机重烘；两条都不通就是"取不到字节"（不是"无事可做"）。

        brain 侧清单自己写了 `has_artifact_url=false // 本批不供字节；盒子据此走本机重烘那条兜底腿`
        （internal/server/carrier.go 的 carrierManifestPayload），所以**默认走重烘**；
        哪天真给了地址，认地址优先——那是同一道哈希闸的两个来源，不是两套标准。
        """
        url = self._artifact_url(manifest)
        if url:
            return self._stage_from_url(url)
        argv, source = self._local_build_argv()
        if argv:
            return self._stage_from_local_build(argv, source)
        # 「取不到字节」有两种完全不同的原因，必须分开说：这台机器**显式关了**本机重烘
        # （disabled_by_config，配置决定），还是**压根没找到构建器**（no_builder_found，装机不完整）。
        # 两者折成同一句话时，运维会去查错的方向——一个是改配置，一个是补文件。
        self._log("CARRIER_LOCAL_BUILD source=%s" % source)
        return "", "artifact_bytes_unavailable"

    # ---------------- 投递（唯一写盘路径 = 既有更新脚本的子进程） ----------------
    def updater_argv(self, source_path, manifest):
        """投递脚本的命令行——-Apply 与 -ExpectedVersion 是这条腿的两条命门，缺一即错。

        -StrategyFile 显式带上**已安装那份的名字**：否则更新脚本按它自己的缺省名写，
        于是"我们比过哈希的文件"和"它写下去的文件"可以是两个文件。
        """
        return [self.ps_exe, "-NoProfile", "-ExecutionPolicy", "Bypass",
                "-File", self.updater_script,
                "-Source", source_path,
                "-QmtPythonDir", self.qmt_python_dir,
                "-StrategyFile", os.path.basename(self.strategy_file),
                "-Apply",
                "-ExpectedVersion", str(manifest.get("strategy_version") or "")]

    def run_updater(self, argv):
        """跑更新脚本并判它的锚。返回 (ok, reason, output_text)。

        只认它自己打的两行锚（RESULT=OK / RESULT=ABORT）＋退出码：更新脚本对半态
        已经做了回滚与立牌，这里绝不"看着像失败就再补一刀"（不重复它的职责）。
        """
        rc, out = self._runner(argv)
        text = _ascii_text(out)
        for line in text.splitlines():
            self._log("CARRIER_UPDATER_OUT=%s" % line[:200])
        if rc == 0 and "STRATEGY_UPDATE_RESULT=OK" in text:
            return True, "", text
        if "STRATEGY_UPDATE_RESULT=ABORT" in text:
            return False, "updater_abort", text
        return False, "updater_rc_%d" % rc, text

    # ---------------- 回执 ----------------
    def post_ack(self, strategy_version, executor_version, ok, reason):
        """POST 回执：字段名与 brain 侧一字不差（四键），任何异常只让回执失败，不改本轮结论。"""
        body = json.dumps({
            "strategy_version": _ascii_text(strategy_version),
            "executor_version": _ascii_text(executor_version),
            "ok": bool(ok),
            "reason": _ascii_text(reason)[:200],
        }, ensure_ascii=True).encode("utf-8")
        status, _ = self._request("POST", self.base_url.rstrip("/") + ACK_PATH, body)
        self._log("CARRIER_ACK_POST status=%d ok=%d" % (status, 1 if ok else 0))
        return status

    # ---------------- 一轮 ----------------
    def run_once(self, dry_run=False):
        """完整一轮，返回 {"status","version","sha256","reason"}（status ∈ OK/DRYRUN/REFUSED/FAILED）。"""
        rep = {"status": "FAILED", "version": "unknown", "sha256": "unknown", "reason": "not_run"}
        manifest = self.fetch_manifest()
        if manifest is None:
            # 读不到清单＝这一轮什么都不知道；"不知道"绝不能被写成"已经一致"（本仓最重复的缺陷）。
            rep.update(reason="manifest_unreadable:" + (self.manifest_error or "unknown"),
                       status="REFUSED")
            self._finish(rep, dry_run, acked=False)
            return rep
        if manifest.get("ok") is not True:
            # brain 侧读不到期望态时回的是 ok=false + unknown_reason（永不回"已是最新"），
            # 那个原因必须原样出现在盒子这一轮里——否则"清单没填齐"和"盒子连不上清单"两种红
            # 会被读成同一件事，处置方向完全不同（一边改发布，一边查网络）。
            why = _ascii_text(manifest.get("unknown_reason") or "no_reason_given")[:80]
            self._log("CARRIER_MANIFEST_NOT_OK unknown_reason=%s request_state=%s"
                      % (why, _ascii_text(manifest.get("request_state") or "unknown")))
            rep.update(reason="manifest_not_ok:" + why.replace(" ", "_"), status="REFUSED")
            self._finish(rep, dry_run, acked=False)
            return rep

        want_version = str(manifest.get("strategy_version") or "").strip()
        want_sha = str(manifest.get("strategy_sha256") or "").strip().lower()
        if not want_version or not want_sha:
            # 清单在但版本/哈希缺：仍是"无法判定"，与"无需更新"两种读数，不许折成一个。
            rep.update(reason="manifest_incomplete_no_version_or_sha", status="REFUSED")
            self._finish(rep, dry_run, acked=False)
            return rep

        rep["version"] = want_version
        rep["sha256"] = want_sha
        request_state = str(manifest.get("request_state") or "unknown").strip() or "unknown"
        self._log("CARRIER_MANIFEST ok=1 strategy_version=%s strategy_sha256=%s "
                  "executor_version=%s updated_at=%s request_state=%s requested_at=%s"
                  % (want_version, want_sha,
                     _ascii_text(manifest.get("executor_version") or "unknown"),
                     _ascii_text(manifest.get("updated_at") or "unknown"),
                     request_state, _ascii_text(manifest.get("requested_at") or "none")))

        have_sha = self.installed_sha256()
        have_ver = self.installed_version()
        # 两个哈希都要打出来：只打"该装的那份"会让值班的人无从比对（J0 那批的同一条纪律）。
        self._log("CARRIER_INSTALLED sha256=%s version=%s file=%s"
                  % (have_sha or "unreadable", have_ver or "unreadable",
                     "set" if self.strategy_file else "unset"))
        if not have_sha:
            # 已安装那份读不到＝比不了；这**不是**"没装上"也不是"已一致"，如实报无法判定。
            rep.update(reason="installed_strategy_unreadable", status="REFUSED")
            self._finish(rep, dry_run, acked=False)
            return rep

        if have_sha == want_sha:
            # 已经一致：不动盘、不下载、不投递。人工点过（request_state=pending）时仍要回执，
            # 否则那次点击永远停在"等盒子回答"——回执是答话，不是动作。预览模式一个字节都不回。
            self._note_executor_diff(manifest)
            reason = "up_to_date"
            acked = False
            if request_state == "pending":
                reason = "up_to_date_ack_requested"
                if not dry_run:
                    self.post_ack(have_ver or want_version, self._executor_ack_version(manifest),
                                  True, reason)
                    acked = True
            rep.update(status="OK", reason=reason)
            self._finish(rep, dry_run, acked=acked)
            return rep

        if self.marker_present():
            # 提前拒并把原因说明白（更新脚本自己也会拒——那是第二道，不是第一道）。
            # 拒也要回话：这块牌是人工才能清的状态，让运营点击停在"等盒子回答"上
            # 只会逼人再点一次，而再点一次结果一样。
            rep.update(reason="stop_auto_update_marker_present", status="REFUSED")
            self._note_executor_diff(manifest)
            if not dry_run:  # 预览不回执：回执会把别人那格 pending 吃掉，预览必须零副作用
                self.post_ack(want_version, self._executor_ack_version(manifest), False, rep["reason"])
            self._finish(rep, dry_run, acked=not dry_run)
            return rep

        if dry_run:
            # 预览：一个字节都不下载、不投递、不回执（回执会把别人的 pending 状态吃掉）。
            rep.update(reason="update_needed_dryrun", status="DRYRUN")
            self._note_executor_diff(manifest)
            self._finish(rep, dry_run, acked=False)
            return rep

        # 到这里才是"确实要换"：先校验目录与时机，再取字节、验哈希、交给更新脚本。
        gate = self._apply_gates()
        if gate:
            rep.update(reason=gate, status="REFUSED")
            self.post_ack(want_version, self._executor_ack_version(manifest), False, gate)
            self._finish(rep, dry_run, acked=True)
            return rep

        staged, stage_reason = self.stage_artifact(manifest)
        if not staged:
            rep.update(reason=stage_reason, status="FAILED")
            self.post_ack(want_version, self._executor_ack_version(manifest), False, stage_reason)
            self._finish(rep, dry_run, acked=True)
            return rep

        got_sha = _sha256_file(staged)
        self._log("CARRIER_DOWNLOAD path=%s sha256=%s expected=%s match=%d"
                  % (staged, got_sha or "unreadable", want_sha, 1 if got_sha == want_sha else 0))
        if got_sha != want_sha:
            # 哈希对不上：这份字节**不许**进活路径，更新脚本一次都不调（未验字节零写入）。
            self._remove_staged(staged)
            rep.update(reason="downloaded_sha_mismatch", status="FAILED")
            self.post_ack(want_version, self._executor_ack_version(manifest), False, "downloaded_sha_mismatch")
            self._finish(rep, dry_run, acked=True)
            return rep

        # 哈希对上了才把临时文件交给投递脚本；脚本返回后再读一次盘上的实际字节，
        # 回执报的是"这台机器上现在真装着什么"，不是"清单说该装什么"。
        ok, fail_reason, _out = self.run_updater(self.updater_argv(staged, manifest))
        self._remove_staged(staged)
        now_sha = self.installed_sha256()
        now_ver = self.installed_version()
        ack_version = now_ver or want_version
        self._note_executor_diff(manifest)
        if ok and now_sha and now_sha.lower() != want_sha:
            # 脚本自己说 OK 但盘上字节不是清单那一份：这正是"以为跑的是 v12、实际是 v07"，
            # 所以本模块认的是**盘上字节**，不是脚本的自述（IMPL §B3 那条命门的盒子侧半边）。
            rep.update(status="FAILED", reason="post_apply_sha_mismatch",
                       version=now_ver or want_version, sha256=now_sha)
            self.post_ack(ack_version, self._executor_ack_version(manifest), False, rep["reason"])
            self._finish(rep, dry_run, acked=True)
            return rep
        if ok and not now_sha:
            rep.update(status="FAILED", reason="post_apply_installed_unreadable")
            self.post_ack(ack_version, self._executor_ack_version(manifest), False, rep["reason"])
            self._finish(rep, dry_run, acked=True)
            return rep
        if ok:
            rep.update(status="OK", version=now_ver or want_version,
                       sha256=now_sha or want_sha, reason="applied")
            self.post_ack(ack_version, self._executor_ack_version(manifest), True, "applied")
        else:
            rep.update(status="FAILED", reason=fail_reason)
            self.post_ack(ack_version, self._executor_ack_version(manifest), False, fail_reason)
        self._finish(rep, dry_run, acked=True)
        return rep

    # ---------------- 一轮的收尾与护栏 ----------------
    def _apply_gates(self):
        """动手前的三道结构性闸：目录、更新脚本、临时目录不许落在 QMT 目录里。"""
        if not self.qmt_python_dir or not os.path.isdir(self.qmt_python_dir):
            return "qmt_python_dir_absent"
        if not self.updater_script or not os.path.exists(self.updater_script):
            return "updater_script_absent"
        if not os.path.exists(self.ps_exe):
            return "ps_exe_absent"
        if _is_within(self.temp_dir, self.qmt_python_dir):
            # 临时产物在 QMT 目录里＝客户端可能把它当成策略加载；这是结构性错误，不是偏好。
            return "temp_dir_inside_qmt_dir"
        return ""

    def _executor_ack_version(self, manifest):
        """回执里的执行器版本：读**本机装好的那份**；读不到就明确说 unknown（不猜、不拿清单值冒充）。"""
        if self.executor_file:
            ver = report_uplink.read_version_constant(self.executor_file)
            if ver:
                return ver
        return "unknown"

    def _note_executor_diff(self, manifest):
        """执行器只报不投：清单说要的那份与本机装的不一样时点名，绝不折进"已一致"。"""
        want = str(manifest.get("executor_sha256") or "").strip().lower()
        if not want:
            self._log("CARRIER_EXECUTOR reason=manifest_has_no_executor_sha")
            return
        have = _sha256_file(self.executor_file) if self.executor_file else ""
        if not have:
            self._log("CARRIER_EXECUTOR state=unreadable installed=none expected=%s" % want[:16])
            return
        if have.lower() != want:
            self._log("CARRIER_EXECUTOR state=differs_not_applied installed=%s expected=%s" % (have[:16], want[:16]))
        else:
            self._log("CARRIER_EXECUTOR state=matches")

    def _remove_staged(self, path):
        """只删**自己刚写的那份临时文件**（活路径与别人的文件一律不碰，也不碰 data.db）。"""
        try:
            if path and os.path.isfile(path) and _is_within(path, self.temp_dir):
                os.remove(path)
        except OSError:
            pass  # 删不掉只是留了一份临时件，绝不因此把结论改掉

    def _log(self, line):
        """所有出话都过 ASCII 这道，且永不打印钥匙。"""
        print(_ascii_text(line))

    def _finish(self, rep, dry_run, acked):
        """打最后那一行人机共读的行，并把这一轮写进状态（dry-run 一个字节都不写）。"""
        self._log("CARRIER_UPDATE_RESULT=%s version=%s sha256=%s reason=%s"
                  % (rep["status"], rep["version"] or "unknown", rep["sha256"] or "unknown", rep["reason"]))
        if dry_run:
            return
        row = dict(rep)
        row["at"] = report_uplink._iso_beijing(self.now_fn())
        row["acked"] = bool(acked)
        # 状态文件里没有 token 这个键——结构上就不给（用例逐字节反查）。
        try:
            self._save_state(row)
        except OSError as exc:
            self._log("CARRIER_STATE reason=write_failed:%s" % exc.__class__.__name__)


def _subprocess_runner(argv):
    """真实执行腿：更新脚本与本地构建都走这一条。异常收敛成 rc=1（并把文本压成 ASCII）。"""
    try:
        proc = subprocess.run(list(argv), capture_output=True, text=True)
        return int(proc.returncode), (proc.stdout or "") + (proc.stderr or "")
    except Exception as exc:  # noqa: BLE001 -- 起不来也是结论，不是崩
        return 1, "runner_exception:%s" % exc.__class__.__name__


def _host_of(url):
    """只取 host:port 用于打印（路径/查询/凭证一概不出）。"""
    return report_uplink._host_of(url)


# ---------------------------------------------------------------------------
# 命令行入口：与 report_uplink 同一套边界（钥匙只在内存、dry-run 零写、无模式即拒跑）。
#   python carrier_updater.py --config C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json --once
#   python carrier_updater.py --config ... --once --dry-run
# 每日收盘后那一跑由计划任务 QMTF-Carrier-Update 调 --once（15:40，owner 裁决 2）；
# 运营在量仔页面上点一下，则由下一次 --once 把 request_state=pending 那格接住。
# ---------------------------------------------------------------------------
def _run_cli(argv, transport=None, runner=None):
    """真正的命令行一轮（main 就调它；transport/runner 只有测试会传，生产一律用默认腿）。"""
    import argparse
    ap = argparse.ArgumentParser(prog="carrier_updater",
                                 description="box-side carrier update poller")
    ap.add_argument("--config", required=True, help="path to config.xt.json (same source as the trade leg)")
    ap.add_argument("--once", action="store_true", help="run exactly one round and exit")
    ap.add_argument("--dry-run", action="store_true", dest="dry_run",
                    help="compare and print only: no download, no apply, no ack")
    args = ap.parse_args(argv)

    try:
        updater = CarrierUpdater(args.config, transport=transport, runner=runner)
    except Exception as exc:  # noqa: BLE001 -- 配置读不到要响亮地失败，但绝不带出凭证内容
        print("CARRIER_UPDATE_RESULT=FAILED version=unknown sha256=unknown "
              "reason=config_unreadable:%s" % exc.__class__.__name__)
        return 2

    print("CARRIER_CONFIG base_host=%s token_present=%d strategy_file=%s updater_script=%s "
          "qmt_python_dir=%s temp_dir=%s state=%s mode=%s"
          % (_host_of(updater.base_url), 1 if updater.token else 0,
             "set" if updater.strategy_file else "unset",
             "set" if updater.updater_script else "unset",
             "set" if updater.qmt_python_dir else "unset",
             updater.temp_dir, os.path.basename(updater.state_path),
             "dry_run" if args.dry_run else "once"))

    if not args.once and not args.dry_run:
        # 缺省动作是"什么都不做"：一条会自己动手的腿在没有旗标时被误跑一次，代价是重启交易客户端。
        print("CARRIER_UPDATE_RESULT=REFUSED version=unknown sha256=unknown reason=no_mode_given_use_once")
        return 2

    rep = updater.run_once(dry_run=args.dry_run)
    if rep["status"] in ("OK", "DRYRUN"):
        return 0
    return 1


def main(argv=None):
    return _run_cli(argv)


if __name__ == "__main__":
    sys.exit(main())
