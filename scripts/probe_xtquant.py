# -*- coding: utf-8 -*-
"""xtquant 连通性探针（§MIGRATION_QMT_DUAL_PATH 方案 A 验证工具）。

在全新进程里用 config.xt.json 凭据建 XtQuantTrader 连接并查资产/持仓/委托，
验证"独立进程直连 miniQMT"可行性；stdout 逐行刷便于 ssh 轮询。只读，不下单。
"""
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

print("=== 2. §ENH-5 xtdata L1 tick 单位校准探针（上线前必跑一次）==="
# quote_feed.py / Go qmt_feed 的 volume 字段原样透传、不猜单位（§FIX-1 教训）。
# 本探针打印同一 tick 的 volume/amount 与"amount/lastPrice 推算的均价量"比值，
# 人工比对结论二选一：volume≈股数（比值≈1，Go 侧系数保持 1）
#                     volume≈手数（比值≈100，Go 侧 SetVolumeToShares(100) 固化）。
try:
    from xtquant import xtdata as _xd
    _xd.connect()
    _codes = ["600519.SH", "600000.SH"]
    _tick = _xd.get_full_tick(_codes) or {}
    for _c, _t in _tick.items():
        _lp = float(_t.get("lastPrice") or 0)
        _vol = float(_t.get("volume") or 0)
        _amt = float(_t.get("amount") or 0)
        _ratio = (_amt / _lp / _vol) if (_lp > 0 and _vol > 0) else 0.0
        print("tick %s: lastPrice=%s volume=%s amount=%s time=%s" % (_c, _lp, _vol, _amt, _t.get("time")))
        print("  amount/(price*volume)=%.2f  （≈1→volume单位=股；≈100→单位=手，须配 SetVolumeToShares(100)）" % _ratio)
    if not _tick:
        print("get_full_tick 返回空（客户端未就绪/非交易时段无 tick），请在盘中重跑")
except Exception as _e:
    print("xtdata 单位探针失败（不影响 trader 探针结论）: %r" % _e)

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
