# -*- coding: utf-8 -*-
"""qmt_gateway.report_uplink — 盒子本地普通 Python 进程的上行客户端（§EXT-BRAIN：回报由本地进程自己 POST）。

为什么存在（docs/PLAN_EXTERNAL_BRAIN_IMPL_20261103.md §2.3、IMPL_QMT_INTERNAL_LOOP_20261103 §4.1/§5.1）：
  券商 2026-11-05 起关闭 QMT 内置策略的一切文件读写与网络连接，文件桥
  （bridge_cmd.json / bridge_report.jsonl）整条退役；柜台读数改由客户端**导出文件**提供
  （broker_reads.py 的只读解析腿），而"导出解析完 → 决策机知道"这最后一跳必须有人把它
  推出去。本模块就是那一跳：一个**普通进程**（不是 QMT 沙箱策略），只走标准库
  urllib，把 positions/account/order/heartbeat 四类事件 POST 到既有的公网 HTTPS
  `POST /api/qmt/report`，归属仍按"这把钥匙属于谁"判定（§安全 T1：回报体 user_id 只是声明，
  首尔侧以 token 解析出的账号为准）。

与 handler.py 的分工（边界，别混）：
  * handler.py 是 QMT xtquant 回调时代的产物（11-05 后随网关交易链一起退役），本模块不依赖它、
    也不改它——两条链共用**同一份信封契约** qmt_gateway/contract/report_fields.json。
  * 本模块只上行读数与版本，**不下单、不撤单、不碰任何账本**（纯观测腿，§OBS-GUARD 同取向）。

四条硬规则（每条都有用例，tests/test_report_uplink.py）：
  1. **钥匙只在内存里**：从 config.xt.json 读（legacy `token` 或 `account_tokens[]`，
     `report_token` 优先），绝不打印、绝不进异常消息、绝不落进任何快照/状态文件。
     （§TOKEN-PERAACCT / B-N2：明文出机器是唯一不能犯的错。）
  2. **missing ≠ empty**：导出文件不存在 ⇒ 该类**一个事件都不发**并把原因记进本地状态；
     文件在而零行才允许发空快照（Go 侧还有市值/本地持仓两道守卫）。把"没取到"发成"空仓"
     会把账户在对账账本里抹平——§MANUAL-SYNC 三条安全边界的抄法（broker_reads 边界 2 同源）。
  3. **只有 2xx 才推进基线**（§FIX-20261028-F02）：发送失败绝不写"已报"哈希账；
     瞬态失败进有界重试队列（超限丢最旧并计数），永久 4xx 进死信账（毒丸不许卡队首，§P1-8）。
  4. **幂等/去重**：同内容哈希只发一次；order 事件继续携带 order_id/signal_id、
     持仓行携带 ts_code——下游唯一键（trade_id/signal_id/order_id）语义一字不动。

心跳（heartbeat）带什么：
  * `strategy_version`：从**已安装的内置策略文件**（config 键 uplink_strategy_file）里按正则
    读出的 STRATEGY_VERSION 常量——不 import、不 exec（那是别人机器上的构建产物，读它
    不等于运行它）；
  * `reason`：结构化 ASCII 键值串，带执行器自身版本 + 文件指纹（SHA256 前 12 位）与
    本模块自身的版本/指纹。Go 侧 leg_status 据此回答"我装的构建是不是正在跑的那份"。
    为什么挤在 reason 里：信封契约只允许**一枚**新字段（strategy_version），
    reason 在 heartbeat 分支 Go 不消费、不会误进任何账。

English: box-local ordinary-Python uplink client. Reads counter readings through broker_reads
(read-only export parsers), posts positions/account/order/heartbeat events to the existing
public-HTTPS POST /api/qmt/report with the box's own key from config.xt.json, never prints the
key, never sends anything for a missing export file (missing != empty), only advances the dedupe
baseline on a 2xx, and stamps the installed built-in strategy version plus the executor's own
version+fingerprint on the heartbeat so the decision brain can answer "is the build I installed
the build that is running?".
"""

import hashlib
import json
import logging
import os
import re
import sqlite3
import sys
import time
from collections import deque
from datetime import datetime, timedelta, timezone

import broker_reads

LOG = logging.getLogger("qmt_gateway.report_uplink")

# 本模块自身版本（进心跳 reason，与执行器版本分开——排查时要知道断的是哪一段）。
UPLINK_VERSION = "uplink-v01-20261105"

# 北京时间轴（回报时间串一律 +08:00，与 handler._now_cn / Go cntime 同源，不自造第三种）。
_BJ = timezone(timedelta(hours=8))

# 与 handler.py 的 §P1-8 同一口径：这些 4xx 是瞬态（钥匙轮换/限流/超时），可以重试；
# 其余 4xx 是永久拒绝，重发一万次也是 400——毒丸必须进死信账，不许卡住后续读数。
RETRYABLE_4XX = frozenset({401, 403, 408, 425, 429})

# 重试队列深度上限（有界，防"决策机整 weekend 不可达"把内存吃穿）。
DEFAULT_QUEUE_MAX = 500
# 已发内容哈希账的保留条数（滚动裁剪；只影响"本机别重复发"，服务端唯一键仍是最终裁判）。
DEFAULT_SENT_KEEP = 2000
# 跳过原因清单的保留条数（missing≠empty 的"记因为什么没发"就落在这里）。
DEFAULT_SKIP_KEEP = 50
# §CARRIER-SHADOW 单事件行数：必须**严格小于** Go 侧 carrierShadowMaxRows(500)——
# 那一侧超上限是整条 400（毒丸进死信），宁可在这里多切一刀，也不要让一天的判定全丢。
DEFAULT_SHADOW_BATCH_ROWS = 200

# STRATEGY_VERSION 常量的读取姿势：按行正则，不 import 目标文件（构建产物可能在别人机器上，
# 读它不等于运行它；而且 import 会执行顶层代码，那是沙箱外都不该有的动作）。
_VERSION_PAT = re.compile(r'^STRATEGY_VERSION\s*=\s*"([^"]+)"', re.MULTILINE)

# 裸 6 位码 → 交易所后缀（首位数字判市场；与 Go 个股侧同一族规则，指数不在本模块射程内）。
_CODE_PAT = re.compile(r"^(\d{6})$")
_SUFFIX_PAT = re.compile(r"^(\d{6})\.(SH|SZ|BJ)$", re.IGNORECASE)
_PREFIX_PAT = re.compile(r"^(SH|SZ|BJ)\.?(\d{6})$", re.IGNORECASE)


