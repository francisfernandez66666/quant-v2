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
import ast
import base64
import datetime
import fcntl
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
CONFIG_PATH = os.path.join(DATA_DIR, "config.json")
CARRIER_ACK_HISTORY = os.path.join(DATA_DIR, "carrier_ack_history.jsonl")
HOTRELOAD_LOCK = os.path.join(DATA_DIR, "carrier_hotreload.lock")

# §SIDE-POLICY-PATH 2026-11-25（D6-c）：引擎自 §W-03 起按账号落盘
# （registry.go:1028 acctDir = DataDir/accounts/<uid>），而这条链一直读根目录
# ⇒ 侧策略快照永远读不到 ⇒ 每日烘出 {'enabled': True} 的无 day 空态 ⇒ 载体整天
# fail-closed（11-25 现场：928~946 skips/round、slope=na gap=na）。与上面 SIGNALS_TODAY
# 同族缺陷、同一种修法：账号目录优先，根目录兜底（跨版本过渡期根目录可能还有旧文件）。
#
# 注意：这四条**必须是模块常量**（import 期求值）而不是函数调用点现场算——
# qmt_gateway/tests/test_hotreload_side_policy.py 会在运行期改写它们；
# 而 carrier_hot_reload_gz.py 每次运行都是新进程（systemd oneshot / timer），
# 所以 import 期求值就是"每次运行重新判一次"，不会冻结。
_SIDE_ACCT_DIR = os.path.join(DATA_DIR, "accounts", OWNER_UID)


def _side_state_path(name):
    """账号目录优先、根目录兜底（返回绝对路径）。

    2026-11-27 收口：兜底只在**根目录那份真的存在**时生效。旧写法 `acct if exists else root`
    在"两份都没有"时返回根目录——而本函数的返回值有三处是**写入**用途（req/pending/ack），
    新交易日的第一个写点会落进根目录，下一个进程又按账号目录读，于是同一份状态分成两本账。
    """
    acct = os.path.join(_SIDE_ACCT_DIR, name)
    if os.path.exists(acct):
        return acct
    root = os.path.join(DATA_DIR, name)
    return root if os.path.exists(root) else acct


SIDE_POLICY_LATEST = _side_state_path("side_policy_latest.json")
SIDE_POLICY_REQ = _side_state_path("side_policy_urgent.req")
SIDE_POLICY_PENDING = _side_state_path("side_policy_urgent.pending.json")
SIDE_POLICY_ACK = _side_state_path("side_policy_urgent.ack")
# §SIDE-POLICY-DEFER 2026-11-27：盒子明确跳过盘中投递（intraday_skip_no_priority）时，
# 同一份请求的版本记在这里，重试轮不再重烘（见 side_policy_deferred_ok 头注）。
SIDE_POLICY_DEFERRED = _side_state_path("side_policy_urgent.deferred.json")

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
    # §TP-LADDER-20261124：每只持仓的止盈梯子起点（战法/开仓日/Stage/峰值/止盈点），
    # 每轮由 bake_tp_plan 从 live.db 的成交重建——运行态，永远以本轮算出来的为准。
    "tp_plan",
    "concept_map", "industry_map", "live_rules", "risk_list_dynamic",
    # §FLOW-SNAPSHOT：30 分钟重算的资金流快照（大盘/板块 delta + 市场状态/仓位档），
    # 由 bake_flow_snapshot 每轮重算写入，永远是运行态而非静态旋钮。
    "flow_snapshot",
    # §SIDE-POLICY：云端侧策略快照（含 13:00 latch 与进攻态 freshness epoch）。
    "side_policy",
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

# §TP-LADDER-20261124：被**正式退役**的静态旋钮。合并方向本来是「base 里没有的新键原样保留
# （宁可多留，不可像 11-12 那次把 600 池冲掉）」，那条纪律针对的是"运行态新增键"；一个被
# owner 废掉的阈值必须真的从盒子上消失，否则 PARAMS 里躺着一个没人读的数，下一次改它的人
# 会以为它还在生效（`take_profit_ratio=1.05` 全平腿就是 2026-11-24 那三笔误清仓的成因）。
# 只删这里点名的键，绝不扩成"base 缺什么就删什么"。
RETIRED_PARAM_KEYS = frozenset(("take_profit_ratio",))


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
        if key in RETIRED_PARAM_KEYS:
            # 退役旋钮：base 里没有、运行态里还留着的，一律不搬过去（见 RETIRED_PARAM_KEYS）。
            continue
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
    for key in RETIRED_PARAM_KEYS:
        base_params.pop(key, None)
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


# §TP-LADDER-20261124：战法名 → 载体止盈点键（真源 internal/config/carrier_knobs.go）。
# 没有列在这里的战法（factor_composite / high_churn / leader_decay / good_news_fade /
# break_down / pattern_template）**本来就没有止盈点**——owner 裁决 1 要求它们也有出口，
# 走的是"峰值回撤"那一档，不是硬凑一个数字进来。
TP_PCT_KNOB_BY_STRATEGY = {
    "dragon": "tp_dragon_take_profit_pct",
    "double_bump": "tp_double_bump_take_profit_pct",
    "dragon_return": "tp_dragon_return_take_profit_pct",
}

