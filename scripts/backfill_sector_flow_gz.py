#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FLOW-SECTOR-AGG 板块资金流历史回填（owner 2026-11-20 指示「历史回填做了」）。

## 背景与口径

`sector_flow_daily` 以前只有当日行（引擎每轮 `fillFlowMoney` 现算现写）。历史回填长期被判
"不可行"，依据是「`push2his/fflow/daykline` 拒 `90.BK*`」。**2026-11-20 复核推翻了这条**：
从盒子实测 `https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get?secid=90.BK0420`
返回 `rc=0` 且 **121 根日线**（2026-05-27 起）。当时间歇断连被记成了结构不可用。

字段顺序（实测，`fields2=f51..f65`）：
    f51 日期 | f52 主力净额 | f53 小单净额 | f54 中单净额 | f55 大单净额 | f56 超大单净额
    f57 主力净占比(%) | f58..f61 各档占比
上游**不直接给当日成交额**，但 `f57 = 主力净额 / 成交额 × 100`，所以
    amount = net / (f57/100)      （f57 ≠ 0 时）
    delta  = clamp(f57 / 3, -1, 1)（与 `signalctl.FlowPctToDelta` 同一个归一，只是分子分母约掉了）
两者都是"用上游已经算好的商反推"，不引入第二套公式。f57=0（净额恰好为 0）⇒ amount 与 delta
都留 NULL —— 那不是"中性"，是"这一栏无法从占比反推"。

## 三条硬边界

1. **只写 `sector_flow_daily`**：不碰 orders/fills/持仓/资金，也不碰 `flow_factor_daily`
   （那是大盘与聚合两列，引擎自己写）。
2. **幂等 + 断点续跑**：主键 `(trade_date, sector)`，`ON CONFLICT DO UPDATE`；每个板块做完
   就落一次状态文件，中断后重跑从没做完的板块继续（496 个板块 × 121 天，中途断了不必重来）。
3. **今天不覆盖**：当日行由引擎现算（口径与这里同源），回填只负责"今天之前"，
   避免把引擎刚写的行用历史批量盖一遍（时间戳与 source 语义都会被冲掉）。

## 数据来源

经**盒子中继**（`/flow/money`，盒子能到东财而广州被 RST）：
  1. `kind=clist&fs=m:90+t:2&fields=f12,f14` 翻页取全部行业板块（实测 496 个）；
  2. `kind=fflow&secid=90.BKxxxx&klt=101&lmt=0` 取该板块全部日线。

