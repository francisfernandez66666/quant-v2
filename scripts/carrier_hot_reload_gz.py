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
    json.dump(d2, open(os.path.join(qg, "strategy_switches_snapshot.json"), "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)

    snap = {
        "captured_at": now,
        "source": "gz:/var/lib/quant-trading-v2/auth.json (carrier_hot_reload_gz)",
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

    qc, rules = load_live()
    wl = (qc.get("qmt", {}) or {}).get("strategies") or []
    if not wl:
        die("whitelist_empty")
    # sha 载荷必须包含当日利空归因（owner 17:35 每半小时一烘的配套）：否则
    # 「归因变了但白名单没变」会被 sha 闸拦成 NOTHING_TO_DO，利空清单永远烘不进载体。
    bearish = extract_bearish()
    bullish = extract_bullish()
    signal_pool = extract_signal_codes()
    payload = json.dumps({"wl": wl, "rules": [r.get("id") for r in rules],
                          "bearish": bearish, "bullish": bullish,
                          "signal_pool": signal_pool}, ensure_ascii=True)
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
    pool = set(doc2["params"].get("symbols") or []) | set(signal_pool) | set(bullish)
    doc2["params"]["symbols"] = sorted(pool)[:SIGNAL_POOL_MAX]
    json.dump(doc2, open("qmt_gateway/local_executor_params.json", "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)

    # 3) 版本号 +1 自动 bump（vNN-YYYYMMDD）
    params_path = "qmt_gateway/local_executor_params.json"
    doc = json.load(open(params_path, encoding="utf-8"))
    cur = doc.get("strategy_version") or "v00-19700101"
    nn = int(str(cur).split("-")[0][1:]) + 1
    ver = "v%02d-%s" % (nn, datetime.date.today().strftime("%Y%m%d"))
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
