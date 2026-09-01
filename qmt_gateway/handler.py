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
from datetime import datetime, time as dtime, timedelta

# 北京时间解析（Python 3.9+ 内置 zoneinfo；降级到 UTC+8 估算）。
try:
    from zoneinfo import ZoneInfo
    _BJ = ZoneInfo("Asia/Shanghai")
except Exception:  # noqa: BLE001
    _BJ = None

log = logging.getLogger("qmt_gateway.handler")

RETRY_DELAYS = [1, 2, 4]  # 秒，指数退避
HEARTBEAT_SEC = 60        # §ROBUST 上行心跳间隔（尽力而为，不入持久化队列）


def _now_beijing():
    """返回当前北京时间（Asia/Shanghai）。zoneinfo 不可用时降级为 UTC+8 估算。"""
    if _BJ is not None:
        return datetime.now(_BJ)
    return datetime.utcnow() + timedelta(hours=8)


def is_active_trading_session():
    """活跃交易窗口判定（与引擎 data.CurrentSession 对齐）：工作日 9:15~15:00。

    非交易时段（盘前/盘后/周末/节假日）返回 False——此时 MiniQMT 被 qmtctl 关闭属预期，
    断连不上报熔断，避免每天收盘刷 high 告警污染信号（见
    docs/MIGRATION_GUANGZHOU_ALLINONE.md §3.4）。节假日简化为工作日即交易日，
    因为非交易时段本就抑制上报，误判影响为零。
    """
    now = _now_beijing()
    if now.weekday() >= 5:  # 周六/周日
        return False
    t = now.time()
    return dtime(9, 15) <= t < dtime(15, 0)


def post_report(base_url, token, payload, retries=3):
    """推送一条回报到首尔 /api/qmt/report。失败按退避重试。返回 bool。"""
    # 拼接首尔上报地址并序列化 payload 为 JSON 字节流
    url = base_url.rstrip("/") + "/api/qmt/report"
    data = json_dumps(payload).encode("utf-8")
    last_err = None
    for attempt in range(max(1, retries)):
        # 构造 POST 请求，带 JSON 头与 Bearer 鉴权
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
    """将对象序列化为 JSON 字符串：ensure_ascii=False 保留中文，非标准类型走 str 兜底。"""
    import json
    return json.dumps(o, ensure_ascii=False, default=str)


