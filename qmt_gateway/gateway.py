#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.gateway — 东莞证券 MiniQMT 网关 REST 服务（AUTO_TRADING_PLAN M2）。

零第三方依赖（标准库 http.server）。接口与首尔侧 trading.QMTClient 契约一致：
  POST /order   {"signal_id","code","name","strategy","side","price_type","price","qty","amount","created_at"} → {"ok","order_id","err"}
  POST /cancel  {"order_id"}                                                    → {"ok","err"}
  GET  /state   → {"connected","account","positions","orders"}
  GET  /health  → {"ok","ts","broker","broker_connected"}   （免鉴权；broker_connected 反映通道真实状态）

Bearer token 双向鉴权。§G1 下单改为「claim 占位 → 下单 → settle 回填」三段式，
signal_id 原子幂等（并发重试/崩溃窗口均不重复真实下单）；§G2 空 signal_id 直接 400；
§G7 整手规则分板块（创业板/科创板最低 200 股、1 股递增；卖出允许零股清仓）；
§G8 _dispatch 顶层异常保护，任何 handler 异常返回 500 JSON 而非裸断连。
broker 由 config 选择（xt/mock）。回报经 outbox 后台线程推送（handler.py）。

运行：
  pip install -r qmt_gateway/requirements.txt   # 仅 mock 联调可不装任何依赖
  python3 qmt_gateway/gateway.py -c qmt_gateway/config.example.json
