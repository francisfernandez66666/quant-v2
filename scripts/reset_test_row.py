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
conn.close()
