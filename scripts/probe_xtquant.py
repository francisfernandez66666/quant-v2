# probe_xtquant.py - Plan A validation: fresh-process single-trader xtquant connect (C:\Python312)
# encoding: utf-8
import json
import sys
import time

sys.stdout.reconfigure(line_buffering=True)

CFG = r"C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json"

with open(CFG, "r", encoding="utf-8") as f:
    cfg = json.load(f)

xt_path = cfg.get("xt_path", "")
account = cfg.get("account", "")
print("cfg: xt_path=%s account=%s" % (xt_path, account))

print("=== 1. xtquant trader connect (fresh process, session 77) ===")
ok = False
try:
    from xtquant.xttrader import XtQuantTrader
    from xtquant.xttype import StockAccount

    trader = XtQuantTrader(xt_path, 77)
    trader.start()
    rc = trader.connect()
    print("connect rc=%s" % rc)
    if rc < 0:
        print("client ver:", trader.query_client_ver() if hasattr(trader, "query_client_ver") else "n/a")
        try:
            from xtquant import xtdata
            import time as _t
            xtdata.connect()
            _t.sleep(1)
            print("xtdata connect state:", xtdata.get_state())
        except Exception as e:
            print("xtdata probe failed: %r" % e)
    if rc == 0:
        acc = StockAccount(account)
        infos = trader.query_account_infos()
        target = None
        for x in (infos or []):
            acct = str(getattr(x, "account_id", "") or getattr(x, "acct_id", ""))
            name = getattr(x.__class__, "__str__", lambda s: "")(x)
            print("acct info:", x)
            if account and account in str(x):
                target = x
        if target is not None:
            print("account found: %s" % target)
        else:
            print("no matching account in infos (count=%s)" % (len(infos) if infos else 0))
        pos = trader.query_stock_positions(acc)
        print("positions count=%s" % (len(pos) if pos else 0))
        try:
            from xtquant import xtdata
            tick = xtdata.get_full_tick(["600000.SH"])
            print("xtdata tick sample: %s" % (list(tick.items())[:1]))
        except Exception as e:
            print("xtdata check failed: %s" % e)
    else:
        print("TRADER CONNECT FAILED rc=%s" % rc)
except Exception as e:
    import traceback
    traceback.print_exc()

print("=== RESULT: A-PATH %s ===" % ("OK" if ok else "FAIL"))
try:
    trader.stop()
except Exception:
    pass
