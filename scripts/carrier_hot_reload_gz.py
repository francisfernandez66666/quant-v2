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
import base64
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

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
# §INTRADAY-BUYSIG：上次**发布**时的 buy 信号集合（差分基准；发布腿写回）。
BUY_STATE_PATH = os.path.join(BUILD_DIR, "qmt_gateway", "buy_signals_state.json")
if not os.path.exists(SIGNALS_TODAY):
    SIGNALS_TODAY = os.path.join(DATA_DIR, "signals_today.json")
# §NEWS-GATE 2026-11-12（R10/R11/R12）：脑侧资讯闸快照——烘焙唯一真源（取代旧
# extract_bearish/bullish 的 signals 筛选）。文件由引擎每轮主循环刷新（day-keyed 原子写）。
NEWS_GATE_TODAY = os.path.join(DATA_DIR, "accounts", OWNER_UID, "news_gate_today.json")
HOTRELOAD_DB = os.environ.get("HOTRELOAD_DB", "/var/lib/quant-trading-v2/trading.db")
# §LIVE-T-20261116：做 T 的锚点与宏观读数只存在于实盘库和引擎账本。
# LIVE_DB 是首买锚点（real_positions.anchor_price/anchor_source，签字回填工具写入）的唯一真源；
# 与 HOTRELOAD_DB（trading.db 研究库）刻意分开——锚点在 trading.db 里恒为空（实测）。
LIVE_DB = os.environ.get("LIVE_DB", "/var/lib/quant-trading-v2/live.db")
T_ROUNDS_LIVE = os.path.join(DATA_DIR, "accounts", OWNER_UID, "t_rounds_live.json")
# T 腿短码锚点可开 T 的来源档位（兜底表；真源是烘参 t_anchor_ok_sources）。
# §T-ANCHOR-COSTFALLBACK（owner 2026-11-18 裁决）：cost_fallback 也放行——物理机上没有
# 首买成交可证的持仓，坚持只认 first_buy_order/first_lot 会让 T 腿永远停在锚点门。
# 优先读 local_executor_params.json（Go anchorTicketSources 经 derive 写入），两份清单
# 因此不会各说各话；读不到时才用这张兜底表。
T_ANCHOR_OK_SOURCES = ("first_buy_order", "first_lot", "cost_fallback")
# §LIVE-T-20261116：T 腿幂等短码表（owner 装填动作，离线确认过：
# 五个码互不为前缀、且不与现有 signal_codes 的 BD/BK/D/DB/DR/FC/GN/HC/LD/LD1/PB/PT/SL/TP 冲突；
# 'X' 前缀战略腿未使用）。空表即 T 腿 fail-closed 不出单——这里装填即解锁。
# 经环境变量可覆盖，改码前必须重跑 rules_t.t_short_code_check 与 g8 唯一闸。
T_SIGNAL_CODES = json.loads(os.environ.get(
    "T_SIGNAL_CODES_JSON",
    '{"t_buy_first":"TB","t_buy_first_pair":"TS","t_sell_first":"XB",'
    '"t_sell_pair":"XS","t_close_out":"TC"}'))

SHORT_STRATEGY_TYPES = ("high_churn", "break_down", "leader_decay", "leader_decay_v2",
                        "good_news_fade")
# 利好池参数（owner 2026-11-10 18:42：利好/d1/n 形打分也烘进载体做机会池；
# 进池只拓宽规则"可看"的票，买入仍要过全部 12 闸，不发明新下单语义）。
BULLISH_MIN_SCORE = float(os.environ.get("BULLISH_MIN_SCORE", "60"))
HOT_POOL_MAX = int(os.environ.get("HOT_POOL_MAX", "30"))
SIGNAL_POOL_MAX = int(os.environ.get("SIGNAL_POOL_MAX", "1000"))  # owner 2026-11-13：监视池上限 1000（裁决 10）

# ---------------------------------------------------------------------------
# §PARAMS-STATIC-BASE（2026-11-19）：静态旋钮与运行态字段分家。
#
# 为什么必须有这一层（现网实锤）：`qmt_gateway/local_executor_params.json` 自 11-12 起
# 不再随 rsync 下发（要用 gz 上那份运行态：监视池/行业桶/预计算/锚点簿），于是**静态旋钮**
# 也跟着冻在 gz 的旧副本上——11-19 烘出来的 `stale_order_sec=900`（仓库真源是 600）让
# 10 分钟超时撤单永远不成立，`close_sweep_from` 也还停在 14:52（11-13 已裁到 14:50）。
# 两处都不是代码缺陷，而是"同一枚数在两地各一份"。
#
# 分法：仓库那份（随 rsync 落成 `local_executor_params.base.json`）是**全部静态旋钮**的
# 唯一真源；下面这组键是**运行态**（由本脚本 / pregen / sync_* 每轮重算写入），永远以
# gz 现值为准。合并方向因此是「先取 base，再把运行态键盖回去」，且任何只存在于运行态
# 文件里的新键都原样保留（宁可多留，不可像 11-12 那次把 600 池冲掉）。
PARAMS_BASE_NAME = "local_executor_params.base.json"
RUNTIME_PARAM_KEYS = frozenset((
    "hot_pool", "precalc", "symbols", "t_anchor_book", "t_macro",
    "concept_map", "industry_map", "live_rules", "risk_list_dynamic",
    # §FLOW-SNAPSHOT：30 分钟重算的资金流快照（大盘/板块 delta + 市场状态/仓位档），
    # 由 bake_flow_snapshot 每轮重算写入，永远是运行态而非静态旋钮。
    "flow_snapshot",
    # §FLOW-SECTOR-JOIN：板块逐票连接键（code -> 东财板块名）。**必须是东财板块名**，
    # 不能用 industry_map（证监会门类，与 sector_delta 的键交集为空）。今日云端还没有
    # 生产者 ⇒ 载体弃权并把命中率打进小时读数；等 owner 裁决取板块成分的那条路后再填。
    "sector_map",
))

# §FLOW-SNAPSHOT（owner 2026-11-19/20 裁决 A）：资金流快照的重算节拍与"实质变化"阈值。
# 30 分钟重算一次（发布只在 delta 实质变化时才发生——一次发布＝盒子客户端一次有序重启）；
# 0.05 的量化步长≈净流入/成交额 0.15 个百分点，低于它的抖动不值得重启一次客户端。
FLOW_SNAP_MIN_INTERVAL_SEC = float(os.environ.get("FLOW_SNAP_MIN_INTERVAL_SEC", "1800"))
FLOW_SNAP_TOL = float(os.environ.get("FLOW_SNAP_TOL", "0.05"))
FLOW_SNAP_ATTEMPT = os.path.join(BUILD_DIR, "qmt_gateway", "flow_snapshot_attempt.json")
# 顶层（不在 params 内）的运行态键：版本号必须单调，不能被 base 里的旧值盖回去。
RUNTIME_DOC_KEYS = frozenset(("strategy_version",))


