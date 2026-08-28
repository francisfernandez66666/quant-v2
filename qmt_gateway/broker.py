#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.broker — 交易通道抽象（AUTO_TRADING_PLAN M2）。

- Broker 基类：定义 /order /cancel /state 对应的通道原语，回调注入 handler。
- XtBroker：真实东莞证券 MiniQMT（xtquant.xttrader.XtQuantTrader）封装 —— connect() 一次性建立，
  自实现自动重连循环；§G3 断线经回调适配器复位 _connected（此前断线后永不重连、/state 谎报在线）；
  §G6 price_type 映射 xtconstant 数值常量（此前传字符串疑似全部废单），market=对手方最优价
  （与首尔侧 trading.QMTClient 契约一致，分沪深市场取常量）；
  §G4 order_stock 返回的本地 seq 以 "seq:<n>" 不透明串返回并维护 seq→交易所委托号映射，
  回调回报真实委托号后自动替换（store.upsert_order 占位替换规则），撤单先解析映射。
  xtquant 为 Windows 专有库 → 延迟 import，Linux/macOS 保持可导入（connect 时才报错）。
- MockBroker：内存账本模拟（等价 Go cmd/qmt-mock）。§修复撤单竞态：已撤订单不再被延迟成交线程
  强改成已成/改持仓。
  （English: trading-channel abstraction — XtBroker wraps MiniQMT with disconnect-aware reconnect,
  xtconstant price-type mapping and seq→exchange-id resolution; MockBroker is an in-memory twin.）
"""
import logging
import threading
import time

# 模块级日志器：各 broker 统一记录连接/断线/回调异常
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
        返回 (ok:bool, order_ref:str, err:str)。order_ref 为不透明串（mock 单号或 "seq:<n>"）。"""
        raise NotImplementedError

    def cancel(self, order_id):
        """撤单。返回 (ok:bool, err:str)。order_ref 无法解析为交易所委托号时返回失败原因。"""
        raise NotImplementedError

    def query_positions(self):
        """返回持仓列表 [{ts_code,name,qty,cost_price,amount,highest_price,...}]。可能为空列表
        （未连接/数据未同步——调用方必须按"不可信快照"处理，禁止据此清账）。"""
        raise NotImplementedError

    def query_asset(self):
        """返回账户资产字典（可用资金/冻结资金/总资产/持仓市值），未连接或失败时返回 None。
        （English: returns account asset dict or None. Keys: cash/frozen_cash/total_asset/market_value.）"""
        raise NotImplementedError

    def subscribe(self):
        """订阅成交/委托/断线回调（真实通道）。"""
        pass


class _CallbackAdapter:
    """§G3/G4 回调适配器：断线通知同时复位 broker._connected；委托回报回填 seq→交易所委托号。

    其余回调方法委托给内部 handler（鸭子类型，xtquant 只调它认识的方法）。
    """

    def __init__(self, broker, inner):
        # 记录被包装的 broker（用于断线复位）与真实 handler（用于事件转发）
        self._broker = broker
        self._inner = inner

    def __getattr__(self, name):
        # 未显式定义的方法（如 on_stock_trade）统一转发给内部真实 handler
        return getattr(self._inner, name)

    def on_disconnected(self):
        self._broker.mark_disconnected()
        try:
            self._inner.on_disconnected()
        except Exception:  # noqa: BLE001
            log.exception("[adapter] inner on_disconnected failed")

    def on_stock_order(self, order):
        try:
            self._broker.record_exchange_order_id(order)
        except Exception:  # noqa: BLE001
            log.exception("[adapter] record exchange order_id failed")
        self._inner.on_stock_order(order)


