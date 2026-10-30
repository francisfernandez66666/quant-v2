#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.gateway — 东莞证券 MiniQMT 网关 REST 服务（AUTO_TRADING_PLAN M2）。

零第三方依赖（标准库 http.server）。接口与首尔侧 trading.QMTClient 契约一致：
  POST /order   {"signal_id","code","name","strategy","strategy_id","strategy_type","side",
                 "price_type","price","qty","amount","created_at","staleness_ms","prev_close","current_price"}
                → {"ok","order_id","err"}
  POST /cancel  {"order_id"}                                                    → {"ok","err"}
  GET  /state   → {"connected","account","positions","orders"}
  GET  /health  → {"ok","ts","broker","broker_connected","live_ready","xt_connected","feed_connected","xt_last_fail_at"}
                （免鉴权；ok=进程存活，live_ready=交易通道就绪，两者语义分开——§N-01）

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
§CLAIMRELEASE（2026-09-22 晚批 N-8）占位释放分流 + 派发队列纵深防线（详见 _do_order /
  _release_order_claim / _alert_unresolved_pending 各处说明）：
  §M-2 的「未 settle 即释放」不变式过宽，把「从未发送」与「已交给通道但结算失败」混为
  一谈，后者被释放后 Go 侧按「重试次数+1」的有限重试会再走一遍下单窗口 → 同 signal_id
  第二笔入队 = 双卖/双买，且旧日志事后无法区分这两种。现在：
    - _release_order_claim 必须显式收到 sent_to_broker（关键字必传，裸调即 TypeError），
      未发送 → 照旧删占位（保留 §M-2 治死锁的原语义）；已发送/已入队 → 保留占位行并
      提升为第三态「待核对」，同 sid 后续 claim 一律 409，同时立即 error 日志告警；
    - 所有超时清理路径只认 status='pending'，第三态由 _alert_unresolved_pending 用**独立
      可配置阈值**反复告警（绝不自动删行），收敛出口＝回报到达（upsert_order 推进）
      或 POST /admin/order-confirm 的显式人工确认；
    - store.dispatch 的部分唯一索引 idx_dispatch_signal_active 兜住"上层防线被拆掉"那一格。
  释放点日志一律写清 reason 与「当时是否已入队」，供事后取证。
§2026-09-22 修复批（本文件四项，详见各处同名 §编号说明；桥侧 qmt_bridge_strategy.py 的
  中文说明按仓库既定策略由本文件承载）：
  §SIDEGATE-PY（M-1）/order 方向白名单：side ∉ {买入,卖出} 一律 400（错误里带实际取值）。
    旧实现只判 `side=="卖出"`，非法串按买入整手校验、却被 broker/桥的三元式下成卖单。
  §M-2 place_order 异常不再遗留 pending 占位（try/finally 释放）+ 运行期 60s 巡检
    `_sweep_stale_pending` 释放超龄（默认 600s）本地占位；两条路径都**只解锁不重发**。
    （§CLAIMRELEASE 收紧：释放前先看「是否已交给通道」——已交付的那一路不再删行，见上。）
  §M-3 委托腿二次确认：桥回报带 order_confirmed，未确认单在 reason/dispatch result/日志里
    显式标注（状态字面量仍为「已报」，原因见 _apply_order_result 的说明）。
  §A4 文件桥位点改为「逐行、apply 成功后推进」，坏行/连续失败行落
    bridge_report_dead.jsonl 死信文件留痕（旧实现先推位点再 apply，异常行被永久跳过）。

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
from datetime import datetime  # §CLAIMRELEASE：「待核对」滞留龄计算（显式北京时区比较）

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# §TZ（2026-09-22 修复批）：网关内全部落库/上报时间串统一走 store._now_cn()
# （显式北京时区，见其 §TZ 说明）；禁止再用「本地 strftime + 硬编码 +08:00 后缀」的写法
# ——那会把非北京时区部署机的本地钟面贴上 +08:00 假偏移（LOW 族根治项）。
from store import (  # noqa: E402
    Store, is_placeholder_order_id, json_default, _now_cn, CN_TZ,
    # §CLAIMRELEASE 第三态字面量与派发队列重复入队的可识别异常（分流与告警两条路都要认它）
    UNRESOLVED_STATUS, DispatchDuplicateSignal,
    # §CANCEL-STATE（2026-10-28）撤单中状态与撤单被拒事件前缀（在途谓词与状态迁移共用同一份字面量）
    CANCEL_PENDING_STATUS, CANCEL_REJECT_PREFIX, IN_FLIGHT_STATUSES,
    # §ORDER-ID-REUSE（2026-10-30）终态集合：/order 幂等回显必须能识别"这个键已经死了"
    TERMINAL_STATUSES,
)
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
    "owner_user_id": "",       # §TOKEN-PERAACCT 资金账号账本持有人：持仓/资金这类账户级事件归它
    # §TOKEN-PERAACCT（2026-10-30）入向凭证从"一把共享钥匙"改为"一账号一把"：
    # [{"user_id": "u_xxx", "token": "...", "label": "可选备注"}]。
    # 上面的 `token` 继续有效并映射到 owner_user_id（缺省回退 user_id），所以 account_tokens
    # 缺失或为空时鉴权行为与今天逐字节一致——不留"配置改了但网关注册表空着"的半态。
    "account_tokens": [],
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
    # §M-2（2026-09-22 修复批）orders 本地 pending 占位的超时阈值（秒，0=关闭巡检）——
    # 旧实现只有 start() 里 release_stale_pending 调一次（:258-262），进程长活期间
    # 「claim 成功但 place_order 抛异常/settle 失败」遗留的占位再无人清理，
    # 同 signal_id 永 409。阈值与启动期同用 600s，运行期巡检复用 _dispatch_reap_loop。
    "pending_stale_sec": 600,
    # §CLAIMRELEASE（2026-09-22 晚批 N-8）第三态「待核对」占位的滞留告警阈值（秒，0=关闭告警）——
    # 它与 pending_stale_sec 是**两件事**：普通占位超龄 = 解锁（删行），第三态超龄 = 只告警不解锁
    # （那笔单大概率已在券商侧，删行等于放行第二笔）。默认 3600s：给回报/对账留出收敛窗口。
    "pending_unresolved_alert_sec": 3600,
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
CONTRACT_IGNORED_FIELDS = {
    "name", "strategy_id", "staleness_ms", "prev_close", "current_price",
    # §C2：VWAP 与回踩超时是 Seoul 侧执行/研究字段，网关不消费但必须显式声明（三点契约）。
    "market_vwap", "vwap_source", "vwap_as_of", "signal_vwap_premium_pct", "timeout_sec",
    # §DYN-SIZE-v3: audit-only marker on the order envelope; the gateway never consumes it.
    "dynamic_budget",
    # §DYN-T（批7/W8-A）同样是"只随单留痕"的标记：日内回转的净额语义在首尔侧 risk.Gate 完成，
    # 网关不复算也不消费。声明集必须与 golden 同步（三点契约），漏一处就是双红。
    "intraday_t",
}

# §SIDEGATE-PY（2026-09-22 修复批，M-1 升级 H-5）下单方向白名单：网关接单的唯一合法取值集。
# 与 Go internal/trading 的 SideBuy/SideSell、桥侧 BUY/SELL（\\u 转义）以及 handler 的方向
# 判定同集；任何其它形态（英文 buy/SELL、空串、带空格的 " 买入 "）都是上游装配错误，
# 绝不允许「按买入校验、下成卖单」。
# English: the only legal order sides accepted at the gateway door — everything else is a 400.
ORDER_SIDES = ("买入", "卖出")

# §A4（2026-09-22 修复批）文件桥"逐行推进位点"的重试计数表里，末段无换行残段的特殊键。
# 用不会与任何 JSON 行文本相等的前缀键，避免和正常行的计数互相污染。
_BRIDGE_TAIL_KEY = "\x00__tail__"