def _iso_beijing(ts):
    """epoch 秒 → RFC3339 北京时刻串（Go 侧 qmtReportFreshness 按 RFC3339 解析 data_ts）。"""
    return datetime.fromtimestamp(ts, _BJ).isoformat()


def normalize_code(raw):
    """导出里的证券代码归一成 6 位数字+.SH/.SZ/.BJ（Go 校验 real_positions 的 ts_code 形态）。

    三种现实形态都认：`600519.SH`、`600519`、`SH600519`。另外必须兜一种**测试抓出来的形状**：
    broker_reads._coerce 会把纯数字单元格转 int，`000001` 被压成 `1`——补齐 6 位是唯一的
    无损还原方式（深市 0 开头代码占近半壁江山，不兜这一下会被整类丢行）。
    认不出的形态返回空串——调用方**丢行并计数**，绝不猜交易所（猜错一个后缀，沪市的票会被
    整行拒收还是落到深市账上，两种都是错账；000001 这种沪指数/深个股同码的老坑
    AGENTS §BUG-INDEX 记过）。
    """
    # 数字单元格被 _coerce 收成 int（前导零丢失）：先按 6 位零填充还原，再走文本形态判定。
    if isinstance(raw, int) or (isinstance(raw, str) and raw.isdigit()):
        raw = "%06d" % int(str(raw))
    s = str(raw or "").strip().upper()
    if not s:
        return ""
    m = _SUFFIX_PAT.match(s)
    if m:
        return "%s.%s" % (m.group(1), m.group(2))
    m = _PREFIX_PAT.match(s)
    if m:
        return "%s.%s" % (m.group(2), m.group(1))
    m = _CODE_PAT.match(s)
    if m:
        first = m.group(1)[0]
        if first in ("6", "9", "5"):
            return "%s.SH" % m.group(1)
        if first in ("0", "1", "2", "3"):
            return "%s.SZ" % m.group(1)
        if first in ("4", "8"):
            return "%s.BJ" % m.group(1)
    return ""


def read_version_constant(path):
    """从目标 py 文件按行正则读 STRATEGY_VERSION（只读，读不到返回空串并留原因，绝不 exec）。"""
    try:
        with open(path, "rb") as fh:  # 只读打开（§F18：构建产物是证据，不许写）
            raw = fh.read()
    except OSError:
        return ""
    for enc in ("utf-8-sig", "utf-8", "gbk"):  # 编码三兜底，同 broker_reads 姿势
        try:
            text = raw.decode(enc)
            break
        except Exception:  # noqa: BLE001 — 解码失败换下一种，全败按"读不到"处理
            text = None
    if not text:
        return ""
    m = _VERSION_PAT.search(text)
    return m.group(1) if m else ""


def file_fingerprint(path):
    """文件内容 SHA256 前 12 位——"装进去的构建"与"正在跑的构建"比对用的最小指纹。"""
    try:
        h = hashlib.sha256()
        with open(path, "rb") as fh:  # 只读
            for chunk in iter(lambda: fh.read(65536), b""):
                h.update(chunk)
        return h.hexdigest()[:12]
    except OSError:
        return ""


def _day_beijing(ts):
    """epoch 秒 → YYYYMMDD（北京时间交易日紧凑形态，与执行器 day_key、Go cntime 同形）。

    影子账本按 day 查，day 形态必须是 Go 侧 carrierShadowDayPat 认的那种 8 位数字：
    拿 `2026-11-05` 去查会一行都查不到，而"查不到"在这条腿上的语义是"这台机器今天没报"，
    不是"我格式写错了"——所以形态只能在这一处定义，别处一律复用本函数。
    """
    return datetime.fromtimestamp(ts, _BJ).strftime("%Y%m%d")


def _sqlite_readonly(path):
    """以**只读**方式打开执行器账本（uri mode=ro）。

    为什么不是普通 connect：普通连接会以写模式打开库文件，SQLite 在打开时就可能创建
    `-wal`/`-shm`；这条腿对执行器那本账只有读的权力——它一旦能写，影子行的幂等真源
    就从执行器转移到了上行腿，同一件事就有了两本账（§MANUAL-SYNC 的对账纪律靠"只有一个写者"）。
    路径里带 `?`/`#` 会被 URI 解析吃掉，所以先转成绝对路径再做百分号转义。
    """
    abs_path = os.path.abspath(path)
    escaped = abs_path.replace("%", "%25").replace("?", "%3F").replace("#", "%23")
    return sqlite3.connect("file:%s?mode=ro" % escaped, uri=True)


_SHADOW_NOTE_KEYS = ("hard_block", "t_allowed", "missing_key_dims", "emotion_phase",
                    "t_leg")  # t_leg: T 判断腿的一串读数（open/dir/block_reason），见 local_executor


def shadow_judgement_note(blob):
    """把执行器账本里的 judgement 压缩串翻成上行侧的三枚 typed 字段。

    执行器存的是 `{"market_scale":…, "target":…, "hard_block":…, …}`（ASCII JSON，
    见 local_executor.sizing_judgement 的键清单），而 Go 的行是 typed 列
    market_scale / target_scale / sizing_note。这里只做**改名 + 拼串**，不产生新数：

      * 拿不到的键**整键缺席**，绝不写 0——Go 侧把 NULL 读成"这一版载体没接判断腿"，
        把 0 读成"定权说了零仓位"，两者是完全不同的结论（missing≠empty 在行内的落形）；
      * 说明类键（hard_block 等）拼成一枚 ASCII 键值串进 sizing_note，账本以后加键
        不需要改 Go 的表结构；
      * blob 坏了 / 不是 JSON ⇒ 返回空 dict：那是"这条判定没带判断腿读数"，
        仍然要把判定行本身报上去（判定与解释是两件事，解释坏不了判定）。
    """
    if not blob:
        return {}
    try:
        raw = json.loads(str(blob))
    except Exception:  # noqa: BLE001 — 判断串坏掉只损失解释，不损失判定
        return {}
    if not isinstance(raw, dict):
        return {}
    out = {}
    scale = raw.get("market_scale")
    if isinstance(scale, (int, float)) and not isinstance(scale, bool):
        out["market_scale"] = float(scale)
    target = raw.get("target")
    if isinstance(target, (int, float)) and not isinstance(target, bool):
        out["target_scale"] = float(target)
    parts = []
    for key in _SHADOW_NOTE_KEYS:
        if key not in raw or raw.get(key) is None:
            continue
        parts.append("%s=%s" % (key, raw.get(key)))
    if parts:
        note = " ".join(parts)
        if raw.get("judgement_error"):
            # 判断腿自己抛了：把这行原样带上，运维看到的是"腿坏了"而不是"腿没说"。
            note = "%s judgement_error=%s" % (note, raw.get("judgement_error"))
        out["sizing_note"] = note[:200]  # Go 侧列宽 200 字节（carrierShadowMaxNote）
    elif raw.get("judgement_error"):
        out["sizing_note"] = ("judgement_error=%s" % raw.get("judgement_error"))[:200]
    return out