# D2 2026-11-25：一笔买入只有在「它自己占买入后净头寸的 >= 这个百分比」时，才把
# 阶梯重置为这笔买入。用途是把六周前 100 股零头造成的假 open_date 打断（现场：
# 000521 open_date 2026-10-12、峰值取到 5.21，而当天真实的段是 11-24 起 4.33）；
# 正常的加仓（500 加到 1500 = 33%）不重置，所以阶梯不会被每次加仓清零。
# 与 pregen_sector_map_gz.py 的 MIN_BOARD_OK_PCT 同族：这是烘焙器的结构常量，
# 不是规则层阈值，不进 carrier_knobs.go。
TP_REBASE_SHARE_PCT = 50.0


def _ts_code_of(code):
    """6 位代码 → `daily.ts_code` 的写法（000001.SZ / 600519.SH / 830799.BJ）。"""
    c = norm(code)
    if len(c) != 6 or not c.isdigit():
        return ""
    if c[:2] == "92" or c[0] in ("4", "8"):
        return c + ".BJ"
    if c[0] in ("6", "9", "5"):
        return c + ".SH"
    return c + ".SZ"


def _tp_explicit_pct(qc, strategy):
    """账号里**显式写过**的止盈点（百分点），没写过返回 None。

    判据是**键在不在**而不是值多少：纸面出厂表里 dragon=10 / double_bump=15，而载体
    dragon 用的是 30（owner 裁决 9，与纸面刻意不同）——把出厂值读回来等于悄悄废掉那条裁决。
    dragon_return 在配置里存的是**小数**（0.25），载体说的是百分点，这里只做一次单位换算。
    """
    strat = ((qc.get("rules") or {}).get("strategy") or {})
    value = None
    if strategy == "double_bump":
        value = (strat.get("double_bump") or {}).get("double_bump_take_profit_pct")
    elif strategy == "dragon_return":
        value = (strat.get("dragon_return") or {}).get("take_profit_pct")
        try:
            if value is not None and float(value) <= 1.0:
                value = float(value) * 100.0
        except (TypeError, ValueError):
            value = None
    else:
        value = (strat.get(strategy) or {}).get("take_profit_pct")
    try:
        value = float(value)
    except (TypeError, ValueError):
        return None
    return value if value > 0 else None


