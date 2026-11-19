#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""probe_qmt_signals.py — QMT 资金流 + 相对放量 统一只读探针（2026-11-19）。

由 probe_qmt_index_flow.py（v3）与 probe_qmt_vol_surge.py（v1）合并而来，
一次跑完出全部读数，供物理机现场实测。两段：

  FLOW 段（口径③）：指数/个股/北向 QMT 原生资金流字段可得性
  VOL  段（口径⑥）：相对放量 r_yest / r_ma5 / is_vol 双闸门 + 停牌口径

## 关键口径（勿踩坑）

1. **000001 裸码有歧义**：sh000001/000001.SH = 上证指数，sz000001/000001.SZ = 平安银行。
   代码库为此付过一整天代价（§BUG-INDEX / §INDEX-BLIND，market.go:2597、market_index.go:4）。
   指数一律显式写死交易所，且 FLOW 段对每个指数同时测后缀式与前缀式，一次定死 QMT 认哪种。
2. **QMT 资金流字段是 zjby*/netInflow* 这套，不是东财 MainNetInflow**：
   period LV1=transactioncount1d/1m、LV2=hftransactioncount*/l2transactioncount；
   get_market_data_ex + 先 download_history_data2。三个坑：
   ① netInflow*Amount 在 LV1 官方全 0，优先看 zjbyNetInflow / 成交量；
   ② 迅投分档阈值自家口径，别和东财对数；③ 不带 Dx=当日累计、带 Dx=增量，
   **绝不做跨日同一时点相减**。
3. **北向独立一套**：FFFFFF.SGT + northfinancechange1d/1m，或 get_north_finance_change('1d')；
   额度口径 NetInFlow/BalanceByDay 与成交口径 BuyMoney-SellMoney 别混。
4. **相对放量**（owner 伪码）：剔停牌后 v=amount 或 volume，r_yest=v[-1]/v[-2]，
   r_ma5=v[-1]/mean(v[-6:-1])，is_vol=(r_yest>1.2 and r_ma5>1.0)，两口径各算一遍。

## 安全纪律（与 probe_qmt_feed.py 同族）

- xtdata-only：只 import xtdata 行情只读，**绝不建 trader 交易会话**
  （10-29 把客户端拖崩 3~6 分钟的元凶就是每次 connect 占 writer 槽不回收）。
- 一次跑完不循环、不下单、不撤单、不落盘、不改任何配置。
- **所有 print 行纯 ASCII**（GBK/SSH 回传会撕裂中文），中文只出现在注释里。

## 用法（盒上 CPython 3.10~3.13，只读）

    python probe_qmt_signals.py                          # 两段全跑（默认）
    python probe_qmt_signals.py --section flow           # 只跑资金流+北向
    python probe_qmt_signals.py --section vol            # 只跑相对放量
    python probe_qmt_signals.py --download --start 20261001 --end 20261119
    python probe_qmt_signals.py --probe-stock 000001.SZ  # 个股资金流对照
    python probe_qmt_signals.py --field amount           # 放量只算成交额口径

判读：
  FLOW 段看 QPROBE FLOW_<标签>_<period>_flow_hits 是否出现 zjby*/netInflow*；
        _allzero_*=1 的金额列不可当信号；NORTH_* 看北向字段。
  VOL  段看 QPROBE VOL_<标签>_<basis>_r_yest / _r_ma5 / _is_vol 与 _has_suspendFlag。
