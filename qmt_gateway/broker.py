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
        """返回通道是否已连接。基类未实现，由子类提供真实/模拟状态。"""
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
        """构造回调适配器。

        :param broker: 被包装的通道实例（断线时调用其 mark_disconnected 复位连接态）。
        :param inner: 真实回报处理器（handler.ReportHandler），事件经转发送达。
        English: builds the callback adapter wrapping a broker (for disconnect reset) and
        the inner report handler (for event forwarding).
        """
        # 记录被包装的 broker（用于断线复位）与真实 handler（用于事件转发）
        self._broker = broker
        self._inner = inner

    def __getattr__(self, name):
        """未显式定义的回调方法（如 on_stock_trade）统一转发给内部真实 handler。"""
        # 未显式定义的方法（如 on_stock_trade）统一转发给内部真实 handler
        return getattr(self._inner, name)

    def on_disconnected(self):
        """断线回调：复位通道连接态并把断线事件转达 handler（异常仅记录不阻断）。"""
        self._broker.mark_disconnected()
        try:
            self._inner.on_disconnected()
        except Exception:  # noqa: BLE001
            log.exception("[adapter] inner on_disconnected failed")

    def on_stock_order(self, order):
        """委托回报回调：先回填 seq→交易所委托号映射，再转达 handler。"""
        try:
            self._broker.record_exchange_order_id(order)
        except Exception:  # noqa: BLE001
            log.exception("[adapter] record exchange order_id failed")
        self._inner.on_stock_order(order)

    def on_order_error(self, order_error):
        """§UAT 2026-08-31：委托失败主推（XtOrderError）。同步 order_stock 返回 -1 时，
        柜台/客户端的真实拒绝原因（error_id + error_msg）经此回调送达——此前未实现，
        原因被静默丢弃，排障只剩裸 seq=-1。order_remark 即下单时的 signal_id。"""
        try:
            log.warning(
                "[broker] order ERROR callback: signal(remark)=%s order_id=%s error_id=%s error_msg=%s",
                getattr(order_error, "order_remark", "") or "",
                getattr(order_error, "order_id", ""),
                getattr(order_error, "error_id", ""),
                getattr(order_error, "error_msg", ""),
            )
        except Exception:  # noqa: BLE001
            log.exception("[adapter] on_order_error handling failed")

    def on_cancel_error(self, cancel_error):
        """§UAT 2026-08-31：撤单失败主推（XtCancelError）——与 on_order_error 同理补齐。"""
        try:
            log.warning(
                "[broker] cancel ERROR callback: order_id=%s error_id=%s error_msg=%s",
                getattr(cancel_error, "order_id", ""),
                getattr(cancel_error, "error_id", ""),
                getattr(cancel_error, "error_msg", ""),
            )
        except Exception:  # noqa: BLE001
            log.exception("[adapter] on_cancel_error handling failed")