class XtBroker(Broker):
    """真实东莞证券 MiniQMT 通道。xtquant 延迟 import；connect() 时初始化。"""

    def __init__(self, account, session_id=1, path="", reconnect_sec=5):
        self.account = account
        self.session_id = session_id
        self.path = path  # xtquant 连接路径（Windows 上通常为 'extended' 或本地端口目录）
        self.reconnect_sec = reconnect_sec
        # 以下为运行期状态：xt 类/交易对象/连接标志，初始均为空/未连
        self._xt = None
        self._trader = None
        self._connected = False
        # 连接/下单/映射读写共用一把锁，保证多线程安全
        self._lock = threading.Lock()
        self.handler = None  # ReportHandler，回调注入
        # §G4 seq ↔ signal_id ↔ 交易所委托号 映射
        self._seq_signal = {}       # seq(str) -> signal_id
        self._signal_seq = {}       # signal_id -> seq(str)
        self._seq_exchange = {}     # seq(str) -> 交易所委托号(str)

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

    @staticmethod
    def price_type_const(price_type, code):
        """§G6 price_type → xtconstant 数值常量。

        limit → FIX_PRICE(11)；market → 对手方最优价格委托（契约语义=对手价），
        沪市取 MARKET_PEER_PRICE_FIRST_SH(44)、深市取 MARKET_PEER_PRICE_FIRST(14)。
        常量名缺失时用文档数值兜底；有 xtconstant 时以库值为准。
        """
        fix, peer_sh, peer_sz = 11, 44, 14
        try:
            from xtquant import xtconstant  # noqa: PLC0415
            fix = getattr(xtconstant, "FIX_PRICE", fix)
            peer_sh = getattr(xtconstant, "MARKET_PEER_PRICE_FIRST_SH", peer_sh)
            peer_sz = getattr(xtconstant, "MARKET_PEER_PRICE_FIRST", peer_sz)
        except Exception:  # noqa: BLE001 — 无 xtquant 环境（mock/测试）
            pass
        if str(price_type or "").lower() == "limit":
            return int(fix)
        head = str(code or "").split(".")[0]
        return int(peer_sh) if head.startswith("6") else int(peer_sz)

    def connect(self):
        """connect() 一次性建立 + 自实现自动重连循环。"""
        XtQuantTrader = self._import_xtquant()
        from xtquant.xttype import StockAccount
        with self._lock:
            if self._connected:
                return True
            trader = XtQuantTrader(self.path, self.session_id)
            if trader.start() is not None:  # 0 表示启动成功
                raise RuntimeError("XtQuantTrader.start() failed")
            if trader.connect() != 0:
                raise RuntimeError("XtQuantTrader.connect() failed")
            acc = StockAccount(self.account)
            time.sleep(1.0)
            asset = trader.query_stock_asset(acc)
            if asset is None:
                raise RuntimeError("query_stock_asset() returned None — 确认东莞 miniQMT 客户端已登录并连接交易")
            # 资产查询成功即认为登录有效，固化账户对象/交易对象并置为已连接
            self._acc = acc
            self._trader = trader
            self._xt = XtQuantTrader
            self._connected = True
        # §G3 注册适配器而非裸 handler：断线即复位 _connected，重连循环得以触发
        if self.handler:
            trader.register_callback(_CallbackAdapter(self, self.handler))
        trader.subscribe(acc)
        log.info("[broker] connected account=%s", self.account)
        return True

    def mark_disconnected(self):
        """§G3 断线复位：让 is_connected()/connect_loop 立即反映真实通道状态。"""
        with self._lock:
            was = self._connected
            self._connected = False
        if was:
            log.warning("[broker] channel marked disconnected — reconnect loop will re-establish")

    def record_exchange_order_id(self, order):
        """§G4 由回调适配器调用：remark(signal_id) 关联出 seq→交易所委托号映射。"""
        remark = getattr(order, "remark", "") or ""
        oid = str(getattr(order, "order_id", "") or "")
        if not remark or not oid:
            return
        with self._lock:
            seq = self._signal_seq.get(remark)
            if seq:
                prev = self._seq_exchange.get(seq)
                if prev and prev != oid:
                    log.warning("[broker] exchange order_id changed for signal=%s: %s -> %s",
                                remark, prev, oid)
                self._seq_exchange[seq] = oid

    def is_connected(self):
        return self._connected

    def place_order(self, req):
        if not self._connected:
            return False, "", "not connected"
        code = req.get("code", "")
        price = float(req.get("price", 0) or 0)
        side = req.get("side", "")
        order_type = 1101 if side == "买入" else 1102  # xtquant: 1101=买, 1102=卖
        pt_const = self.price_type_const(req.get("price_type"), code)
        signal_id = req.get("signal_id", "")
        with self._lock:
            seq = self._trader.order_stock(
                self._acc,
                code,
                order_type,
                int(req.get("qty", 0)),
                pt_const,
                price,
                req.get("strategy", ""),
                signal_id,
            )
        if seq <= 0:
            return False, "", "order_stock failed (seq=%s)" % seq
        seq_s = str(seq)
        with self._lock:
            # 维护 seq ↔ signal_id 双向映射，供撤单时反查
            self._seq_signal[seq_s] = signal_id
            if signal_id:
                self._signal_seq[signal_id] = seq_s
        # §G4 返回不透明 "seq:<n>" 引用：首尔可凭此撤单（映射解析）；真实委托号由回报回填
        return True, "seq:%s" % seq_s, ""

    def cancel(self, order_id):
        if not self._connected:
            return False, "not connected"
        oid = str(order_id or "")
        # 撤单引用可能是 seq: 占位串，需先解析成真实交易所委托号
        if oid.startswith(("seq:", "SEQ:", "pending:")):
            seq = oid.split(":", 1)[1]
            with self._lock:
                mapped = self._seq_exchange.get(seq)
            if not mapped:
                return False, "交易所委托号尚未回报，暂不可撤（seq=%s）" % seq
            oid = mapped
        try:
            # 交易所委托号为整数；非法串直接失败
            n = int(oid)
        except ValueError:
            return False, "invalid order_id: %r" % order_id
        try:
            self._trader.cancel_order_stock(self._acc, n)
        except Exception as e:  # noqa: BLE001
            return False, str(e)
        return True, ""

    def query_positions(self):
        if not self._connected:
            return []
        poss = self._trader.query_stock_positions(self._acc)
        out = []
        for p in poss or []:
            # 将 xtquant 持仓对象字段映射为网关统一持仓字典
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

    def query_asset(self):
        """查询账户资产（可用资金/冻结/总资产/市值）。xtquant query_asset 返回 dict，
        字段名取 cash/frozen_cash/total_asset/market_value；未连接或异常返回 None。"""
        if not self._connected or self._trader is None:
            return None
        try:
            raw = self._trader.query_asset(self._acc)
            if not raw:
                return None
            # xtquant 可能返回 {..., 'asset': {...}} 或直接平铺字段；统一取可用结构
            a = raw.get("asset", raw) if isinstance(raw, dict) else raw
            return {
                "cash": float(getattr(a, "cash", 0) or 0),
                "frozen_cash": float(getattr(a, "frozen_cash", 0) or 0),
                "total_asset": float(getattr(a, "total_asset", 0) or 0),
                "market_value": float(getattr(a, "market_value", 0) or 0),
            }
        except Exception as e:  # noqa: BLE001
            log.warning("[broker] query_asset failed: %s", e)
            return None


