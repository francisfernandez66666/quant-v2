#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""carrier_hot_reload_gz.py -- 战法白名单热更新链的**云端原生版**（方案 A，owner 2026-11-10 17:27）。

为什么存在：Mac launchd 版（carrier_hot_reload.sh）依赖 Mac 开机；Win 盒子版要把
发版权放进券商豁免机器。本脚本把整条链搬到**永远在线的 gz** 上，纯 stdlib、零 ssh：

    读本机 auth.json 白名单/审批因子 → sha 比对（无变化幂等退出）
    → 重建两份快照 → sync_strategy_switches/sync_live_rules --write（漂移自动 ABORT）
    → Q3 利空归因（读本机 signals_today.json，空=保留上一版，绝不静默清空）
    → gen_carrier_risk_list 合并 → 烘载体（9 闸在 build_local_executor 里）
    → 本地发布：产物拷 /opt/quant/friend-pkg/carrier/ + 写 carrier_manifest.json
    → 盒子既有 15:40 carrier_updater 按 sha 拉取。

烘焙子集从仓库 rsync 到 BUILD_DIR（scripts/rsync_carrier_build.sh 维护同源）；
本脚本只相信 BUILD_DIR 里那份参数与源码，绝不动态改代码。

用法（root 运行，systemd timer 每天 08:30 / 15:10 各一次）：
    python3 /opt/quant/carrier_build/scripts/carrier_hot_reload_gz.py [--force]
