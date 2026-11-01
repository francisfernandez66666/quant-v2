#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""probe_qmt_feed.py — §SUBSCRIPTION-A（2026-11-01 裁决 3）券商行情盘中只读探针。

为什么另写一个脚本，而不直接用现成的 scripts/probe_xtquant.py：
  现脚本第 1 段会新建一条交易会话（xt 的 trader 通道）做连接探针 —— 那正是 2026-10-29 把 QMT 客户端
  3~6 分钟拖崩一次的元凶（每次 connect 在客户端共享内存占一个 writer 槽且**不回收**，实测
  713 连败；见 docs/POSTMORTEM_QMT_LINK_20261029.md）。我们要读的只有 xtdata 的行情，
  所以本脚本**不建任何 trader 会话、不进循环、只跑一次**，并配门禁负锁盯这两条形。

产出的四条判读（都是 A 腿的硬前置，缺一条就不许开 qmt_feed_primary）：
  ① 量纲：amount/(lastPrice*volume) 的中位数 ⇒ 股(≈1) / 手(≈100) / 不成立(其它)。
     这一条直接决定 VWAP 会不会差 100 倍，是钱的问题；判不出来就停在 A6 之前，**不猜**。
  ② 覆盖率 hit=n/total：80% 阈值与"分母取可交易标的数"的口径要靠它复核。
  ③ 五档字段：xtdata 是否给 ask/bid 五档、字段叫什么名 ⇒ 决定 A7-2 能不能做。
  ④ 指数与 K 线：`000001.SH`/`000001.SZ` 各自返回什么（个股后缀函数会把 000001 补成 .SZ＝平安银行），
     以及 K 线接口能否拿到（依赖 numpy/pandas 环境；不可用时**只出取证单，不当场决定改法**——裁决 11）。

用法（决策机 PowerShell，只读、不下单、不落盘）：
    python probe_qmt_feed.py                      # 默认几只样票 + 指数候选
    python probe_qmt_feed.py --codes 600519,600000,002961
    python probe_qmt_feed.py --codes-file C:\\opt\\quant\\pool.txt   # 一行一个裸码（明天可直接喂现网监控池）
