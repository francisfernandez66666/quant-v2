# -*- coding: utf-8 -*-
"""把网关派发队列的 TEST-DRY 演练行统一置 done（比 cleanup_test_rows 更宽：不限 inflight）。

用于演练后残留行的强制清账；随后回列最近 3 行核对。幂等，目标库为生产机 qmt_gateway/data.db。
"""
# reset_test_row.py - settle stale dry-test dispatch rows (ASCII source)
import sqlite3
from contextlib import closing

DB = r"C:\qmt\quant-trading-v2\qmt_gateway\data.db"

# §FIX-7（2026-09-20）：用 contextlib.closing 保证退出即关闭连接，避免句柄泄漏。
# 注意坑点：`with sqlite3.connect(...)` 只管事务提交/回滚，**不会**关闭连接——
# 要真正 close 必须用 closing()（或显式 conn.close()）。原实现此处漏了 close。
with closing(sqlite3.connect(DB)) as conn:
    conn.execute(
        "UPDATE dispatch SET status='done', result='ok-cleaned-by-test-cleanup' "
        "WHERE signal_id LIKE 'TEST-DRY%'")
    conn.commit()
    cur = conn.execute(
        "SELECT seq, kind, code, side, qty, status, order_id, result FROM dispatch "
        "WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC LIMIT 3")
    for r in cur.fetchall():
        print(r)