def load_xt_config(config_path):
    """读 config.xt.json（utf-8 优先、gbk 兜底），返回 dict；文件缺失/坏 JSON 抛 ValueError。

    只取字段、绝不打日志——这个文件里同时住着钥匙，任何"把读到的配置打印出来"的姿势
    都等于把凭证写进日志（§TOKEN-PERAACCT 的教训形状）。
    """
    try:
        with open(config_path, "rb") as fh:
            raw = fh.read()
    except OSError as exc:
        raise ValueError("config_unreadable:%s" % exc.__class__.__name__)
    for enc in ("utf-8-sig", "utf-8", "gbk"):
        try:
            return json.loads(raw.decode(enc))
        except Exception:  # noqa: BLE001 — 换下一种编码；全败统一收敛成一个不带原文的错
            continue
    raise ValueError("config_not_json")


def resolve_token(cfg):
    """挑出这台盒子自己的那把钥匙：report_token 显式值 > legacy token > account_tokens 唯一项。

    形态对齐 gateway.py：account_tokens[{user_id,token}] 在册时**必须恰好一条**（B-N1 一台腿
    一个资金账号；多于一条时本机无法安全猜哪把属于自己，直接拒绝启动而不是任选——
    拿错别人的钥匙上报，成交会落进别人账本）。返回空串＝没有钥匙，调用方 fail-closed。
    """
    rt = str(cfg.get("report_token") or "").strip()
    if rt:
        return rt
    legacy = str(cfg.get("token") or "").strip()
    if legacy:
        return legacy
    entries = [e for e in (cfg.get("account_tokens") or []) if isinstance(e, dict)]
    owner = str(cfg.get("owner_user_id") or cfg.get("user_id") or "").strip()
    if owner:
        mine = [e for e in entries if str(e.get("user_id") or "").strip() == owner]
        if len(mine) == 1:
            return str(mine[0].get("token") or "").strip()
    if len(entries) == 1:
        return str(entries[0].get("token") or "").strip()
    return ""


def _canonical_hash(event):
    """事件内容哈希：排序键 + ensure_ascii，与"谁在什么时候发"无关——
    at/data_ts 会随导出文件 mtime 变化，所以同一份未变的导出重复解析必然同哈希（去重的判据），
    而重导出的文件 mtime 变了就是新读数（该重发）。"""
    blob = json.dumps(event, sort_keys=True, ensure_ascii=True, default=str)
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()