class ReportHandler:
    """回报处理器：写库 + outbox 异步推送首尔。"""

    def __init__(self, store, report_url, report_token, user_id="", max_outbox=5000):
        """构造回报处理器。

        :param store: 本地 SQLite 账本（Store 实例）。
        :param report_url: 首尔回报接收地址（引擎 /api/qmt/report）。
        :param report_token: 回报鉴权 token（与首尔侧配置一致）。
        :param user_id: 多账号归属标识（§P1-9），回报/落库统一携带。
        :param max_outbox: outbox 落库行数上限，超限裁剪最旧。
        English: builds the report handler — persists events to the local store and
        pushes them to the decision-side /api/qmt/report via a durable outbox.
        """
        self.store = store
        self.report_url = report_url
        self.report_token = report_token
        self.user_id = user_id
        # 断线标记：供首尔侧感知通道状态
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
        """启动后台回报发送线程 + 上行心跳线程（幂等，重复调用直接返回）。

        发送线程消费持久化 outbox；心跳线程在无交易时段也周期上报，
        用于刷新首尔侧 last_report_at 证明回程连通。
        English: starts the report-sender thread and the heartbeat thread (idempotent).
        """
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
        """停止回报发送线程：置停止信号并唤醒发送协程使其退出。"""
        self._stop.set()
        with self._outbox_cv:
            self._outbox_cv.notify_all()

    def _send_loop(self):
        """持久化 outbox 消费循环：按序取最旧一条 → 发送 → 成功出队；失败指数退避无限重试。
        进程重启后自动从库里续发上次未完成的回报（配合首尔幂等，重发安全）。"""
        # backoff：失败重试退避基数，每失败一次翻倍（封顶 30s）
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
        """委托回报（xtquant order 对象）。at/created_at 双字段兼容首尔契约。

        §FIX-0921 拒因透传（2026-09-01 实录：沪市对手方最优价被柜台废单，XtMiniQmt 日志才见
        prctype=84/stat 57——xtquant 回调对象携带 status_msg/order_status_msg 被此处丢弃，
        引擎侧永远看不到柜台废单原因）。尽力提取原因字段透传给引擎（字段名跨构建不定，
        逐一探测，缺失为空串）。
        """
        reason = ""
        for attr in ("order_status_msg", "status_msg", "strategy_name", "error_info", "msg"):
            v = getattr(order, attr, None)
            if v:
                reason = str(v)
                break
        ts = time.strftime("%Y-%m-%dT%H:%M:%S+08:00")
        self.on_order({
            "order_id": str(getattr(order, "order_id", "")),
            "signal_id": self._signal_of(order),
            "code": getattr(order, "stock_code", ""),
            "side": self._side_of(order),
            "status": self._status(getattr(order, "order_status", "")),
            "price": float(getattr(order, "price", 0) or 0),
            "qty": int(getattr(order, "order_volume", 0) or 0),
            "reason": reason,
            "created_at": ts,
            "at": ts,
        })

    def on_stock_trade(self, trade):
        """成交回报（xtquant trade 对象）。"""
        self.on_trade({
            "order_id": str(getattr(trade, "order_id", "")),
            "trade_id": str(getattr(trade, "trade_id", "") or getattr(trade, "order_sysid", "") or ""),
            "name": getattr(trade, "stock_name", "") or "",
            "code": getattr(trade, "stock_code", ""),
            "side": self._side_of(trade),
            "price": float(getattr(trade, "traded_price", 0) or 0),
            "qty": int(getattr(trade, "traded_volume", 0) or 0),
            "amount": float(getattr(trade, "traded_amount", 0) or 0),
            "traded_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            "signal_id": self._signal_of(trade),
        })

    def on_disconnected(self):
        """断线：记录并（交易时段）推送首尔（首尔侧据此熔断暂停下单）。

        非交易时段（盘前/盘后/周末/节假日）不上报——此时 MiniQMT 被 qmtctl 关闭属预期，
        上报会每天触发引擎熔断误报（见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §3.4）。
        """
        self.disconnected = True
        if is_active_trading_session():
            log.warning("[handler] channel disconnected during trading session — reporting to Seoul (triggers fuse)")
            self._push({"type": "disconnect", "at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00")})
        else:
            log.info("[handler] channel disconnected off-hours (MiniQMT killed by qmtctl) — silent, no fuse")

    @staticmethod
    def _side_of(obj):
        """§FIX 2026-08-31：买卖方向识别——本机东莞证券 xtconstant 构建 STOCK_BUY=23/STOCK_SELL=24，
        此前硬编码 `order_type == 1101`（主流文档枚举空间）恒不命中，导致一切回报被判"卖出"
        （实盘首两笔买入成交全被记成卖出，账本方向失真）。
        现按多字段多枚举空间探测：order_type(本构建 23/24，兼容主流 1101/1102)
        → offset_type(柜台回推口径 48=买/50=卖，已由实盘成交实证)。
        都不命中时记警告并返回空串。
        English: detect buy/sell across enum spaces (local build 23/24, legacy 1101/1102,
        counter offset 48/50); warn and return "" when nothing matches."""
        for attr, buys, sells in (("order_type", (23, 1101), (24, 1102)),
                                  ("offset_type", (48,), (50,))):
            v = getattr(obj, attr, None)
            if v is None:
                continue
            try:
                v = int(v)
            except (TypeError, ValueError):
                continue
            if v in buys:
                return "买入"
            if v in sells:
                return "卖出"
        log.warning("[handler] side detect failed: order_type=%r offset_type=%r",
                    getattr(obj, "order_type", None), getattr(obj, "offset_type", None))
        return ""

    @staticmethod
    def _signal_of(obj):
        """§FIX 2026-08-31：signal_id 归因——xtquant 回调对象的备注字段名为
        strategy_name/order_remark，此前读 `remark`（不存在）恒为空 → 委托/成交无法归因、
        orders 行被 skip、按战法盈亏统计断链（柜台回推 remark 实际带完整 signal_id，见
        XtMiniQmt 日志 `m_pRemark.m_strMsg`）。
        English: attribute signal_id from strategy_name/order_remark/remark (first non-empty)."""
        for attr in ("strategy_name", "order_remark", "remark"):
            v = str(getattr(obj, attr, "") or "").strip()
            if v:
                return v
        return ""

    @staticmethod
    def _status(code):
        """xtquant 委托状态码 → 中文（简化为 已报/已成/已撤/已废）。"""
        # xtquant 状态码常量映射表：键为数字状态码，值为中文语义
        m = {48: "未报", 49: "待报", 50: "已报", 51: "已报待撤", 52: "部成待撤", 53: "部撤",
             54: "已撤", 55: "部成", 56: "已成", 57: "废单", 255: "未知"}
        return m.get(int(code or 0), "已报")

    # ── 事件落库 + 推送 ──
    def on_order(self, ev):
        """委托回报回调：落库并推送首尔。无 signal_id 的委托忽略（无法归因/幂等）。

        §修复 G3：回调体全程异常保护——落库/推送异常绝不应冒泡到 xtquant 回调线程，
        否则可能打断通道回调、丢失后续回报；异常仅记录并降级跳过本事件。
        English: order-report callback — persists the order and pushes it to the decision
        side; orders without a signal_id are ignored (no attribution/idempotency).
        """
        # §修复 G3（2026-08-29）：回调体全程异常保护——落库/推送异常绝不应冒泡到 xtquant
        # 回调线程（否则可能打断通道回调、丢失后续回报）。异常仅记录并降级跳过本事件。
        try:
            # 无 signal_id 的委托忽略（无法归因/幂等）；否则落库并推送首尔
            if not ev.get("signal_id"):
                log.info("[handler] order without signal_id, skip: %s", ev)
                return
            # §P1-9 落库携带归属账号 ID（多账号隔离）
            ev["user_id"] = self.user_id
            self.store.upsert_order(ev)
            self._push({"type": "order", **ev})
        except Exception:  # noqa: BLE001
            log.exception("[handler] on_order failed, event skipped: %s", ev)

    def on_trade(self, ev):
        """成交回报回调：落库 + 去重 + 推送首尔。重复重放不推送（避免持仓翻倍）。

        §修复 G3：回调异常保护（见 on_order 说明）。§P1-9 落库携带归属账号 ID。
        English: trade/fill callback — persists, de-duplicates and pushes to the decision
        side; replayed duplicates are dropped to avoid doubling positions.
        """
        # §修复 G3（2026-08-29）：回调异常保护（见 on_order 说明）。
        try:
            # 成交先落库并去重；重复重放不推送，避免持仓翻倍
            # §P1-9 落库携带归属账号 ID（多账号隔离）
            ev["user_id"] = self.user_id
            pos, is_dup = self.store.apply_fill(ev)
            if is_dup:
                log.warning("[handler] duplicate trade replay ignored: %s %s %s@%s x%s",
                            ev.get("order_id"), ev.get("side"), ev.get("code"),
                            ev.get("price"), ev.get("qty"))
                return
            self._push({"type": "trade", **ev})
        except Exception:  # noqa: BLE001
            log.exception("[handler] on_trade failed, event skipped: %s", ev)

    def on_positions(self, positions):
        """全量对账 + 推送。§G5：空快照守卫——只有连续两次空快照且本地有持仓才接受清空。"""
        # §修复 G3（2026-08-29）：回调异常保护（见 on_order 说明）。
        try:
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
            # §P1-9 对账持仓逐条补归属账号 ID（多账号隔离）
            for p in positions:
                p["user_id"] = self.user_id
            n = self.store.reconcile_positions(positions)
            log.info("[handler] reconciled %d positions", n)
            self._push({"type": "positions", "positions": list(positions)})
        except Exception:  # noqa: BLE001
            log.exception("[handler] on_positions failed, reconcile skipped")

    def on_account(self, asset):
        """账户资产回报（可用资金/冻结/总值/市值）。asset 为 None 时跳过（查询失败）。"""
        if not asset:
            return
        self._push({"type": "account", "asset": dict(asset)})

    def push_disconnect(self):
        """对外封装断线回调（供外部调用方触发），等价于 on_disconnected。"""
        self.on_disconnected()

    def _push(self, payload):
        """把回报入 outbox 持久化队列并唤醒发送线程（先落库后发送，崩溃/重启不丢回报）。

        未配置 report_url 时直接丢弃（本地联调场景）；超 max_outbox 上限裁剪最旧
        （极端保护）。English: enqueues a report into the durable outbox and wakes the
        sender; dropped locally when no report_url is configured, trimmed when overflowing.
        """
        # 未配置上报地址则直接丢弃（本地联调场景）
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
    """周期全量对账：broker.query_positions() → handler.on_positions()；同时上报账户资产
    （可用资金/冻结/总值/市值）→ handler.on_account()。stop 事件可退出。"""
    while not stop or not stop.is_set():
        time.sleep(interval_sec)
        try:
            if broker.is_connected():
                handler.on_positions(broker.query_positions())
                handler.on_account(broker.query_asset())
        except Exception as e:  # noqa: BLE001
            log.warning("[handler] periodic reconcile failed: %s", e)
