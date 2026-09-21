#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.gateway — 东莞证券 MiniQMT 网关 REST 服务（AUTO_TRADING_PLAN M2）。

零第三方依赖（标准库 http.server）。接口与首尔侧 trading.QMTClient 契约一致：
  POST /order   {"signal_id","code","name","strategy","strategy_id","strategy_type","side",
                 "price_type","price","qty","amount","created_at","staleness_ms","prev_close","current_price"}
                → {"ok","order_id","err"}
  POST /cancel  {"order_id"}                                                    → {"ok","err"}
  GET  /state   → {"connected","account","positions","orders"}
  GET  /health  → {"ok","ts","broker","broker_connected"}   （免鉴权；broker_connected 反映通道真实状态）

§A1/A2（AUDIT_FULLSTACK_20260918）字段消费口径——/order 15 字段逐字段显式声明：
  消费：signal_id/code/side/price_type/price/qty/amount/created_at/strategy_type/strategy（broker 备注）；
  忽略：name/strategy_id/staleness_ms/prev_close/current_price（首尔侧专属闸的输入，网关不复算）。
  集合以 qmt_gateway/contract/order_fields.json 为 golden，Go 侧 internal/trading 契约测试与
  本文件 CONTRACT_CONSUMED_FIELDS/CONTRACT_IGNORED_FIELDS 三点校验，新增字段漏声明即双红。

Bearer token 双向鉴权。§G1 下单改为「claim 占位 → 下单 → settle 回填」三段式，
signal_id 原子幂等（并发重试/崩溃窗口均不重复真实下单）；§G2 空 signal_id 直接 400；
§G7 整手规则分板块（创业板/科创板最低 200 股、1 股递增；卖出允许零股清仓）；
§G8 _dispatch 顶层异常保护，任何 handler 异常返回 500 JSON 而非裸断连。
broker 由 config 选择（xt/mock）。回报经 outbox 后台线程推送（handler.py）。
§TZ（2026-09-22）：本文件所有落库/上报时间串统一 store._now_cn()（显式北京时区），
  根治裸 strftime 本地钟面贴假 +08:00（LOW 族）。
§REJECT（2026-09-22）：/dispatch/result 的 trade 回报在 trade_id 与 order_id 皆空时
  拒收 400 留痕（与 §F5 缺 order_id 拒 400 同口径；handler.on_trade 为第二道防线）。

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
from urllib.parse import urlparse, parse_qs

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# §TZ（2026-09-22 修复批）：网关内全部落库/上报时间串统一走 store._now_cn()
# （显式北京时区，见其 §TZ 说明）；禁止再用「本地 strftime + 硬编码 +08:00 后缀」的写法
# ——那会把非北京时区部署机的本地钟面贴上 +08:00 假偏移（LOW 族根治项）。
from store import Store, is_placeholder_order_id, json_default, _now_cn  # noqa: E402
from ids import Idempotency  # noqa: E402
from broker import build_brokers, XtBroker, QueuedBroker  # noqa: E402
from handler import ReportHandler, periodic_reconcile, is_active_trading_session  # noqa: E402
from quote_feed import QuoteFeed  # noqa: E402

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
    # §QMT-DUAL 双路径参数
    "failover_enable": False,           # 自动翻转开关（xt 断连 N 秒→queued，交易时段）
    "failover_sec": 60,                 # xt 断连超过该秒数触发自动翻转
    "bridge_heartbeat_timeout_sec": 15,  # 桥心跳新鲜窗口（超时视为离线）
    # §M16（2026-09-22）：inflight 派发项超时收割阈值（秒，0=关闭）——桥回报丢失/重启后
    # dispatch 行不再永挂；回收后 order 类回写「已废」（判废不重排，防重复真实下单）
    "dispatch_inflight_reap_sec": 1800,
    # §A1（AUDIT_FULLSTACK_20260918）网关侧独立风控（与首尔 risk.Gate 同语义，0/空=闸关闭）
    # §ENH-5 批E 只读 L1 行情通道（xtdata get_full_tick 轮询；与交易链路完全隔离）
    "quote_feed": True,             # feed 总开关（xtdata 缺失环境自动静默停用）
    "feed_poll_sec": 3,             # 轮询间隔（秒，下限 1s）
    "max_order_amount": 0,              # 单笔金额绝对帽（元，买卖双向；amount 缺失回退 qty×price）
    "allowed_strategies": [],           # 战法白名单（非空时买入单 strategy_type 必须命中）
    "strict_fields": False,             # 白名单启用时缺 strategy_type 是否 fail-close 拒单
}

# §A2 契约字段显式声明集（与 contract/order_fields.json、Go OrderRequest 三点校验）：
# consumed = 网关/ broker 链路真实读取的 /order 字段；ignored = 首尔侧专属闸输入，网关不复算。
# 新增 OrderRequest 字段必须同时进本集与 golden，否则 pytest + go test 双红。
CONTRACT_CONSUMED_FIELDS = {
    "signal_id", "code", "side", "price_type", "price", "qty",
    "amount", "created_at", "strategy_type", "strategy",
}
CONTRACT_IGNORED_FIELDS = {"name", "strategy_id", "staleness_ms", "prev_close", "current_price"}


def load_config(path):
    """加载网关配置：默认配置 + 用户 JSON 文件覆盖 + 环境变量 token 注入。

    优先级：环境变量 QUANT_GATEWAY_TOKEN > 配置文件 token；report_token 同理
    （QUANT_GATEWAY_REPORT_TOKEN，缺省回退 token）。返回合并后的配置字典。
    English: loads gateway config — defaults merged with the user JSON file, then
    environment-variable tokens (QUANT_GATEWAY_TOKEN / QUANT_GATEWAY_REPORT_TOKEN) override file values.
    """
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