class ReportUplink:
    """上行客户端：读导出 → 组事件 → 去重 → POST → 只有 2xx 才推进"已报"账。

    生命周期设计成"可长跑也可 cron 单轮"：run_once() 完成一轮完整链路并返回统计 dict，
    flush_retries() 只排空积压队列；状态（已发哈希/死信/跳过原因）落 uplink_state.json，
    进程重启不丢幂等基线——这就是 §FIX-20261028-F02 的"写成功才推进基线"在本腿上的落形。
    """

    def __init__(self, config_path, now_fn=time.time, opener=None):
        """config_path：网关的 config.xt.json（与交易链同一份真源，不自造第二本账）。

        now_fn/opener 仅供测试注入（假时钟 + 假传输）；生产用 wall clock 与 urllib。
        """
        self.config_path = os.path.abspath(config_path)
        cfg = load_xt_config(self.config_path)
        self.cfg = cfg
        self.base_url = str(cfg.get("report_url") or "").strip()  # 地址来自配置，绝不硬编码 IP
        self.token = resolve_token(cfg)
        self.user_id = str(cfg.get("user_id") or cfg.get("owner_user_id") or "").strip()
        self._now_fn = now_fn
        # opener 签名：(url, token, body_bytes) -> (status:int)；网络失败抛异常。
        self._opener = opener or _urllib_opener
        # 导出根目录白名单（broker_reads 的准入函数只认显式登记的根，缺省为空＝什么都不读）。
        roots = cfg.get("uplink_export_roots") or []
        if isinstance(roots, str):
            roots = [roots]
        self.export_roots = tuple(str(r) for r in roots)
        # 2026-11-10（PLAN 1.3 第 12 项）：D:\qmt 下 HGT/SGT 是港股通空壳账户的导出目录，
        # 与 A 股主账户同名前缀文件并存，按 mtime 抢占时空壳（只有表头）会顶掉真数据。
        # 配置了 uplink_fund_account 后，行里「资金账号」列不匹配的整行丢弃——三个目录
        # 里只认主账户自己的那份。缺省为空＝不过滤（单目录部署不需要）。
        self.fund_account = str(cfg.get("uplink_fund_account") or "").strip()
        # 已安装内置策略文件（心跳版本的读取源）与执行器文件（自身指纹源）。
        self.strategy_file = str(cfg.get("uplink_strategy_file") or "").strip()
        self.executor_file = str(cfg.get("uplink_executor_file") or
                                  os.path.join(os.path.dirname(self.config_path), "local_executor.py")).strip()
        state_dir = str(cfg.get("uplink_state_dir") or os.path.dirname(self.config_path))
        self.state_path = os.path.join(state_dir, "uplink_state.json")
        # §CARRIER-SHADOW：执行器那本影子账的位置。三级兜底，全部指向**同一个**执行器写下的库：
        #   1) 配置里显式登记（运维改了 --state-db 就必须在这里同步，改一处不够）；
        #   2) 环境变量 LOCAL_EXECUTOR_DB（执行器自己的缺省来源，同一台机器上不必登记两次）；
        #   3) 与配置同目录的 executor_state.db（装包默认布局：执行器与 config.xt.json 同级）。
        # 三条都拿不到 ⇒ 属性为空串，_read_shadow_rows 记 journal_not_configured 后一个字节都不发：
        # "影子窗口今天没数据"与"这台机器根本没接影子账本"是两种修复，不许混成一条空上报。
        self.shadow_db = str(cfg.get("uplink_shadow_db")
                             or os.environ.get("LOCAL_EXECUTOR_DB")
                             or os.path.join(os.path.dirname(self.config_path),
                                             "executor_state.db")).strip()
        # 单批行数：Go 侧 carrierShadowMaxRows=500 整条拒收，所以上行必须先切好；
        # 200 是给单事件留体积余量（一天几十条判定 × 每行十几个字段）。
        self.shadow_max_rows = max(1, int(cfg.get("uplink_shadow_max_rows")
                                          or DEFAULT_SHADOW_BATCH_ROWS))
        self.queue_max = int(cfg.get("uplink_queue_max") or DEFAULT_QUEUE_MAX)
        self.sent_keep = int(cfg.get("uplink_sent_keep") or DEFAULT_SENT_KEEP)
        self.skip_keep = int(cfg.get("uplink_skip_keep") or DEFAULT_SKIP_KEEP)
        # 运行态：重试队列（有界 FIFO）+ 各读数类别的半文件参照账本（跨轮保留上一份行数）。
        self._retry = deque()
        self._ledgers = {}
        self._load_state()

    # ---------------- 状态文件（绝不写钥匙，测试逐字节钉死） ----------------
    def _load_state(self):
        """从状态文件恢复"已发哈希/死信/跳过原因"；文件坏或不存在按空态起步（读侧 fail-open、
        写侧从不因状态丢失而放宽——最坏是把上一条已发内容重发一次，服务端唯一键兜底）。"""
        self.sent_hashes = []
        self.dead_hashes = []
        self.skips = []
        self.stats = {"sent": 0, "dead": 0, "dropped_queue_overflow": 0, "net_fail": 0}
        try:
            with open(self.state_path, "rb") as fh:
                doc = json.loads(fh.read().decode("utf-8", "replace"))
            self.sent_hashes = [str(h) for h in doc.get("sent_hashes") or []]
            self.dead_hashes = [str(h) for h in doc.get("dead_hashes") or []]
            self.skips = list(doc.get("skips") or [])
            for k in self.stats:
                self.stats[k] = int(doc.get("stats", {}).get(k) or 0)
        except OSError:
            return  # 首跑没有状态文件：合法空态
        except Exception:  # noqa: BLE001 — 状态文件坏了不炸进程，按空态重头记账即可
            LOG.warning("[uplink] state file unreadable, starting blank (no key material in it anyway)")

    def _save_state(self):
        """原子落盘（tmp+rename），只写哈希与原因串，**结构上就不包含**任何凭证字段。"""
        doc = {
            "sent_hashes": self.sent_hashes[-self.sent_keep:],
            "dead_hashes": self.dead_hashes[-200:],
            "skips": self.skips[-self.skip_keep:],
            "stats": self.stats,
            "saved_at": _iso_beijing(self._now_fn()),
        }
        tmp = self.state_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, ensure_ascii=True, indent=1, sort_keys=True)
        os.replace(tmp, self.state_path)

    def _note_skip(self, kind, reason):
        """把"这一类为什么一个都没发"记进本地状态——missing≠empty 的可取证半边。"""
        self.skips.append({"kind": kind, "reason": reason, "at": _iso_beijing(self._now_fn())})
        self.skips = self.skips[-self.skip_keep:]

    # ---------------- 事件构造（信封字段名逐字镜像 golden/report_fields.json） ----------------
    def _base_event(self, etype):
        """所有事件共有的两键：type + user_id（与 handler._push 同形；Go 归属只信 token）。"""
        return {"type": etype, "user_id": self.user_id}

    def _latest_export(self, kind):
        """在登记的导出根里按文件名形态找该类**最新**一份；找不到返回 None（不猜、不兜底）。

        2026-11-10（PLAN 1.3 第 12 项）：旧姿势 `[root + "/*"]` 只扫一层——
        QMT 把导出落在 D:\\qmt 的 HGT/SGT/Stock **子目录**里，于是所有类别
        file_not_found、脑侧全天 0 事件。改走递归发现（四道准入闸全部复用
        broker_reads，不另起第二套）。
        """
        if not self.export_roots:
            return None
        hits = [e for e in broker_reads.discover_exports_recursive(
            self.export_roots, self.export_roots) if e.kind == kind]
        if not hits:
            return None
        # 2026-11-11 实锤（SGT 空壳顶掉 Stock 真账户）：HGT/SGT 港股通壳账户的
        # 同名导出**只有表头、每分钟刷新**，mtime 永远最新；资金账号过滤救不了它
        # （过滤只对有数据行的文件说话）。改为**按内容择优**：从新到旧读，第一份
        # 解析出 ≥1 行数据的即用；全空才退回最新一份（合法空语义不丢）。
        # English: prefer the newest export that actually parses rows; header-only
        # shells refresh constantly and must not shadow the real account file.
        for exp in hits:
            try:
                if broker_reads.ReadLedger(kind, source="probe").refresh(exp.path).row_count > 0:
                    return exp
            except Exception:  # noqa: BLE001 - 读坏的候选跳过，继续找下一份
                continue
        return hits[0]

    def _ledger_for(self, kind):
        """每类读数一本跨轮账本：半文件骤降判定需要"上一份可信行数"当参照物。"""
        led = self._ledgers.get(kind)
        if led is None:
            led = broker_reads.ReadLedger(kind, source="uplink")
            self._ledgers[kind] = led
        return led

    def build_events(self):
        """按当前导出与已安装版本构造本轮事件清单（positions/account/order/shadow/heartbeat）。

        每一类都独立走"缺→不发并记原因 / 半文件→不发 / 空→可发"三态，任何一类意外都不
        拖累其它类（单类读数坏掉是常态，全轮陪葬才是事故）。
        2026-11-13 §C3：本轮若发生过任何 skip（file_not_found / parse_fail / 资金账号
        不匹配…），追加一条 diagnostic 观测事件——旧实现这些原因只躺在盒子本地状态里，
        云端完全静默，#5 的四候选根因因此迟迟无法定案。diagnostic 只观测不落账（Go 侧
        case diagnostic 只留痕），没有 skip 时整条不发（常态零噪音）。
        """
        skips_before = len(self.skips)
        events = []
        events.extend(self._build_positions_event())
        events.extend(self._build_account_event())
        events.extend(self._build_order_events())
        events.extend(self._build_fill_events())
        events.extend(self._build_shadow_events())
        hb = self._build_heartbeat()
        if hb is not None:
            events.append(hb)
        new_skips = self.skips[skips_before:]
        if new_skips:
            events.append({
                "type": "diagnostic",
                "user_id": self.user_id,
                "skips": new_skips,
            })
        return events

    def _read_kind(self, kind):
        """发现→账本刷新→返回 (rows, ReadResult)；rows=None 即"这类不发"。

        三态处置逐条对齐 broker_reads 的边界 2/3（missing≠empty、半文件保留旧值不上新）：
          * missing / parse_fail / unsupported ⇒ **一个事件都不发**并把原因记进本地状态；
            半文件时上一份可信读数早已按当时内容发过（去重账挡重复），绝不把旧值重贴成新读数。
          * empty（文件在而零行）⇒ rows=[] 返回——这是合法的"空读数"，positions 据此
            可以发空快照（Go 侧还有市值/本地持仓两道守卫兜最后），与 missing 结构性不同。
          * ok ⇒ rows 为本次解析结果。
        """
        exp = self._latest_export(kind)
        if exp is None:
            self._note_skip(kind, "file_not_found")
            return None, None
        led = self._ledger_for(kind)
        result = led.refresh(exp.path)  # refresh 把本次 ReadResult 原样回传（行数参照从账本取）
        if result.state in (broker_reads.STATE_MISSING,
                            broker_reads.STATE_PARSE_FAIL,
                            broker_reads.STATE_UNSUPPORTED):
            self._note_skip(kind, "%s:%s" % (result.state, result.reason))
            return None, result
        rows = self._filter_fund_account(kind, result.rows)
        if rows is None:
            return None, result
        return rows, result

    def _filter_fund_account(self, kind, rows):
        """按「资金账号」列过滤出主账户自己的行（2026-11-10，PLAN 1.3 第 12 项）。

        HGT/SGT 是港股通空壳账户的导出，与 A 股主账户同名前缀文件并存：
        只按 mtime 挑"最新一份"会让空壳顶掉主账户数据。未配置
        uplink_fund_account（单目录部署）或行里没有该列时不做任何过滤——
        过滤只在两边都能对得上号时才说话。全部行都不匹配 ⇒ 视同 missing
        （"这份文件不是我们账户的"），一个事件都不发并记原因。
        """
        if not self.fund_account:
            return rows
        if not rows:
            return rows
        if not any(str(r.get("fund_account") or "").strip() for r in rows):
            return rows  # 这类导出没有资金账号列：无从过滤，不猜
        mine = [r for r in rows
                if str(r.get("fund_account") or "").strip() == self.fund_account]
        if not mine:
            self._note_skip(kind, "fund_account_mismatch:expected=%s" % len(rows))
            return None
        return mine

    def _stamp_of(self, result):
        """导出文件 mtime → RFC3339 北京串（进 data_ts/at 让 Go 按新鲜度裁决，不自造时钟）。"""
        if result is not None and result.asof:
            return _iso_beijing(result.asof)
        return _iso_beijing(self._now_fn())

    def _build_positions_event(self):
        rows, result = self._read_kind("positions")
        if rows is None:
            return []  # missing/parse_fail：一个事件都不发，原因已在 state 里
        out = self._base_event("positions")
        out["synced"] = True  # 导出文件是柜台已同步会话的产物；未同步会话导不出对账单
        out["data_ts"] = self._stamp_of(result)
        mapped = []
        for row in rows:
            code = normalize_code(row.get("symbol"))
            if not code:
                self._note_skip("positions", "bad_code_row:%r" % (row.get("symbol"),))
                continue  # 认不出市场的行整行丢弃——猜后缀会把票落到别的交易所账上
            qty = int(row.get("qty") or 0)

            def _fnum(value):
                try:
                    return float(value)
                except (TypeError, ValueError):
                    return 0.0

            cost = _fnum(row.get("cost_price"))
            r = {"ts_code": code, "name": str(row.get("name") or ""),
                 "qty": qty, "user_id": self.user_id,
                 # 2026-11-13: cost/amount/highest/anchor were missing entirely,
                 # so every reconciled row landed with cost_price=0. The Go side
                 # only back-fills a zero local cost, so it could never recover.
                 "cost_price": cost,
                 "amount": round(qty * cost, 2),
                 "highest_price": _fnum(row.get("highest_price"))}
            anchor = row.get("anchor_price")
            if anchor not in (None, ""):
                r["anchor_price"] = _fnum(anchor)
            # can_use_qty：导出有"可用数量"才带（*int 语义，缺键=nil=未知，绝不拿 0 冒充"不可卖"）
            if row.get("usable_qty") not in (None, ""):
                try:
                    r["can_use_qty"] = int(row.get("usable_qty"))
                except (TypeError, ValueError):
                    pass
            if row.get("updated_at"):
                r["updated_at"] = str(row.get("updated_at"))
            mapped.append(r)
        out["positions"] = mapped
        return [out]

    def _build_account_event(self):
        rows, result = self._read_kind("asset")
        if rows is None:
            return []
        if not rows:
            # 资产文件在而零行：拿不到任何资产键＝没有可报事实，与 positions 不同（这里没有
            # "空仓"这种合法空语义——空资产行发过去会被 Go 判 empty asset 回 400 进死信刷屏）。
            self._note_skip("asset", "empty_asset_rows")
            return []
        row = rows[0]
        asset = {}
        total = _to_float(row.get("total_assets"))
        cash = _to_float(row.get("usable_cash"))
        mv = _to_float(row.get("market_value"))
        if total is not None:
            asset["total_asset"] = total
        if cash is not None:
            asset["cash"] = cash
        if mv is not None:
            asset["market_value"] = mv
        # frozen_cash：导出没有这列（多数对账单如此），按恒等式 总资产-可用-市值 推导；
        # 推成负数说明三数不同源/不同刻，宁可不报这个键也不报一个假的冻结额。
        if None not in (total, cash, mv):
            frozen = round(total - cash - mv, 2)
            if frozen >= 0:
                asset["frozen_cash"] = frozen
        if not asset:
            self._note_skip("asset", "no_asset_keys")
            return []
        out = self._base_event("account")
        out["synced"] = True
        out["data_ts"] = self._stamp_of(result)
        out["asset"] = asset
        return [out]

    def _build_order_events(self):
        rows, result = self._read_kind("orders")
        if rows is None:
            return []
        events = []
        stamp = self._stamp_of(result)
        for row in rows:
            code = normalize_code(row.get("symbol"))
            if not code:
                self._note_skip("orders", "bad_code_row:%r" % (row.get("symbol"),))
                continue
            order_id = str(row.get("order_id") or "").strip()
            if not order_id:
                # Go 对缺 order_id 的委托回报是显式 400 拒收（§F5：主键缺失无法幂等落行）。
                # 与其发一条注定进死信的，不如本地记原因——这也是"缺就别说"的一致取向。
                self._note_skip("orders", "row_without_order_id")
                continue
            out = self._base_event("order")
            out["order_id"] = order_id
            out["code"] = code
            out["side"] = _side_word(row.get("side"))
            out["status"] = str(row.get("status") or "").strip()
            out["price"] = _to_float(row.get("price")) or 0.0
            out["qty"] = int(_to_float(row.get("qty")) or 0)
            out["signal_id"] = ""  # 柜台导出不含信号归因；Go 走 ext:<order_id> 占位键（§F5）
            out["reason"] = ""
            created = str(row.get("time") or "").strip()
            out["created_at"] = created or stamp
            out["at"] = stamp
            events.append(out)
        return events

    def _build_fill_events(self):
        """§TRADE-LEDGER（2026-11-13）：成交导出 → type=trade 事件（D-B 缺口的补腿）。

        背景：载体自闭环（QMT 内置策略）的成交此前**根本不上云**——handler.py 网关腿
        跑在 QMT 进程内（11-05 起禁网已死），fills 导出有解析器（broker_reads "fills"）
        却没有 builder。实测 2026-11-13：gz fills 表当日 0 行。台账（交易结果记录）以
        成交腿为地基，本 builder 补上这条腿。

        行字段：成交价/成交数量/成交金额/成交时间/成交编号/委托号/投资备注（备注经
        broker_reads 新映射，供 Go 反解战法归因）。柜台导出无费用列 → 不发（Go 按 0
        落，与旧格式兼容）；signal_id 恒空串（导出无信号归因，Go 按 remark 反解）。
        幂等锚 = 成交编号（Go ApplyRealFill §M4 trade_id 判重先行，重放不双记）。
        English: build type=trade events from the fills export — the missing uplink leg
        that feeds the per-fill trade ledger; remark rides along for strategy attribution.
        """
        rows, result = self._read_kind("fills")
        if rows is None:
            return []
        events = []
        stamp = self._stamp_of(result)
        for row in rows:
            code = normalize_code(row.get("symbol"))
            if not code:
                self._note_skip("fills", "bad_code_row:%r" % (row.get("symbol"),))
                continue
            out = self._base_event("trade")
            out["order_id"] = str(row.get("order_id") or "").strip()
            out["code"] = code
            out["side"] = _side_word(row.get("side"))
            out["price"] = _to_float(row.get("price")) or 0.0
            out["qty"] = int(_to_float(row.get("qty")) or 0)
            out["amount"] = _to_float(row.get("amount")) or 0.0
            out["traded_at"] = str(row.get("time") or "").strip() or stamp
            out["trade_id"] = str(row.get("fill_id") or "").strip()
            out["remark"] = str(row.get("remark") or "").strip()
            out["signal_id"] = ""  # 导出无信号归因；Go 按 remark 反解（§TRADE-LEDGER）
            out["at"] = stamp
            if out["qty"] <= 0 or out["price"] <= 0:
                self._note_skip("fills", "zero_price_or_qty_row")
                continue
            events.append(out)
        return events

    # ---------------- §CARRIER-SHADOW：载体影子判定（5 日对照窗口的分子） ----------------
    def _read_shadow_rows(self):
        """读执行器本地账本里**今天**的 shadow 决策；返回 rows（可能为空）或 None（这类不发）。

        三态与 broker_reads 边界 2 同源，一个都不许折：
          * 账本文件不存在 / 打不开 / 没有 shadow_decisions 表（11-05 之前建的旧账本）
            ⇒ **一个事件都不发**并记原因——"这台机器的载体还不会记账"与"今天没有影子决策"
            是两种完全不同的修复（前者换载体，后者等盘中）；
          * 表在而今天零行 ⇒ 同样**一个都不发**并记 no_rows_today（硬规则 2：今天没有行就
            不许把"空列表"发出去——Go 侧一旦收下空批次，那一天就会被对照表当成"报过的一天"，
            5 日窗口能被一串静默日自己凑满）；
          * 今天有行 ⇒ 原样返回。
        只读连接（mode=ro）：这条腿对执行器账本**只有读**的权力，写一次就把幂等键的
        真源从执行器挪到了上行腿，那是两本账。
        """
        if not self.shadow_db:
            self._note_skip("shadow", "journal_not_configured")
            return None
        if not os.path.exists(self.shadow_db):
            self._note_skip("shadow", "journal_missing")
            return None
        try:
            conn = _sqlite_readonly(self.shadow_db)
        except Exception as exc:  # noqa: BLE001 — 打不开＝这类不发，原因写清、绝不发空
            self._note_skip("shadow", "journal_unreadable:%s" % type(exc).__name__)
            return None
        try:
            day = _day_beijing(self._now_fn())
            has = conn.execute(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='shadow_decisions'"
            ).fetchone()
            if not has or int(has[0] or 0) == 0:
                # 旧载体建的账本：它压根没这枚表。报"今天零条"是假话（今天到底有没有跑过
                # 一轮判定，这张表回答不了），必须报"这台机器的载体还不会记影子账"。
                self._note_skip("shadow", "journal_table_absent")
                return None
            rows = conn.execute(
                "SELECT day, short_code, symbol, kind, side, qty, price, amount, judgement,"
                " first_ts, last_ts FROM shadow_decisions WHERE day = ?"
                " ORDER BY first_ts, short_code", (day,)).fetchall()
            if not rows:
                self._note_skip("shadow", "no_rows_today")
                return None
            return rows
        except Exception as exc:  # noqa: BLE001 — 查询失败同样是一个都不发（不发≠空发）
            self._note_skip("shadow", "journal_query_failed:%s" % type(exc).__name__)
            return None
        finally:
            conn.close()

    def _build_shadow_events(self):
        """把今天的影子决策打成 carrier_shadow 事件（可分批），每批的 at 由内容推出。

        为什么 `at` 不能取当前时钟：硬规则 4 的幂等基线是**内容哈希**，at 一进哈希就成了
        墙钟，每轮都产生新哈希——同一份没变的账本会被反复重发（洪泛决策机），
        而那正是"重启后不许重灌"要防的形状。取本批最新的 decided_at：账本不变⇒事件不变⇒
        去重账挡下（与 _canonical_hash 那段注释同一判据）。

        分批（shadow_max_rows）只为单个体积：行数封顶在 Go 侧的 carrierShadowMaxRows，
        超上限它整条 400（毒丸可见、不静默截断），所以上行侧必须先切好。
        """
        rows = self._read_shadow_rows()
        if rows is None:
            return []
        mapped = []
        for r in rows:
            code = normalize_code(r[2])
            if not code:
                # 认不出交易所的行整行丢弃（猜后缀会把票落到别的交易所账上，与 positions 同口径）
                self._note_skip("shadow", "bad_code_row:%r" % (r[2],))
                continue
            row = {"day": str(r[0]), "ts_code": code, "kind": str(r[3] or ""),
                   "side": str(r[4] or ""), "short_code": str(r[1] or ""),
                   "qty": int(r[5] or 0), "price": float(r[6] or 0.0),
                   "decided_at": _iso_beijing(float(r[10] or 0.0))}
            if not row["kind"] or not row["short_code"]:
                # Go 侧逐行拒收的就是这两枚（空种类进不了任何一格、空短码会互相覆盖），
                # 与其发一条注定被摘掉的，不如本地记原因——与 order 缺 order_id 那条同处置。
                self._note_skip("shadow", "row_without_kind_or_short_code")
                continue
            if r[7] is not None:
                row["amount"] = float(r[7])  # 缺＝载体没给金额（卖出信号常如此），绝不写 0
            note = shadow_judgement_note(r[8])
            for key in ("market_scale", "target_scale"):
                if note.get(key) is not None:
                    row[key] = note[key]
            if note.get("sizing_note"):
                row["sizing_note"] = note["sizing_note"]
            mapped.append(row)
        if not mapped:
            # 有行但全被摘掉：这一类**不发**（发了就是空批次，Go 侧按纪律 400）。
            # 原因已经逐条记在 bad_code_row/row_without_kind 里，这里再补一条汇总，
            # 让 dry-run 那一屏看得见"表里有 N 行但一条都没报"这种最难自查的形态。
            self._note_skip("shadow", "all_rows_rejected")
            return []
        events = []
        size = max(1, int(self.shadow_max_rows or 1))
        for start in range(0, len(mapped), size):
            chunk = mapped[start:start + size]
            out = self._base_event("carrier_shadow")
            # 批内 decided_at 字典序取最大＝内容派生的"这批最新的判定时刻"，重跑同一份账本必同值。
            out["at"] = max(str(c.get("decided_at") or "") for c in chunk)
            out["shadow"] = chunk
            events.append(out)
        return events

    def _build_heartbeat(self):
        """心跳带版本三件套：已装内置策略版本（进信封 strategy_version）+ 执行器/本模块
        自身版本与指纹（进 reason，ASCII 键值串）。策略文件读不到时 strategy_version 缺席
        ——Go 的"空不覆盖已知"纪律保证这不会把已知的旧版本抹掉。"""
        ver = read_version_constant(self.strategy_file) if self.strategy_file else ""
        if self.strategy_file and not ver:
            self._note_skip("strategy_version", "file_missing_or_no_constant")
        exec_ver = read_version_constant(self.executor_file)
        exec_fp = file_fingerprint(self.executor_file)
        own_fp = file_fingerprint(os.path.abspath(__file__))
        out = self._base_event("heartbeat")
        out["at"] = _iso_beijing(self._now_fn())
        if ver:
            out["strategy_version"] = ver
        parts = ["uplink_version=%s" % UPLINK_VERSION, "uplink_fingerprint=%s" % own_fp]
        if exec_ver:
            parts.append("executor_version=%s" % exec_ver)
        if exec_fp:
            parts.append("executor_fingerprint=%s" % exec_fp)
        out["reason"] = " ".join(parts)
        return out

    # ---------------- 发送（只有 2xx 才推进基线） ----------------
    def _post(self, event):
        """POST 单个事件。返回 'sent' / 'retry' / 'dead'。异常文本一律不带钥匙。"""
        if not self.base_url:
            # 没配 report_url：结构性不能上报（fail-closed，而不是把读数泼到默认路由上）。
            self._note_skip("transport", "report_url_not_configured")
            return "retry"
        url = self.base_url.rstrip("/") + "/api/qmt/report"
        body = json.dumps(event, ensure_ascii=True, default=str).encode("utf-8")
        try:
            status = self._opener(url, self.token, body)
        except Exception:  # noqa: BLE001 — 任何传输异常都不许带崩读数腿；错误文本不含请求头
            self.stats["net_fail"] += 1
            LOG.warning("[uplink] transport error kind=%s", event.get("type"))
            return "retry"
        if 200 <= status < 300:
            return "sent"
        if 400 <= status < 500 and status not in RETRYABLE_4XX:
            return "dead"  # 永久拒绝（毒丸）：记死信，不再重发（§P1-8 同口径）
        return "retry"

    def run_once(self):
        """完整一轮：先排空重试队列（保持原序，老事实先到），再构造并发本轮新事件。

        返回统计 dict（供 cron 日志与测试断言）。发送成功才把内容哈希推进"已发"账——
        这条就是 §FIX-20261028-F02 在本腿的落形：基线只随真实成功的写推进。
        """
        report = {"sent": 0, "retry": 0, "dead": 0, "skipped_dupe": 0}
        pending = []
        while self._retry:
            pending.append(self._retry.popleft())
        for event in self.build_events():
            pending.append(event)
        for event in pending:
            h = _canonical_hash(event)
            if h in self.sent_hashes or h in self.dead_hashes:
                report["skipped_dupe"] += 1
                continue  # 同内容已定论（成功或死信），不重复占网络
            outcome = self._post(event)
            if outcome == "sent":
                self.sent_hashes.append(h)
                self.sent_hashes = self.sent_hashes[-self.sent_keep:]
                self.stats["sent"] += 1
                report["sent"] += 1
            elif outcome == "dead":
                self.dead_hashes.append(h)
                self.stats["dead"] += 1
                report["dead"] += 1
                LOG.warning("[uplink] permanently rejected, dead-lettered kind=%s", event.get("type"))
            else:
                self._retry.append(event)
                # 有界队列：溢出丢最旧并计数（保新弃旧——旧委托/持仓下一份导出会重来）。
                while len(self._retry) > self.queue_max:
                    self._retry.popleft()
                    self.stats["dropped_queue_overflow"] += 1
                report["retry"] += 1
        self._save_state()
        return report


