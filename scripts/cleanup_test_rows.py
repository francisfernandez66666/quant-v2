# frontend cleanup of the two stale test rows (ASCII source)
import sqlite3
conn = sqlite3.connect(r"C:\qmt\quant-trading-v2\qmt_gateway\data.db")
conn.execute("UPDATE dispatch SET status='done', result='ok-cleaned-by-test-cleanup' WHERE signal_id LIKE 'TEST-DRY%' AND status='inflight'")
conn.commit()
for r in conn.execute("SELECT seq, status, order_id, result FROM dispatch WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC"):
    print(r)
conn.close()