English: backfills `sector_flow_daily` history from the box relay's per-board fflow daily series;
net/amount/delta are derived from the upstream's own figures (no second formula); idempotent,
resumable, never rewrites today's engine-written row.
"""
import argparse
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime

DATA_DIR = os.environ.get("QUANT_DATA_DIR", "/var/lib/quant-trading-v2")
AUTH = os.path.join(DATA_DIR, "auth.json")
DB_PATH = os.environ.get("SECTOR_FLOW_DB", os.path.join(DATA_DIR, "trading.db"))
STATE_PATH = os.environ.get("SECTOR_FLOW_STATE", os.path.join(DATA_DIR, "sector_flow_backfill_state.json"))
OWNER_UID = os.environ.get("OWNER_UID", "u_1785377969825355000")

REQ_TIMEOUT = 25
SLEEP_MS = int(os.environ.get("SECTOR_FLOW_SLEEP_MS", "40"))
PAGE = 100
MAX_PAGES = 20
INDUSTRY_FS = "m:90+t:2"
LIST_FIELDS = "f12,f14"

# FlowPctToDelta 的同一个满档口径（3%）；这里只做 clamp(pct/3)，不引入新阈值。
FULL_SCALE_PCT = 3.0


def die(reason):
    print("SECTOR_FLOW_BACKFILL_RESULT=ABORT reason=%s" % reason)
    sys.exit(1)


def load_endpoint():
    """从运营账号在册快照取 (gateway_url, token)：地址跟着钥匙的持有者走。"""
    base = (os.environ.get("QUANT_GATEWAY_URL") or "").strip()
    token = (os.environ.get("QUANT_GATEWAY_TOKEN") or "").strip()
    if base and token:
        return base.rstrip("/"), token
    try:
        with open(AUTH, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError) as exc:
        die("auth_unreadable:%s" % exc.__class__.__name__)
    cfg = [c for c in (doc.get("configs") or [])
           if c.get("key") == "quant_config_json_v1" and c.get("user_id") == OWNER_UID]
    if not cfg:
        die("owner_config_missing")
    raw = cfg[0].get("value")
    qc = json.loads(raw) if isinstance(raw, str) else (raw or {})
    qmt = qc.get("qmt") or {}
    base = str(qmt.get("gateway_url") or "").strip()
    token = str(qmt.get("token") or "").strip()
    if not base or not token:
        die("gateway_endpoint_missing")
    return base.rstrip("/"), token


def relay(base, token, params):
    """取一次中继响应并解出上游原始 body 的 dict。失败即抛错（绝不拿 0 冒充读数）。"""
    url = base + "/flow/money?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/json",
    })
    with urllib.request.urlopen(req, timeout=REQ_TIMEOUT) as resp:
        env = json.loads(resp.read().decode("utf-8", "replace"))
    if not env.get("ok"):
        raise RuntimeError("relay_not_ok status=%s err=%s" % (
            env.get("upstream_status"), env.get("err")))
    body = env.get("upstream_body") or ""
    if not body.strip():
        raise RuntimeError("relay_empty_body")
    out = json.loads(body)
    if not isinstance(out, dict):
        raise RuntimeError("relay_body_not_object")
    return out


def rows_of(payload):
    """解 clist 的两种 diff 形态（索引 map / 数组），按索引排序保证顺序确定。"""
    data = (payload or {}).get("data") or {}
    diff = data.get("diff")
    rows = []
    if isinstance(diff, dict):
        def idx(item):
            key = str(item[0])
            return int(key) if key.isdigit() else 0
        rows = [item[1] for item in sorted(diff.items(), key=idx)]
    elif isinstance(diff, list):
        rows = list(diff)
    total = data.get("total")
    try:
        total = int(total)
    except (TypeError, ValueError):
        total = -1
    return [r for r in rows if isinstance(r, dict)], total


def industry_boards(base, token):
    """行业板块 [(bk_code, name)]，按 BK 码升序（确定性）。"""
    out = []
    total = -1
    for page in range(1, MAX_PAGES + 1):
        payload = relay(base, token, {"kind": "clist", "fs": INDUSTRY_FS,
                                      "fields": LIST_FIELDS, "pn": str(page), "pz": str(PAGE)})
        rows, page_total = rows_of(payload)
        if page == 1:
            total = page_total
        for row in rows:
            code = str(row.get("f12") or "").strip()
            name = str(row.get("f14") or "").strip()
            if code.startswith("BK") and code[2:].isdigit() and name:
                out.append((code, name))
        if not rows or (total > 0 and len(out) >= total):
            break
        if SLEEP_MS > 0:
            time.sleep(SLEEP_MS / 1000.0)
    # 去重（分页边界上偶尔会重复一行），保持码序
    seen = set()
    uniq = []
    for code, name in sorted(out, key=lambda x: x[0]):
        if code in seen:
            continue
        seen.add(code)
        uniq.append((code, name))
    return uniq, total


def daily_rows(payload):
    """把一个板块的 fflow 日线解成 [(date, net, pct)]。"""
    kl = ((payload or {}).get("data") or {}).get("klines") or []
    out = []
    for line in kl:
        parts = str(line).split(",")
        if len(parts) < 7:
            continue
        date = parts[0].strip()
        if len(date) != 10 or date[4] != "-":
            continue
        try:
            net = float(parts[1])
            pct = float(parts[6])
        except ValueError:
            continue
        out.append((date, net, pct))
    return out


def derive(net, pct):
    """(amount, delta)：两者都由上游自己的净占比反推；pct=0 ⇒ 双 NULL（不是中性）。"""
    if pct == 0.0:
        return None, None
    amount = net / (pct / 100.0)
    if amount <= 0:
        return None, None
    d = pct / FULL_SCALE_PCT
    if d > 1.0:
        d = 1.0
    elif d < -1.0:
        d = -1.0
    return amount, d


def load_state():
    try:
        with open(STATE_PATH, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        if isinstance(doc, dict):
            return doc
    except (OSError, ValueError):
        pass
    return {"done": [], "rows": 0}


def save_state(state):
    tmp = STATE_PATH + ".tmp"
    try:
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(state, fh, ensure_ascii=True, sort_keys=True)
        os.replace(tmp, STATE_PATH)
    except OSError as exc:
        print("SECTOR_FLOW_BACKFILL_STATE_FAIL %s" % exc.__class__.__name__)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true", help="只打印将写多少行，不落库")
    ap.add_argument("--days", type=int, default=0, help="只回填最近 N 个交易日（0=上游给多少要多少）")
    ap.add_argument("--reset", action="store_true", help="忽略断点状态从头跑")
    args = ap.parse_args()

    today = datetime.now().astimezone().strftime("%Y-%m-%d")
    base, token = load_endpoint()
    try:
        boards, total = industry_boards(base, token)
    except (urllib.error.URLError, RuntimeError, ValueError, OSError) as exc:
        die("board_list_failed:%s" % exc.__class__.__name__)
    if not boards:
        die("board_list_empty")
    print("SECTOR_FLOW_BACKFILL_BOARDS n=%d upstream_total=%s" % (len(boards), total))

    state = {"done": [], "rows": 0} if args.reset else load_state()
    done = set(state.get("done") or [])
    start_done = len(done)
    total_rows = int(state.get("rows") or 0)

    conn = sqlite3.connect(DB_PATH, timeout=30)
    conn.execute("PRAGMA journal_mode=WAL")
    written = 0
    skipped_boards = 0
    failed = []
    try:
        for code, name in boards:
            if code in done:
                skipped_boards += 1
                continue
            try:
                payload = relay(base, token, {"kind": "fflow", "secid": "90." + code,
                                              "klt": "101", "lmt": "0"})
            except (urllib.error.URLError, RuntimeError, ValueError, OSError) as exc:
                failed.append("%s:%s" % (code, exc.__class__.__name__))
                continue
            rows = daily_rows(payload)
            if args.days > 0:
                rows = rows[-args.days:]
            batch = []
            for date, net, pct in rows:
                if date >= today:
                    continue  # 今天由引擎现算，回填不覆盖
                amount, delta = derive(net, pct)
                batch.append((date, name, net, amount, delta))
            if not args.dry_run and batch:
                conn.executemany(
                    "INSERT INTO sector_flow_daily (trade_date, sector, net_inflow, amount, delta) "
                    "VALUES (?,?,?,?,?) ON CONFLICT(trade_date, sector) DO UPDATE SET "
                    "net_inflow=excluded.net_inflow, amount=excluded.amount, delta=excluded.delta",
                    batch)
                conn.commit()
            written += len(batch)
            done.add(code)
            if not args.dry_run:
                total_rows += len(batch)
                state = {"done": sorted(done), "rows": total_rows,
                         "updated_at": int(time.time())}
                save_state(state)
            if SLEEP_MS > 0:
                time.sleep(SLEEP_MS / 1000.0)
    finally:
        conn.close()

    print("SECTOR_FLOW_BACKFILL_WRITE rows=%d boards_done=%d boards_skipped=%d failed=%d %s" % (
        written, len(done) - start_done, skipped_boards, len(failed), " ".join(failed[:8])))
    if args.dry_run:
        print("SECTOR_FLOW_BACKFILL_RESULT=DRYRUN rows=%d" % written)
        return 0
    print("SECTOR_FLOW_BACKFILL_RESULT=OK rows_this_run=%d boards=%d rows_total=%d" % (
        written, len(done), total_rows))
    return 0


if __name__ == "__main__":
    sys.exit(main())