def merge_static_params(runtime_doc, base_doc):
    """Overlay the shipped static base on one runtime params doc -> (merged, patched).

    `patched` is [(key, runtime_value, base_value)] for the static keys the base
    corrected; the caller prints it (ASCII, one line per key) so a silent
    re-tune of a live strategy is never silent.

    Kept pure (no file IO) on purpose: the box-side behaviour is tested in
    qmt_gateway/tests/test_hotreload_static_params.py.
    """
    merged = json.loads(json.dumps(base_doc))
    base_params = merged.get("params") or {}
    runtime_params = (runtime_doc or {}).get("params") or {}
    patched = []
    for key in sorted(runtime_params):
        if key in RUNTIME_PARAM_KEYS:
            base_params[key] = runtime_params[key]
            continue
        if key not in base_params:
            # A key the base does not know: it can only be runtime state that
            # was added after this base was cut. Keeping it is the fail-safe
            # direction (dropping it is how the 11-12 pool wipe happened).
            base_params[key] = runtime_params[key]
            continue
        if base_params[key] != runtime_params[key]:
            patched.append((key, runtime_params[key], base_params[key]))
    merged["params"] = base_params
    for key in RUNTIME_DOC_KEYS:
        if isinstance(runtime_doc, dict) and key in runtime_doc:
            merged[key] = runtime_doc[key]
    return merged, patched


def _params_base_path():
    return os.path.join(BUILD_DIR, "qmt_gateway", PARAMS_BASE_NAME)


def static_base_digest():
    """Digest of the shipped base (empty when absent) — folded into the bake sha
    so "only a static knob changed" still triggers a re-bake."""
    try:
        with open(_params_base_path(), "rb") as fh:
            return hashlib.sha256(fh.read()).hexdigest()[:16]
    except OSError:
        return ""


def apply_static_params_base():
    """Merge the shipped static base into the runtime params file (in place).

    Runs before every bake. Absent base = old deployment: keep the runtime copy
    untouched and say so loudly (never a silent half-state).
    """
    base_path = _params_base_path()
    runtime_path = os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json")
    if not os.path.exists(base_path):
        print("HOTRELOAD_PARAMS_BASE_ABSENT file=%s action=keep_runtime_copy" % PARAMS_BASE_NAME)
        return 0
    try:
        with open(runtime_path, encoding="utf-8") as fh:
            runtime_doc = json.load(fh)
        with open(base_path, encoding="utf-8") as fh:
            base_doc = json.load(fh)
    except (OSError, ValueError) as exc:
        die("params_base_unreadable:%s" % repr(exc)[:80])
    merged, patched = merge_static_params(runtime_doc, base_doc)
    for key, old, new in patched:
        print("HOTRELOAD_PARAMS_STATIC_PATCH key=%s runtime=%s base=%s" % (
            key, json.dumps(old, ensure_ascii=True)[:90], json.dumps(new, ensure_ascii=True)[:90]))
    with open(runtime_path, "w", encoding="utf-8") as fh:
        json.dump(merged, fh, ensure_ascii=True, indent=2)
    print("HOTRELOAD_PARAMS_MERGED static_patched=%d" % len(patched))
    return len(patched)



def die(reason):
    """带 ASCII 锚的硬退出：HOTRELOAD_GZ_ABORT reason=...——任何烘焙前置失败
    （快照漂移/同步失败/闸门红）都必须走这里点名成因，绝不带病继续发布。"""
    print("HOTRELOAD_GZ_ABORT reason=%s" % reason)
    sys.exit(1)


def norm(code):
    """信号代码归一化成 6 位纯数字：剥 .SZ/.SH/.BJ 后缀并大写——signals_today
    与利空/利好池里的代码形态不一（600519.SH/600519），监视池与差分都以此为唯一口径。"""
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


