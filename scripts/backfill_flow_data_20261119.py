#!/usr/bin/env python3
# backfill_flow_data_20261119.py — §FLOW-MONEY 历史数据补齐（owner 裁决"数据云端也要补齐"，2026-11-19）。
#
# 背景：云端（广州 Ubuntu）对东财全族 IP 被封（push2/push2delay/push2his 全 RemoteDisconnected，实测），
#       云端只能从新浪拿指数日 K（无 amount）。历史补数改走**本机取数 → JSON → scp → 云端写库**：
#   1. baostock（本机 sidecar http://127.0.0.1:8787，owner 裁决"两者都要"之一）：上证 sh.000001 /
#      沪深300 sh.000300 的完整 OHLCV+amount（元）历史 → 补 index_daily（含 11-06 断更段）。
#   2. 东财 fflow daykline（本机实测可达，klt=101）：大盘逐日主力净额（元）→ 配 baostock 当日
#      成交额 → market_flow_pct / flow_market_delta。
#   3. 量比历史：baostock amount 降序序列 → r_yest/r_ma5（阈值与 signalctl 同口径：放量
#      r_yest>1.2 且 r_ma5>1.0；缩量 r_yest<1.0 且 r_ma5<0.95（2026-11-25 修正）；平量 0）→ vol_surge_delta。
#
# 用法：
#   本机生成： python3 scripts/backfill_flow_data_20261119.py generate --out /tmp/flow_backfill.json
#   离线生成（§FLOW-MONEY 现网形态，2026-11-19 实测后新增）：
#     · 本机 baostock 直连已断（login 报"网络接收错误"）⇒ 指数走广州 sidecar 的 CSV：
#         ssh gz "curl -sS 'http://127.0.0.1:8788/index_kline?code=sh.000001&start=2020-01-02&end=...'" \
#           > /tmp/idx/000001.SH.csv      （000300.SH.csv 同形）
#     · 广州对东财全族不可达（push2/push2his/push2delay 全 RST，实测）⇒ fflow 由**盒子**取：
#         scripts/win_remote_exec.sh exec '...Invoke-WebRequest ...daykline...' > /tmp/fflow.json
#       然后： generate --index-csv-dir /tmp/idx --fflow-file /tmp/fflow.json
#   云端应用： python3 scripts/backfill_flow_data_20261119.py apply --file /tmp/flow_backfill.json \
#               --db /var/lib/quant-trading-v2/trading.db [--apply] [--skip-backup]
#               # 缺 --apply 只预览不写；盘余量 < 1.2× 库大小时必须 --skip-backup 显式确认
#
# 写库纪律：
#   - 只写**历史日**（排除北京今日）——引擎盘中经 UpsertFlowVolumeOnly/MoneyOnly 写当日行，
#     回填绝不碰当日，两写口互不覆盖的纪律不破。
#   - apply 缺省 dry-run（打印计数），显式 --apply 才写；写前对 db 做 sqlite backup 到同目录 .bak。
#   - flow_factor_daily 用 INSERT OR REPLACE 全行写是安全的：回填行是**当时无任何数据的整行**
#     （表此前 0 行），不覆盖任何已有读数；今日行被排除。
# English: backfill index_daily (SH Composite/CSI300 full history via local baostock) and the
# flow_factor_daily history legs (volume ratios + index money-flow deltas); local fetch, cloud apply.
import argparse
import datetime as dt
import gzip
import json
import os
import shutil
import sys
import urllib.request

PYDATA = "http://127.0.0.1:8787"
# 与 signalctl/flow_regime.go 阈值逐字同口径（改一处必改另一处，门禁 §226 锁语义）。
R_YES_SURGE, R_MA5_SURGE = 1.2, 1.0
R_YES_SHRINK, R_MA5_SHRINK = 1.0, 0.9
FLOW_FULL_SCALE_PCT = 3.0  # 与 signalctl.flowFullScalePct 同口径


def vol_state(r_yest, r_ma5):
    """量态判据，与 signalctl.VolStateOf 逐字同口径。"""
    if r_yest is None or r_ma5 is None or r_yest <= 0 or r_ma5 <= 0:
        return None
    if r_yest > R_YES_SURGE and r_ma5 > R_MA5_SURGE:
        return "surge"
    if r_yest < R_YES_SHRINK and r_ma5 < R_MA5_SHRINK:
        return "shrink"
    return "neutral"


def vol_delta(state):
    if state == "surge":
        return 1.0
    if state == "shrink":
        return -1.0
    return 0.0


