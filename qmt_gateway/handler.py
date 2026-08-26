#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.handler — 回报处理（AUTO_TRADING_PLAN M2）。

从真实通道（xtquant on_stock_order / on_stock_trade / on_disconnected）或 mock 通道收取事件，
写本地 SQLite（store）→ 经 outbox 队列由后台专职线程推给首尔 POST /api/qmt/report
（Bearer 鉴权，超时+有限重试；失败滞留 outbox 无限重试，不丢事件）。

§G9 改造：回报推送不再跑在 xtquant 回调线程里（此前最长阻塞 ~37s 且失败即丢），
回调只入队，发送由独立线程按序完成。
§G5 改造：on_positions 空快照守卫——未连接/数据未同步时的空持仓不再触发对账清空，
连续 ≥2 次空快照且本地确有持仓时才接受"全平"语义。
（English: report handling — callbacks only enqueue; a dedicated sender thread drains the outbox
in order with bounded retries, so slow Seoul never blocks channel callbacks nor loses events.
Empty position snapshots are ignored (with warning) unless seen twice consecutively.）
"""
import logging
import threading
import time
import urllib.request
import urllib.error

log = logging.getLogger("qmt_gateway.handler")

RETRY_DELAYS = [1, 2, 4]  # 秒，指数退避
HEARTBEAT_SEC = 60        # §ROBUST 上行心跳间隔（尽力而为，不入持久化队列）


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
    """回报处理器：写库 + outbox 异步推送首尔。"""

    def __init__(self, store, report_url, report_token, user_id="", max_outbox=5000):
        self.store = store
        self.report_url = report_url
        self.report_token = report_token
        self.user_id = user_id
        self.disconnected = False
        # §G5 连续空快照计数（对账清空守卫）
        self._empty_snaps = 0
        # §ROBUST outbox 改为 store 持久化队列（崩溃/重启续发，不丢回报）；
        # _pending 仅作 sender 唤醒信号。max_outbox 转义为落库行数上限。
        self._outbox_cv = threading.Condition()
        self._pending = 0
        self._stop = threading.Event()
        self._sender_thread = None
        self._heartbeat_thread = None
        self._max_outbox = max_outbox

    def start_sender(self):
        if self._sender_thread is not None:
            return
        self._sender_thread = threading.Thread(
            target=self._send_loop, name="report-sender", daemon=True)
        self._sender_thread.start()
        # §ROBUST 上行心跳：无交易时段也周期证明回程连通（首尔侧刷新 last_report_at）
        self._heartbeat_thread = threading.Thread(
            target=self._heartbeat_loop, name="report-heartbeat", daemon=True)
        self._heartbeat_thread.start()

    def stop_sender(self):
        self._stop.set()
        with self._outbox_cv:
            self._outbox_cv.notify_all()

    def _send_loop(self):
        """持久化 outbox 消费循环：按序取最旧一条 → 发送 → 成功出队；失败指数退避无限重试。
        进程重启后自动从库里续发上次未完成的回报（配合首尔幂等，重发安全）。"""
        backoff = 2
        while not self._stop.is_set():
            item = self.store.outbox_oldest()
            if item is None:
                with self._outbox_cv:
                    self._pending = 0
                    while not self._stop.is_set() and self._pending == 0 \
                            and self.store.outbox_count() == 0:
                        self._outbox_cv.wait(timeout=1)
                continue
            oid, payload = item
            if payload is None:  # 坏行（不可解析）：丢弃防卡队
                log.error("[handler] outbox row %s unparsable — dropped", oid)
                self.store.outbox_delete(oid)
                continue
            if post_report(self.report_url, self.report_token, payload):
                self.store.outbox_delete(oid)
                backoff = 2
            else:
                # 滞留队首无限重试：交易回报不允许静默丢失
                time.sleep(min(backoff, 30))
                backoff *= 2

    def _heartbeat_loop(self):
        """上行心跳：每 60s 尽力而为发一条 type=heartbeat（不入持久化队列，
        失败只告警——它承载的是「回程连通性」信号而非交易数据）。"""
        while not self._stop.is_set():
            if self._stop.wait(HEARTBEAT_SEC):
                return
            if not self.report_url:
                continue
            ok = post_report(self.report_url, self.report_token,
                             {"type": "heartbeat", "user_id": self.user_id}, retries=1)
            if not ok:
                log.warning("[handler] heartbeat push failed (uplink degraded)")

    # ── xtquant 回调 / mock 直调 ──
    def on_stock_order(self, order):
        """委托回报（xtquant order 对象）。at/created_at 双字段兼容首尔契约。"""
        ts = time.strftime("%Y-%m-%dT%H:%M:%S+08:00")
        self.on_order({
            "order_id": str(getattr(order, "order_id", "")),
            "signal_id": getattr(order, "remark", "") or "",
            "code": getattr(order, "stock_code", ""),
            "side": "买入" if getattr(order, "order_type", 0) == 1101 else "卖出",
            "status": self._status(getattr(order, "order_status", "")),
            "price": float(getattr(order, "price", 0) or 0),
            "qty": int(getattr(order, "order_volume", 0) or 0),
            "created_at": ts,
            "at": ts,
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
        pos, is_dup = self.store.apply_fill(ev)
        if is_dup:
            log.warning("[handler] duplicate trade replay ignored: %s %s %s@%s x%s",
                        ev.get("order_id"), ev.get("side"), ev.get("code"),
                        ev.get("price"), ev.get("qty"))
            return
        self._push({"type": "trade", **ev})

    def on_positions(self, positions):
        """全量对账 + 推送。§G5：空快照守卫——只有连续两次空快照且本地有持仓才接受清空。"""
        if not positions:
            held = len(self.store.list_positions())
            self._empty_snaps += 1
            if held == 0:
                return  # 本来就无持仓，无事可做
            if self._empty_snaps < 2:
                log.warning("[handler] empty positions snapshot #%d — reconcile skipped "
                            "(防通道异常清空账本)", self._empty_snaps)
                return
            log.warning("[handler] two consecutive empty snapshots — accepting full clear")
        else:
            self._empty_snaps = 0
        n = self.store.reconcile_positions(positions)
        log.info("[handler] reconciled %d positions", n)
        self._push({"type": "positions", "positions": list(positions)})

    def push_disconnect(self):
        self.on_disconnected()

    def _push(self, payload):
        if not self.report_url:
            return
        payload["user_id"] = self.user_id
        # §ROBUST 先落库后发送：崩溃/重启不丢回报；超上限删最旧（极端保护）
        self.store.outbox_enqueue(payload)
        dropped = self.store.outbox_trim(self._max_outbox)
        if dropped:
            log.error("[handler] durable outbox overflow, dropped %d oldest events", dropped)
        with self._outbox_cv:
            self._pending += 1
            self._outbox_cv.notify_all()


def periodic_reconcile(handler, broker, interval_sec=60, stop=None):
    """周期全量对账：broker.query_positions() → handler.on_positions()。stop 事件可退出。"""
    while not stop or not stop.is_set():
        time.sleep(interval_sec)
        try:
            if broker.is_connected():
                handler.on_positions(broker.query_positions())
        except Exception as e:  # noqa: BLE001
            log.warning("[handler] periodic reconcile failed: %s", e)