def bake_tp_plan(qc=None):
    """§TP-LADDER-20261124：把「每只持仓的止盈梯子起点」从云端真源烘进载体。

    载体没有磁盘（券商禁令）、每次投递都重载策略，柜台 POSITION 行又不带入场战法与
    峰值 ⇒ 三件事只能云端算好：**入场战法**（决定止盈点）、**开仓日**（决定峰值窗口）、
    **Stage**（卖过一半没有）。真源只有一处：live.db 的 fills——按时间正序模拟净头寸，
    头寸从 0 变正的那一刻就是开仓日与战法（signal_id = buy:<码>:<战法>:<日>）；
    成交里出现 sell:<码>:tp:<日> 即 Stage 1（§TP-LADDER 的 qmt.go 卖出补链写的）。

    ⚠️ 不用 real_positions.signal_id/strategy/buy_date 判这两件事：对账快照建的行三列全空，
    成交 UPDATE 分支也不写（实读 000680/000521/000415 全空，而成交明明是 buy:...:fac_160）。
    它们只用来做两件事：确认"现在还持有"、以及给峰值补一个最高价读数。

    峰值 = max(real_positions.highest_price, 开仓日至今的 daily.high)。
    全量或弃权：库读不到 → 空表 ⇒ 载体整条阶梯弃权（fail-closed，绝不按 0 当真）。
    """
    today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
    today_dash = datetime.datetime.now().astimezone().strftime("%Y-%m-%d")
    tp_params = {}
    try:
        with open(os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json"),
                  encoding="utf-8") as fh:
            tp_params = json.load(fh).get("params") or {}
    except (OSError, ValueError):
        tp_params = {}
    if qc is None:
        try:
            qc, _rules = load_live()
        except Exception as exc:  # noqa: BLE001 — 读不到配置只影响"显式值优先"
            print("HOTRELOAD_TP_PLAN_CONFIG_MISS %s" % repr(exc)[:80])
            qc = {}

    # ---- 1) 按成交重建「现在这一轮建仓」：开仓日、战法、Stage ----
    legs = {}
    try:
        import sqlite3
        conn = sqlite3.connect("file:%s?mode=ro" % LIVE_DB, uri=True, timeout=5)
        try:
            cur = conn.execute(
                "SELECT code, side, qty, traded_at, signal_id, price FROM fills "
                "WHERE user_id=? ORDER BY traded_at, id", (OWNER_UID,))
            for code, side, qty, traded_at, signal_id, price in cur:
                c = norm(code)
                if len(c) != 6 or not c.isdigit():
                    continue
                st = legs.setdefault(c, {"net": 0, "open": None, "strategy": "",
                                         "stage": 0, "open_price": None})
                try:
                    q = int(qty or 0)
                except (TypeError, ValueError):
                    q = 0
                if q <= 0:
                    continue
                if str(side) == "买入":
                    net_after = st["net"] + q
                    # D2 2026-11-25：段重置两条判据——
                    #   ① 净头寸从 0 起来（清仓后重建，原行为）；
                    #   ② 这笔买入本身就是当前头寸的主体（零头被打断）。
                    # `stage == 0` 是硬约束：Stage 1 之后的大额加仓**不许**把阶梯
                    # 打回 Stage 0，否则同一只票当天会被再卖一次半仓。
                    if st["stage"] == 0 and (st["net"] <= 0 or
                                             q * 100.0 >= net_after * TP_REBASE_SHARE_PCT):
                        st["open"] = str(traded_at or "")[:10] or None
                        st["strategy"] = _strategy_of_signal(signal_id)
                        st["stage"] = 0
                        # 分母只在该段第一笔（或主体那笔）上写；同段后续加仓不覆盖。
                        try:
                            st["open_price"] = float(price) if price else None
                        except (TypeError, ValueError):
                            st["open_price"] = None
                    st["net"] = net_after
                else:
                    st["net"] -= q
                    if st["net"] <= 0:
                        # 清仓：下一笔买入才算新一轮建仓，阶梯从 Stage 0 重来。
                        st["net"] = 0
                        st["open"] = None
                        st["strategy"] = ""
                        st["stage"] = 0
                        st["open_price"] = None
                    elif _leg_of_signal(signal_id) == "tp":
                        st["stage"] = 1
        finally:
            conn.close()
    except Exception as exc:  # noqa: BLE001 — 库缺表/锁繁忙 → 空表，载体 fail-closed
        print("HOTRELOAD_TP_PLAN_FALLBACK %s" % repr(exc)[:80])
        return {}

    # ---- 2) 现在真的还持有什么（real_positions）+ 峰值读数 ----
    held = {}
    try:
        import sqlite3
        conn = sqlite3.connect("file:%s?mode=ro" % LIVE_DB, uri=True, timeout=5)
        try:
            cur = conn.execute(
                "SELECT ts_code, COALESCE(qty,0), COALESCE(highest_price,0) FROM real_positions "
                "WHERE user_id=? AND COALESCE(qty,0)>0", (OWNER_UID,))
            for code, qty, highest in cur:
                held[norm(code)] = float(highest or 0)
        finally:
            conn.close()
    except Exception as exc:  # noqa: BLE001
        print("HOTRELOAD_TP_PLAN_HELD_FALLBACK %s" % repr(exc)[:80])
        return {}

    out = {}
    manual = 0
    unknown = 0
    for code, highest in held.items():
        st = legs.get(code) or {}
        open_day = st.get("open")
        strategy = st.get("strategy") or ""
        if not open_day or st.get("net", 0) <= 0:
            # 没有可归属的买入成交：手动仓 / 成交史被截断。owner 裁决 8 说手动仓不参与
            # 止盈，方向是"不动它"，所以照实标 manual，载体据此两条腿都不过问。
            manual += 1
            out[code] = {"strategy": strategy, "open_date": open_day or "",
                         "stage": 0, "peak": round(highest, 4) if highest > 0 else None,
                         "tp_pct": None, "manual": True, "open_price": None}
            continue
        tp_pct = _tp_explicit_pct(qc, strategy) if strategy else None
        if tp_pct is None and strategy in TP_PCT_KNOB_BY_STRATEGY:
            knob = TP_PCT_KNOB_BY_STRATEGY[strategy]
            try:
                tp_pct = float(tp_params.get(knob))
            except (TypeError, ValueError):
                tp_pct = None
                unknown += 1
                print("HOTRELOAD_TP_PLAN_WARN key=%s strategy=%s code=%s" % (knob, strategy, code))
        peak = highest if highest > 0 else None
        try:
            import sqlite3
            conn = sqlite3.connect("file:%s?mode=ro" % HOTRELOAD_DB, uri=True, timeout=5)
            try:
                row = conn.execute(
                    "SELECT MAX(high) FROM daily WHERE ts_code=? AND trade_date>=? AND trade_date<=?",
                    (_ts_code_of(code), open_day.replace("-", ""), today_dash.replace("-", ""))
                ).fetchone()
                if row and row[0] is not None and float(row[0]) > 0:
                    daily_high = float(row[0])
                    peak = daily_high if peak is None else max(peak, daily_high)
            finally:
                conn.close()
        except Exception as exc:  # noqa: BLE001 — 峰值缺一条腿只影响阶梯宽容度
            print("HOTRELOAD_TP_PLAN_DAILY_MISS code=%s %s" % (code, repr(exc)[:60]))
        out[code] = {"strategy": strategy, "open_date": open_day,
                     "stage": int(st.get("stage") or 0),
                     "peak": round(peak, 4) if peak else None,
                     "tp_pct": tp_pct, "manual": False,
                     # D2：段起点价（止盈 pnl 的分母）。缺则载体回落 position_cost。
                     "open_price": (round(float(st.get("open_price")), 4)
                                    if st.get("open_price") else None)}
    print("HOTRELOAD_TP_PLAN n=%d manual=%d unknown_tp=%d day=%s" % (
        len(out), manual, unknown, today))
    return {today: out} if out else {}


def tp_plan_digest(tp_plan):
    """Comparable fingerprint of the baked ladder plan (peak deliberately excluded).

    The static tuple per name goes into the digest: strategy (which picks the
    take-profit point), open_date (which bounds the peak window), stage (half
    already sold) and open_price (D2 2026-11-25: the denominator of the
    take-profit P&L, which only moves when the accumulation segment is
    rebuilt). Peak moves every single day, so folding it in would re-bake and
    therefore **restart the box client** on every timer tick; the peak still
    travels in the payload, it just does not decide "changed".

    Empty/absent plan -> "" so "never baked" and "baked empty" stay
    distinguishable from "baked and unchanged" in the log.
    """
    if not isinstance(tp_plan, dict) or not tp_plan:
        return ""
    body = {}
    for day, per in tp_plan.items():
        if not isinstance(per, dict):
            continue
        body[str(day)] = {
            str(code): {k: (row or {}).get(k)
                        for k in ("strategy", "open_date", "stage", "manual", "tp_pct",
                                  "open_price")}
            for code, row in per.items()
        }
    if not body:
        return ""
    return hashlib.sha256(json.dumps(body, ensure_ascii=True, sort_keys=True)
                          .encode()).hexdigest()[:16]


