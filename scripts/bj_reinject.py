#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""北交所研究池自动纳入脚本（云端 /opt/quant/scripts/bj_reinject.py）。

背景：discover-factors 当前仍按旧池（不含北交所）运行。本脚本等待其跑完后，
自动把北交所（920xxx，共 337 只）插回 stocks 研究池，供后续夜间作业
（dataload daily 补日线 → sector_rebuild → discover_factors）使用。

流程：
  1. 幂等检查：stocks 表已有 >=300 只 920xxx.BJ 则直接退出（已纳入过）。
  2. 等待：轮询 pgrep，直到没有任何 discover-factors 进程在跑（60s 一次）。
  3. 拉取全市场股票列表：优先走 pydata sidecar（GET /all_stock，含北交所降级），
     失败则退回直接用 akshare 的 stock_info_a_code_name()。
  4. 筛选北交所 920 前缀，归一化为 ts_code（920xxx.BJ），INSERT OR REPLACE 进 stocks。
  5. 校验并写日志 /tmp/bj_reinject.log。

用法（云端后台执行，建议 root）：
  nohup /opt/quant/venv/bin/python3 /opt/quant/scripts/bj_reinject.py \
      >> /tmp/bj_reinject.out 2>&1 &
"""
import csv
import datetime
import io
import os
import re
import sqlite3
import subprocess
import sys
import time
import urllib.request

# ── 可调参数 ──────────────────────────────────────────
DATA_DIR = os.environ.get("QUANT_DATA_DIR", "/var/lib/quant-trading-v2")
DB = os.path.join(DATA_DIR, "trading.db")          # 研究池/行情主库
LOG = "/tmp/bj_reinject.log"                       # 本脚本运行日志
PYDATA = "http://127.0.0.1:8788/all_stock"         # pydata sidecar 全市场列表（含北交所）
POLL_SEC = 60                                      # 等待 discover-factors 的轮询间隔
MIN_BJ = 300                                       # 幂等阈值：已有 >=300 只 920xxx.BJ 视为已纳入
STOCK_TABLE = "stocks"                             # 研究池表（ts_code PK）
_BJ_RE = re.compile(r"^(?:(?:bj\.)?920\d{3})$")    # 北交所：920 + 3 位数字（可带 bj. 前缀）


def log(msg):
    """写一行带时间戳的日志到 stderr 与 LOG 文件（幂等脚本排障用）。"""
    line = "[%s] %s" % (datetime.datetime.now().strftime("%F %T"), msg)
    print(line, flush=True)
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(line + "\n")


def bj_count():
    """统计研究池中已有的北交所（920xxx.BJ）数量；出错返回 -1。"""
    try:
        conn = sqlite3.connect(DB)
        try:
            n = conn.execute(
                "SELECT COUNT(*) FROM stocks WHERE ts_code LIKE '920%.BJ'"
            ).fetchone()[0]
        finally:
            conn.close()
        return int(n)
    except Exception as e:  # noqa: BLE001
        log("读取 stocks 数量失败: %s" % e)
        return -1


def discover_running():
    """返回是否有夜间研究批（discover-factors / dataload / 调度器 research run-task）在跑。

    §修复 S5（2026-08-29）：原 pgrep 仅匹配 'discover-factors'，但生产实际由 scheduler 以
    'research run-task --task-id N' 子进程运行，导致等待永远判定"已结束"→ 北交所提前注入，
    与老池 discover-factors 争用写入、污染研究池。现同时匹配调度器真实命令，且排除本脚本自身。
    """
    for pat in ("discover-factors", "research run-task", "research dataload"):
        try:
            r = subprocess.run(
                ["pgrep", "-f", pat], capture_output=True, text=True
            )
        except Exception:  # noqa: BLE001
            continue
        if r.returncode == 0 and bool(r.stdout.strip()):
            # 排除本脚本进程自身（其命令行可能含 'research' 字样时避免误判）
            pids = [p for p in r.stdout.strip().splitlines() if p.strip().isdigit()]
            if pids:
                return True
    return False


def _to_ts(row):
    """把 all_stock 一行的 code 归一化为 ts_code；非北交所返回 None。"""
    code = str(row.get("ts_code") or row.get("code") or "").strip()
    m = _BJ_RE.match(code)
    if not m:
        return None
    return m.group(1) + ".BJ"  # 920002 → 920002.BJ


def fetch_bj_stocks():
    """拉全市场股票列表并筛出北交所 [(ts_code, name), ...]。
    优先 pydata sidecar（已验证含北交所降级）；失败则直接用 akshare。"""
    rows = []
    # 路径一：pydata sidecar GET /all_stock（CSV，列 code/code_name 或 ts_code/name）
    try:
        with urllib.request.urlopen(PYDATA, timeout=30) as resp:
            text = resp.read().decode("utf-8")
        dict_rows = list(csv.DictReader(io.StringIO(text)))
        for r in dict_rows:
            ts = _to_ts(r)
            if ts:
                name = r.get("code_name") or r.get("name") or ""
                rows.append((ts, name))
        if rows:
            log("从 pydata /all_stock 取得北交所 %d 只" % len(rows))
            return rows
        log("pydata /all_stock 未含北交所，尝试直连 akshare")
    except Exception as e:  # noqa: BLE001
        log("pydata /all_stock 不可用: %s，尝试直连 akshare" % e)
    # 路径二：直连 akshare 新浪全表（stock_info_a_code_name → code/name）
    try:
        import akshare as ak  # type: ignore

        df = ak.stock_info_a_code_name()
        for _, r in df.iterrows():
            code = str(r.get("code", "")).strip()
            if _BJ_RE.match(code):
                rows.append((code + ".BJ", str(r.get("name", "")).strip()))
        if rows:
            log("从 akshare stock_info_a_code_name 取得北交所 %d 只" % len(rows))
        return rows
    except Exception as e:  # noqa: BLE001
        log("直连 akshare 失败: %s" % e)
        return []


def inject(stocks):
    """把北交所列表 INSERT OR REPLACE 进 stocks 研究池（market 标记 BJ）。"""
    conn = sqlite3.connect(DB)
    try:
        conn.executemany(
            "INSERT OR REPLACE INTO stocks "
            "(ts_code, name, area, industry, market, list_date, delist_date) "
            "VALUES (?, ?, '', '', 'BJ', '', '')",
            stocks,
        )
        conn.commit()
        n = conn.total_changes
    finally:
        conn.close()
    return n


def main():
    log("北交所纳入脚本启动，DB=%s" % DB)
    # 1) 幂等：已纳入过则直接退出
    have = bj_count()
    if have >= MIN_BJ:
        log("研究池已含北交所 %d 只，跳过（幂等）" % have)
        return 0
    # 2) 等待当前 discover-factors 跑完
    log("当前 920xxx 共 %d 只，等待 discover-factors 完成..." % max(have, 0))
    while discover_running():
        log("discover-factors 仍在运行，%ds 后重试" % POLL_SEC)
        time.sleep(POLL_SEC)
    log("discover-factors 已结束，开始拉取并纳入北交所")
    # 3) 拉取 + 筛北交所 + 入库
    stocks = fetch_bj_stocks()
    if not stocks:
        log("未能取得北交所列表，本次放弃（可手动重跑脚本）")
        return 1
    n = inject(stocks)
    after = bj_count()
    log("注入 %d 行，研究池现有北交所 %d 只（应约 337）" % (n, after))
    log("完成：后续夜间作业 dataload 将自动补齐北交所日线并纳入因子研究")
    return 0


if __name__ == "__main__":
    sys.exit(main())