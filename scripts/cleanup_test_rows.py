# -*- coding: utf-8 -*-
"""清理网关派发队列里的历史演练行（§QMT drill 遗留）。

把 TEST-DRY% 测试信号中仍挂 inflight 的行强制置 done（结果标 ok-cleaned-by-test-cleanup），
并回列全部 TEST-DRY 行核对。幂等，可重复执行。目标库为生产机上的 qmt_gateway/data.db。
"""
# frontend cleanup of the two stale test rows (ASCII source)
import sqlite3
conn = sqlite3.connect(r"C:\qmt\quant-trading-v2\qmt_gateway\data.db")
# 演练行收尾：inflight → done（不动其它状态，避免覆盖真实回执）
conn.execute("UPDATE dispatch SET status='done', result='ok-cleaned-by-test-cleanup' WHERE signal_id LIKE 'TEST-DRY%' AND status='inflight'")
conn.commit()
for r in conn.execute("SELECT seq, status, order_id, result FROM dispatch WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC"):
    print(r)
conn.close()