def fetch_baostock_index(bs_code, start, end):
    """直连 baostock 库取指数日 K（升序）。volume 股→/100 归手（与 dataload baostock.go:166 同口径），
    amount 已是元（main.go:91-92）。返回 [{date,open,high,low,close,vol(手),amount(元)}]。"""
    import baostock as bs
    lg = bs.login()
    if lg.error_code != "0":
        raise RuntimeError(f"baostock login failed: {lg.error_msg}")
    try:
        rs = bs.query_history_k_data_plus(
            bs_code, "date,open,high,low,close,volume,amount",
            start_date=start, end_date=end, frequency="d", adjustflag="3")
        if rs.error_code != "0":
            raise RuntimeError(f"baostock query failed: {rs.error_msg}")
        rows = []
        while rs.error_code == "0" and rs.next():
            rec = rs.get_row_data()
            def f(v):
                try:
                    return float(v)
                except (ValueError, TypeError):
                    return None
            vol = f(rec[5])
            rows.append({
                "date": rec[0][:10],
                "open": f(rec[1]), "high": f(rec[2]), "low": f(rec[3]),
                "close": f(rec[4]), "vol": vol / 100.0 if vol else None,
                "amount": f(rec[6]),
            })
        return [r for r in rows if r["date"] and r["close"]]
    finally:
        bs.logout()


def parse_index_csv(path):
    """解析 pydata sidecar `/index_kline` 返回的 CSV（§FLOW-MONEY 离线生成腿）。

    为什么要有这条腿：本机 baostock 直连已不可用（`login()` 报"网络接收错误"），
    而广州 sidecar 是现成的、有数据的（实测 1667 根/只）。口径与 `fetch_baostock_index`
    逐字一致：sidecar 原样返回 baostock 值 ⇒ volume 股需 /100 归手，amount 已是元。
    English: parse the pydata sidecar index_kline CSV into the same row shape.
    """
    rows = []
    with open(path, "r", encoding="utf-8") as f:
        header = f.readline().strip().split(",")
        idx = {name: i for i, name in enumerate(header)}
        need = ("date", "open", "high", "low", "close", "volume", "amount")
        if any(k not in idx for k in need):
            raise RuntimeError(f"index csv 缺列: {path} header={header}")
        for line in f:
            parts = line.rstrip("\n").split(",")
            if len(parts) < len(header):
                continue

            def f(name):
                try:
                    return float(parts[idx[name]])
                except (ValueError, TypeError, IndexError):
                    return None

            vol = f("volume")
            rows.append({
                "date": parts[idx["date"]][:10],
                "open": f("open"), "high": f("high"), "low": f("low"),
                "close": f("close"), "vol": vol / 100.0 if vol else None,
                "amount": f("amount"),
            })
    return [r for r in rows if r["date"] and r["close"]]


def load_fflow_file(path):
    """读取预先落盘的 fflow 日线（盒子上取的），形状与 `fetch_em_fflow_index` 一致。"""
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    out = []
    for line in ((data.get("data") or {}).get("klines") or []):
        parts = line.split(",")
        if len(parts) < 2:
            continue
        try:
            out.append({"date": parts[0][:10], "main_net": float(parts[1])})
        except ValueError:
            continue
    return out


def fetch_em_fflow_index(days):
    """东财 push2his fflow daykline（klt=101）：大盘逐日主力净额（元）。本机实测可达。"""
    url = ("https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get"
           "?secid=1.000001&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56"
           f"&klt=101&lmt={days}")
    req = urllib.request.Request(url, headers={
        "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
        "Referer": "https://quote.eastmoney.com/",
        "Accept": "application/json, text/plain, */*",
    })
    with urllib.request.urlopen(req, timeout=30) as r:
        data = json.loads(r.read().decode("utf-8", "ignore"))
    out = []
    for line in ((data.get("data") or {}).get("klines") or []):
        parts = line.split(",")
        if len(parts) < 6:
            continue
        out.append({"date": parts[0][:10], "main_net": float(parts[1])})
    return out