def extract_buy_codes():
    """§INTRADAY-BUYSIG（2026-11-16 owner 拍板）：今日全部 buy 信号代码集合——
    与上次**发布**时的集合做差分，新增非空即置 manifest.intraday_priority=true，
    让盘中班次（≤10 分钟）把含新信号的载体投下去并有序重启客户端；否则普通
    版本推进只能等 15:40 收盘班次（今天 4 个 93~99 分 buy 信号因此全天没下盒）。
    行的字段以 signals_today.json 实测为准：action/kind=="buy"（watch/N形观察
    不是买入意图），direction 在 ("做空","利空") 的行剔除。English: today's buy
    signal codes; a non-empty diff vs the last PUBLISH flips intraday_priority."""
    buys = set()
    try:
        with open(SIGNALS_TODAY, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        return buys
    rows = data if isinstance(data, list) else (data or {}).get("signals") or []
    for s in rows:
        if not isinstance(s, dict):
            continue
        if str(s.get("direction") or "") in ("做空", "利空"):
            continue
        kind = str(s.get("action") or s.get("kind") or "").lower()
        if kind != "buy":
            continue
        c = norm(s.get("code"))
        if len(c) == 6 and c.isdigit():
            buys.add(c)
    return buys


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


def assemble_scan_pool(prev_symbols, signal_pool, bullish, score_rank, pool_max):
    """监视池三级优先级截断（2026-11-13 owner 裁决「当日信号 > 评分 > 自选」）。

    旧实现 `sorted(pool)[:MAX]` 是**字典序截断**——池只增不减，一旦超上限，000/001/002
    开头的老代码被永久钉住，新信号 / 600xxx / 688xxx 永远进不来（#7 裁决 10 ④ 必带修复；
    把 600 扩到 1000 只会把缺陷推迟、放大）。
    三级语义（纯函数，无 IO，可单测）：
      tier1 当日信号——有票必进（同层按码序稳定，保证幂等）；
      tier2 评分——利好池分数 ∪ scores_fast 评分榜，分数高者在前（并列按码序）；
      tier3 上一轮自选——超上限的老代码自然滚动淘汰（不再字典序钉死）。
    输入均为 6 位码（norm 后形态）；bullish 值形如 {"score": 88.5, ...}。
    English: priority-based scan-pool truncation — signals beat scores beat stale watchlist;
    lexicographic pinning of old low-prefix codes is retired.
    """
    prev = set(prev_symbols or [])
    tier2 = dict(score_rank or {})
    for c, info in (bullish or {}).items():
        s = 0.0
        if isinstance(info, dict):
            try:
                s = float(info.get("score") or 0.0)
            except (TypeError, ValueError):
                s = 0.0
        tier2[c] = max(tier2[c], s) if c in tier2 else s
    sig = set(signal_pool or [])
    ordered = []
    ordered.extend(sorted(sig))
    ordered.extend(c for c, _s in sorted(tier2.items(), key=lambda kv: (-kv[1], kv[0]))
                   if c not in sig)
    ordered.extend(sorted(prev - sig - set(tier2)))
    return ordered[: max(0, int(pool_max))]


def extract_score_rank():
    """当日评分榜代码→分数映射（accounts/<OWNER>/scores_fast.json 的 scores keys）。

    owner 2026-11-12：监视池要 600 只——当日信号只有 ~100 只，池子凑不满。
    scores_fast 是引擎当日实算的全市场活跃池（~593 只、trading_day 必须当日才采信），
    并入后监视池直达 SIGNAL_POOL_MAX。扩池只加「看得见」：每一票仍过 QMT 载体
    12 闸（确认窗/VWAP/集中度/1万闸/熔断…），评分榜高分本身不构成任何买入理由。
    2026-11-13（#7 裁决 10 ④）：返回**代码→分数**映射而非纯代码清单——
    三级优先级截断需要在 tier2 内按分数排序，纯清单做不到（字典序截断缺陷的根因之一）。
    English: today's scored universe as {code: score}; the priority truncation ranks
    tier-2 entries by score, which a bare code list cannot express.
    """
    try:
        path = os.path.join(DATA_DIR, "accounts", OWNER_UID, "scores_fast.json")
        with open(path, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return {}
    today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    if str(doc.get("trading_day") or "") != today:
        return {}  # 非当日评分榜绝不留宿（与 load_precalc 同一纪律）
    out = {}
    for c, s in (doc.get("scores") or {}).items():
        if isinstance(c, str) and len(c) == 6 and c.isdigit():
            try:
                out[c] = float(s)
            except (TypeError, ValueError):
                out[c] = 0.0
    return out


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


def load_sector_map():
    """§FLOW-SECTOR-JOIN：读当日板块逐票连接键（code -> 东财板块名）。

    返回 (meta, mapping)：跨日/缺失/空表 ⇒ (meta, None)。
    与 precalc 同一纪律——跨日不留宿；这份表只影响"逐票板块 delta 能不能连上"，
    所以缺当日就弃权（blend renormalize 到 大盘+量能），绝不用昨天的成分表冒充今天。
    """
    try:
        with open("qmt_gateway/local_executor_sector_map.json", "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return {"day": "", "count": 0}, None
    today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    if str(doc.get("day") or "") != today:
        return {"day": str(doc.get("day") or ""), "count": 0}, None
    raw = doc.get("sector_map") or {}
    if not isinstance(raw, dict) or not raw:
        return {"day": doc.get("day"), "count": 0}, None
    mapping = {}
    for code, name in raw.items():
        code = str(code or "").strip()
        name = str(name or "").strip()
        if len(code) == 6 and code.isdigit() and name:
            mapping[code] = name
    return {"day": doc.get("day"), "count": len(mapping)}, (mapping or None)


def extract_bearish():
    """signals_today.json 的做空/利空归因 → 当日利空候选（空=保留上一版）。

    §NEWS-GATE 2026-11-12（R12）：本函数降级为**回退源**——news_gate_today.json 当日新鲜时
    烘焙一律走资讯闸快照（负分个股+板块负分，比信号筛选更细、归因更快）；快照缺失/过期
    （引擎未跑/刚迁移）才回退本函数，绝不静默空清单。
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
        if (str(s.get("direction") or "") in ("做空",)
                or str(s.get("alert_type") or s.get("alertType") or "") in ("做空", "利空")
                or str(s.get("strategy_type") or s.get("strategyType") or "") in SHORT_STRATEGY_TYPES):
            c = norm(s.get("code"))
            if len(c) == 6 and c.isdigit():
                codes.append(c)
    return sorted(set(codes))


def extract_held_codes():
    """OWNER 当日持仓代码（trading.db real_positions 只读，qty>0）→ force_sell_boost 判定用。

    表/列缺失或库不可读 → 空表（fail-open：退化为"负分全量加大卖出观察"，不下猛药）。
    """
    try:
        import sqlite3
        conn = sqlite3.connect("file:%s?mode=ro" % HOTRELOAD_DB, uri=True, timeout=5)
        try:
            cur = conn.execute(
                "SELECT DISTINCT code FROM real_positions "
                "WHERE user_id=? AND COALESCE(qty,0) > 0", (OWNER_UID,))
            out = set()
            for row in cur:
                c = norm(row[0])
                if len(c) == 6 and c.isdigit():
                    out.add(c)
            return out
        finally:
            conn.close()
    except Exception as exc:  # noqa: BLE001 — 库缺表/锁繁忙都不阻断烘焙
        print("HOTRELOAD_HELD_FALLBACK %s" % repr(exc)[:80])
        return set()


def _t_anchor_ok_sources():
    """可开 T 的锚点来源清单：优先读烘参，读不到才退回模块常量。

    真源是 Go 的 anchorTicketSources（经 derive_carrier_params.py 写进
    local_executor_params.json）。载体在盒子上判的是同一份烘参，所以这里必须跟它一致——
    两份手抄清单一旦漂移，锚点簿会按一份过滤、rules_t 又按另一份判，T 腿会静默不出。
    """
    try:
        with open(os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json"),
                  encoding="utf-8") as fh:
            vals = (json.load(fh).get("params") or {}).get("t_anchor_ok_sources") or []
        vals = [str(v) for v in vals if str(v)]
        if vals:
            return tuple(vals)
    except (OSError, ValueError):
        pass
    return T_ANCHOR_OK_SOURCES


def bake_t_anchor_book():
    """§LIVE-T-20261116：把「可开 T 的持仓首买锚点」从 live.db 烘进载体参数。

    QMT 沙箱零外部输入、从柜台持仓对象读不到 anchor_price/anchor_source（柜台
    get_trade_detail_data 根本没有这两列），所以做 T 的 step0 锚点门只能靠外脑
    把锚点烘进 params.t_anchor_book，沙箱在 _run_t 里注入 ctx.positions_live。

    只读 live.db（mode=ro），只取 qty>0 且 anchor_source ∈ 白名单且 anchor_price>0 的行。
    库不可读/表缺失 → 空表并打锚（fail-closed：沙箱 step0 会因此拒绝开 T，不会瞎下）。
    """
    out = {}
    ok_sources = _t_anchor_ok_sources()
    try:
        import sqlite3
        conn = sqlite3.connect("file:%s?mode=ro" % LIVE_DB, uri=True, timeout=5)
        try:
            ph = ",".join("?" for _ in ok_sources)
            cur = conn.execute(
                "SELECT ts_code, anchor_price, anchor_source FROM real_positions "
                "WHERE user_id=? AND COALESCE(qty,0)>0 AND anchor_price>0 "
                "AND anchor_source IN (%s)" % ph, (OWNER_UID,) + ok_sources)
            for code, price, source in cur:
                code = norm(code)
                if len(code) != 6 or not code.isdigit():
                    continue
                out[code] = {"anchor_price": round(float(price), 6),
                             "anchor_source": str(source)}
        finally:
            conn.close()
    except Exception as exc:  # noqa: BLE001 — 库缺表/锁繁忙 → 空表，沙箱 fail-closed
        print("HOTRELOAD_T_ANCHOR_FALLBACK %s" % repr(exc)[:80])
        return {}
    print("HOTRELOAD_T_ANCHOR n=%d" % len(out))
    return out


def bake_t_macro():
    """§LIVE-T-20261116：把 T 方向门必需的宏观读数烘进载体参数。

    market_scale 来自引擎 T 账本 funds（每轮刷新，已带 timestamp）；
    market_state/index_change_pct 若账本没有则留空——沙箱 t_gate_open 对
    market_state 缺失是 abstain（放行）、对 market_scale/index 缺失是 fail-closed，
    所以两者都缺时 T 会自己停开，符合「缺证据不瞎做」纪律。

    带 asof 供沙箱判新鲜度（t_regime_stale_sec）；取不到 → {} → 沙箱停开 T。
    """
    out = {}
    try:
        doc = json.load(open(T_ROUNDS_LIVE, encoding="utf-8"))
        funds = doc.get("funds") or {}
        ms = funds.get("market_scale")
        if ms is not None and float(ms) > 0:
            out["market_scale"] = round(float(ms), 6)
        # T-EQUITY-FALLBACK 2026-11-17: the box counter's ACCOUNT table does not
        # fill m_dBalance on this broker, so _account_total_asset() returns None
        # and t_cap_base has no denominator -> T stays dead all day. Bake the
        # engine's own total_asset (already refreshed every round in funds) as a
        # fallback. The carrier uses it ONLY when the counter gives no reading,
        # and only while this asof is fresh (asof_age_sec below); stale -> no T.
        ta = funds.get("total_asset")
        if ta is not None and float(ta) > 0:
            out["total_asset"] = round(float(ta), 2)
        out["index_source"] = funds.get("index_source") or ""
        out["asof"] = funds.get("t_at") or funds.get("at") or ""
        # 沙箱不能用 datetime.fromisoformat（g2 闸禁），这里在云端算好"读数年龄秒"，
        # 载体只比数字。取不到/格式错 → 不写该键 → 沙箱按新鲜度未知 fail-closed。
        try:
            _asof_dt = datetime.datetime.fromisoformat(str(out["asof"]))
            out["asof_age_sec"] = max(0.0, (datetime.datetime.now(_asof_dt.tzinfo) - _asof_dt).total_seconds())
        except (ValueError, TypeError):
            pass
        # index_change_pct 由 §LIVE-T-20261116 写入 T 账本 funds（Go 外脑同帧烘出）。
        # 0 是合法的平盘读数，不是缺失——缺失用 index_source 空来判断。
        if funds.get("index_change_pct") is not None:
            out["index_change_pct"] = float(funds["index_change_pct"])
        # market_state 账本暂无；缺失时沙箱 market_state gate 走 abstain（放行），
        # 这是设计语义：不把"没读到位"伪装成"读到牛市/熊市"。
        if funds.get("market_state") is not None:
            out["market_state"] = funds["market_state"]
            if funds.get("market_state_at") is not None:
                out["market_state_age_sec"] = funds["market_state_at"]
    except Exception as exc:  # noqa: BLE001 — 账本缺失 → 空表，沙箱停开 T
        print("HOTRELOAD_T_MACRO_FALLBACK %s" % repr(exc)[:80])
        return {}
    print("HOTRELOAD_T_MACRO keys=%s asof=%s" % (sorted(out.keys()), out.get("asof")))
    return out


def _flow_snap_read_json(path, default):
    """Read one small JSON state file; a missing/corrupt file yields the default."""
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return default


def _flow_snap_quantize(value, step):
    """Round a delta to a comparison grid so sub-threshold noise cannot churn.

    Two jobs: (a) `changed` must mean "materially different", otherwise a sector
    delta wobbling by 0.001 every 30 minutes would republish and restart the
    box's QMT client for nothing; (b) the sha payload digest uses the SAME grid,
    so "changed" and "sha changed" can never disagree (a digest that disagrees
    with the change test is how a real update gets filed as NOTHING_TO_DO).
    """
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    if number != number:  # NaN
        return None
    return round(number / step) * step if step > 0 else number


def flow_snapshot_digest(snapshot):
    """Comparable fingerprint of a flow snapshot (baked_at deliberately excluded).

    Returns "" when there is no usable snapshot, so "never baked" and "baked
    empty" stay distinguishable from "baked and unchanged" in the log.
    """
    if not isinstance(snapshot, dict) or not snapshot.get("day"):
        return ""
    step = FLOW_SNAP_TOL
    market = _flow_snap_quantize(snapshot.get("market_delta"), step)
    sectors = snapshot.get("sector_delta") or {}
    quantized = {}
    if isinstance(sectors, dict):
        for name, value in sectors.items():
            q = _flow_snap_quantize(value, step)
            if q is not None:
                quantized[str(name)] = q
    body = {
        "day": str(snapshot.get("day") or ""),
        "market": market,
        "sector": quantized,
        "state": snapshot.get("market_state"),
        "max_pos_pct": _flow_snap_quantize(snapshot.get("max_pos_pct"), 0.01),
    }
    return hashlib.sha256(json.dumps(body, ensure_ascii=True, sort_keys=True)
                          .encode()).hexdigest()[:16]


def _read_flow_inputs(day_compact):
    """Read today's money-flow readings from the research DB (read-only).

    Deliberately re-uses what the Go engine already normalised rather than
    re-implementing the EastMoney parse here:

      * flow_factor_daily.market_flow_pct / flow_market_delta -- the engine's
        60s market leg (index fflow through the box relay, FlowPctToDelta).
      * sector_flow_daily.sector / delta -- the engine's per-board leg.
      * market_risk_daily.market_state / max_pos_pct -- the state machine's
        regime and its recommended max-position tier (the posRel denominator).

    Two date spellings, on purpose: the flow tables are written with a dashed
    `2006-01-02` key (cntime.DayOf) while market_risk_daily uses the compact
    `20060102` one (data.TradingDayDate). Querying with the wrong shape returns
    zero rows and would look exactly like "no flow today".
    """
    dashed = "%s-%s-%s" % (day_compact[0:4], day_compact[4:6], day_compact[6:8])
    out = {"market_pct": None, "market_delta": None, "sectors": {},
           "market_state": None, "max_pos_pct": None}
    import sqlite3
    conn = sqlite3.connect("file:%s?mode=ro" % HOTRELOAD_DB, uri=True, timeout=5)
    try:
        row = conn.execute(
            "SELECT market_flow_pct, flow_market_delta FROM flow_factor_daily "
            "WHERE trade_date=?", (dashed,)).fetchone()
        if row:
            if row[0] is not None:
                out["market_pct"] = float(row[0])
            if row[1] is not None:
                out["market_delta"] = float(row[1])
        for sector, delta in conn.execute(
                "SELECT sector, delta FROM sector_flow_daily "
                "WHERE trade_date=? AND delta IS NOT NULL", (dashed,)):
            if sector:
                out["sectors"][str(sector)] = float(delta)
        row = conn.execute(
            "SELECT market_state, max_pos_pct FROM market_risk_daily WHERE trade_date=?",
            (day_compact,)).fetchone()
        if row:
            out["market_state"] = str(row[0]) if row[0] else None
            if row[1] is not None and float(row[1]) > 0:
                out["max_pos_pct"] = float(row[1])
    finally:
        conn.close()
    return out


def bake_flow_snapshot():
    """§FLOW-SNAPSHOT: bake the 30-minute money-flow snapshot into the carrier.

    Owner ruling 2026-11-19/20 (option A): the decision host recomputes the
    market + per-sector flow readings every 30 minutes and publishes a carrier
    ONLY when the deltas materially changed (a publish means an ordered restart
    of the box's QMT client, so churn is the cost we are avoiding). Between
    publishes the box keeps the previous snapshot, and the carrier abstains once
    that snapshot is older than its TTL (45 min) or belongs to another day --
    abstaining is the honest answer to "the flow reading is two hours old".

    Returns (snapshot, changed). `snapshot` is what should be written into
    params.flow_snapshot (the previous one is returned untouched when nothing
    moved). `changed` drives manifest.intraday_priority.
    """
    now = time.time()
    day_compact = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    try:
        with open(os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json"),
                  encoding="utf-8") as fh:
            current = ((json.load(fh) or {}).get("params") or {}).get("flow_snapshot")
    except (OSError, ValueError):
        current = None
    state = _flow_snap_read_json(FLOW_SNAP_ATTEMPT, {})
    last_attempt = float(state.get("at") or 0.0)
    due = (not isinstance(current, dict)
           or str(current.get("day") or "") != day_compact
           or now - last_attempt >= FLOW_SNAP_MIN_INTERVAL_SEC)
    if not due:
        # Throttle: one recompute per half hour. The sha payload carries the
        # digest, so an unchanged snapshot naturally yields NOTHING_TO_DO.
        return current, False
    try:
        readings = _read_flow_inputs(day_compact)
    except Exception as exc:  # noqa: BLE001 -- an unreadable DB must not kill the bake
        print("HOTRELOAD_FLOW_SNAPSHOT abstain reason=db_unreadable detail=%s"
              % repr(exc)[:80])
        readings = None
    try:
        with open(FLOW_SNAP_ATTEMPT, "w", encoding="utf-8") as fh:
            json.dump({"at": now, "day": day_compact}, fh)
    except OSError as exc:
        print("HOTRELOAD_FLOW_SNAPSHOT_STATE_FAIL %s" % type(exc).__name__)
    if not readings or (readings["market_delta"] is None and not readings["sectors"]):
        # Nothing readable yet (pre-open, engine not running, relay down). Keep
        # whatever is baked: a cross-day snapshot is already inert because the
        # carrier checks the day, and a same-day one still has its own TTL.
        print("HOTRELOAD_FLOW_SNAPSHOT abstain reason=no_readings day=%s" % day_compact)
        return current, False
    snapshot = {
        "day": day_compact,
        "baked_at": round(now, 3),
        "market_delta": readings["market_delta"],
        "market_flow_pct": readings["market_pct"],
        "sector_delta": readings["sectors"],
        "market_state": readings["market_state"],
        "max_pos_pct": readings["max_pos_pct"],
        "source": "cloud_flow_bake",
    }
    changed = flow_snapshot_digest(snapshot) != flow_snapshot_digest(current)
    print("HOTRELOAD_FLOW_SNAPSHOT day=%s market_delta=%s sectors=%d state=%s max_pos=%s "
          "changed=%d digest=%s" % (
              day_compact,
              "na" if snapshot["market_delta"] is None else "%.4f" % snapshot["market_delta"],
              len(snapshot["sector_delta"]), snapshot["market_state"] or "-",
              "na" if snapshot["max_pos_pct"] is None else "%.3f" % snapshot["max_pos_pct"],
              1 if changed else 0, flow_snapshot_digest(snapshot)))
    if not changed:
        # Keep the OLD baked_at on purpose: republishing a byte-identical reading
        # would restart the client for nothing, and refreshing the timestamp
        # without a publish would be a freshness claim the box never received.
        return current, False
    return snapshot, True


def extract_news_gate():
    """§NEWS-GATE（R10/R11/R12）：读当日资讯闸快照，返回 None=不可用作真源（回退旧规则）。

    返回 dict：
      negative        个股负分代码（D1<0，明确利空）
      sector_negative 板块级强利空映射代码（R4）
      positive        {code:{score(0~100),source:"d1"}} 正分个股（≥16，利好池烘焙源）
      force_sell      负分且当日持仓 → 烘 params.force_sell_boost（载体卖出加速）
      reversal_events 盘中反转事件（R11；触发盒内拉取/重启编排与 SSH 兜底）
    当日性：snap.day ≠ 今天 → None（昨日快照绝不留宿，与 load_precalc 同纪律）。
    """
    try:
        with open(NEWS_GATE_TODAY, "r", encoding="utf-8") as fh:
            snap = json.load(fh)
    except (OSError, ValueError):
        return None
    if not isinstance(snap, dict):
        return None
    today = datetime.datetime.now().astimezone().strftime("%Y-%m-%d")
    if str(snap.get("day") or "") != today:
        return None
    neg = sorted({norm(c) for c in (snap.get("negative_codes") or {})
                  if isinstance(c, str) and len(norm(c)) == 6 and norm(c).isdigit()})
    sec_neg = sorted({norm(c) for c in (snap.get("sector_negative_codes") or {})
                      if isinstance(c, str) and len(norm(c)) == 6 and norm(c).isdigit()})
    positive = {}
    for c, score in (snap.get("positive_codes") or {}).items():
        cc = norm(c)
        try:
            s = float(score)
        except (TypeError, ValueError):
            continue
        if len(cc) == 6 and cc.isdigit() and s > 0:
            # 归一到 0~100（与旧 extract_bullish 的 conf×100 同尺度，hot_pool/concept_heat 消费端不变）
            positive[cc] = {"score": int(round(s / 40 * 100)), "source": "d1"}
    held = extract_held_codes()
    neg_all = set(neg) | set(sec_neg)
    force_sell = sorted(neg_all & held)
    rev = snap.get("reversal_events") or []
    return {"negative": neg, "sector_negative": sec_neg, "positive": positive,
            "force_sell": force_sell, "reversal_events": rev if isinstance(rev, list) else []}


def ssh_intraday_trigger(ver):
    """§NEWS-GATE（R11）SSH 兜底腿：盘中反转发布后不等盒子的 10 分钟盘中班次，
    直接经 WG 直连盒子触发一次 carrier_run.ps1 -Intraday（owner 裁决：计划任务主通道、
    计划任务失败走 SSH——本函数与盘中班次互为冗余，updater 的 intraday 闸与幂等哈希
    保证重复触发不会重复重启：已同版本时 up_to_date 直接返回）。
    失败仅日志不阻断（下一班计划任务仍会接住）。"""
    key = os.environ.get("BOX_SSH_KEY", "/home/ubuntu/.ssh/win_admin")
    user = os.environ.get("BOX_SSH_USER", "57516")
    host = os.environ.get("BOX_SSH_HOST", "10.77.0.2")
    ps1 = os.environ.get("BOX_CARRIER_RUN",
                         "C:\\qmt\\quant-trading-v2\\qmt_gateway\\carrier_run.ps1")
    if not os.path.exists(key):
        print("HOTRELOAD_SSH_SKIP reason=no_key path=%s" % key)
        return
    ps = ('powershell -NoProfile -ExecutionPolicy Bypass -File "%s" -Intraday' % ps1)
    b64 = base64.b64encode(ps.encode("utf-16-le")).decode()
    cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=6",
           "-o", "StrictHostKeyChecking=accept-new", "-i", key,
           "%s@%s" % (user, host), "powershell", "-NoProfile", "-EncodedCommand", b64]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=180)
        tail = (r.stdout + r.stderr).strip().splitlines()
        print("HOTRELOAD_SSH_TRIGGER version=%s rc=%d %s" % (
            ver, r.returncode, tail[-1][:160] if tail else ""))
    except Exception as exc:  # noqa: BLE001 — 兜底腿失败不影响已发布产物
        print("HOTRELOAD_SSH_TRIGGER_FAIL version=%s %s" % (ver, repr(exc)[:120]))


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
    """跑子流程并回显最后一行日志（rc/输出尾行可见——烘焙链每步的失败都要留痕，
    不静默吞）；返回 CompletedProcess，调用方按 returncode 决定 die 还是继续。"""
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    tail = (r.stdout + r.stderr).strip().splitlines()
    print("  RUN %s rc=%d %s" % (" ".join(cmd[-2:]), r.returncode, tail[-1] if tail else ""))
    return r



# §GATEWAY-FILE-SYNC（owner 2026-11-20「flow_proxy.py 盒上文件更新自动化做了」）：
# 网关侧脚本（flow_proxy.py 这类"盒上文件"）**不在载体投递面里**——载体投递只换策略文件，
# 所以每次改网关文件都得手工 `win_remote_exec.sh push` 再重启网关（2026-11-20 就是这么修
# `b:BK*` 白名单的，两次现场都要人记着）。这里把网关文件放进同一个静态发布面：
#   <friend-pkg>/leg/gateway/<name>            （Caddy 直出，与载体产物同一个 root）
#   <friend-pkg>/leg/gateway/manifest.json     （{name: {"sha256": ..., "size": ...}}）
# 盒子侧 carrier_updater 投递成功后按这份清单自检并同步（见 qmt_gateway/carrier_updater.py）。
# 幂等：内容没变就不重写（比较 sha），所以每 5 分钟一轮的热更不会把静态目录写成热的。
GATEWAY_SYNC_FILES = ("flow_proxy.py",)
# ⚠️ Caddy 的 `handle_path /leg/*` 会**剥掉 /leg 前缀**再用 root=/opt/quant/friend-pkg，
# 所以磁盘路径是 `friend-pkg/gateway/...`，而 URL 是 `/leg/gateway/...`。多写一层 leg
# 会让 URL 404（发布成功、取不到——本仓最贵的形状之一，改这里先读 Caddyfile）。
GATEWAY_PUBLISH_DIR = os.path.join("/opt/quant/friend-pkg", "gateway")


def publish_gateway_files():
    """把网关侧脚本同步到静态发布面（幂等）。返回 [(name, sha256)] 供日志与排障。"""
    out = []
    os.makedirs(GATEWAY_PUBLISH_DIR, exist_ok=True)
    index = {}
    for name in GATEWAY_SYNC_FILES:
        src = os.path.join(BUILD_DIR, "qmt_gateway", name)
        if not os.path.isfile(src):
            print("HOTRELOAD_GATEWAY_SYNC skip=%s reason=src_missing" % name)
            continue
        raw = open(src, "rb").read()
        sha = hashlib.sha256(raw).hexdigest()
        dst = os.path.join(GATEWAY_PUBLISH_DIR, name)
        same = False
        if os.path.isfile(dst):
            same = hashlib.sha256(open(dst, "rb").read()).hexdigest() == sha
        if not same:
            tmp = dst + ".tmp"
            with open(tmp, "wb") as fh:
                fh.write(raw)
            os.replace(tmp, dst)
        index[name] = {"sha256": sha, "size": len(raw)}
        out.append((name, sha))
        print("HOTRELOAD_GATEWAY_SYNC name=%s sha=%s changed=%s" % (name, sha[:16], not same))
    try:
        mpath = os.path.join(GATEWAY_PUBLISH_DIR, "manifest.json")
        body = json.dumps({"files": index, "updated_at": int(time.time())},
                          ensure_ascii=True, sort_keys=True)
        old = ""
        if os.path.isfile(mpath):
            old = open(mpath, "r", encoding="utf-8").read()
        if old.strip() != body.strip():
            tmp = mpath + ".tmp"
            with open(tmp, "w", encoding="utf-8") as fh:
                fh.write(body)
            os.replace(tmp, mpath)
    except OSError as exc:
        print("HOTRELOAD_GATEWAY_SYNC manifest_fail=%s" % exc.__class__.__name__)
    return out

def main():
    """烘焙发布主流程：快照重建 → 双派生（漂移 ABORT）→ 利空归因/利好池/D1 资讯闸
    烘参 → 版本 bump → 10 闸构建 → 发布产物+manifest → SSH 兜底触发。sha 无变化
    时 NOTHING_TO_DO 幂等空轮；--force 跳过 sha 比对（事故恢复/试点实测用）。"""
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true", help="skip sha compare")
    args = ap.parse_args()

    if not os.path.isdir(BUILD_DIR):
        die("build_dir_missing:" + BUILD_DIR)
    os.chdir(BUILD_DIR)

    # §GATEWAY-FILE-SYNC：网关侧脚本进静态发布面（幂等）。放在 sha 比对**之前**——否则
    # "载体没变化"的轮次会 NOTHING_TO_DO 提前退出，网关文件永远同步不出去。
    try:
        publish_gateway_files()
    except Exception as exc:  # noqa: BLE001 - 同步失败不许阻断载体烘焙
        print("HOTRELOAD_GATEWAY_SYNC_FAIL %s" % exc.__class__.__name__)

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

    # §FLOW-SECTOR-JOIN（owner 2026-11-20 裁决 A）：板块逐票连接键当日缺失 ⇒ 补跑一次生产者。
    # 生产者要经盒子中继逐板块取成分（几百次请求、分钟级），所以**限频 30 分钟**且失败不阻断
    # 热更——今天取不到就弃权（blend renormalize 到 大盘+量能），绝不留宿昨天的成分表。
    try:
        _meta, _mapping = load_sector_map()
        if not _mapping:
            _retry_path = os.path.join(BUILD_DIR, "qmt_gateway", "sector_map_retry_ts")
            _last = 0.0
            if os.path.exists(_retry_path):
                try:
                    with open(_retry_path, "r", encoding="utf-8") as fh:
                        _last = float(fh.read().strip() or 0)
                except (OSError, ValueError):
                    _last = 0.0
            _now = datetime.datetime.now().astimezone().timestamp()
            if _now - _last > 1800:
                r = subprocess.run(["python3", "scripts/pregen_sector_map_gz.py"],
                                   cwd=BUILD_DIR, capture_output=True, text=True, timeout=600)
                with open(_retry_path, "w", encoding="utf-8") as fh:
                    fh.write(str(_now))
                tail = (r.stdout + r.stderr).strip().splitlines()
                print("HOTRELOAD_SECTOR_MAP rc=%d %s" % (
                    r.returncode, tail[-1] if tail else ""))
    except Exception as exc:  # noqa: BLE001 - 取不到成分表不阻断热更
        print("HOTRELOAD_SECTOR_MAP_FAIL %s" % repr(exc)[:100])

    qc, rules = load_live()
    wl = (qc.get("qmt", {}) or {}).get("strategies") or []
    if not wl:
        die("whitelist_empty")
    # sha 载荷必须包含当日利空归因（owner 17:35 每半小时一烘的配套）：否则
    # 「归因变了但白名单没变」会被 sha 闸拦成 NOTHING_TO_DO，利空清单永远烘不进载体。
    # §NEWS-GATE 2026-11-12（R12）：真源换为资讯闸快照（负分个股+板块负分+正分 D1 +
    # force_sell + 反转事件）；快照缺失/过期才回退旧 signals 筛选（绝不静默空清单）。
    ng = extract_news_gate()
    if ng is not None:
        bearish = sorted(set(ng["negative"]) | set(ng["sector_negative"]))
        bullish = ng["positive"]
        force_sell = ng["force_sell"]
        reversal_events = ng["reversal_events"]
        print("HOTRELOAD_NEWS_GATE neg=%d sector_neg=%d pos=%d force_sell=%d reversal=%d" % (
            len(ng["negative"]), len(ng["sector_negative"]), len(bullish),
            len(force_sell), len(reversal_events)))
    else:
        bearish = extract_bearish()
        bullish = extract_bullish()
        force_sell = []
        reversal_events = []
        print("HOTRELOAD_NEWS_GATE_FALLBACK signals_filter neg=%d bullish=%d" % (len(bearish), len(bullish)))
    signal_pool = extract_signal_codes()
    precalc_meta, _precalc = load_precalc()
    # §FLOW-SECTOR-JOIN：板块逐票连接键（当日文件；缺失 ⇒ 载体弃权并把命中率打进小时读数）。
    # 在 sha 之前算：表变了必须触发重烘投递（与 precalc/锚点/资金流同一条纪律）。
    sector_map_meta, sector_map = load_sector_map()
    # §LIVE-T-20261116：做 T 的首买锚点与宏观读数（外脑烘参，只读，不碰下单）。
    # 在 sha 之前算，确保任一变化都会触发重烘投递——否则盒子上永远拿不到新锚点/新宏观。
    t_anchor_book = bake_t_anchor_book()
    t_macro = bake_t_macro()
    t_bake_digest = hashlib.sha256(json.dumps(
        {"a": t_anchor_book, "m": t_macro, "c": T_SIGNAL_CODES}, ensure_ascii=True,
        sort_keys=True).encode()).hexdigest()[:16]
    # §FLOW-SNAPSHOT：资金流快照（30 分钟节流重算；只有实质变化才换 digest）。
    # 必须在 sha 之前算：否则"资金流变了但白名单没变"会被 sha 闸拦成 NOTHING_TO_DO，
    # 盒子上永远拿不到新的 delta（与当日利空归因、T 锚点同一条纪律）。
    flow_snapshot, flow_changed = bake_flow_snapshot()
    flow_digest = flow_snapshot_digest(flow_snapshot)
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
                          "force_sell": force_sell,
                          "reversal": hashlib.sha256(json.dumps(
                              reversal_events, ensure_ascii=True,
                              sort_keys=True).encode()).hexdigest()[:16],
                          "signal_pool": signal_pool, "precalc": precalc_meta,
                          # §FLOW-SECTOR-JOIN：板块连接键的当日指纹纳入 sha——
                          # 成分表申领的板块变了（或首次生成）就必须重烘。
                          "sector_map": sector_map_meta,
                          "buy_codes": sorted(extract_buy_codes()),
                          "t_bake": t_bake_digest,
                          # §FLOW-SNAPSHOT：资金流快照（量化后的可比指纹）纳入 sha——
                          # 内容不变时 NOTHING_TO_DO（不重启客户端），变了就必须重烘投递。
                          "flow_snapshot": flow_digest,
                          # §PARAMS-STATIC-BASE: a change to a static knob (or to
                          # this base file itself) must re-bake, otherwise the box
                          # keeps running the old number forever.
                          "static_base": static_base_digest(),
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

    # 0) §PARAMS-STATIC-BASE：先把仓库那份静态真源合进运行态参数文件。
    # 必须在快照/派生/烘焙之前——后面每一步读的都是这份 params。
    apply_static_params_base()

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
    # 2026-11-11 owner 需求：个股监视（信号监视）池扩容——扫描池 = 原自选 ∪ 当日全部信号
    # ∪ 利好池 ∪ scores_fast 评分榜，去重后截断到 SIGNAL_POOL_MAX（2026-11-13 起默认 1000）。
    # 只拓"可看"的票，买入仍过全部 12 闸。QMT 用 get_full_tick 逐票拉取（非订阅），无订阅上限风险。
    #
    # 2026-11-13 owner 裁决（#7 裁决 10 ④ 必带修复）：旧实现 `sorted(pool)[:MAX]` 是
    # **字典序截断**——池只增不减，一旦超上限，000/001/002 开头的老代码被永久钉住，
    # 新信号 / 600xxx / 688xxx 永远进不来。三级优先级截断（「当日信号 > 评分 > 自选」）
    # 的纯函数实现见 assemble_scan_pool（含行为单测），此处只做装配调用。
    doc2["params"]["symbols"] = assemble_scan_pool(
        doc2["params"].get("symbols") or [], signal_pool, bullish,
        extract_score_rank(), SIGNAL_POOL_MAX)
    # §NEWS-GATE（R10）：负分且当日持仓 → params.force_sell_boost（ASCII 键值；每次烘焙
    # **重算覆盖**——该桶语义是"当前持仓的利空票"，清空是正确状态而非数据丢失，与 risk_list
    # 的"空=保旧"并集纪律刻意不同）。ng is None（快照缺失）时不改动该键（保旧）。
    if ng is not None:
        doc2["params"]["force_sell_boost"] = {
            c: {"level": "boost"} for c in force_sell
        }
        doc2["params"]["news_gate"] = {
            "negative": bearish, "updated_at": datetime.datetime.now().astimezone()
            .isoformat(timespec="seconds"),
        }
    # §NEWS-GATE（R5）：资讯因子加权系数（rules_factor 的 NewsEvent 贡献乘子），旋钮经环境变量。
    doc2["params"]["news_weight_boost"] = float(os.environ.get("NEWS_WEIGHT_BOOST", "2.0"))
    # §PRECALC（2026-11-11 owner 拍板）：盘前预计算因子并入 params——载体在缺历史序列的
    # 构建上用 precalc 替代 get_market_data_ex（均线参考/量比基线/近高近低），只有当日新鲜
    # 的文件才并入；并入与否都计入 sha（因子日变即重烘）。
    precalc_meta, precalc = load_precalc()
    if precalc:
        doc2["params"]["precalc"] = precalc
    else:
        doc2["params"].pop("precalc", None)  # 过期因子绝不留宿：宁可弃权也不拿昨天的量比当今天的
    # §FLOW-SECTOR-JOIN：板块逐票连接键（code -> 东财板块名）。**不是 industry_map**：
    # 后者是证监会门类，与 sector_delta 的键交集为空（会用成一条恒 0% 的腿）。
    if sector_map:
        doc2["params"]["sector_map"] = sector_map
    else:
        doc2["params"].pop("sector_map", None)
    # §LIVE-T-20261116：外脑烘参——做 T 的锚点表 + 宏观读数。沙箱零外部输入，
    # 从柜台读不到 anchor，也没法自算 market_scale，只能靠这里烘进去。
    # 空表/空宏观是安全的：沙箱 t_gate_open 会 fail-closed 停开 T（不会瞎下）。
    doc2["params"]["t_anchor_book"] = t_anchor_book
    doc2["params"]["t_macro"] = t_macro
    doc2["params"]["t_signal_codes"] = dict(T_SIGNAL_CODES)
    # §FLOW-SNAPSHOT：写资金流快照（含 baked_at，供载体判 TTL）。没有可用快照时
    # **不写空壳**——跨日的旧键由载体自己按 day 弃权，写一个空 dict 反而会抹掉
    # 昨天那份仍可能被读的证据（跨日弃权已经够了）。
    if isinstance(flow_snapshot, dict) and flow_snapshot.get("day"):
        doc2["params"]["flow_snapshot"] = flow_snapshot
    json.dump(doc2, open("qmt_gateway/local_executor_params.json", "w", encoding="utf-8"),
              ensure_ascii=True, indent=2)

    # §INTRADAY-BUYSIG：本次 buy 集合 vs 上次发布基准的差分 → intraday_priority。
    buys_now = extract_buy_codes()
    try:
        buys_prev = set(json.load(open(BUY_STATE_PATH, encoding="utf-8")))
    except (OSError, ValueError):
        buys_prev = set()
    new_buys = sorted(buys_now - buys_prev)
    print("HOTRELOAD_BUY_DIFF now=%d prev=%d new=%d %s" % (
        len(buys_now), len(buys_prev), len(new_buys), ",".join(new_buys[:12])))

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
    # §PUBLISH-EXACT-20261119：必须按**本次版本名**取产物，绝不按字典序兜底。
    # 现场：v99 之后版本涨到 v104，`sorted(arts)[-1]` 因为 '9' > '1' 又拿回 v99，
    # 于是 "PUBLISHED version=v104" 落下的字节其实是旧的 buggy v99（sha 与 v99 逐字节相同），
    # 盒子就算拉到也还是坏的那份——11-12 只修了「版本名不撞车」，取文件这一半没修。
    # English: pick THIS round's version by exact filename; lexical sort silently republishes
    # the older buggy artifact once the counter crosses v99 -> v100.
    art_path = os.path.join(out_dir, "qmt_internal_strategy_%s.py" % ver)
    if not os.path.exists(art_path):
        die("artifact_version_not_baked version=%s" % ver)
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
        # §NEWS-GATE（R11）：盘中反转事件存在 ⇒ 盒子盘中拉取班次（QMTF-Carrier-Update-Intraday）
        # 允许在盘中投递+重启（否则盘中班次只记账不动客户端，重启窗口留给反转/收盘后班次）。
        # §INTRADAY-BUYSIG（2026-11-16 owner 拍板）：盘中新 buy 信号同样走 intraday——
        # 差分基准是**上次发布**（buy_signals_state.json 只在发布腿写回，NOTHING_TO_DO
        # 轮不写，否则一次空轮就会把新信号吃掉）；每次投递重启 1~2 分钟，同集合天然防抖。
        "intraday_priority": bool(reversal_events) or bool(new_buys) or bool(flow_changed),
    }
    json.dump(manifest, open(os.path.join(manifest_dir, "carrier_manifest.json"), "w",
                             encoding="utf-8"), ensure_ascii=True, indent=2)
    # §INTRADAY-BUYSIG：发布腿才推进 buy 信号基准（NOTHING_TO_DO 轮绝不写，
    # 否则新信号被一次空轮消费、永远到不了盘中班次）。
    try:
        json.dump(sorted(buys_now), open(BUY_STATE_PATH, "w", encoding="utf-8"))
    except OSError as exc:
        print("HOTRELOAD_BUY_STATE_FAIL %s" % type(exc).__name__)

    # §NEWS-GATE（R11）：盘中反转发布 → SSH 兜底立即触发盒子盘中拉取（不等 10 分钟班次）。
    if reversal_events:
        ssh_intraday_trigger(ver)

    with open(STATE, "w") as fh:
        fh.write(sha)
    print("HOTRELOAD_GZ_PUBLISHED version=%s sha=%s wl=%d bearish=%d bullish=%d pool=%d new_buys=%d" % (
        ver, sha_art, len(wl), len(bearish), len(top), len(doc2["params"].get("symbols") or []),
        len(new_buys)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
