# reset_test_row.ps1 - settle the stale dry test dispatch row(s) - heredoc-safe (ASCII source)
$sql = "import sqlite3;`n" +
       "c = sqlite3.connect(r'C:\qmt\quant-trading-v2\qmt_gateway\data.db')`n" +
       "c.execute(" + [char]34 + "UPDATE dispatch SET status='done', result='ok-cleaned-by-test-cleanup' WHERE signal_id LIKE 'TEST-DRY%'" + [char]34 + ")`n" +
       "c.commit()`n" +
       "print(list(c.execute('SELECT seq,kind,code,side,qty,status,order_id,result FROM dispatch WHERE signal_id LIKE ' + chr(39) + 'TEST-DRY%' + chr(39) + ' ORDER BY id DESC LIMIT 3')))`n"
& 'C:\Python312\python.exe' -c $sql
