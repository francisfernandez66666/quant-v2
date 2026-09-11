#encoding:gbk
#!/usr/bin/env python3
# -*- coding: ascii -*-
"""qmt_bridge_strategy v9 - file-transport bridge with command execution.

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
        f = open(TRACE_PATH, "a")
        f.write(_t.strftime("%Y-%m-%d %H:%M:%S ") + str(msg) + "\n")
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
        fp = open(REPORT_PATH, "a")
        fp.write(json.dumps(payload, ensure_ascii=False) + "\n")
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
        return xtconstant
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
        _trace("embed trade fns: %s" % ",".join(sorted(names)))
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
        _trace("embed probe: API present, ACCOUNT empty -- keep builtin, retry each cycle")
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
            mv = float(getattr(p, "m_dBalance", 0) or getattr(p, "market_value", 0) or 0)
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
            if ot == self.op_buy:
                side = BUY
            elif ot == self.op_sell:
                side = SELL
            else:
                side_raw = str(getattr(t, "direction", "") or getattr(t, "bs_type", "") or "")
                up = side_raw.upper()
                side = BUY if ("BUY" in up or "\u4e70" in side_raw) else SELL
            amount = float(getattr(t, "m_dTradeAmount", 0) or getattr(t, "amount", 0) or price * qty)
            out.append({
                "order_id": remark or str(getattr(t, "m_strOrderSysID", "") or getattr(t, "order_id", "") or ""),
                "code": code, "side": side, "price": price, "qty": qty, "amount": amount,
                "trade_id": str(getattr(t, "m_strTradeID", "") or getattr(t, "traded_id", "") or
                                getattr(t, "trade_no", "") or getattr(t, "id", "") or ""),
                "traded_at": str(getattr(t, "m_strTradeTime", "") or getattr(t, "traded_time", "") or
                                 getattr(t, "time", "") or ""),
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
        ptype = self.pr_limit if str(req.get("price_type", "")).lower() == "limit" else self.pr_market
        price = float(req.get("price", 0) or 0)
        qty = int(req.get("qty", 0) or 0)
        # official sandbox signature: passorder(opType, orderType, accountid, orderCode,
        # prType, modelprice, volume[, ContextInfo]) -- 7 or 8 args by build
        args7 = (op_type, self.order_type, self._embed_acct(), c6, ptype, price, qty)
        try:
            if self._ctx is not None:
                try:
                    po(*(args7 + (self._ctx,)))
                except TypeError:
                    po(*args7)
            else:
                po(*args7)
            self._last_place = (head, op_type, price, qty, time.time())
            return True, "", ""
        except Exception as e:
            _trace("embed passorder error: " + repr(e))
            return False, "", "passorder error: %s" % e

    def embed_cancel(self, exchange_order_id, code=""):
        ca = self._builtin("cancel")
        if ca:
            try:
                ca(int(str(exchange_order_id)), self._embed_acct())
                return True, ""
            except TypeError:
                try:
                    ca(self._embed_acct(), int(str(exchange_order_id)))
                    return True, ""
                except Exception as e:
                    _trace("embed cancel(2) error: " + repr(e))
            except Exception as e:
                _trace("embed cancel error: " + repr(e))
                return False, "cancel error: %s" % e
        return False, "builtin cancel unavailable"

    def embed_resolve(self, req, timeout_sec=8.0):
        """poll ORDER for the just-placed order -> m_strOrderSysID. No userOrderId in
        this build -> match by fingerprint (code/op/price/qty), first unclaimed row."""
        code6 = str((req.get("code") or "").split(".")[0])
        _head, _op, _price, _qty, t0 = self._last_place or (code6, 0, 0.0, 0, time.time() - 5)
        deadline = time.time() + timeout_sec
        while time.time() < deadline:
            for o in self.embed_orders():
                c = str(getattr(o, "m_strInstrumentID", "") or getattr(o, "code", "") or "")
                if c and c != code6:
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
        if self.dry_run or not signal_id:
            return pending_ref
        if self.embed_usable():
            oid = self.embed_resolve(signal_id)
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
    if kind == "order":
        try:
            ok, order_id, err = _xt().place(cmd)
        except Exception as e:
            _trace("place error: " + repr(e))
            ok, order_id, err = False, "", "place error: %s" % e
        if ok:
            # poll exchange order id so gateway can map seq->exchange id (for cancels)
            order_id = _xt().resolve_order_id(str(cmd.get("signal_id", "")), order_id)
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
    else:
        # unknown kind: negative-ack so gateway can settle it rather than hang inflight
        _report({"type": "order_result", "seq": seq, "ok": False,
                 "order_id": "", "err": "unknown kind: %s" % kind})
    _record_seen(seq)
    return True


def run_forever():
    import json
    cfg = _read_cfg()
    _trace("cfg loaded: dry=" + str(cfg.get("dry_run")))
    try:
        _xt().probe_embed()
    except Exception as e:
        _trace("probe error: " + repr(e))
    seen = _load_seen()
    _trace("seen seqs loaded: %d" % len(seen))
    last_cmd_ts = 0.0
    n = 0
    last_pos = 0.0
    last_hb = 0.0
    pos_sec = float(cfg.get("positions_sec", 30.0) or 30.0)
    hb_sec = float(cfg.get("heartbeat_sec", 5.0) or 5.0)
    poll = float(cfg.get("poll_sec", 1.0) or 1.0)
    adapter = _xt()
    while True:
        n += 1
        now = time.time()
        # 1) heartbeat (gateway drives queued_connected from this)
        if now - last_hb >= hb_sec:
            last_hb = now
            try:
                ev = json.dumps({"type": "heartbeat",
                                 "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                                 "n": n}, ensure_ascii=False)
                fp = open(REPORT_PATH, "a")
                fp.write(ev + "\n")
                fp.close()
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
                if cts > last_cmd_ts:
                    last_cmd_ts = cts
                    for c in (data.get("cmds") or []):
                        handled = _handle_cmd(c, seen)
                        if handled and c.get("seq"):
                            seen.add(str(c.get("seq")))
            except Exception as e:
                _trace("cmd parse fail: " + repr(e))
        # 3) positions / asset / trades periodic snapshots -- the core channel that
        #    refills the engine real-book (positions reconcile) and account cash display.
        if now - last_pos >= pos_sec:
            last_pos = now
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
        time.sleep(poll)


def run_forever_safe():
    try:
        run_forever()
    except Exception as e:
        _trace("EXCEPTION in run_forever: " + repr(e))


def init(ContextInfo):
    """Blocking init: loop forever HERE (sandbox daemon threads were being reaped
    right after init returned; blocking guarantees the loop's lifetime)."""
    _trace("init called (blocking cmdline loop)")
    try:
        try:
            _xt()._ctx = ContextInfo
        except Exception as e:
            _trace("ctx inject fail: " + repr(e))
        run_forever()
    except Exception as e:
        _trace("EXCEPTION in init loop: " + repr(e))


def handlebar(ContextInfo):
    return
