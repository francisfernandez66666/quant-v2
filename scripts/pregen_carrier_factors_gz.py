#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""pregen_carrier_factors_gz.py — 盘前预计算因子（§PRECALC，2026-11-11 owner 拍板）。

为什么存在：2026-11-11 实锤，东莞证券 QMT 构建拿不到历史序列
（get_market_data_ex 全签名被拒，只有 get_full_tick）⇒ 载体里所有依赖
历史数据的战法（均线参考/量比确认/近高近低）在盒内**全部保守弃权**，
系统永远不会触发买入。合规出路是官方豁免形态里的「盘前一次性导入数据」：
在 gz 上用公开行情把每票的因子**预计算**好，烘进载体 params.precalc，
盘中 tick 只做实时确认触发——盒内不需要任何历史数据，也不发起任何网络请求。

数据源：同花顺 hithink 公开日线（d.10jqka.com.cn v6/line，owner 指定）。
  盘前运行时返回的 K 线止于上一交易日，prev_close/MA 天然不含当日。

输出（BUILD_DIR 内）：qmt_gateway/local_executor_precalc.json
  {"day": "20261111", "asof": iso, "precalc": {symbol: {
      prev_close, ma_ref, vol_ma5, high10, low10 }}}
carrier_hot_reload_gz.py 会把它并进 params["precalc"]（当日新鲜才并入），
并把 day+count 计入发布 sha——因子变了就重烘。

判读锚点（纯 ASCII）：
  PREGEN_RESULT day=... codes=... ok=... fail=... elapsed_s=...
  PREGEN_WRITE path=... bytes=...
退出码：0 成功（ok>=1）；2 无码可跑；3 全部失败。

三条纪律：
  * 只读公开行情 + 只写 BUILD_DIR 内文件，不碰任何交易通道；
  * 单票失败不拖全局（fail 计数可见，不静默凑数）；
  * 载体侧消费本文件时**绝不回退成"发明一个数"**——缺 precalc 的票维持弃权。
