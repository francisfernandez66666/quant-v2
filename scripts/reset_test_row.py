# -*- coding: utf-8 -*-
"""把网关派发队列的 TEST-DRY 演练行统一置 done（比 cleanup_test_rows 更宽：不限 inflight）。

用于演练后残留行的强制清账；随后回列最近 3 行核对。幂等，目标库为生产机 qmt_gateway/data.db。
"""
# reset_test_row.py - settle stale dry-test dispatch rows (ASCII source)
import sqlite3
conn = sqlite3.connect(r"C:\qmt\quant-trading-v2\qmt_gateway\data.db")
cur = conn.execute(
    "UPDATE dispatch SET status='done', result='ok-cleaned-by-test-cleanup' "
    "WHERE signal_id LIKE 'TEST-DRY%'" )
conn.commit()
cur = conn.execute(
    "SELECT seq, kind, code, side, qty, status, order_id, result FROM dispatch "
    "WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC LIMIT 3" )
for r in cur.fetchall():
    print(r)
