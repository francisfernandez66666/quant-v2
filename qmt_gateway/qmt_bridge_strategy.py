#encoding:gbk
#!/usr/bin/env python3
# -*- coding: ascii -*-
"""qmt_bridge_strategy v9 - file-transport bridge with command execution.

[Comment policy] This is the ONLY deliberately Chinese-free source file in the repo --
by design, not debt: the QMT model editor / trading sandbox loads it under GBK (taboo 3),
and non-ASCII bytes in comments/literals get shredded there (BUY/SELL are \\u escapes for
the same reason). Its protocol counterpart qmt_gateway/gateway.py carries the full
Chinese commentary for both sides of this bridge.

Transport: local files in BRIDGE_DIR (sandbox TCP is unavailable - no _socket pyd).
  bridge_report.jsonl  (bridge -> gateway, appended lines): heartbeat / order_result
    / cancel_result / trade / positions / account  (same JSON schema as the old
    POST /dispatch/result contract; gateway sidecar applies them verbatim).
  bridge_cmd.json      (gateway -> bridge, atomic rewrite): {"ts":..., "cmds":[...]}
    rows from the gateway dispatch queue; each is executed via the embedded
    xttrader API (dry_run -> ack-only).

Persistence / dedupe: processed seqs are recorded into bridge_seen.jsonl and
reloaded on restart so a strategy restart cannot double-execute a command.
"""
import os
import sys
import time

BRIDGE_DIR = r"C:\qmt\quant-trading-v2\qmt_gateway"
TRACE_PATH = BRIDGE_DIR + "\\bridge_boot.log"
REPORT_PATH = BRIDGE_DIR + "\\bridge_report.jsonl"
REPORT_MAX = 5 * 1024 * 1024
CFG_PATH = BRIDGE_DIR + "\\config.bridge.json"
CMD_PATH = BRIDGE_DIR + "\\bridge_cmd.json"
SEEN_PATH = BRIDGE_DIR + "\\bridge_seen.jsonl"
HEARTBEAT_SEC = 5
CMD_POLL_SEC = 2


def _trace(msg):
    try:
        import time as _t
        line = _t.strftime("%Y-%m-%d %H:%M:%S ") + str(msg) + "\n"
        # FIX 2026-09-14: sandbox default text encoding is GBK; the gateway sidecar
        # reads this file back as UTF-8. Always write explicit UTF-8 bytes.
        f = open(TRACE_PATH, "ab")
        f.write(line.encode("utf-8", errors="replace"))
        f.close()
    except Exception:
        pass


def _report(payload):
    """append one JSONL event; truncate the report file when it exceeds REPORT_MAX."""
    import json
    import time as t
    try:
        try:
            sz = os.path.getsize(REPORT_PATH)
        except Exception:
            sz = 0
        if sz > REPORT_MAX:  # rotate: rewrite from scratch (gateway sidecar rewinds)
            try:
                os.remove(REPORT_PATH)
            except Exception:
                pass
        fp = open(REPORT_PATH, "ab")
        # FIX 2026-09-14 GBK incident: Chinese names/positions written by the sandbox
        # defaulted to GBK while the gateway sidecar decodes UTF-8 -> mojibake in the
        # engine ledger. ensure_ascii=True escapes every non-ASCII char, encoding-proof.
        fp.write((json.dumps(payload, ensure_ascii=True) + "\n").encode("ascii", errors="replace"))
        fp.close()
        return True
    except Exception as e:
        _trace("report fail: " + repr(e))
        return False


#  embedded xtquant adapter -- canonical XtQuantTrader instance API (mirrors
#  qmt_gateway/broker.py::XtBroker, the production-verified call surface:
#  XtQuantTrader(path, session) -> start/connect -> query_stock_* -> order_stock).
#  2026-09-11 prod incident: the old code called a non-existent xttrader.trade.*
#  facade (AttributeError at first real-mode call) and the strategy loop never
#  pushed positions/asset snapshots, so the engine real-book stayed empty.
#  This rewrite: real adapter + periodic positions/asset/trades reporting on the
#  REPORT_PATH file-bridge protocol (same schema as the HTTP bridge contract).

def _xtc_safe():
    """module-level xtconstant access with import-fallback (no env -> zeros)."""
    try:
        from xtquant import xtconstant as xtc
        return xtc
    except Exception:
        return None


def _price_type_const(price_type, code, xtc=None):
    fix, sh_conv, peer = 11, 43, 14
    try:
        if xtc is not None:
            fix = int(getattr(xtc, "FIX_PRICE", fix))
            sh_conv = int(getattr(xtc, "MARKET_SH_CONVERT_5_LIMIT", sh_conv))
            peer = int(getattr(xtc, "MARKET_PEER_PRICE_FIRST", peer))
    except Exception:
        pass
    if str(price_type or "").lower() == "limit":
        return fix
    head = str(code or "").split(".")[0]
    return sh_conv if head.startswith("6") else peer


def _expect_suffix(code):
    """Exchange-suffix pre-check (6->SH / 0,3->SZ / 4,8->BJ), mirrors broker.py."""
    head = str(code or "").split(".")[0]
    if head.startswith("6"):
        return "SH"
    if head[:1] in ("0", "3"):
        return "SZ"
    if head[:1] in ("4", "8"):
        return "BJ"
    return ""


