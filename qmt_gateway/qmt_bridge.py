#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.qmt_bridge — QMT 完整版内置"模型交易"策略桥（§QMT-DUAL 兜底路径）。

跑在东莞证券 QMT 完整版客户端的「模型交易」Python 环境里，替代原外部 xtquant
（miniQMT）下单通道：轮询网关派发队列取单 → 用内置 xttrader 真实下单 → 回报结果。
网关 HTTP 契约（/order /cancel /state /health + /api/qmt/report）对量仔零改动。

核心设计（与网关 /dispatch 协议对应）：
  - 心跳/持仓/资产快照 → POST /dispatch/result（驱动网关 QueuedBroker.is_connected）
  - GET /dispatch/pending → 订单/撤单，逐个执行后回报 order_result / cancel_result
  - query_stock_trades 回补成交 → POST /dispatch/result type=trade（网关按 trade_id 去重）

适配策略（真机探测要点，见 docs/MIGRATION_QMT_DUAL_PATH.md §R3）：
  - QMT 内置环境预载 `from xtquant import xttrader, xtconstant`，无需 pip；
  - 所有 xttrader 交互集中在 XtAdapter，字段名/返回值形态跨构建有差异时只改这一处；
  - 内置环境可能无 requests → 全程 urllib.request；网络异常按步降级不阻塞主循环；
  - dry_run=True 时不下单（L0 联调/演练探测用），下单函数仅记录并返回模拟结果。