"""
import argparse
import sys

sys.stdout.reconfigure(line_buffering=True)

# ---- FLOW 段常量 ----
# (语义标签, 后缀式, 前缀式)。指数一律显式交易所；同时测两种写法。
INDEX_CODES = [
    ("SSE_COMP", "000001.SH", "sh000001"),    # 上证指数
    ("SZSE_COMP", "399001.SZ", "sz399001"),   # 深证成指
    ("CSI300", "000300.SH", "sh000300"),      # 沪深300
]
# QMT 官方资金流字段（owner 查证讯投字段表）。不是东财 MainNetInflow。
FLOW_FIELDS = [
    "zjbyNetInflow", "zjbyMost", "zjbyBig", "zjbyMedium", "zjbySmall",
    "netInflowMostAmount", "netInflowBigAmount",
    "netInflowMediumAmount", "netInflowSmallAmount",
    "netInflowMostVolume", "netInflowBigVolume",
    "netInflowMediumVolume", "netInflowSmallVolume",
    "bidMostAmount", "offMostAmount",
    "bidMostAmountDx", "offMostAmountDx",
    "netInflowMostAmountDx", "netInflowBigAmountDx",
]
AMOUNT_KEYS = ["netInflowMostAmount", "netInflowBigAmount", "bidMostAmount", "offMostAmount"]
FLOW_PERIODS = ["transactioncount1d", "transactioncount1m",
                "hftransactioncount1d", "l2transactioncount"]
NORTH_CODE = "FFFFFF.SGT"
NORTH_PERIODS = ["northfinancechange1d", "northfinancechange1m"]

# ---- VOL 段常量 ----
# 样本：茅台(个股高流动性)、平安银行(000001.SZ 个股)、上证指数(000001.SH 指数对照)、沪深300。
VOL_DEFAULT_CODES = ["600519.SH", "000001.SZ", "000001.SH", "000300.SH"]
DAY_FIELDS = ["amount", "volume", "suspendFlag", "tradingDay", "close", "open", "high", "low"]
R_YEST_GATE = 1.2
R_MA5_GATE = 1.0


def kv(key, value):
    """机器可读单行输出（纯 ASCII）。"""
    print("QPROBE %s=%s" % (key, value))


def short(v, n=160):
    return repr(v)[:n]


def fnum(x):
    """DataFrame 单元格 → float；NaN/None/不可解析 → None。"""
    try:
        f = float(x)
        if f != f:  # NaN
            return None
        return f
    except (TypeError, ValueError):
        return None


def cols_of(df):
    try:
        return [str(c) for c in df.columns]
    except Exception:  # noqa: BLE001
        return []


def series_of(df, name):
    """取一列成 float 列表（保留原始位置，None 代表无效）。"""
    try:
        return [fnum(x) for x in list(df[name])]
    except Exception:  # noqa: BLE001
        return []


def tail_of(df, name, n=3):
    try:
        return [float(x) for x in list(df[name])[-n:]]
    except Exception:  # noqa: BLE001
        return None


def frame_of(data, code):
    """get_market_data_ex 返回 dict：优先按 code 取，否则取第一份。"""
    if not isinstance(data, dict) or not data:
        return None
    return data.get(code) or next(iter(data.values()))


def do_download(xtdata, codes, periods, args, tag):
    """统一下载：优先 data2(stock_list, period, start, end)，回退 data1。"""
    dl2 = getattr(xtdata, "download_history_data2", None)
    dl1 = getattr(xtdata, "download_history_data", None)
    kv(tag + "_DL2_present", "1" if callable(dl2) else "0")
    kv(tag + "_DL1_present", "1" if callable(dl1) else "0")
    for code in codes:
        for period in periods:
            try:
                if callable(dl2):
                    dl2(stock_list=[code], period=period,
                        start_time=args.start, end_time=args.end)
                elif callable(dl1):
                    dl1(code, period)
                else:
                    kv(tag + "_DL_ABSENT", "1")
                    return
                kv(tag + "_DL_" + code.replace(".", "_") + "_" + period, "ok")
            except Exception as exc:  # noqa: BLE001
                kv(tag + "_DL_" + code.replace(".", "_") + "_" + period + "_err", short(exc))


# ==================== FLOW 段 ====================

def probe_flow(xtdata, code, period, field_list, label, args):
    """单个 (代码, period, 字段表) 取数，打印列 / 命中资金流字段 / LV1 全 0 检测。"""
    try:
        data = xtdata.get_market_data_ex(
            field_list=field_list, stock_list=[code], period=period)
    except Exception as exc:  # noqa: BLE001
        kv(label + "_err", short(exc))
        return
    df = frame_of(data, code)
    if df is None:
        kv(label, "EMPTY type=%s" % type(data).__name__)
        return
    cols = cols_of(df)
    kv(label + "_n", getattr(df, "shape", [0])[0] if hasattr(df, "shape") else len(df))
    kv(label + "_cols", ",".join(cols[:40]))
    hits = [c for c in cols if any(f.lower() in c.lower() for f in FLOW_FIELDS)]
    kv(label + "_flow_hits", ",".join(hits) if hits else "none")
    for name in hits[:12]:
        tv = tail_of(df, name)
        kv(label + "_tail_" + name, short(tv, 120))
        if name in AMOUNT_KEYS or "amount" in name.lower():
            kv(label + "_allzero_" + name, "1" if (tv is not None and all(
                (x == 0 or x != x) for x in tv)) else "0")


def run_flow(xtdata, args):
    kv("FLOW_begin", "1")
    # 目标：内置三指数各两写法；--codes 覆盖时按给定码原样（各测一次）。
    if args.codes:
        targets = [(c.replace(".", "_"), c) for c in
                   [x.strip() for x in args.codes.split(",") if x.strip()]]
        kv("FLOW_codes_source", "cli")
    else:
        targets = []
        for label, suff, pref in INDEX_CODES:
            targets.append((label + "_SUFFIX", suff))
            targets.append((label + "_PREFIX", pref))
        kv("FLOW_codes_source", "builtin_sh000001_sz000001_caveat")
    kv("FLOW_target_count", len(targets))
    kv("FLOW_targets", ",".join("%s:%s" % t for t in targets))

    # API 存在性清单。
    try:
        apis = [n for n in dir(xtdata)
                if any(k in n.lower() for k in ("flow", "inflow", "capital", "money", "transaction"))]
        kv("FLOW_apis_in_xtdata", ",".join(sorted(apis)[:40]) if apis else "none")
    except Exception as exc:  # noqa: BLE001
        kv("FLOW_apis_err", short(exc))

    if args.download:
        do_download(xtdata, [c for _, c in targets], FLOW_PERIODS, args, "FLOW")

    # 逐码 × period × {全字段, 资金流字段} + 日线对照。
    for label, code in targets:
        safe = label.replace(".", "_")
        for period in FLOW_PERIODS:
            probe_flow(xtdata, code, period, [], "FLOW_IDX_%s_%s_ALL" % (safe, period), args)
            probe_flow(xtdata, code, period, FLOW_FIELDS, "FLOW_IDX_%s_%s_FLDF" % (safe, period), args)
        probe_flow(xtdata, code, "1d", [], "FLOW_IDX_%s_DAY_ALL" % safe, args)
        if args.download:
            probe_flow(xtdata, code, "transactioncount1d", [],
                       "FLOW_IDX_%s_AFTERDL_ALL" % safe, args)

    # 个股对照：确认本构建到底有没有资金流数据（LV1 金额全 0 时看 zjby/成交量）。
    if args.probe_stock:
        sc = args.probe_stock.replace(".", "_")
        for period in FLOW_PERIODS:
            probe_flow(xtdata, args.probe_stock, period, [], "FLOW_STK_%s_%s_ALL" % (sc, period), args)
            probe_flow(xtdata, args.probe_stock, period, FLOW_FIELDS,
                       "FLOW_STK_%s_%s_FLDF" % (sc, period), args)

    # 北向：独立 period + 独立代码；额度口径 vs 成交口径别混。
    try:
        g = getattr(xtdata, "get_north_finance_change", None)
        kv("FLOW_north_API_present", "1" if callable(g) else "0")
        if callable(g):
            for period in ("1d", "1m"):
                try:
                    kv("FLOW_north_get_" + period, short(g(period), 240))
                except Exception as exc:  # noqa: BLE001
                    kv("FLOW_north_get_" + period + "_err", short(exc))
    except Exception as exc:  # noqa: BLE001
        kv("FLOW_north_API_err", short(exc))
    for period in NORTH_PERIODS:
        probe_flow(xtdata, NORTH_CODE, period, [], "FLOW_NORTH_" + period, args)
    kv("FLOW_end", "1")


# ==================== VOL 段 ====================

def probe_vol(xtdata, code, label, args):
    """日线字段可得性 + owner 伪码 r_yest/r_ma5/is_vol（amount/volume 两口径）。"""
    try:
        data = xtdata.get_market_data_ex(
            field_list=DAY_FIELDS, stock_list=[code], period="1d",
            start_time=args.start, end_time=args.end)
    except Exception as exc:  # noqa: BLE001
        kv(label + "_err", short(exc))
        return
    df = frame_of(data, code)
    if df is None:
        kv(label, "EMPTY type=%s" % type(data).__name__)
        return
    cols = cols_of(df)
    n = getattr(df, "shape", [0, 0])[0] if hasattr(df, "shape") else len(df)
    kv(label + "_keys", ",".join(cols[:40]))
    kv(label + "_n", n)

    kv(label + "_has_suspendFlag", "1" if "suspendFlag" in cols else "0")
    kv(label + "_has_amount", "1" if "amount" in cols else "0")
    kv(label + "_has_volume", "1" if "volume" in cols else "0")
    kv(label + "_has_tradingDay", "1" if "tradingDay" in cols else "0")
    tail_n = min(6, n)
    for c in ("tradingDay", "suspendFlag", "amount", "volume"):
        if c in cols:
            kv(label + "_tail_" + c, short(list(df[c])[-tail_n:], 160))

    bases = ("amount", "volume") if args.field == "both" else (args.field,)
    for basis in bases:
        if basis not in cols:
            continue
        vals = series_of(df, basis)
        # 剔停牌：有 suspendFlag 则 flag==0；无则退 volume>0（intraday_ratio.go fail-closed）。
        if "suspendFlag" in cols:
            flags = series_of(df, "suspendFlag")
            clean = [v for v, fl in zip(vals, flags) if fl is None or fl == 0]
            kv(label + "_" + basis + "_suspend_drop", len(vals) - len(clean))
        else:
            clean = [v for v in vals if v is None or v > 0]
            kv(label + "_" + basis + "_suspend_drop", "n/a(no_flag,vol_filter)")
        clean = [v for v in clean if v is not None]
        if len(clean) < 6:
            kv(label + "_" + basis + "_short", "need>=6 got=%d" % len(clean))
            continue
        last, prev = clean[-1], clean[-2]
        ma5 = sum(clean[-6:-1]) / 5.0
        r_yest = last / prev if prev else None
        r_ma5 = last / ma5 if ma5 else None
        kv(label + "_" + basis + "_last", short(last))
        kv(label + "_" + basis + "_prev", short(prev))
        kv(label + "_" + basis + "_ma5", short(round(ma5, 4) if ma5 else ma5))
        kv(label + "_" + basis + "_r_yest", short(round(r_yest, 4) if r_yest is not None else None))
        kv(label + "_" + basis + "_r_ma5", short(round(r_ma5, 4) if r_ma5 is not None else None))
        is_vol = (r_yest is not None and r_ma5 is not None and
                  r_yest > args.r_yest and r_ma5 > args.r_ma5)
        kv(label + "_" + basis + "_is_vol", "1" if is_vol else "0")


def run_vol(xtdata, args):
    kv("VOL_begin", "1")
    kv("VOL_gates", "r_yest>%.2f and r_ma5>%.2f" % (args.r_yest, args.r_ma5))
    codes = ([c.strip() for c in args.codes.split(",") if c.strip()]
             if args.codes else VOL_DEFAULT_CODES)
    kv("VOL_codes", ",".join(codes))
    kv("VOL_code_caveat", "000001.SH=SSE_COMP 000001.SZ=PINGAN_BANK both listed")
    if args.download:
        do_download(xtdata, codes, ["1d"], args, "VOL")
    for code in codes:
        probe_vol(xtdata, code, "VOL_" + code.replace(".", "_"), args)
    kv("VOL_end", "1")


def main():
    ap = argparse.ArgumentParser(description="QMT money-flow + relative-volume unified read-only probe")
    ap.add_argument("--section", default="all", choices=["all", "flow", "vol"],
                    help="which section to run (default: all)")
    ap.add_argument("--codes", default="",
                    help="comma list of FULLY-SUFFIXED codes; FLOW tests each as-is, "
                         "VOL uses the same list; empty = built-in defaults")
    ap.add_argument("--probe-stock", default="",
                    help="FLOW-only: extra stock code WITH suffix as flow control")
    ap.add_argument("--download", action="store_true",
                    help="call download_history_data2 (or legacy) first (slower)")
    ap.add_argument("--start", default="20261001", help="download/read start_time YYYYMMDD")
    ap.add_argument("--end", default="20261119", help="download/read end_time YYYYMMDD")
    ap.add_argument("--field", default="both", choices=["both", "amount", "volume"],
                    help="VOL: which basis to report (both by default)")
    ap.add_argument("--r-yest", type=float, default=R_YEST_GATE, help="VOL r_yest gate")
    ap.add_argument("--r-ma5", type=float, default=R_MA5_GATE, help="VOL r_ma5 gate")
    args = ap.parse_args()

    print("QPROBE_START probe=qmt_signals_v1 section=%s" % args.section)
    try:
        from xtquant import xtdata  # xtdata-only; no trader session
    except Exception as exc:  # noqa: BLE001
        kv("xtdata_import", "FAIL " + short(exc))
        print("QPROBE_DONE verdict=blocked")
        return 2
    kv("xtdata_import", "OK")

    if args.section in ("all", "flow"):
        run_flow(xtdata, args)
    if args.section in ("all", "vol"):
        run_vol(xtdata, args)

    print("QPROBE_DONE paste_all_QPROBE_lines_back verdict=see_flow_hits_and_vol_is_vol")
    return 0


if __name__ == "__main__":
    sys.exit(main())
