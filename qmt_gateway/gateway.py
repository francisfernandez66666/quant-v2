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

====================================================================================
安全加固（本文件）相关环境变量与运维须知（中文）：
  - QUANT_GATEWAY_TOKEN       网关鉴权 token。优先从此环境变量读取，未设置才回退 config 明文。
  - QUANT_GATEWAY_REPORT_TOKEN 推送首尔 /api/qmt/report 的 token，可选；缺省同 QUANT_GATEWAY_TOKEN。
  - ALLOWED_IPS               逗号分隔的允许来源 IP（如决策机出口 IP）。非空时，/order、/cancel、
                              /state 等敏感端点会校验来源 IP，不在白名单返回 403。
  - QUANT_GATEWAY_TLS_CERT / QUANT_GATEWAY_TLS_KEY  两者均设置则启动 HTTPS，否则启动 HTTP。
  - QUANT_GATEWAY_BIND        显式指定对外监听地址（如 0.0.0.0:8789）。未设置且配置为 0.0.0.0 时，
                              自动收敛为 127.0.0.1 仅本机可访问。/health 仅允许本机回环访问。

重要（需用户手动操作，脚本不代劳）：
  1) 真实 token 轮换请在【券商端】手动完成，本仓库代码不做任何 token 改写/轮换。
  2) 历史上若 config.xt.json 曾以明文提交过 token，请用 `git filter-repo` 清除仓库历史中的明文，
     并在本地把该文件加入忽略/移出仓库；本改动不触碰 .gitignore 也不改 git 历史。