判读锚点：HOTRELOAD_GZ_NOTHING_TO_DO / HOTRELOAD_GZ_PUBLISHED version= / HOTRELOAD_GZ_ABORT reason=
"""

import argparse
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys

BUILD_DIR = os.environ.get("CARRIER_BUILD_DIR", "/opt/quant/carrier_build")
DATA_DIR = os.environ.get("QUANT_DATA_DIR", "/var/lib/quant-trading-v2")
OWNER_UID = os.environ.get("OWNER_UID", "u_1785377969825355000")  # admin，物理机账号
CARRIER_DIR = os.environ.get("CARRIER_DIR", "/opt/quant/friend-pkg/carrier")
STATE = os.environ.get("HOTRELOAD_STATE", "/var/lib/quant-trading-v2/carrier_hotreload_gz.sha")
BASE_URL = os.environ.get("BASE_URL", "https://quant-trading.top")

AUTH = os.path.join(DATA_DIR, "auth.json")
APPLIED = os.path.join(DATA_DIR, "applied_factors.json")
# §UNIVERSE-20261112：引擎自 §W-03 起按账号隔离落盘（accounts/<uid>/signals_today.json，
# registry.go「每账号独立持久化目录」），根目录那份是 2026-08-14 的老全局文件——
# 热更新链一直读旧全局文件，监视池因此永远只有 ~123 只（11-12 盒子实锤 universe=123）。
# 修复：读 OWNER（admin，物理机账号）账号目录的当日文件；缺失时回退根目录旧文件
# （总比空池好，且 extract_* 对旧日代码同样只做「扩池不买入」）。
SIGNALS_TODAY = os.path.join(DATA_DIR, "accounts", OWNER_UID, "signals_today.json")
if not os.path.exists(SIGNALS_TODAY):
    SIGNALS_TODAY = os.path.join(DATA_DIR, "signals_today.json")

SHORT_STRATEGY_TYPES = ("high_churn", "break_down", "leader_decay", "leader_decay_v2",
                        "good_news_fade")
# 利好池参数（owner 2026-11-10 18:42：利好/d1/n 形打分也烘进载体做机会池；
# 进池只拓宽规则"可看"的票，买入仍要过全部 12 闸，不发明新下单语义）。
BULLISH_MIN_SCORE = float(os.environ.get("BULLISH_MIN_SCORE", "60"))
HOT_POOL_MAX = int(os.environ.get("HOT_POOL_MAX", "30"))
SIGNAL_POOL_MAX = int(os.environ.get("SIGNAL_POOL_MAX", "600"))  # owner：监视池上限 600


def die(reason):
    print("HOTRELOAD_GZ_ABORT reason=%s" % reason)
    sys.exit(1)


def norm(code):
    c = str(code or "").strip().upper()
    for suf in (".SZ", ".SH", ".BJ"):
        if c.endswith(suf):
            c = c[: -len(suf)]
    return c


def load_live():
    """auth.json 的 quant_config_json_v1（OWNER 账号）+ 审批因子；只读。"""
    with open(AUTH, "r", encoding="utf-8") as fh:
        d = json.load(fh)
    cfg = [c for c in d.get("configs", [])
           if c.get("key") == "quant_config_json_v1" and c.get("user_id") == OWNER_UID]
    if not cfg:
        die("owner_config_missing")
    qc = json.loads(cfg[0]["value"]) if isinstance(cfg[0]["value"], str) else cfg[0]["value"]
    with open(APPLIED, "r", encoding="utf-8") as fh:
        af = json.load(fh)
    rules = af if isinstance(af, list) else af.get("rules", [])
    return qc, rules


def extract_bullish():
    """signals_today.json 的做多高分信号 → 机会池 {code:{score,source}}（去重取最高分）。"""
    pool = {}
    try:
        with open(SIGNALS_TODAY, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        data = None
    rows = data if isinstance(data, list) else (data or {}).get("signals") or []
    for s in rows:
        if not isinstance(s, dict):
            continue
        if str(s.get("direction") or "") in ("做空", "利空"):
            continue
        conf = s.get("confidence")
        try:
            score = round(float(conf) * 100.0)
        except (TypeError, ValueError):
            continue
        if score < BULLISH_MIN_SCORE:
            continue
        c = norm(s.get("code"))
        if not (len(c) == 6 and c.isdigit()):
            continue
        src = str(s.get("strategy_type") or s.get("strategyType")
                  or s.get("strategy") or "scan")
        if c not in pool or score > pool[c]["score"]:
            pool[c] = {"score": score, "source": src[:24]}
    return pool


def extract_signal_codes():
    """signals_today.json 里**全部**信号代码（不限方向）→ 信号监视池来源。
    owner 2026-11-13:36 需求：个股监视（信号监视）池上限扩到 600，随信号自动扩池，
    不再烘死在自选清单里。English: all signal codes feed the carrier scan universe.
    """
    codes = []
    try:
        with open(SIGNALS_TODAY, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        data = None
    rows = data if isinstance(data, list) else (data or {}).get("signals") or []
    for s in rows:
        if not isinstance(s, dict):
            continue
        c = norm(s.get("code"))
        if len(c) == 6 and c.isdigit():
            codes.append(c)
    return sorted(set(codes))


def extract_score_pool():
    """当日评分榜全量代码（accounts/<OWNER>/scores_fast.json 的 scores keys）→ 监视池来源。

    owner 2026-11-12：监视池要 600 只——当日信号只有 ~100 只，池子凑不满。
    scores_fast 是引擎当日实算的全市场活跃池（~593 只、trading_day 必须当日才采信），
    并入后监视池直达 SIGNAL_POOL_MAX=600。扩池只加「看得见」：每一票仍过 QMT 载体
    12 闸（确认窗/VWAP/集中度/1万闸/熔断…），评分榜高分本身不构成任何买入理由。
    English: merge today's scored universe into the carrier scan pool, capped at 600.
    """
    try:
        path = os.path.join(DATA_DIR, "accounts", OWNER_UID, "scores_fast.json")
        with open(path, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return []
    today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    if str(doc.get("trading_day") or "") != today:
        return []  # 非当日评分榜绝不留宿（与 load_precalc 同一纪律）
    out = []
    for c in (doc.get("scores") or {}):
        if isinstance(c, str) and len(c) == 6 and c.isdigit():
            out.append(c)
    return sorted(set(out))


def load_precalc():
    """读当日预计算因子文件（§PRECALC）。返回 (meta, precalc_dict)：非当日/缺失 ⇒ (空meta, None)。
    过期因子绝不留宿：宁可盒内弃权，也不拿昨天的量比当今天的。"""
    try:
        with open("qmt_gateway/local_executor_precalc.json", "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return {"day": "", "count": 0}, None
    today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    if doc.get("day") != today:
        return {"day": str(doc.get("day") or ""), "count": 0}, None
    precalc = doc.get("precalc") or {}
    return {"day": doc.get("day"), "count": len(precalc)}, precalc


def extract_bearish():
    """signals_today.json 的做空/利空归因 → 当日利空候选（空=保留上一版）。"""
    codes = []
    try:
        with open(SIGNALS_TODAY, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        data = None
    rows = data if isinstance(data, list) else (data or {}).get("signals") or []
    for s in rows:
        if not isinstance(s, dict):
            continue
        if (str(s.get("direction") or "") in ("做空",)
                or str(s.get("alert_type") or s.get("alertType") or "") in ("做空", "利空")
                or str(s.get("strategy_type") or s.get("strategyType") or "") in SHORT_STRATEGY_TYPES):
            c = norm(s.get("code"))
            if len(c) == 6 and c.isdigit():
                codes.append(c)
    return sorted(set(codes))


def scrub(o):
    """凭据段剔除 + 非 ASCII 拒绝（gate_ascii：产物必须逐字节纯 ASCII）。"""
    if isinstance(o, dict):
        return {k: scrub(v) for k, v in o.items()
                if not any(t in str(k).lower() for t in ("token", "password", "secret", "api_key"))}
    if isinstance(o, list):
        return [scrub(v) for v in o]
    if isinstance(o, str):
        if any(ord(ch) > 127 for ch in o):
            raise ValueError("non_ascii_in_snapshot")
        return o
    return o


def rebuild_snapshots(qc, rules, wl):
    """重建两份快照（与 Mac 版同构；写 BUILD_DIR 里的仓库路径）。"""
    qg = os.path.join(BUILD_DIR, "qmt_gateway")
    now = datetime.datetime.now().astimezone().isoformat(timespec="seconds")
    by = {r["id"]: r for r in rules if isinstance(r, dict) and r.get("id")}
    sel = [by[i] for i in wl if i in by]

    d2 = json.load(open(os.path.join(qg, "strategy_switches_snapshot.json"), encoding="utf-8"))
    d2["enabled_strategies"] = wl
    d2["factor_rules"] = sel
    d2["captured_at"] = now
    d2["source"] = "gz auth.json+applied_factors.json (carrier_hot_reload_gz)"
    # §OWNER-SCOPE-20261112：下发载荷显式带归属账号，盒子回传时按同一 uid 对齐，
    # 杜绝「一批白名单/参数被别的账号的物理机领走并回报到别人账上」。
    d2["owner_user_id"] = OWNER_UID
    d2["user_id"] = OWNER_UID
    json.dump(d2, open(os.path.join(qg, "strategy_switches_snapshot.json"), "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)

    snap = {
        "captured_at": now,
        "source": "gz:/var/lib/quant-trading-v2/auth.json (carrier_hot_reload_gz)",
        "owner_user_id": OWNER_UID,   # §OWNER-SCOPE-20261112 交易权限分发的归属账号
        "user_id": OWNER_UID,
        "configs": [{"user_id": OWNER_UID, "key": "quant_config_json_v1",
                     "has_qmt_strategies": True, "rules": scrub(qc)}],
        "applied_factor_ids": [r.get("id") for r in rules],
    }
    json.dump(snap, open(os.path.join(qg, "live_rules_snapshot.json"), "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)
    return sel


def run(cmd, cwd=None):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    tail = (r.stdout + r.stderr).strip().splitlines()
    print("  RUN %s rc=%d %s" % (" ".join(cmd[-2:]), r.returncode, tail[-1] if tail else ""))
    return r


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true", help="skip sha compare")
    args = ap.parse_args()

    if not os.path.isdir(BUILD_DIR):
        die("build_dir_missing:" + BUILD_DIR)
    os.chdir(BUILD_DIR)

    # §B 自动补推（2026-11-11 owner 要求）：precalc 缺当日 / 覆盖率<80% ⇒ 自动补跑一次
    # pregen（限频：每小时最多一次，重试时间戳落 precalc_retry_ts），失败不阻断热更
    # （pregen 自带 ntfy 通知与连续失败计数）。
    try:
        st_path = "qmt_gateway/precalc_status.json"
        st = {}
        if os.path.exists(st_path):
            with open(st_path, "r", encoding="utf-8") as fh:
                st = json.load(fh)
        today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
        stale = st.get("day") != today or int(st.get("coverage") or 0) < 80
        retry_ts_path = "qmt_gateway/precalc_retry_ts"
        last_retry = 0.0
        if os.path.exists(retry_ts_path):
            with open(retry_ts_path, "r", encoding="utf-8") as fh:
                last_retry = float(fh.read().strip() or 0)
        now_ts = datetime.datetime.now().astimezone().timestamp()
        if stale and now_ts - last_retry > 3600:
            r = subprocess.run(["python3", "scripts/pregen_carrier_factors_gz.py"],
                               cwd=BUILD_DIR, capture_output=True, text=True, timeout=900)
            with open(retry_ts_path, "w", encoding="utf-8") as fh:
                fh.write(str(now_ts))
            tail = (r.stdout + r.stderr).strip().splitlines()
            print("HOTRELOAD_DATARETRY rc=%d %s" % (r.returncode, tail[0] if tail else ""))
    except Exception as exc:  # noqa: BLE001 - 补推失败不阻断热更
        print("HOTRELOAD_DATARETRY_FAIL %s" % repr(exc)[:100])

    qc, rules = load_live()
    wl = (qc.get("qmt", {}) or {}).get("strategies") or []
    if not wl:
        die("whitelist_empty")
    # sha 载荷必须包含当日利空归因（owner 17:35 每半小时一烘的配套）：否则
    # 「归因变了但白名单没变」会被 sha 闸拦成 NOTHING_TO_DO，利空清单永远烘不进载体。
    bearish = extract_bearish()
    bullish = extract_bullish()
    signal_pool = extract_signal_codes()
    precalc_meta, _precalc = load_precalc()
    # §BUCKETS-SHA-20261112：行业/概念桶内容纳入 sha——桶由 pregen 的回填腿维护，
    # 桶变化必须触发重烘投递（否则 params 变了产物不变，盒子上永远拿不到桶）。
    # 注意：本脚本没有 PARAMS_PATH 常量（那是 pregen 的），params 走 BUILD_DIR 相对
    # CWD 的路径（与 331 行 doc2 的读法同一份）。
    try:
        _bp = json.load(open(os.path.join(BUILD_DIR, "qmt_gateway",
                                           "local_executor_params.json"),
                              encoding="utf-8")).get("params") or {}
    except (OSError, ValueError):
        _bp = {}
    payload = json.dumps({"wl": wl, "rules": [r.get("id") for r in rules],
                          "bearish": bearish, "bullish": bullish,
                          "signal_pool": signal_pool, "precalc": precalc_meta,
                          "buckets": {"industry": len(_bp.get("industry_map") or {}),
                                      "concept": len(_bp.get("concept_map") or {}),
                                      "digest": hashlib.sha256(json.dumps(
                                          {"i": sorted((_bp.get("industry_map") or {}).items())[:4000],
                                           "c": sorted((_bp.get("concept_map") or {}).items())[:4000]},
                                          ensure_ascii=True).encode()).hexdigest()[:16]}}, ensure_ascii=True)
    sha = hashlib.sha256(payload.encode()).hexdigest()

    if not args.force and os.path.exists(STATE) and open(STATE).read().strip() == sha:
        print("HOTRELOAD_GZ_NOTHING_TO_DO sha=%s" % sha)
        return 0
    print("HOTRELOAD_GZ payload sha=%s wl=%d bearish=%d bullish=%d" % (
        sha, len(wl), len(bearish), len(bullish)))

    # 1) 快照重建 + 双派生（漂移各自 ABORT，绝不带病烘参）
    try:
        rebuild_snapshots(qc, rules, wl)
    except ValueError as exc:
        die(str(exc))
    for cmd in (["python3", "scripts/sync_strategy_switches.py", "--write"],
                ["python3", "scripts/sync_live_rules.py", "--write"]):
        r = run(cmd)
        if r.returncode != 0:
            die("sync_failed:" + cmd[1])

    # 2) Q3 利空归因：候选已在 sha 前提取，这里落盘合并（空=保留上一版）
    with open("qmt_gateway/risk_list_llm.json", "w", encoding="utf-8") as fh:
        json.dump(bearish, fh, ensure_ascii=True)
    r = run(["python3", "scripts/gen_carrier_risk_list.py"])
    if r.returncode != 0:
        die("risklist_failed")

    # 2b) 利好机会池烘参：top-N 高分票进 hot_pool（ASCII 键值，进产物前过 scrub 同规）。
    top = sorted(bullish.items(), key=lambda kv: -kv[1]["score"])[:HOT_POOL_MAX]
    doc2 = json.load(open("qmt_gateway/local_executor_params.json", encoding="utf-8"))
    doc2["params"]["hot_pool"] = {
        "bullish": dict(top),
        "updated_at": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
    }
    # 2026-11-11 owner 需求：个股监视（信号监视）池上限扩到 600——扫描池 = 原自选 ∪
    # 当日全部信号 ∪ 利好池，去重排序后截断到 SIGNAL_POOL_MAX。只拓"可看"的票，
    # 买入仍过全部 12 闸。QMT 用 get_full_tick 逐票拉取（非订阅），无订阅上限风险。
    pool = set(doc2["params"].get("symbols") or []) | set(signal_pool) | set(bullish) | set(extract_score_pool())
    doc2["params"]["symbols"] = sorted(pool)[:SIGNAL_POOL_MAX]
    # §PRECALC（2026-11-11 owner 拍板）：盘前预计算因子并入 params——载体在缺历史序列的
    # 构建上用 precalc 替代 get_market_data_ex（均线参考/量比基线/近高近低），只有当日新鲜
    # 的文件才并入；并入与否都计入 sha（因子日变即重烘）。
    precalc_meta, precalc = load_precalc()
    if precalc:
        doc2["params"]["precalc"] = precalc
    else:
        doc2["params"].pop("precalc", None)  # 过期因子绝不留宿：宁可弃权也不拿昨天的量比当今天的
    json.dump(doc2, open("qmt_gateway/local_executor_params.json", "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)

    # 3) 版本号 +1 自动 bump（vNN-YYYYMMDD）。§VER-COLLISION-20261112：out_dir 里
    # 历史产物会按字典序被 sorted()[-1] 错拿（v18>v17 ⇒ 11-12 实锤 pool=600 新产物
    # 被丢、发布的还是旧 v18 文件——AGENTS「11-11 v14/v15/v17 混名事故」的代码位置）。
    # 修复：烘焙前把版本 bump 到「out_dir 里还不存在」的唯一值；发布只取本次版本名。
    params_path = "qmt_gateway/local_executor_params.json"
    out_dir = "/var/lib/quant-trading-v2/carrier_build_out"
    doc = json.load(open(params_path, encoding="utf-8"))
    cur = doc.get("strategy_version") or "v00-19700101"
    nn = int(str(cur).split("-")[0][1:])
    day = datetime.date.today().strftime("%Y%m%d")
    ver = "v%02d-%s" % (nn + 1, day)
    while os.path.exists(os.path.join(out_dir, "qmt_internal_strategy_%s.py" % ver)):
        nn += 1
        ver = "v%02d-%s" % (nn + 1, day)
    doc["strategy_version"] = ver
    json.dump(doc, open(params_path, "w", encoding="utf-8"), ensure_ascii=True, indent=2)

    # 4) 烘（9 闸）。--out-dir 必须在仓库外（构建器有 out_dir_inside_repository 守卫）。
    out_dir = "/var/lib/quant-trading-v2/carrier_build_out"
    os.makedirs(out_dir, exist_ok=True)
    r = run(["python3", "scripts/build_local_executor.py", "--target", "sandbox",
             "--out-dir", out_dir])
    if r.returncode != 0:
        die("bake_failed2 version=%s" % ver)
    arts = [f for f in os.listdir(out_dir) if f.startswith("qmt_internal_strategy_")]
    if not arts:
        die("artifact_missing")
    art_path = os.path.join(out_dir, sorted(arts)[-1])
    sha_art = hashlib.sha256(open(art_path, "rb").read()).hexdigest()

    # 5) 本地发布：产物 + manifest（与 publish_carrier.sh 的三值同源语义一致）
    os.makedirs(CARRIER_DIR, exist_ok=True)
    dest = os.path.join(CARRIER_DIR, "qmt_internal_strategy_%s.py" % ver)
    shutil.copyfile(art_path, dest)
    manifest_dir = os.path.join(DATA_DIR, "accounts", OWNER_UID)
    os.makedirs(manifest_dir, exist_ok=True)
    manifest = {
        "strategy_version": ver,
        "strategy_sha256": sha_art,
        # §OWNER-SCOPE-20261112：manifest 直接落到 accounts/<OWNER_UID>/ 且带 uid，
        # 盒子拉取后校验「这批载体参数属于哪个账号」，只允许该账号的柜台回报回写。
        "owner_user_id": OWNER_UID,
        "user_id": OWNER_UID,
        "artifact_url": "%s/leg/carrier/qmt_internal_strategy_%s.py" % (BASE_URL, ver),
        "updated_at": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "request_state": "pending",
        "mode": "live",
    }
    json.dump(manifest, open(os.path.join(manifest_dir, "carrier_manifest.json"), "w",
                             encoding="utf-8"), ensure_ascii=True, indent=2)

    with open(STATE, "w") as fh:
        fh.write(sha)
    print("HOTRELOAD_GZ_PUBLISHED version=%s sha=%s wl=%d bearish=%d bullish=%d pool=%d" % (
        ver, sha_art, len(wl), len(bearish), len(top), len(doc2["params"].get("symbols") or [])))
    return 0


if __name__ == "__main__":
    sys.exit(main())