def cmd_generate(args):
    end = dt.date.today().strftime("%Y%m%d")
    start = "20200102"
    payload = {"generated_at": dt.datetime.now().isoformat(timespec="seconds"),
               "today": dt.date.today().isoformat(), "index_daily": [], "flow_rows": []}

    # ① index_daily：上证 + 沪深300 全历史（baostock amount=元，vol 已由 sidecar 归一手）。
    for bs_code, ts_code in (("sh.000001", "000001.SH"), ("sh.000300", "000300.SH")):
        if args.index_csv_dir:
            rows = parse_index_csv(os.path.join(args.index_csv_dir, ts_code + ".csv"))
        else:
            rows = fetch_baostock_index(bs_code, start, end)
        rows.sort(key=lambda r: r["date"])
        prev_close = None
        for r in rows:
            pc = prev_close
            chg = (r["close"] - pc) if (pc and r["close"] and pc) else None
            pct = (chg / pc * 100) if (chg is not None and pc) else None
            payload["index_daily"].append({
                "ts_code": ts_code, "trade_date": r["date"].replace("-", ""),
                "open": r["open"], "high": r["high"], "low": r["low"], "close": r["close"],
                "pre_close": pc, "change": chg, "pct_chg": pct,
                "vol": r["vol"], "amount": r["amount"],
            })
            prev_close = r["close"]
        print(f"[generate] {ts_code}: {len(rows)} rows "
              f"({rows[0]['date']} ~ {rows[-1]['date']})" if rows else f"[generate] {ts_code}: 0 rows", file=sys.stderr)

    # ② flow_factor_daily 历史：量比三列（baostock amount 序列）+ 大盘资金流两列（东财 fflow）。
    sh = [r for r in payload["index_daily"] if r["ts_code"] == "000001.SH" and r["amount"]]
    amounts_asc = [(r["trade_date"], r["amount"]) for r in sh]
    fflow = {r["date"]: r["main_net"]
             for r in (load_fflow_file(args.fflow_file) if args.fflow_file else fetch_em_fflow_index(1200))}
    print(f"[generate] fflow rows: {len(fflow)}", file=sys.stderr)
    for i in range(5, len(amounts_asc)):  # 前 5 根不足窗口，跳过
        win = [a for _, a in amounts_asc[i - 5:i + 1]]  # 升序 6 根（旧→新）
        last, prev = win[-1], win[-2]
        r_yest = last / prev if prev > 0 else None
        mean5 = sum(win[:-1]) / 5.0
        r_ma5 = last / mean5 if mean5 > 0 else None
        state = vol_state(r_yest, r_ma5)
        day_compact = amounts_asc[i][0]
        day = f"{day_compact[:4]}-{day_compact[4:6]}-{day_compact[6:]}"
        row = {"trade_date": day, "r_yest": r_yest, "r_ma5": r_ma5,
               "vol_surge_delta": vol_delta(state) if state is not None else None,
               "market_flow_pct": None, "flow_market_delta": None,
               "source": "eastmoney_backfill"}
        if day in fflow and last > 0:
            pct = fflow[day] / last * 100  # 净额/当日成交额（元/元）
            row["market_flow_pct"] = round(pct, 6)
            row["flow_market_delta"] = max(-1.0, min(1.0, pct / FLOW_FULL_SCALE_PCT))
        payload["flow_rows"].append(row)
    print(f"[generate] flow_rows: {len(payload['flow_rows'])}", file=sys.stderr)

    out = args.out
    if out.endswith(".gz"):
        with gzip.open(out, "wt", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False)
    else:
        with open(out, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False)
    print(f"[generate] written {out} (index={len(payload['index_daily'])} flow={len(payload['flow_rows'])})",
          file=sys.stderr)