====================================================================================
"""
import argparse
import hmac
import json
import logging
import os
import ssl
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

    # token 优先从环境变量 QUANT_GATEWAY_TOKEN 读取（脱离代码仓库，避免明文提交）；
    # 环境变量未设置时回退到配置文件明文，并打印 warning 提示用户改用环境变量。
    env_token = os.environ.get("QUANT_GATEWAY_TOKEN")
    if env_token:
        cfg["token"] = env_token
    else:
        cfg_token = cfg.get("token", "")
        # 仅当配置里确实写了非默认 token 时才告警（默认占位 "change-me" 不告警）
        if cfg_token and cfg_token != DEFAULT_CONFIG.get("token", ""):
            log.warning(
                "[gateway] token 来自配置文件明文（config），建议改用环境变量 QUANT_GATEWAY_TOKEN "
                "以避免泄露；真实 token 请由用户在券商端手动轮换，并用 git filter-repo 清除历史明文"
            )

    # report_token 同理：优先环境变量 QUANT_GATEWAY_REPORT_TOKEN，否则回退 token
    env_report_token = os.environ.get("QUANT_GATEWAY_REPORT_TOKEN")
    if env_report_token:
        cfg["report_token"] = env_report_token
    elif not cfg.get("report_token"):
        cfg["report_token"] = cfg.get("token", "")
    return cfg


def _code_head(code):
    """取证券代码数字头："600519.SH" → "600519"。"""
    return str(code or "").split(".")[0]


def lot_rule(code, side):
    """§G7 分板块申报单位。

    返回 (min_qty:int, step:int)：
       科创板(688)：最低 200 股、1 股递增；
       创业板(300/301)：最低 100 股、1 股递增；
       北交所(920/83/87/43 等 8/4 开头)：最低 100 股、1 股递增；
       主板/其他：100 股整手。
    §修复 T4（2026-08-29）：此前 创业板与科创板同判 200 股 → 所有合法创业板 100 股小单被拒、
    整板无法成交。现拆分。
    卖方向允许任意 ≥1 股（送转零股、基金零股清仓是交易所允许的），由调用方放宽。
    """
    head = _code_head(code)
    if head[:2] == "68":
        return 200, 1   # 科创板 688：最低 200 股、1 股递增
    if head[:2] == "30":
        return 100, 1   # 创业板 300/301：最低 100 股、1 股递增
    if head[:2] == "92" or head[:1] in ("8", "4"):
        return 100, 1   # 北交所（920/83/87/43 等）：最低 100 股、1 股递增
    return 100, 100     # 主板/其他：100 股整手


class Gateway:
    def __init__(self, cfg):
        self.cfg = cfg
        # 组装四大核心组件：本地账本 / 幂等守卫 / 交易通道 / 回报处理器
        self.store = Store(cfg["db"])
        self.ids = Idempotency(self.store)
        self.broker = build_broker(cfg)
        self.user_id = cfg.get("user_id", "")  # §P1-9 多账号归属：落库/上报统一带此 ID
        self.handler = ReportHandler(
            self.store, cfg.get("report_url", ""), cfg.get("report_token", ""), self.user_id,
        )
        self.broker.handler = self.handler
        self._stop = threading.Event()
        self._reconcile_thread = None
        self._broker_thread = None
        # 来源 IP 白名单（由 main 从环境变量 ALLOWED_IPS 注入；空列表表示不做 IP 限制，仅依赖 token）
        self.allowed_ips = []

    def start(self):
        # §修复 T5（2026-08-29）：启动时先清理「超时残留 pending 占位」——上次进程在下单窗口内 crash
        # 留下的 pending 行会永久阻塞 signal_id（首尔重试恒得 409 duplicate in-flight）。删除超过
        # 10 分钟的残留占位行，安全解锁；仍在 10 分钟内的（极短窗口内刚崩溃）保留并告警，避免
        # 与可能已真实发出的券商委托冲突。
        released = 0
        try:
            released = self.store.release_stale_pending(max_age_sec=600)
        except Exception as _e:  # noqa: BLE001 — 清理解放失败不应阻断启动
            log.warning("[gateway] 清理残留 pending 失败: %s", _e)
        if released:
            log.warning("[gateway] 已释放 %d 个超时残留 pending 占位（崩溃恢复，避免 signal_id 永久死锁）", released)
        # 仍存在的 pending（10 分钟内、可能对应真实在途委托）仅告警，不自动释放，需人工核对券商侧。
        for p in self.store.list_pending():
            log.warning("[gateway] STALE pending order (within 10min, verify against broker): signal_id=%s "
                        "code=%s side=%s qty=%s",
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
                    # 连上即推一次账户资产（可用资金等），首尔侧即时展示
                    self.handler.on_account(self.broker.query_asset())
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
            "user_id": self.user_id,  # §P1-9 多账号隔离归属
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
            "user_id": self.user_id,  # §P1-9 多账号隔离归属
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
    gateway = None  # 类级注入，跨实例共享

    # 本机回环地址集合：/health 仅对它们开放
    _LOCAL_ADDRS = ("127.0.0.1", "::1", "::ffff:127.0.0.1")

    def _real_ip(self):
        """取对端真实 IP。

        安全注意：本网关是 Windows 执行机上的直连服务（首尔的下单/查询直连 :8789，
        不经过任何反向代理），因此**只信任 socket 对端地址**。旧实现优先取
        X-Forwarded-For 首跳——直连部署下攻击者伪造该头即可绕过 ALLOWED_IPS
        白名单（第二道防线失效，仅剩 token）。如未来引入反向代理，需以配置显式
        声明信任代理后才能启用 XFF 还原。
        """
        return self.client_address[0]

    def _ip_allowed(self, path):
        """按端点类型做 IP 访问控制，返回 (allowed, status, err)。

        - /health：本机回环（127.0.0.1 / ::1）或 ALLOWED_IPS 白名单（决策机首尔，
          其引擎以 /health 驱动熔断状态机）可访问，其余 403——陌生人探测不到健康状态；
        - 其它敏感端点：若配置了 ALLOWED_IPS 白名单，来源 IP 必须命中，否则 403。
        """
        # /health：回环或白名单（首尔引擎 probeHealth 驱动熔断，403 会被计为探测失败）
        if path == "/health":
            if self.client_address[0] in self._LOCAL_ADDRS:
                return True, 200, ""
            allowed = self.gateway.allowed_ips
            if allowed and self._real_ip() in allowed:
                return True, 200, ""
            return False, 403, "health endpoint is localhost-only"
        # 其它端点：若启用 IP 白名单则校验（空列表表示不限制，仅依赖 token）
        allowed = self.gateway.allowed_ips
        ip = self._real_ip()
        if allowed and ip not in allowed:
            log.warning("[gateway] rejected %s %s from non-allowed IP %s",
                        self.command, path, ip)
            return False, 403, "source IP not allowed"
        return True, 200, ""

    def _auth_ok(self):
        """Bearer token 双向鉴权（常量时间比较，防时序侧信道）。

        校验用 token 优先来自环境变量 QUANT_GATEWAY_TOKEN，未设置时回退配置文件明文。
        """
        auth = self.headers.get("Authorization", "")
        token = self.gateway.cfg.get("token", "")
        expected = "Bearer " + token
        # hmac.compare_digest 对字符串做常量时间比较，避免 == 的短路时序泄露
        return hmac.compare_digest(auth, expected)

    def _respond(self, status, payload):
        data = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _dispatch(self):
        # 统一分发入口：解析路径/读取 JSON 体 → IP 访问控制 → 鉴权 → 交由 Gateway.handle 处理
        parsed = urlparse(self.path)
        # 先做 IP 访问控制（健康检查与敏感端点分别处理）
        ok, status, err = self._ip_allowed(parsed.path)
        if not ok:
            return self._respond(status, {"ok": False, "err": err})
        length = int(self.headers.get("Content-Length") or 0)
        body = None
        if length > 0:
            # 有请求体时按长度读取并解析 JSON；解析失败返回 400
            raw = self.rfile.read(length)
            try:
                body = json.loads(raw.decode("utf-8"))
            except ValueError:
                return self._respond(400, {"ok": False, "err": "bad JSON body"})
        # /health 免鉴权（但已受 IP 限制）；其余端点需 Bearer token
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
    # 命令行入口：解析参数 → 装载配置 → 启动 HTTP/HTTPS 服务与网关后台线程
    ap = argparse.ArgumentParser(description="MiniQMT gateway (M2)")
    ap.add_argument("-c", "--config", default="", help="config JSON path")
    ap.add_argument("--listen", default="", help="override listen addr")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)

    # stderr 基线日志先配置（basicConfig 在 root 已有 handler 时是 no-op，
    # 因此文件 handler 必须加在它之后，保证双通道都生效）
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    # ── 文件日志（自管 + PID 隔离，根治两大生产缺陷）──
    # 缺陷一：watchdog 以外部重定向写 gateway.log（Windows 下 UTF-16 且句柄被
    # 假死旧实例持有），新实例启动后日志全部丢失——排障只见"最后一刻"残影。
    # 缺陷二：单文件无限增长。
    # 方案：进程内 RotatingFileHandler（UTF-8、5MB×3 轮转）写 gateway-<pid>.log，
    # 文件名含 PID 使新旧实例互不锁文件；启动时仅保留最近 10 个旧 PID 日志；
    # stderr 保留（watchdog/控制台仍可见）。可用 config 的 log_file 覆盖路径。
    try:
        _cfg_dir = os.path.dirname(os.path.abspath(args.config)) if args.config else os.path.dirname(os.path.abspath(__file__))
        _cfg_probe = {}
        try:
            with open(args.config, "r", encoding="utf-8") as _f:
                _cfg_probe = json.load(_f)
        except Exception:  # noqa: BLE001 — 配置缺失/未加载时用默认路径即可
            pass
        _log_file = _cfg_probe.get("log_file") or os.path.join(_cfg_dir, "gateway-%d.log" % os.getpid())
        from logging.handlers import RotatingFileHandler  # noqa: PLC0415
        _fh = RotatingFileHandler(_log_file, maxBytes=5 * 1024 * 1024, backupCount=3, encoding="utf-8")
        _fh.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(name)s: %(message)s"))
        logging.getLogger().addHandler(_fh)
        # 清理旧 PID 日志：按修改时间只保留最近 10 个 gateway-*.log
        import glob  # noqa: PLC0415
        _olds = sorted(glob.glob(os.path.join(_cfg_dir, "gateway-*.log")), key=os.path.getmtime, reverse=True)
        for _p in _olds[10:]:
            try:
                os.remove(_p)
            except OSError:  # noqa: PERF203 — 被占用的旧文件跳过，不影响启动
                pass
        logging.getLogger("qmt_gateway").info("[main] log file: %s", _log_file)
    except Exception as _e:  # noqa: BLE001 — 日志文件不可用时退回 stderr-only，网关必须能跑
        print("[gateway] file logging disabled: %s" % _e, file=sys.stderr)

    cfg = load_config(args.config)
    if args.listen:
        cfg["listen"] = args.listen

    # —— 绑定地址收敛（防误暴露到 0.0.0.0）——
    # 仅当显式指定 --listen 或 QUANT_GATEWAY_BIND 时才允许对外绑定；
    # 否则若配置监听 0.0.0.0，自动收敛到 127.0.0.1 仅本机可访问。
    bind_env = os.environ.get("QUANT_GATEWAY_BIND", "")
    if bind_env:
        cfg["listen"] = bind_env
    else:
        host = cfg["listen"].partition(":")[0]
        if host in ("0.0.0.0", ""):
            port = cfg["listen"].partition(":")[2] or "8789"
            cfg["listen"] = "127.0.0.1:" + port
            log.warning("[gateway] listen 原为 0.0.0.0，已自动收敛为 127.0.0.1 "
                        "（如需对外，请设置 QUANT_GATEWAY_BIND=0.0.0.0:8789）")

    # —— IP 白名单（敏感端点来源校验）——
    # 环境变量 ALLOWED_IPS 为空则不限制（仅依赖 token）；非空则严格校验。
    allowed_raw = os.environ.get("ALLOWED_IPS", "")
    allowed_ips = [x.strip() for x in allowed_raw.split(",") if x.strip()] if allowed_raw else []
    if allowed_ips:
        log.info("[gateway] IP 白名单已启用，允许来源：%s", ", ".join(allowed_ips))
    else:
        log.warning("[gateway] 未设置 ALLOWED_IPS，敏感端点仅依赖 token 防护（建议配置决策机出口 IP）")

    gw = Gateway(cfg)
    gw.allowed_ips = allowed_ips  # 注入白名单，供 _Handler 校验
    _Handler.gateway = gw

    host, _, port = cfg["listen"].partition(":")
    server = ThreadingHTTPServer((host, int(port)), _Handler)

    # —— 可选 TLS ——
    # 仅当两个证书环境变量均设置才启用 HTTPS；否则 HTTP（已收敛到本机）。
    cert = os.environ.get("QUANT_GATEWAY_TLS_CERT", "")
    key = os.environ.get("QUANT_GATEWAY_TLS_KEY", "")
    if cert and key:
        tls_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls_ctx.load_cert_chain(certfile=cert, keyfile=key)
        server.socket = tls_ctx.wrap_socket(server.socket, server_side=True)
        scheme = "https"
        log.info("[gateway] TLS 已启用（HTTPS）")
    else:
        scheme = "http"
        if allowed_ips:
            log.warning("[gateway] 未启用 TLS，敏感流量为明文（建议配置 QUANT_GATEWAY_TLS_CERT/KEY）")

    log.info("[gateway] listening on %s://%s (broker=%s account=%s report_url=%s)",
             scheme, cfg["listen"], cfg.get("broker"), cfg.get("account"), cfg.get("report_url") or "(none)")
    gw.start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("[gateway] shutting down")
        gw.stop()
        server.shutdown()


if __name__ == "__main__":
    main()
