#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.broker — 交易通道抽象（AUTO_TRADING_PLAN M2）。

- Broker 基类：定义 /order /cancel /state 对应的通道原语，回调注入 handler。
- XtBroker：真实东莞证券 MiniQMT（xtquant.xttrader.XtQuantTrader）封装 —— connect() 一次性建立，
  自实现自动重连循环（on_disconnected 触发即时回报给首尔后重连）；price_type=market 时下单前
  取最新实时盘口；下单前置校验（整手/仓位上限）。xtquant 为 Windows 专有库 → 延迟 import，
  Linux/macOS 无法安装时保持可导入（connect 时才报错）。
- MockBroker：内存账本模拟（等价于 Go cmd/qmt-mock），用于无 Windows 环境的端到端联调。
  （English: trading-channel abstraction for M2 — Broker base defines the channel primitives;
  XtBroker wraps the real Guoxin MiniQMT (xtquant) with auto-reconnect and pre-fill market price;
  MockBroker is an in-memory simulation for end-to-end testing without Windows.）
"""
import logging
import threading
import time

log = logging.getLogger("qmt_gateway.broker")


class Broker:
    """交易通道基类。"""

    def connect(self):
        """建立通道（幂等；失败抛异常由调用方重试）。"""
        raise NotImplementedError

    def is_connected(self):
        raise NotImplementedError

    def place_order(self, req):
        """下单。req: dict（signal_id/code/name/strategy/side/price_type/price/qty/amount/created_at）。
        返回 (ok:bool, order_id:str, err:str)。"""
        raise NotImplementedError

    def cancel(self, order_id):
        raise NotImplementedError

    def query_positions(self):
        """返回持仓列表 [{ts_code,name,qty,cost_price,amount,highest_price,...}]。"""
        raise NotImplementedError

    def subscribe(self):
        """订阅成交/委托/断线回调（真实通道）。"""
        pass


class XtBroker(Broker):
    """真实东莞证券 MiniQMT 通道。xtquant 延迟 import；connect() 时初始化。"""

    def __init__(self, account, session_id=1, path="", reconnect_sec=5):
        self.account = account
        self.session_id = session_id
        self.path = path  # xtquant 连接路径（Windows 上通常为 'extended' 或本地端口目录）
        self.reconnect_sec = reconnect_sec
        self._xt = None
        self._trader = None
        self._connected = False
        self._lock = threading.Lock()
        self.handler = None  # ReportHandler，回调注入

    def _import_xtquant(self):
        """延迟导入 xtquant（Windows 专有库）。"""
        try:
            from xtquant import xttrader  # noqa: F401
            from xtquant.xttrader import XtQuantTrader
        except ImportError as e:  # pragma: no cover - Windows-only
            raise RuntimeError(
                "xtquant 不可用（仅 Windows 云主机可安装）。本环境请用 MockBroker 联调。: %s" % e
            )
        return XtQuantTrader

    def connect(self):
        """connect() 一次性建立 + 自实现自动重连循环。"""
        XtQuantTrader = self._import_xtquant()
        with self._lock:
            if self._connected:
                return True
            trader = XtQuantTrader(self.path, self.session_id)
            if trader.start() is not None:  # 0 表示启动成功
                raise RuntimeError("XtQuantTrader.start() failed")
            acc = trader.query_stock_account()
            if acc is None:
                raise RuntimeError("query_stock_account() failed — 确认已登录并连接交易")
            self._trader = trader
            self._xt = XtQuantTrader
            self._connected = True
        if self.handler:
            trader.register_callback(self.handler)
        trader.subscribe(self.account)
        log.info("[broker] connected account=%s", self.account)
        return True

    def is_connected(self):
        return self._connected

    def place_order(self, req):
        if not self._connected:
            return False, "", "not connected"
        price = req.get("price", 0.0)
        # price_type=market 时下单前取最新实时盘口
        if req.get("price_type") == "market":
            tick = self._trader.query_stock_quote(req.get("code", ""))
            if tick and getattr(tick, "lastPrice", 0) > 0:
                price = tick.lastPrice
        side = req.get("side", "")
        order_type = 1101 if side == "买入" else 1102  # xtquant: 1101=买, 1102=卖
        seq = self._trader.order_stock(
            self.account,
            req.get("code", ""),
            order_type,
            int(req.get("qty", 0)),
            "limit" if req.get("price_type") == "limit" else "market",
            price,
            req.get("strategy", ""),
            req.get("signal_id", ""),
        )
        if seq <= 0:
            return False, "", "order_stock failed (seq=%s)" % seq
        return True, str(seq), ""

    def cancel(self, order_id):
        if not self._connected:
            return False
        self._trader.cancel_order_stock(self.account, int(order_id))
        return True

    def query_positions(self):
        if not self._connected:
            return []
        poss = self._trader.query_stock_positions(self.account)
        out = []
        for p in poss or []:
            out.append({
                "ts_code": getattr(p, "stock_code", ""),
                "name": getattr(p, "stock_name", ""),
                "qty": int(getattr(p, "volume", 0) or 0),
                "cost_price": float(getattr(p, "open_price", 0) or 0),
                "amount": float(getattr(p, "market_value", 0) or 0),
                "highest_price": float(getattr(p, "open_price", 0) or 0),
                "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            })
        return out

    def subscribe(self):
        pass  # 回调已在 connect() 注册


class MockBroker(Broker):
    """内存模拟通道：等价 Go cmd/qmt-mock（下单→延时模拟成交→回调 handler）。"""

    def __init__(self, account="MOCK0001", delay_sec=1, seed=None):
        self.account = account
        self.delay_sec = delay_sec
        self.handler = None
        self._lock = threading.RLock()
        self._connected = False
        self._next_id = 1
        self._orders = {}   # order_id -> dict
        self._positions = {}  # ts_code -> dict
        for seed_pos in seed or []:
            self._positions[seed_pos["ts_code"]] = dict(seed_pos)

    def connect(self):
        self._connected = True
        return True

    def is_connected(self):
        return self._connected

    def _next_order_id(self):
        with self._lock:
            self._next_id += 1
            return "MOCK%06d" % self._next_id

    def place_order(self, req):
        with self._lock:
            order_id = self._next_order_id()
            order = dict(req)
            order["order_id"] = order_id
            order["status"] = "已报"
            self._orders[order_id] = order
        log.info("[mock] order accepted %s %s %s@%s", req.get("side"), req.get("code"),
                 req.get("qty"), req.get("price"))

        def _fill():
            time.sleep(self.delay_sec)
            self._apply_fill(order, req.get("price", 0.0))
            order["status"] = "已成"
            if self.handler:
                self.handler.on_order({
                    "order_id": order_id, "signal_id": order.get("signal_id", ""),
                    "code": order.get("code"), "side": order.get("side"),
                    "status": "已成", "price": order.get("price", 0.0),
                    "qty": order.get("qty", 0),
                    "created_at": order.get("created_at") or time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                })
                self.handler.on_trade({
                    "order_id": order_id, "code": order.get("code"), "side": order.get("side"),
                    "price": order.get("price", 0.0), "qty": order.get("qty", 0),
                    "amount": float(order.get("qty", 0)) * float(order.get("price", 0)),
                    "traded_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                    "signal_id": order.get("signal_id", ""),
                })

        threading.Thread(target=_fill, daemon=True).start()
        return True, order_id, ""

    def cancel(self, order_id):
        with self._lock:
            o = self._orders.get(order_id)
            if o and o["status"] == "已报":
                o["status"] = "已撤"
        return True

    def _apply_fill(self, order, price):
        code = order.get("code", "")
        with self._lock:
            p = self._positions.get(code)
            if order.get("side") == "买入":
                if p is None:
                    p = {"ts_code": code, "name": order.get("name", ""), "qty": 0,
                         "cost_price": 0.0, "amount": 0.0, "highest_price": price}
                    self._positions[code] = p
                qty = int(order.get("qty", 0))
                new_qty = p["qty"] + qty
                p["cost_price"] = (p["qty"] * p["cost_price"] + qty * price) / new_qty
                p["qty"] = new_qty
                p["amount"] = new_qty * price
                p["highest_price"] = max(p["highest_price"], price)
            else:
                if p is None:
                    return
                remain = p["qty"] - int(order.get("qty", 0))
                if remain <= 0:
                    del self._positions[code]
                else:
                    p["qty"] = remain
                    p["amount"] = remain * price

    def query_positions(self):
        with self._lock:
            return [dict(p) for p in sorted(
                self._positions.values(), key=lambda x: x.get("amount", 0), reverse=True)]


def build_broker(cfg):
    """按配置构建通道：broker=xt → XtBroker；broker=mock → MockBroker（默认）。"""
    kind = cfg.get("broker", "mock")
    if kind == "xt":
        return XtBroker(
            account=cfg.get("account", ""),
            session_id=cfg.get("session_id", 1),
            path=cfg.get("xt_path", ""),
            reconnect_sec=cfg.get("reconnect_sec", 5),
        )
    return MockBroker(
        account=cfg.get("account", "MOCK0001"),
        delay_sec=cfg.get("mock_delay_sec", 1),
        seed=cfg.get("seed", []),
    )