def cmd_apply(args):
    opener = gzip.open if args.file.endswith(".gz") else open
    with opener(args.file, "rt", encoding="utf-8") as f:
        payload = json.load(f)
    today = payload.get("today") or dt.date.today().isoformat()
    today_compact = today.replace("-", "")
    import sqlite3
    conn = sqlite3.connect(args.db)
    cur = conn.cursor()
    idx_rows = [r for r in payload["index_daily"] if r["trade_date"] != today_compact]
    flow_rows = [r for r in payload["flow_rows"] if r["trade_date"] != today]
    print(f"[apply] excluding today={today}: index={len(idx_rows)} flow={len(flow_rows)}")
    # 写前备份（同目录 .bak，sqlite 在线 backup API）——只在真写时做，预览不做。
    # §FLOW-MONEY 现场约束：广州库已 10GB、盘只剩 7.4G ⇒ 全量副本做不出来。
    # 因此先量余量：够 1.2× 库大小才备份；不够时必须显式 --skip-backup 才继续，
    # 并打印"可逆凭据"（本次只写此前不存在的目标集，按 trade_date / ts_code 可整段删除）。
    if args.apply:
        bak = args.db + ".bak_backfill_20261119"
        db_size = os.path.getsize(args.db)
        free = shutil.disk_usage(os.path.dirname(os.path.abspath(args.db))).free
        if args.skip_backup or free < db_size * 1.2:
            if not args.skip_backup:
                print(f"[apply] BACKUP_ABORT reason=disk_headroom db={db_size} free={free} "
                      f"（需 --skip-backup 显式确认）", file=sys.stderr)
                conn.close()
                sys.exit(3)
            print(f"[apply] BACKUP_SKIPPED reason=explicit_flag db={db_size} free={free}", file=sys.stderr)
        else:
            src = sqlite3.connect(args.db)
            dst = sqlite3.connect(bak)
            src.backup(dst)
            dst.close()
            src.close()
            print(f"[apply] backup -> {bak}")

    # 只补空、不覆盖：已有 (ts_code, trade_date) 行一律跳过（存量 000300.SH 1658 行、
    # 以及任何已由盘中腿写入的历史行，都不该被一份离线快照原地替换）。
    have = set(cur.execute(
        "SELECT ts_code, trade_date FROM index_daily WHERE ts_code IN ('000001.SH','000300.SH')"
    ).fetchall())
    new_idx = [r for r in idx_rows if (r["ts_code"], r["trade_date"]) not in have]
    have_flow = set(x[0] for x in cur.execute("SELECT trade_date FROM flow_factor_daily").fetchall())
    new_flow = [r for r in flow_rows if r["trade_date"] not in have_flow]
    print(f"[apply] 补空：index {len(idx_rows)}→{len(new_idx)} 行（跳过已存在 {len(idx_rows)-len(new_idx)}）"
          f"，flow {len(flow_rows)}→{len(new_flow)} 行（跳过已存在 {len(flow_rows)-len(new_flow)}）")
    print(f"[apply] 回滚凭据：DELETE FROM index_daily WHERE ts_code='000001.SH'; "
          f"DELETE FROM flow_factor_daily WHERE trade_date>='{new_flow[0]['trade_date'] if new_flow else '-'}';")
    if not args.apply:
        print("[apply] DRY-RUN（--apply 才写库）")
        conn.close()
        return
    cur.executemany(
        "INSERT OR REPLACE INTO index_daily (ts_code,trade_date,open,high,low,close,pre_close,"
        "change,pct_chg,vol,amount) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
        [(r["ts_code"], r["trade_date"], r["open"], r["high"], r["low"], r["close"],
          r["pre_close"], r["change"], r["pct_chg"], r["vol"], r["amount"]) for r in new_idx])
    cur.executemany(
        "INSERT OR REPLACE INTO flow_factor_daily (trade_date,market_flow_pct,sector_flow_pct,"
        "r_yest,r_ma5,flow_market_delta,flow_sector_delta,vol_surge_delta,source,updated_at) "
        "VALUES (?,?,NULL,?,?,?,NULL,?,?,datetime('now','localtime'))",
        [(r["trade_date"], r["market_flow_pct"], r["r_yest"], r["r_ma5"],
          r["flow_market_delta"], r["vol_surge_delta"], r["source"]) for r in new_flow])
    conn.commit()
    n1 = cur.execute("SELECT count(*),max(trade_date) FROM index_daily WHERE ts_code='000001.SH'").fetchone()
    n2 = cur.execute("SELECT count(*) FROM flow_factor_daily").fetchone()
    print(f"[apply] DONE index_daily 000001.SH={n1} flow_factor_daily={n2}")
    conn.close()


def main():
    ap = argparse.ArgumentParser(description="§FLOW-MONEY 历史数据补齐（本机生成/云端应用）")
    sub = ap.add_subparsers(dest="cmd", required=True)
    g = sub.add_parser("generate", help="本机：baostock+东财取数生成 JSON")
    g.add_argument("--out", default="/tmp/flow_backfill.json")
    g.add_argument("--index-csv-dir", default="",
                   help="离线：目录下放 000001.SH.csv/000300.SH.csv（pydata sidecar /index_kline 原始 CSV）")
    g.add_argument("--fflow-file", default="",
                   help="离线：预先落盘的 fflow daykline JSON（盒子取数，绕过云端东财封锁）")
    g.set_defaults(func=cmd_generate)
    a = sub.add_parser("apply", help="云端：JSON 写库（缺省 dry-run）")
    a.add_argument("--file", required=True)
    a.add_argument("--db", required=True)
    a.add_argument("--apply", action="store_true")
    a.add_argument("--skip-backup", action="store_true",
                   help="盘余量不足时显式跳过全量副本（本次只补空，可整段删除回滚）")
    a.set_defaults(func=cmd_apply)
    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