（English: in-client bridge strategy for the full QMT "model trading" environment. It polls
the gateway dispatch queue, executes orders through the embedded xttrader API and reports
results back; all broker interaction is isolated in XtAdapter for on-box calibration, and
urllib is used since the embedded Python may lack requests. dry_run places nothing.）
"""
import json
import logging
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, time as dtime, timedelta

try:
    from zoneinfo import ZoneInfo
    _BJ = ZoneInfo("Asia/Shanghai")
except Exception:  # noqa: BLE001
    _BJ = None

log = logging.getLogger("qmt_bridge")


def _now_bj():
    """北京时间（zoneinfo 不可用时退化为 UTC+8 估算）。"""
    if _BJ is not None:
        return datetime.now(_BJ)
    return datetime.utcnow() + timedelta(hours=8)


def is_trading_window(now=None):
    """与网关/引擎对齐的活跃窗口：工作日 9:15~15:00（简化为工作日即交易日，非交易时段
    抑制不影响正确性）。桥在该窗口外也可心跳保活（客户端被 qmtctl 杀掉前）。"""
    now = now or _now_bj()
    if now.weekday() >= 5:
        return False
    t = now.time()
    return dtime(9, 15) <= t < dtime(15, 0)


def _price_type_const(price_type, code, xtconstant):
    """price_type(market/limit) → 内置 xtconstant 常量（与 broker.py 同口径）：
    limit=FIX_PRICE；market 分市场：沪 43(最优五档即时成交剩余转限价) / 深 14(对手方最优)。
    常量缺失时用文档数值兜底。"""
    fix, sh_conv, peer = 11, 43, 14
    try:
        fix = int(getattr(xtconstant, "FIX_PRICE", fix))
        sh_conv = int(getattr(xtconstant, "MARKET_SH_CONVERT_5_LIMIT", sh_conv))
        peer = int(getattr(xtconstant, "MARKET_PEER_PRICE_FIRST", peer))
    except Exception:  # noqa: BLE001
        pass
    if str(price_type or "").lower() == "limit":
        return fix
    head = str(code or "").split(".")[0]
    return sh_conv if head.startswith("6") else peer


class XtAdapter:
    """QMT 内置 xttrader 交互适配器（真机差异集中在此，便于探测/校准与单测替换）。

    order_book 键与内置 API 对齐：account/order_type('buy'|'sell')/stock_code/
    price_type/price/volume/strategy_name/order_remark。
    """

    def __init__(self, account="", dry_run=False):
        # 真机模式在首次下单时才延迟载入内置 xtquant（见 _ensure）；dry_run 恒可达
        self.account = account
        self.dry_run = dry_run
        self._xt = None
        self._xtc = None

    def _ensure(self):
        """延迟导入内置 xtquant（模型交易环境已预载）。不可用即抛 RuntimeError。"""
        if self._xt is not None:
            return
        try:
            from xtquant import xttrader  # noqa: PLC0415
            from xtquant import xtconstant  # noqa: PLC0415
            self._xt = xttrader
            self._xtc = xtconstant
        except ImportError as e:  # pragma: no cover — 仅真机有 xtquant
            raise RuntimeError("内置 xtquant 不可用（确认策略跑在 QMT 模型交易环境）: %s" % e)

    def available(self):
        """内置环境是否可用（dry_run 恒 True）。"""
        if self.dry_run:
            return True
        try:
            self._ensure()
            return True
        except Exception:  # noqa: BLE001
            return False

    # ── 下单 / 撤单 ──
    def place(self, req):
        """真实下单。返回 (ok, order_id, err)。dry_run 时不下单仅记录。"""
        signal_id = str(req.get("signal_id", "") or "")
        if self.dry_run:
            log.info("[bridge:dry] place %s %s %s qty=%s signal=%s",
                     req.get("side"), req.get("code"), req.get("price_type"),
                     req.get("qty"), signal_id)
            return True, "DRYRUN-%d" % int(time.time() * 1000), ""
        self._ensure()
        order_type = "buy" if req.get("side") == "买入" else "sell"
        book = {
            "order_type": order_type,
            "stock_code": req.get("code", ""),
            "price_type": _price_type_const(req.get("price_type"), req.get("code"), self._xtc),
            "price": float(req.get("price", 0) or 0),
            "volume": int(req.get("qty", 0) or 0),
            "strategy_name": "qmt_bridge",
            "order_remark": signal_id,
        }
        if self.account:
            book["account"] = self.account
        # §真机探测：trade.stock 返回 (retcode, order_id)；retcode=0 成功。
        # 跨构建若为 dict 形态，按 keys 探测兼容。
        try:
            res = self._xt.trade.stock(book)
        except Exception as e:  # noqa: BLE001
            log.exception("[bridge] place failed (API error)")
            return False, "", "xttrader.trade.stock error: %s" % e
        ret, oid = self._parse_result(res)
        if ret != 0:
            return False, "", "order rejected (ret=%s): %s" % (ret, self._dump(book))
        return True, str(oid), ""

    def cancel(self, seq, exchange_order_id, code=""):
        """撤单：order_book 携带交易所委托号。返回 (ok, err)。"""
        if self.dry_run:
            log.info("[bridge:dry] cancel order_id=%s", exchange_order_id)
            return True, ""
        self._ensure()
        try:
            int(exchange_order_id)
        except (TypeError, ValueError):
            return False, "invalid exchange order_id: %s" % exchange_order_id
        book = {"order_id": int(exchange_order_id)}
        if code:
            book["stock_code"] = code
        if self.account:
            book["account"] = self.account
        try:
            res = self._xt.trade.cancel_stock(book)
        except Exception as e:  # noqa: BLE001
            log.exception("[bridge] cancel failed (API error)")
            return False, "xttrader.trade.cancel_stock error: %s" % e
        ret, _ = self._parse_result(res)
        return (True, "") if ret == 0 else (False, "cancel rejected (ret=%s)" % ret)

    @staticmethod
    def _parse_result(res):
        """解析 trade.stock/cancel_stock 返回值：兼容 (retcode, order_id) 元组与 dict。"""
        if isinstance(res, (tuple, list)):
            res = list(res)
            ret = int(res[0]) if res else -1
            oid = res[1] if len(res) > 1 else ""
            return ret, oid
        if isinstance(res, dict):
            ret = int(res.get("retcode", res.get("ret", -1)))
            return ret, res.get("order_id", "")
        return -1, ""

    @staticmethod
    def _dump(book):
        """下单参数脱敏打印（排障用）。"""
        b = dict(book)
        b.pop("account", None)
        return json.dumps(b, ensure_ascii=False)

    # ── 查询：持仓 / 资产 / 委托 / 成交 ──
    def query_positions(self):
        """持仓快照 → 网关统一 dict 列表。dry_run 返回空。"""
        if self.dry_run:
            return []
        self._ensure()
        raw = self._xt.trade.query_stock_positions()
        out = []
        for p in raw or []:
            item = p if isinstance(p, dict) else p
            ts_code = self._field(item, "stock_code", "m_strStockCode")
            name = self._field(item, "stock_name", "m_strStockName") or ""
            volume = int(self._field(item, "volume", "m_nVolume") or 0)
            cost = float(self._field(item, "open_price", "m_dOpenPrice") or 0)
            mv = float(self._field(item, "market_value", "m_dMarketValue") or 0)
            if not ts_code:
                continue
            out.append({
                "ts_code": ts_code, "name": name, "qty": volume,
                "cost_price": cost, "amount": mv, "highest_price": cost,
                "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            })
        return out

    def query_asset(self):
        """资产快照 → 网关统一 dict；查询失败返回 None。"""
        if self.dry_run:
            return None
        self._ensure()
        try:
            raw = self._xt.trade.query_stock_asset()
        except Exception:  # noqa: BLE001
            log.exception("[bridge] query asset failed")
            return None
        if not raw:
            return None
        item = raw if isinstance(raw, dict) else raw
        g = lambda k, d=0.0: float(self._field(item, k, "", d) or d)  # noqa: E731
        return {
            "cash": g("cash", 0.0),
            "frozen_cash": g("frozen_cash", 0.0),
            "total_asset": g("total_asset", 0.0),
            "market_value": g("market_value", 0.0),
        }

    def query_trades(self):
        """返回成交列表（每项含 order_remark 归因 + 买卖方向）。dry_run 返回空。"""
        if self.dry_run:
            return []
        self._ensure()
        raw = self._xt.trade.query_stock_trades()
        out = []
        for t in raw or []:
            item = t if isinstance(t, dict) else t
            signal = str(self._field(item, "order_remark", "m_strRemark") or "")
            side = self._side_of(item)
            ts_code = self._field(item, "stock_code", "m_strStockCode") or ""
            oid = str(self._field(item, "order_id", "m_nOrderID") or "")
            tid = str(self._field(item, "trade_id", "order_sysid", "m_strOrderSysID") or "")
            if not ts_code or not oid:
                continue
            out.append({
                "order_id": oid, "trade_id": tid, "signal_id": signal,
                "name": str(self._field(item, "stock_name", "m_strStockName") or ""),
                "code": ts_code, "side": side,
                "price": float(self._field(item, "traded_price", "m_dTradedPrice") or 0),
                "qty": int(self._field(item, "traded_volume", "m_nTradedVolume") or 0),
                "amount": float(self._field(item, "traded_amount", "m_dTradedAmount") or 0),
                "traded_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            })
        return out

    @staticmethod
    def _field(item, *keys, default=""):
        """跨形态取字段：支持 dict 键名与对象属性名（含备选键）。"""
        for k in keys:
            if not k:
                continue
            if isinstance(item, dict) and k in item:
                return item[k]
            v = getattr(item, k, None)
            if v is not None:
                return v
        return default

    @staticmethod
    def _side_of(item):
        """买卖方向（与 handler._side_of 同口径：order_type 23/24|1101/1102）。"""
        for attr in ("order_type",):
            v = XtAdapter._field(item, attr)
            if v is None:
                continue
            try:
                v = int(v)
            except (TypeError, ValueError):  # noqa: BLE001
                continue
            if v in (23, 1101):
                return "买入"
            if v in (24, 1102):
                return "卖出"
        return ""


class Bridge:
    """桥主循环（可单测：注入 FakeAdapter，逐次 run_once 驱动）。"""

    def __init__(self, gateway_url, token="", account="", poll_sec=1.0,
                 heartbeat_sec=5.0, positions_sec=30.0, dry_run=False):
        """构造桥。

        :param gateway_url: 网关地址（如 http://127.0.0.1:8789）。
        :param token: 网关 Bearer token（与 config 一致）。
        :param account: 券商资金账号（透传给 xttrader）。
        :param poll_sec: 取单轮询间隔（秒，默认 1s）。
        :param heartbeat_sec: 心跳上报间隔（秒，驱动网关连通性判定）。
        :param positions_sec: 持仓/资产快照上报间隔（秒）。
        :param dry_run: dry-run 只查询不下单（L0 联调探测）。
        """
        self.base = gateway_url.rstrip("/")
        self.token = token
        self.account = account
        self.poll_sec = poll_sec
        self.heartbeat_sec = heartbeat_sec
        self.positions_sec = positions_sec
        self.dry_run = dry_run
        self.adapter = XtAdapter(account=account, dry_run=dry_run)
        self._last_hb = 0.0
        self._last_pos = 0.0
        self._seen_trades = set()  # 已上报成交 trade_id（进程内去重；网关侧亦有 trade_id 去重）

    # ── HTTP 原语（urllib，零第三方依赖）──
    def _post(self, path, payload):
        """POST JSON 到网关，返回 (status, dict|None)。失败返回 (0, None)。"""
        data = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        req = urllib.request.Request(self.base + path, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                try:
                    return resp.status, json.loads(resp.read().decode("utf-8"))
                except ValueError:  # noqa: BLE001
                    return resp.status, None
        except (urllib.error.URLError, OSError) as e:
            log.warning("[bridge] POST %s failed: %s", path, e)
            return 0, None

    def _get(self, path):
        """GET JSON 到网关，返回 (status, dict|None)。失败返回 (0, None)。"""
        req = urllib.request.Request(self.base + path, method="GET")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return resp.status, json.loads(resp.read().decode("utf-8"))
        except (urllib.error.URLError, OSError) as e:
            log.warning("[bridge] GET %s failed: %s", path, e)
            return 0, None

    # ── 单轮主逻辑 ──
    def run_once(self, now=None):
        """执行一轮：心跳 → 快照 → 取单执行 → 回补成交。全程异常按步降级不抛出。"""
        now = now or time.time()
        # 1) 心跳（驱动网关 QueuedBroker.is_connected）
        if now - self._last_hb >= self.heartbeat_sec:
            self._post("/dispatch/result", {"type": "heartbeat", "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00")})
            self._last_hb = now
        # 2) 持仓/资产快照（周期性；网关 /state 与对账数据源）
        if now - self._last_pos >= self.positions_sec:
            self._report_snapshot()
            self._last_pos = now
        # 3) 取单执行
        self._process_pending()
        # 4) 回补成交
        self._report_new_trades()

    def _report_snapshot(self):
        """上报持仓与资产快照（各自独立失败，不影响其他）。"""
        try:
            poss = self.adapter.query_positions()
            self._post("/dispatch/result", {"type": "positions", "positions": poss})
        except Exception:  # noqa: BLE001
            log.exception("[bridge] positions snapshot failed")
        try:
            asset = self.adapter.query_asset()
            if asset:
                self._post("/dispatch/result", {"type": "account", "asset": asset})
        except Exception:  # noqa: BLE001
            log.exception("[bridge] asset snapshot failed")

    def _process_pending(self):
        """取派发队列并逐个执行订单/撤单，回报结果。"""
        status, body = self._get("/dispatch/pending")
        if status != 200 or not body or not body.get("ok"):
            return
        for item in body.get("items") or []:
            seq = str(item.get("seq", "") or "")
            kind = item.get("kind", "order")
            try:
                if kind == "order":
                    self._execute_order(item)
                elif kind == "cancel":
                    self._execute_cancel(item)
                else:
                    log.warning("[bridge] unknown dispatch kind=%s seq=%s", kind, seq)
            except Exception:  # noqa: BLE001
                log.exception("[bridge] execute %s seq=%s failed", kind, seq)
                self._post("/dispatch/result", {"type": "order_result", "seq": seq,
                                                "ok": False, "err": "bridge exception"})

    def _execute_order(self, item):
        """执行一笔下单并回报结果（order_result 含交易所委托号或拒因）。"""
        seq = str(item.get("seq", "") or "")
        signal_id = item.get("signal_id", "")
        if not signal_id:
            log.warning("[bridge] order without signal_id: seq=%s", seq)
            self._post("/dispatch/result", {"type": "order_result", "seq": seq,
                                            "ok": False, "err": "missing signal_id"})
            return
        ok, order_id, err = self.adapter.place(item)
        if ok:
            log.info("[bridge] placed seq=%s signal=%s order_id=%s", seq, signal_id, order_id)
            self._post("/dispatch/result", {"type": "order_result", "seq": seq, "ok": True,
                                            "order_id": order_id, "err": ""})
        else:
            log.warning("[bridge] place rejected seq=%s signal=%s: %s", seq, signal_id, err)
            self._post("/dispatch/result", {"type": "order_result", "seq": seq, "ok": False,
                                            "err": err or "place failed"})

    def _execute_cancel(self, item):
        """执行撤单并回报结果。"""
        seq = str(item.get("seq", "") or "")
        exchange_id = str(item.get("order_id", "") or "")
        ok, err = self.adapter.cancel(seq, exchange_id, code=item.get("code", ""))
        if ok:
            log.info("[bridge] cancelled seq=%s order_id=%s", seq, exchange_id)
        else:
            log.warning("[bridge] cancel failed seq=%s order_id=%s: %s", seq, exchange_id, err)
        self._post("/dispatch/result", {"type": "cancel_result", "seq": seq, "ok": ok, "err": err})

    def _report_new_trades(self):
        """回补成交：query_stock_trades 里未上报的（trade_id 去重）→ type=trade。"""
        try:
            trades = self.adapter.query_trades()
        except Exception:  # noqa: BLE001
            log.exception("[bridge] query trades failed")
            return
        for t in trades:
            tid = t.get("trade_id", "")
            if tid and tid in self._seen_trades:
                continue
            self._seen_trades.add(tid)
            self._post("/dispatch/result", {"type": "trade", **t})

    # ── 常驻循环 ──
    def run_forever(self):
        """常驻循环：直到进程退出（QMT 停策略/客户端被杀即止）。"""
        log.info("[bridge] starting (url=%s dry_run=%s poll=%.1fs hb=%.1fs pos=%.1fs)",
                 self.base, self.dry_run, self.poll_sec, self.heartbeat_sec, self.positions_sec)
        while True:
            try:
                self.run_once()
            except Exception:  # noqa: BLE001
                log.exception("[bridge] run_once unhandled error")
            time.sleep(self.poll_sec)


def main(argv=None):
    """命令行入口（真机以策略形式在 QMT 内运行；此处供本机/容器联调与排障）。

    配置优先级：内置默认 < config.bridge.json < 命令行参数。真机在 QMT 内置环境里无法
    传参，因此把网关地址/token 等写进脚本同目录 config.bridge.json 即可（见 config.bridge.example.json）。
    """
    import argparse  # noqa: PLC0415
    import os  # noqa: PLC0415
    import json  # noqa: PLC0415
    default_cfg = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.bridge.json")
    ap = argparse.ArgumentParser(description="QMT in-client bridge (standalone/dry-run)")
    ap.add_argument("--config", default=default_cfg, help="bridge config JSON path")
    ap.add_argument("--gateway", default="", help="gateway URL")
    ap.add_argument("--token", default="", help="gateway Bearer token")
    ap.add_argument("--account", default="", help="broker account")
    ap.add_argument("--poll", type=float, default=0.0)
    ap.add_argument("--hb", type=float, default=0.0)
    ap.add_argument("--pos", type=float, default=0.0)
    ap.add_argument("--dry-run", dest="dry_run", action="store_true", default=None)
    ap.add_argument("--once", action="store_true", help="run one round then exit")
    args = ap.parse_args(argv)

    cfg = {}
    if args.config and os.path.exists(args.config):
        try:
            with open(args.config, "r", encoding="utf-8") as f:
                cfg = json.load(f) or {}
        except Exception as e:  # noqa: BLE001
            log.warning("[bridge] config 读取失败，使用默认值: %s", e)

    gateway = args.gateway or cfg.get("gateway_url") or "http://127.0.0.1:8789"
    token = args.token or cfg.get("token") or ""
    account = args.account or cfg.get("account") or ""
    poll = args.poll or float(cfg.get("poll_sec", 1.0))
    hb = args.hb or float(cfg.get("heartbeat_sec", 5.0))
    pos = args.pos or float(cfg.get("positions_sec", 30.0))
    dry_run = cfg.get("dry_run", False) if args.dry_run is None else args.dry_run

    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    b = Bridge(gateway, token=token, account=account, poll_sec=poll,
               heartbeat_sec=hb, positions_sec=pos, dry_run=dry_run)
    if args.once:
        b.run_once()
        return
    b.run_forever()


if __name__ == "__main__":
    main()