"""
import argparse
import json
import os
import statistics
import sys

sys.stdout.reconfigure(line_buffering=True)  # ssh 轮询友好：逐行刷，不等缓冲

CFG = r"C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json"


def kv(key, value):
    """机器可读单行（键=值），交付记录里可以直接粘这些行，不用重述。"""
    print("probe %s=%s" % (key, value))


def read_pool(args):
    """取要探测的裸码集合：命令行 > 文件 > 默认样票。"""
    codes = []
    if args.codes:
        codes = [c.strip() for c in args.codes.split(",") if c.strip()]
    elif args.codes_file and os.path.exists(args.codes_file):
        with open(args.codes_file, "r", encoding="utf-8", errors="replace") as f:
            codes = [ln.strip().split(".")[0] for ln in f if ln.strip()]
    else:
        codes = ["600519", "600000", "000001", "002961", "370181"]
    return codes


def suffixed(code):
    """裸码 → 带后缀（个股口径）。注意：**指数不能走这个函数**——
    6→.SH、其余→.SZ 的规则会把上证指数 000001 补成平安银行，这正是本探针要测出来的坑。"""
    return (code + ".SH") if code.startswith(("6", "9")) else (code + ".SZ")


def main():
    ap = argparse.ArgumentParser(description="QMT 行情只读探针（xtdata-only，一次不循环）")
    ap.add_argument("--codes", default="")
    ap.add_argument("--codes-file", default="")
    ap.add_argument("--index-codes", default="000001,000300,399001,399006")
    args = ap.parse_args()

    print("probe start ts=%s" % time_now())

    # ── 环境取证：xtdata / numpy / pandas。失败只报读数，不改任何东西（裁决 11）──
    try:
        from xtquant import xtdata  # 唯一允许的 xtquant 导入：行情只读，**不建交易会话**
    except Exception as e:  # noqa: BLE001
        kv("xtdata_import", "FAIL")
        kv("xtdata_err", repr(e)[:200])
        print("PROBE_DONE verdict=blocked")
        return 2
    kv("xtdata_import", "OK")
    for mod in ("numpy", "pandas"):
        try:
            m = __import__(mod)
            kv("py_env_%s" % mod, getattr(m, "__version__", "?"))
        except Exception as e:  # noqa: BLE001
            kv("py_env_%s" % mod, "none")
            kv("py_env_%s_err" % mod, repr(e)[:160])

    try:
        xtdata.connect()
    except Exception as e:  # noqa: BLE001
        kv("xtdata_connect", "FAIL")
        kv("xtdata_connect_err", repr(e)[:200])

    codes = read_pool(args)
    suff = [suffixed(c) for c in codes]
    try:
        raw = xtdata.get_full_tick(suff) or {}
    except Exception as e:  # noqa: BLE001
        kv("get_full_tick", "FAIL")
        kv("get_full_tick_err", repr(e)[:200])
        print("PROBE_DONE verdict=blocked")
        return 2

    hits = 0
    ratios = []
    missing_price = 0
    time_units = set()
    book_fields = set()
    sample = None
    for code, tk in raw.items():
        if not isinstance(tk, dict):
            continue
        lp = float(tk.get("lastPrice") or 0)
        vol = float(tk.get("volume") or 0)
        amt = float(tk.get("amount") or 0)
        if lp <= 0:
            missing_price += 1
            continue
        hits += 1
        if vol > 0 and amt > 0:
            ratios.append(amt / (lp * vol))
        t = tk.get("time") or tk.get("tickTime") or 0
        try:
            t = float(t)
        except (TypeError, ValueError):
            t = 0
        if t > 0:
            time_units.add("ms" if t >= 1e12 else "sec")
        # 五档：xtdata 的 get_full_tick 在部分构建里是 askPrice/bidPrice 列表，有些构建叫
        # askBids/bidOps……所以这里**只报观测到的键名**，不猜哪个是对的（A7-2 的字段清单靠它）
        for k in tk.keys():
            kl = str(k).lower()
            if "ask" in kl or "bid" in kl:
                book_fields.add(str(k))
        if sample is None:
            sample = tk

    total = len(suff)
    kv("pool_requested", total)
    kv("hit", "%d/%d" % (hits, total))
    if total:
        kv("hit_pct", "%.1f" % (100.0 * hits / total))
    kv("missing_price", missing_price)
    kv("time_field", ",".join(sorted(time_units)) if time_units else "none")
    kv("ask_fields", ",".join(sorted(book_fields)) if book_fields else "none")
    kv("book", "5" if any(str(k).lower().startswith("ask") for k in book_fields) else "0")
    if sample is not None:
        try:
            kv("tick_keys", ",".join(sorted(str(k) for k in sample.keys())[:30]))
        except Exception:  # noqa: BLE001
            pass

    # ── 量纲判读：写死规则，不许人当场解释 ──
    med = statistics.median(ratios) if ratios else 0.0
    kv("volume_ratio", "%.2f" % med)
    if med <= 0:
        unit = "unknown"
    elif 0.9 <= med <= 1.1:
        unit = "shares"
    elif 90 <= med <= 110:
        unit = "lots"
    else:
        unit = "implausible"
    kv("tick_unit", unit)

    # ── 指数：两种后缀各试一次，看拿到的是点位还是个股价 ──
    idx_codes = [c.strip() for c in args.index_codes.split(",") if c.strip()]
    for code in idx_codes:
        for suf in (".SH", ".SZ"):
            try:
                one = xtdata.get_full_tick([code + suf]) or {}
            except Exception as e:  # noqa: BLE001
                kv("idx_%s%s" % (code, suf.replace(".", "")), "ERR")
                kv("idx_%s%s_err" % (code, suf.replace(".", "")), repr(e)[:120])
                continue
            tk = one.get(code + suf) or {}
            lp = float(tk.get("lastPrice") or 0) if isinstance(tk, dict) else 0
            kv("idx_%s%s" % (code, suf.replace(".", "")), "%.2f" % lp if lp > 0 else "empty")
    kv("idx_note", "000001.SH 应≈上证指数点位；000001.SZ 若拿到的是平安银行股价(十几元量级)，"
                   "说明指数必须走独立后缀白名单，不能复用个股后缀函数")

    # ── K 线可行性：接口能不能返回东西（不猜单位、不落盘）──
    try:
        kl = xtdata.get_market_data([], [codes[0]], period="1d", count=5)
        kv("kline_ok", "1" if kl is not None and getattr(kl, "shape", None) is not None else "0")
        kv("kline_shape", str(getattr(kl, "shape", "n/a")))
    except Exception as e:  # noqa: BLE001
        kv("kline_ok", "0")
        kv("kline_err", repr(e)[:200])

    # 配置文件存在性只报布尔，绝不打印内容（里面有账户与令牌）
    kv("cfg_present", "1" if os.path.exists(CFG) else "0")

    verdict = "ok" if unit in ("shares", "lots") and hits > 0 else "blocked"
    print("PROBE_DONE verdict=%s" % verdict)
    if unit == "implausible":
        print("PROBE_NOTE 单位口径两档都不像 ⇒ A 腿停在 A6 之前，不许开 qmt_feed_primary")
    if unit == "lots":
        print("PROBE_NOTE 量纲=手 ⇒ 开主源前必须把 runtime.qmt_feed_volume_to_shares 配成 100")
    return 0 if verdict == "ok" else 1


def time_now():
    from datetime import datetime
    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


if __name__ == "__main__":
    sys.exit(main())
