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


#  embedded xtquant adapter (mirrors qmt_gateway.qmt_bridge.XtAdapter) 

def _price_type_const(price_type, code, xtc):
    fix, sh_conv, peer = 11, 43, 14
    try:
        fix = int(getattr(xtconstant, "FIX_PRICE", fix))
        sh_conv = int(getattr(xtconstant, "MARKET_SH_CONVERT_5_LIMIT", sh_conv))
        peer = int(getattr(xtconstant, "MARKET_PEER_PRICE_FIRST", peer))
    except Exception:
        pass
    if str(price_type or "").lower() == "limit":
        return fix
    head = str(code or "").split(".")[0]
    return sh_conv if head.startswith("6") else peer


def _parse_result(res):
    if isinstance(res, (tuple, list)):
        res = list(res)
        ret = int(res[0]) if res else -1
        oid = res[1] if len(res) > 1 else ""
        return ret, str(oid)
    try:
        return int(res.get("ret", res.get("retcode", -1))), str(res.get("order_id", ""))
    except Exception:
        return -1, ""


class _XtOps:
    """embedded xttrader proxied adapter; loads lazily at first non-dry call."""

    def __init__(self, account, dry_run):
        self.account = account
        self.dry_run = dry_run
        self._xt = None
        self._xtc = None
        self._import_tried = False

    def ensure(self):
        if self._import_tried:
            return
        self._import_tried = True
        try:
            from xtquant import xttrader
            from xtquant import xtconstant
            self._xt = xttrader
            self._xtc = xtconstant
            _trace("xtquant embedded import OK")
        except Exception as e:
            _trace("xtquant import FAIL: " + repr(e))
            raise RuntimeError("embedded xtquant unavailable: %s" % e)

    def place(self, req):
        signal_id = str(req.get("signal_id", "") or "")
        if self.dry_run:
            _trace("dry place %s %s %s qty=%s signal=%s" % (
                req.get("side"), req.get("code"), req.get("price_type"),
                req.get("qty"), signal_id))
            return True, "DRYRUN-%d" % int(time.time() * 1000), ""
        self.ensure()
        order_type = "buy" if str(req.get("side", "")) == "" else "sell"
        book = {
            "order_type": order_type,
            "stock_code": str(req.get("code", "")),
            "price_type": _price_type_const(req.get("price_type"), req.get("code"), self._xtc),
            "price": float(req.get("price", 0) or 0),
            "volume": int(req.get("qty", 0) or 0),
            "strategy_name": "qmt_bridge",
            "order_remark": signal_id,
        }
        if self.account:
            book["account"] = self.account
        try:
            res = self._xt.trade.stock(book)
        except Exception as e:
            _trace("place xt error: " + repr(e))
            return False, "", "xttrader.trade.stock error: %s" % e
        ret, oid = _parse_result(res)
        if ret != 0:
            return False, "", "order rejected (ret=%s)" % ret
        return True, oid, ""

    def cancel(self, seq, exchange_order_id, code=""):
        if self.dry_run:
            _trace("dry cancel order_id=%s" % exchange_order_id)
            return True, ""
        self.ensure()
        try:
            int(exchange_order_id)
        except (TypeError, ValueError):
            return False, "invalid exchange order_id: %s" % exchange_order_id
        book = {"order_id": int(exchange_order_id)}
        if code:
            book["stock_code"] = str(code)
        if self.account:
            book["account"] = self.account
        try:
            res = self._xt.trade.cancel_stock(book)
        except Exception as e:
            _trace("cancel xt error: " + repr(e))
            return False, "xttrader.trade.cancel_stock error: %s" % e
        ret, _ = _parse_result(res)
        return (True, "") if ret == 0 else (False, "cancel rejected (ret=%s)" % ret)


XT = _XtAdapter_holder = None


def _xt():
    global _XtAdapter_holder
    if _XtAdapter_holder is None:
        import json
        with open(CFG_PATH, "rb") as f:
            cfg = json.loads(f.read().decode("gbk"))
        _XtAdapter_holder = _XtOps(account=str(cfg.get("account", "")),
                                dry_run=bool(cfg.get("dry_run", False)))
    return _XtAdapter_holder


#  main loops 

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
        ok, order_id, err = _xt().place(cmd)
        _report({"type": "order_result", "seq": seq, "ok": ok,
                 "order_id": order_id, "err": err})
        _trace("order_result seq=%s ok=%s oid=%s" % (seq, ok, order_id))
    elif kind == "cancel":
        ok, err = _xt().cancel(seq, cmd.get("order_id"), cmd.get("code"))
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
    import time as t
    f = open(CFG_PATH, "rb")
    cfg = json.loads(f.read().decode("gbk"))
    f.close()
    _trace("cfg loaded: dry=" + str(cfg.get("dry_run")))
    seen = _load_seen()
    _trace("seen seqs loaded: %d" % len(seen))
    last_cmd_ts = 0.0
    n = 0
    poll = float(cfg.get("poll_sec", 1.0) or 1.0)
    while True:
        n += 1
        # 1) heartbeat (gateway drives queued_connected from this)
        try:
            ev = json.dumps({"type": "heartbeat",
                             "ts": t.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                             "n": n}, ensure_ascii=False)
            fp = open(REPORT_PATH, "a")
            fp.write(ev + "\n")
            fp.close()
        except Exception as e:
            _trace("report fail: " + repr(e))
        # 2) commands (ts-gated + per-seq dedup)
        try:
            csize = os.path.getsize(CMD_PATH)
        except Exception:
            csize = 0
        if csize > 0:
            f = open(CMD_PATH, "rb")
            raw = f.read()
            f.close()
            try:
                data = json.loads(raw.decode("utf-8"))
                cts = float(data.get("ts", 0) or 0)
                if cts > last_cmd_ts:
                    last_cmd_ts = cts
                    for c in (data.get("cmds") or []):
                        handled = _handle_cmd(c, seen)
                        if handled and c.get("seq"):
                            seen.add(str(c.get("seq")))
            except Exception as e:
                _trace("cmd parse fail: " + repr(e))
        t.sleep(float(cfg.get("poll_sec", 1.0) or 1.0))


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
        run_forever()
    except Exception as e:
        _trace("EXCEPTION in init loop: " + repr(e))


def handlebar(ContextInfo):
    return