def _urllib_opener(url, token, body):
    """真实传输腿：标准库 urllib + **默认 TLS 校验**（不建 unverified context，一行都不许）。

    返回 HTTP 状态码；非 2xx 不抛（HTTPError 被吃掉取 .code），网络层错误原样上抛由 _post
    收成 retry——异常消息只有 URL/原因，结构上不含 Authorization 头。
    """
    import urllib.error
    import urllib.request
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:  # 校验由 ssl 默认工厂负责
            return int(resp.status)
    except urllib.error.HTTPError as exc:
        return int(exc.code)


def _to_float(v):
    """宽松取数：拿不到数返回 None（缺就别说），拿到返回 float。"""
    try:
        if v is None or v == "":
            return None
        return float(str(v).replace(",", ""))
    except (TypeError, ValueError):
        return None


def _side_word(v):
    """买卖方向归一：导出文本里"买入/卖出/证券买入/担保品买入…"一律折到 Go 认的两个词；
    认不出返回空串——order 事件里 side 仅展示（Go §2026-09-11 放宽），不猜方向也不造假。"""
    s = str(v or "").strip()
    if not s:
        return ""
    if "买" in s or s.upper() in ("B", "BUY"):
        return "买入"
    if "卖" in s or s.upper() in ("S", "SELL"):
        return "卖出"
    return ""