class MockBroker(Broker):
    """内存模拟通道：等价 Go cmd/qmt-mock（下单→延时模拟成交→回调 handler）。"""

    def __init__(self, account="MOCK0001", delay_sec=1, seed=None, account_init=100000.0):
        self.account = account
        self.account_init = account_init
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

    def mark_disconnected(self):
        self._connected = False

    def is_connected(self):
        return self._connected

    def _next_order_id(self):
        # 自增生成 mock 委托号（MOCK 前缀 + 6 位序号），保证唯一
        self._next_id += 1
        return "MOCK%06d" % self._next_id

    def place_order(self, req):
        with self._lock:
            # 生成 mock 委托号并落内存账本，状态先置“已报”
            order_id = self._next_order_id()
            order = dict(req)
            order["order_id"] = order_id
            order["status"] = "已报"
            self._orders[order_id] = order
        log.info("[mock] order accepted %s %s %s@%s", req.get("side"), req.get("code"),
                 req.get("qty"), req.get("price"))

        def _fill():
            time.sleep(self.delay_sec)
            filled_snapshot = None
            with self._lock:
                o = self._orders.get(order_id)
                # §修复撤单竞态：已撤/已删订单不再延迟成交、不改持仓、不回调
                if o is None or o.get("status") != "已报":
                    return
                o["status"] = "已成"
                self._apply_fill_locked(order, req.get("price", 0.0))
                filled_snapshot = dict(o)
            ts = time.strftime("%Y-%m-%dT%H:%M:%S+08:00")
            if self.handler:
                self.handler.on_order({
                    "order_id": order_id, "signal_id": order.get("signal_id", ""),
                    "code": order.get("code"), "side": order.get("side"),
                    "status": "已成", "price": order.get("price", 0.0),
                    "qty": order.get("qty", 0),
                    "created_at": filled_snapshot.get("created_at") or ts,
                    "at": ts,
                })
                self.handler.on_trade({
                    "order_id": order_id, "code": order.get("code"), "side": order.get("side"),
                    "price": order.get("price", 0.0), "qty": order.get("qty", 0),
                    "amount": float(order.get("qty", 0)) * float(order.get("price", 0)),
                    "traded_at": ts,
                    "signal_id": order.get("signal_id", ""),
                })

        threading.Thread(target=_fill, daemon=True).start()
        return True, order_id, ""

    def cancel(self, order_id):
        with self._lock:
            o = self._orders.get(str(order_id))
            if o is None:
                return False, "order not found"
            if o["status"] == "已成":
                return False, "order already filled"
            if o["status"] == "已报":
                o["status"] = "已撤"
                return True, ""
            return False, "order in state %s" % o["status"]

    def _apply_fill_locked(self, order, price):
        """调用方须持 _lock。"""
        code = order.get("code", "")
        p = self._positions.get(code)
        # 买入：加仓或新建，按加权成本更新持仓成本与最高价
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

    def query_asset(self):
        """mock 账户资产：可用资金 = 初始资金 - 持仓市值（模拟），冻结 0、市值=持仓汇总。"""
        with self._lock:
            mv = sum(p.get("amount", 0.0) for p in self._positions.values())
            init = float(self.account_init) if hasattr(self, "account_init") else 100000.0
            return {
                "cash": max(init - mv, 0.0),
                "frozen_cash": 0.0,
                "total_asset": init,
                "market_value": mv,
            }


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