"""
import json
import math
import os
import sys
import time
import urllib.request
from datetime import datetime, timedelta

BUILD_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))  # /opt/quant/carrier_build
PARAMS_PATH = os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_params.json")
OUT_PATH = os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_precalc.json")
MA_WINDOW = 5      # ma_ref：昨收起往前 5 日收盘均值（与载体 ma_window 语义对齐）
VOL_WINDOW = 5     # vol_ma5：昨收起往前 5 日均量（量比基线）
HIGH_LOW_N = 10    # high10/low10：近 10 日（不含当日）
REQ_TIMEOUT = 8
VOL_UNIT_MULT = float(os.environ.get("PREGEN_VOL_UNIT_MULT", "1"))  # hithink 日线量=股，与 QMT tick 同量纲
# 云端自动研究库（trading.db.daily，5546 只全市场 tushare 口径）：hithink 失败的票兜底。
# 注意两点：① vol 单位是**手** ⇒ ×100 换算成股；② 库新鲜度由 dataload 管道决定
#   （2026-11-11 实测停在 20261106——迁移后 dataload 服务/定时器缺失），兜底行会带
#   data_day 让消费方可见 staleness，绝不冒充最新。
LOCAL_DB = os.environ.get("PREGEN_LOCAL_DB", "/var/lib/quant-trading-v2/trading.db")
LOCAL_VOL_MULT = float(os.environ.get("PREGEN_LOCAL_VOL_MULT", "100"))
FLOW_DIR = os.environ.get("PREGEN_FLOW_DIR", "/var/lib/quant-trading-v2/moneyflow_history")
BAKE_SERIES_N = int(os.environ.get("PREGEN_SERIES_N", "20"))
SLEEP_MS = int(os.environ.get("PREGEN_SLEEP_MS", "80"))
# §PRECALC-SERIES（2026-11-16 owner 拍板，PLAN_CARRIER_FACTOR_DATAFACE_20261116）：
# 池子 11-13 已扩到 1000 而 pregen 写死 600 —— 403 只连基线都没有。改为缺省覆盖
# params.symbols 全量；PREGEN_MAX_CODES>0 时显式截断（200 只试点实测 QMT 加载体积用）。
MAX_CODES = int(os.environ.get("PREGEN_MAX_CODES", "0"))
# 序列烘焙：61 因子全部 input="bars"（factor_defs 实测 max_window=60），载体
# _precalc_bars 无本地历史时整段消费。N=80 与载体 bars_count(80) 对齐；价格×1000
# int 化压体积（1000 票×80 根≈2.6MB，断言 A5：QMT 对 3MB 产物的加载需 200 只试点先行）。
SERIES_BARS_N = int(os.environ.get("PREGEN_SERIES_BARS", "80"))
SERIES_PRICE_SCALE = 1000
MIN_SEQ_BARS = 5   # 少于 5 根的序列没有因子价值，不烘（如实缺）

# §P3-FACTOR-FIX 2026-11-27（PLAN_FACTOR_FIX_P0_20261126 §4）：
# ① ann_date 前视回退（owner D3）：报告期公告日为空时用 end_date + 120 天估计，
#    **不整行丢弃**——否则盘前会把尚未披露的报告当成已知（前视偏差），或者干脆
#    少掉一期财务。
ANN_FALLBACK_DAYS = int(os.environ.get("PREGEN_ANN_FALLBACK_DAYS", "120"))
# ② 个股资金流 json 的新鲜窗：末根日期距今超过这个天数就判"不新鲜"，改用板块代理。
#    注意 pregen 是**盘前**跑的，"今日"的个股资金流结构上不可能存在，所以判据是
#    "最近若干个自然日内"而不是"必须是今天"；代理来源会在 flow_src 里如实标注。
FLOW_FRESH_DAYS = int(os.environ.get("PREGEN_FLOW_FRESH_DAYS", "5"))
# FLOW_WINDOW 资金流窗口天数（5 日档）。判据与 Go 侧 internal/research/market_legs.go 的
# flowWindowSessions 同义——**两处不许各自改数**（改了载体与回放就是两个窗口在打分）。
FLOW_WINDOW = 5
# FLOW_CAP_ROUND 占流通市值那一档的小数位。它必须是 8（见写点注释）：
# 这个量纲是 1e-5 级，4 位精度会把绝大多数票烘成 0.0 —— 旧写法就栽在这里。
FLOW_CAP_ROUND = 8
# ③ 板块连接键（sector_map）是**结构表**不是日读数：板块成分不会每天大改，
#    但也不能拿一张很旧的表冒充。超过这个天数就不烘板块代理（宁可弃权）。
SECTOR_MAP_MAX_AGE_DAYS = int(os.environ.get("PREGEN_SECTOR_MAP_MAX_AGE_DAYS", "10"))
# ④ 板块资金流残差窗口（与 rules_factor 的 flow_resid 语义一致：最新一日对前 20 日）。
SECTOR_FLOW_WINDOW = int(os.environ.get("PREGEN_SECTOR_FLOW_WINDOW", "20"))


def fetch_klines(symbol):
    """同花顺日线（owner 2026-11-11 14:14 指定 hithink，不用东财）。

    实测（gz 2026-11-11）：v6/line/hs_{code}/01/last.js 的 JSONP payload 里
    **K 线在 payload["data"] 字符串**：记录用 ';' 分隔、字段用 ',' 分隔——
    日期,开,高,低,收,量,额,涨跌幅,…；**量的单位就是股**（000520 日量 1.6 千万）
    ⇒ PREGEN_VOL_UNIT_MULT 缺省 1（量比= tick量/日线量，单位天然一致）。
    K 线止于上一交易日（当日 K 在 payload["today"]，不用）⇒ prev_close/MA
    天然不含当日。带 UA 即可（Referer 非必需）。北交所码无覆盖 ⇒ 单票失败
    计数可见（不猜）。
    """
    code = symbol.split(".")[0]
    url = "https://d.10jqka.com.cn/v6/line/hs_%s/01/last.js" % code
    req = urllib.request.Request(url, headers={
        "User-Agent": "Mozilla/5.0 (quant-pregen)",
        "Referer": "https://10jqka.com.cn/",
    })
    with urllib.request.urlopen(req, timeout=REQ_TIMEOUT) as resp:
        text = resp.read().decode("gbk", errors="replace")
    payload = json.loads(text[text.index("(") + 1: text.rindex(")")])
    raw = str(payload.get("data") or "")
    bars = []
    for record in raw.split(";"):
        fields = record.split(",")
        if len(fields) < 6:
            continue
        try:
            bars.append({"close": float(fields[4]), "high": float(fields[2]),
                         "low": float(fields[3]),
                         "volume": float(fields[5]) * VOL_UNIT_MULT,
                         "date": fields[0],
                         "amount": float(fields[6]) if len(fields) > 6 and fields[6] else 0.0})
        except (ValueError, IndexError):
            continue
    return bars or None


def bars_seq(bars):
    """§PRECALC-SERIES（2026-11-16 owner 拍板）：最近 SERIES_BARS_N 根日线 →
    紧凑序列 [high,low,close,volume,amount]（价格×1000 int），喂给载体
    _precalc_bars（61 因子全部 input="bars"，bars_coverage 实测仅 52/961——
    序列是唯一能激活因子引擎的腿）。只接受 hithink 源（口径统一前复权，
    与本地 bars 绝不拼接——两源口径不同，接缝假跳变会污染动量类因子）。
    盘前语义：止于上一交易日，当日 bar 由载体用 tick 合成。
    English: last N daily bars, compact int rows; hithink source only."""
    tail = bars[-SERIES_BARS_N:]
    if len(tail) < MIN_SEQ_BARS:
        return None
    rows = []
    for b in tail:
        try:
            rows.append([int(round(b["high"] * SERIES_PRICE_SCALE)),
                         int(round(b["low"] * SERIES_PRICE_SCALE)),
                         int(round(b["close"] * SERIES_PRICE_SCALE)),
                         int(round(b["volume"])),
                         int(round(b.get("amount") or 0.0)),
                         int(str(b.get("date") or "0")[:8] or 0)])
        except (KeyError, ValueError, TypeError):
            continue
    if len(rows) < MIN_SEQ_BARS:
        return None
    return {"n": len(rows), "d0": int(str(tail[0].get("date") or "0")[:8] or 0),
            "scale": SERIES_PRICE_SCALE, "rows": rows}


def fetch_local_daily(symbol, db_path=LOCAL_DB):
    """云端自动研究库兜底（owner 14:2x 提示）：trading.db.daily（tushare 口径，全市场）。
    只读；vol 手→股。新鲜度以库为准（可能落后数日），返回 (bars, data_day) 或 (None, None)。"""
    import sqlite3
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True, timeout=5)
    except sqlite3.Error:
        return None, None
    try:
        rows = conn.execute(
            "SELECT trade_date, close, high, low, vol FROM daily WHERE ts_code=? "
            "ORDER BY trade_date DESC LIMIT ?", (symbol, MA_WINDOW + HIGH_LOW_N + 2)).fetchall()
    except sqlite3.Error:
        return None, None
    finally:
        conn.close()
    if not rows:
        return None, None
    bars = [{"close": r[1], "high": r[2], "low": r[3],
             "volume": float(r[4] or 0) * LOCAL_VOL_MULT} for r in reversed(rows)]
    return bars, rows[0][0]


def compute(bars):
    """盘前语义：所有窗口都止于上一交易日（盘前运行时当日K线不存在）。"""
    closes = [b["close"] for b in bars]
    vols = [b["volume"] for b in bars]
    highs = [b["high"] for b in bars]
    lows = [b["low"] for b in bars]
    if len(closes) < 2:
        return None
    ma_n = min(MA_WINDOW, len(closes))
    vol_n = min(VOL_WINDOW, len(vols))
    hl_n = min(HIGH_LOW_N, len(highs))
    return {
        "prev_close": round(closes[-1], 4),
        "ma_ref": round(sum(closes[-ma_n:]) / float(ma_n), 4),
        "vol_ma5": round(sum(vols[-vol_n:]) / float(vol_n), 2),
        "high10": round(max(highs[-hl_n:]), 4),
        "low10": round(min(lows[-hl_n:]), 4),
    }


def _q(sql, args):
    import sqlite3
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % LOCAL_DB, uri=True, timeout=5)
    except sqlite3.Error:
        return None
    try:
        return conn.execute(sql, args).fetchall()
    except sqlite3.Error:
        return None
    finally:
        conn.close()


def _ts_code(symbol):
    """6 位纯码按交易所推断 tushare 后缀——§TS-CODE-FIX 20261116：daily_basic/
    fina_indicator 的键是 600xxx.SH / 000xxx.SZ / 300xxx.SZ / 8xxxxx.BJ 全市场
    5546 只（实测新鲜 20261113），而 6 位码原样查询永远打空——300576 有 1663 行
    daily_basic 却被查成 0，估值/换手/财务/资金流四类 bakeables 因此集体缺烘
    （载体侧 TurnoverStd20/DebtToAssets/valuation 系列因子集体 declined 的根因）。
    English: infer the tushare suffix from the 6-digit code; passthrough when
    the code already carries one."""
    s = str(symbol or "").strip().upper()
    if "." in s:
        return s
    if len(s) == 6 and s.isdigit():
        head = s[0]
        if head == "6":
            return s + ".SH"
        if head in ("0", "3"):
            return s + ".SZ"
        if head in ("4", "8", "9"):
            return s + ".BJ"
    return s


def _effective_ann(ann_date, end_date):
    """D3（owner 2026-11-27）：公告日为空 ⇒ end_date + 120 天，**不丢整行**。

    为什么不是"严格按 ann_date 过滤、空的丢掉"：丢一行等于少一期财务，而多算
    一行未披露的报告是**前视偏差**（拿未来数据打分，回测会好看、实盘会打脸）。
    两个错误方向里，回退口径是保守的那个——120 天是 A 股年报/半年报/季报的
    法定披露上限附近，估计值只会把"可用"往后推，不会提前。
    English: fall back to end_date + 120 days when ann_date is empty (never drop).
    """
    text = str(ann_date or "").strip().replace("-", "").replace("/", "")
    if text:
        return text[:8]
    tail = str(end_date or "").strip().replace("-", "").replace("/", "")
    if not tail:
        return ""
    try:
        stamp = datetime.strptime(tail[:8], "%Y%m%d") + timedelta(days=ANN_FALLBACK_DAYS)
    except ValueError:
        return ""
    return stamp.strftime("%Y%m%d")


def load_sector_map(max_age_days=None):
    """个股 -> 东财板块名（生产者 scripts/pregen_sector_map_gz.py 的产物）。

    返回 (mapping, day)。表缺失/损坏/超龄一律返回 ({}, None)：板块代理是
    **回退腿**，缺了就是缺了，绝不拿一张很旧的表冒充今天的成分。
    """
    limit = SECTOR_MAP_MAX_AGE_DAYS if max_age_days is None else int(max_age_days)
    path = os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_sector_map.json")
    try:
        with open(path, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return {}, None
    mapping = doc.get("sector_map") or {}
    day = str(doc.get("day") or "")
    if not mapping or len(day) != 8 or not day.isdigit():
        return {}, None
    try:
        age = (datetime.now().astimezone().date()
               - datetime.strptime(day, "%Y%m%d").date()).days
    except ValueError:
        return {}, None
    if age > limit:
        print("PREGEN_SECTOR_MAP_STALE day=%s age_days=%d limit=%d" % (day, age, limit))
        return {}, None
    return mapping, day


def fetch_sector_flow_map(day):
    """板块资金流标量（sector_flow_daily）：{sector: {pct, pct5, z, day}}。

    口径（与 rules_factor 的 flow_resid / FlowToday 完全同尺度）：
      pct  = net_inflow / amount * 100          最新一日净流入率（%）
      pct5 = 近 5 日净流入合计 / 成交额合计 * 100
      z    = (pct - 前 20 日均值) / 标准差       残差 z（样本 < 8 就不给）
    只读、按板块聚合；缺表/缺行返回 {}（诚实弃权，绝不编 0——0 是"板块中性"，
    与"没有读数"是两件事）。
    """
    sql = ("SELECT sector, trade_date, net_inflow, amount FROM sector_flow_daily "
           "WHERE trade_date >= (SELECT MIN(trade_date) FROM ("
           "  SELECT DISTINCT trade_date FROM sector_flow_daily "
           "  ORDER BY trade_date DESC LIMIT ?))")
    rows = _q(sql, (SECTOR_FLOW_WINDOW + 5,)) or []
    buckets = {}
    for sector, trade_day, net, amount in rows:
        if sector is None or not amount or float(amount) <= 0.0:
            continue
        try:
            rate = float(net or 0.0) / float(amount) * 100.0
        except (TypeError, ValueError):
            continue
        buckets.setdefault(sector, []).append((str(trade_day), rate, float(net or 0.0), float(amount)))
    out = {}
    for sector, items in buckets.items():
        items.sort(key=lambda row: row[0])
        latest_day, latest_rate = items[-1][0], items[-1][1]
        tail5 = items[-5:]
        amount5 = sum(row[3] for row in tail5)
        pct5 = (sum(row[2] for row in tail5) / amount5 * 100.0) if amount5 > 0 else None
        history = [row[1] for row in items[-(SECTOR_FLOW_WINDOW + 1):-1]]
        resid = None
        if len(history) >= 8:
            mean = sum(history) / float(len(history))
            var = sum((value - mean) ** 2 for value in history) / float(len(history))
            if var > 0:
                resid = (latest_rate - mean) / (var ** 0.5)
        entry = {"pct": round(latest_rate, 6), "day": latest_day}
        if pct5 is not None:
            entry["pct5"] = round(pct5, 6)
        if resid is not None:
            entry["z"] = round(resid, 6)
        out[sector] = entry
    return out


def bake_bakeables(symbol, params_doc, lu_boards=None, market=None, float_shares=None, day=None):
    """§PRECALC v2（2026-11-11 owner 定案）：QMT 直取价量（bars），**QMT 拿不到的才烘**——
    估值序列/换手序列（daily_basic，当日新鲜）、财务分位（fina_indicator 最新档）、
    资金流（个股 json 优先 / 板块代理回退）、新闻事件=D1 分数（scalar_proxy 带值）、
    概念热度=当日利好池强度分（同为 scalar_proxy）。形态逐一对齐 rules_factor 的消费端：
    valuation_series={"pe_ttm":[..],..}（_field_series 面板）、fin_pit={"eps":..}（_pit_value）、
    flow/heat 标量（_scalar_score + params.scalar_bands）。缺源的字段如实缺省（绝不编造）。

    §P3-FACTOR-FIX 2026-11-27 新增（PLAN_FACTOR_FIX_P0_20261126 §4.2）：
      stom_21 / cmra / flow_resid / fin_series 四个新标量；total_share_series 改用腾讯
      流通股本（daily_basic.total_share 现网全 NULL，LnMktCap 结构性死）；个股资金流
      不新鲜时用板块净流入率代理（flow_src 如实标注来源）。"""
    ts = _ts_code(symbol)
    out = {}
    n = BAKE_SERIES_N
    bake_day = str(day or datetime.now().astimezone().strftime("%Y%m%d"))
    # §WIRING-B 2026-11-17：valuation 面板补齐 pcf_ttm/dv_ttm（旧只 SELECT pe/pb/ps ⇒
    # CFP_ttm 权重 0.299 与 DP 的 fac_ 规则恒 declined），并带 total_share 喂 LnMktCap。
    # daily_basic 列都已建且 fresh 11-16，纯加列、不改源。
    rows = _q("SELECT trade_date,pe_ttm,pb,ps_ttm,pcf_ttm,dv_ttm,turnover_rate,total_share "
              "FROM daily_basic WHERE ts_code=? AND pe_ttm IS NOT NULL "
              "ORDER BY trade_date DESC LIMIT ?", (ts, n)) \
        or _q("SELECT trade_date,pe_ttm,pb,ps_ttm,pcf_ttm,dv_ttm,turnover_rate,total_share "
              "FROM daily_basic WHERE ts_code=? ORDER BY trade_date DESC LIMIT ?", (ts, n))
    if rows:
        val = {"pe_ttm": [r[1] for r in rows if r[1] is not None],
               "pb": [r[2] for r in rows if r[2] is not None],
               "ps_ttm": [r[3] for r in rows if r[3] is not None],
               "pcf_ttm": [r[4] for r in rows if r[4] is not None],
               "dv_ttm": [r[5] for r in rows if r[5] is not None]}
        val = {k: v for k, v in val.items() if v}
        if val:
            out["valuation_series"] = val
        tos = [r[6] for r in rows if r[6] is not None]
        if tos:
            out["turnover_series"] = tos
        # §P3 2026-11-27：total_share 现网**全 NULL**（daily_basic 实测 COUNT(total_share)=0），
        # 旧写法于是从不烘 total_share_series ⇒ LnMktCap 结构性死。真列有值时仍以真列为准
        # （那才是"总市值"口径），否则回退 main() 已取到的腾讯**流通股本**（976/997 覆盖）。
        # 口径变化要在注释里写明：回退腿烘出来的 LnMktCap 是**流通市值**对数，不是总市值
        # 对数——同一条腿、同一个量级，但口径不同。
        # 烘成同值序列（不是单元素）：percentile 至少要 5 个样本，单元素会被判样本不足。
        tsh = [r[7] for r in rows if r[7] is not None]
        if tsh:
            out["total_share_series"] = tsh
        elif float_shares:
            out["total_share_series"] = [float(float_shares)] * n
    # §P3 2026-11-27（owner D3）：财务档加 ann_date 前视闸。旧写法 ORDER BY end_date
    # DESC LIMIT 1 直接把"最近报告期"当已知——盘前会把尚未披露的报告算进来（前视偏差）。
    # 现在取 40 期、按有效公告日（空则 end_date+120）过滤后取最新一档，并顺带烘
    # fin_series（最近 6 个已披露报告期的 roe，最旧在前——percentile 读末位为最新）。
    fin_rows = _q("SELECT eps,roe,roe_waa,roa,grossprofit_margin,netprofit_margin,"
                  "debt_to_assets,yoy_or,yoy_net_profit,ann_date,end_date FROM fina_indicator "
                  "WHERE ts_code=? ORDER BY end_date DESC LIMIT 40", (ts,)) or []
    disclosed = []
    for row in fin_rows:
        if len(row) < 11:
            continue
        ann = _effective_ann(row[9], row[10])
        if ann and ann <= bake_day:
            disclosed.append(row)
    if disclosed:
        fin = disclosed[0]
        cols = ["eps", "roe", "roe_waa", "roa", "grossprofit_margin",
                "netprofit_margin", "debt_to_assets", "yoy_or", "yoy_net_profit"]
        # 2026-11-27 wiring: factor_defs.YoyNetProfit reads fin_pit["yoy_net_profit"],
        # but the old SELECT never fetched that column (21416 non-null rows in the
        # live DB) -- the leg was structurally absent. Fetch it and let the same
        # point-in-time ann_date gate decide whether it is known.
        pit = {c: v for c, v in zip(cols, fin[:9]) if v is not None}
        # §FIN-ALIAS 20261116：factor_defs 的 financial 家族读 fin_pit["gross_margin"]
        # /["net_margin"]（factor_defs GrossMargin.field=gross_margin），而 tushare
        # 列名是 grossprofit_margin/netprofit_margin——键名错位让 GrossMargin/NetMargin
        # 因子 declined（fac_150 实证）。别名对齐，原键保留不破坏旧消费。
        if "grossprofit_margin" in pit:
            pit["gross_margin"] = pit["grossprofit_margin"]
        if "netprofit_margin" in pit:
            pit["net_margin"] = pit["netprofit_margin"]
        if pit:
            out["fin_pit"] = pit
        # fin_series：最近 6 个**已披露**报告期的 roe，最旧在前。
        # 少于 2 期不烘（percentile 至少要 5 个样本，这里只保证形状正确；样本不足
        # 时 rules_factor 的 time_series_percentile 会自己弃权）。
        roes = [row[1] for row in disclosed if row[1] is not None][:6]
        if len(roes) >= 2:
            out["fin_series"] = list(reversed([float(v) for v in roes]))
        # 2026-11-27 wiring: factor_defs.SUE reads fin_pit["single_quarter_ni_yoy"],
        # a field no tushare table carries. Derive it from the cumulative
        # n_income_attr_p panel in income: single quarter = Qn - Q(n-1), then
        # YoY against the same quarter one year earlier. Only disclosed reports
        # (same ann_date gate as the rest of fin_pit) may contribute.
        ann_by_end = {}
        for row in fin_rows:
            if len(row) >= 11:
                ann_by_end[str(row[10])] = _effective_ann(row[9], row[10])
        inc_rows = _q("SELECT end_date,n_income_attr_p FROM income "
                      "WHERE ts_code=? ORDER BY end_date DESC LIMIT 16", (ts,)) or []
        cum = {}
        for end_date, val in inc_rows:
            ann = ann_by_end.get(str(end_date))
            if ann and ann <= bake_day and val is not None:
                cum[str(end_date)] = float(val)
        for end_date in sorted(cum, reverse=True):
            year, month = int(end_date[:4]), int(end_date[4:6])
            q = (month - 1) // 3 + 1
            if q < 1 or q > 4:
                continue
            # Same-quarter lookup uses the report end month/day from the panel,
            # not a fabricated 31st: find any prior-year row with the same MMDD.
            prev_same = None
            if prev_same is None:
                for k, v in cum.items():
                    if k[:4] == str(year - 1) and k[4:] == end_date[4:]:
                        prev_same = v
                        break
            if q == 1:
                single = cum[end_date]
            else:
                prev_cum = None
                for k, v in cum.items():
                    if k[:4] == str(year) and (int(k[4:6]) - 1) // 3 + 1 == q - 1:
                        prev_cum = v
                        break
                if prev_cum is None:
                    continue
                single = cum[end_date] - prev_cum
            if prev_same in (None, 0.0) or single is None:
                continue
            out.setdefault("fin_pit", {})["single_quarter_ni_yoy"] = round(
                (single / abs(prev_same) - 1.0) * 100.0, 4)
            break
    code6 = symbol.split(".")[0]
    # §P3 2026-11-27（owner D2 已批准带值）：两枚盒内拿不到的标量。
    #   stom_21 = Σ ln(1 + 换手率%/100)，最近 21 个交易日 —— "活跃度"的累计量
    #   cmra    = 最近 253 根收盘的累计对数收益极差 —— "趋势跨度"
    # 都只烘**标量**，不烘 253 根序列（体积换不来信息：消费端要的就是极差）。
    trs = _q("SELECT turnover_rate FROM daily_basic WHERE ts_code=? AND turnover_rate IS NOT NULL "
             "ORDER BY trade_date DESC LIMIT 21", (ts,)) or []
    if len(trs) >= 21:
        out["stom_21"] = round(sum(math.log(1.0 + float(r[0]) / 100.0)
                                   for r in trs if r[0] is not None), 6)
    closes = [float(r[0]) for r in (_q(
        "SELECT close FROM daily WHERE ts_code=? AND trade_date<=? AND close>0 "
        "ORDER BY trade_date DESC LIMIT 253", (ts, bake_day)) or []) if r and r[0]]
    if len(closes) >= 253:
        # 累计对数收益的基准取最旧一根；极差对基准的常数平移不敏感，口径稳定。
        base = closes[-1]
        acc, cum = 0.0, []
        for value in reversed(closes):
            acc += math.log(value / base)
            cum.append(acc)
        out["cmra"] = round(max(cum) - min(cum), 6)
    # §REPLAY-INPUTS 2026-11-28（档 3/档 4）：资金流输入**优先读库**。
    # 原来只读 `<DataDir>/moneyflow_history/<code>.json`——那是引擎"偶然碰到某只票才缓存"的副产品
    # （现网实测 2870 个文件里 68.1% 的末根已超过 5 天），既不是采集也没有覆盖率。
    # 现在唯一写口是 `quant-moneyflow.service`（15:35 timer，上游 120 根滚动窗口），
    # json 只作**旧库尚未回填时的兜底**：它没有 f57 净占比，因此只能支撑"占流通市值"那一条腿，
    # 绝不能拿它去填 flow_today_pct——那会把"占成交额"的键写成另一种量纲（本批要消灭的缺陷）。
    # 先落一个 False：库路径命中后置 True，下面的 json 兜底与板块兜底都**只在还没数时**才写。
    # 这个顺序不能颠倒——被后面的 `stock_flow_done = False` 抹掉的话，
    # 板块腿会覆盖刚拿到的个股读数，而两边代码各自看起来都完全正确。
    stock_flow_done = False
    # ── 库路径（首选）：stock_moneyflow_daily 由 15:35 的 systemd timer 天天追加 ──
    # 判据分两层，各管各的，不许互相顶班：
    #   当日档：取"不晚于烘参日的最后一行"，再按 FLOW_FRESH_DAYS 判新鲜——
    #     与 json 路径同语义（盘前跑时"今日"结构上不存在，判据只能是末根账龄而不是必须今天）。
    #   5 日档：要求那 5 天是该股**自己的连续交易日**（daily 序列）且每天都有行、成交额都在。
    #     中间断一天还硬算，等于拿 6 日窗口的比率冒充 5 日窗口——数值正常、语义已变。
    try:
        flow_rows = _q(
            "SELECT trade_date, main_net, main_pct, amount FROM stock_moneyflow_daily "
            "WHERE ts_code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT %d" % FLOW_WINDOW,
            (ts, bake_day)) or []
        session_days = [str(r[0]) for r in (_q(
            "SELECT trade_date FROM daily WHERE ts_code=? AND trade_date<=? "
            "ORDER BY trade_date DESC LIMIT %d" % FLOW_WINDOW, (ts, bake_day)) or [])]
    except TypeError:
        flow_rows, session_days = [], []
    have = {str(r[0]): r for r in flow_rows}
    if have:
        newest = max(have.keys())
        fresh = False
        try:
            age = (datetime.strptime(bake_day, "%Y%m%d").date()
                   - datetime.strptime(newest, "%Y%m%d").date()).days
            fresh = 0 <= age <= FLOW_FRESH_DAYS
        except ValueError:
            fresh = False
        if fresh:
            row = have[newest]
            if row[2] is not None:
                out["flow_today_pct"] = round(float(row[2]), 4)
            window = [d for d in session_days if d <= newest][:FLOW_WINDOW]
            if len(window) == FLOW_WINDOW and all(d in have for d in window):
                nets = [float(have[d][1] or 0.0) for d in window]
                amts = [have[d][3] for d in window]
                if all(a is not None and float(a) > 0 for a in amts):
                    out["flow_5d_pct"] = round(
                        sum(nets) / sum(float(a) for a in amts) * 100.0, 4)
            cap_den = 0.0
            if float_shares and closes:
                cap_den = float(float_shares) * closes[0]
            elif circ and circ[0] and circ[0][0]:
                cap_den = float(circ[0][0]) * 10000.0  # circ_mv 万元 -> 元
            if cap_den > 0:
                # ★ 精度必须是 8 位，不是沿用上面那两条的 4 位：
                #   占流通市值这个量纲天生是 1e-5 级（5000 万净额 / 1.2e10 市值 ≈ 4.2e-4 %）。
                #   旧写法 round(...,4) 会把绝大多数票**直接压成 0.0**——现网实测确实如此，
                #   于是 band(-0.5..1.2) 把它恒定读成 0.29 分，这条腿实际上一直在给常数。
                #   这是本批新抓到的成因（除了 68% 末根陈旧之外的第二个"腿在但等于没用"）。
                out["flow_cap_today_pct"] = round(float(row[1] or 0.0) / cap_den * 100.0, FLOW_CAP_ROUND)
                if "flow_5d_pct" in out:
                    out["flow_cap_5d_pct"] = round(sum(nets) / cap_den * 100.0, FLOW_CAP_ROUND)
            out["flow_src"] = "stock_db"
            out["flow_src_day"] = newest
            stock_flow_done = True

    flow_path = os.path.join(FLOW_DIR, code6 + ".json")
    circ = _q("SELECT circ_mv FROM daily_basic WHERE ts_code=? AND circ_mv IS NOT NULL "
              "ORDER BY trade_date DESC LIMIT 1", (ts,))
    try:
        flow_doc = json.load(open(flow_path, encoding="utf-8")) if os.path.exists(flow_path) else None
    except (OSError, ValueError):
        flow_doc = None
    if flow_doc and isinstance(flow_doc.get("days"), list) and flow_doc["days"]:
        days = flow_doc["days"][-5:]
        mains = [float(d.get("MainNet") or 0) for d in days]
        last_day = str(days[-1].get("Date") or "").replace("-", "")[:8]
        # 新鲜度：pregen 是盘前跑的，"今日"的个股资金流结构上不存在 ⇒ 判据是
        # "末根在最近 FLOW_FRESH_DAYS 个自然日内"，不是"必须是今天"。
        fresh = False
        if len(last_day) == 8 and last_day.isdigit():
            try:
                age = (datetime.strptime(bake_day, "%Y%m%d").date()
                       - datetime.strptime(last_day, "%Y%m%d").date()).days
                fresh = 0 <= age <= FLOW_FRESH_DAYS
            except ValueError:
                fresh = False
        if fresh:
            # 分母：circ_mv 现网全 NULL（旧写法因此从不烘 flow_*），改用
            # 流通股本 × 最新收盘（与 LnMktCap 同源）；两者都缺才回退 circ_mv。
            cap = 0.0
            if float_shares and closes:
                cap = float(float_shares) * closes[0]
            elif circ and circ[0] and circ[0][0]:
                cap = float(circ[0][0]) * 10000.0  # circ_mv 万元 -> 元
            # §REPLAY-INPUTS 拆两键（owner 2026-11-28 裁决 Q3）：
            #   flow_today_pct / flow_5d_pct   = 主力净额 ÷ **成交额** ×100（与大盘腿、板块腿同一把尺）
            #   flow_cap_today_pct / flow_cap_5d_pct = 主力净额 ÷ **流通市值** ×100（原公式，留档备后续）
            # 本 json 分支只有净额、没有占比，因此**只写 cap 那一侧**；
            # 占成交额那一侧交给下面的库路径或板块兜底腿，绝不拿另一种量纲冒充。
            if cap > 0 and "flow_cap_today_pct" not in out:
                # 同上：cap 量纲要 8 位精度，4 位会把绝大多数票压成 0。
                out["flow_cap_today_pct"] = round(mains[-1] / cap * 100.0, FLOW_CAP_ROUND)
                if len(mains) >= 2:
                    out["flow_cap_5d_pct"] = round(sum(mains) / cap * 100.0, FLOW_CAP_ROUND)
                out["flow_src"] = "stock_json_cap_only"
                out["flow_src_day"] = last_day
                # json 只有净额、没有占比 ⇒ 占成交额那一侧仍无读数，
                # 所以**不置 stock_flow_done**：板块兜底仍可去补 flow_today_pct（同一量纲）。
    # 第二源（板块级回退）：个股资金流不新鲜时，用该股所属板块的净流入率代理。
    # flow_resid 是**板块级** z 分（与个股级 flow_today_pct/flow_5d_pct 分属两条腿），
    # 与个股腿是否新鲜无关，只要有板块表就烘。两源都不许"编 0"：都缺就不烘键。
    # market 允许为 None（老调用方/用例只传 params_doc）：缺板块上下文就是"没有板块腿"，
    # 不是崩溃点——沿用本文件一贯的"缺数弃权"口径。
    market = market or {}
    sector_flow = market.get("sector_flow") or {}
    boards = sector_flow.get("boards") or {}
    sector_name = (market.get("sector_map") or {}).get(code6)
    entry = boards.get(sector_name) if sector_name else None
    if isinstance(entry, dict):
        if not stock_flow_done:
            out["flow_today_pct"] = entry["pct"]
            if entry.get("pct5") is not None:
                out["flow_5d_pct"] = entry["pct5"]
            out["flow_src"] = "sector"
            out["flow_src_day"] = entry.get("day")
        if entry.get("z") is not None:
            out["flow_resid"] = entry["z"]
    # §D1-LEVEL 2026-11-17：带出当日新闻级别，烘 news_propagation（good_news_fade 的
    # dict 四元组缺的就是这枚）。D1 日更只记当天，旧行 level 空串=未知→不烘（诚实弃权）。
    d1 = _q("SELECT score, date, level FROM d1_scores WHERE code=? ORDER BY created_at DESC LIMIT 1",
            (code6,))
    if d1 is None:
        # 回退：schema 迁移尚未跑到本库（旧库无 level 列）——保住原有 news_event 烘值，
        # 只是没有 propagation（诚实弃权），绝不因缺列把 D1 分一起丢掉。
        d1 = _q("SELECT score, date FROM d1_scores WHERE code=? ORDER BY created_at DESC LIMIT 1",
                (code6,))
    # §NEWS-GATE 2026-11-12（R8/R12）：D1 改带符号分 [−40,+40] 原样烘入——负分=明确利空
    # 直接下传（载体负分拒买闸与 NewsEvent 负带消费），不再 blocked→0 抹平（旧 Blocked 标记废除，
    # d1_scores.blocked 列已由引擎按 score<0 派生，无需在此读）。0/缺行=无事件（中性，不烘键）。
    if d1 and d1[0] and d1[0][0] is not None:
        score = float(d1[0][0])
        if score != 0:
            out["news_event"] = score
        if score < 0:
            out["news_negative"] = True
        # §WIRING-B 2026-11-17：good_news_fade 还要 event.day 定位事件那根 bar。
        # d1_scores.date 即评估交易日（带横线），烘成 yyyymmdd 喂 _news_event_obj。
        if d1[0][1]:
            out["news_day"] = str(d1[0][1]).replace("-", "")[:8]
        # §D1-LEVEL 2026-11-17：Level 非空且不含"个股" = 会传导（与 Go adapter.go 同口径：
        # 板块/宏观级事件会传导、个股级不会）。缺/空 Level 不烘，good_news_fade 诚实弃权。
        # 只取当日 D1 行的 Level——D1 日更、过期不存，不回溯历史（owner 口径）。
        if len(d1[0]) > 2 and d1[0][2]:
            # 明确有 Level：显式烘 True/False（个股级不传导=False，板块/宏观=会传导=True）。
            # 空 Level（本列落地前的旧行/未知）不烘→propagation None→诚实弃权，不回溯。
            _lv = str(d1[0][2])
            out["news_propagation"] = "个股" not in _lv
    hp = (params_doc.get("hot_pool") or {}).get("bullish") or {}
    hit = hp.get(symbol) or hp.get(code6)
    if isinstance(hit, dict) and hit.get("score") is not None:
        out["concept_heat"] = float(hit["score"])
    # §LIMIT-BOARD 20261116：连板数按 input_key=limit_board_count 键名烘入
    # （scalar_proxy 的 ctx 取值即同名键）。**池外票烘 0**：外脑对"今日未涨停"
    # 的票有值 0 照算（lu_board3 在 fac_160 权重仅 0.199，主体是价量因子），
    # 若烘 absent 则整条规则 declined、永远与外脑分叉——0 与 absent 语义在
    # percentile 排名里不同，这里必须取 0 对齐。池数据整体缺失（拉取失败）时
    # 仍走 absent（真实缺源绝不编造）。
    if lu_boards is not None:
        out["limit_board_count"] = float(lu_boards.get(code6) or 0.0)
    # §WIRING-B 2026-11-17：把"做了一半"的情绪/涨停池类读数补齐（全部从已拉/新鲜数据派生，
    # 不另开多日扫描，避免烘错值污染买入判定）。market 由 main() 算一次传入。
    market = market or {}
    # 2026-11-27 wiring: hot_pool.bullish only covers ~30/997 codes, so the
    # ConceptHeat leg of fac_176/177 abstained for almost every stock. Fall back
    # to a LOCAL proxy with an explicit source label: percentile rank of the
    # stock's board net-inflow rate among all boards in sector_flow_daily.
    # This is NOT the external concept ranking; it is marked as a proxy so the
    # semantic drift is visible instead of silently pretending to be the feed.
    if out.get("concept_heat") is None:
        _boards = ((market.get("sector_flow") or {}).get("boards") or {})
        _bname = (market.get("sector_map") or {}).get(code6)
        _entry = _boards.get(_bname) if _bname else None
        if isinstance(_entry, dict) and _entry.get("pct") is not None:
            _vals = sorted(float(v.get("pct")) for v in _boards.values()
                           if isinstance(v, dict) and v.get("pct") is not None)
            if len(_vals) >= 20:
                _pct = float(_entry["pct"])
                out["concept_heat"] = round(
                    sum(1 for x in _vals if x < _pct) * 100.0 / len(_vals), 2)
                out["concept_heat_src"] = "sector_flow_proxy"
    # 全市场涨停家数 + 当日最高连板（lu_boards 已含每票连板数，max/len 直取，无需回看历史）。
    if "limit_up_count" in market:
        out["limit_up_count"] = market["limit_up_count"]
    if "ladder_height" in market:
        out["ladder_height"] = market["ladder_height"]
    # 板块龙头（leader_decay 要 is_leader）：sector_history.top_stocks membership，bool 显式。
    if "leader_set" in market:
        out["is_leader"] = bool(code6 in market["leader_set"])
    # §WIRING-B+ 2026-11-17：全市场情绪历史是账户级标量（每票同值），market 已在 main() 聚合一次。
    # 历史列表进 ctx 供 owner 后续把 emo_chg5/lu_reblast_low 从 scalar_proxy 改接序列消费端；
    # 现阶段无 scalar_bands ⇒ rules_factor 仍弃权（列表喂 _as_number=None，与缺席同形、不假打分）。
    # broken_board_ratio/count 给最近一交易日的标量（含 emotion_day 让 as-of 可见），单位见
    # fetch_market_history 注释（0..1 比值）——owner 标定 band 时须对齐。
    for _mk in ("limit_up_count_history", "broken_board_ratio_history",
                "broken_board_count", "broken_board_ratio", "emotion_day",
                "ladder_height_history", "broken_board_count_history"):
        if _mk in market:
            out[_mk] = market[_mk]
    # 涨停价（leader_decay 的 up_stop = 今日板价）：stk_limit 最新一行的 up_limit。
    up = _q("SELECT up_limit FROM stk_limit WHERE ts_code=? ORDER BY trade_date DESC LIMIT 1",
            (ts,))
    if up and up[0] and up[0][0] is not None:
        out["up_stop"] = float(up[0][0])
    # §CONSEC-BOARDS 2026-11-17（leader_decay_v2 的否决项）：今日之前的连续涨停
    # 天数，从 daily+stk_limit 逐日算（不能用 pool 的 ladder_height 代替）。
    # 盘前跑 ⇒ 最新日K即昨日 ⇒ 正好等于盘中规则要的"今日之前连板数"。
    # 无历史 ⇒ 不烘 ⇒ v2 诚实弃权（绝不编一个连板数）。
    consec = compute_consec_boards(ts)
    if consec is not None:
        out["consec_boards"] = float(consec)
    # §SECTOR-DROP 2026-11-17（owner：sector_limit_up_drop_pct 走云端烘）：该股所属
    # 板块的涨停家数较昨日降幅。industry_map/sector_drop_map 由 main() 一次查好放
    # market（避免每票查库）。本票查不到行业或该板块降幅无定义 ⇒ 不烘 ⇒ 消费侧
    # 安全降级为 0（Go adapter "empty = 0"，绝不为缺失编一个降幅）。
    _ind_map = market.get("industry_map")
    _drop_map = market.get("sector_drop_map")
    if isinstance(_ind_map, dict) and isinstance(_drop_map, dict):
        _industry = _ind_map.get(ts)
        if _industry is not None and _industry in _drop_map:
            out["sector_limit_up_drop_pct"] = _drop_map[_industry]
    # §WIRING-B 2026-11-17 lu_seal_strength/lu_early_seal：与 Go research/assemble.go
    # 逐字同口径（scaler_bands 照这套峰值口径标定，绝不能用东财池"当前 fund"替代——
    # 那是另一分布＝漂移＝钱路径分叉，正是本仓反复防的形状）。
    #   seal_amount_ratio = max_seal_money / (circ_mv 万元 ×1e4)  （峰值封单/流通市值）
    #   first_seal_minute = HH*60+MM（first_seal_time "HH:MM"，如 09:41→581；早封阈值 600=10:00）
    # 只在该票有涨停事件行(continue_cnt>=1)时烘；ths_limit_up_daily 缺该行就不烘（诚实弃权，
    # 绝不拿非事件日/缺失值编一个 ratio=0——0 与 absent 在分档里语义不同）。
    # English: matches engine assemble.go exactly; peak seal (max_seal_money), not EM current fund.
    seal = _q("SELECT continue_cnt, first_seal_time, max_seal_money FROM ths_limit_up_daily "
              "WHERE ts_code=? ORDER BY trade_date DESC LIMIT 1", (ts,))
    if seal and seal[0] and seal[0][0] is not None and int(seal[0][0]) >= 1:
        fst, max_seal = seal[0][1], seal[0][2]
        # 2026-11-27 wiring: circ_mv is all NULL in daily_basic, so the peak-seal
        # leg never baked seal_amount_ratio (0/997 coverage). Fall back to the
        # same流通市值 denominator already used by flow: tencent float_shares
        # x latest close. Same unit (yuan), same band, no invented zero.
        cap_yuan = 0.0
        if circ and circ[0] and circ[0][0]:
            cap_yuan = float(circ[0][0]) * 10000.0
        elif float_shares and float_shares > 0 and closes:
            cap_yuan = float(float_shares) * float(closes[0])
        if cap_yuan > 0 and max_seal and float(max_seal) > 0:
            out["seal_amount_ratio"] = round(float(max_seal) / cap_yuan, 6)
        if fst and len(str(fst)) >= 4 and ":" in str(fst):
            try:
                hh, mm = str(fst).split(":")[0], str(fst).split(":")[1]
                out["first_seal_minute"] = float(int(hh) * 60 + int(mm))
            except (ValueError, IndexError):
                pass
    return out


# ---------------------------------------------------------------------------
# §TURNOVER-WIRING 2026-11-19（owner：补烘流通股本）：T 的换手率闸需要流通股本，
# 而本地库里没有（trading.db.daily_basic.float_share 全 NULL、circ_mv 也全 NULL，
# tushare 侧没 token）。腾讯行情是现成的、可用的（本仓行情链本来就有腾讯腿），
# 一条请求可带 50 只票，字段里同时有**流通市值**与**流通股本**。
# 只烘 float_shares（股），换手率由载体用「当日成交额 ÷ (昨收 × 流通股本)」现算——
# 不烘换手率本身：那是盘中变量，烘成固定值等于给闸喂一个错的常数。
TENCENT_QUOTE = "http://qt.gtimg.cn/q="
TENCENT_BATCH = 50
_TENCENT_HEADERS = {"User-Agent": "Mozilla/5.0", "Referer": "https://gu.qq.com/"}


def _tencent_code(ts_code):
    """`000034` / `000034.SZ` -> `sz000034`（腾讯要「小写交易所前缀 + 6 位码」）。

    2026-11-19 首次真跑踩到的坑：params.symbols 里是**裸 6 位码**，旧实现原样拼进 URL
    ⇒ 腾讯回 `v_pv_none_match`，整批解析为空，`float_shares` 一只都没烘进去（0/993）。
    所以裸码必须按首位补前缀：6/9 沪、0/2/3 深、4/8 北交所。
    """
    text = str(ts_code or "").strip().upper()
    if "." in text:
        code, suffix = text.split(".", 1)
        prefix = {"SH": "sh", "SZ": "sz", "BJ": "bj"}.get(suffix, "sz")
        return prefix + code
    if len(text) == 6 and text.isdigit():
        if text[0] in ("6", "9"):
            return "sh" + text
        if text[0] in ("4", "8"):
            return "bj" + text
        return "sz" + text
    return text.lower()


def parse_tencent_float_shares(payload):
    """腾讯行情响应 -> {ts_code: float_shares(股)}。纯函数（拆出来才能单测）。

    字段位（实测 sz000034，2026-11-19）：
      [3] 现价、[38] 换手率、[44] 流通市值（亿）、[45] 总市值（亿）、
      [72] 流通股本（股）、[73] 总股本（股）。
    为什么两条路：不同板块/停牌/指数的字段位会漂，所以先认 [72]，再用
    「流通市值 × 1e8 ÷ 现价」交叉校验（差 >10% 就认为 [72] 不是股本，改用推导值）；
    两条都拿不到就**不烘**（载体侧继续保持弃权，绝不喂一个猜出来的股本）。
    """
    out = {}
    for chunk in str(payload or "").split(";"):
        chunk = chunk.strip()
        if not chunk.startswith("v_"):
            continue
        head, _, body = chunk.partition("=")
        code = head[2:].strip()
        fields = body.strip().strip('"').split("~")
        if len(fields) < 46 or not code:
            continue
        ts_code = None
        if code[:2] in ("sz", "sh", "bj") and code[2:].isdigit():
            ts_code = code[2:] + ("." + code[:2].upper())
        if ts_code is None:
            continue

        def _num(idx):
            try:
                return float(fields[idx])
            except (IndexError, TypeError, ValueError):
                return None

        price = _num(3)
        circ_mv_yi = _num(44)
        shares = _num(72)
        derived = None
        if price and price > 0 and circ_mv_yi and circ_mv_yi > 0:
            derived = circ_mv_yi * 1e8 / price
        if shares and shares > 0:
            if derived and abs(shares - derived) / derived > 0.10:
                shares = derived
        else:
            shares = derived
        if shares and shares > 0:
            out[ts_code] = shares
    return out


def fetch_listed_days(day):
    """F3 2026-11-25: 上市天数（baostock query_stock_basic.ipoDate）-> {6位码: 天数}。

    为什么需要：`t_min_listed_days=60`（新股不做的风控）在全仓没有任何生产者
    ——云端 `stocks.list_date` 实测覆盖 **0/5546**（2026-11-25 直读），
    于是 `rules_t.t_liquidity_gate` 每轮返回 `listed_days_unavailable`，
    11-23 的 255 轮 / 11-24 的 270 轮 T 腿全部倒在这条闸上。

    owner 2026-11-25 裁决：**先 A（闸关）后 B（数据覆盖≥95% 自动切 B）**。
    切换判据是**数据覆盖**，所以生产者必须先存在，B 档才有机会自动生效。

    源已实测（2026-11-25，gz）：`bs.query_stock_basic()` 返回 9061 行，
    字段 `code/code_name/ipoDate/outDate/type/status`，`type=='1'` 是股票
    （`2` 是指数——`sh.000001` 是上证指数，不过滤会把 000001 写成 1991 年）。
    一次全量调用即可，不需要逐票请求。

    纪律：库缺失/登录失败/解析失败一律返回 `{}`（缺源不阻断主链，与
    `fetch_float_shares` 同一条）；`ipoDate` 为空的行跳过，绝不发明一个天数。
    """
    try:
        import baostock as bs  # noqa: F401 - 可选依赖，缺了就是"没有这一维"
    except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链
        print("PREGEN_LISTED_DAYS_FAIL import %s" % type(exc).__name__)
        return {}
    try:
        today = datetime.strptime(str(day), "%Y%m%d")
    except (TypeError, ValueError):
        print("PREGEN_LISTED_DAYS_FAIL bad_day=%s" % day)
        return {}
    out = {}
    try:
        login = bs.login()
        if str(getattr(login, "error_code", "1")) != "0":
            print("PREGEN_LISTED_DAYS_FAIL login %s" % getattr(login, "error_msg", ""))
            return {}
        rs = bs.query_stock_basic()
        while rs.error_code == "0" and rs.next():
            row = dict(zip(rs.fields, rs.get_row_data()))
            if str(row.get("type") or "") != "1":
                continue  # 指数/其它：`sh.000001` 与平安银行同码，混进来就是错的天数
            ipo = str(row.get("ipoDate") or "")
            if not ipo:
                continue
            try:
                days = (today - datetime.strptime(ipo, "%Y-%m-%d")).days
            except ValueError:
                continue
            if days < 0:
                continue
            code = str(row.get("code") or "").split(".")[-1]
            if len(code) == 6 and code.isdigit():
                out[code] = days
    except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链
        print("PREGEN_LISTED_DAYS_FAIL query %s" % type(exc).__name__)
        return {}
    finally:
        try:
            bs.logout()
        except Exception:  # noqa: BLE001 - 登出失败无关结果
            pass
    return out


def fetch_float_shares(symbols):
    """批量取流通股本（腾讯，50 只/请求）。失败/缺字段一律跳过（返回 {} 也合法）。"""
    result = {}
    codes = [s for s in symbols if s]
    for start in range(0, len(codes), TENCENT_BATCH):
        batch = codes[start:start + TENCENT_BATCH]
        url = TENCENT_QUOTE + ",".join(_tencent_code(c) for c in batch)
        try:
            req = urllib.request.Request(url, headers=_TENCENT_HEADERS)
            with urllib.request.urlopen(req, timeout=REQ_TIMEOUT) as resp:
                payload = resp.read().decode("gbk", errors="replace")
        except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链，只少一份股本
            print("PREGEN_FLOAT_SHARES_FAIL batch=%d %s" % (start // TENCENT_BATCH, type(exc).__name__))
            continue
        # 回键用**裸 6 位码**：params.symbols / precalc 的键都是裸码（带后缀查不到）。
        result.update({k.split(".")[0]: v for k, v in parse_tencent_float_shares(payload).items()})
        time.sleep(SLEEP_MS / 1000.0)
    return result


def fetch_lu_boards(day):
    """§LIMIT-BOARD 20261116（owner 拍板接入 lu_board3）：东财涨停池直拉
    （akshare stock_zt_pool_em 同源底层，无需 venv 依赖）→ {6位码: 连板数}。
    连板数是**当日实时事件数据**（首板=1、2 连板=2…；池外票=未涨停，消费侧
    缺省弃权——外脑同口径）。失败返回 None：涨停池缺源时 limit_board_count
    因子如实 declined，绝不编造（同 fetch 的单票纪律）。hithink 不提供涨停池，
    东财仅作此一项事件数据源（日线主源仍是 owner 指定的 hithink）。
    English: EM limit-up pool -> {code: consecutive-limit count}; None on failure."""
    url = ("https://push2ex.eastmoney.com/getTopicZTPool"
           "?ut=7eea3edcaed734bea9cbfc24409ed989&dpt=wz.ztzt"
           "&Pageindex=0&pagesize=10000&sort=fbt%%3Aasc&date=%s" % day)
    req = urllib.request.Request(url, headers={
        "User-Agent": "Mozilla/5.0 (quant-pregen)",
        "Referer": "https://quote.eastmoney.com/",
    })
    with urllib.request.urlopen(req, timeout=REQ_TIMEOUT) as resp:
        doc = json.loads(resp.read().decode("utf-8", errors="replace"))
    pool = ((doc or {}).get("data") or {}).get("pool") or []
    boards = {}
    for item in pool:
        code = str(item.get("c") or "").strip()
        lbc = item.get("lbc")
        if len(code) == 6 and code.isdigit() and lbc is not None:
            try:
                boards[code] = float(lbc)
            except (TypeError, ValueError):
                continue
    return boards or None


def fetch_market_history(day, lookback=20):
    """§WIRING-B+ 2026-11-17（owner：把能算的都算出来并持久化）：全市场情绪历史——
    涨停家数 / 炸板家数 / 炸板率——按交易日从**权威落库表** ths_limit_up_daily +
    ths_break_pool_daily 聚合。这两张表正是引擎 internal/data/risk_pools.go 与
    emotion.go 的取数源（连板=ContinueDayCnt、炸板=OpenTimes），pregen 只读它们做
    逐日 COUNT，**不另起东财多日抓取**，所以与引擎口径天然一致、零漂移（本仓反复防的
    "换源即钱路径分叉"形状，这里从源头规避）。

    产出（喂 rules_factor 的历史型 scalar_proxy：emo_chg5 ← limit_up_count_history，
    lu_reblast_low ← broken_board_ratio_history）：
      limit_up_count_history     = [各交易日涨停家数]（旧→新）
      broken_board_count         = 最近一交易日炸板家数（int 标量）
      broken_board_ratio         = 最近一交易日 炸板/(涨停+炸板)（0..1 比值；引擎 emotion.go
                                   :44-47 同式但 ×100 出百分数——此处按字段名 ratio 给 0..1，
                                   owner 标定 scalar_bands 时单位须对齐这里，注释已点明）
      broken_board_ratio_history = [各交易日炸板率 0..1]（旧→新）
      emotion_day                = 最近一交易日 YYYYMMDD（as-of，让陈旧可见）

    缺表 / 无行 / 盘前池未同步 ⇒ 返回 None：消费侧 rules_factor 见 None 即 scalar_proxy
    弃权（绝不补 0 冒充"今天没人炸板"——0 与缺读在分档里语义不同，同 seal 纪律）。
    **逐日缺行同样不补 0**（§F4 2026-11-27）：炸板池表某天没有行时，那一天整条炸板序列
    跳过，序列变短但每个值都是真的；最新一天缺行则连 broken_board_count/ratio 两个标量
    都不烘（消费侧见 None 弃权）。原先的 `br_by_day.get(d, 0)` 把「表停更」写成「没人
    炸板」，而 ths_break_pool_daily 自 20261117 起就没有新行 ⇒ 序列尾部 7 个 0.0 是编的：
    emo_blast_rate（方向 -1）会把假 0 读成"历史最低炸板率"，分位 0 反向改写成满分贡献，
    新规则 fac_173 因此在没有真实炸板数据时反而更容易过阈值——这是钱路径上的假读数。
    English: per-session market emotion series read from the engine's own authoritative
    pool tables; None on any gap so consumers decline honestly rather than score a 0.
    A day missing from the break-pool table is skipped, never filled with a fabricated 0.
    """
    # §PERCENTILE-SERIES 2026-11-17（owner 路线B）：补 MAX(continue_cnt) 得每日最高连板，
    # 连同炸板家数都烘成 history 序列，供 emo_max_board/emo_blast_cnt 走原生 percentile。
    lu_rows = _q("SELECT trade_date, COUNT(*), MAX(continue_cnt) FROM ths_limit_up_daily "
                 "WHERE trade_date<=? GROUP BY trade_date "
                 "ORDER BY trade_date DESC LIMIT ?", (day, lookback))
    if not lu_rows:
        return None
    br_rows = _q("SELECT trade_date, COUNT(*) FROM ths_break_pool_daily "
                 "WHERE trade_date<=? GROUP BY trade_date "
                 "ORDER BY trade_date DESC LIMIT ?", (day, lookback)) or []
    br_by_day = {str(r[0]): int(r[1]) for r in br_rows if r and r[0] is not None}
    sessions = []  # (trade_date, limit_cnt, max_board, break_cnt|None) 旧→新
    for r in reversed(list(lu_rows)):
        d = str(r[0])
        lc = int(r[1]) if r[1] is not None else 0
        mb = int(r[2]) if len(r) > 2 and r[2] is not None else 0
        # §F4：缺行的那天留 None，绝不补 0（详见函数头注释：假 0 会被 emo_blast_rate
        # 当成"历史最低炸板率"并反向拿满分）。
        sessions.append((d, lc, mb, br_by_day.get(d)))
    if not sessions:
        return None
    lu_hist = [float(s[1]) for s in sessions]
    board_hist = [float(s[2]) for s in sessions]
    blast_sessions = [s for s in sessions if s[3] is not None]
    blast_cnt_hist = [float(s[3]) for s in blast_sessions]
    ratio_hist = []
    for (_d, lc, _mb, bc) in blast_sessions:
        denom = lc + bc
        ratio_hist.append(round(bc / float(denom), 6) if denom > 0 else 0.0)
    last_d, _last_lc, last_mb, _last_bc = sessions[-1]
    out = {
        "limit_up_count_history": lu_hist,
        "ladder_height_history": board_hist,
        "broken_board_count_history": blast_cnt_hist,
        "broken_board_ratio_history": ratio_hist,
        "ladder_height": float(last_mb),
        "emotion_day": last_d,
    }
    if blast_sessions:
        blast_d, blast_lc, _blast_mb, blast_bc = blast_sessions[-1]
        if blast_d != last_d:
            # 两表 as-of 不同步：炸板序列仍用真实日（上面已烘），但 broken_board_count/ratio
            # 是"最近一交易日"的标量，绝不能拿 7 个交易日前的值冒充今天 ⇒ 整键不烘，
            # 消费侧见 None 弃权。把差距打进烘焙日志，别让停更静默。
            print("PREGEN_BLAST_STALE limit_day=%s blast_day=%s sessions=%d/%d"
                  % (last_d, blast_d, len(blast_sessions), len(sessions)))
        else:
            denom = blast_lc + blast_bc
            out["broken_board_count"] = float(blast_bc)
            out["broken_board_ratio"] = round(blast_bc / float(denom), 6) if denom > 0 else 0.0
    return out


def fetch_sector_drop_map(day):
    """§SECTOR-DROP 2026-11-17（owner 路线：sector_limit_up_drop_pct 走云端烘）：
    板块涨停家数较昨日下降比例 = (昨 limitup_cnt - 今) / 昨，按 industry 聚合。

    真源 = sector_history(trade_date, industry, limitup_cnt)（权威落库表，
    13.8 万行、盘前 fresh 到上一交易日），Go shortbase.go:32 的同名字段口径。
    "今日" = 表内 MAX(trade_date)，"昨日" = 其前一交易日（表内次大日期），
    不硬编码自然日（跳过周末/停市）。昨日为 0 的板块降幅无定义 ⇒ 不烘该板块
    （消费侧缺读数安全降级为 0，与 Go "empty = 0" 同口径，绝不编造降幅）。

    返回 {industry: drop(0..1+)} 或 None（缺表/缺两日数据）。降幅可 >1 吗？
    不会：今 >= 0、昨 > 0 ⇒ drop <= 1。今 > 昨时 drop < 0（涨停增加），照烘
    （负降幅让 leader_decay 的 >=0.5 判据自然不触发，语义正确）。
    """
    dates = _q("SELECT DISTINCT trade_date FROM sector_history "
               "WHERE trade_date<=? ORDER BY trade_date DESC LIMIT 2", (day,))
    if not dates or len(dates) < 2:
        return None
    today, yday = str(dates[0][0]), str(dates[1][0])
    cur = _q("SELECT industry, COALESCE(limitup_cnt,0) FROM sector_history "
             "WHERE trade_date=?", (today,)) or []
    prev = _q("SELECT industry, COALESCE(limitup_cnt,0) FROM sector_history "
              "WHERE trade_date=?", (yday,)) or []
    prev_map = {}
    for industry, cnt in prev:
        prev_map[str(industry)] = int(cnt or 0)
    out = {}
    for industry, cnt in cur:
        p = prev_map.get(str(industry))
        if p is None or p <= 0:            # 昨日无该板块或 0 家 ⇒ 降幅无定义 ⇒ 不烘
            continue
        out[str(industry)] = (p - int(cnt or 0)) / float(p)
    return out or None


def parse_leader_set(rows):
    """sector_history.top_stocks -> 6-digit leader-code set (§LEADER-JSON-FIX).

    top_stocks is a JSON array in the live DB ('["600805.SH","600834.SH"]') --
    the old comma-split kept the quotes, isdigit failed on EVERY part and the
    leader set came back empty, so is_leader was never baked and leader_decay(v1)
    stayed dead on its first protective reading. Unit-test mocks did not use the
    JSON shape, so only the real-data pilot exposed it (2026-11-17). JSON first,
    legacy delimiter shapes as the fallback.
    """
    leader_set = set()
    for r0 in (rows or []):
        raw = str(r0[0] or "")
        codes = []
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, list):
                codes = [str(x) for x in parsed]
        except (ValueError, TypeError):
            codes = [p for p in raw.replace("|", ",").split(",")]
        for part in codes:
            p = part.strip().strip('"[]').split(".")[0]
            if p.isdigit():
                leader_set.add(p)
    return leader_set


def fetch_industry_map():
    """个股 ts_code -> industry（stocks 真源，5546 行/5208 有行业），供 bake 按票
    查所属板块降幅。表名是 stocks 不是 stock_basic——后者在 trading.db 不存在，
    _q 静默返 None，只有真跑才暴露（2026-11-17 试点实锤）。

    缺表/空表返回 None ⇒ bake 不烘 sector_limit_up_drop_pct，消费侧安全降级。
    """
    rows = _q("SELECT ts_code, industry FROM stocks "
              "WHERE industry IS NOT NULL AND industry!=''", ()) or []
    out = {}
    for code, industry in rows:
        if code and industry:
            out[str(code)] = str(industry)
    return out or None


# ConsecBoards tolerance: same ruler as Go shortbase shortSealTolerance (0.995)
# and params.ld_seal_tolerance. A day counts as a limit-up when its close is at
# or above the authoritative stk_limit.up_limit times this factor.
CONSEC_SEAL_TOL = 0.995
CONSEC_LOOKBACK = 12


def compute_consec_boards(ts, tol=CONSEC_SEAL_TOL, lookback=CONSEC_LOOKBACK):
    """§CONSEC-BOARDS 2026-11-17：个股"今日之前"的连续涨停天数（leader_decay_v2）。

    Go shortbase.go:69 ConsecBoards / adapter.go:435-446 counts consecutive
    limit-ups going BACKWARD from the second-to-last bar (i.e. excluding today).
    pregen runs before the open, so the newest daily bar IS yesterday: count
    backward from the newest bar. That bakes exactly the value the in-client
    rule needs during today's session -- "boards before today" -- with no need
    for a per-stock limit history inside the sandbox.

    Board price comes from stk_limit.up_limit (authoritative, same source as the
    baked up_stop), NOT from a name-based 10/20/30% guess -- a fidelity gain over
    Go. A day is a limit-up when close >= up_limit*tol. Days without an up_limit
    row break the streak (honest: cannot prove a board, do not invent one).

    Returns int >= 0, or None when there is no usable daily history (bake skips
    the key => the rule declines rather than scoring a fabricated board count).
    """
    rows = _q("SELECT trade_date, close FROM daily WHERE ts_code=? "
              "AND close IS NOT NULL ORDER BY trade_date DESC LIMIT ?", (ts, lookback)) or []
    if not rows:
        return None
    limits = _q("SELECT trade_date, up_limit FROM stk_limit WHERE ts_code=? "
                "AND up_limit IS NOT NULL ORDER BY trade_date DESC LIMIT ?",
                (ts, lookback)) or []
    up_by_day = {}
    for trade_date, up_limit in limits:
        up_by_day[str(trade_date)] = float(up_limit)
    count = 0
    for trade_date, close in rows:          # newest (yesterday) first
        up = up_by_day.get(str(trade_date))
        if up is None or up <= 0 or close is None:
            break
        if float(close) >= up * tol:
            count += 1
        else:
            break
    return count


def _notify(title, body):
    """§B 机制（2026-11-11 owner 要求）：手机推送走系统现成的 ntfy 运维告警通道
    （config.json notify.ntfy_url/topic，与 APK 推送网关并行的独立冗余通道）。
    best-effort：推送失败只记日志，绝不影响 pregen 本身的退出码。
    English: best-effort phone push over the existing ntfy ops channel.
    """
    try:
        cfg = json.load(open("/var/lib/quant-trading-v2/config.json", encoding="utf-8"))
        notify = cfg.get("notify") or {}
        url = (notify.get("ntfy_url") or "https://ntfy.sh").rstrip("/")
        topic = notify.get("ntfy_topic") or ""
        if not topic:
            print("PREGEN_NOTIFY_FAIL reason=no_topic")
            return
        req = urllib.request.Request(
            url + "/" + topic, data=body.encode("utf-8"),
            headers={"Title": title, "Priority": "high", "Tags": "warning"})
        urllib.request.urlopen(req, timeout=8)
        print("PREGEN_NOTIFY_OK")
    except Exception as exc:  # noqa: BLE001 - 通知是尽力而为，不拖垮主流程
        print("PREGEN_NOTIFY_FAIL %s" % type(exc).__name__)


def _write_status(day, symbols_n, ok, fail_n, consec_fail, recovered):
    """状态文件（BUILD_DIR 内）：consec_fail/coverage 给 hot reload 的自动补推限频与判定。"""
    status_path = os.path.join(BUILD_DIR, "qmt_gateway", "precalc_status.json")
    coverage = ok * 100 // max(1, symbols_n)
    status = {"day": day, "codes": symbols_n, "ok": ok, "fail": fail_n,
              "coverage": coverage, "consec_fail": consec_fail,
              "recovered": recovered,
              "last_attempt": datetime.now().astimezone().isoformat(timespec="seconds")}
    tmp = status_path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(status, fh, ensure_ascii=True, indent=1)
    os.replace(tmp, status_path)
    return status, coverage


def main():
    """盘前预计算主流程：桶回填 → 全量拉日线（基线 6 字段 + bars_seq 序列）→
    原子写产物 → 状态落盘/通知。单票失败计数可见绝不拖全局；退出码
    0=成功（ok≥1）/2=无码可跑/3=全部失败（§B 机制按 coverage 判连续失败推手机）。"""
    started = time.time()
    # §BUCKETS-20261112：集中度桶回填（行业/概念映射自装机以来恒空，载体桶闸
    # fail-closed 拒全部买入）。独立 venv 脚本多源补齐（baostock/东财/雪球）；
    # 失败绝不阻断 precalc 主链——桶缺时买入照样被闸拒，这里只负责把桶填上。
    venv_py = os.environ.get("QUANT_VENV_PY", "/opt/quant/venv/bin/python3")
    buckets_script = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                  "gen_industry_buckets_gz.py")
    try:
        import subprocess
        r = subprocess.run([venv_py, buckets_script, "--build-dir", BUILD_DIR],
                           capture_output=True, text=True, timeout=900)
        tail = (r.stdout or "").strip().splitlines()[-1:] or [""]
        print("BUCKETS_STEP rc=%s %s" % (r.returncode, tail[0][:200]))
    except Exception as exc:  # noqa: BLE001
        print("BUCKETS_STEP rc=exc %r" % (exc,))
    with open(PARAMS_PATH, "r", encoding="utf-8") as fh:
        params = json.load(fh).get("params") or {}
    symbols = list(params.get("symbols") or [])
    if MAX_CODES > 0:
        symbols = symbols[:MAX_CODES]
    if not symbols:
        print("PREGEN_RESULT day= codes=0 ok=0 fail=0 elapsed_s=0 (no symbols)")
        return 2
    day = datetime.now().astimezone().strftime("%Y%m%d")
    # §LIMIT-BOARD 20261116：一次请求全市场涨停池（失败不阻断主链，limit_board_count
    # 因子如实 declined——与价量单票失败同一纪律）。
    try:
        lu_boards = fetch_lu_boards(day)
        print("PREGEN_ZT_POOL day=%s codes=%s" % (day, len(lu_boards or {})))
    except Exception as exc:  # noqa: BLE001 - 涨停池缺源不拖全局
        lu_boards = None
        print("PREGEN_ZT_POOL_FAIL %s" % type(exc).__name__)
    # §WIRING-B 2026-11-17：全市场情绪/龙头标量算一次传入每票（从已拉的 lu_boards +
    # sector_history 派生，绝不另起多日扫描）。缺源时键不放进 market，消费侧 None 诚实弃权。
    market = {}
    if lu_boards:
        market["limit_up_count"] = float(len(lu_boards))
        boards_vals = [v for v in lu_boards.values() if isinstance(v, (int, float))]
        if boards_vals:
            market["ladder_height"] = float(max(boards_vals))
    try:
        leader_rows = _q("SELECT top_stocks FROM sector_history WHERE trade_date=("
                         "SELECT MAX(trade_date) FROM sector_history) "
                         "AND top_stocks IS NOT NULL AND top_stocks!=''", ())
        leader_set = parse_leader_set(leader_rows)
        if leader_set:
            market["leader_set"] = leader_set
        print("PREGEN_LEADER_SET n=%s" % len(leader_set))
    except Exception as exc:  # noqa: BLE001
        print("PREGEN_LEADER_FAIL %s" % type(exc).__name__)
    # §SECTOR-DROP 2026-11-17（owner：sector_limit_up_drop_pct 走云端烘）：一次算出
    # 板块涨停家数降幅 + 个股行业映射，放 market 供每票 O(1) 查（缺源不阻断主链）。
    try:
        drop_map = fetch_sector_drop_map(day)
        ind_map = fetch_industry_map()
        if drop_map:
            market["sector_drop_map"] = drop_map
        if ind_map:
            market["industry_map"] = ind_map
        print("PREGEN_SECTOR_DROP industries=%s stocks=%s" % (
            len(drop_map or {}), len(ind_map or {})))
    except Exception as exc:  # noqa: BLE001
        print("PREGEN_SECTOR_DROP_FAIL %s" % type(exc).__name__)
    # §WIRING-B+ 2026-11-17：全市场情绪历史从权威落库表逐日聚合（涨停/炸板/炸板率多日序列），
    # 喂 rules_factor 的历史型 scalar_proxy（emo_chg5 / lu_reblast_low）。缺表/盘前未同步⇒None，
    # 消费侧诚实弃权。缺源不阻断主链（与涨停池同一纪律）。
    try:
        hist = fetch_market_history(day)
        if hist:
            market.update(hist)
            print("PREGEN_EMOTION_HIST day=%s sessions=%s break_ratio=%s"
                  % (hist.get("emotion_day"), len(hist.get("limit_up_count_history") or []),
                     hist.get("broken_board_ratio")))
        else:
            print("PREGEN_EMOTION_HIST none")
    except Exception as exc:  # noqa: BLE001
        print("PREGEN_EMOTION_HIST_FAIL %s" % type(exc).__name__)
    # §TURNOVER-WIRING：流通股本每天取一次（腾讯，50 只/请求），写进每票 precalc。
    float_shares_map = {}
    try:
        float_shares_map = fetch_float_shares(symbols)
        print("PREGEN_FLOAT_SHARES codes=%d of=%d" % (len(float_shares_map), len(symbols)))
    except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链
        print("PREGEN_FLOAT_SHARES_FAIL %s" % type(exc).__name__)
    # F3 2026-11-25：上市天数（静态字段，一天取一次全市场）。载体 `t_min_listed_days`
    # 闸靠它，覆盖率达到 `t_listed_days_coverage_min_pct`(95%) 才会自动从 A 档切 B 档。
    listed_days_map = {}
    try:
        listed_days_map = fetch_listed_days(day)
        covered = sum(1 for s in symbols if listed_days_map.get(s) is not None)
        pct = (100.0 * covered / len(symbols)) if symbols else 0.0
        print("PREGEN_LISTED_DAYS codes=%d of=%d pct=%.1f"
              % (len(listed_days_map), len(symbols), pct))
    except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链
        print("PREGEN_LISTED_DAYS_FAIL %s" % type(exc).__name__)
    # §P3 2026-11-27：板块连接键 + 板块资金流标量。两条都是**回退腿**：
    #   sector_map   —— 个股 -> 东财板块名（生产者 pregen_sector_map_gz.py 的产物，超过
    #                   SECTOR_MAP_MAX_AGE_DAYS 天就整表弃权，不拿旧成分冒充今天）
    #   sector_flow  —— 板块净流入率/5 日率/残差 z（读 sector_flow_daily 当日新鲜行）
    # 缺任一条都不阻断主链：个股资金流新鲜时走个股腿，否则 flow_* 键干脆不烘。
    try:
        smap, smap_day = load_sector_map()
        if smap:
            market["sector_map"] = smap
        sflow = fetch_sector_flow_map(day)
        if sflow:
            market["sector_flow"] = {"boards": sflow}
        print("PREGEN_SECTOR_FLOW day=%s map_day=%s map=%s boards=%s"
              % (day, smap_day, len(smap), len(sflow)))
    except Exception as exc:  # noqa: BLE001 - 缺源不阻断主链
        print("PREGEN_SECTOR_FLOW_FAIL %s" % type(exc).__name__)
    precalc, ok, failed = {}, 0, []
    for symbol in symbols:
        try:
            bars = fetch_klines(symbol)
            fact = compute(bars) if bars else None
            src = "hithink"
            if fact:
                seq = bars_seq(bars)
                if seq:
                    fact["bars_seq"] = seq
            if not fact:
                # owner 2026-11-11 14:2x：云端自动研究库（5546 只）兜底——hithink 无覆盖/
                # 拉取失败的票，用本地 trading.db.daily；data_day 让 staleness 可见。
                bars2, data_day = fetch_local_daily(symbol)
                fact = compute(bars2) if bars2 else None
                src = "local_daily"
                if fact:
                    fact["data_day"] = data_day
                    fact["src"] = src
            bakeables = bake_bakeables(symbol, params, lu_boards, market,
                                       float_shares=float_shares_map.get(symbol), day=day) if fact else {}
            if fact:
                fact.update(bakeables)
                if float_shares_map.get(symbol):
                    fact["float_shares"] = float_shares_map[symbol]
                # F3 2026-11-25：上市天数（静态字段）。载体 t_liquidity_gate 读它；
                # 缺失即 A 档（闸显式关）并在读数里点名，绝不静默放行。
                if listed_days_map.get(symbol) is not None:
                    fact["listed_days"] = listed_days_map[symbol]
                precalc[symbol] = fact
                ok += 1
            else:
                # 价量缺失也可能只是 hithink 单票失败：bakeables 仍值得烘（估值/财务/D1）
                bakeables = bake_bakeables(symbol, params, lu_boards, market,
                                           float_shares=float_shares_map.get(symbol), day=day)
                if bakeables:
                    bakeables["src"] = "bake_only"
                    if float_shares_map.get(symbol):
                        bakeables["float_shares"] = float_shares_map[symbol]
                    if listed_days_map.get(symbol) is not None:
                        bakeables["listed_days"] = listed_days_map[symbol]
                    precalc[symbol] = bakeables
                    ok += 1
                else:
                    failed.append(symbol)
        except Exception as exc:  # noqa: BLE001 - 单票失败不拖全局，计数可见
            failed.append(symbol)
            print("PREGEN_FAIL %s %s" % (symbol, type(exc).__name__))
        time.sleep(SLEEP_MS / 1000.0)
    doc = {"day": day,
           "asof": datetime.now().astimezone().isoformat(timespec="seconds"),
           "precalc": precalc}
    raw = json.dumps(doc, ensure_ascii=True, sort_keys=True).encode("utf-8")
    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    tmp = OUT_PATH + ".tmp"
    with open(tmp, "wb") as fh:
        fh.write(raw)
    os.replace(tmp, OUT_PATH)
    elapsed = round(time.time() - started, 1)
    print("PREGEN_RESULT day=%s codes=%d ok=%d fail=%d elapsed_s=%s"
          % (day, len(symbols), ok, len(failed), elapsed))
    print("PREGEN_WRITE path=%s bytes=%d" % (OUT_PATH, len(raw)))
    print("PREGEN_FAILED=%s" % ",".join(failed[:12]) if failed else "PREGEN_FAILED=none")
    # §B 机制：失败判定（全败 或 覆盖率<50%）计入连续失败；连续≥2 次推手机通知；
    # 此前连续失败≥2 且本次恢复 → 推「已恢复」。状态落 BUILD_DIR 供 hot reload 限频补推。
    prev = {}
    try:
        prev = json.load(open(os.path.join(BUILD_DIR, "qmt_gateway", "precalc_status.json"),
                              encoding="utf-8"))
    except (OSError, ValueError):
        prev = {}
    prev_consec = int(prev.get("consec_fail") or 0)
    failed_run = (ok == 0) or (ok * 100 < 50 * max(1, len(symbols)))
    consec_fail = prev_consec + 1 if failed_run else 0
    recovered = (not failed_run) and prev_consec >= 2
    status, coverage = _write_status(day, len(symbols), ok, len(failed), consec_fail, recovered)
    if consec_fail >= 2:
        _notify("Quant预计算连续失败x%d" % consec_fail,
                "day=%s ok=%d fail=%d coverage=%d%% 上游恢复前盒内相关战法维持保守弃权"
                % (day, ok, len(failed), coverage))
    elif recovered:
        _notify("Quant预计算已恢复", "day=%s ok=%d coverage=%d%%（此前连续失败%d次）"
                % (day, ok, coverage, prev_consec))
    return 0 if ok >= 1 else 3


if __name__ == "__main__":
    sys.exit(main())