# ---------------------------------------------------------------------------
# 命令行入口（2026-11-04 补）
#
# 为什么必须补这一段：本模块写出来之后**没有人驱动它**——没有 CLI、没有任何调用点。
# "回报由本地进程自己 POST"是 owner 2026-11-04 的第 1 条裁决，而一条只能被 import 的腿
# 在今晚的家用机测试里等于没有：没法看它到底发出了几个事件、也没法证明
# "导出文件缺失时一个都不发"这条纪律真在生效。
#
# 三条边界（与模块内四条硬规则同源，不新造口径）：
#   1. **钥匙永不出口**：只打 report_url 的 host:port 与 token 的**有无/长度**，
#      绝不打印 token 本体、绝不打印请求头；
#   2. `--dry-run` 一个字节都不发、状态文件一个字都不写（只组事件、只数数）；
#   3. 地址与导出根一律取自 config.xt.json（同一份真源），命令行不许传 IP/token。
#
# 用法
#   python report_uplink.py --config C:\qmt\...\config.xt.json --once --dry-run
#   python report_uplink.py --config ... --once            # 真上一轮（2xx 才推进基线）
# ---------------------------------------------------------------------------

def _host_of(url):
    """只取 host:port 用于打印——路径与查询串里可能带东西，凭证更是绝对不出。"""
    text = str(url or "")
    if "://" in text:
        text = text.split("://", 1)[1]
    return text.split("/", 1)[0]


