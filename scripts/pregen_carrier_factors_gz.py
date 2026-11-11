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
MAX_CODES = int(os.environ.get("SIGNAL_POOL_MAX", "600"))


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
                         "volume": float(fields[5]) * VOL_UNIT_MULT})
        except (ValueError, IndexError):
            continue
    return bars or None


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
    return symbol if "." in symbol else symbol


def bake_bakeables(symbol, params_doc):
    """§PRECALC v2（2026-11-11 owner 定案）：QMT 直取价量（bars），**QMT 拿不到的才烘**——
    估值序列/换手序列（daily_basic，当日新鲜）、财务分位（fina_indicator 最新档）、
    资金流（moneyflow_history MainNet/circ_mv）、新闻事件=D1 分数（scalar_proxy 带值）、
    概念热度=当日利好池强度分（同为 scalar_proxy）。形态逐一对齐 rules_factor 的消费端：
    valuation_series={"pe_ttm":[..],..}（_field_series 面板）、fin_pit={"eps":..}（_pit_value）、
    flow/heat 标量（_scalar_score + params.scalar_bands）。缺源的字段如实缺省（绝不编造）。"""
    ts = _ts_code(symbol)
    out = {}
    n = BAKE_SERIES_N
    rows = _q("SELECT trade_date,pe_ttm,pb,ps_ttm,turnover_rate FROM daily_basic "
              "WHERE ts_code=? AND pe_ttm IS NOT NULL ORDER BY trade_date DESC LIMIT ?", (ts, n)) \
        or _q("SELECT trade_date,pe_ttm,pb,ps_ttm,turnover_rate FROM daily_basic "
              "WHERE ts_code=? ORDER BY trade_date DESC LIMIT ?", (ts, n))
    if rows:
        val = {"pe_ttm": [r[1] for r in rows if r[1] is not None],
               "pb": [r[2] for r in rows if r[2] is not None],
               "ps_ttm": [r[3] for r in rows if r[3] is not None]}
        val = {k: v for k, v in val.items() if v}
        if val:
            out["valuation_series"] = val
        tos = [r[4] for r in rows if r[4] is not None]
        if tos:
            out["turnover_series"] = tos
    fin = _q("SELECT eps,roe,roe_waa,roa,grossprofit_margin,netprofit_margin,"
             "debt_to_assets,yoy_or FROM fina_indicator WHERE ts_code=? "
             "ORDER BY end_date DESC LIMIT 1", (ts,))
    if fin and fin[0]:
        cols = ["eps", "roe", "roe_waa", "roa", "grossprofit_margin",
                "netprofit_margin", "debt_to_assets", "yoy_or"]
        pit = {c: v for c, v in zip(cols, fin[0]) if v is not None}
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
    d1 = _q("SELECT score,blocked FROM d1_scores WHERE code=? ORDER BY created_at DESC LIMIT 1",
            (code6,))
    if d1 and d1[0]:
        score, blocked = d1[0]
        out["news_event"] = 0.0 if blocked else float(score or 0.0)
    hp = (params_doc.get("hot_pool") or {}).get("bullish") or {}
    hit = hp.get(symbol) or hp.get(code6)
    if isinstance(hit, dict) and hit.get("score") is not None:
        out["concept_heat"] = float(hit["score"])
    return out


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
    started = time.time()
    with open(PARAMS_PATH, "r", encoding="utf-8") as fh:
        params = json.load(fh).get("params") or {}
    symbols = list(params.get("symbols") or [])[:MAX_CODES]
    if not symbols:
        print("PREGEN_RESULT day= codes=0 ok=0 fail=0 elapsed_s=0 (no symbols)")
        return 2
    day = datetime.now().astimezone().strftime("%Y%m%d")
    precalc, ok, failed = {}, 0, []
    for symbol in symbols:
        try:
            bars = fetch_klines(symbol)
            fact = compute(bars) if bars else None
            src = "hithink"
            if not fact:
                # owner 2026-11-11 14:2x：云端自动研究库（5546 只）兜底——hithink 无覆盖/
                # 拉取失败的票，用本地 trading.db.daily；data_day 让 staleness 可见。
                bars2, data_day = fetch_local_daily(symbol)
                fact = compute(bars2) if bars2 else None
                src = "local_daily"
                if fact:
                    fact["data_day"] = data_day
                    fact["src"] = src
            bakeables = bake_bakeables(symbol, params) if fact else {}
            if fact:
                fact.update(bakeables)
                precalc[symbol] = fact
                ok += 1
            else:
                # 价量缺失也可能只是 hithink 单票失败：bakeables 仍值得烘（估值/财务/D1）
                bakeables = bake_bakeables(symbol, params)
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
