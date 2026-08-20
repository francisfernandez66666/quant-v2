#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.handler — 回报处理（AUTO_TRADING_PLAN M2）。

从真实通道（xtquant on_stock_order / on_stock_trade / on_disconnected）或 mock 通道收取事件，
写本地 SQLite（store）→ 即时 POST 首尔 POST /api/qmt/report（Bearer 鉴权，超时+有限重试）。

xtquant 回调签名：on_stock_order(order)、on_stock_trade(trade)、on_disconnected()。
未接 xtquant 时这些方法可被 mock 通道直接调用（鸭子类型）。
（English: report handling for M2 — receives events from the real channel (xtquant callbacks) or the
mock channel, writes the local SQLite book, then POSTs each event to Seoul POST /api/qmt/report
(Bearer auth, timeout + limited retries).）
"""
import logging
import time
import urllib.request
import urllib.error

log = logging.getLogger("qmt_gateway.handler")

RETRY_DELAYS = [1, 2, 4]  # 秒，指数退避


def post_report(base_url, token, payload, retries=3):
    """推送一条回报到首尔 /api/qmt/report。失败按退避重试。返回 bool。"""
    url = base_url.rstrip("/") + "/api/qmt/report"
    data = json_dumps(payload).encode("utf-8")
    last_err = None
    for attempt in range(max(1, retries)):
        req = urllib.request.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                if resp.status != 200:
                    last_err = "HTTP %s" % resp.status
                else:
                    return True
        except (urllib.error.URLError, OSError) as e:
            last_err = str(e)
        if attempt + 1 < max(1, retries):
            time.sleep(RETRY_DELAYS[min(attempt, len(RETRY_DELAYS) - 1)])
    log.warning("[handler] report push failed after %d tries: %s", retries, last_err)
    return False


def json_dumps(o):
    import json
    return json.dumps(o, ensure_ascii=False, default=str)


class ReportHandler:
    """回报处理器：写库 + 推送首尔。"""

    def __init__(self, store, report_url, report_token, user_id=""):
        self.store = store
        self.report_url = report_url
        self.report_token = report_token
        self.user_id = user_id
        self.disconnected = False

    # ── xtquant 回调 / mock 直调 ──
    def on_stock_order(self, order):
        """委托回报（xtquant order 对象）。"""
        self.on_order({
            "order_id": str(getattr(order, "order_id", "")),
            "signal_id": getattr(order, "remark", "") or "",
            "code": getattr(order, "stock_code", ""),
            "side": "买入" if getattr(order, "order_type", 0) == 1101 else "卖出",
            "status": self._status(getattr(order, "order_status", "")),
            "price": float(getattr(order, "price", 0) or 0),
            "qty": int(getattr(order, "order_volume", 0) or 0),
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
        })

    def on_stock_trade(self, trade):
        """成交回报（xtquant trade 对象）。"""
        self.on_trade({
            "order_id": str(getattr(trade, "order_id", "")),
            "code": getattr(trade, "stock_code", ""),
            "side": "买入" if getattr(trade, "order_type", 0) == 1101 else "卖出",
            "price": float(getattr(trade, "traded_price", 0) or 0),
            "qty": int(getattr(trade, "traded_volume", 0) or 0),
            "amount": float(getattr(trade, "traded_amount", 0) or 0),
            "traded_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            "signal_id": getattr(trade, "remark", "") or "",
        })

    def on_disconnected(self):
        """断线：记录并推送首尔（首尔侧据此熔断暂停下单）。"""
        self.disconnected = True
        log.warning("[handler] channel disconnected — reporting to Seoul")
        self._push({"type": "disconnect", "at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00")})

    @staticmethod
    def _status(code):
        """xtquant 委托状态码 → 中文（简化为 已报/已成/已撤/已废）。"""
        m = {48: "未报", 49: "待报", 50: "已报", 51: "已报待撤", 52: "部成待撤", 53: "部撤",
             54: "已撤", 55: "部成", 56: "已成", 57: "废单", 255: "未知"}
        return m.get(int(code or 0), "已报")

    # ── 事件落库 + 推送 ──
    def on_order(self, ev):
        if not ev.get("signal_id"):
            log.info("[handler] order without signal_id, skip: %s", ev)
            return
        self.store.upsert_order(ev)
        self._push({"type": "order", **ev})

    def on_trade(self, ev):
        self.store.apply_fill(ev)
        self._push({"type": "trade", **ev})

    def on_positions(self, positions):
        n = self.store.reconcile_positions(positions)
        log.info("[handler] reconciled %d positions", n)
        self._push({"type": "positions", "positions": positions})

    def push_disconnect(self):
        self.on_disconnected()

    def _push(self, payload):
        if not self.report_url:
            return
        payload["user_id"] = self.user_id
        post_report(self.report_url, self.report_token, payload)


def periodic_reconcile(handler, broker, interval_sec=60, stop=None):
    """周期全量对账：broker.query_positions() → handler.on_positions()。stop 事件可退出。"""
    while not stop or not stop.is_set():
        time.sleep(interval_sec)
        try:
            if broker.is_connected():
                handler.on_positions(broker.query_positions())
        except Exception as e:  # noqa: BLE001
            log.warning("[handler] periodic reconcile failed: %s", e)