BUY_CONST_FALLBACK, SELL_CONST_FALLBACK = 23, 24
# FIX 2026-09-14 drill-3: BUY/SELL were referenced but never defined since the
# dry-run port -- the first REAL order died with NameError (sandbox has no
# xtquant, place() only hits the name on the live path). Contract values match
# broker.py Chinese side ("buy"/"sell" in utf-8), same as orders/fills ledger.
# Written as unicode escapes to keep the file pure ASCII (sandbox taboo 3).
BUY, SELL = "\u4e70\u5165", "\u5356\u51fa"


class _XtOps:
    """canonical embedded adapter (XtQuantTrader instance), lazy at first non-dry call.
    On start/connect failure the trader MUST be stop()ed to avoid leaking one set of
    shared-memory writers per reconnect cycle (WaitingFreeWriter limit), same as broker.py."""

    def __init__(self, account, dry_run, xt_path, session_id, embed_account_id="stock",
                 po_params=None):
        self.account = account
        self.dry_run = dry_run
        self.xt_path = xt_path
        self.session_id = int(session_id or 2)
        self.embed_account_id = embed_account_id or "stock"
        self._import_tried = False
        self._trader = None
        self._acc = None
        self._xtc = _xtc_safe()
        self._embed_ok = None  # None=not probed (2026-09-11: sandbox builtin API first)
        self._acct_type_ok = None  # FIX 2026-09-14: set by probe_embed on first success;
                                   # _gtdd read it unconditionally -> AttributeError on
                                   # every order/position/trade query when it was missing.
        self._ctx = None       # QMT ContextInfo injected by init()
        # passorder slots differ across broker builds -> fully configurable,
        # defaults follow the classic DGZQ QMT convention.
        pp = po_params or {}
        self.op_buy = int(pp.get("op_buy", 23))
        self.op_sell = int(pp.get("op_sell", 24))
        self.order_type = int(pp.get("order_type", 1101))
        self.pr_limit = int(pp.get("pr_limit", 11))
        self.pr_market = int(pp.get("pr_market", 5))

    # ---- QMT model-sandbox BUILTIN API (primary since 2026-09-11) ------------------
    # XtQuantTrader is the miniQMT/standalone-trading EXTERNAL interface. The strategy
    # itself runs INSIDE the QMT client process, so connecting back to itself via
    # XtQuantTrader fails (connect() != 0) -- and miniQMT is being retired by the
    # broker anyway. The embedded model-trading API operates the account directly:
    #   get_trade_detail_data(acct_id, "stock", "account"/"position"/"order"/"trade")
    #   passorder(opType, priceType, acct, code, 5, price, volume, strategy, 0, "", userOrderId)
    #   cancel(order_id, acct)
    # XtQuantTrader stays as the legacy miniQMT fallback only.
    #
    # ---- QMT model-trading SANDBOX builtin API (primary since 2026-09-11) ----------
    # Verified against the live client's own mpython/pythonbalance.py and the official
    # demos (get_trade_detail_data(accountid, datatype, "POSITION")):
    #   get_trade_detail_data(accountid, accounttype, DATATYPE[, strategyname])
    #       DATATYPE UPPERCASE: ACCOUNT / POSITION / ORDER / DEAL
    #       objects expose m_str*/m_n*/m_d* fields (m_strInstrumentID, m_nVolume, ...)
    #   passorder(opType, orderType, accountid, orderCode, prType, modelprice, volume[, ContextInfo])
    #       opType 23=buy / 24=sell (DG build); NO userOrderId slot -> fingerprint resolve
    #   ContextInfo-driven cancel is build-specific; probe logs the real surface.

    def _builtin(self, name):
        g = globals().get(name)
        if callable(g):
            return g
        import builtins
        b = getattr(builtins, name, None)
        return b if callable(b) else None

    def _embed_acct(self):
        """Resolve the funding accountID for sandbox trade queries (NOT literal 'stock')."""
        if getattr(self, "_acct_resolved", None):
            return self._acct_resolved
        self._acct_resolved = None
        ga = self._builtin("get_account")
        if ga:
            for t in ("stock", "STOCK", "future"):
                try:
                    a = ga(t)
                except Exception:
                    continue
                if isinstance(a, (list, tuple)) and a:
                    self._acct_resolved = str(a[0]); break
                if isinstance(a, str) and a:
                    self._acct_resolved = a; break
        if not self._acct_resolved:
            self._acct_resolved = self.account or self.embed_account_id
        _trace("embed acct resolved: %r" % self._acct_resolved)
        return self._acct_resolved

    def probe_embed(self):
        """Capability probe; dumps the sandbox trade-function surface to boot.log so
        per-build signature differences are diagnosable at a glance."""
        names = []
        try:
            import builtins as _b
            for n in dir(_b):
                if any(k in n.lower() for k in ("order", "trade", "cancel", "account")):
                    names.append(n)
            for n in globals():
                if any(k in n.lower() for k in ("order", "trade", "cancel", "account")) and n not in names:
                    names.append(n)
        except Exception:
            pass
        if not getattr(self, "_probe_fns", None):
            self._probe_fns = sorted(names)
        _trace("embed trade fns: %s" % ",".join(self._probe_fns))
        gtdd = self._builtin("get_trade_detail_data")
        po = self._builtin("passorder")
        if not (gtdd and po):
            self._embed_ok = False
            return False
        acct = self._embed_acct()
        for atype in ("STOCK", "stock", "8", "10"):
            try:
                rows = gtdd(acct, atype, "ACCOUNT")
            except Exception:
                rows = None
            if rows:
                attrs = [a for a in dir(rows[0]) if a.startswith("m_")][:18]
                _trace("embed ACCOUNT acct=%s atype=%s rows=%d attrs=%r" % (acct, atype, len(rows), attrs))
                self._acct_type_ok = atype
                self._embed_ok = True
                return True
        # API present but ACCOUNT momentarily empty (session still syncing) -> stay builtin
        self._embed_ok = True
        self._probe_empty = getattr(self, "_probe_empty", 0) + 1
        if self._probe_empty <= 1 or self._probe_empty % 10 == 0:
            _trace("embed probe: API present, ACCOUNT empty -- keep builtin (n=%d)"
                   % self._probe_empty)
        return True

    def embed_usable(self):
        if self._embed_ok is None:
            self.probe_embed()
        return bool(self._embed_ok)

    def _gtdd(self, table):
        """get_trade_detail_data(acct, accounttype, "TABLE"); [] is a valid empty snapshot."""
        gtdd = self._builtin("get_trade_detail_data")
        acct = self._embed_acct()
        for at in (self._acct_type_ok or "STOCK", "STOCK", "stock", "8"):
            try:
                return gtdd(acct, at, table.upper()) or []
            except Exception:
                continue
        return []

    @staticmethod
    def _obj_code(o):
        c = str(getattr(o, "m_strInstrumentID", "") or getattr(o, "code", "") or
                getattr(o, "stock_code", "") or "")
        ex = str(getattr(o, "m_strExchangeID", "") or getattr(o, "sector_name", "") or "")
        if c and "." not in c:
            up = ex.upper()
            sec = "SH" if ("SH" in up or up in ("S",)) else \
                  ("SZ" if ("SZ" in up or up in ("Z",)) else (_expect_suffix(c) or "SH"))
            c = "%s.%s" % (c, sec)
        return c

    def embed_asset(self):
        rows = self._gtdd("ACCOUNT")
        out = None
        for r in rows:
            mv = float(getattr(r, "m_dInstrumentValue", 0) or getattr(r, "market_value", 0) or 0)
            cash = float(getattr(r, "m_dAvailable", 0) or getattr(r, "available", 0) or
                         getattr(r, "cash", 0) or 0)
            frz = float(getattr(r, "m_dFrozenCash", 0) or getattr(r, "frozen_cash", 0) or 0)
            tot = float(getattr(r, "m_dBalance", 0) or getattr(r, "total_asset", 0) or (cash + frz + mv))
            out = {"cash": cash, "frozen_cash": frz, "total_asset": tot, "market_value": mv}
        return out

    def embed_positions(self):
        rows = self._gtdd("POSITION")
        out = []
        for p in rows:
            code = self._obj_code(p)
            qty = int(getattr(p, "m_nVolume", 0) or getattr(p, "volume", 0) or 0)
            if qty <= 0 or not code:
                continue
            open_p = float(getattr(p, "m_dOpenPrice", 0) or getattr(p, "open_price", 0) or 0)
            # FIX 2026-09-14: m_dBalance alone reads 0 on this build -> every snapshot
            # carried amount=0 and the engine header showed total pnl = -total_cost
            # (-113,927 for a ~-1k drawdown). Try the full field family, then estimate
            # qty*last so the ledger always has a usable market value.
            mv = 0.0
            for attr in ("m_dBalance", "m_dMarketValue", "m_dInstrumentValue",
                         "m_dPositionValue", "market_value"):
                try:
                    mv = float(getattr(p, attr, 0) or 0)
                except Exception:
                    mv = 0.0
                if mv > 0:
                    break
            if mv <= 0:
                last = 0.0
                for attr in ("m_dLastPrice", "m_dInstrumentLastPrice", "last_price", "price"):
                    try:
                        last = float(getattr(p, attr, 0) or 0)
                    except Exception:
                        last = 0.0
                    if last > 0:
                        break
                mv = last * qty
            out.append({
                "ts_code": code,
                "name": str(getattr(p, "m_strInstrumentName", "") or getattr(p, "name", "") or ""),
                "qty": qty,
                "can_use_qty": int(getattr(p, "m_nCanUseVolume", 0) or getattr(p, "can_use_volume", 0) or 0),
                "open_price": open_p, "cost_price": open_p, "amount": mv,
                "highest_price": open_p,
                "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            })
        return out

    def embed_orders(self):
        return self._gtdd("ORDER")

    def embed_trades_rows(self):
        rows = self._gtdd("DEAL")
        out = []
        for t in rows:
            code = self._obj_code(t)
            remark = str(getattr(t, "m_strRemark", "") or getattr(t, "order_remark", "") or "")
            price = float(getattr(t, "m_dPrice", 0) or getattr(t, "traded_price", 0) or 0)
            qty = int(getattr(t, "m_nVolume", 0) or getattr(t, "traded_volume", 0) or 0)
            ot = int(getattr(t, "m_nOrderType", 0) or getattr(t, "order_type", 0) or 0)
            # FIX 2026-09-14 drill-3: DEAL rows carry m_nDirection=48/49 (ASCII
            # '0'/'1') while m_nOrderType uses the counter's own codes -- the old
            # mapping fell through to SELL on a real BUY fill. Direction first.
            try:
                drc = int(getattr(t, "m_nDirection", -1))
            except (TypeError, ValueError):
                drc = -1
            if drc in (0, 48):
                side = BUY
            elif drc in (49, 1):
                side = SELL
            elif ot == self.op_buy:
                side = BUY
            elif ot == self.op_sell:
                side = SELL
            else:
                side_raw = str(getattr(t, "direction", "") or getattr(t, "bs_type", "") or "")
                up = side_raw.upper()
                side = BUY if ("BUY" in up or "\u4e70" in side_raw) else SELL
            amount = float(getattr(t, "m_dTradeAmount", 0) or getattr(t, "amount", 0) or price * qty)
            td = str(getattr(t, "m_strTradeDate", "") or getattr(t, "trade_date", "") or "")
            tt = str(getattr(t, "m_strTradeTime", "") or getattr(t, "traded_time", "") or
                     getattr(t, "time", "") or "")
            # FIX 2026-09-14: counter DEAL carries date/time split ("20260914"+"131351");
            # reporting the bare HHMMSS broke the engine trade blotter (no time column).
            if td and tt:
                traded = "%s-%s-%sT%s:%s:%s+08:00" % (td[0:4], td[4:6], td[6:8],
                                                      tt[0:2].zfill(2), tt[2:4].zfill(2),
                                                      tt[4:6].zfill(2))
            elif tt:
                traded = time.strftime("%Y-%m-%dT", time.localtime()) + \
                    tt[0:2].zfill(2) + ":" + tt[2:4].zfill(2) + ":" + tt[4:6].zfill(2) + "+08:00"
            else:
                traded = td or ""
            out.append({
                "order_id": remark or str(getattr(t, "m_strOrderSysID", "") or getattr(t, "order_id", "") or ""),
                "code": code, "side": side, "price": price, "qty": qty, "amount": amount,
                "trade_id": str(getattr(t, "m_strTradeID", "") or getattr(t, "traded_id", "") or
                                getattr(t, "trade_no", "") or getattr(t, "id", "") or ""),
                "traded_at": traded,
                "signal_id": remark,
            })
        return out

    def embed_place(self, req):
        po = self._builtin("passorder")
        code = str(req.get("code", "") or "")
        expect = _expect_suffix(code)
        if not expect:
            return False, "", "unsupported stock code: %s" % code
        suffix = code.split(".")[1].upper() if "." in code else ""
        if suffix and suffix != expect:
            return False, "", "exchange suffix mismatch: %s should be .%s" % (code, expect)
        head = code.split(".")[0]
        c6 = "%s.%s" % (head, expect)
        side = str(req.get("side", "") or "")
        op_type = self.op_buy if side == BUY else self.op_sell
        is_limit = str(req.get("price_type", "") or "").lower() == "limit"
        ptype = self.pr_limit if is_limit else self.pr_market
        price = float(req.get("price", 0) or 0) if is_limit else -1.0
        qty = int(req.get("qty", 0) or 0)
        signal_id = str(req.get("signal_id", "") or "")[:24]
        _trace("embed place side=%s op=%s limit=%s ptype=%s price=%s qty=%s sid=%s" % (
            ascii(side), op_type, is_limit, ptype, price, qty, signal_id))
        if not po:
            return False, "", "builtin passorder unavailable"
        # FIX 2026-09-14 drill-3: the 7-positional call left quickOrder at its
        # default 0 (K-line mode) -> the client skipped every submission with
        # "model not initialized yet" and NOTHING reached the counter. Official signature:
        # passorder(opType, orderType, accountid, orderCode, prType, price,
        #           volume, strategyName, quickOrder, userOrderId, ContextInfo)
        # quickOrder=1 => submit immediately, independent of bar/init. Ladder:
        # ladder keeps older builds (7/8 args) working via TypeError fallback.
        core = (op_type, self.order_type, self._embed_acct(), c6, ptype, price, qty)
        attempts = [
            core + ("qmt_bridge", 2, signal_id, self._ctx),
            core + ("qmt_bridge", 2, signal_id),
            core + ("qmt_bridge", 1, signal_id, self._ctx),
            core + ("qmt_bridge", 1, signal_id),
            core + ("qmt_bridge", 1),
            core + ("qmt_bridge",),
            core,
        ]
        last_err = None
        for args in attempts:
            if len(args) == 11 and self._ctx is None:
                continue
            try:
                po(*args)
            except TypeError as e:
                last_err = e
                continue
            except Exception as e:
                _trace("embed passorder error (n=%d): %s" % (len(args), repr(e)))
                return False, "", "passorder error: %s" % e
            self._last_place = (head, op_type, price, qty, time.time())
            _trace("passorder ok n_args=%d" % len(args))
            return True, "", ""
        _trace("embed passorder all arities failed: %s" % repr(last_err))
        return False, "", "passorder arity fail: %s" % last_err

    def embed_cancel(self, exchange_order_id, code=""):
        # FIX 2026-09-14: (int,str)/(str,int) two-arg ladders all died with
        # C++ signature errors (seq:11 watchdog cancel lost). Official model
        # form is cancel(order_id:str, account_id, account_type, ContextInfo)
        # -- try the documented 4-arg forms first, then legacy 2-arg fallbacks.
        ca = self._builtin("cancel")
        if not ca:
            return False, "builtin cancel unavailable"
        oid = str(exchange_order_id or "").strip()
        if not oid:
            return False, "cancel needs exchange order_id"
        acct = self._embed_acct()
        ctx = getattr(self, "_ctx", None)
        attempts = [
            ("4s", (oid, acct, "STOCK", ctx)),
            ("4l", (oid, acct, "stock", ctx)),
            ("3", (oid, acct, "STOCK")),
            ("2i", (int(oid), acct)),
            ("2s", (oid, acct)),
        ]
        errs = []
        for tag, args in attempts:
            if tag.startswith("4") and ctx is None:
                continue
            if tag == "2i":
                try:
                    args = (int(oid), acct)
                except ValueError:
                    continue
            try:
                ca(*args)
                _trace("embed cancel ok via %s" % tag)
                return True, ""
            except Exception as e:
                errs.append("%s:%s" % (tag, repr(e)[:80]))
        _trace("embed cancel all failed: " + " | ".join(errs))
        return False, "cancel error: %s" % errs[-1] if errs else "no attempt"

    def embed_resolve(self, req, signal_id="", timeout_sec=8.0):
        """poll ORDER for the just-placed order -> m_strOrderSysID. Prefer the remark
        we submit as userOrderId (exact); fall back to fingerprint match (code/op/
        price/qty, first unclaimed row). Defensive: legacy callers passed a bare
        signal_id string here -- normalize instead of crashing (2026-09-14 storm)."""
        sig = ""
        if isinstance(req, dict):
            code6 = str((req.get("code") or "").split(".")[0])
            sig = str(req.get("signal_id", "") or "")
        else:
            sig = str(req or "")
            code6 = ""
        sig = str(signal_id or sig or "")
        _head, _op, _price, _qty, t0 = self._last_place or (code6, 0, 0.0, 0, time.time() - 5)
        deadline = time.time() + timeout_sec
        while time.time() < deadline:
            for o in self.embed_orders():
                remark = str(getattr(o, "m_strRemark", "") or getattr(o, "order_remark", "")
                             or getattr(o, "remark", "") or "")
                c = str(getattr(o, "m_strInstrumentID", "") or getattr(o, "code", "") or "")
                if sig and remark == sig:
                    oid = str(getattr(o, "m_strOrderSysID", "") or getattr(o, "order_id", "") or "")
                    if oid:
                        return oid
                if code6 and c and c != code6:
                    continue
                ot = int(getattr(o, "m_nOrderType", 0) or 0)
                if _op and ot and ot != _op:
                    continue
                pq = float(getattr(o, "m_dOrderPrice", 0) or getattr(o, "m_dPrice", 0) or 0)
                qv = int(getattr(o, "m_nOrderVolume", 0) or getattr(o, "volume", 0) or 0)
                if _price and pq and abs(pq - _price) > 0.001:
                    continue
                if _qty and qv and qv != _qty:
                    continue
                oid = str(getattr(o, "m_strOrderSysID", "") or getattr(o, "order_id", "") or "")
                if oid:
                    return oid
            time.sleep(0.5)
        return ""

    # ---- legacy miniQMT XtQuantTrader fallback (kept; broker may retire miniQMT) ----
    def ensure(self):
        if self._import_tried:
            if self._trader is None:
                raise RuntimeError("embedded xtquant unavailable (previous import failed)")
            return
        self._import_tried = True
        try:
            from xtquant.xttrader import XtQuantTrader
            from xtquant import xtconstant
            from xtquant.xttype import StockAccount
        except Exception as e:
            _trace("xtquant import FAIL: " + repr(e))
            raise RuntimeError("embedded xtquant unavailable: %s" % e)
        trader = XtQuantTrader(self.xt_path, self.session_id)
        try:
            if trader.start() is not None:  # 0 = started
                raise RuntimeError("XtQuantTrader.start() failed")
            if trader.connect() != 0:
                raise RuntimeError("XtQuantTrader.connect() failed")
            acc = StockAccount(self.account)
            time.sleep(0.8)
            if trader.query_stock_asset(acc) is None:
                raise RuntimeError("query_stock_asset() None -- client not logged in / trade unlocked?")
        except Exception:
            try:
                trader.stop()
            except Exception:
                pass
            raise
        self._trader = trader
        self._acc = acc
        self._xtc = xtconstant
        _trace("xtquant connected account=%s session=%s" % (self.account, self.session_id))

    def place(self, req):
        """place one order -> (ok, order_ref, err). order_ref "seq:<n>"; resolve_order_id
        maps it to the exchange order id for gateway-side seq mapping / future cancels."""
        signal_id = str(req.get("signal_id", "") or "")
        if self.dry_run:
            _trace("dry place %s %s %s qty=%s signal=%s" % (
                req.get("side"), req.get("code"), req.get("price_type"),
                req.get("qty"), signal_id))
            return True, "DRYRUN-%d" % int(time.time() * 1000), ""
        if self.embed_usable():
            return self.embed_place(req)
        self.ensure()
        code = str(req.get("code", "") or "")
        expect = _expect_suffix(code)
        if not expect:
            return False, "", "unsupported stock code: %s" % code
        suffix = code.split(".")[1].upper() if "." in code else ""
        if suffix and suffix != expect:
            return False, "", "exchange suffix mismatch: %s should be .%s" % (code, expect)
        if suffix != expect:
            code = "%s.%s" % (code.split(".")[0], expect)
        side = str(req.get("side", "") or "")
        xtc = self._xtc
        ot = int(getattr(xtc, "STOCK_BUY", BUY_CONST_FALLBACK)) if side == BUY else \
            int(getattr(xtc, "STOCK_SELL", SELL_CONST_FALLBACK))
        pt = int(_price_type_const(req.get("price_type"), code, xtc))
        price = float(req.get("price", 0) or 0)
        qty = int(req.get("qty", 0) or 0)
        try:
            seq = self._trader.order_stock(
                self._acc, code, ot, qty, pt, price, "qmt_bridge", signal_id)
        except Exception as e:
            _trace("place xt error: " + repr(e))
            return False, "", "order_stock error: %s" % e
        if seq <= 0:
            _trace("place rejected seq=%s sig=%s" % (seq, signal_id))
            return False, "", ("order_stock failed (seq=%s): %s qty=%s -- counter rejected"
                               " (check trade permission / unlock / session)" % (seq, code, qty))
        return True, "seq:%s" % seq, ""

    def resolve_order_id(self, signal_id, pending_ref, req=None):
        """poll orders by order_remark(signal_id) -> exchange order id (for gateway
        seq->exchange mapping and later cancels). dry_run keeps the dry ref."""
        # FIX 2026-09-14 drill-3: the embed call used to pass signal_id (a string)
        # into embed_resolve(req) -> AttributeError on req.get("code") -> the whole
        # _handle_cmd body after place() died before _report/_record_seen, so the
        # gateway never got a result and the bridge re-PLACED the order every poll
        # (~220 submissions in 4 min -- counter-side storm, caught by quickTrade=0
        # only because none of them reached the exchange). Never trust callers.
        try:
            if self.dry_run or not signal_id:
                return pending_ref
            if self.embed_usable():
                oid = self.embed_resolve(req or {}, signal_id)
                return oid or pending_ref
            for _ in range(3):
                try:
                    orders = self._trader.query_stock_orders(self._acc) or []
                except Exception:
                    return pending_ref
                for o in orders:
                    remark = str(getattr(o, "order_remark", "") or getattr(o, "remark", "") or "")
                    if remark == signal_id:
                        oid = str(getattr(o, "order_id", "") or "")
                        if oid:
                            return oid
                time.sleep(1.0)
        except Exception as e:
            _trace("resolve_order_id error: " + repr(e))
        return pending_ref

    def cancel(self, seq, exchange_order_id, code=""):
        if self.dry_run:
            _trace("dry cancel order_id=%s" % exchange_order_id)
            return True, ""
        if self.embed_usable():
            return self.embed_cancel(exchange_order_id, code)
        self.ensure()
        try:
            n = int(str(exchange_order_id or ""))
        except (TypeError, ValueError):
            return False, "invalid exchange order_id: %s" % exchange_order_id
        try:
            self._trader.cancel_order_stock(self._acc, n)
            return True, ""
        except Exception as e:
            _trace("cancel xt error: " + repr(e))
            return False, "cancel_order_stock error: %s" % e

    def query_positions(self):
        """positions snapshot -> gateway dict list (schema == handler.on_positions)."""
        if self.dry_run:
            return []
        if self.embed_usable():
            try:
                return self.embed_positions()
            except Exception as e:
                _trace("embed positions error: " + repr(e))
        self.ensure()
        try:
            raw = self._trader.query_stock_positions(self._acc) or []
        except Exception as e:
            _trace("query positions error: " + repr(e))
            return []
        out = []
        for p in raw:
            if int(getattr(p, "volume", 0) or 0) <= 0:
                continue
            out.append({
                "ts_code": str(getattr(p, "stock_code", "") or ""),
                "name": str(getattr(p, "stock_name", "") or ""),
                "qty": int(getattr(p, "volume", 0) or 0),
                "can_use_qty": int(getattr(p, "can_use_volume", 0) or 0),
                "open_price": float(getattr(p, "open_price", 0) or 0),
                "cost_price": float(getattr(p, "open_price", 0) or 0),
                "amount": float(getattr(p, "market_value", 0) or 0),
                "highest_price": float(getattr(p, "open_price", 0) or 0),
                "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            })
        return out

    def query_asset(self):
        """account asset snapshot (cash/frozen/total/market_value). dry_run -> None."""
        if self.dry_run:
            return None
        if self.embed_usable():
            try:
                return self.embed_asset()
            except Exception as e:
                _trace("embed asset error: " + repr(e))
        self.ensure()
        try:
            raw = self._trader.query_stock_asset(self._acc)
        except Exception as e:
            _trace("query asset error: " + repr(e))
            return None
        if not raw:
            return None

        def g(k, d=0.0):
            if isinstance(raw, dict):
                return raw.get(k, d)
            return getattr(raw, k, d)

        return {
            "cash": float(g("cash") or 0),
            "frozen_cash": float(g("frozen_cash") or 0),
            "total_asset": float(g("total_asset") or 0),
            "market_value": float(g("market_value") or 0),
        }

    def query_trades(self):
        """trades list (order_remark attribution + side) -> type=trade rows."""
        if self.dry_run:
            return []
        if self.embed_usable():
            try:
                return self.embed_trades_rows()
            except Exception as e:
                _trace("embed trades error: " + repr(e))
        self.ensure()
        try:
            raw = self._trader.query_stock_trades(self._acc) or []
        except Exception as e:
            _trace("query trades error: " + repr(e))
            return []
        xtc = self._xtc
        buy_c = int(getattr(xtc, "STOCK_BUY", BUY_CONST_FALLBACK))
        out = []
        for t in raw:
            ot = int(getattr(t, "order_type", 0) or 0)
            side = BUY if ot == buy_c else SELL
            price = float(getattr(t, "executed_price", getattr(t, "trd_price", 0.0)) or 0.0)
            qty = int(getattr(t, "executed_volume", getattr(t, "trd_volume", getattr(t, "volume", 0))) or 0)
            amount = float(getattr(t, "executed_amount", getattr(t, "trd_amount", price * qty)) or 0)
            remark = str(getattr(t, "order_remark", "") or getattr(t, "remark", "") or "")
            tid = str(getattr(t, "executed_id", getattr(t, "trd_id", getattr(t, "traded_id", ""))) or "")
            out.append({
                "order_id": remark or str(getattr(t, "order_id", "") or ""),
                "code": str(getattr(t, "stock_code", "") or ""),
                "side": side, "price": price, "qty": qty, "amount": amount,
                "trade_id": tid,
                "traded_at": str(getattr(t, "trd_time", getattr(t, "trade_time", "")) or ""),
                "signal_id": remark,
            })
        return out


import time  # noqa: E402  (canonical adapter needs time for connect sleep / order polling)


XT = _XtAdapter_holder = None


def _read_cfg():
    """cfg multi-encoding fallback: utf-8-sig / utf-8 / gbk (ops tooling rewrites vary)."""
    import json
    raw = open(CFG_PATH, "rb").read()
    for enc in ("utf-8-sig", "utf-8", "gbk"):
        try:
            return json.loads(raw.decode(enc))
        except Exception:
            continue
    return {}


def _xt():
    global _XtAdapter_holder
    if _XtAdapter_holder is None:
        cfg = _read_cfg()
        _XtAdapter_holder = _XtOps(account=str(cfg.get("account", "")),
                                   dry_run=bool(cfg.get("dry_run", False)),
                                   xt_path=str(cfg.get("xt_path", "") or ""),
                                   session_id=int(cfg.get("session_id", 2) or 2),
                                   embed_account_id=str(cfg.get("embed_account_id", "stock") or "stock"),
                                   po_params=cfg.get("passorder") or {})
    return _XtAdapter_holder


def _load_cmds():
    import json
    try:
        f = open(CMD_PATH, "rb")
        raw = f.read()
        f.close()
        data = json.loads(raw.decode("utf-8"))
        return data
    except Exception as e:
        _trace("cmd read fail: " + repr(e))
        return {"ts": 0, "cmds": []}


def _record_seen(seq):
    try:
        f = open(SEEN_PATH, "a")
        f.write(str(seq) + "\n")
        f.close()
    except Exception:
        pass


def _load_seen():
    out = set()
    try:
        f = open(SEEN_PATH, "rb")
        for ln in f:
            s = ln.strip()
            if s:
                out.add(s)
        f.close()
    except Exception:
        pass
    return out


def _handle_cmd(cmd, seen):
    import json
    kind = str(cmd.get("kind", ""))
    seq = str(cmd.get("seq", ""))
    if not seq or seq in seen:
        return False
    _trace("cmd kind=%s seq=%s" % (kind, seq))
    # FIX 2026-09-14 drill-3: dedupe is registered BEFORE execution. A crash after
    # the order actually left the bridge must NOT make the bridge re-place it every
    # poll (old order: 221 duplicate submissions in ~4 minutes). Worst case of
    # record-first is a stuck-inflight row -- recoverable by ops; a duplicated
    # real order is not.
    _record_seen(seq)
    seen.add(seq)
    try:
        if kind == "order":
            try:
                ok, order_id, err = _xt().place(cmd)
            except Exception as e:
                _trace("place error: " + repr(e))
                ok, order_id, err = False, "", "place error: %s" % e
            if ok:
                # poll exchange order id so gateway can map seq->exchange id (for cancels)
                try:
                    order_id = _xt().resolve_order_id(str(cmd.get("signal_id", "")),
                                                      order_id, cmd)
                except Exception as e:
                    _trace("resolve error (kept seq ref): " + repr(e))
            _report({"type": "order_result", "seq": seq, "ok": ok,
                     "order_id": order_id, "err": err})
            _trace("order_result seq=%s ok=%s oid=%s" % (seq, ok, order_id))
        elif kind == "cancel":
            try:
                ok, err = _xt().cancel(seq, cmd.get("order_id"), cmd.get("code"))
            except Exception as e:
                _trace("cancel error: " + repr(e))
                ok, err = False, "cancel error: %s" % e
            _report({"type": "cancel_result", "seq": seq, "ok": bool(ok), "err": err})
            _trace("cancel_result seq=%s ok=%s" % (seq, ok))
        elif kind == "diag":
            # ops diagnostic: raw dump of trade-detail tables for the live session
            dump = {}
            adapter = _xt()
            for table in ("ACCOUNT", "POSITION", "ORDER", "DEAL"):
                try:
                    rows = adapter._gtdd(table)
                    e0 = {}
                    if rows:
                        p0 = rows[0]
                        for a in dir(p0):
                            if a.startswith("_"):
                                continue
                            try:
                                v = getattr(p0, a)
                                if callable(v):
                                    continue
                                e0[a] = str(v)[:40]
                            except Exception:
                                pass
                    dump[table] = {"n": len(rows), "first": e0}
                except Exception as e:
                    dump[table] = {"err": repr(e)}
            _report({"type": "diag", "seq": seq, "dump": dump})
            _trace("diag done: " + ",".join("%s=%s" % (k, dump[k].get("n", dump[k].get("err"))) for k in dump))
        else:
            # unknown kind: negative-ack so gateway can settle it rather than hang inflight
            _report({"type": "order_result", "seq": seq, "ok": False,
                     "order_id": "", "err": "unknown kind: %s" % kind})
    except Exception as e:
        _trace("handle_cmd fatal seq=%s: %s" % (seq, repr(e)))
        try:
            _report({"type": "order_result", "seq": seq, "ok": False,
                     "order_id": "", "err": "bridge internal error: %s" % e})
        except Exception:
            pass
    return True


_TICK = {"cfg": None, "seen": None, "last_cmd_ts": 0.0, "n": 0,
         "last_pos": 0.0, "last_hb": 0.0, "pos_sec": 30.0, "hb_sec": 5.0}


def _bridge_tick():
    """FIX 2026-09-14 drill-3 round 3: get_trade_detail_data only returns rows on
    the model MAIN thread (init/handlebar callbacks). A background worker thread
    saw ACCOUNT/POSITION/ORDER/DEAL empty FOREVER (diag dump n=0 x4 while the
    account was logged in and an order had already filled on the counter), while
    the same queries worked from the blocking-init main thread all morning. The
    bridge therefore now runs ENTIRELY inline on the handlebar tick (~3s cadence
    observed live) and the worker thread is retired -- which also kills the
    thread-freeze-across-session-breaks failure mode. English: everything runs on
    QMT's main callback thread; gtdd data is only visible there."""
    st = _TICK
    if st["cfg"] is None:
        cfg = _read_cfg()
        st["cfg"] = cfg
        st["pos_sec"] = float(cfg.get("positions_sec", 30.0) or 30.0)
        st["hb_sec"] = float(cfg.get("heartbeat_sec", 5.0) or 5.0)
        _trace("cfg loaded: dry=" + str(cfg.get("dry_run")))
    if st["seen"] is None:
        st["seen"] = _load_seen()
        _trace("seen seqs loaded: %d" % len(st["seen"]))
    cfg = st["cfg"]
    seen = st["seen"]
    st["n"] += 1
    now = time.time()
    # 1) heartbeat (gateway drives queued_connected from this)
    if now - st["last_hb"] >= st["hb_sec"]:
        st["last_hb"] = now
        try:
            _report({"type": "heartbeat",
                     "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                     "n": st["n"]})
        except Exception as e:
            _trace("report fail: " + repr(e))
    # 2) commands (ts-gated + per-seq dedup; bridge_seen.jsonl reload on restart)
    try:
        csize = os.path.getsize(CMD_PATH)
    except Exception:
        csize = 0
    if csize > 0:
        try:
            data = _load_cmds()
            cts = float(data.get("ts", 0) or 0)
            cmds = data.get("cmds") or []
            if cts > st["last_cmd_ts"]:
                st["last_cmd_ts"] = cts
            else:
                # FIX 2026-09-14 drill-3: gateway clock once jumped ahead (9/11
                # file carried ts=2026-09-23); a cached future ts then silently
                # gate-locked every real order after it. ts is change-detection
                # only -- unknown seqs must still be handled (seen dedupe keeps
                # this idempotent).
                cmds = [c for c in cmds if str(c.get("seq", "")) not in seen]
            for c in cmds:
                handled = _handle_cmd(c, seen)
                if handled and c.get("seq"):
                    seen.add(str(c.get("seq")))
        except Exception as e:
            _trace("cmd parse fail: " + repr(e))
    # 3) positions / asset / trades periodic snapshots -- main-thread gtdd only.
    if now - st["last_pos"] >= st["pos_sec"]:
        st["last_pos"] = now
        adapter = _xt()
        try:
            poss = adapter.query_positions()
            if poss:
                _report({"type": "positions", "positions": poss})
            else:
                _trace("empty positions snapshot (session not synced?)")
        except Exception as e:
            _trace("positions snapshot error: " + repr(e))
        try:
            asset = adapter.query_asset()
            if asset:
                _report({"type": "account", "asset": asset})
        except Exception as e:
            _trace("asset snapshot error: " + repr(e))
        try:
            for trow in adapter.query_trades():
                _report({"type": "trade", **trow})
        except Exception as e:
            _trace("trades snapshot error: " + repr(e))


def init(ContextInfo):
    _trace("init called (handlebar-tick mode)")
    try:
        _xt()._ctx = ContextInfo
    except Exception as e:
        _trace("ctx inject fail: " + repr(e))
    try:
        _xt().probe_embed()
    except Exception as e:
        _trace("probe error: " + repr(e))


def handlebar(ContextInfo):
    try:
        _xt()._ctx = ContextInfo
    except Exception:
        pass
    try:
        _bridge_tick()
    except Exception as e:
        _trace("tick fatal: " + repr(e))