# 网关核心：组装本地账本 / 幂等守卫 / 交易通道 / 回报处理器，并驱动 HTTP 服务与行情全推（QuoteFeed）。
class Gateway:
    def __init__(self, cfg):
        """构造网关实例：组装本地账本 / 幂等守卫 / 交易通道 / 回报处理器四大组件。

        §QMT-DUAL 双路径：broker 非 mock 时同时装配 XtBroker（xt 主）与 QueuedBroker
        （queued 兜底），任一时刻仅 active 一个接单；active 由 config.broker 决定、
        可经 /admin/broker 切换、交易时段 xt 断连可自动翻转。
        :param cfg: 已合并的配置字典（含 db/broker/report_url/report_token/user_id 等）。
        :ivar _stop: 停止信号 Event，供各后台线程（重连/对账/回报发送）优雅退出。
        """
        self.cfg = cfg
        # 组装四大核心组件：本地账本 / 幂等守卫 / 交易通道 / 回报处理器
        self.store = Store(cfg["db"])
        self.ids = Idempotency(self.store)
        self.user_id = cfg.get("user_id", "")  # §P1-9 多账号归属：落库/上报统一带此 ID
        self.handler = ReportHandler(
            self.store, cfg.get("report_url", ""), cfg.get("report_token", ""), self.user_id,
        )
        # §QMT-DUAL 双通道装配：mock 单通道；xt/queued 双通道且 active 原子切换
        self.active_broker, self.brokers, self.active_key = build_brokers(cfg, self.store)
        for _b in self.brokers.values():
            _b.handler = self.handler
        self._stop = threading.Event()
        self._reconcile_thread = None
        self._broker_thread = None
        self._failover_thread = None
        # §QMT-F16 文件桥 sidecar：QMT 模型沙箱无法联网（缺 C 扩展 socket）→
        # 桥以 JSONL 文件上报事件（bridge_report.jsonl 追加行），由本线程读文件
        # 并复用 _do_dispatch_result 语义（心跳/快照/派发回报/推量仔零改动）。
        self._file_bridge_thread = None
        # §M16：inflight 派发项超时收割线程（启动先清一次 + 周期巡检）
        self._dispatch_reap_thread = None
        # §ENH-5 批E：只读 L1 行情 feed（独立线程/独立异常域；断连只影响 feed_connected 观察字段）
        self.feed = QuoteFeed(cfg)
        # §主备反转：queued（桥）最近一次心跳确认时间（failover 判定用）。
        # §P2-12（2026-09-15）：删除死字段 _xt_last_connected——2026-09-11 主备语义反转为
        # queued 主 / xt 备后，failover 判定只读 _queued_last_connected，旧字段仅剩误导读与
        # 陈旧测试引用（test_queued_broker 曾据此整体失效，见 P2-12 修复）。
        self._queued_last_connected = 0.0
        # 来源 IP 白名单（由 main 从环境变量 ALLOWED_IPS 注入；空列表表示不做 IP 限制，仅依赖 token）
        self.allowed_ips = []

    def switch_broker(self, key, reason=""):
        """原子切换 active 通道（xt/queued/mock）。返回 (ok:bool, err:str)。"""
        if key not in self.brokers:
            return False, "unknown broker: %s" % key
        if key == self.active_key:
            return True, ""
        old = self.active_key
        self.active_key = key
        self.active_broker = self.brokers[key]
        log.warning("[gateway] broker switched %s -> %s (%s)", old, key, reason or "manual")
        # 推送 broker 变更事件到量仔（/api/qmt/report 未知 type 会被忽略，仅观察用）
        try:
            self.handler._push({"type": "broker", "broker": key, "from": old,
                                "at": _now_cn()})
        except Exception:  # noqa: BLE001
            log.exception("[gateway] push broker-change event failed")
        return True, ""

    def start(self):
        """启动网关：清理崩溃残留 pending → 启动回报发送线程 → 后台连接 broker → 可选对账线程。

        启动即先释放超过 10 分钟的超时 pending 占位（避免崩溃后 signal_id 永久死锁），
        对仍在 10 分钟内的在途委托仅告警、不自动释放，交由人工核对券商侧。
        English: boots the gateway — cleans stale pending placeholders, starts the report
        sender, connects the broker in the background, and optionally starts reconciliation.
        """
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
        # §M16：dispatch 表与 orders 同批做启动清理——上次进程存活期内被桥取走（inflight）
        # 但回报丢失/桥重启未结算的超龄派发项，启动先收割一次，随后周期巡检兜底。
        self._reap_dispatch_inflight("启动")
        if int(self.cfg.get("dispatch_inflight_reap_sec", 1800) or 0) > 0:
            self._dispatch_reap_thread = threading.Thread(
                target=self._dispatch_reap_loop, daemon=True, name="dispatch-reap")
            self._dispatch_reap_thread.start()
        # 回报 outbox 发送线程
        self.handler.start_sender()
        # 连接 broker（失败则后台重试，不阻塞 HTTP 起服）
        self._broker_thread = threading.Thread(target=self._connect_loop, daemon=True)
        self._broker_thread.start()
        # §QMT-DUAL 自动翻转线程（xt 断连 N 秒且交易时段 → queued；切回仅手动）
        self._failover_thread = threading.Thread(target=self._failover_loop, daemon=True)
        self._failover_thread.start()
        # §QMT-F16 文件桥 sidecar（哑文件读事件行 → 复用 dispatch/result 语义）
        self._file_bridge_thread = threading.Thread(
            target=self._file_bridge_loop, daemon=True, name="file-bridge")
        self._file_bridge_thread.start()
        # §ENH-5 批E：L1 行情 feed 线程（enable 关或 xtdata 缺失时内部静默降级）
        self.feed.start()
        if self.cfg.get("reconcile_sec", 0) > 0:
            # active 通道运行时可变（自动翻转），对账源用 callable 取当前 active
            self._reconcile_thread = threading.Thread(
                target=periodic_reconcile, args=(self.handler, self._active_broker_fn),
                kwargs={"interval_sec": self.cfg.get("reconcile_sec", 60), "stop": self._stop},
                daemon=True,
            )
            self._reconcile_thread.start()

    def _active_broker_fn(self):
        """对账/重连循环取当前 active 通道（运行时切换后立即生效）。"""
        return self.active_broker

    def _file_bridge_path(self):
        """§QMT-F16 桥上报文件路径：与网关同目录 bridge_report.jsonl（沙箱可写）。"""
        base_dir = os.path.dirname(os.path.abspath(__file__))
        return os.path.join(self.cfg.get("bridge_report_dir") or base_dir, "bridge_report.jsonl")

    def _file_bridge_cmd_path(self):
        """§QMT-F17 命令下发文件：网关侧每轮把 pending 派发行推送成该文件（桥执行）。"""
        base_dir = os.path.dirname(os.path.abspath(__file__))
        return os.path.join(self.cfg.get("bridge_report_dir") or base_dir, "bridge_cmd.json")

    def _file_bridge_push_pending(self):
        """§QMT-F16 file 桥命令下发：有 pending 派发项 → 原子写 bridge_cmd.json。
        所有 pending 行由桥逐条执行并回报，gateway _apply_* 根据 seq 结算→done。
        实现只对「有 pending」的轮次写文件（无 pending 不打扰桥）。"""
        try:
            pending = self.store.dispatch_pending(limit=50)
        except Exception:  # noqa: BLE001
            log.exception("[file-bridge] dispatch_pending failed")
            return
        cmd_path = self._file_bridge_cmd_path()
        if not pending:
            # FIX 2026-09-14 drill-3：已结算指令留在 cmd 文件里 = 桥每次重启重放
            # 老单（seq:7 重启即重投 3 次，幸而柜台以"可用数量不足"拒绝）。
            # 空推时把文件同步为「仅 inflight」：done 清掉，桥掉线期间未消费的
            # 单保留（清了=丢单）。English: reconcile cmd file to inflight-only.
            try:
                inflight = self.store.dispatch_inflight(limit=50)
            except Exception:  # noqa: BLE001
                # 取 inflight 失败就整轮跳过：宁可不重写命令文件，也不能写空——
                # 写空 = 桥侧丢掉未消费的在途单（丢单比留一份旧命令文件更危险）
                return
            self._file_bridge_reconcile_cmds(cmd_path, inflight)
            return
        payload = {
            "ts": time.time(),
            "cmds": [self._cmd_from_dispatch(p) for p in pending],
        }
        tmp = cmd_path + ".tmp"
        try:
            body = json.dumps(payload, ensure_ascii=False, default=json_default)
            with open(tmp, "w", encoding="utf-8") as f:
                f.write(body)
            os.replace(tmp, cmd_path)
        except Exception:  # noqa: BLE001
            log.exception("[file-bridge] push pending to cmd file failed")
        else:
            log.info("[file-bridge] pushed %d pending cmd(s) to %s", len(pending), cmd_path)

    def _cmd_from_dispatch(self, p):
        """派发 DB 行 → 文件桥命令体（push 与 reconcile 共用，口径一致）。"""
        return {
            "seq": p.get("seq"), "kind": p.get("kind"),
            "signal_id": p.get("signal_id"), "code": p.get("code"),
            "side": p.get("side"), "price_type": p.get("price_type"),
            "price": p.get("price"), "qty": p.get("qty"),
            "order_id": p.get("order_id"),
        }

    def _file_bridge_reconcile_cmds(self, cmd_path, inflight):
        """无 pending 轮次：cmd 文件应等于「当前 inflight 清单」。内容 seq 集合已
        一致则不写（避免 0.5s 轮询空转）；done 残留或多余条目 → 原子重写。"""
        want = [self._cmd_from_dispatch(r) for r in (inflight or [])]
        try:
            with open(cmd_path, "rb") as f:
                cur = json.loads(f.read().decode("utf-8", errors="replace") or "{}")
            cur_seqs = [str(c.get("seq", "")) for c in (cur.get("cmds") or [])]
            if cur_seqs == [str(c["seq"]) for c in want]:
                return
        except OSError:
            if not want:
                return  # 文件不存在且 inflight 为空：已经是目标态
        except Exception:  # noqa: BLE001 — 坏文件照重写
            pass
        tmp = cmd_path + ".tmp"
        try:
            body = json.dumps({"ts": time.time(), "cmds": want},
                              ensure_ascii=False, default=json_default)
            with open(tmp, "w", encoding="utf-8") as f:
                f.write(body)
            os.replace(tmp, cmd_path)
            log.info("[file-bridge] reconciled cmd file to %d inflight row(s)", len(want))
        except Exception:  # noqa: BLE001
            log.exception("[file-bridge] reconcile cmd file failed")

    def _file_bridge_loop(self):
        """§QMT-F16 文件桥 sidecar：读桥的 JSONL 上报 + 推 pending 命令文件，
        回报语义与 HTTP PATH 一字不差。上报文件被轮转缩小则回到 0 偏移。"""
        report_path = self._file_bridge_path()
        # FIX 2026-09-14 drill-3: restart used to re-read the report file from
        # offset 0 -- a 3.6MB replay storm (thousands of stale positions/account
        # applies, 12+ min of blocking, and stale heartbeats marking the bridge
        # "connected" while it was dead). Persist the offset; on first run (or
        # truncation) resume from EOF instead of replaying history. Events are
        # live-flow only; the reconnect hook re-pushes a full reconcile anyway.
        try:
            _eof = os.path.getsize(report_path)
        except OSError:
            _eof = 0
        saved = self.store.bridge_snapshot_get("report_pos", None)
        try:
            # 首次运行：只回放尾部 64KB（覆盖"离线期间到达的行"，跳过 3.6MB 历史风暴；
            # 结算/成交回报幂等，尾部重复无害）。English: on first run replay only
            # the tail window -- full history replay caused a 12-min apply storm.
            pos = int(saved) if saved is not None else max(0, _eof - 65536)
        except (TypeError, ValueError):
            pos = max(0, _eof - 65536)
        if pos > _eof:
            pos = _eof
        log.info("[file-bridge] watching %s pos=%d size=%d", report_path, pos, _eof)
        # §2026-09-11 主接线时延：0.5s 轮询（信号→下单→回传闭环秒级；本地文件读开销可忽略）
        while not self._stop.is_set():
            if self._stop.wait(0.5):
                break
            # ① 扫上报
            try:
                size = os.path.getsize(report_path)
            except OSError:
                pos = 0
                size = 0
            if size < pos:  # 文件被重写（轮转），从头再读
                pos = 0
            if size != pos:
                try:
                    with open(report_path, "rb") as f:
                        f.seek(pos)
                        block = f.read().decode("utf-8", errors="replace")
                        pos = f.tell()
                except OSError as e:  # noqa: BLE001 — 文件被占用等瞬态，跳过本轮
                    log.warning("[file-bridge] read failed: %s", e)
                    block = ""
                for line in block.splitlines():
                    text = line.strip()
                    if not text:
                        continue
                    try:
                        body = json.loads(text)
                    except Exception as e:  # noqa: BLE001 — 残行不中断流
                        log.warning("[file-bridge] bad line skipped: %s (%s)", text[:80], e)
                        continue
                    try:
                        code, resp = self._do_dispatch_result(body)
                        log.debug("[file-bridge] %s -> %s", body.get("type"), code)
                    except Exception:  # noqa: BLE001 — 单事件失败不阻断后续行
                        log.exception("[file-bridge] apply %s failed", body.get("type"))
                try:
                    self.store.bridge_snapshot_set("report_pos", pos)
                except Exception:  # noqa: BLE001 — 偏移写失败下轮重放，幂等无害
                    log.exception("[file-bridge] persist report pos failed")
            # ② 推命令（无 pending 不打扰桥）
            self._file_bridge_push_pending()

    def _reap_dispatch_inflight(self, where=""):
        """§M16：收割超龄 inflight 派发项并做对账降级回写。

        收割动作本身在 store（dispatch_reap_stale_inflight：inflight→done 判废留痕，
        不回 pending——桥可能已真实下单，重排=重复下单）；此处负责把结果透传到业务面：
          - kind=order 且有 signal_id：按 _apply_order_result 失败分支同语义回写 orders
            「已废」（带收割拒因），量仔侧委托不再永挂「已报」，与 §R4-1 撤单闭环对称；
          - kind=cancel/diag：只结算派发列并告警留痕——撤单超时不代表底层委托状态变化，
            不代为推进 orders；桥若迟到回报，dispatch_set_result 仍可覆盖 result 幂等落 done。
        返回收割条数；配置阈值<=0 表示关闭（不收割）。
        English: §M16 — reaps stale inflight dispatch rows and down-settles order rows as
        rejected so nothing hangs in inflight after lost bridge reports.
        """
        reap_sec = int(self.cfg.get("dispatch_inflight_reap_sec", 1800) or 0)
        if reap_sec <= 0:
            return 0
        try:
            reason = "inflight 超时（>%ds）未回报，网关收割（§M16%s）" % (
                reap_sec, ("，" + where) if where else "")
            rows = self.store.dispatch_reap_stale_inflight(reap_sec, reason=reason)
        except Exception:  # noqa: BLE001 — 收割失败不影响主循环，下轮重试
            log.exception("[gateway] §M16 dispatch inflight 收割失败")
            return 0
        for r in rows:
            log.warning("[gateway] §M16 收割超龄 inflight 派发项 seq=%s kind=%s signal=%s code=%s",
                        r.get("seq"), r.get("kind"), r.get("signal_id"), r.get("code"))
            if r.get("kind") == "order" and r.get("signal_id"):
                ts = _now_cn()  # §TZ
                try:
                    self.handler.on_order({
                        "order_id": r.get("order_id") or r.get("seq"), "signal_id": r.get("signal_id"),
                        "code": r.get("code"), "side": r.get("side"), "status": "已废", "reason": reason,
                        "price": float(r.get("price", 0) or 0), "qty": int(r.get("qty", 0) or 0),
                        "created_at": r.get("created_at") or ts, "at": ts,
                    })
                except Exception:  # noqa: BLE001 — 回写失败已有 dispatch 判废留痕，不阻断其余收割
                    log.exception("[gateway] §M16 收割回写已废失败 seq=%s", r.get("seq"))
        return len(rows)

    def _dispatch_reap_loop(self):
        """§M16 周期收割：每 60s 巡检一次 inflight 超龄派发项（阈值见配置，默认 30min）。"""
        while not self._stop.is_set():
            if self._stop.wait(60):
                break
            try:
                self._reap_dispatch_inflight()
            except Exception:  # noqa: BLE001 — 巡检线程永不因异常退出
                log.exception("[gateway] §M16 dispatch reap loop error")

    def stop(self):
        """优雅停止：置停止信号并停掉回报发送线程（重连/对账线程随之退出）。
        §UAT-D8：join 各后台线程（短超时）——旧实现只置事件即返回，pytest teardown 时
        守护线程仍在跑，解释器退出阶段刷 'Exception ignored in ... thread' 告警，
        掩盖真实失败且污染 CI 日志。English: §UAT-D8 — join the background threads with a
        short timeout so pytest teardown no longer leaks live daemon threads."""
        self._stop.set()
        self.handler.stop_sender()
        self.feed.stop()  # §ENH-5：feed 线程同样登记回收，防 pytest teardown 泄漏守护线程
        for th in (getattr(self, "_broker_thread", None),
                   getattr(self, "_failover_thread", None),
                   getattr(self, "_file_bridge_thread", None),
                   getattr(self, "_dispatch_reap_thread", None),  # §M16 inflight 收割巡检
                   getattr(self, "_reconcile_thread", None)):
            if th is not None and th.is_alive():
                th.join(timeout=2)

    def _connect_loop(self):
        """后台线程：主动重连 XtBroker；任一 active 通道从断开转连接时推一次对账与资产。

        QueuedBroker 无需主动连接（在线由桥心跳驱动），其持仓/资产快照由桥经
        /dispatch/result 周期上报。English: reconnect thread — retries XtBroker, and on a
        disconnected→connected transition of the ACTIVE channel pushes a full position
        reconciliation plus account asset so the decision side aligns fast.
        """
        prev = {k: b.is_connected() for k, b in self.brokers.items()}
        xt_fail_streak = 0
        while not self._stop.is_set():
            # 主动重连：外部 xtquant 与 mock 需要主动 connect；queued 由桥心跳驱动
            # （连接动作放在 sleep 之前，保证进程启动后立即触发，缩短首单等待）
            # §2026-09-11 主备反转止血：queued（桥）在线时 xt 只是备胎，不再空转
            # 重连——每次 XtQuantTrader 连接尝试都会在客户端共享内存占一个 writer 槽，
            # 桥正常时的无意义重试会耗尽 writer（WaitingFreeWriter exceeded）并拖崩
            # QMT 客户端（生产实录：网关每秒重连 → 客户端闪退）。
            for key, b in self.brokers.items():
                if isinstance(b, QueuedBroker):
                    continue
                if (self.active_key == "queued"
                        and self.brokers.get("queued") is not None
                        and self.brokers["queued"].is_connected()):
                    continue
                if not b.is_connected():
                    try:
                        b.connect()
                        log.info("[gateway] broker %s connected", key)
                    except Exception as e:  # noqa: BLE001
                        # 连接失败：失败计数递增后按指数退避等待（封顶 60s），
                        # continue 跳过本轮其余通道的重连，避免每秒猛戳 QMT 客户端
                        xt_fail_streak += 1
                        backoff = min(60, int(self.cfg.get("reconnect_sec", 5)) * (2 ** min(xt_fail_streak, 4)))
                        log.warning("[gateway] %s connect failed: %s (backoff %ss, streak=%d)",
                                    key, e, backoff, xt_fail_streak)
                        self._stop.wait(backoff)
                        continue
            for key, b in self.brokers.items():
                conn = b.is_connected()
                # 仅当 active 通道发生「断开→连接」转换时推一次全量对账 + 资产
                if conn and not prev[key] and key == self.active_key:
                    # §UAT-D8（2026-09-16）：对账查询整体包 try——一次 query_positions/query_asset
                    # 抛异常（xtquant 抖动、通道半初始化）旧实现会**杀死整个重连/对账线程**，
                    # 此后网关不再自动重连、不再推对账，决策侧静默失去实盘真相。降级为记日志+下一轮重试。
                    # English: §UAT-D8 — guard the reconnect-transition reconcile so a transient
                    # query error can't kill the whole loop thread (silent loss of auto-reconnect).
                    try:
                        self.handler.disconnected = False
                        poss = b.query_positions()
                        if poss:
                            self.handler.on_positions(poss)
                        else:
                            log.warning("[gateway] post-connect empty positions snapshot — "
                                        "reconcile skipped (account data may not be synced yet)")
                        self.handler.on_account(b.query_asset())
                    except Exception:  # noqa: BLE001
                        log.exception("[gateway] post-connect reconcile failed (will retry on next transition)")
                prev[key] = conn
            # §UAT-D8：可被停止信号即时打断（time.sleep 会让 stop() 的 join 白等满周期）
            if self._stop.wait(1):
                break

    def _failover_loop(self):
        """§QMT-DUAL 主备自动翻转循环（2026-09-11 语义反转：queued 主 / xt 备）：
        交易时段 active=queued 且桥心跳断 ≥ failover_sec 且 xt 在线 → 翻 xt 顶班；
        active=xt 且桥恢复新鲜 → 自动回切 queued。每 5s 轮询，异常不阻断。"""
        while not self._stop.is_set():
            if self._stop.wait(5):  # §UAT-D8 可中断等待，替代 time.sleep(5)
                break
            try:
                self._maybe_failover()
            except Exception:  # noqa: BLE001
                log.exception("[gateway] failover loop error")

    def _maybe_failover(self):
        """自动翻转判定（§QMT-DUAL 主备语义 2026-09-11 反转）。

        主接线 = queued（QMT 模型沙箱文件桥——券商 2026-09 下旬关 miniQMT 后唯一存活通道）；
        备 = xt（miniQMT 独立交易，可跑但随时可能被停）。规则：
          - active=queued 且心跳断 ≥ failover_sec、xt 可用 → 翻 xt 顶班（保下单回路不中断）；
          - active=xt 且桥心跳恢复新鲜 → 自动回切 queued（主接线优先归位）。
        非交易时段断连属预期（客户端被 qmtctl 杀），不翻转。
        """
        if not self.cfg.get("failover_enable", False):
            return
        if not is_active_trading_session():
            return  # 非交易时段断连属预期，不翻转
        xt = self.brokers.get("xt")
        queued = self.brokers.get("queued")
        if xt is None or queued is None:
            return
        now = time.time()
        if self.active_key == "queued":
            if queued.is_connected():
                self._queued_last_connected = now
                return
            if self._queued_last_connected == 0.0:
                self._queued_last_connected = now  # 启动后首次探测，先给观察窗口
                return
            if now - self._queued_last_connected < int(self.cfg.get("failover_sec", 60)):
                return
            if xt.is_connected():
                self.switch_broker("xt", reason="auto-failover: bridge heartbeat stale %.0fs" %
                                   (now - self._queued_last_connected))
            return
        # active == xt：桥恢复即回切主接线
        if queued.is_connected():
            self.switch_broker("queued", reason="auto-failback: bridge online")

    def handle(self, method, path, body, request_handler):
        """路由分发。返回 (status, payload_dict)。

        §QMT-DUAL 新增 /dispatch/*（桥取单/回报，Bearer 鉴权）与 /admin/broker
        （手动切换 active 通道）。下单/撤单/状态均路由到当前 active 通道。
        """
        if path == "/health":
            # ok=进程活着；broker=active 通道名；broker_connected=active 通道真实状态；
            # xt_connected/queued_connected 双状态供兜底可观测（§QMT-DUAL）
            return 200, self._health_payload()
        if path == "/dispatch/pending" and method == "GET":
            return self._do_dispatch_pending()
        if path == "/settlement" and method == "GET":
            # §P0-1a（2026-09-15）券商交割单三方对账的网关权威源——此端点此前**根本不存在**
            # （Go 侧 FetchSettlement 一直在调它，真实网关与 mock 同样 404，README §U-2 宣称的
            # 日终四方对账从未真正生效）。放在连接门之前：断线时也返回 connected=false 让
            # Go 侧按"交割单不可信跳过"处理，而不是把 503 当对账失败反复告警。
            return self._do_settlement(request_handler)
        if path == "/dispatch/result" and method == "POST":
            return self._do_dispatch_result(body)
        if path == "/dispatch/enqueue" and method == "POST":
            return self._do_dispatch_enqueue(body)
        if path == "/admin/broker" and method == "POST":
            return self._do_admin_broker(body)
        if path == "/admin/status" and method == "GET":
            return self._do_admin_status()
        if path.startswith("/quotes") and method == "GET":
            # §ENH-5 批E：只读行情通道放在 broker 连接闸之前——xtdata 与 xttrader 是
            # 两条独立通道，交易断连时行情应照常可查（行情可用性绝不与交易熔断互相污染）
            return self._do_quotes(request_handler)
        if not self.active_broker.is_connected():
            return 503, {"ok": False, "err": "broker not connected"}
        if path == "/order" and method == "POST":
            return self._do_order(body)
        if path == "/cancel" and method == "POST":
            return self._do_cancel(body)
        if path == "/state" and method == "GET":
            return self._do_state()
        return 404, {"ok": False, "err": "not found"}

    def _do_quotes(self, request_handler=None):
        """处理 GET /quotes?codes=600519.SH,600000.SH：Level-1 tick 只读查询（§ENH-5 批E）。

        codes 必填（Go 侧 qmt_feed 每 1~3s 带全监控池来询）；返回体里 ticks 只含
        命中的代码——feed 未接通（xtdata 缺失/刚启动）时为空 dict + 200，
        Go 侧按"无命中不注入"处理，新浪链照常兜底。
        English: read-only L1 tick query; empty ticks + 200 when the feed is not connected —
        the Go side simply skips injection.
        """
        query = ""
        if request_handler is not None:
            query = urlparse(getattr(request_handler, "path", "") or "").query
        params = parse_qs(query or "")
        raw_codes = (params.get("codes") or [""])[0]
        codes = [c.strip() for c in raw_codes.split(",") if c.strip()]
        if not codes:
            return 400, {"ok": False, "err": "codes required (comma separated, e.g. 600519.SH)"}
        if not self.feed.enable:
            return 200, {"ok": True, "ticks": {}, "feed_connected": False,
                         "err": "quote feed disabled by config"}
        ticks = self.feed.snapshot(codes)
        return 200, {
            "ok": True,
            "ticks": ticks,
            "feed_connected": self.feed.is_connected(),
            "feed_age_sec": self.feed.age_sec(),
        }

    def _health_payload(self):
        """组装 /health 响应：active 通道状态 + 双通道（xt/queued）状态。"""
        broker_connected = self.active_broker.is_connected()
        payload = {
            "ok": True,
            "ts": _now_cn(),
            "broker": self.active_key,
            "broker_connected": bool(broker_connected),
            "broker_mode": self.active_key,  # 与 Go 侧解析字段兼容的别名
        }
        if "xt" in self.brokers:
            payload["xt_connected"] = bool(self.brokers["xt"].is_connected())
        if "queued" in self.brokers:
            payload["queued_connected"] = bool(self.brokers["queued"].is_connected())
        # §ENH-5：行情 feed 状态仅作观察字段——Go 侧 Health() 只解析 ok/broker_connected，
        # xtdata 断连绝不参与交易熔断判定（计划铁律：行情面不得污染 §GAP2-W1 fail-closed 面）
        payload["feed_connected"] = self.feed.is_connected()
        payload["feed_age_sec"] = self.feed.age_sec()
        return payload

    def _do_dispatch_pending(self):
        """桥取单（GET /dispatch/pending）：原子取 pending 项并标记 inflight。"""
        items = self.store.dispatch_pending(limit=50)
        return 200, {"ok": True, "items": items}

    def _do_dispatch_enqueue(self, body):
        """运维注入派发项（当前仅 kind=diag：让桥做一次交易明细原始 dump）。
        English: ops-only enqueue for diagnostic commands (bridge raw trade-detail dump)."""
        req = body or {}
        kind = str(req.get("kind", "") or "")
        if kind != "diag":
            return 400, {"ok": False, "err": "kind must be diag"}
        seq = self.store.dispatch_enqueue_diag(req.get("signal_id", ""))
        return 200, {"ok": True, "seq": seq}

    def _do_dispatch_result(self, body):
        """桥回报（POST /dispatch/result）：结算派发项 + 按现有协议推量仔。

        事件类型：order_result（下单结果）/ cancel_result / trade（成交）/
        positions（持仓快照）/ account（资产快照）/ heartbeat（心跳）。
        English: applies a bridge report — settles the dispatch row and routes the event
        through the existing handler protocol to the decision side.
        """
        # 按事件类型分发：快照类先落 bridge_snapshot（供启动期回放），事件类走 _apply_*。
        # 未知类型必须 400 而不是静默吞掉——桥升级新增事件时这里要能被发现。
        req = body or {}
        etype = req.get("type", "")
        try:
            if etype == "heartbeat":
                self.store.bridge_heartbeat()
                return 200, {"ok": True, "err": ""}
            if etype == "positions":
                poss = req.get("positions") or []
                self.store.bridge_snapshot_set("positions", poss)
                self.handler.on_positions(poss)
                return 200, {"ok": True, "err": ""}
            if etype == "account":
                asset = req.get("asset")
                if asset:
                    self.store.bridge_snapshot_set("asset", asset)
                    self.handler.on_account(asset)
                return 200, {"ok": True, "err": ""}
            if etype == "order_result":
                return self._apply_order_result(req)
            if etype == "cancel_result":
                return self._apply_cancel_result(req)
            if etype == "trade":
                return self._apply_trade(req)
            if etype == "diag":
                seq = str(req.get("seq", "") or "")
                self.store.dispatch_set_result(seq, {"ok": True, "diag": req.get("dump")})
                log.info("[gateway] diag report seq=%s dump=%s", seq,
                         json.dumps(req.get("dump"), ensure_ascii=False, default=json_default)[:4000])
                return 200, {"ok": True, "err": ""}
            return 400, {"ok": False, "err": "unknown dispatch result type: %s" % etype}
        except Exception as e:  # noqa: BLE001
            log.exception("[gateway] dispatch result failed: %s", etype)
            return 500, {"ok": False, "err": "dispatch result error: %s" % e}

    def _apply_order_result(self, req):
        """下单结果回报：结算派发项；ok→已报（回填交易所委托号），失败→已废（带拒因）。"""
        # seq 是派发项主键：桥回执只带 seq + 结果，方向/代码等语义字段从派发行取（权威在本端）。
        seq = str(req.get("seq", "") or "")
        ok = bool(req.get("ok"))
        order_id = str(req.get("order_id", "") or "")
        err = str(req.get("err", "") or "")
        row = self.store.dispatch_set_result(seq, {"ok": ok, "order_id": order_id, "err": err})
        if row is None:
            return 404, {"ok": False, "err": "unknown seq: %s" % seq}
        signal_id = row.get("signal_id", "")
        if not signal_id:
            log.warning("[gateway] order_result without signal_id: seq=%s", seq)
            return 200, {"ok": True, "err": ""}
        ts = _now_cn()  # §TZ
        if ok:
            self.handler.on_order({
                "order_id": order_id or seq, "signal_id": signal_id, "code": row.get("code"),
                "side": row.get("side"), "status": "已报",
                "price": float(row.get("price", 0) or 0), "qty": int(row.get("qty", 0) or 0),
                "created_at": row.get("created_at") or ts, "at": ts,
            })
        else:
            self.handler.on_order({
                "order_id": seq, "signal_id": signal_id, "code": row.get("code"),
                "side": row.get("side"), "status": "已废", "reason": err,
                "price": float(row.get("price", 0) or 0), "qty": int(row.get("qty", 0) or 0),
                "created_at": row.get("created_at") or ts, "at": ts,
            })
        return 200, {"ok": True, "err": ""}

    def _apply_cancel_result(self, req):
        """撤单结果回报：ok→已撤；失败→保持已报并透出原因。"""
        seq = str(req.get("seq", "") or "")
        ok = bool(req.get("ok"))
        err = str(req.get("err", "") or "")
        row = self.store.dispatch_set_result(seq, {"ok": ok, "err": err})
        if row is None:
            return 404, {"ok": False, "err": "unknown seq: %s" % seq}
        signal_id = row.get("signal_id", "")
        if not signal_id:
            return 200, {"ok": True, "err": ""}
        ts = _now_cn()  # §TZ
        self.handler.on_order({
            "order_id": row.get("order_id") or seq, "signal_id": signal_id, "code": row.get("code"),
            "side": row.get("side"), "status": "已撤" if ok else "已报",
            "price": 0.0, "qty": 0, "reason": err,
            "created_at": row.get("created_at") or ts, "at": ts,
        })
        return 200, {"ok": True, "err": ""}

    def _apply_trade(self, req):
        """成交回报：走 handler.on_trade（落库去重 + 推送量仔），归因缺失用派发项回填。

        成交隐含委托状态推进（部成/已成），与 mock/xt 回调口径一致——桥回报不含委托
        状态事件，量仔侧需据此感知订单终结态。

        §P0 方向权威化（2026-09-18 买入卖出不分事故）：凡本端派发过的单，其「方向」以派发项
        （dispatch.side，等于我们下单时请求的方向）为**唯一权威**，桥/柜台 DEAL 行携带的方向
        推断一律不得覆盖。理由：桥侧方向只能从柜台对象字段反推（m_nDirection/m_nOffsetFlag/
        m_nOrderType 的枚举空间跨券商构建不一致，本项目已两次踩坑：2026-08-31 order_type 判反、
        2026-09-14 DEAL 方向补丁），而派发项是我们自己写下的物理事实——零推断、零歧义。
        旧实现用 `setdefault("side", …)`：键已存在（桥行恒带 side）即不覆盖 —— 桥的误判方向
        会原样落进 fills，把一笔真实卖出记成买入（持仓成本/已实现盈亏/胜率全线污染）。
        English: §P0 — for any order this gateway actually dispatched, the dispatch row's side is
        authoritative and the bridge's inferred DEAL direction must never override it. The old
        `setdefault` was a no-op whenever the bridge row already carried a (possibly wrong) side.
        """
        # 归因回填 + 派发项定位：seq → 交易所委托号 → signal_id 三级回落。
        # FIX 2026-09-14 drill-3: 桥的 DEAL 行 m_strRemark 实测为空（passorder userOrderId
        # 不落 remark），成交归因必须能按交易所委托号反查派发项。
        # §P0 2026-09-18：补 signal_id 一级——xt/桥路径在 remark 非空时把 order_id 填成
        # remark（signal_id），按委托号反查必落空，方向权威化会被静默跳过。
        drow = None
        if not req.get("signal_id"):
            seq = str(req.get("seq", "") or "")
            drow = self.store.dispatch_get(seq) if seq else None
            if drow is None and req.get("order_id"):
                drow = self.store.dispatch_by_order_id(str(req.get("order_id")))
            if drow:
                req["signal_id"] = drow.get("signal_id", "")
                req.setdefault("code", drow.get("code", ""))
        if drow is None:
            oid = str(req.get("order_id", "") or "")
            drow = self.store.dispatch_by_order_id(oid) if oid else None
        if drow is None and req.get("signal_id"):
            drow = self.store.dispatch_by_signal_id(str(req.get("signal_id")))
        if drow:
            # 代码回填（与方向同源的权威性）：带 signal_id 的回报不再走上面的归因分支，
            # 若其 code 缺失，同样以派发项为准——否则 apply_fill 会拿空代码查持仓、
            # 卖出被判为"无底仓 no-op"而静默漏账。
            if not req.get("code"):
                req["code"] = drow.get("code", "")
            auth = str(drow.get("side", "") or "")
            inferred = str(req.get("side", "") or "")
            if auth:
                if inferred and inferred != auth:
                    # 方向推断与派发事实不符——枚举空间漂移的信号，必须留痕（不可静默采纳）。
                    log.warning(
                        "[gateway] trade side mismatch: dispatch=%s inferred=%s code=%s oid=%s seq=%s"
                        " — using dispatch side", auth, inferred,
                        drow.get("code", ""), req.get("order_id", ""), drow.get("seq", ""))
                # 权威覆盖（非 setdefault）：本端下单方向 > 柜台字段反推。
                req["side"] = auth
        # §REJECT（2026-09-22 修复批，LOW「trade_id 空且 order_id 空回报拒收」）：
        # 归因回填后仍两把身份锚皆空 → 400 显式拒收（桥回报走 HTTP/文件桥回执语义，
        # 与 §F5 Go 侧 order 回报缺 order_id 拒 400 同口径；网关 handler.on_trade 侧
        # 另有第二道拒收，双防线）。不落库=不产生无法去重/无法对账的 fills 垃圾行。
        _tid = str(req.get("trade_id", "") or "").strip()
        _oid = str(req.get("order_id", "") or "").strip()
        if not _oid and drow:
            _oid = str(drow.get("order_id", "") or "").strip()
            if _oid:
                req["order_id"] = _oid
        if not _tid and not _oid:
            log.warning("[gateway] §REJECT trade 回报 trade_id/order_id 皆空，拒收 400: "
                        "code=%s side=%s qty=%s signal=%s seq=%s",
                        req.get("code"), req.get("side"), req.get("qty"),
                        req.get("signal_id"), req.get("seq", ""))
            return 400, {"ok": False, "err": "trade report has neither trade_id nor order_id — rejected"}
        # on_trade 返回值（False=重放去重命中/第二道拒收）不改变本端 200 语义：
        # 重放本就该被幂等吞掉，桥据此结算回报不重投。
        self.handler.on_trade({
            "order_id": req.get("order_id", ""), "trade_id": req.get("trade_id", ""),
            "name": req.get("name", ""), "code": req.get("code", ""), "side": req.get("side", ""),
            "price": float(req.get("price", 0) or 0), "qty": int(req.get("qty", 0) or 0),
            "amount": float(req.get("amount", 0) or 0),
            "traded_at": req.get("traded_at") or _now_cn(),
            "signal_id": req.get("signal_id", ""),
            # §P2-FEE 20260918：费用腿透传（桥 DEAL 行尽力带 commission，缺=0；
            # handler.on_trade → store.apply_fill 落 fills.fee，供 /settlement 费用差对账）。
            "fee": float(req.get("fee", 0) or 0),
            "stamp_tax": float(req.get("stamp_tax", 0) or 0),
        })
        # §QMT-DUAL 委托状态推进：按累计成交量对比申报量判已成/部成（对同一交易所委托号）
        oid = str(req.get("order_id", "") or "")
        prow = self.store.dispatch_by_order_id(oid) if oid else None
        if prow and prow.get("signal_id"):
            filled = self.store.order_filled_qty(oid)
            order_qty = int(prow.get("qty", 0) or 0)
            status = "已成" if filled >= order_qty else "部成"
            self.handler.on_order({
                "order_id": oid, "signal_id": prow.get("signal_id", ""),
                "code": prow.get("code", ""), "side": prow.get("side", ""), "status": status,
                "price": float(prow.get("price", 0) or 0), "qty": int(prow.get("qty", 0) or 0),
                "created_at": prow.get("created_at", ""),
                "at": _now_cn(),
            })
        return 200, {"ok": True, "err": ""}

    def _do_admin_broker(self, body):
        """手动切换 active 通道（POST /admin/broker {"broker":"xt|queued"}）。"""
        key = str((body or {}).get("broker", "") or "")
        if key not in self.brokers:
            return 400, {"ok": False, "err": "broker must be one of: %s" % ", ".join(self.brokers)}
        ok, err = self.switch_broker(key, reason="admin")
        if not ok:
            return 409, {"ok": False, "err": err}
        return 200, {"ok": True, "broker": key, "err": ""}

    def _do_admin_status(self):
        """观察端点（GET /admin/status）：active 通道、双通道在线态、派发队列统计。"""
        payload = {
            "ok": True,
            "ts": _now_cn(),
            "active": self.active_key,
            "failover_enable": bool(self.cfg.get("failover_enable", False)),
        }
        payload["brokers"] = {k: bool(b.is_connected()) for k, b in self.brokers.items()}
        if "queued" in self.brokers:
            payload["dispatch"] = self.store.dispatch_stats()
        return 200, payload

    def _do_order(self, body):
        """处理 POST /order 下单：参数校验 → 幂等占位 → 真实下单 → 回填委托号。

        校验 code/qty/signal_id（非空）、限价单必须有价格；按板块整手规则校验买入数量；
        signal_id 为空一律拒绝（幂等与审计唯一锚点，§G2）；同 signal_id 幂等去重。
        English: handles POST /order — validates params, takes the idempotent placeholder,
        places the order via the broker, and fills back the exchange order id.
        """
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

        # §A1（AUDIT_FULLSTACK_20260918）网关侧独立金额帽：与首尔 risk.Gate.checkMaxOrderAmount
        # 同语义——0=关闭，买卖双向拒单；amount 缺失/非正回退 qty×参考价（首尔同款回退）。
        # 拒单发生在 claim 之前，不消耗幂等占位，首尔可安全修正后重试。
        max_amt = float(self.cfg.get("max_order_amount", 0) or 0)
        if max_amt > 0:
            try:
                amt = float(req.get("amount", 0) or 0)
            except (TypeError, ValueError):
                amt = 0.0
            if amt <= 0:
                amt = qty * price
            if amt > max_amt:
                return 400, {"ok": False,
                             "err": "amount %.2f exceeds gateway cap %.2f" % (amt, max_amt)}

        # §A1 战法白名单（网关第二道闸）：空列表=关闭（与首尔开关的存量语义一致）；
        # 非空时买入方向必须显式命中 strategy_type（卖出/零股清仓不设准入，同 signalctl 直通语义）。
        # strict_fields=true 时缺键 fail-close 拒单；默认 false 缺键放行（旧客户端兼容窗口）。
        allowed = [str(x) for x in (self.cfg.get("allowed_strategies") or [])]
        if allowed and side == "买入":
            stype = str(req.get("strategy_type", "") or "")
            if not stype:
                if bool(self.cfg.get("strict_fields", False)):
                    return 400, {"ok": False, "err": "strategy_type required (strict_fields)"}
            elif stype not in allowed:
                return 400, {"ok": False,
                             "err": "strategy %s not in gateway whitelist" % stype}

        # §G1 原子占位：抢不到 = 已处理过（幂等返回）或正在下单中（409 防并发穿透）
        draft = {
            "signal_id": signal_id, "code": code, "side": side,
            "price": price, "qty": qty,
            "created_at": req.get("created_at") or "",
            "user_id": self.user_id,  # §P1-9 多账号隔离归属
        }
        _t0 = time.time()  # §PERF 下单 accept 延迟埋点（性能门禁 G3 数据源）
        claimed, existing = self.ids.claim(draft)
        # 没抢到占位 = 已下过（幂等返回）或正在下单中（409 防并发穿透）
        if not claimed:
            existing = existing or {}
            oid = existing.get("order_id") or ""
            if is_placeholder_order_id(oid):
                return 409, {"ok": False, "err": "duplicate signal_id in-flight"}
            # 幂等：已下过 → 返回原委托引用（不重复下单）
            return 200, {"ok": True, "order_id": oid, "err": ""}

        ok, order_ref, err = self.active_broker.place_order(req)
        if not ok:
            # 失败释放占位，允许后续重试真正重新下单
            self.ids.release(signal_id)
            return 400, {"ok": False, "err": err or "place order failed"}
        # settle：占位行回填真实委托引用（seq:<n> 或 mock 单号；交易所号随后续回报替换）
        self.ids.settle({
            "order_id": order_ref, "signal_id": signal_id, "code": code, "side": side,
            "status": "已报", "price": price, "qty": qty,
            "created_at": req.get("created_at") or _now_cn(),
            "user_id": self.user_id,  # §P1-9 多账号隔离归属
        })
        log.info("[gateway] order accept: signal=%s code=%s side=%s qty=%s ref=%s "
                 "accept_ms=%.1f broker=%s", signal_id, code, side, qty, order_ref,
                 (time.time() - _t0) * 1000, self.active_key)
        return 200, {"ok": True, "order_id": order_ref, "err": ""}

    def _do_cancel(self, body):
        """处理 POST /cancel 撤单：校验委托引用后委托 broker，结果如实返回。

        失败返回 409 且不吞掉错误——让首尔侧继续跟踪该委托并告警，避免误判成功。
        English: handles POST /cancel — validates the order ref and delegates to the broker,
        returning failures honestly so the decision side keeps tracking the order.
        """
        # 撤单：校验引用后委托给 broker，结果如实返回（失败不让首尔误判成功）
        order_ref = str((body or {}).get("order_id", "") or "")
        if not order_ref:
            return 400, {"ok": False, "err": "order_id required"}
        # §修复：撤单结果不再被吞——失败让首尔侧继续跟踪该委托并告警
        ok, err = self.active_broker.cancel(order_ref)
        if ok:
            return 200, {"ok": True, "err": ""}
        return 409, {"ok": False, "err": err or "cancel failed"}

    def _do_state(self):
        """处理 GET /state 状态查询：返回通道连接态、账户与全部委托（含历史）。

        通道未连接时持仓返回空列表（调用方按不可信快照处理，不据此清账）。
        English: handles GET /state — returns connection state, account and all orders;
        positions are empty when the broker is disconnected (treated as untrusted snapshot).
        """
        positions = self.active_broker.query_positions() if self.active_broker.is_connected() else []
        orders = self.store.list_orders()
        payload = {
            "connected": self.active_broker.is_connected(),
            "account": self.cfg.get("account", ""),
            "positions": positions,
            "orders": orders,
        }
        # §QMT-DUAL 透出 active 通道名，便于量仔/前端展示当前执行路径
        payload["broker_mode"] = self.active_key
        return 200, payload

    def _do_settlement(self, request_handler=None):
        """处理 GET /settlement?date=YYYY-MM-DD：日终结算三方对账的券商权威源（§P0-1a）。

        装配口径：
        - trades = 网关注销账本 fills 表当日全部成交（serial 用 §G1 唯一成交编号 trade_id，
          Go 侧对账物理事实键匹配，不再依赖 serial 也成立）；
        - connected = active 通道实时连接态（False 时 Go 侧按"交割单不可信"静默跳过）；
        - cash = 最近一次账户资产快照（桥/xt 回报的 asset），缺失时为空 dict——
          Go 侧仅当 cash.cash>0 才计算现金差，不误报。

        English: §P0-1a — GET /settlement?date=YYYY-MM-DD serves the broker-authoritative leg of
        three-way settlement from the gateway fills ledger (trade_id as serial), with connection
        state and the latest asset snapshot for the cash-diff leg.
        """
        query = ""
        if request_handler is not None:
            query = urlparse(getattr(request_handler, "path", "") or "").query
        params = parse_qs(query or "")
        day = (params.get("date") or [""])[0].strip()
        # 日期口径校验：只接受 YYYY-MM-DD（Go 侧 data.TradingDayDate 兼容由 Go 端负责）
        if len(day) != 10 or day[4] != "-" or day[7] != "-" or not (day[:4] + day[5:7] + day[8:]).isdigit():
            return 400, {"ok": False, "err": "date required, format YYYY-MM-DD"}
        connected = bool(self.active_broker.is_connected())
        trades = self.store.settlement_trades(day)
        asset = getattr(self.handler, "_last_asset", None) or {}
        cash = {}
        for k in ("cash", "frozen_cash", "total_asset", "market_value"):
            try:
                cash[k] = float(asset.get(k) or 0.0)
            except (TypeError, ValueError):
                cash[k] = 0.0
        return 200, {
            "ok": True,
            "date": day,
            "account": self.cfg.get("account", ""),
            "trades": trades,
            "cash": cash,
            "connected": connected,
            "broker": self.active_key,
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
        """写 HTTP JSON 响应：UTF-8 编码、ensure_ascii=False 保留中文，串化兜底。"""
        data = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _dispatch(self):
        """统一 HTTP 分发入口：解析路径/读 JSON 体 → IP 访问控制 → Bearer 鉴权 → 转交 Gateway.handle。

        顶层 try/except（§G8）：handler 内任何异常返回结构化 500 而非裸断连，
        避免首尔侧把无响应当 transport error 无限重试打爆网关。/health 免鉴权但受 IP 限制。
        English: unified dispatch — parse/read body, enforce IP allow-list and Bearer auth,
        then delegate to Gateway.handle with a top-level exception guard returning a structured 500.
        """
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
        """处理 GET 请求（/health、/state 等只读端点），统一走 _dispatch。"""
        self._dispatch()

    def do_POST(self):  # noqa: N802
        """处理 POST 请求（/order、/cancel 等写端点），统一走 _dispatch。"""
        self._dispatch()

    def log_message(self, *args):  # noqa: A003
        # 静默基类默认访问日志（避免每请求打一行噪声，错误已由网关自身结构化日志覆盖）
        pass


def main(argv=None):
    """命令行入口：解析参数 → 装载配置 → 启动 HTTP/HTTPS 服务与网关后台线程。

    支持 --config / --listen / --verbose；日志双通道（stderr + PID 隔离文件）。
    English: CLI entry — parse args, load config, and start the HTTP/HTTPS server
    with the gateway's background threads (reconnect/reconcile/report sender).
    """
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
