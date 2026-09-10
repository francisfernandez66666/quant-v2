import sqlite3
conn = sqlite3.connect(r"C:\qmt\quant-trading-v2\qmt_gateway\data.db")
cur = conn.execute("SELECT seq, kind, code, side, qty, status, order_id, result FROM dispatch WHERE signal_id LIKE 'TEST-DRY%' ORDER BY id DESC LIMIT 5")
for row in cur:
    print(row)
conn.close()