def main(argv=None):
    import argparse

    ap = argparse.ArgumentParser(prog="report_uplink",
                                 description="box-local readout uplink (POST /api/qmt/report)")
    ap.add_argument("--config", required=True, help="path to config.xt.json (the same source the trade leg uses)")
    ap.add_argument("--once", action="store_true", help="run exactly one round and exit")
    ap.add_argument("--dry-run", action="store_true", dest="dry_run",
                    help="build and count events, send nothing, write no state")
    args = ap.parse_args(argv)

    try:
        uplink = ReportUplink(args.config)
    except Exception as exc:  # noqa: BLE001 -- config unreadable must be loud, and key-free
        print("UPLINK_RESULT=ABORT reason=config_unreadable err=%s" % repr(exc)[:120])
        return 2

    print("UPLINK_CONFIG host=%s token_present=%d user_id_len=%d export_roots=%d "
          "strategy_file=%s state=%s"
          % (_host_of(uplink.base_url), 1 if uplink.token else 0, len(uplink.user_id or ""),
             len(uplink.export_roots), "present" if uplink.strategy_file else "unset",
             os.path.basename(uplink.state_path)))

    if args.dry_run:
        events = uplink.build_events()
        kinds = {}
        for ev in events:
            kinds[str(ev.get("type"))] = kinds.get(str(ev.get("type")), 0) + 1
        print("UPLINK_EVENTS n=%d kinds=%s"
              % (len(events), ",".join("%s:%d" % kv for kv in sorted(kinds.items())) or "none"))
        # 半文件/缺件的"为什么没发"必须同样可见，否则 0 事件分不清"确实没有"和"取不到"。
        for skip in uplink.skips[-5:]:
            print("UPLINK_SKIP kind=%s reason=%s" % (skip.get("kind"), skip.get("reason")))
        print("UPLINK_RESULT=DRYRUN sent=0 dryrun=1")
        return 0

    if not args.once:
        print("UPLINK_RESULT=ABORT reason=no_mode_given_use_once")
        return 2
    if not uplink.base_url or not uplink.token:
        print("UPLINK_RESULT=ABORT reason=upstream_unconfigured host_present=%d token_present=%d"
              % (1 if uplink.base_url else 0, 1 if uplink.token else 0))
        return 2

    report = uplink.run_once()
    print("UPLINK_RESULT=OK sent=%d retry=%d dead=%d skipped_dupe=%d net_fail=%d drops=%d"
          % (report.get("sent", 0), report.get("retry", 0), report.get("dead", 0),
             report.get("skipped_dupe", 0), uplink.stats.get("net_fail", 0),
             uplink.stats.get("dropped_queue_overflow", 0)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
