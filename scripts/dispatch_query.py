# -*- coding: utf-8 -*-
"""只读查询网关派发队列最近的 TEST-DRY 演练行（seq/kind/code/状态/回执）。

排查 drill 用的最小取证工具：不改任何数据，目标库为生产机 qmt_gateway/data.db。
"""
import sqlite3
conn = sqlite3.connect(r"C:\qmt\quant-trading-v2\qmt_gateway\data.db")
cur = conn.execute("SELECT seq, kind, code, side, qty, status, order_id, result FROM dispatch WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC LIMIT 5")
for row in cur:
    print(row)
conn.close()