def load_config(path):
    """加载网关配置：默认配置 + 用户 JSON 文件覆盖 + 环境变量 token 注入。

    优先级：环境变量 QUANT_GATEWAY_TOKEN > 配置文件 token；report_token 同理
    （QUANT_GATEWAY_REPORT_TOKEN，缺省回退 token）。返回合并后的配置字典。
    English: loads gateway config — defaults merged with the user JSON file, then
    environment-variable tokens (QUANT_GATEWAY_TOKEN / QUANT_GATEWAY_REPORT_TOKEN) override file values.
    """
    cfg = dict(DEFAULT_CONFIG)
    if path and os.path.exists(path):
        # §0926E2E-W2C（2026-09-26 二波）落盘密钥权限自检：config.xt.json 含明文 token+资金账号，
        # 权限宽于 0600（组/其他可读）时启动打 warning——与 QUANT_GATEWAY_TOKEN 明文回退告警
        # （本函数下方）同族，只吵不改：拒启动会让现网在无人值守重启时直接断链，处置权在人。
        # 口径按运行时真实取值链（§探针纪律）：**只在 POSIX 判定**——Windows 的 st_mode 恒报
        # 0o666（NTFS ACL 与 stat 是两套体系），拿它判宽严是"永久性假红"；Windows 侧的收敛
        # 责任在生成端 ensure_gateway_config.ps1（icacls 收敛），不在这里。
        try:
            if os.name == "posix":
                import stat as _stat
                mode = _stat.S_IMODE(os.stat(path).st_mode)
                if mode & 0o077:
                    log.warning(
                        "[gateway] 配置文件 %s 权限 %03o 宽于 0600：同机其他账号可读到明文 token，"
                        "请 chmod 600 收敛", path, mode
                    )
        except OSError as e:  # stat 失败（竞态删除等）只跳过自检，不影响启动
            log.warning("[gateway] 配置文件权限自检跳过（stat 失败）: %s", e)
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
        # §TOKEN-PERAACCT（2026-10-30）入向凭证注册表：token → user_id（一账号一把钥匙）。
        # owner_user_id = 这个资金账号账本的持有人（缺省回退旧 user_id 键，保持单账号形态不变）。
        # 旧键 cfg["token"] 继续在册并映射到 owner：account_tokens 缺失/为空时鉴权与今天逐字节一致，
        # 绝不制造"配置改了但注册表是空的"那种所有账号一起 401 的半态。
        self.owner_user_id = str(cfg.get("owner_user_id") or self.user_id or "")
        self._auth_tokens = {}
        for item in (cfg.get("account_tokens") or []):
            if not isinstance(item, dict):
                log.warning("[gateway] §TOKEN-PERAACCT 忽略非对象的 account_tokens 项: %r", item)
                continue
            tk = str(item.get("token") or "")
            uid = str(item.get("user_id") or "")
            if not tk or not uid:
                log.warning("[gateway] §TOKEN-PERAACCT 忽略缺 token/user_id 的 account_tokens 项")
                continue
            prev = self._auth_tokens.get(tk)
            if prev and prev != uid:
                # 同一把钥匙挂两个账号：归属不可判定，后到的那条直接丢弃（宁可少一把也不制造歧义）
                log.error("[gateway] §TOKEN-PERAACCT 重复 token 冲突（已挂 %s，忽略 %s）——"
                          "每把实盘钥匙只允许属于一个账号", prev, uid)
                continue
            self._auth_tokens[tk] = uid
        if self._auth_tokens and not self.owner_user_id:
            log.error("[gateway] §TOKEN-PERAACCT 配了 account_tokens 但没有 owner_user_id/user_id："
                      "持仓与资金这类账户级事件将无账号可归")
        if cfg.get("token"):
            self._auth_tokens.setdefault(str(cfg["token"]), self.owner_user_id)
        # 反向表 uid → 那把在册钥匙：出向回报据此选钥匙（§TOKEN-PERAACCT）。
        # 同一账号配了多条时取第一条并告警——归属靠钥匙，钥匙含糊就等于归属含糊。
        self._tokens_by_uid = {}
        for tk, uid in self._auth_tokens.items():
            if uid in self._tokens_by_uid:
                log.warning("[gateway] §TOKEN-PERAACCT 账号 %s 在册多把钥匙，出向固定用 %s",
                            uid, str(self._tokens_by_uid[uid])[:6] + "…")
                continue
            self._tokens_by_uid[uid] = tk

        def _report_token_for(uid):
            # 空归属（历史事件/账户级事实）走默认钥匙；有归属但注册表里没有 ⇒ 返回空串，
            # 由处理器回退默认钥匙并计数——**绝不因为对不上号就不发**。
            if not uid:
                return ""
            return self._tokens_by_uid.get(uid, "")

        self.handler = ReportHandler(
            self.store, cfg.get("report_url", ""), cfg.get("report_token", ""),
            # 处理器级归属＝owner：持仓/资金这类"资金账号自己的账"永远归账本持有人，
            # 下单类事件则在 _resolve_uid 里回查成真实下单账号（§TOKEN-PERAACCT N3）。
            self.owner_user_id or self.user_id,
            token_for=_report_token_for,
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
        # §ORDER-ID-REUSE：非终态僵尸行的当日清理标记（只在拿到可信委托表快照后清一次）。
        self._order_purge_day = ""
        # §N-01（2026-10-20）：最近一次 xt 连接失败时刻（北京时间字符串，空=从未失败）。
        # 只记录发生时刻，不记录错误原文——/health 免鉴权，内部异常串不得外泄。
        self._xt_last_fail_at = ""
        # §P2-G（2026-10-06 修复批 波 5）命令文件写失败的可见化：
        # total=累计失败轮次，streak=连续失败轮次（任一轮写成功即归零），
        # held=最近一次失败时被按住（保持 pending、未下发）的单数。
        # 三个都是进程内计数（重启归零），与网关既有口径一致：告警出口就是
        # log.error + /admin/status 观察位，网关侧没有独立推送通道。
        self._file_bridge_push_fail_total = 0
        self._file_bridge_push_fail_streak = 0
        self._file_bridge_push_fail_held = 0
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
        # §CLAIMRELEASE 第三态「待核对」告警的去重集（同一 signal_id 每进程只主动 error 一次，
        # 之后由 /admin/status 与超龄巡检持续可见）——巡检 60s 一轮，无去重会刷屏。
        self._unresolved_alerted = set()
        # §MANUAL-SYNC（2026-10-28）券商当日委托表快照 + 消失计数：
        #   _orders_snap 缓存最新快照（by_id 供成交方向背书），
        #   _order_miss 记录"本地在途但柜台表里连续没看见"的次数（达到阈值才判终态）。
        self._orders_snap = {"by_id": {}, "ts": 0.0, "data_ts": "", "n": 0}
        self._order_miss = {}
        self._orders_snap_skipped_unknown = 0
        # §CLOSE-RECON（2026-10-28）收盘对账观测位：桥在 15:01 的收盘快照打 close_snapshot=true，
        # 网关只记"最近一次收盘快照的时刻/行数"——它回答"当天收盘那 3 分钟的成交有没有回传"，
        # 是 600362 事故（委托行说已撤、实际成交没回传）的唯一现网信号。
        self._close_snap = {"at": "", "day": "", "ts": 0.0, "n": 0, "positions": 0, "trades": 0}
        # §M-3/§MANUAL-SYNC 未确认单观察项：signal_id -> 首次判未确认的时刻。
        # 委托表快照里一旦出现它的 remark 就消项；宽限期内始终没出现 → 回写发送失败。
        self._order_unconfirmed = {}
        # §CANCEL-STATE（2026-10-28）撤单被拒判定：order_id -> 连续几轮快照显示"仍在途"。
        # 撤单请求发出后本地是「撤单中」；若券商委托表连着 N 轮都还显示它在途（已报/部成），
        # 说明撤单**没有生效**（收盘集合竞价/已成交/柜台拒单），需要显式回退并告警。
        # 为什么不能靠快照直接回退：那样"撤单请求还在飞"与"交易所没撤成"两件事会混成一次
        # 静默回退，操作人看不到"我发的撤单被拒了"。所以要走计数 + 显式 §CANCEL-REJECT 事件。
        self._cancel_pending_miss = {}

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
        # §M-2：pending 占位超时阈值改为配置项（默认仍 600s，与历史硬编码同值，行为零变化），
        # 启动期清理与运行期巡检共用这一个口径。
        pending_stale_sec = int(self.cfg.get("pending_stale_sec", 600) or 0)
        released = 0
        try:
            released = self.store.release_stale_pending(max_age_sec=pending_stale_sec)
        except Exception as _e:  # noqa: BLE001 — 清理解放失败不应阻断启动
            log.warning("[gateway] 清理残留 pending 失败: %s", _e)
        if released:
            log.warning("[gateway] 已释放 %d 个超时残留 pending 占位（崩溃恢复，避免 signal_id 永久死锁）", released)
        # §ORDER-ID-REUSE（2026-10-30）：启动时只清**终态**历史行。QMT 的交易所委托号会跨日复用，
        # 而 orders.order_id 是主键——历史行越多，撞键概率越高（今天撞上了 9-22 的一条旧行，
        # 终态回报因此被吞）。非终态行可能对应真实在途单：**必须等拿到可信的券商委托表快照**
        # 再按"旧且不在快照里"清（见 _apply_orders_snapshot），启动时按日期一刀切是错的方向。
        try:
            purged = self.store.purge_stale_orders(_now_cn()[:10])
            if purged:
                log.warning("[gateway] §ORDER-ID-REUSE 启动清理历史委托行 %d 条", purged)
        except Exception as _e:  # noqa: BLE001
            log.warning("[gateway] §ORDER-ID-REUSE 启动清理失败: %s", _e)
        # 仍存在的 pending（10 分钟内、可能对应真实在途委托）仅告警，不自动释放，需人工核对券商侧。
        for p in self.store.list_pending():
            log.warning("[gateway] STALE pending order (within 10min, verify against broker): signal_id=%s "
                        "code=%s side=%s qty=%s",
                        p.get("signal_id"), p.get("code"), p.get("side"), p.get("qty"))
        # §CLAIMRELEASE：启动期同样要把第三态「待核对」行暴露出来（重启不丢证据、也不被上面
        # 那条超时清理误删）——它们是上一次进程"已把订单交给通道却没结算完"的现场。
        self._alert_unresolved_pending("启动")
        # §M16：dispatch 表与 orders 同批做启动清理——上次进程存活期内被桥取走（inflight）
        # 但回报丢失/桥重启未结算的超龄派发项，启动先收割一次，随后周期巡检兜底。
        self._reap_dispatch_inflight("启动")
        # §M-2：巡检线程同时负责 orders 的 pending 占位超时释放——旧实现只在这里
        # 启动「inflight 收割」，运行期对 pending 完全无兜底（M-2 的第二半）。
        # 两个阈值任一开启即起线程；两者皆 0 才不起（保持"可完全关闭巡检"的运维口径）。
        if int(self.cfg.get("dispatch_inflight_reap_sec", 1800) or 0) > 0 or pending_stale_sec > 0:
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
        实现只对「有 pending」的轮次写文件（无 pending 不打扰桥）。

        §P2-G（2026-10-06 修复批 波 5）顺序翻转：本函数原先第一步就调
        `dispatch_pending()`，而那个函数**取单即翻 inflight**（HTTP 桥的正确语义——
        响应体就是把单交给对方了）。file 桥借用它之后，"写命令文件失败"这一路变成
        确定性的丢单：行已经是 inflight，桥永远看不到这张单，网关侧只能等 §M16 的
        超龄收割（默认 1800s）把它判废，而判废不回 pending ⇒ 这笔交易在系统里
        安静地消失了，只有 log.exception 一行。现改为
        **peek（只读）→ 写文件成功 → mark_inflight**：写失败时行仍是 pending，
        下一轮 0.5s 后照常重推，同时把失败次数计进 /admin/status 观察位
        （网关侧没有独立告警出口，log.error + 观察位是本仓既有惯例）。
        English: §P2-G — peek pending, write the cmd file first, only then flip to
        inflight; a failed write keeps the rows pending and is counted outwardly.
        """
        try:
            pending = self.store.dispatch_pending_peek(limit=50)
        except Exception:  # noqa: BLE001
            log.exception("[file-bridge] dispatch_pending_peek failed")
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
            # §P2-G 关键分支：写盘失败**不动任何状态**——上面的 peek 是只读的，
            # 这批行仍为 pending，下一轮照常重推。计数进观察位是为了让"每轮都写失败"
            # 这种持续故障能从 log 的汪洋里浮出来（log.exception 只能证明抛过异常，
            # 看不出是抖动还是系统性坏了）。
            self._record_file_bridge_push_failure(len(pending), cmd_path)
            return
        # 文件已落盘才推进状态：mark 只翻「仍为 pending」的行，
        # 与 dispatch_pending 的 inflight_at 同源（收割龄仍按被取走的时间计）。
        try:
            moved = self.store.dispatch_mark_inflight([p.get("seq") for p in pending])
        except Exception:  # noqa: BLE001
            # 状态没翻成而文件已写好 ⇒ 下一轮会把同一批再 peek 出来重写。
            # 这不是重复下单：桥按 seq 去重（bridge_seen.jsonl 落盘成功才执行，
            # 见 qmt_bridge_strategy._record_seen/_load_seen），重写同一份命令体无害。
            # 反过来若在这里抢翻 inflight，就等于回到"网关以为交出去了、桥其实没拿到"。
            log.exception("[file-bridge] mark inflight failed (cmd file already written; "
                          "bridge dedupes by seq, rows stay pending)")
            return
        self._file_bridge_push_fail_streak = 0
        self._file_bridge_push_fail_held = 0
        log.info("[file-bridge] pushed %d pending cmd(s) to %s (marked inflight=%d)",
                 len(pending), cmd_path, moved)
        if moved != len(pending):
            # 翻动的行数少于本次写进文件的行数：这批里有行被别的消费者抢先取走
            # （现网 file 桥是唯一消费者，出现即说明 bridge_mode 配错/双通道并开）。
            log.warning("[file-bridge] mark inflight %d/%d：派发行被别的消费者取走，"
                        "确认 bridge_mode 是否只有 file 桥一条下发通道", moved, len(pending))

    def _record_file_bridge_push_failure(self, held, cmd_path):
        """§P2-G 写失败的可见化：连续/累计计数 + 被按住的单数进 /admin/status。

        只计数不抛异常：本函数在 0.5s 轮询线程里，抛出去等于把命令下发线程打死，
        比"这一轮没写成"严重得多。streak 由成功分支归零，所以观察位上
        `streak>0` 就等价于"此刻仍在失败"，`total` 只用于区分抖动与系统性故障。
        """
        self._file_bridge_push_fail_total += 1
        self._file_bridge_push_fail_streak += 1
        self._file_bridge_push_fail_held = int(held or 0)
        log.error("[file-bridge] 命令文件写入失败：%d 单保持 pending（下轮重推，§P2-G），"
                  "连续失败 %d 轮 / 累计 %d 次 path=%s",
                  held, self._file_bridge_push_fail_streak,
                  self._file_bridge_push_fail_total, cmd_path)
        return self._file_bridge_push_fail_streak

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
        回报语义与 HTTP PATH 一字不差。上报文件被轮转缩小则回到 0 偏移。

        §A4（2026-09-22 修复批）：单行消费/位点推进/死信留痕的细则在
        `_bridge_consume_report`；本循环只负责轮转判定、位点持久化与命令推送。
        `line_retry` 是**本进程内存态**的行级重试计数（重启即清零，最坏是多试一轮，
        事件应用幂等无害），刻意不落库——为一条畸形行去加持久化状态不值得。
        """
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
        line_retry = {}  # §A4 行级重试计数（含末段半行等待轮数），仅本进程内存态
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
                # §A4（2026-09-22 修复批）位点推进改「逐行、且只在处理成功后推进」，
                # 具体缺陷与理由见 _bridge_consume_report 的说明。
                pos = self._bridge_consume_report(report_path, pos, line_retry)
                try:
                    self.store.bridge_snapshot_set("report_pos", pos)
                except Exception:  # noqa: BLE001 — 偏移写失败下轮重放，幂等无害
                    log.exception("[file-bridge] persist report pos failed")
            # ② 推命令（无 pending 不打扰桥）
            self._file_bridge_push_pending()

    # §A4 单行 apply 连续失败的最大重试轮数（0.5s/轮 → 约 1.5s），超限落死信并跳过：
    # 既保住「异常行不再被静默跳过」，也不允许一条毒行把 tail 循环卡死。
    _BRIDGE_LINE_RETRY_MAX = 3
    # §A4 末段无换行残段的最大等待轮数（约 10s）：桥正常追加写会在下一轮补全换行符，
    # 超龄说明这是桥崩溃留下的半行，按整行处理（解析失败即死信推进），不再阻塞后续事件。
    _BRIDGE_TAIL_STALL_ROUNDS = 20

    def _file_bridge_dead_path(self):
        """§A4 死信文件路径：与上报文件同目录的 bridge_report_dead.jsonl（运维取证入口）。"""
        base_dir = os.path.dirname(os.path.abspath(__file__))
        return os.path.join(self.cfg.get("bridge_report_dir") or base_dir,
                            "bridge_report_dead.jsonl")

    def _bridge_dead_letter(self, text, reason):
        """§A4 死信落盘：把无法应用的原始行连同原因追加一行，写失败只告警不抛出。

        绝不因为"留痕写不下去"反过来打断 tail 循环——位点推进由调用方决定。
        """
        try:
            with open(self._file_bridge_dead_path(), "a", encoding="utf-8") as f:
                f.write(json.dumps({"at": _now_cn(), "reason": str(reason)[:300],
                                    "line": str(text)[:2000]}, ensure_ascii=False,
                                   default=json_default) + "\n")
        except Exception:  # noqa: BLE001 — 死信写失败退回日志，绝不影响主循环
            log.exception("[file-bridge] §A4 死信落盘失败（原始行见本日志）: %s", str(text)[:200])

    def _bridge_apply_line(self, text, retries):
        """§A4 应用单行上报。返回 True=可以把位点推过这一行，False=本轮保留、下轮重试。

        判定口径：
          - JSON 解析失败 → 确定性缺陷，重试无意义：直接死信 + 推进（旧实现是静默 continue）；
          - apply 抛异常 / 网关回 5xx（_do_dispatch_result 内部吞异常后也是 5xx）→ 视作瞬态，
            同一行最多重试 _BRIDGE_LINE_RETRY_MAX 轮，超限落死信并推进（防毒行卡死 tail）；
          - 4xx（unknown seq / 未知 type 等）→ 网关权威判定，确定性拒绝，推进并告警留痕。
        """
        try:
            body = json.loads(text)
        except Exception as e:  # noqa: BLE001 — §A4 坏行不再静默跳过：落死信留痕
            log.warning("[file-bridge] §A4 坏 JSON 行 → 死信: %s (%s)", text[:120], e)
            self._bridge_dead_letter(text, "bad_json: %s" % e)
            return True
        try:
            code, resp = self._do_dispatch_result(body)
        except Exception as e:  # noqa: BLE001 — 单事件失败不阻断后续行（但位点不再越过它）
            return self._bridge_line_failed(text, body, retries, "apply raised: %s" % e)
        if int(code or 0) >= 500:
            return self._bridge_line_failed(text, body, retries,
                                            "gateway 5xx: %s" % str(resp)[:160])
        if int(code or 0) >= 400:
            # 确定性拒绝（seq 不存在/类型未知）：推进位点，但必须留痕——旧实现只打 debug，
            # 生产 INFO 级别下等于静默丢弃。
            log.warning("[file-bridge] 回报被拒 %s -> %s %s（推进位点，不重试）",
                        (body or {}).get("type") if isinstance(body, dict) else "?",
                        code, str(resp)[:160])
        else:
            log.debug("[file-bridge] %s -> %s", (body or {}).get("type"), code)
        retries.pop(text, None)
        return True

    def _bridge_line_failed(self, text, body, retries, reason):
        """§A4 单行应用失败：未超限保留原位点重试，超限落死信并放行位点（防毒行卡死）。"""
        n = int(retries.get(text, 0)) + 1
        etype = (body or {}).get("type") if isinstance(body, dict) else "?"
        if n < self._BRIDGE_LINE_RETRY_MAX:
            retries[text] = n
            log.warning("[file-bridge] §A4 apply %s 失败（第 %d/%d 次，位点不推进、下轮重试）: %s",
                        etype, n, self._BRIDGE_LINE_RETRY_MAX, reason)
            return False
        retries.pop(text, None)
        log.error("[file-bridge] §A4 apply %s 连续 %d 次失败 → 死信留痕并跳过: %s",
                  etype, n, reason)
        self._bridge_dead_letter(text, "apply_failed_x%d: %s" % (n, reason))
        return True

    def _bridge_consume_report(self, report_path, pos, retries):
        """§A4（2026-09-22 修复批）读并逐行应用上报文件，返回**实际消费到**的位点。

        缺陷原文：旧实现（:428-430）先 `pos = f.tell()` 把位点一次性推到本次读到的块尾，
        再逐行 apply，:443-447 的 apply 异常只 `log.exception` 就继续、:448-451 才把
        已经越过失败行的位点持久化 → **异常行被永久跳过、事件彻底丢失**（成交/结算回报
        丢失＝账本失真），:438-442 的坏 JSON 行同样是 `continue` 静默吞掉。
        为什么这么改：位点是"已处理到哪"的唯一事实，只能在**该行处理成功后**推进；
        失败行先重试（事件应用是幂等的，重复无害），连续失败超限则写死信文件
        `bridge_report_dead.jsonl` 留痕后再越过——既不静默丢，也不让一条毒行卡死 tail。
        另外补上末段半行保护：桥是追加写，读到结尾无换行符的残段时本轮不消费，
        等下一轮补全（避免把正常行的一部分误判成坏行）。
        English: §A4 — advance the offset only after a line has actually been applied;
        failing lines are retried and finally dead-lettered instead of being skipped
        with the offset already moved past them.
        """
        try:
            with open(report_path, "rb") as f:
                f.seek(pos)
                data = f.read()
        except OSError as e:  # noqa: BLE001 — 文件被占用等瞬态：位点不动，下轮重试
            log.warning("[file-bridge] read failed: %s", e)
            return pos
        if not data:
            return pos
        segs = data.split(b"\n")
        tail_consumed = False            # 末段"死半行"是否被强制当整行消费（字节数不带换行符）
        if data.endswith(b"\n"):
            lines = segs[:-1]          # 末尾空段就是换行符本身，不是一行
            tail = b""
        else:
            lines = segs[:-1]          # 末段无换行：可能是桥正在写的半行
            tail = segs[-1]
        if tail:
            stalled = int(retries.get(_BRIDGE_TAIL_KEY, 0)) + 1
            if stalled >= self._BRIDGE_TAIL_STALL_ROUNDS:
                retries.pop(_BRIDGE_TAIL_KEY, None)
                lines.append(tail)      # 残段确认不会再被补全 → 当整行处理（坏行→死信）
                tail_consumed = True
            else:
                retries[_BRIDGE_TAIL_KEY] = stalled
        else:
            retries.pop(_BRIDGE_TAIL_KEY, None)
        consumed = 0
        for idx, raw in enumerate(lines):
            # 该行占用的字节 = 内容 + 1 个换行符；唯一例外是"被确认消费的死半行"（无换行符）
            is_last = idx == len(lines) - 1
            step = len(raw) + (0 if (is_last and tail_consumed) else 1)
            text = raw.decode("utf-8", errors="replace").strip()
            if not text:
                consumed += step
                continue
            if not self._bridge_apply_line(text, retries):
                break                   # 该行未消费：位点停在它之前，其余留到下轮
            consumed += step
        return pos + consumed

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

    def _sweep_stale_pending(self, where=""):
        """§M-2（2026-09-22 修复批）运行期 pending 占位超时释放。

        缺陷原文：`release_stale_pending` 只在 `start()` 里调一次（:258-262），
        而运行期 `_dispatch_reap_loop`（:494-502）只清 dispatch 的 inflight、
        从不清 orders 的 pending → 进程长活期间遗留的占位（异常路径、settle 失败、
        线程被 kill）永无人解锁，同 signal_id 后续请求恒 409「duplicate in-flight」。
        为什么这么改：把启动期那一条清理搬进 60s 巡检，阈值共用 `pending_stale_sec`
        （默认 600s，与启动期同口径），并保留 ids.py:5-10 记录的 fail-safe 语义——
        **只删本地未结算占位行（status='pending'），绝不自动重发/重排下单**，
        重复真实下单的防线仍然是 claim 的 signal_id 唯一键 + 「不重排」这一条。
        未超龄的 pending（可能是本端正在下单的毫秒级窗口）只告警留痕，交人工核对。
        English: §M-2 — runtime sweep that releases only over-aged *local* placeholders
        (never re-issues an order), reusing the same 600s threshold as the boot cleanup.
        """
        stale_sec = int(self.cfg.get("pending_stale_sec", 600) or 0)
        if stale_sec <= 0:
            return 0
        try:
            n = self.store.release_stale_pending(max_age_sec=stale_sec)
        except Exception:  # noqa: BLE001 — 巡检释放失败下轮重试，绝不影响其它巡检
            log.exception("[gateway] §M-2 pending 占位巡检释放失败")
            return 0
        if n:
            log.warning("[gateway] §M-2 运行期释放 %d 个超龄（>%ds）pending 占位（%s）——"
                        "仅解锁本地占位，网关未重发任何订单，请核对柜台侧是否已有委托",
                        n, stale_sec, where or "周期巡检")
        # §CLAIMRELEASE：同一条巡检顺带盯第三态（**只告警不删行**，见其方法说明）。
        # 放在这里而不是新开线程：巡检本来就是"本地占位体检"的唯一收敛点，两条共用 60s 节奏，
        # 且第三态绝不能被上面的 release 顺手清掉——两条 SQL 的 status 谓词天然互斥。
        self._alert_unresolved_pending(where or "周期巡检")
        # §MANUAL-SYNC：派发队列里从未被桥取走的 pending 行也要有出口，否则同 signal_id
        # 的重试在 enqueue 判重处恒 500（前端有单、券商没有、新买入发不出去）。
        try:
            self._reap_stale_pending_dispatch(where or "周期巡检")
        except Exception:  # noqa: BLE001 — 收割失败不影响其余巡检，下轮重试
            log.exception("[gateway] §MANUAL-SYNC pending 派发收割失败")
        return n

    def _reap_stale_pending_dispatch(self, where=""):
        """§MANUAL-SYNC（2026-10-28）：收割从未被桥取走的 pending 派发项。

        pending 行意味着命令还在网关侧、桥从未取走（§P2-G 写文件失败 / 桥长期离线），
        所以柜台确定没有这笔单：判 done 后回写量仔「发送失败」（可重试），绝不回到
        pending（重排＝可能是重复下单）。阈值 dispatch_pending_reap_sec，<=0 关闭。
        English: settles dispatch rows still pending past the timeout as failed (never
        re-queued), which unblocks same-signal retries and the stuck budget row.
        """
        sec = int(self.cfg.get("dispatch_pending_reap_sec", 600) or 0)
        if sec <= 0:
            return 0
        reason = "pending 超时（>%ds）未被桥取走，网关收割（§MANUAL-SYNC%s）" % (
            sec, ("，" + where) if where else "")
        rows = self.store.dispatch_reap_stale_pending(sec, reason=reason)
        for r in rows:
            log.warning("[gateway] §MANUAL-SYNC 收割超龄 pending 派发项 seq=%s kind=%s signal=%s code=%s",
                        r.get("seq"), r.get("kind"), r.get("signal_id"), r.get("code"))
            if r.get("kind") == "order" and r.get("signal_id"):
                ts = _now_cn()
                try:
                    # 「发送失败」是唯一能解开同 signal_id 幂等的可重试终态（Go 侧
                    # ResetFailedRealOrder 只放行发送失败 / 零成交已撤）。
                    self.handler.on_order({
                        "order_id": r.get("order_id") or r.get("seq"), "signal_id": r.get("signal_id"),
                        "code": r.get("code"), "side": r.get("side"), "status": "发送失败",
                        "reason": reason,
                        "price": float(r.get("price", 0) or 0), "qty": int(r.get("qty", 0) or 0),
                        "created_at": r.get("created_at") or ts, "at": ts,
                    })
                except Exception:  # noqa: BLE001 — 回写失败已有派发判废留痕，不阻断其余收割
                    log.exception("[gateway] §MANUAL-SYNC pending 收割回写失败 seq=%s", r.get("seq"))
        return len(rows)

    def _alert_unresolved_pending(self, where=""):
        """§CLAIMRELEASE 第三态「待核对」占位的滞留告警：**只告警、绝不自动删行**。

        为什么必须与 _sweep_stale_pending 分开：普通 pending 占位超龄 = 从未发送的残骸，
        删掉解锁是安全的（§M-2/§T5）；第三态那一行代表「订单已交给通道、结果未知」，
        按同一个 600s 阈值删掉它就等于把第三态降级回普通 pending —— N-8 的双卖门原样 reopened。
        所以这里：
          - 阈值独立可配（pending_unresolved_alert_sec，默认 3600s；0=关闭超龄告警）；
          - 动作只有 error 日志（网关既有告警出口：本仓 Python 侧无独立告警通道，
            log.error + /admin/status 观察位是既有惯例，见 §REJECT/§M-3 的处置口径），
            同一 signal_id 每进程只重复告一次，避免 60s 巡检刷屏；
          - 收敛出口不在这里：回报到达（upsert_order 推进）/ §M16 派发收割回写「已废」
            / 人工 POST /admin/order-confirm。
        返回本轮新告警条数（单测断言用）。
        English: §CLAIMRELEASE — alert-only dwell warning for the third state; it never
        deletes the row, because deleting it is exactly the double-sell path.
        """
        alert_sec = int(self.cfg.get("pending_unresolved_alert_sec", 3600) or 0)
        try:
            rows = self.store.list_unresolved_pending()
        except Exception:  # noqa: BLE001 — 巡检线程永不因异常退出
            log.exception("[gateway] §CLAIMRELEASE 读取「待核对」占位失败")
            return 0
        now = datetime.now(CN_TZ)
        alerted = 0
        for r in rows:
            sid = str(r.get("signal_id", "") or "")
            if not sid or sid in self._unresolved_alerted:
                continue
            age = 0.0
            try:
                # created_at 是"占位被抢走"的时刻 = 订单交给通道的时间锚（与 §T5 同源）；
                # 用带 +08:00 的显式北京时区比较，不受本机时区影响（§TZ 口径）
                ts = str(r.get("created_at", "") or "")
                age = (now - datetime.fromisoformat(ts)).total_seconds()
            except Exception:  # noqa: BLE001 — 时间串形态异常（老数据/测试值）不影响告警本身
                age = 0.0
            # 首次出现即告（不等超龄）：这是资金安全级事实；超龄后再告一次（阈值 0 = 不重复）
            self._unresolved_alerted.add(sid)
            alerted += 1
            log.error("[gateway] §CLAIMRELEASE 告警：signal_id=%s 处于「结果未知/待核对」"
                      "（code=%s side=%s qty=%s 已滞留 %.0fs，%s）——该行代表订单可能已在券商侧，"
                      "网关拒绝同信号重复下单且**不会自动清理**；请核对柜台后用 "
                      "POST /admin/order-confirm 收敛（超龄阈值=%ds）",
                      sid, r.get("code"), r.get("side"), r.get("qty"), age,
                      where or "周期巡检", alert_sec)
        return alerted

    def _dispatch_reap_loop(self):
        """§M16 周期收割：每 60s 巡检一次 inflight 超龄派发项（阈值见配置，默认 30min）。

        §M-2：同一条巡检线程顺带跑 orders 的 pending 占位超时释放（旧实现只收 inflight，
        pending 无人解锁 = 本条缺陷的另一半）。两条各自 try 保护，互不牵连。
        """
        while not self._stop.is_set():
            if self._stop.wait(60):
                break
            try:
                self._reap_dispatch_inflight()
            except Exception:  # noqa: BLE001 — 巡检线程永不因异常退出
                log.exception("[gateway] §M16 dispatch reap loop error")
            try:
                self._sweep_stale_pending()
            except Exception:  # noqa: BLE001 — 巡检线程永不因异常退出
                log.exception("[gateway] §M-2 pending sweep loop error")

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
                        # §N-01：把"最后一次失败发生在几点"落成 /health 可读字段
                        self._xt_last_fail_at = _now_cn()
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
                            self.handler.on_positions(poss, synced=True, data_ts=_now_cn())
                        else:
                            self.handler.on_positions([], synced=False, data_ts=_now_cn())
                            log.warning("[gateway] post-connect empty positions snapshot — "
                                        "reconcile skipped (account data may not be synced yet)")
                        self.handler.on_account(b.query_asset(), synced=True, data_ts=_now_cn())
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
        if path == "/admin/order-confirm" and method == "POST":
            # §CLAIMRELEASE 第三态人工确认出口：放在 broker 连接闸之前——要核对的正是
            # "通道状态不明"的单，绝不能因为通道断开就没法收敛
            return self._do_admin_order_confirm(body)
        if path.startswith("/quotes") and method == "GET":
            # §ENH-5 批E：只读行情通道放在 broker 连接闸之前——xtdata 与 xttrader 是
            # 两条独立通道，交易断连时行情应照常可查（行情可用性绝不与交易熔断互相污染）
            return self._do_quotes(request_handler)
        if path == "/orders" and method == "GET":
            # §ORDER-STATE-PULL（2026-10-30）：当日委托状态的只读对账源。放在连接闸之前：
            # 断线/熔断时决策端最需要"最后已知状态"来纠正停滞行，不能因为通道断开就拒答。
            return self._do_orders(request_handler)
        if not self.active_broker.is_connected():
            return 503, {"ok": False, "err": "broker not connected"}
        if path == "/order" and method == "POST":
            # §TOKEN-PERAACCT：下单方账号由入向钥匙确定，落进幂等占位行与委托行
            return self._do_order(body, getattr(request_handler, "_caller_uid", ""))
        if path == "/cancel" and method == "POST":
            return self._do_cancel(body, getattr(request_handler, "_caller_uid", ""))
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

    def _do_orders(self, request_handler=None):
        """GET /orders?day=YYYY-MM-DD：当日委托状态只读快照（§ORDER-STATE-PULL，2026-10-30）。

        为什么要有这个端点：委托终态原本只有"网关 → outbox → 决策端"的**推送**一条腿，
        落库失败/事件丢失/进程重启都会让决策端永久停在「撤单中」，而在途态是全额占用
        战略额度的（10-30 300502 的「废单」被撞键吞掉，锁死 3.6 万空间）。
        本端点让决策端能主动拉"网关已知的全部状态"来纠正本地，而不是干等一次推送。

        安全：与其余端点同一 Bearer 鉴权；只读；单账号（网关本身单账号）；
        `day` 只做格式校验后做前缀过滤，不接受 SQL。空 day 返回全部（人工排障用）。
        English: read-only same-day order-state snapshot for the decision side's pull reconciliation.
        """
        query = ""
        if request_handler is not None:
            query = urlparse(getattr(request_handler, "path", "") or "").query
        params = parse_qs(query or "")
        day = str((params.get("day") or [""])[0] or "").strip()
        if day and (len(day) != 10 or day[4] != "-" or day[7] != "-"):
            return 400, {"ok": False, "err": "day must be YYYY-MM-DD"}
        # §TOKEN-PERAACCT：对账源按调用方账号收窄——别人下的单不进你的账（历史上所有行
        # user_id 为空，故今天这个过滤是恒等的；签发之后才真正分流）。
        # 无归属行（手动单 ext:/历史遗留）对所有在册账号可见：宁可多看一条，也不让谁
        # 因为归属缺失就把自己那笔停滞单救不回来。owner 可显式 ?scope=all 看全量。
        caller = str(getattr(request_handler, "_caller_uid", "") or "") if request_handler is not None else ""
        scope_all = (str((params.get("scope") or [""])[0]).strip() == "all"
                     and caller == self.owner_user_id)
        out = []
        for row in self.store.list_orders():
            r = dict(row)
            created = str(r.get("created_at") or "")
            if day and created[:10] != day:
                continue
            row_uid = str(r.get("user_id") or "")
            # 只在"调用方账号明确"时分流；空 caller（旧调用点/直连测试）保持今天的恒等行为
            if not scope_all and caller and row_uid and row_uid != caller:
                continue
            out.append({
                "order_id": str(r.get("order_id") or ""),
                "signal_id": str(r.get("signal_id") or ""),
                "code": str(r.get("code") or ""),
                "side": str(r.get("side") or ""),
                "status": str(r.get("status") or ""),
                "price": float(r.get("price") or 0),
                "qty": int(r.get("qty") or 0),
                "created_at": created,
            })
        # 快照可信度随本响应一起下发：决策端阶段二**只有在新鲜且非空的委托表快照下**才允许
        # 按"查无此单"判终态（网关账被重置/桥刚起来时 snapshot_n=0，缺失就不是证据）。
        snap = getattr(self, "_orders_snap", None) or {}
        snap_ts = float(snap.get("ts", 0) or 0)
        return 200, {
            "ok": True, "day": day, "n": len(out), "orders": out,
            "snapshot_n": int(snap.get("n", 0) or 0),
            "snapshot_age": (max(0, int(time.time() - snap_ts)) if snap_ts > 0 else -1),
        }

    def _health_payload(self):
        """组装 /health 响应：进程存活 + 通道就绪 + 双通道（xt/queued）状态。

        §N-01（2026-10-20）：`ok` 只表示"网关进程能回 HTTP"，不再兼任"交易通道可用"。
        通道健康单列 `live_ready`（= active broker 已连接）；xt/feed 三连接位恒下发布尔，
        并补 `xt_last_fail_at`（最近一次 xt 连接失败时刻，枚举/时间戳，不是凭据）——
        这样"通道全断但进程活着"在看门狗、拨测与前端各有一份可读事实。
        English: §N-01 — `ok` means the HTTP process answers; channel readiness is `live_ready`.
        The three connection booleans and `xt_last_fail_at` are always present.
        """
        broker_connected = self.active_broker.is_connected()
        payload = {
            "ok": True,  # §N-01：仅进程存活语义（旧版被误读成"通道健康"）
            "ts": _now_cn(),
            "broker": self.active_key,
            "broker_connected": bool(broker_connected),
            "broker_mode": self.active_key,  # 与 Go 侧解析字段兼容的别名
            # 通道就绪：当前接单通道真的连上了才算 ready；进程活着但通道断时这里必须为 false
            "live_ready": bool(broker_connected),
            # 最近一次 xt 连接失败时刻（从未失败则为空串）；看门狗与 /readyz 用它回答"几点断的"
            "xt_last_fail_at": self._xt_last_fail_at,
        }
        # 三个连接位恒下发，避免消费方按"字段缺失"误判成未知/健康
        payload["xt_connected"] = bool(self.brokers["xt"].is_connected()) if "xt" in self.brokers else False
        if "queued" in self.brokers:
            payload["queued_connected"] = bool(self.brokers["queued"].is_connected())
        # §ENH-5：行情 feed 状态仅作观察字段——Go 侧 Health() 只解析 ok/broker_connected，
        # xtdata 断连绝不参与交易熔断判定（计划铁律：行情面不得污染 §GAP2-W1 fail-closed 面）
        payload["feed_connected"] = self.feed.is_connected()
        payload["feed_age_sec"] = self.feed.age_sec()
        # §P1-4（2026-10-30）：委托事件落库失败累计 + 委托表快照新鲜度。
        # 前者非零=终态回报曾写不进本地副本（已推给决策端的不受影响，但必须有人看）；
        # 后者 -1=从未收到过快照（撤单终态的唯一权威源断了），秒数过大=桥/通道停了。
        payload["order_fail_total"] = int(getattr(self.handler, "_order_store_fail_total", 0))
        # §TOKEN-PERAACCT 观测位：在册钥匙数（>1 才叫真"一账号一把"）、owner 是否声明、
        # 以及"事件有归属但注册表没这把钥匙"的回退次数——回退非零就说明引擎侧钥匙与网关注册表
        # 漂移了（配置发出去没登记），那些事件会被首尔按默认钥匙归到 owner 名下。
        payload["credential_accounts"] = len(self._tokens_by_uid)
        payload["owner_uid_set"] = bool(self.owner_user_id)
        payload["report_token_fallback_total"] = int(getattr(self.handler, "_report_token_fallback", 0))
        snap_ts = float(((getattr(self, "_orders_snap", None) or {}).get("ts", 0)) or 0)
        payload["orders_snapshot_age"] = (max(0, int(time.time() - snap_ts)) if snap_ts > 0 else -1)
        # §0926E2E-W2D：回报 outbox 观察面。溢出语义已从"删最旧"改为"冻结+只吵"，
        # 深度必须可见（daily_ops_check/看门狗按 outbox_overflow 排人），人工收敛走
        # outbox_admin.py --yes。读库失败记 -1 本身即观察信号，绝不让 /health 因它 500。
        try:
            depth = self.handler.store.outbox_count()
            payload["outbox_depth"] = int(depth)
            payload["outbox_overflow"] = bool(depth > self.handler._max_outbox)
        except Exception as e:  # noqa: BLE001
            payload["outbox_depth"] = -1
            payload["outbox_overflow"] = False
            log.warning("[gateway] /health outbox_depth 读取失败: %s", e)
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
            # §CLOSE-RECON：收盘快照事件打 close_snapshot=true，这里先记一笔观测位
            # （各事件类型随后走各自的处理分支）。
            if req.get("close_snapshot"):
                self._note_close_snapshot(etype, req)
            if etype == "positions":
                poss = req.get("positions") or []
                synced = req.get("synced")
                data_ts = req.get("data_ts")
                self.store.bridge_snapshot_set("positions", poss)
                # v3：把桥侧的"新鲜度"原样透传（None＝旧版桥没带这两个键，下游按未知处理，
                # 绝不把"没带"当成"是新鲜的"）。English: pass bridge freshness through untouched
                # (None = legacy bridge that doesn't carry these keys; never read as "fresh").
                self.handler.on_positions(poss, synced=synced, data_ts=data_ts)
                return 200, {"ok": True, "err": ""}
            if etype == "account":
                asset = req.get("asset")
                if asset:
                    synced = req.get("synced")
                    data_ts = req.get("data_ts")
                    self.store.bridge_snapshot_set("asset", asset)
                    self.handler.on_account(asset, synced=synced, data_ts=data_ts)
                return 200, {"ok": True, "err": ""}
            if etype == "order_result":
                return self._apply_order_result(req)
            if etype == "orders_snapshot":
                return self._apply_orders_snapshot(req)
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

    # §MANUAL-SYNC：本地在途单在柜台委托表里连续缺失多少次才判终态（6×10s≈60s 宽限，
    # 避免刚报出去/刚撤掉的单被下一轮快照误杀）。
    ORDER_MISS_LIMIT = 6

    def _note_close_snapshot(self, etype, req):
        """§CLOSE-RECON 记录桥侧收盘快照的观测位（时刻/行数/日期）。

        为什么要有这个位：miniQMT 已关停，收盘那 3 分钟（15:00-15:05）的成交只能靠
        桥在 15:01 打的收盘快照回传。快照没来 ⇒ 那几分钟的成交不在账上，必须靠对账单勘误。
        「最近一次收盘快照在哪天、有多少行」是这条链是否健康的唯一现网信号。
        English: records the bridge close-of-day snapshot (when, how many rows) so the
        operator can see whether the closing-auction fills could be reconciled at all.
        """
        data_ts = str(req.get("data_ts", "") or "")
        day = data_ts[:10] if data_ts else ""
        try:
            n = 0
            pos = 0
            trades = 0
            if etype == "orders_snapshot":
                n = len(req.get("orders") or [])
            elif etype == "trade":
                trades = 1
            elif etype == "positions":
                pos = len(req.get("positions") or [])
            self._close_snap = {
                "at": data_ts, "day": day, "ts": time.time(),
                "n": int(self._close_snap.get("n", 0) or 0) + n,
                "positions": int(self._close_snap.get("positions", 0) or 0) + pos,
                "trades": int(self._close_snap.get("trades", 0) or 0) + trades,
            }
        except Exception:  # noqa: BLE001 — 观测位坏了绝不影响事件处理
            log.warning("[gateway] §CLOSE-RECON 收盘快照观测位更新失败", exc_info=True)

    # §CANCEL-STATE（2026-10-28）：撤单请求发出后，券商委托表连着几轮仍显示该委托在途，
    # 才判定"撤单没生效"并回退在途态（默认 3 轮 ≈ 30s，快照 10s 一轮）。
    # 为什么要有这个窗口：撤单请求发出到柜台真正落状态之间有延迟，第一轮快照看到"还在途"
    # 是正常的；立刻回退会造成"发了撤单又被说没撤 → 再撤一次"的抖动。
    CANCEL_REJECT_LIMIT = 3

    def _apply_orders_snapshot(self, req):
        """§MANUAL-SYNC（2026-10-28）券商当日委托表快照：补手动单 / 推进我方单 / 收敛消失单。

        为什么需要这条腿：委托状态此前只有推送（回调动/桥回执）。手动在券商 App 下单或
        撤单不会产生任何回报；桥只在动笔后 8s 查一次 ORDER，之后只轮成交。结果是本地行
        永远停在「已报」，继续占住买入冻结额度与同票同日幂等键，新买入被门控拦死。
        ORDER 表是柜台自己的当日委托账（账号级），每 10s 全量拉一次即可让两侧收敛。

        安全边界（宁可晚收敛，绝不误杀真实在途单）：
          - synced=false（查询失败）→ 只告警，本轮不推进、不判定消失；
          - 空快照 → 不判定消失（"今天没委托"与"会话没同步"在桥上不可区分）；
          - 消失判定附加条件：当日单 + 连续缺失 ≥ ORDER_MISS_LIMIT 次 + order_id 是真实
            交易所单号（pending:/seq: 占位从未到券商，不在本函数职责内）；
          - 消失时的终态按已成交量决定：满量→已成、部分→部撤、零量→已撤。
        English: reconciles the counter's own ORDER table (account scoped) against the local
        book — adopts hand-placed orders under ext:<order_id>, advances statuses through the
        monotonic guard, and closes rows that keep missing from a trusted snapshot. This is
        the only way to observe a hand-cancel, which produces no callback at all.
        """
        rows = req.get("orders") or []
        if req.get("synced") is False:
            log.warning("[gateway] §MANUAL-SYNC 委托表快照未同步（查询失败），本轮不推进状态、不做消失判定")
            return 200, {"ok": True, "err": ""}
        by_id = {}
        for r in rows:
            oid = str((r or {}).get("order_id", "") or "")
            if oid:
                by_id[oid] = r
        self._orders_snap = {
            "by_id": by_id, "ts": time.time(),
            "data_ts": str(req.get("data_ts", "") or ""), "n": len(by_id),
        }
        # §ORDER-ID-REUSE / §8.5：拿到可信快照后，当日清一次"旧且不在柜台委托表里"的在途僵尸行。
        # 只做一次（每天首次可信快照），且逐行写日志，绝不按日期单独删在途行。
        today = _now_cn()[:10]
        if by_id and self._order_purge_day != today:
            try:
                self.store.purge_stale_orders(today, broker_ids=set(by_id.keys()))
                self._order_purge_day = today
            except Exception:  # noqa: BLE001 — 清理失败不影响快照处理
                log.warning("[gateway] §ORDER-ID-REUSE 快照后清理失败", exc_info=True)
        for oid, r in by_id.items():
            self._order_miss.pop(oid, None)
            try:
                self._apply_order_snapshot_row(oid, r)
            except Exception:  # noqa: BLE001 — 单行失败不拖垮整轮快照
                log.exception("[gateway] §MANUAL-SYNC 快照行应用失败 oid=%s", oid)
        try:
            self._resolve_missing_orders(by_id)
        except Exception:  # noqa: BLE001
            log.exception("[gateway] §MANUAL-SYNC 消失对账失败")
        return 200, {"ok": True, "err": ""}

    @staticmethod
    def _norm_snap_created(raw):
        """柜台时间串 → 量仔口径 "YYYY-MM-DD HH:MM:SS"。

        为什么必须归一：桥的 m_strInsertDate 是 8 位紧凑日期（20261028），而量仔的
        「当日委托」过滤、在途冻结/日期作用域全部按 "YYYY-MM-DD" 前缀匹配——
        不转格式，手动单会落库但永远不出现在列表里、也不进冻结账（看起来就像没回传）。
        """
        s = str(raw or "").strip()
        if not s:
            return ""
        head = s[:8]
        if len(s) >= 8 and head.isdigit():
            rest = s[8:].strip().replace("T", " ").strip()
            if not rest:
                rest = "00:00:00"
            elif rest.isdigit():
                rest = rest.zfill(6)[:6]
                rest = "%s:%s:%s" % (rest[:2], rest[2:4], rest[4:6])
            return "%s-%s-%s %s" % (head[:4], head[4:6], head[6:8], rest)
        return s

    def _apply_order_snapshot_row(self, oid, r):
        """快照行 → 委托事件：归因（备注 wire 形态 → 派发项前缀 → 交易所委托号），
        命中派发项用派发方向（权威），未命中按手动单挂 ext:<委托号>。状态未变化不重推。"""
        code = str(r.get("code", "") or "")
        remark = str(r.get("remark", "") or "")
        created = self._norm_snap_created(r.get("created_at", ""))
        day = created[:10]
        status = str(r.get("status", "") or "")
        if not status or status.startswith("未知"):
            # 读不懂的状态不冒充任何已知态（§W7-C 同姿势），计数留痕等人工核对
            self._orders_snap_skipped_unknown += 1
            log.warning("[gateway] §MANUAL-SYNC 委托表状态不可读，跳过 oid=%s code=%s status=%s",
                        oid, code, status)
            return
        drow = None
        if remark:
            drow = self.store.dispatch_by_signal_id(remark)
            if drow is None:
                drow, _why = self.store.dispatch_by_signal_prefix(remark, code, day)
        if drow is None:
            drow = self.store.dispatch_by_order_id(oid)
        if drow:
            sid = str(drow.get("signal_id", "") or "")
            if not sid:
                return
            # 柜台委托表里出现了这笔单 → 未确认观察项消项（§M-3 自我纠正）
            self._order_unconfirmed.pop(sid, None)
            side = str(drow.get("side", "") or r.get("side", "") or "")
        else:
            # 手动单：xt/Go 侧手工单的既有约定就是 ext:<order_id>（orders.signal_id UNIQUE），
            # 两侧同形，后续状态推进与撤单都按同一把键收敛。
            sid = "ext:" + oid
            side = str(r.get("side", "") or "")
        # order_by_signal 返回 sqlite3.Row（按下标/列名取值，没有 .get）
        cur = self.store.order_by_signal(sid)
        # §CANCEL-STATE（2026-10-28）撤单中 × 委托表快照的三态分叉：
        #   ① 快照给终态（已撤/部撤/已成/废单）→ 撤单生效，正常推进；
        #   ② 快照仍是已报/部成（还在途）→ 撤单**没生效**。此时**不能**直接推快照状态
        #      （那会把「撤单中」无声无息改回已报，操作人看不到"我发的撤单被拒了"），
        #      改为计数；连续 N 轮仍在途 → 显式推 §CANCEL-REJECT 拒因并回退在途态；
        #   ③ 快照给的是别的在途态 → 同上处理。
        if cur is not None and str(cur["status"] or "") == CANCEL_PENDING_STATUS:
            if status in ("已撤", "部撤", "废单", "已废", "已成"):
                self._cancel_pending_miss.pop(oid, None)  # 撤单生效，计数清零
            elif status in IN_FLIGHT_STATUSES:
                n = self._cancel_pending_miss.get(oid, 0) + 1
                self._cancel_pending_miss[oid] = n
                limit = int(self.cfg.get("cancel_reject_miss_limit", self.CANCEL_REJECT_LIMIT) or 0)
                if limit <= 0 or n < limit:
                    return  # 还在观察窗内：保持「撤单中」，不推任何状态
                log.warning("[gateway] §CANCEL-STATE 撤单未生效（连续 %d 轮委托表仍在途）oid=%s signal=%s "
                            "→ 推送撤单被拒并回退在途态", n, oid, sid)
                self._cancel_pending_miss.pop(oid, None)
                self.handler.on_order({
                    "order_id": oid, "signal_id": sid, "code": code, "side": side,
                    "status": status,
                    "price": float(r.get("price", 0) or 0),
                    "qty": int(r.get("qty", 0) or 0),
                    "created_at": created or _now_cn(),
                    "reason": "%s 券商委托表连续 %d 轮显示该委托仍在途，撤单未生效"
                              % (CANCEL_REJECT_PREFIX, n),
                    "at": _now_cn(),
                })
                return
        if (cur is not None and str(cur["status"] or "") == status
                and str(cur["order_id"] or "") == oid):
            return  # 状态与单号都没变：快照重放，不重复上报
        reason = ""
        if status in ("已撤", "部撤", "废单"):
            reason = "§MANUAL-SYNC 券商委托表状态=%s" % status
        self.handler.on_order({
            "order_id": oid, "signal_id": sid, "code": code, "side": side,
            "status": status,
            "price": float(r.get("price", 0) or 0),
            "qty": int(r.get("qty", 0) or 0),
            "created_at": created or _now_cn(),
            "reason": reason, "at": _now_cn(),
        })

    def _resolve_missing_orders(self, by_id):
        """本地「在途」单在可信快照里连续缺失 → 按成交量落终态（§MANUAL-SYNC）。

        为什么需要：手动撤单没有回报，行会永远停在已报；另外「网关已受理、柜台其实
        没有这笔单」的形态也只有靠柜台委托表证伪。判定条件见 _apply_orders_snapshot。
        """
        if not by_id:
            log.info("[gateway] §MANUAL-SYNC 委托表快照为空，跳过消失判定（不可信快照）")
            return
        limit = int(self.cfg.get("manual_sync_miss_limit", self.ORDER_MISS_LIMIT) or 0)
        if limit <= 0:
            return  # 运维可关闭消失判定（只保留"补齐/推进"，不做终态收敛）
        today = _now_cn()[:10]
        for row in self.store.list_orders():
            oid = str(row.get("order_id", "") or "")
            status = str(row.get("status", "") or "")
            # §CANCEL-STATE：「撤单中」也在在途集合里——它在柜台委托表里消失说明撤单生效了
            #（该走终态判定），不能因为"状态不是已报/部成"就把它当成非在途直接跳过。
            if status not in IN_FLIGHT_STATUSES:
                self._order_miss.pop(oid, None)
                continue
            if not oid or oid.startswith("pending:") or oid.startswith("seq:"):
                continue  # 占位引用本就不在券商表里：交给 §M16 / pending 收割
            if str(row.get("created_at", "") or "")[:10] != today:
                continue
            if oid in by_id:
                continue
            n = self._order_miss.get(oid, 0) + 1
            self._order_miss[oid] = n
            if n < limit:
                continue
            qty = int(row.get("qty", 0) or 0)
            try:
                filled = int(self.store.order_filled_qty(oid) or 0)
            except Exception:  # noqa: BLE001
                filled = 0
            if qty > 0 and filled >= qty:
                new_status = "已成"
            elif filled > 0:
                new_status = "部撤"
            else:
                new_status = "已撤"
            reason = "§MANUAL-SYNC 券商委托表连续 %d 次缺失（疑似手动撤单/柜台未受理）" % n
            log.warning("[gateway] §MANUAL-SYNC 委托在柜台委托表中消失 oid=%s signal=%s code=%s "
                        "本地=%s（已成交 %d/%d）→ %s",
                        oid, row.get("signal_id"), row.get("code"), status, filled, qty, new_status)
            self.handler.on_order({
                "order_id": oid, "signal_id": row.get("signal_id", ""),
                "code": row.get("code", ""), "side": row.get("side", ""),
                "status": new_status,
                "price": float(row.get("price", 0) or 0), "qty": qty,
                "created_at": row.get("created_at", "") or _now_cn(),
                "reason": reason, "at": _now_cn(),
            })
            self._order_miss.pop(oid, None)
        self._resolve_unconfirmed_orders(by_id)

    def _resolve_unconfirmed_orders(self, by_id):
        """§M-3/§MANUAL-SYNC：未获柜台确认的单，宽限期后收敛为「发送失败」。

        为什么需要：桥的 passorder 不抛异常就报 ok，但柜台可能事后拒单；这类单的
        order_result 已经结算（§M16 的 inflight 收割够不到），如果没有这条出口，
        本地行会永挂「已报」——既占买入冻结，又把同票同日的幂等键占死，新买入被拦。
        安全边界：
          - 只在可信（非空）快照轮次里判，且给宽限期（默认 60s）等柜台入表；
          - 快照里若存在「同代码+同方向+同数量」的无备注行，无法证伪就不收敛
            （remark 可能被柜台吞掉，宁可留待人工也不误判成未到券商）；
          - 只回写可重试的「发送失败」，绝不重发。
        English: after a grace window, an order the counter never confirmed is settled as
        send-failed (retryable) unless an unattributed same-shape row exists in the snapshot.
        """
        grace = float(self.cfg.get("manual_sync_unconfirmed_grace_sec", 60) or 0)
        if grace <= 0 or not self._order_unconfirmed:
            return
        now = time.time()
        # 无备注（无法归因）的柜台行指纹集：能对上就不许判"未到券商"
        unattributed = set()
        for r in by_id.values():
            if str(r.get("remark", "") or ""):
                continue
            unattributed.add((str(r.get("code", "") or ""), str(r.get("side", "") or ""),
                              int(r.get("qty", 0) or 0)))
        for sid, since in list(self._order_unconfirmed.items()):
            if now - since < grace:
                continue
            row = self.store.order_by_signal(sid)
            if row is None:
                self._order_unconfirmed.pop(sid, None)
                continue
            status = str(row["status"] or "")
            if status not in ("已报", "已报待撤"):
                self._order_unconfirmed.pop(sid, None)
                continue
            fp = (str(row["code"] or ""), str(row["side"] or ""), int(row["qty"] or 0))
            if fp in unattributed:
                continue  # 证伪不了：柜台有同形无备注行，留人工核对
            reason = ("§MANUAL-SYNC 柜台委托表 %d 秒内始终没有这笔委托且下单未获确认，"
                      "视为未到券商（可重试）" % int(grace))
            log.warning("[gateway] §MANUAL-SYNC 未确认单收敛为发送失败 signal=%s order=%s code=%s",
                        sid, row["order_id"], row["code"])
            self.handler.on_order({
                "order_id": row["order_id"], "signal_id": sid,
                "code": row["code"], "side": row["side"], "status": "发送失败",
                "price": float(row["price"] or 0), "qty": int(row["qty"] or 0),
                "created_at": row["created_at"] or _now_cn(),
                "reason": reason, "at": _now_cn(),
            })
            self._order_unconfirmed.pop(sid, None)

    def _apply_order_result(self, req):
        """下单结果回报：结算派发项；ok→已报（回填交易所委托号），失败→已废（带拒因）。

        §M-3（2026-09-22 修复批）委托腿二次确认：桥的 passorder 只要「不抛异常」就
        return True（qmt_bridge_strategy.py:601-611），旧实现据此一路上报「已报」，
        而柜台**事后**拒绝（资金/权限/涨跌停/合约状态）时委托腿没有任何补偿轮询——
        成交腿有 `_bridge_tick` 的 DEAL 轮询，委托腿没有 → 本地永驻「已报」。
        现在桥在回报里附带 `order_confirmed`（ORDER 表轮询是否真的见到这笔委托，
        复用 embed_resolve 那条 8s 轮 ORDER 的路径），本端据此把「已报」降级为
        **显式未确认**：状态字面量仍保持「已报」，理由是首尔侧的委托状态机与资金/
        敞口闸全部按 `status IN ('已报','部成',…)` 精确匹配（internal/store/real_positions.go
        LocalBuyFrozen / SumOpenSellQty、qmt.go 的可撤判定），改成任何未知状态字面量
        会让在途单从冻结额与可撤集合里凭空消失（比"停在已报"更危险，且不在本次可改的
        Go 侧范围内）。因此这里做到的是**可区分 + 可取证**：
          - reason 带 §M-3 显式拒因文案（回报载荷字段集不变，Go 已按 reason 落日志/运维流水）；
          - dispatch 行 result JSON 记 `confirmed: false`（对账/巡检可查）；
          - WARNING 日志留痕，网关绝不因"未确认"重发订单（幂等锚 ok/seq 语义一字未动）。
        English: §M-3 — the bridge now publishes whether the counter's ORDER table really
        showed the order. Unconfirmed accepts keep the 已报 literal (the Seoul-side state
        machine and the cash/open-qty gates match on it exactly) but carry an explicit
        §M-3 reason, a `confirmed:false` dispatch marker and a warning log; nothing is
        ever re-sent here, and the ok/seq idempotency anchor is unchanged.
        """
        # seq 是派发项主键：桥回执只带 seq + 结果，方向/代码等语义字段从派发行取（权威在本端）。
        seq = str(req.get("seq", "") or "")
        ok = bool(req.get("ok"))
        order_id = str(req.get("order_id", "") or "")
        err = str(req.get("err", "") or "")
        # §M-3 未确认标记：只在 ok 且桥**显式**回报 order_confirmed=false 时降级；
        # 字段缺失（旧版桥/mock）按已确认处理，避免把存量通道一律打成可疑单。
        confirmed_raw = req.get("order_confirmed", True)
        confirmed = False if (ok and confirmed_raw is False) else True
        unconfirm_reason = ("§M-3 柜台委托未确认：下单已被客户端受理但桥在 ORDER 表轮询窗口内"
                            "未见到该委托，请人工核对柜台侧（网关未重发）")
        row = self.store.dispatch_set_result(
            seq, {"ok": ok, "order_id": order_id, "err": err, "confirmed": bool(confirmed)})
        if row is None:
            return 404, {"ok": False, "err": "unknown seq: %s" % seq}
        signal_id = row.get("signal_id", "")
        if not signal_id:
            log.warning("[gateway] order_result without signal_id: seq=%s", seq)
            return 200, {"ok": True, "err": ""}
        ts = _now_cn()  # §TZ
        if ok:
            # §MANUAL-SYNC：下单被客户端受理但柜台 ORDER 表没见到这笔单（§M-3 未确认），
            # 记一笔带时刻的观察项——后续每轮委托表快照里若始终没有它的 remark，就在
            # 宽限期后回写「发送失败」解锁同 signal 重试（否则这行会永挂已报：
            # order_result 已结算，§M16 的 inflight 收割也够不到它）。
            if not confirmed:
                self._order_unconfirmed[signal_id] = time.time()
            else:
                self._order_unconfirmed.pop(signal_id, None)
            if not confirmed:
                log.warning("[gateway] §M-3 委托腿未获柜台确认 seq=%s signal=%s code=%s side=%s "
                            "ref=%s —— 上报 reason 已标注未确认，状态字面量保持已报（见 §M-3 说明）",
                            seq, signal_id, row.get("code"), row.get("side"), order_id or seq)
            self.handler.on_order({
                "order_id": order_id or seq, "signal_id": signal_id, "code": row.get("code"),
                "side": row.get("side"), "status": "已报",
                # §M-3 未确认单必须让首尔/运维看得见：复用契约里已有的 reason 字段承载
                # 显式文案（不新增契约字段，避免 §A2/§F2 三点契约校验漂移）。
                "reason": "" if confirmed else unconfirm_reason,
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
        """撤单结果回报：ok→「撤单中」；失败→带 §CANCEL-REJECT 拒因退回在途态。

        §CANCEL-STATE（2026-10-28）语义更正：网关回的 ok=True 只代表"QMT 客户端受理了
        撤单调用"，**不是交易所确认撤成**。旧实现直接推「已撤」，于是 2026-10-28 的
        600362（15:00 集合竞价成交、撤单被交易所拒绝）在本地被记成"已撤"，实际成交
        既没有 fills 也没有持仓。现在只推「撤单中」，终态由券商委托表快照裁决。

        失败（ok=False）→ 推在途态并带 §CANCEL-REJECT 前缀：这是**唯一**允许把「撤单中」
        退回在途态的显式事件（迟到的快照不许回退，否则会陷入"撤→快照说还在途→再撤"循环）。
        """
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
        if ok:
            # 请求已受理、交易所未确认 → 「撤单中」（非终态，冻结照算）
            self.handler.on_order({
                "order_id": row.get("order_id") or seq, "signal_id": signal_id, "code": row.get("code"),
                "side": row.get("side"), "status": CANCEL_PENDING_STATUS,
                "price": 0.0, "qty": 0,
                "reason": "§CANCEL-STATE 撤单请求已被客户端受理，等待交易所确认",
                "created_at": row.get("created_at") or ts, "at": ts,
            })
        else:
            # 撤单被拒（收盘集合竞价/已成交/柜台拒单）：显式拒因，允许回退在途态
            reason = "%s 撤单被拒: %s" % (CANCEL_REJECT_PREFIX, err or "网关未给出原因")
            log.warning("[gateway] §CANCEL-STATE 撤单被拒 seq=%s signal=%s err=%s", seq, signal_id, err)
            self.handler.on_order({
                "order_id": row.get("order_id") or seq, "signal_id": signal_id, "code": row.get("code"),
                "side": row.get("side"), "status": "已报",
                "price": 0.0, "qty": 0, "reason": reason,
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

        §SIDE-AUTH-2（2026-09-23 夜间批）方向权威补漏：上面那条规矩只在**查到派发行**时成立。
        三级回落（seq→交易所委托号→signal_id）全落空时，旧实现不加任何标记地直接采用桥/柜台
        推断入库——而桥侧 handler._side_of 是多枚举空间猜测（order_type 23/24 vs 1101/1102、
        offset_type 48/50），代码自己记着已两次踩坑；猜错＝主动把卖出说成买入，Go 侧照单
        入库并改持仓账（2026-09-22 603468.SH 事故链的源头）。现在未命中派发行（或命中但
        派发行为空方向）的成交一律打 `side_unverified=true` + 单独 warning 留痕，
        由 handler.on_trade → store.apply_fill 走「待核对」通道（与 UNRESOLVED_STATUS 同姿势：
        落库保证据、绝不动持仓账）；命中派发行则显式剥除该键，既有权威覆盖路径零变化。
        English: when no dispatch row backs the report, the bridge's guessed side is no longer
        silently trusted — the fill is flagged side_unverified, warned once, and booked through
        the unresolved (待核对) channel that leaves positions untouched.
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
        # §SIGID-TRUNC 第四级回落：按**前缀**反查派发项。
        # 为什么必须有这一级：柜台的 userOrderId 槽是短字段，桥提交的 signal_id（本仓形态
        # `buy:603468:fac_1:20260922`，25 字符）到柜台只剩前 24 位，成交回报里的 remark 也就
        # 是这 24 位——于是上面三级（seq→交易所委托号→signal_id 精确查）里最该命中的那一级
        # 恒不命中（dispatch_by_signal_id 是等值查）。后果不止归因缺失：查不到派发行 ⇒ 方向
        # 无背书 ⇒ 每一笔这样的成交都被 §SIDE-AUTH-2 打进「待核对」通道（不动持仓账、等人工），
        # 实盘等于**成交永远入不了账**；而落库的 signal_id 是截断值 ⇒ Go 侧按 signal_id 前缀
        # 聚合的部成在途净额（SumFilledQty）也永不命中。
        # 为什么还原放在网关而不是桥里：权威只有派发表（下单时我们亲手写下的物理事实），
        # 桥没有这张表、只能报柜台给的原始值（桥侧也不该"顺手补全"——补出来的就不是柜台证据了）。
        # 歧义口径：同前缀＋同代码＋同交易日内候选必须 (signal_id, side, code) 完全一致才认，
        # 否则 store 返回 ambiguous ⇒ 这里绝不猜，保持"查不到派发行"的原样走待核对，并留一条
        # warning（真出现说明 signal_id 生成或派发链有更深问题，得让人看见）。
        # English: 4th-level attribution by prefix, because the counter only echoes the truncated
        # wire form of our signal id. A unique candidate set (same code/day, agreeing on
        # signal_id+side+code) is required; ambiguity is left un-repaired and warned.
        _sig_wire = "" if drow else str(req.get("signal_id", "") or "")
        if _sig_wire:
            _drow, _why = self.store.dispatch_by_signal_prefix(
                _sig_wire, str(req.get("code", "") or ""), str(req.get("traded_at", "") or "")[:10])
            if _drow is None and _why == "ambiguous":
                log.warning("[gateway] §SIGID-TRUNC 前缀归因歧义（同前缀多派发项不一致），"
                            " 不猜、按未证方向处理: signal_prefix=%s code=%s oid=%s traded_at=%s",
                            _sig_wire, req.get("code", ""), req.get("order_id", ""),
                            req.get("traded_at", ""))
            elif _drow:
                _full = str(_drow.get("signal_id", "") or "")
                if _full and _full != _sig_wire:
                    # 还原本身要留痕：账本里从此是完整编号，与柜台回单/历史行的截断值不同，
                    # 对表时得知道哪个是原始值（orig 值仍完整保存在 fills 原行的勘误语义里）。
                    log.warning("[gateway] §SIGID-TRUNC 成交回报 signal_id 为柜台截断值，"
                                " 已按派发项还原: %s -> %s (code=%s oid=%s trade_id=%s)",
                                _sig_wire, _full, req.get("code", ""), req.get("order_id", ""),
                                req.get("trade_id", ""))
                req["signal_id"] = _full or _sig_wire
                drow = _drow
        if drow:
            # 代码回填（与方向同源的权威性）：带 signal_id 的回报不再走上面的归因分支，
            # 若其 code 缺失，同样以派发项为准。
            # §SELLFILL-DECOUPLE（2026-10-06 修复批 波 2）改了这条理由：旧文写"否则 apply_fill
            # 会拿空代码查持仓、卖出被判为『无底仓 no-op』而静默漏账"——那半句现在不成立了
            # （无持仓的卖出也已无条件落 fills，见 store.apply_fill）。但回填**照旧必需**，
            # 欠的从"流水"变成"减仓"：空代码查不到持仓行 ⇒ 这笔真实卖出的股票没从本地账扣掉，
            # 本地持仓比真实仓位重，下一轮就会按并不存在的量挂卖出单。
            # English: code backfill stays mandatory, only the reason changed — an empty code no
            # longer loses the fills journal (that was the §SELLFILL-DECOUPLE defect), it now loses
            # the position *reduction*, leaving the local book heavier than the real position.
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
        # §SIDE-AUTH-2 方向权威补漏：只有「查到派发行且派发行带方向」才算方向已证。
        # 为什么未命中派发行不能信推断：桥/柜台方向是从多个互不兼容的枚举空间反推出来的
        # （本项目 2026-08-31、2026-09-14 两次实锤判反），猜错就是把卖出说成买入——这正是
        # 本批（§SIDE-AUTH-2）要根治的 603468.SH 事故形态（09-22 真实卖出记成买入）；
        # 而派发行是我们自己下单时写下的物理事实，零推断。
        # 派发行在但 side 为空属退化数据，方向同样不可证，一并标记（宁可多一条待核对，
        # 也不许再出现"静默采信猜测"）。命中者显式剥除该键——既有已证路径的字段面零变化。
        # English: only a dispatch row carrying a side can vouch for the direction; anything
        # else is marked side_unverified and routed to the unresolved channel (positions untouched).
        #
        # §MANUAL-SYNC（2026-10-28）手动成交回传：未命中派发行的成交此前一律进「待核对」，
        # 于是券商 App 里手买的票永远不进成交/盈亏账。现在补一个**独立第二来源**：
        # 券商委托表里同一委托号的方向若与成交行推断方向一致（两个柜台字段族互相印证，
        # 不是单源猜测），就按外部成交入账，信号键用 ext:<委托号>（与 Go 侧手工单占位键同形）。
        # 两源不一致/查不到 → 维持待核对（绝不拿单源猜测动持仓账）。
        _manual_vouched = False
        if drow is None:
            _moid = str(req.get("order_id", "") or "")
            _msnap = (self._orders_snap.get("by_id") or {}).get(_moid) or {}
            _minferred = str(req.get("side", "") or "")
            _mside = str(_msnap.get("side", "") or "")
            if (_moid and _minferred in ("买入", "卖出") and _mside == _minferred):
                _manual_vouched = True
                req["signal_id"] = "ext:" + _moid
                if not req.get("code"):
                    req["code"] = _msnap.get("code", "")
                log.info("[gateway] §MANUAL-SYNC 外部（手动）成交入账：oid=%s code=%s side=%s qty=%s"
                         "（委托表方向与成交行一致）",
                         _moid, req.get("code"), _minferred, req.get("qty"))
        _side_vouched = (bool(drow) and bool(str((drow or {}).get("side", "") or ""))) \
            or _manual_vouched
        if _side_vouched:
            req.pop("side_unverified", None)
        else:
            req["side_unverified"] = True
            log.warning("[gateway] §SIDE-AUTH-2 成交回报未命中派发行，方向仅为桥/柜台推断（不可采信入库）"
                        " —— 转「待核对」通道: code=%s order_id=%s seq=%s inferred=%s",
                        req.get("code"), req.get("order_id", ""), req.get("seq", ""),
                        req.get("side", ""))
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
        _ev = {
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
        }
        # §SIDE-AUTH-2：方向未获派发行证实的成交带标记入库+上报——落库走「待核对」通道
        # （fills 行保留全部可复核字段但不动持仓账），上报侧 Go 凭该标记留痕拒入账本。
        # 命中派发行的回报**不带该键**（保持既有契约字段面，避免无谓的契约噪声）。
        if req.get("side_unverified"):
            _ev["side_unverified"] = True
        self.handler.on_trade(_ev)
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
        # §CLAIMRELEASE 观察位：第三态「待核对」清单 + 派发队列纵深索引是否在位。
        # 这两项都是"要人去看才知道"的资金安全事实，塞进既有运维端点而不是另开告警通道
        # （网关侧没有独立告警出口，log.error + /admin/status 是本仓既有惯例）。
        try:
            unresolved = self.store.list_unresolved_pending()
        except Exception:  # noqa: BLE001 — 观察端点绝不因账本异常而 500
            unresolved = []
        payload["unresolved_orders"] = [
            {"signal_id": r.get("signal_id"), "code": r.get("code"), "side": r.get("side"),
             "qty": r.get("qty"), "created_at": r.get("created_at"),
             # 取证用：这条「待核对」信号是否还在派发队列里（在 = 桥迟早会回报并自动收敛；
             # 不在 = 只能人工核对柜台，走 POST /admin/order-confirm）
             "dispatch_in_flight": self.store.dispatch_active_order_exists(
                 str(r.get("signal_id", "") or ""))}
            for r in unresolved[:20]]
        payload["unresolved_count"] = len(unresolved)
        # §P1-4（2026-10-30）撤单中卡单观察位：网关本地账口径（与 Go 侧 funds 帧的
        # pending_buy_n 对照，两边对不上就说明事件在链路上丢了）。
        try:
            stuck_n, stuck_age = self.store.stuck_cancel_stats()
        except Exception:  # noqa: BLE001 — 观察位坏了不影响状态端点
            stuck_n, stuck_age = -1, 0
        payload["stuck_cancel_orders"] = {"n": int(stuck_n), "oldest_age_sec": int(stuck_age)}
        payload["dispatch_signal_guard"] = bool(getattr(self.store, "dispatch_signal_guard", False))
        # §P2-G 观察位：命令文件写失败（单仍 pending、下轮重推）的三个计数。
        # streak>0 = 此刻还在失败，total 只用于区分抖动与系统性故障（磁盘满/目录权限/
        # 路径被删）。放在 /admin/status 而不是另开告警通道，与 unresolved_orders 同姿势。
        payload["file_bridge_push"] = {
            "fail_total": int(self._file_bridge_push_fail_total),
            "fail_streak": int(self._file_bridge_push_fail_streak),
            "fail_held": int(self._file_bridge_push_fail_held),
        }
        # §W7-C 观察位：xtquant 委托状态码映射失败（fail-closed 后落「未知(原始值)」）的累计次数
        # 与最后一次原始值。为什么必须在这里看得见：「未知…」串不进引擎侧三本资金账
        # （买入冻结 / 跨日降废 / 在途卖量都按精确中文状态串匹配），所以它**不会**以资金异常的
        # 形态浮出来；total>0 就是"柜台有一种我们读不懂的状态码"的唯一现网信号，
        # 需要按 last 的原始值把该码补进 handler.XT_STATUS_CODES。
        payload["unknown_status"] = {
            "total": int(getattr(self.handler, "unknown_status_total", 0) or 0),
            "last": str(getattr(self.handler, "last_unknown_status", "") or ""),
        }
        # §MANUAL-SYNC 观察位：委托表快照是否在跑、最新快照行数/时刻、跳过多少不可读状态。
        # 「快照 n=0 且盘中有我方在途单」＝桥侧 ORDER 表不可见（会话未同步/构建差异），
        # 是手动单无法回传的唯一外部前提，必须能一眼看到。
        payload["orders_sync"] = {
            "snapshot_n": int(self._orders_snap.get("n", 0) or 0),
            "snapshot_ts": str(self._orders_snap.get("data_ts", "") or ""),
            "miss_pending": len(self._order_miss),
            "unknown_status_skipped": int(self._orders_snap_skipped_unknown),
            "miss_limit": int(self.cfg.get("manual_sync_miss_limit", self.ORDER_MISS_LIMIT) or 0),
        }
        # §CLOSE-RECON 观察位：最近一次收盘快照（桥 15:01 定时任务打的那一帧）。
        # day 为空＝桥从来没打过收盘快照 ⇒ 收盘那 3 分钟的成交不在账上，
        # 只能靠对账单勘误；这是 600362 事故的现网判据。
        payload["close_snapshot"] = {
            "day": str(self._close_snap.get("day", "") or ""),
            "at": str(self._close_snap.get("at", "") or ""),
            "orders_n": int(self._close_snap.get("n", 0) or 0),
            "positions_n": int(self._close_snap.get("positions", 0) or 0),
            "trades_n": int(self._close_snap.get("trades", 0) or 0),
        }
        return 200, payload

    def _do_admin_order_confirm(self, body):
        """§CLAIMRELEASE 第三态的**人工确认**收敛出口（POST /admin/order-confirm）。

        用法：核对券商侧委托后二选一——
          {"signal_id":"...","decision":"released"}        柜台确无此单 → 删占位、解锁该信号，
                                                          后续同 sid 可正常重新下单；
          {"signal_id":"...","decision":"settled","order_id":"123"}（可选）
                                                          柜台已有此单 → 把占位行改写成正常
                                                          终态（默认「已撤」，可指定），
                                                          order_id 给了就一并回填。
        为什么只认第三态行：released 的谓词是 status='待核对'，误传 signal_id 不会把正常委托
        或普通 pending 占位删掉；本端点**不重发**任何订单（与 §M-2 的"只解锁不重发"一致）。
        English: §CLAIMRELEASE — the manual reconciliation exit for the third state; it only
        touches rows in 待核对 and never re-issues an order.
        """
        req = body or {}
        sid = str(req.get("signal_id", "") or "")
        decision = str(req.get("decision", "") or "").strip().lower()
        if not sid:
            return 400, {"ok": False, "err": "signal_id required"}
        if decision not in ("released", "settled"):
            return 400, {"ok": False, "err": "decision must be 'released' or 'settled'"}
        row = self.store.order_by_signal(sid)
        if row is None:
            return 404, {"ok": False, "err": "no such signal_id in local book"}
        row = dict(row)  # store.order_by_signal 返回 sqlite3.Row，统一转 dict 后再取字段
        if row.get("status") != UNRESOLVED_STATUS:
            return 409, {"ok": False,
                         "err": "order is not in %s state (status=%s) — nothing to confirm" % (
                             UNRESOLVED_STATUS, row.get("status"))}
        if decision == "released":
            n = self.store.release_unresolved_pending(sid)
            self._unresolved_alerted.discard(sid)
            log.warning("[gateway] §CLAIMRELEASE 人工确认：signal_id=%s 柜台侧无此委托，"
                        "已删除「待核对」占位解锁该信号（本端未重发任何订单）→ 可重新下单", sid)
            return 200, {"ok": True, "released": bool(n), "err": ""}
        # settled：回填真实委托号（可空）并推进到正常终态，之后 claim 走幂等返回分支
        status = str(req.get("status", "") or "已撤")
        order = dict(row)
        order["status"] = status
        oid = str(req.get("order_id", "") or "")
        if oid:
            order["order_id"] = oid
        self.store.upsert_order(order)
        self._unresolved_alerted.discard(sid)
        log.warning("[gateway] §CLAIMRELEASE 人工确认：signal_id=%s 转正常终态 status=%s "
                    "order_id=%s（占位保留为正式委托行，同 sid 后续下单走幂等返回）",
                    sid, status, oid or "(未提供，沿用占位引用)")
        return 200, {"ok": True, "status": status, "err": ""}

    def _do_order(self, body, caller_uid=""):
        """处理 POST /order 下单：参数校验 → 幂等占位 → 真实下单 → 回填委托号。

        :param caller_uid: 入向钥匙解析出的下单账号（§TOKEN-PERAACCT）；空=回退 owner（旧形态）。

        校验 code/qty/signal_id（非空）、限价单必须有价格；按板块整手规则校验买入数量；
        signal_id 为空一律拒绝（幂等与审计唯一锚点，§G2）；同 signal_id 幂等去重。
        §SIDEGATE-PY（M-1）方向白名单：side ∉ {买入,卖出} 一律 400（不再"按买入校验、
        下成卖单"）；§M-2 下单窗口 try/finally：place_order/settle 任何异常路径都不遗留
        占位（网关自身永不重发）。
        §CLAIMRELEASE（N-8）把 §M-2 那条不变式收窄为「按是否已交给通道分流」：
        未交给通道 → 释放占位（同信号可重试）；已交给通道而结算失败 → 占位保留并转
        第三态「待核对」，同 sid 后续一律 409 + 告警（宁可拒单也不双卖）。
        English: handles POST /order — validates params (incl. the hard side whitelist),
        takes the idempotent placeholder, places the order via the broker, and fills back
        the exchange order id; the placeholder is only released on paths where the order
        never reached the broker (§CLAIMRELEASE).
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

        # §SIDEGATE-PY（2026-09-22 修复批，M-1 升级 H-5）方向白名单——缺陷原文：
        # 旧实现 `side = req.get("side","")` 之后**没有任何取值校验**，只在下一步判
        # `if side == "卖出"`，else 分支把「一切非法串」按买入整手规则放行；而真正决定
        # 柜台方向的 broker.py:310 与 qmt_bridge_strategy.py:563/769 都是
        # `买入 才买、否则卖` 的三元式 → 一个拼错的方向（"buy"/"SELL"/" 买入 "/"")
        # 会被「按买入校验、下成卖单」，并顺带让首尔 risk.Gate 中按 Side 精确匹配的
        # T+1 限售/涨停拒买/跌停拒卖三道闸同时不触发（M-1 定性：方向翻转 + 风控失效）。
        # 为什么这么改：在入口即拒（400）而不是在末端猜——网关不复算方向语义，
        # 任何非白名单取值都无法安全落地。错误信息带上实际收到的值（repr，含引号/空格）
        # 便于排障一眼看出是上游装配错还是编码错。校验刻意发生在 claim 之前：
        # 与 §A1 金额帽同口径，不消耗 signal_id 幂等占位；4xx 让 Go 下单口直接失败返回，
        # 不进 outbox 重试链（重试也不会改变入参，无限重试只会刷屏）。
        # English: §SIDEGATE-PY — hard whitelist at the door; previously any junk side was
        # validated as a buy but *executed* as a sell by the broker-side ternary.
        raw_side = req.get("side", "")
        if not (isinstance(raw_side, str) and raw_side in ORDER_SIDES):
            log.warning("[gateway] §SIDEGATE-PY 非法下单方向，拒 400: side=%r signal=%s code=%s",
                        raw_side, signal_id, code)
            return 400, {"ok": False,
                         "err": "side must be one of %s, got %r" % ("/".join(ORDER_SIDES), raw_side)}
        side = raw_side
        # 按板块取申报单位规则后做整手校验（方向已被白名单收口：else 分支必为买入）
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
        # §TOKEN-PERAACCT：这一行归属**下单方账号**（入向钥匙解出来的 uid），解不出来才回退 owner。
        # 它是后续回报归属的锚：网关推给决策端时按这条行的 user_id 决定"这单是谁的"。
        # §TOKEN-PERAACCT：这一行的归属就是"用哪把钥匙进来的人"——请求体里自带 user_id 一律**覆盖**，
        # 绝不采信客户端自报（自报归属等于把记账权交给调用方）。解不出钥匙时才回退通道级账号。
        place_uid = str(caller_uid or self.user_id or "")
        req["user_id"] = place_uid
        draft = {
            "signal_id": signal_id, "code": code, "side": side,
            "price": price, "qty": qty,
            "created_at": req.get("created_at") or "",
            "user_id": place_uid,  # §P1-9 多账号隔离归属（§TOKEN-PERAACCT 起为真实下单账号）
        }
        _t0 = time.time()  # §PERF 下单 accept 延迟埋点（性能门禁 G3 数据源）
        claimed, existing = self.ids.claim(draft)
        # 没抢到占位 = 已下过（幂等返回）或正在下单中（409 防并发穿透）
        if not claimed:
            existing = existing or {}
            oid = existing.get("order_id") or ""
            status = str(existing.get("status") or "")
            # §ORDER-ID-REUSE（2026-10-30）：**终态行不许假成功**。
            # 旧实现把"不是占位号"一律回 200 + 原单号，于是决策端重发一笔已撤/已废的信号时
            # 会拿到"受理成功"，把本地行改回「已报」并再次撤单——造出一笔券商根本不存在的
            # 在途单（10-30 002415 的 09:39 就是这样，之后没有任何回报能收敛它）。
            # 现在明确回 409 + code=terminal，调用方据此把该 signal_id 记为当日终结。
            if status in TERMINAL_STATUSES:
                log.warning("[gateway] §ORDER-ID-REUSE signal=%s 重发被拒：该键已终态(%s, order=%s)，"
                            "不重复下单", signal_id, status, oid)
                return 409, {"ok": False, "code": "terminal", "status": status,
                             "err": "signal_id already terminal (%s); not re-placed" % status}
            # §CLAIMRELEASE 第三态单独一条错误文案：普通 in-flight 与「已交给通道但结果未知」
            # 在处置上完全不同（前者等一等就好，后者必须人工核对柜台），共用一句
            # "duplicate signal_id in-flight" 会让运维与首尔侧都看不出差别。
            if status == UNRESOLVED_STATUS:
                log.error("[gateway] §CLAIMRELEASE signal=%s 处于「结果未知/待核对」态，本次 /order "
                          "直接拒绝（该行代表订单可能已在券商侧，宁可拒单也不重复下单）；"
                          "请核对柜台委托后用 POST /admin/order-confirm 收敛", signal_id)
                return 409, {"ok": False,
                             "err": "signal_id unresolved (already handed to broker, awaiting "
                                     "reconciliation) — 结果未知/待核对，拒绝重复下单"}
            if is_placeholder_order_id(oid):
                return 409, {"ok": False, "err": "duplicate signal_id in-flight"}
            # 幂等：已下过 → 返回原委托引用（不重复下单）
            return 200, {"ok": True, "order_id": oid, "err": ""}

        # §M-2（2026-09-22 修复批）下单窗口异常保护——缺陷原文：旧实现
        # `ok, order_ref, err = self.active_broker.place_order(req)`（旧 :1042）裸调用，
        # 通道抛异常（xt IPC 抖动、桥写命令文件失败、结果装配 KeyError…）时异常冒泡到
        # _Handler._dispatch（:1225 的 §G8 顶层保护）被吞成 500，**但本函数已抢到的
        # pending 占位不会 release** → 同 signal_id 之后每次重试都恒 409
        # 「duplicate signal_id in-flight」，该信号永久死锁（M-2 定性）。
        # 为什么这么改：try/finally 守住「未 settle 即释放占位」这一条不变式——无论异常
        # 从 place_order 还是 settle 冒出，占位都不会遗留；异常仍回 500（与旧 HTTP 状态码
        # 一致，Go 侧不新增重试语义），并由运行期巡检 _sweep_stale_pending 兜底。
        # 语义边界（ids.py:5-10 的 fail-safe 结论保持不变）：这里释放的只是**本地占位行**，
        # 网关绝不自动重发；单可能已到达柜台，是否重试由调用方（首尔）决策，
        # 防重复下单的最后防线仍是 claim 的 signal_id 唯一键 + 「只清理未结算占位」。
        # English: §M-2 — the claim is no longer left rotting on any non-settled path.
        #
        # §CLAIMRELEASE（2026-09-22 晚批 N-8）把这条不变式**收窄**：「未 settle 即释放」过宽——
        # 它把「从未发送」与「已入队但结算失败」混为一谈。后者被释放后，Go 侧按
        # 「重试次数+1」的有限重试会再次 claim 成功并二次入队（dispatch 表当时没有
        # signal_id 唯一约束）= 双卖/双买。现在 finally 按 sent_to_broker 分流：
        #   False（place_order 抛异常或显式返回失败 = 通道没受理）→ 删占位，§M-2 治死锁的
        #     原语义原样保留（该信号还能再下单）；这一路的残余风险（异常恰好发生在
        #     "已写进 dispatch"之后）由 store 侧部分唯一索引 idx_dispatch_signal_active 兜住
        #     ——"已入队"在 DB 里留了行，索引不看占位，所以重试到不了第二笔。
        #   True（place_order 已返回 ok：订单不可撤回地进了队列/柜台）→ **保留**占位行并提升为
        #     第三态「待核对」：同 sid 后续 claim 一律 409（宁可拒单也不双卖）+ 立即告警，
        #     回报/对账到达后由 upsert_order 自然收敛为正常终态。
        accepted = False
        sent_to_broker = False  # §CLAIMRELEASE 分流判据：这一笔是否已不可撤回地交给通道
        why = ""
        try:
            ok, order_ref, err = self.active_broker.place_order(req)
            if not ok:
                # 失败释放占位，允许后续重试真正重新下单（原有语义，现由 finally 统一兜底）；
                # ok=False 是通道的**显式拒绝**（方向闸/未连接/柜台 seq<=0），发生在报送之前
                why = "place_order 返回失败: %s" % (err or "")
                return 400, {"ok": False, "err": err or "place order failed"}
            # 走到这里订单已落进派发队列/已被柜台受理（QueuedBroker 是「入队即 return True」），
            # 之后任何一步失败都不许再把这块地抽走——这正是 §CLAIMRELEASE 的分流点
            sent_to_broker = True
            # settle：占位行回填真实委托引用（seq:<n> 或 mock 单号；交易所号随后续回报替换）
            self.ids.settle({
                "order_id": order_ref, "signal_id": signal_id, "code": code, "side": side,
                "status": "已报", "price": price, "qty": qty,
                "created_at": req.get("created_at") or _now_cn(),
                "user_id": place_uid,  # §P1-9 / §TOKEN-PERAACCT：与占位行同一归属，不中途换人
            })
            accepted = True
            log.info("[gateway] order accept: signal=%s code=%s side=%s qty=%s ref=%s "
                     "accept_ms=%.1f broker=%s", signal_id, code, side, qty, order_ref,
                     (time.time() - _t0) * 1000, self.active_key)
            return 200, {"ok": True, "order_id": order_ref, "err": ""}
        except DispatchDuplicateSignal as e:
            # 派发队列的纵深防线拦下这一笔：同 sid 已有在途 order 行 = 「订单已在队列里」是
            # 既成事实（多半来自上一次占位被释放后的重试），所以**按已入队分流**（保留占位转
            # 第三态），绝不允许因为"本轮没写进行"就把信号解锁——那正是 N-8 的双卖路径。
            why = "派发队列拒绝重复入队: %s" % e
            sent_to_broker = True
            log.error("[gateway] §CLAIMRELEASE 派发队列纵深防线拦下同 signal_id 的第二笔入队"
                      "（signal=%s code=%s side=%s qty=%s）——本端不重发，占位转「待核对」：%s",
                      signal_id, code, side, qty, e)
            return 500, {"ok": False, "err": str(e)}
        except Exception as e:  # noqa: BLE001 — §M-2 异常绝不冒泡成"占位遗留"
            why = "下单窗口异常: %s" % e
            log.exception("[gateway] §M-2 下单窗口异常（signal=%s code=%s side=%s）——"
                          "本端不重发，释放占位后由调用方决定是否重试", signal_id, code, side)
            return 500, {"ok": False, "err": "place order error: %s" % e}
        finally:
            if not accepted:
                # §CLAIMRELEASE 负向锁落点：sent_to_broker 是**关键字必传**参数，漏传即 TypeError，
                # 让"不区分是否已发送就删占位"的写法无法再被新增（N-8 的成因正是这个分叉缺失）。
                self._release_order_claim(signal_id, why, sent_to_broker=sent_to_broker)

    def _release_order_claim(self, signal_id, why="", *, sent_to_broker):
        """§M-2 + §CLAIMRELEASE 占位收尾：按「是否已交给通道」决定删占位还是转第三态。

        :param sent_to_broker: 必填关键字参数——本轮是否已进入 broker 发送/入队阶段。
          False：订单从未离开本端 → 删占位（§M-2 原语义，避免同信号永久 409 死锁）。
          True：订单已不可撤回地入队/报送 → **保留**占位行并提升为第三态「待核对」，
          同 signal_id 的后续下单一律 409（宁可拒单也不双卖），并立即走网关告警出口
          （error 级日志 + /admin/status 的 unresolved_orders 观察位）。
        两条路径都不重发、不入队任何订单；日志都写清 reason 与「当时是否已入队」，
        事后取证能一眼分辨（旧实现两种情况同一句「已释放占位」，取证为零）。
        English: §CLAIMRELEASE — release the local placeholder only when the order never
        reached the broker; otherwise keep the claim row in the unresolved third state.
        """
        try:
            if not sent_to_broker:
                self.ids.release(signal_id)
                log.warning("[gateway] §M-2 已释放 signal_id=%s 的本地占位（原因：%s；"
                            "sent_to_broker=False，本笔**从未进入** broker 发送/入队）——"
                            "网关不自动重发，重试由调用方决策", signal_id, why or "未说明")
                return
            # 已交给通道：占位行保留（signal_id UNIQUE 这道幂等防线不拆），只换状态 + 告警
            moved = self.store.mark_pending_unresolved(signal_id)
            self._unresolved_alerted.discard(signal_id)  # 让巡检线程对这个新第三态再告一次
            log.error("[gateway] §CLAIMRELEASE signal_id=%s 本地占位**保留并转「待核对」**"
                      "（sent_to_broker=True：订单已交给通道/入队，结算未落账；原因：%s）。"
                      "同一 signal_id 的后续下单一律 409——宁可拒单也不重复下单；"
                      "回报到达后自动收敛，或人工核对券商侧后用 POST /admin/order-confirm 解锁%s",
                      signal_id, why or "未说明",
                      "" if moved else "（注：该行已不是 pending，可能已被回报推进）")
        except Exception:  # noqa: BLE001 — 收尾失败只能等运行期巡检兜底，不得再吞掉原始错误
            log.exception("[gateway] §CLAIMRELEASE 占位收尾失败 signal_id=%s "
                          "（sent_to_broker=%s，等待巡检兜底）", signal_id, sent_to_broker)

    def _do_cancel(self, body, caller_uid=""):
        """处理 POST /cancel 撤单：校验委托引用后委托 broker，结果如实返回。

        失败返回 409 且不吞掉错误——让首尔侧继续跟踪该委托并告警，避免误判成功。
        :param caller_uid: 发起撤单的账号（§TOKEN-PERAACCT，只用于留痕——撤单作用的委托归属
            仍按 orders 行判定，A 账号撤不动 B 账号的单由 broker 侧委托引用天然约束）。
        English: handles POST /cancel — validates the order ref and delegates to the broker,
        returning failures honestly so the decision side keeps tracking the order.
        """
        # 撤单：校验引用后委托给 broker，结果如实返回（失败不让首尔误判成功）
        order_ref = str((body or {}).get("order_id", "") or "")
        if not order_ref:
            return 400, {"ok": False, "err": "order_id required"}
        if caller_uid:
            log.info("[gateway] §TOKEN-PERAACCT 撤单请求 ref=%s 由账号 %s 发起",
                     order_ref, caller_uid)
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
        # §MANUAL-SYNC：透出委托表快照的规模与时刻，运维据此判断手动单是否已进来
        payload["orders_snapshot_at"] = str(self._orders_snap.get("data_ts", "") or "")
        payload["orders_snapshot_n"] = int(self._orders_snap.get("n", 0) or 0)
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

    def _auth_caller(self):
        """Bearer 鉴权并解析调用方账号（§TOKEN-PERAACCT，2026-10-30）。

        返回 (ok, user_id)。在册钥匙是一把集合而不是一条常量：每个 App 账号持有自己那一把，
        网关据此知道"这笔委托是谁下的"（此前所有人共用一把，回报只能砸给 users 顺序里的
        第一个命中者，下单方自己那行永远没人推进——10-30 的 test01 双份账就是这么来的）。
        常量时间比较逐把做（hmac.compare_digest），绝不因比对顺序泄露哪把先命中。
        English: authenticates against the per-account token registry and returns the caller's
        user id; the legacy single token stays valid (mapped to the owner account).
        """
        auth = self.headers.get("Authorization", "")
        for tk, uid in self.gateway._auth_tokens.items():
            if hmac.compare_digest(auth, "Bearer " + tk):
                return True, uid
        return False, ""

    def _auth_ok(self):
        """Bearer token 双向鉴权（常量时间比较，防时序侧信道）。

        校验用 token 优先来自环境变量 QUANT_GATEWAY_TOKEN，未设置时回退配置文件明文。
        §TOKEN-PERAACCT 起它只是 `_auth_caller` 的布尔外壳（既有调用点与用例语义不变）。
        """
        ok, _ = self._auth_caller()
        return ok

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
        self._caller_uid = ""
        if parsed.path != "/health":
            ok, uid = self._auth_caller()
            if not ok:
                return self._respond(401, {"ok": False, "err": "unauthorized"})
            # 调用方账号随请求落到 handler 上，handle() 及其下游据此记账（§TOKEN-PERAACCT）。
            self._caller_uid = uid
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
