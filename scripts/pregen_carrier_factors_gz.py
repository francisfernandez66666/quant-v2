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
import os
import sys
import time
import urllib.request
from datetime import datetime

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


def bake_bakeables(symbol, params_doc, lu_boards=None, market=None):
    """§PRECALC v2（2026-11-11 owner 定案）：QMT 直取价量（bars），**QMT 拿不到的才烘**——
    估值序列/换手序列（daily_basic，当日新鲜）、财务分位（fina_indicator 最新档）、
    资金流（moneyflow_history MainNet/circ_mv）、新闻事件=D1 分数（scalar_proxy 带值）、
    概念热度=当日利好池强度分（同为 scalar_proxy）。形态逐一对齐 rules_factor 的消费端：
    valuation_series={"pe_ttm":[..],..}（_field_series 面板）、fin_pit={"eps":..}（_pit_value）、
    flow/heat 标量（_scalar_score + params.scalar_bands）。缺源的字段如实缺省（绝不编造）。"""
    ts = _ts_code(symbol)
    out = {}
    n = BAKE_SERIES_N
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
        tsh = [r[7] for r in rows if r[7] is not None]
        if tsh:
            out["total_share_series"] = tsh
    fin = _q("SELECT eps,roe,roe_waa,roa,grossprofit_margin,netprofit_margin,"
             "debt_to_assets,yoy_or FROM fina_indicator WHERE ts_code=? "
             "ORDER BY end_date DESC LIMIT 1", (ts,))
    if fin and fin[0]:
        cols = ["eps", "roe", "roe_waa", "roa", "grossprofit_margin",
                "netprofit_margin", "debt_to_assets", "yoy_or"]
        pit = {c: v for c, v in zip(cols, fin[0]) if v is not None}
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
    code6 = symbol.split(".")[0]
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
        if circ and circ[0] and circ[0][0]:
            cap = float(circ[0][0]) * 10000.0  # circ_mv 万元 -> 元
            if cap > 0:
                out["flow_today_pct"] = round(mains[-1] / cap * 100.0, 4)
                if len(mains) >= 2:
                    out["flow_5d_pct"] = round(sum(mains) / cap * 100.0, 4)
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
                "broken_board_count", "broken_board_ratio", "emotion_day"):
        if _mk in market:
            out[_mk] = market[_mk]
    # 涨停价（leader_decay 的 up_stop = 今日板价）：stk_limit 最新一行的 up_limit。
    up = _q("SELECT up_limit FROM stk_limit WHERE ts_code=? ORDER BY trade_date DESC LIMIT 1",
            (ts,))
    if up and up[0] and up[0][0] is not None:
        out["up_stop"] = float(up[0][0])
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
        if circ and circ[0] and circ[0][0] and max_seal and float(max_seal) > 0:
            cap_yuan = float(circ[0][0]) * 10000.0
            if cap_yuan > 0:
                out["seal_amount_ratio"] = round(float(max_seal) / cap_yuan, 6)
        if fst and len(str(fst)) >= 4 and ":" in str(fst):
            try:
                hh, mm = str(fst).split(":")[0], str(fst).split(":")[1]
                out["first_seal_minute"] = float(int(hh) * 60 + int(mm))
            except (ValueError, IndexError):
                pass
    return out


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
    English: per-session market emotion series read from the engine's own authoritative
    pool tables; None on any gap so consumers decline honestly rather than score a 0.
    """
    lu_rows = _q("SELECT trade_date, COUNT(*) FROM ths_limit_up_daily "
                 "WHERE trade_date<=? GROUP BY trade_date "
                 "ORDER BY trade_date DESC LIMIT ?", (day, lookback))
    if not lu_rows:
        return None
    br_rows = _q("SELECT trade_date, COUNT(*) FROM ths_break_pool_daily "
                 "WHERE trade_date<=? GROUP BY trade_date "
                 "ORDER BY trade_date DESC LIMIT ?", (day, lookback)) or []
    br_by_day = {str(r[0]): int(r[1]) for r in br_rows if r and r[0] is not None}
    sessions = []  # (trade_date, limit_cnt, break_cnt) 旧→新
    for r in reversed(list(lu_rows)):
        d = str(r[0])
        lc = int(r[1]) if r[1] is not None else 0
        bc = br_by_day.get(d, 0)
        sessions.append((d, lc, bc))
    if not sessions:
        return None
    lu_hist = [float(s[1]) for s in sessions]
    ratio_hist = []
    for (_d, lc, bc) in sessions:
        denom = lc + bc
        ratio_hist.append(round(bc / float(denom), 6) if denom > 0 else 0.0)
    last_d, last_lc, last_bc = sessions[-1]
    denom = last_lc + last_bc
    return {
        "limit_up_count_history": lu_hist,
        "broken_board_ratio_history": ratio_hist,
        "broken_board_count": float(last_bc),
        "broken_board_ratio": round(last_bc / float(denom), 6) if denom > 0 else 0.0,
        "emotion_day": last_d,
    }


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
        leader_set = set()
        for r0 in (leader_rows or []):
            for part in str(r0[0]).replace("|", ",").split(","):
                p = part.strip().split(".")[0]
                if p.isdigit():
                    leader_set.add(p)
        if leader_set:
            market["leader_set"] = leader_set
        print("PREGEN_LEADER_SET n=%s" % len(leader_set))
    except Exception as exc:  # noqa: BLE001
        print("PREGEN_LEADER_FAIL %s" % type(exc).__name__)
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
            bakeables = bake_bakeables(symbol, params, lu_boards, market) if fact else {}
            if fact:
                fact.update(bakeables)
                precalc[symbol] = fact
                ok += 1
            else:
                # 价量缺失也可能只是 hithink 单票失败：bakeables 仍值得烘（估值/财务/D1）
                bakeables = bake_bakeables(symbol, params, lu_boards, market)
                if bakeables:
                    bakeables["src"] = "bake_only"
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