def _strategy_of_signal(signal_id):
    """`buy:<码>:<战法>:<日>` → 战法名（认不出返回空串，绝不猜）。"""
    text = str(signal_id or "")
    parts = text.split(":")
    if len(parts) >= 3 and parts[0] == "buy":
        return parts[2].strip()
    return ""


def _leg_of_signal(signal_id):
    """`sell:<码>:<tp|tt>:<日>` → tp / tt（其他形态返回空串）。"""
    text = str(signal_id or "")
    parts = text.split(":")
    if len(parts) >= 3 and parts[0] == "sell" and parts[2] in ("tp", "tt"):
        return parts[2]
    return ""


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
    失败仅日志不阻断（下一班计划任务仍会接住）。

    返回 (ok, tail)：tail 是盒子最后一行输出。**必须把 tail 带回去**——盒子在
    `intraday_priority=false` 时会回 `CARRIER_UPDATE_RESULT=OK reason=intraday_skip_no_priority`
    （rc=0 但一个字节都没拉），把它当成"已送达"会让加急请求永远等不到 ACK
    （2026-11-27 现场：30 秒一轮、每轮 2 分钟烘焙、版本号涨到 v2063，盒子整天没收到）。"""
    key = os.environ.get("BOX_SSH_KEY", "/home/ubuntu/.ssh/win_admin")
    user = os.environ.get("BOX_SSH_USER", "57516")
    host = os.environ.get("BOX_SSH_HOST", "10.77.0.2")
    ps1 = os.environ.get("BOX_CARRIER_RUN",
                         "C:\\qmt\\quant-trading-v2\\qmt_gateway\\carrier_run.ps1")
    if not os.path.exists(key):
        print("HOTRELOAD_SSH_SKIP reason=no_key path=%s" % key)
        return False, "no_key"
    ps = ('powershell -NoProfile -ExecutionPolicy Bypass -File "%s" -Intraday' % ps1)
    b64 = base64.b64encode(ps.encode("utf-16-le")).decode()
    cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=6",
           "-o", "StrictHostKeyChecking=accept-new", "-i", key,
           "%s@%s" % (user, host), "powershell", "-NoProfile", "-EncodedCommand", b64]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=180)
        tail = (r.stdout + r.stderr).strip().splitlines()
        last = tail[-1][:160] if tail else ""
        print("HOTRELOAD_SSH_TRIGGER version=%s rc=%d %s" % (ver, r.returncode, last))
        return r.returncode == 0, last
    except Exception as exc:  # noqa: BLE001 — 兜底腿失败不影响已发布产物
        print("HOTRELOAD_SSH_TRIGGER_FAIL version=%s %s" % (ver, repr(exc)[:120]))
        return False, repr(exc)[:120]


def side_policy_deferred_ok():
    """§SIDE-POLICY-DEFER：这份请求是否已经"发布完成但盒子明确跳过"，本轮不必再烘。

    判据三条同时成立才挂起：① req 文件在册且四字段 latch 齐全；② deferred 里记着同一个
    request_id 且 latch 逐字相同；③ 那个版本还没有 ACK。任何一条不成立就照旧走完整流程
    （新请求 / latch 变了 / 盒子真应用了），**绝不把"挂起"当"已送达"**。
    这不是省事：盒子 `intraday_priority=false` 时盘中一律跳过，重烘只会每 30 秒烧 2 分钟
    CPU 并把版本号推高，而盒子一次也收不到。"""
    request = _read_json(SIDE_POLICY_REQ, {}) or {}
    latch = side_request_latch(request)
    req_id = str(request.get("request_id") or "")
    if not latch or not req_id:
        return False
    entry = (_read_json(SIDE_POLICY_DEFERRED, {}) or {}).get(req_id) or {}
    if [str(x) for x in (entry.get("latch") or [])] != list(latch):
        return False
    acked = {str(row.get("version")) for row in _ack_history()
             if row.get("ok") is True and str(row.get("version") or "")}
    if str(entry.get("version") or "") in acked:
        return False
    print("HOTRELOAD_SIDE_POLICY_DEFERRED request_id=%s version=%s reason=%s" % (
        req_id, entry.get("version"), entry.get("reason") or "intraday_skip_no_priority"))
    return True


def _clear_side_policy_req(req_id):
    """撤掉"已经处理完"的那份 req（只撤 request_id 相同的，不能顺手删更新的请求）。

    为什么必须撤：`.path` 是 `PathExists=`（level-triggered），只要文件在，systemd 就会在
    每次 service 退出后立刻再触发一次；不撤的话 10 分钟就能把 `.path` 打成
    unit-start-limit-hit（2026-11-27 现场 13:27:05）。"""
    current = _read_json(SIDE_POLICY_REQ, {}) or {}
    if str(current.get("request_id") or "") != str(req_id):
        return
    try:
        os.remove(SIDE_POLICY_REQ)
    except OSError:
        pass
    pending = _read_json(SIDE_POLICY_PENDING, {}) or {}
    if req_id in pending:
        pending.pop(req_id, None)
        _atomic_json(SIDE_POLICY_PENDING, pending)


def mark_side_policy_deferred(req_id, version, request, reason):
    """记下"已发布但盒子跳过"的那一版，供下一轮短路。空 req_id 不写（无主记录会赖着）。

    除了落账本，这里还要把**这一份 req 撤掉**：盒子已明确跳过，产物也已经发布，
    请求的使命到此为止（盒子按自己的班次在开盘前 / 15:40 拉取）。"""
    if not req_id:
        return
    latch = [str(x) for x in (side_request_latch(request) or [])]
    _atomic_json(SIDE_POLICY_DEFERRED, {
        req_id: {"version": str(version), "latch": latch,
                 "reason": str(reason or "intraday_skip_no_priority"),
                 "at": datetime.datetime.now().astimezone().isoformat(timespec="seconds")}})
    _clear_side_policy_req(req_id)
    print("HOTRELOAD_SIDE_POLICY_PARK request_id=%s version=%s reason=%s" % (
        req_id, version, reason or "intraday_skip_no_priority"))


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
    # §P5-ENABLED 2026-11-27（PLAN_FACTOR_FIX_P0_20261126 §6 / owner D7）：战法库的
    # `enabled` 开关以前只管研究引擎与回测，**没有**同步进实盘白名单派生链——停用的战法
    # 在载体里仍参与最佳规则竞争。这里在**快照重建**处过滤（不动 sync_strategy_switches
    # 的 derive()：那里 ② 要求"规则表里的每条都在白名单"，只滤规则不滤白名单会自相矛盾）。
    # 停用即从实盘白名单剔除：两条腿一起滤，allow 与 factor_rules 才自洽。
    disabled = {rid for rid, rule in by.items()
                if isinstance(rule, dict) and rule.get("enabled", True) is False}
    wl = [rid for rid in wl if rid not in disabled]
    sel = [by[i] for i in wl if i in by]
    # SIDE-POLICY compatibility migration: old applied_factors entries have no style.
    # fac_118 is the single real scored-left producer; all others stay unclassified.
    for rule in sel:
        if not rule.get("entry_style"):
            rule["entry_style"] = "left" if rule.get("id") == "fac_118" else "unclassified"

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

def _read_json(path, default=None):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return default


def _atomic_json(path, value):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(value, fh, ensure_ascii=True, sort_keys=True, indent=2)
    os.replace(tmp, path)


def runtime_side_policy_enabled():
    root = _read_json(CONFIG_PATH, {}) or {}
    runtime = ((root.get("rules") or {}).get("runtime") or {})
    return bool(runtime.get("side_policy_enabled"))


def side_policy_digest(snapshot):
    if not isinstance(snapshot, dict) or not snapshot.get("day"):
        return ""
    # §SIDE-PM-GATE 2026-11-26: `pm_release` must be part of the digest, otherwise
    # the 13:00 false->true flip never changes the sha and the box keeps the AM
    # ramp until the next regular round (or forever, on a quiet afternoon).
    body = {k: snapshot.get(k) for k in (
        "day", "enabled", "mode", "level", "allow_new", "allow_add",
        "target_multiplier", "left_weight", "right_weight", "freshness_epoch",
        "pm_release", "reason")}
    latch = snapshot.get("latch") or {}
    body["latch"] = {k: latch.get(k) for k in ("kind", "until", "reason")}
    return hashlib.sha256(json.dumps(body, ensure_ascii=True, sort_keys=True).encode()).hexdigest()[:16]


def _latch_active(snapshot, now):
    if not isinstance(snapshot, dict) or snapshot.get("day") != now.strftime("%Y%m%d"):
        return False
    latch = snapshot.get("latch") or {}
    try:
        return latch.get("kind") == "no_new" and float(snapshot.get("latch_until_ts") or latch.get("until_ts") or 0) > now.timestamp()
    except (TypeError, ValueError):
        return False


def merge_side_policy(current, latest, request, now):
    """Regular state plus sticky 13:00 latch; urgent request always wins until 14:00.

    §SIDE-POLICY-PATH (D6-c 2026-11-25): `latest` missing / stale means "we could
    not read today's state", NOT "the policy is empty". The direction must be
    fail-closed (no `new`, no `add`) and the reason must say so -- otherwise the
    read-out `{'enabled': True}` looks exactly like a real bear snapshot.

    The fallback MUST be a COMPLETE state that passes rules_sizing.side_policy_state
    field by field. A half snapshot (just day/allow_new/allow_add) hits bad_enum /
    bad_value and falls into _side_fail_closed(), whose allow_add is True (L1
    semantics) -- so "cannot read the state" would flip back to "may add", and the
    reason would be replaced by bad_value. Both directions wrong at once.
    """
    if not isinstance(latest, dict) or latest.get("day") != now.strftime("%Y%m%d"):
        base = {
            "enabled": True,
            "day": now.strftime("%Y%m%d"),
            "mode": "bear", "level": 1,
            "allow_new": False, "allow_add": False,
            "target_multiplier": 1.0, "left_weight": 1.0, "right_weight": 1.0,
            "asof_ts": now.timestamp(), "latch_until_ts": 0.0,
            "reason": "side_policy_state_missing",
        }
        print("HOTRELOAD_SIDE_POLICY_MISSING path=%s day=%s" % (SIDE_POLICY_LATEST, base["day"]))
    else:
        base = dict(latest)
    applied_request = False
    if isinstance(request, dict) and request.get("day") == now.strftime("%Y%m%d") and request.get("kind") == "no_new":
        until = request.get("until") or "14:00"
        try:
            hh, mm = [int(x) for x in until.split(":")]
            until_ts = now.replace(hour=hh, minute=mm, second=0, microsecond=0).timestamp()
        except (TypeError, ValueError):
            until_ts = now.replace(hour=14, minute=0, second=0, microsecond=0).timestamp()
        if until_ts > now.timestamp():
            base["latch"] = {"kind": "no_new", "until": until,
                             "reason": request.get("reason") or "pm1300_urgent"}
            base["latch_until_ts"] = until_ts
            base["allow_new"] = False
            base["reason"] = "pm1300_latch"
            applied_request = True
    if not applied_request and _latch_active(current, now):
        base["latch"] = (current or {}).get("latch") or {}
        base["latch_until_ts"] = (current or {}).get("latch_until_ts") or 0
        base["allow_new"] = False
        base["reason"] = "pm1300_latch"
    return base


def _ack_history():
    out = []
    try:
        with open(CARRIER_ACK_HISTORY, encoding="utf-8") as fh:
            for line in fh:
                try:
                    out.append(json.loads(line))
                except ValueError:
                    continue
    except OSError:
        pass
    return out


def reap_side_policy_requests(now=None):
    now = now or datetime.datetime.now().astimezone()
    pending = _read_json(SIDE_POLICY_PENDING, {}) or {}
    request = _read_json(SIDE_POLICY_REQ, {}) or {}
    if not pending or not request:
        return False
    acked = {str(row.get("version")) for row in _ack_history()
             if row.get("ok") is True and str(row.get("version") or "")}
    req_id = str(request.get("request_id") or "")
    version = str(pending.get(req_id) or "")
    if not version or version not in acked:
        return False
    ack = {"request_id": req_id, "version": version, "ok": True,
           "at": now.isoformat(timespec="seconds")}
    _atomic_json(SIDE_POLICY_ACK, ack)
    # Persist the pending removal first. If either write fails, req remains the
    # retry authority and a later round can safely repeat this idempotent reap.
    pending.pop(req_id, None)
    _atomic_json(SIDE_POLICY_PENDING, pending)
    # §SIDE-POLICY-DEFER：ACK 到了 ⇒ 挂起记录必须一起清掉，否则同一个 request_id
    # 的下一份请求会被这条陈旧挂起短路（看起来"已处理"，实际一次都没投）。
    deferred = _read_json(SIDE_POLICY_DEFERRED, {}) or {}
    if req_id in deferred:
        deferred.pop(req_id, None)
        _atomic_json(SIDE_POLICY_DEFERRED, deferred)
    try:
        os.remove(SIDE_POLICY_REQ)
    except OSError:
        pending[req_id] = version
        _atomic_json(SIDE_POLICY_PENDING, pending)
        raise
    print("HOTRELOAD_SIDE_POLICY_REAP request_id=%s version=%s" % (req_id, version))
    return True


def retry_pending_side_policy():
    pending = _read_json(SIDE_POLICY_PENDING, {}) or {}
    request = _read_json(SIDE_POLICY_REQ, {}) or {}
    req_id = str(request.get("request_id") or "")
    version = str(pending.get(req_id) or "")
    if not req_id or not version:
        return False
    ok, tail = ssh_intraday_trigger(version)
    print("HOTRELOAD_SIDE_POLICY_RETRY request_id=%s version=%s rc=%d" % (
        req_id, version, 0 if ok else 1))
    # 盒子明确跳过盘中投递 ⇒ 这不是失败，是"已发布、等收盘那一次"。挂起后本轮即结束，
    # 下一轮由 side_policy_deferred_ok 短路，不再重烘（否则 30 秒一轮、每轮 2 分钟 CPU）。
    if ok and "intraday_skip" in (tail or ""):
        mark_side_policy_deferred(req_id, version, request, tail)
        return True
    return ok


def side_request_latch(request):
    """The exact four fields that must survive into the published payload."""
    if not isinstance(request, dict):
        return None
    values = (request.get("day"), request.get("kind"),
              request.get("until") or "14:00", request.get("reason") or "pm1300_urgent")
    if not all(isinstance(value, str) and value for value in values):
        return None
    return values


def published_side_policy(version):
    """Read PARAMS from the exact published artifact without executing carrier code."""
    if not isinstance(version, str) or not version or os.path.basename(version) != version:
        return None
    path = os.path.join(CARRIER_DIR, "qmt_internal_strategy_%s.py" % version)
    tree = ast.parse(open(path, encoding="utf-8").read(), filename=path)
    for node in tree.body:
        if not isinstance(node, ast.Assign) or not any(
                isinstance(target, ast.Name) and target.id == "PARAMS" for target in node.targets):
            continue
        params = ast.literal_eval(node.value)
        return params if isinstance(params, dict) else None
    return None


def early_retry_side_policy(request, now):
    """Return None when the published payload lost this latch and must be rebaked."""
    latch = side_request_latch(request)
    req_id = str(request.get("request_id") or "") if isinstance(request, dict) else ""
    if not latch or not req_id:
        print("HOTRELOAD_SIDE_POLICY_REQUEST_INVALID")
        return False
    manifest_path = os.path.join(DATA_DIR, "accounts", OWNER_UID, "carrier_manifest.json")
    manifest = _read_json(manifest_path, {}) or {}
    version = manifest.get("strategy_version")
    if not isinstance(version, str) or not version:
        print("HOTRELOAD_SIDE_POLICY_MANIFEST_UNKNOWN request_id=%s" % req_id)
        return False
    try:
        params = published_side_policy(version)
    except (OSError, ValueError, SyntaxError) as exc:
        print("HOTRELOAD_SIDE_POLICY_PAYLOAD_UNREADABLE version=%s error=%s" % (
            version, exc.__class__.__name__))
        return None
    snapshot = params.get("side_policy") if isinstance(params, dict) else None
    actual = None
    if isinstance(snapshot, dict) and params.get("side_policy_enabled") is True:
        row = snapshot.get("latch") or {}
        candidate = (snapshot.get("day"), row.get("kind"), row.get("until"), row.get("reason"))
        if all(isinstance(value, str) and value for value in candidate):
            actual = candidate
    if actual != latch:
        print("HOTRELOAD_SIDE_POLICY_REBAKE request_id=%s version=%s reason=latch_mismatch" % (
            req_id, version))
        return None
    # §SIDE-POLICY-PENDING 2026-11-26 (plan 19.7 item 1): pending is a ONE-KEY
    # binding, not an accumulator. Only the id currently named in the req file can
    # ever be reaped/retried, so a stale key would linger forever and make the
    # read-out look like a stuck request.
    pending = _read_json(SIDE_POLICY_PENDING, {}) or {}
    if pending != {req_id: version}:
        _atomic_json(SIDE_POLICY_PENDING, {req_id: version})
        print("HOTRELOAD_SIDE_POLICY_REBIND request_id=%s version=%s" % (req_id, version))
    # An applied ACK may have arrived before this rebind; consume it before SSH.
    if reap_side_policy_requests(now):
        return True
    return retry_pending_side_policy()


def _main_impl():
    """烘焙发布主流程：快照重建 → 双派生（漂移 ABORT）→ 利空归因/利好池/D1 资讯闸
    烘参 → 版本 bump → 10 闸构建 → 发布产物+manifest → SSH 兜底触发。sha 无变化
    时 NOTHING_TO_DO 幂等空轮；--force 跳过 sha 比对（事故恢复/试点实测用）。"""
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true", help="skip sha compare")
    ap.add_argument("--urgent-side-policy", action="store_true", help="process 13:00 side-policy request immediately")
    args = ap.parse_args()

    if not os.path.isdir(BUILD_DIR):
        die("build_dir_missing:" + BUILD_DIR)
    os.chdir(BUILD_DIR)

    # §SIDE-POLICY-DEFER 2026-11-27（现场：加急请求整天重烘）：盒子在
    # `intraday_priority=false` 时对盘中投递一律 `intraday_skip_no_priority`（rc=0 但
    # 一个字节都没拉），ACK 永远不来 ⇒ 重试定时器每 30 秒拉一轮、每轮 2 分钟烘焙，
    # 版本号一路涨到 v2063 而盒子一次没收到。已明确跳过且 latch 未变的请求在这里短路，
    # 收盘后（15:40 盒子日常拉取）自然落地；ACK 到达或请求变更即解除。
    #
    # 同一处还挡住第二种空转（13:21 现场）：重试定时器每 30 秒的 `--urgent-side-policy`
    # 在没有请求时也会走完整烘焙（sha 随买点/利好池漂移 ⇒ 每轮都烘、每轮都投，
    # 盒子客户端被反复重启）。加急通道**没有请求就该立即空转退出**，日常轮询归 5 分钟那个 timer。
    if args.urgent_side_policy:
        side_req = _read_json(SIDE_POLICY_REQ, {}) or {}
        if not side_req:
            print("HOTRELOAD_SIDE_POLICY_IDLE reason=no_request")
            return 0
        if side_policy_deferred_ok():
            # 同一份请求被引擎重写（进程重启后 urgentWritten 的内存去重丢失）时，
            # 这里再撤一次 req，否则 PathExists 会让 service 反复触发到启动限流。
            _clear_side_policy_req(str(side_req.get("request_id") or ""))
            return 0

    # §GATEWAY-FILE-SYNC：网关侧脚本进静态发布面（幂等）。放在 sha 比对**之前**——否则
    # "载体没变化"的轮次会 NOTHING_TO_DO 提前退出，网关文件永远同步不出去。
    try:
        publish_gateway_files()
    except Exception as exc:  # noqa: BLE001 - 同步失败不许阻断载体烘焙
        print("HOTRELOAD_GATEWAY_SYNC_FAIL %s" % exc.__class__.__name__)

    if not args.urgent_side_policy:
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
    # §SIDE-POLICY：常规轮读 latest；13:00 req 把 no-new latch 粘到 14:00。
    runtime_side_enabled = runtime_side_policy_enabled()
    current_side = (_read_json(os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json"), {}) or {}).get("params", {}).get("side_policy") or {}
    latest_side = _read_json(SIDE_POLICY_LATEST, {}) or {}
    side_request = _read_json(SIDE_POLICY_REQ, {}) or {}
    side_policy = merge_side_policy(current_side, latest_side, side_request, datetime.datetime.now().astimezone())
    side_policy["enabled"] = runtime_side_enabled
    side_digest = side_policy_digest(side_policy)
    current_side_digest = side_policy_digest(current_side)
    side_changed = side_digest != current_side_digest or bool(current_side.get("enabled")) != runtime_side_enabled
    if side_request and not runtime_side_enabled:
        try:
            os.remove(SIDE_POLICY_REQ)
        except OSError:
            pass
        side_request = {}
    # §TP-LADDER-20261124：每只持仓的止盈梯子起点。同样在 sha 之前算——新开仓、
    # "已经卖过一半"这两种变化都必须触发重烘，否则载体阶梯停在旧阶段（会把剩下的
    # 一半再卖一次，或永远不卖）。sha 里只放**静态三元组**：峰值每天都在变，把它算进
    # sha 会让每个整点都重烘投递，而一次投递＝客户端一次有序重启。
    tp_plan = bake_tp_plan(qc)
    tp_digest = tp_plan_digest(tp_plan)
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
                          # §TP-LADDER-20261124：阶梯阶段指纹（不含峰值，见上）。
                          "tp_plan": tp_digest,
                          # §FLOW-SNAPSHOT：资金流快照（量化后的可比指纹）纳入 sha——
                          # 内容不变时 NOTHING_TO_DO（不重启客户端），变了就必须重烘投递。
                          "flow_snapshot": flow_digest,
                          "side_policy": side_digest,
                          "side_policy_enabled": runtime_side_enabled,
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

    reap_side_policy_requests()
    side_request = _read_json(SIDE_POLICY_REQ, {}) or {}
    if side_request:
        now = datetime.datetime.now().astimezone()
        try:
            hh, mm = [int(x) for x in str(side_request.get("until") or "14:00").split(":")]
            until = now.replace(hour=hh, minute=mm, second=0, microsecond=0)
        except (TypeError, ValueError):
            until = now.replace(hour=14, minute=0, second=0, microsecond=0)
        if (side_request.get("day") != now.strftime("%Y%m%d") or
                until.timestamp() <= now.timestamp()):
            # Expiry is a state transition: failure must be nonzero and leave req intact.
            os.remove(SIDE_POLICY_REQ)
            print("HOTRELOAD_SIDE_POLICY_EXPIRED request_id=%s" % side_request.get("request_id"))
            side_request = {}
    if not args.force and os.path.exists(STATE) and open(STATE).read().strip() == sha:
        if side_request:
            retry = early_retry_side_policy(side_request, datetime.datetime.now().astimezone())
            if retry is not None:
                return 0 if retry else 3
            # Manifest advanced but its artifact does not carry this exact latch.
            # Fall through to the normal bake; never supersede pending on version alone.
        else:
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
    doc2["params"]["tp_plan"] = tp_plan
    doc2["params"]["t_signal_codes"] = dict(T_SIGNAL_CODES)
    # §FLOW-SNAPSHOT：写资金流快照（含 baked_at，供载体判 TTL）。没有可用快照时
    # **不写空壳**——跨日的旧键由载体自己按 day 弃权，写一个空 dict 反而会抹掉
    # 昨天那份仍可能被读的证据（跨日弃权已经够了）。
    if isinstance(flow_snapshot, dict) and flow_snapshot.get("day"):
        doc2["params"]["flow_snapshot"] = flow_snapshot
    doc2["params"]["side_policy_enabled"] = runtime_side_enabled
    doc2["params"]["side_policy"] = side_policy
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
        "intraday_priority": bool(reversal_events) or bool(new_buys) or bool(flow_changed) or bool(side_changed),
    }
    json.dump(manifest, open(os.path.join(manifest_dir, "carrier_manifest.json"), "w",
                             encoding="utf-8"), ensure_ascii=True, indent=2)
    # §INTRADAY-BUYSIG：发布腿才推进 buy 信号基准（NOTHING_TO_DO 轮绝不写，
    # 否则新信号被一次空轮消费、永远到不了盘中班次）。
    try:
        json.dump(sorted(buys_now), open(BUY_STATE_PATH, "w", encoding="utf-8"))
    except OSError as exc:
        print("HOTRELOAD_BUY_STATE_FAIL %s" % type(exc).__name__)

    with open(STATE, "w") as fh:
        fh.write(sha)
    # §NEWS-GATE（R11）：盘中反转发布 → SSH 兜底立即触发盒子盘中拉取。
    if reversal_events:
        ssh_intraday_trigger(ver)
    # §SIDE-POLICY：13:00 加急发布绑定 request_id→version，并立即拉盒子；失败保留 req 重试。
    if side_request:
        req_id = str(side_request.get("request_id") or "")
        if req_id:
            # §SIDE-POLICY-PENDING 2026-11-26 (plan 19.7 item 1): one key only.
            _atomic_json(SIDE_POLICY_PENDING, {req_id: ver})
        ok, tail = ssh_intraday_trigger(ver)
        if not ok:
            print("HOTRELOAD_SIDE_POLICY_SSH_FAIL request_id=%s version=%s" % (req_id, ver))
            return 3
        # §SIDE-POLICY-DEFER：跳过 ≠ 送达。记下这一版，重试轮不再重烘。
        if "intraday_skip" in (tail or ""):
            mark_side_policy_deferred(req_id, ver, side_request, tail)
    print("HOTRELOAD_GZ_PUBLISHED version=%s sha=%s wl=%d bearish=%d bullish=%d pool=%d new_buys=%d" % (
        ver, sha_art, len(wl), len(bearish), len(top), len(doc2["params"].get("symbols") or []),
        len(new_buys)))
    return 0


def main():
    os.makedirs(DATA_DIR, exist_ok=True)
    lock = open(HOTRELOAD_LOCK, "a+")
    try:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("HOTRELOAD_GZ_LOCK_BUSY path=%s" % HOTRELOAD_LOCK)
            return 75
        return _main_impl()
    finally:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
        except OSError:
            pass
        lock.close()


if __name__ == "__main__":
    sys.exit(main())