（English: zero-dependency REST gateway matching the Seoul-side QMTClient contract; Bearer auth;
atomic claim-before-place idempotency on signal_id; board-aware lot rules; top-level dispatch
exception guard; broker chosen by config (xt/mock); reports pushed via outbox thread.）
"""
import argparse
import json
import logging
import os
import sys
import threading
import time

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from store import Store, is_placeholder_order_id  # noqa: E402
from ids import Idempotency  # noqa: E402
from broker import build_broker  # noqa: E402
from handler import ReportHandler, periodic_reconcile  # noqa: E402

# 模块级日志器
log = logging.getLogger("qmt_gateway")

# 默认配置：可被外部 config JSON 覆盖；report_url 留空则不推送首尔
DEFAULT_CONFIG = {
    "listen": "0.0.0.0:8789",
    "token": "change-me",
    "broker": "mock",
    "account": "MOCK0001",
    "db": "data.db",
    "user_id": "",             # 回报归属账号 ID（首尔侧 /api/qmt/report 归因用，建议显式配置）
    "report_url": "",          # 首尔服务器地址（如 http://43.108.86.140:8080），留空不推送
    "report_token": "",        # 推送 /api/qmt/report 的 token（默认同 token）
    "reconcile_sec": 60,       # 周期全量对账间隔（0=关闭）
    "seed": [],                # mock 预置持仓：[{"ts_code","name","qty","cost_price"}]
    # xt 通道参数
    "xt_path": "",
    "session_id": 1,
    "reconnect_sec": 5,
}


def load_config(path):
    cfg = dict(DEFAULT_CONFIG)
    if path and os.path.exists(path):
        with open(path, "r", encoding="utf-8") as f:
            user_cfg = json.load(f)
        cfg.update(user_cfg)
    if not cfg.get("report_token"):
        cfg["report_token"] = cfg.get("token", "")
    return cfg


def _code_head(code):
    """取证券代码数字头："600519.SH" → "600519"。"""
    return str(code or "").split(".")[0]


def lot_rule(code, side):
    """§G7 分板块申报单位。

    返回 (min_qty:int, step:int)：
      创业板(30)/科创板(68)：最低 200 股、之后 1 股递增；
      主板/其他：100 股整手。
    卖方向允许任意 ≥1 股（送转零股、基金零股清仓是交易所允许的），由调用方放宽。
    """
    head = _code_head(code)
    if head[:2] in ("30", "68"):
        return 200, 1
    return 100, 100


class Gateway:
    def __init__(self, cfg):
        self.cfg = cfg
        # 组装四大核心组件：本地账本 / 幂等守卫 / 交易通道 / 回报处理器
        self.store = Store(cfg["db"])
        self.ids = Idempotency(self.store)
        self.broker = build_broker(cfg)
        self.handler = ReportHandler(
            self.store, cfg.get("report_url", ""), cfg.get("report_token", ""), cfg.get("user_id", ""),
        )
        self.broker.handler = self.handler
        self._stop = threading.Event()
        self._reconcile_thread = None
        self._broker_thread = None

    def start(self):
        # §G1 启动检查：残留 pending 占位 = 上次进程在下单窗口内 crash，
        # 这些 signal_id 永久阻塞（安全侧失效），需人工核对券商侧是否已有委托。
        pendings = self.store.list_pending()
        for p in pendings:
            log.warning("[gateway] STALE pending order from previous crash: signal_id=%s "
                        "code=%s side=%s qty=%s — blocked to prevent duplicate real order; "
                        "verify against broker and resolve manually",
                        p.get("signal_id"), p.get("code"), p.get("side"), p.get("qty"))
        # 回报 outbox 发送线程
        self.handler.start_sender()
        # 连接 broker（失败则后台重试，不阻塞 HTTP 起服）
        self._broker_thread = threading.Thread(target=self._connect_loop, daemon=True)
        self._broker_thread.start()
        if self.cfg.get("reconcile_sec", 0) > 0:
            self._reconcile_thread = threading.Thread(
                target=periodic_reconcile, args=(self.handler, self.broker),
                kwargs={"interval_sec": self.cfg.get("reconcile_sec", 60), "stop": self._stop},
                daemon=True,
            )
            self._reconcile_thread.start()

    def stop(self):
        self._stop.set()
        self.handler.stop_sender()

    def _connect_loop(self):
        # 后台重连线程：通道断开时持续重试 connect()，直到进程停止
        while not self._stop.is_set():
            if not self.broker.is_connected():
                try:
                    self.broker.connect()
                    self.handler.disconnected = False
                    # 连上即推一次全量对账，首尔侧快速对齐（空快照由 on_positions 守卫）
                    poss = self.broker.query_positions()
                    if poss:
                        self.handler.on_positions(poss)
                    else:
                        log.warning("[gateway] post-connect empty positions snapshot — "
                                    "reconcile skipped (account data may not be synced yet)")
                except Exception as e:  # noqa: BLE001
                    log.warning("[gateway] broker connect failed: %s (retry %ss)", e,
                                self.cfg.get("reconnect_sec", 5))
                    time.sleep(self.cfg.get("reconnect_sec", 5))
                    continue
            time.sleep(1)

    def handle(self, method, path, body, request_handler):
        """路由分发。返回 (status, payload_dict)。"""
        if path == "/health":
            # ok=进程活着；broker_connected=通道真实状态（§修复 /health 说谎在线）
            return 200, {
                "ok": True,
                "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                "broker": self.cfg.get("broker", ""),
                "broker_connected": bool(self.broker.is_connected()),
            }
        if not self.broker.is_connected():
            return 503, {"ok": False, "err": "broker not connected"}
        if path == "/order" and method == "POST":
            return self._do_order(body)
        if path == "/cancel" and method == "POST":
            return self._do_cancel(body)
        if path == "/state" and method == "GET":
            return self._do_state()
        return 404, {"ok": False, "err": "not found"}

    def _do_order(self, body):
        # 下单主流程：参数校验 → 幂等占位 → 真实下单 → 回填
        req = body or {}
        code = str(req.get("code", "") or "")
        try:
            qty = int(req.get("qty", 0) or 0)
            price = float(req.get("price", 0) or 0)
        except (TypeError, ValueError):
            return 400, {"ok": False, "err": "qty/price must be numbers"}
        if not code or qty <= 0:
            return 400, {"ok": False, "err": "code/qty required"}
        if price <= 0 and str(req.get("price_type", "")).lower() != "market":
            return 400, {"ok": False, "err": "price required for limit orders"}

        signal_id = str(req.get("signal_id", "") or "")
        # §G2 空 signal_id 一律拒绝——它是幂等与审计的唯一锚点，不允许静默放行
        if not signal_id:
            return 400, {"ok": False, "err": "signal_id required"}

        side = req.get("side", "")
        # 按板块取申报单位规则后做整手校验
        min_qty, step = lot_rule(code, side)
        if side == "卖出":
            if qty < 1:
                return 400, {"ok": False, "err": "qty must be >= 1"}
            # 卖出允许非整手（送转零股/科创零股），不按买入规则拦截
        else:
            if qty < min_qty or ((qty - min_qty) % step) != 0:
                return 400, {"ok": False,
                             "err": "qty violates board lot rule (%s: min %d step %d)" % (code, min_qty, step)}

        # 仓位上限（双端校验之一；一次查询复用）
        max_pos = int(self.cfg.get("max_positions", 0) or 0)
        if max_pos > 0 and side == "买入":
            held_list = self.store.list_positions()
            held_codes = [p["ts_code"] for p in held_list]
            if len(held_list) >= max_pos and code not in held_codes:
                return 400, {"ok": False, "err": "max_positions reached"}

        # §G1 原子占位：抢不到 = 已处理过（幂等返回）或正在下单中（409 防并发穿透）
        draft = {
            "signal_id": signal_id, "code": code, "side": side,
            "price": price, "qty": qty,
            "created_at": req.get("created_at") or "",
        }
        claimed, existing = self.ids.claim(draft)
        # 没抢到占位 = 已下过（幂等返回）或正在下单中（409 防并发穿透）
        if not claimed:
            existing = existing or {}
            oid = existing.get("order_id") or ""
            if is_placeholder_order_id(oid):
                return 409, {"ok": False, "err": "duplicate signal_id in-flight"}
            # 幂等：已下过 → 返回原委托引用（不重复下单）
            return 200, {"ok": True, "order_id": oid, "err": ""}

        ok, order_ref, err = self.broker.place_order(req)
        if not ok:
            # 失败释放占位，允许后续重试真正重新下单
            self.ids.release(signal_id)
            return 400, {"ok": False, "err": err or "place order failed"}
        # settle：占位行回填真实委托引用（seq:<n> 或 mock 单号；交易所号随后续回报替换）
        self.ids.settle({
            "order_id": order_ref, "signal_id": signal_id, "code": code, "side": side,
            "status": "已报", "price": price, "qty": qty,
            "created_at": req.get("created_at") or time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
        })
        return 200, {"ok": True, "order_id": order_ref, "err": ""}

    def _do_cancel(self, body):
        # 撤单：校验引用后委托给 broker，结果如实返回（失败不让首尔误判成功）
        order_ref = str((body or {}).get("order_id", "") or "")
        if not order_ref:
            return 400, {"ok": False, "err": "order_id required"}
        # §修复：撤单结果不再被吞——失败让首尔侧继续跟踪该委托并告警
        ok, err = self.broker.cancel(order_ref)
        if ok:
            return 200, {"ok": True, "err": ""}
        return 409, {"ok": False, "err": err or "cancel failed"}

    def _do_state(self):
        positions = self.broker.query_positions() if self.broker.is_connected() else []
        orders = self.store.list_orders()
        return 200, {
            "connected": self.broker.is_connected(),
            "account": self.cfg.get("account", ""),
            "positions": positions,
            "orders": orders,
        }


    class _Handler(BaseHTTPRequestHandler):
        # 类级注入网关实例，使每个连接的处理器都能访问同一 Gateway（ThreadingHTTPServer 每连接新建 handler）
        gateway = None  # 类级注入，跨实例共享（ThreadingHTTPServer 每连接新建 handler）

    def _auth_ok(self):
        # Bearer token 双向鉴权：比对请求头与配置 token
        auth = self.headers.get("Authorization", "")
        token = self.gateway.cfg.get("token", "")
        return auth == "Bearer " + token

    def _respond(self, status, payload):
        data = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _dispatch(self):
        # 统一分发入口：解析路径/读取 JSON 体 → 鉴权 → 交由 Gateway.handle 处理
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length") or 0)
        body = None
        if length > 0:
            # 有请求体时按长度读取并解析 JSON；解析失败返回 400
            raw = self.rfile.read(length)
            try:
                body = json.loads(raw.decode("utf-8"))
            except ValueError:
                return self._respond(400, {"ok": False, "err": "bad JSON body"})
        if parsed.path != "/health" and not self._auth_ok():
            return self._respond(401, {"ok": False, "err": "unauthorized"})
        try:
            status, payload = self.gateway.handle(self.command, parsed.path, body, self)
        except Exception as e:  # noqa: BLE001
            # §G8 顶层异常保护：handler 内任何异常返回结构化错误而非裸断连，
            # 避免首尔侧把无响应当 transport error 无限重试打爆网关。
            log.exception("[gateway] unhandled error in %s %s", self.command, parsed.path)
            status, payload = 500, {"ok": False, "err": "internal error: %s" % e}
        self._respond(status, payload)

    def do_GET(self):  # noqa: N802
        self._dispatch()

    def do_POST(self):  # noqa: N802
        self._dispatch()

    def log_message(self, *args):  # noqa: A003
        pass


    def main(argv=None):
        # 命令行入口：解析参数 → 装载配置 → 启动 HTTP 服务与网关后台线程
        ap = argparse.ArgumentParser(description="MiniQMT gateway (M2)")
    ap.add_argument("-c", "--config", default="", help="config JSON path")
    ap.add_argument("--listen", default="", help="override listen addr")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    cfg = load_config(args.config)
    if args.listen:
        cfg["listen"] = args.listen

    gw = Gateway(cfg)
    _Handler.gateway = gw

    host, _, port = cfg["listen"].partition(":")
    server = ThreadingHTTPServer((host, int(port)), _Handler)
    log.info("[gateway] listening on %s (broker=%s account=%s report_url=%s)",
             cfg["listen"], cfg.get("broker"), cfg.get("account"), cfg.get("report_url") or "(none)")
    gw.start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("[gateway] shutting down")
        gw.stop()
        server.shutdown()


if __name__ == "__main__":
    main()