class XtBroker(Broker):
    """真实东莞证券 MiniQMT 通道。xtquant 延迟 import；connect() 时初始化。"""

    def __init__(self, account, session_id=1, path="", reconnect_sec=5):
        """构造真实 MiniQMT 通道。

        :param account: 券商资金账号。
        :param session_id: xtquant 会话号（默认 1，按会话隔离的 IPC 队列名含此号）。
        :param path: xtquant 连接路径（Windows 上通常为 'extended' 或本地端口目录）。
        :param reconnect_sec: 重连循环失败重试间隔（秒）。
        English: builds the real MiniQMT broker with account/session/path and reconnect interval.
        """
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

        limit → FIX_PRICE(11)；market → 分市场映射：
          沪市 → MARKET_SH_CONVERT_5_LIMIT(43)「最优五档即时成交剩余转限价」（§FIX-0921：
          2026-09-01 14:43 实录：本东莞构建 xtconstant 无 MARKET_PEER_PRICE_FIRST_SH 常量，
          回退值 44(对手方最优价) 发沪市被柜台 200ms 内废单（XtMiniQmt 日志 prctype=84,
          stat 50→57）；沪市合规市价等价物是 42/43，取 43（剩余转限价，按我方价格兜底成交）；
          深市 → MARKET_PEER_PRICE_FIRST（对手方最优价，本构建=44；昨日 000600.SZ 实证可成交）。
        常量名缺失时用文档数值兜底；有 xtconstant 时以库值为准。
        """
        fix, sh_conv_limit, peer_sz = 11, 43, 14
        try:
            from xtquant import xtconstant  # noqa: PLC0415
            fix = getattr(xtconstant, "FIX_PRICE", fix)
            sh_conv_limit = getattr(xtconstant, "MARKET_SH_CONVERT_5_LIMIT", sh_conv_limit)
            peer_sz = getattr(xtconstant, "MARKET_PEER_PRICE_FIRST", peer_sz)
        except Exception:  # noqa: BLE001 — 无 xtquant 环境（mock/测试）
            pass
        if str(price_type or "").lower() == "limit":
            return int(fix)
        head = str(code or "").split(".")[0]
        return int(sh_conv_limit) if head.startswith("6") else int(peer_sz)

    def connect(self):
        """connect() 一次性建立 + 自实现自动重连循环。"""
        XtQuantTrader = self._import_xtquant()
        from xtquant.xttype import StockAccount
        with self._lock:
            if self._connected:
                return True
            # §修复 G4（2026-08-29）：重连前先停掉旧 trader，避免旧实例仍持有订阅、
            # 与新 trader 重复推送回报（双份成交回调）。首次连接 _trader 为 None 跳过。
            if self._trader is not None:
                try:
                    self._trader.stop()
                except Exception:  # noqa: BLE001
                    log.warning("[broker] stop stale trader on reconnect failed")
                self._trader = None
                self._acc = None
            trader = XtQuantTrader(self.path, self.session_id)
            # §FIX 2026-09-10 实录：start/connect/query 任一失败必须 stop() 本次 trader，
            # 否则每个重连周期（5s）泄漏一组共享内存 writer，累积后 xtquant 抛
            # "WaitingFreeWriter instances exceed maximum limit"，此后永远无法重连。
            try:
                if trader.start() is not None:  # 0 表示启动成功
                    raise RuntimeError("XtQuantTrader.start() failed")
                if trader.connect() != 0:
                    raise RuntimeError("XtQuantTrader.connect() failed")
                acc = StockAccount(self.account)
                time.sleep(1.0)
                asset = trader.query_stock_asset(acc)
                if asset is None:
                    raise RuntimeError(
                        "query_stock_asset() returned None — 确认东莞 miniQMT 客户端已登录并连接交易")
            except Exception:
                try:
                    trader.stop()
                except Exception:  # noqa: BLE001 — 清理失败不掩盖原始异常
                    log.warning("[broker] stop leaked trader after failed connect error")
                raise
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
        # §FIX 2026-09-10 实录：xtquant 委托回调对象备注字段名是 order_remark
        # （handler.py 同款修复的遗漏点）——读 remark 恒为空导致映射永不回填、撤单必失败。
        remark = getattr(order, "order_remark", "") or getattr(order, "remark", "") or ""
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
        """返回真实 MiniQMT 通道是否已连接（_connected 由适配器/重连循环维护）。"""
        return self._connected

    def place_order(self, req):
        """真实下单（xtquant order_stock）。

        前置校验：交易所后缀匹配（6→SH / 0,3→SZ / 4,8→BJ，防止 601086.SZ 类误传致 seq=-1）；
        order_type 取本机 xtconstant（STOCK_BUY/SELL=23/24，§FIX 2026-08-31 跨构建枚举错位根因）；
        返回 (ok, order_ref, err)，order_ref 为 "seq:<n>" 不透明串，真实委托号由回报回填。
        English: real order placement with exchange-suffix pre-check and local xtconstant
        order_type; returns an opaque "seq:<n>" ref resolved to the exchange id by callbacks.
        """
        if not self._connected:
            return False, "", "not connected"
        code = str(req.get("code", "") or "")
        # ── 交易所后缀前置校验 ──
        # 柜台对代码/交易所不匹配的委托只返回 seq=-1（无原因），在下单前本地拦截并
        # 给出明确错误（生产实际踩坑：601086 误传 .SZ → seq=-1 排障无门）。
        head = code.split(".")[0]
        suffix = code.split(".")[1].upper() if "." in code else ""
        _expect = ("SH" if head.startswith("6") else
                   "SZ" if head[:1] in ("0", "3") else
                   "BJ" if head[:1] in ("4", "8") else "")
        if not _expect:
            return False, "", "unsupported stock code: %s" % code
        if suffix and suffix != _expect:
            return False, "", "exchange suffix mismatch: %s should be %s" % (code, _expect)
        if suffix != _expect:
            code = "%s.%s" % (head, _expect)
        price = float(req.get("price", 0) or 0)
        side = req.get("side", "")
        # §FIX 2026-08-31：order_type 必须取自本机 xtconstant——东莞证券构建 STOCK_BUY=23/STOCK_SELL=24，
        # 此前硬编码主流文档口径 1101/1102（属另一枚举空间），客户端 orderservice 校验不过，
        # 一律回 "invalid order type [-1]"（市价/限价、沪市/深市全中，查询不受影响）。
        # English: derive order_type from the LOCAL xtconstant (DGZQ build: STOCK_BUY=23/STOCK_SELL=24);
        # the previously hardcoded 1101/1102 belong to a different enum space and were rejected
        # client-side with "invalid order type [-1]" for every market/price-type.
        # （xtquant 为 Windows 专有库延迟导入，此处内联 import）
        from xtquant import xtconstant as _xtc
        ot = int(getattr(_xtc, "STOCK_BUY", 23)) if side == "买入" else int(getattr(_xtc, "STOCK_SELL", 24))
        order_type = ot
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
            # seq=-1 = 柜台/客户端拒绝受理且无异步原因可查。上下文全量带出 +
            # 常见原因提示，避免排障无门（查询通而下单拒 → 优先查 QMT 策略交易
            # 权限是否开通、客户端是否已输入交易密码解锁）。
            log.warning("[broker] order rejected seq=-1: %s %s qty=%s price_type=%s price=%s signal=%s",
                        side, code, req.get("qty"), req.get("price_type"), price, signal_id)
            return False, "", ("order_stock failed (seq=-1): %s %s qty=%s price_type=%s price=%s — "
                               "柜台拒绝受理（常见原因：QMT 策略交易权限未开通/客户端交易未解锁/非交易时段）") % (
                side, code, req.get("qty"), req.get("price_type"), price)
        seq_s = str(seq)
        with self._lock:
            # 维护 seq ↔ signal_id 双向映射，供撤单时反查
            self._seq_signal[seq_s] = signal_id
            if signal_id:
                self._signal_seq[signal_id] = seq_s
        # §G4 返回不透明 "seq:<n>" 引用：首尔可凭此撤单（映射解析）；真实委托号由回报回填
        return True, "seq:%s" % seq_s, ""

    def cancel(self, order_id):
        """真实撤单（cancel_order_stock）。支持 "seq:<n>" 占位引用 → 解析为真实交易所委托号后撤单。

        未回报委托号时不可撤（返回失败原因）；非法串返回失败。
        English: real cancel; resolves an opaque "seq:" ref to the exchange order id first.
        """
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
        """查询真实持仓：映射 xtquant 持仓对象为网关统一字典（ts_code/name/qty/cost/amount...）。

        未连接返回空列表（调用方按不可信快照处理）；英文: queries real positions from
        xtquant and maps them into the gateway's unified position dicts.
        """
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
        """订阅回调（真实通道在 connect() 时已注册，此方法保持空实现以符合基类契约）。"""
        pass  # 回调已在 connect() 注册

    def query_asset(self):
        """查询账户资产（可用资金/冻结/总资产/市值）。xtquant query_stock_asset 返回
        XtAsset 对象（cash/frozen_cash/market_value/total_asset 属性，或平铺 dict）；
        未连接或异常返回 None。"""
        if not self._connected or self._trader is None:
            return None
        try:
            raw = self._trader.query_stock_asset(self._acc)
            if not raw:
                return None

            def g(k, d=0.0):
                # 统一取值辅助：兼容 dict 与对象属性两种返回形态
                if isinstance(raw, dict):
                    return raw.get(k, d)
                return getattr(raw, k, d)

            return {
                "cash": float(g("cash") or 0),
                "frozen_cash": float(g("frozen_cash") or 0),
                "total_asset": float(g("total_asset") or 0),
                "market_value": float(g("market_value") or 0),
            }
        except Exception as e:  # noqa: BLE001
            log.warning("[broker] query_asset failed: %s", e)
            return None


class MockBroker(Broker):
    """内存模拟通道：等价 Go cmd/qmt-mock（下单→延时模拟成交→回调 handler）。"""

    def __init__(self, account="MOCK0001", delay_sec=1, seed=None, account_init=100000.0):
        """构造内存模拟通道。

        :param account: mock 资金账号（默认 MOCK0001）。
        :param delay_sec: 模拟成交延时（秒），0 表示立即成交。
        :param seed: 种子持仓列表 [{ts_code,name,qty,cost_price,...}]，用于对账测试。
        :param account_init: 初始可用资金（默认 100000），种子持仓按成本占用现金。
        English: builds the in-memory mock broker with optional seed positions and a
        explicit cash account initialized to account_init minus seed-position cost.
        """
        self.account = account
        self.account_init = account_init
        self.delay_sec = delay_sec
        self.handler = None
        self._lock = threading.RLock()
        self._connected = False
        self._next_id = 1
        self._orders = {}   # order_id -> dict
        self._positions = {}  # ts_code -> dict
        # §P1-17 显式现金模型：可用资金随成交实时扣减/回补，而非仅由市值反推
        # （反推会因行情波动虚增/虚减现金，导致对账失真）。
        self._cash = float(account_init)
        for seed_pos in seed or []:
            self._positions[seed_pos["ts_code"]] = dict(seed_pos)
            # 种子持仓视为已占用现金（按成本），避免初始可用资金虚高
            self._cash -= float(seed_pos.get("qty", 0)) * float(seed_pos.get("cost_price", 0) or 0)

    def connect(self):
        """模拟通道连接：直接置为已连接（无真实握手）。"""
        self._connected = True
        return True

    def mark_disconnected(self):
        """模拟通道断线：仅置位标记（测试/mock 场景使用）。"""
        self._connected = False

    def is_connected(self):
        """返回模拟通道是否已连接。"""
        return self._connected

    def _next_order_id(self):
        """自增生成 mock 委托号（MOCK + 6 位序号），保证唯一。"""
        # 自增生成 mock 委托号（MOCK 前缀 + 6 位序号），保证唯一
        self._next_id += 1
        return "MOCK%06d" % self._next_id

    def place_order(self, req):
        """mock 下单：登记内存账本 → 后台延时模拟成交 → 回调 handler（订单+成交回报）。

        §修复撤单竞态：订单已撤/已删时不再延迟成交、不改持仓、不回调。
        English: mock order placement — records the order, then a delayed thread simulates
        the fill and fires order/trade callbacks; cancelled/deleted orders never fill.
        """
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
            """后台模拟成交线程：延时后把订单置为已成并回调订单+成交回报。"""
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
        """mock 撤单：仅「已报」态可撤（置为已撤），已成/不存在返回失败。"""
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
        """调用方须持 _lock。§P1-17 同步更新显式现金：买入扣减 price*qty，卖出回补 price*qty。"""
        code = order.get("code", "")
        qty = int(order.get("qty", 0))
        # 下单方向决定现金流向：买入占用、卖出释放
        if order.get("side") == "买入":
            self._cash -= qty * float(price)
        else:
            self._cash += qty * float(price)
        p = self._positions.get(code)
        # 买入：加仓或新建，按加权成本更新持仓成本与最高价
        if order.get("side") == "买入":
            if p is None:
                p = {"ts_code": code, "name": order.get("name", ""), "qty": 0,
                     "cost_price": 0.0, "amount": 0.0, "highest_price": price}
                self._positions[code] = p
            new_qty = p["qty"] + qty
            p["cost_price"] = (p["qty"] * p["cost_price"] + qty * price) / new_qty
            p["qty"] = new_qty
            p["amount"] = new_qty * price
            p["highest_price"] = max(p["highest_price"], price)
        else:
            if p is None:
                return
            remain = p["qty"] - qty
            if remain <= 0:
                del self._positions[code]
            else:
                p["qty"] = remain
                p["amount"] = remain * price

    def query_positions(self):
        """返回 mock 持仓快照（按市值降序），调用方持锁读取内存账本。"""
        with self._lock:
            return [dict(p) for p in sorted(
                self._positions.values(), key=lambda x: x.get("amount", 0), reverse=True)]

    def query_asset(self):
        """mock 账户资产：可用资金=显式现金账（成交实时扣减/回补），冻结 0、市值=持仓汇总。
        §P1-17 不再用 初始资金-市值 反推现金（会随行情波动虚增/虚减）。"""
        with self._lock:
            mv = sum(p.get("amount", 0.0) for p in self._positions.values())
            cash = self._cash
            return {
                "cash": max(cash, 0.0),
                "frozen_cash": 0.0,
                "total_asset": max(cash, 0.0) + mv,
                "market_value": mv,
            }


class QueuedBroker(Broker):
    """§QMT-DUAL 本地派发队列通道（QMT 完整版内置策略桥兜底）。

    量仔 /order 入本地 dispatch 表（返回 "seq:<n>" 占位引用），由跑在 QMT 客户端内置
    "模型交易"环境里的 qmt_bridge.py 轮询消费并真实下单；结果经 /dispatch/result 回填，
    网关按现有 handler 协议（order/trade/positions/account）推量仔，HTTP 契约不变。

    在线判定 = 桥心跳新鲜度（bridge_state.last_heartbeat ≤ heartbeat_timeout_sec）；
    持仓/资产 = 桥最近一次快照。撤单先解析 seq→交易所委托号（未回报前不可撤，与
    XtBroker 语义一致）。English: the local dispatch-queue channel backing the QMT
    in-client bridge; connectivity is heartbeat freshness, positions/asset come from the
    bridge snapshot, and cancels resolve seq refs to exchange order ids first.
    """

    def __init__(self, store, account="", heartbeat_timeout_sec=15, user_id=""):
        """构造本地派发队列通道。

        :param store: 本地 SQLite 账本（Store 实例，dispatch/bridge_state 表）。
        :param account: 券商资金账号（仅透出，实际执行在桥侧）。
        :param heartbeat_timeout_sec: 桥心跳新鲜窗口（秒），超时视为离线。
        :param user_id: 多账号归属标识（派发/落库统一携带）。
        """
        self.store = store
        self.account = account
        self.heartbeat_timeout_sec = heartbeat_timeout_sec
        self.user_id = user_id
        self.handler = None

    def connect(self):
        """桥在线由心跳驱动，connect 无需动作（幂等返回 True）。"""
        return True

    def is_connected(self):
        """桥是否在线：心跳新鲜度。"""
        try:
            return bool(self.store.bridge_connected(self.heartbeat_timeout_sec))
        except Exception:  # noqa: BLE001
            return False

    def place_order(self, req):
        """把订单写入派发队列，返回 (True, "seq:<n>", "")。"""
        seq = self.store.dispatch_enqueue_order(req, user_id=self.user_id)
        log.info("[queued] order enqueued %s seq=%s signal=%s", req.get("side"), seq,
                 req.get("signal_id"))
        return True, seq, ""

    def cancel(self, order_id):
        """把撤单请求写入派发队列。仅当交易所委托号已回报（或直接传入真实号）才可撤。"""
        oid = str(order_id or "")
        exchange_id, signal_id, code, side = self._resolve_cancel_target(oid)
        if not exchange_id:
            return False, "交易所委托号尚未回报，暂不可撤（%s）" % oid
        seq = self.store.dispatch_enqueue_cancel(
            oid, exchange_id, signal_id=signal_id, code=code, side=side, user_id=self.user_id)
        log.info("[queued] cancel enqueued seq=%s target=%s", seq, exchange_id)
        return True, ""

    def _resolve_cancel_target(self, order_id):
        """解析撤单目标：seq 占位 → 查派发项取交易所委托号；纯数字视为真实委托号。"""
        if order_id.startswith(("seq:", "SEQ:", "pending:")):
            row = self.store.dispatch_get(order_id)  # dispatch 表 seq 列存完整 "seq:<id>"
            if row is None:
                return "", "", "", ""
            return (str(row.get("order_id", "") or ""), row.get("signal_id", ""),
                    row.get("code", ""), row.get("side", ""))
        try:
            int(order_id)
        except ValueError:
            return "", "", "", ""
        row = self.store.dispatch_by_order_id(order_id)
        return (order_id, (row or {}).get("signal_id", ""),
                (row or {}).get("code", ""), (row or {}).get("side", ""))

    def query_positions(self):
        """返回桥最近一次持仓快照（未同步返回空列表，调用方按不可信快照处理）。"""
        return self.store.bridge_snapshot_get("positions") or []

    def query_asset(self):
        """返回桥最近一次账户资产快照（未同步返回 None）。"""
        return self.store.bridge_snapshot_get("asset")

    def subscribe(self):
        """无回调订阅（结果经 /dispatch/result 回填）。"""
        pass


def build_brokers(cfg, store):
    """§QMT-DUAL 按配置构建通道集合。

    双通道模式（broker=xt|queued）：同时持有 XtBroker（外部 xtquant→客户端）与
    QueuedBroker（派发队列→内置桥），任一时刻仅 active 一个接单；
    broker=mock 时仅单 MockBroker（联调）。返回 (active_broker, brokers_dict, active_key)。
    English: builds the broker set — dual-channel (xt + queued) for the compat+QMT
    dual-path deployment, single MockBroker for local wiring; returns the active broker,
    the full dict, and the active key.
    """
    kind = cfg.get("broker", "mock")
    if kind == "mock":
        mb = MockBroker(
            account=cfg.get("account", "MOCK0001"),
            delay_sec=cfg.get("mock_delay_sec", 1),
            seed=cfg.get("seed", []),
            account_init=float(cfg.get("account_init", 100000.0)),
        )
        return mb, {"mock": mb}, "mock"
    xt = XtBroker(
        account=cfg.get("account", ""),
        session_id=cfg.get("session_id", 1),
        path=cfg.get("xt_path", ""),
        reconnect_sec=cfg.get("reconnect_sec", 5),
    )
    queued = QueuedBroker(
        store,
        account=cfg.get("account", ""),
        heartbeat_timeout_sec=cfg.get("bridge_heartbeat_timeout_sec", 15),
        user_id=cfg.get("user_id", ""),
    )
    brokers = {"xt": xt, "queued": queued}
    active = kind if kind in brokers else "xt"
    return brokers[active], brokers, active


def build_broker(cfg):
    """按配置构建单通道（mock 联调用）。broker=xt → XtBroker；broker=queued → 需 store，
    此处仅构建 xt/mock（QueuedBroker 由 build_brokers 在网关装配时传入 store）。"""
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
        account_init=float(cfg.get("account_init", 100000.0)),  # §P1-17 多账号初始资金
    )
