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
            if fact:
                precalc[symbol] = fact
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
    return 0 if ok >= 1 else 3


if __name__ == "__main__":
    sys.exit(main())
