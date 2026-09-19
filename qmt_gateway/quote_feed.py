# -*- coding: utf-8 -*-
"""quote_feed.py — §ENH-5 批E：xtdata Level-1 全推行情 feed（只读通道）。

设计铁律（计划 §① 风险/边界）：
- 与交易主链路完全隔离：独立线程 + 独立 try/except，xtdata 缺失/断连只让
  feed_connected=False，绝不影响 XtBroker 下单链路与 /health 的 ok/broker_connected 判定；
- 惰性导入：xtquant 仅 Windows 决策机可用（不可 pip 装），import 失败静默降级，
  mac/Linux 开发态与 mock 联调环境 feed 呈现"未接通"但接口照常 200（ticks 为空）；
- 轮询式 get_full_tick（每 feed_poll_sec 秒，默认 3s）：相比 subscribe_whole_quote 回调，
  轮询无重连回调注册失效问题、状态可观测，且自用池仅数百只，负载可控；
- 交易时段闸门：休市时 xtdata 常被关闭（qmtctl），停轮询防每日收盘后错误刷屏，
  与 handler.is_active_trading_session 同口径。

⚠ 单位口径（§FIX-1 教训）：tick["volume"] 原样透传，本模块与 Go 侧均不做手/股猜测；
上线前必须在决策机跑 scripts/probe_xtquant.py 打印原始 volume 与新浪同日成交量比对，
确认后在 Go 侧 SetVolumeToShares 固化换算系数（默认 1=股）。

English: read-only Level-1 quote feed backed by xtdata.get_full_tick on a dedicated thread.
Isolated from the trading path by design (lazy import, own error handling, session gate);
failures only surface as feed_connected=false while /health's trading verdict stays untouched.
"""
import logging
import threading
import time

log = logging.getLogger("qmt_gateway.feed")


class QuoteFeed:
    """get_full_tick 轮询缓存：codes→最新 tick 的内存环形表 + 连接态观察。"""

    def __init__(self, cfg):
        self.enable = bool(cfg.get("quote_feed", True))
        self.poll_sec = max(1.0, float(cfg.get("feed_poll_sec", 3) or 3))
        self._stop = threading.Event()
        self._thread = None
        self._lock = threading.Lock()
        self._ticks = {}          # 带后缀代码 → 归一化 tick dict
        self._codes = set()       # 订阅池（带后缀），由 /quotes 请求动态累积
        self._last_ok_at = 0.0    # 最近一轮成功轮询（time.time）
        self._import_failed = False
        self._xtdata = None

    # ---------- 生命周期 ----------

    def start(self):
        """启动轮询线程（enable=False 或已启动时为 no-op）。"""
        if not self.enable or self._thread is not None:
            return
        self._thread = threading.Thread(target=self._loop, name="quote-feed", daemon=True)
        self._thread.start()

    def stop(self):
        """停止并回收线程（幂等；join 由 Gateway.stop 的超时约定兜底）。"""
        self._stop.set()
        th, self._thread = self._thread, None
        if th is not None and th.is_alive():
            th.join(timeout=2)

    # ---------- 对外读接口 ----------

    def is_connected(self):
        """feed 连接态：仅观察，不参与交易熔断判定（/health 消费）。"""
        with self._lock:
            return self._connected_locked()

    def _connected_locked(self):
        if not self.enable or self._import_failed:
            return False
        # 交易时段内以"最近一轮成功"为准；休市时保留最近状态（收盘不断连属预期）
        return (time.time() - self._last_ok_at) < self.poll_sec * 3 + 5

    def age_sec(self):
        """距最近一次成功轮询的秒数（从未成功为 -1）。"""
        with self._lock:
            if self._last_ok_at == 0:
                return -1.0
            return round(time.time() - self._last_ok_at, 1)

    def snapshot(self, codes):
        """返回请求代码集的当前 tick；同时把这些代码并入订阅池（下轮生效）。

        :param codes: 带交易所后缀的代码列表（如 600519.SH）
        :return: dict 带后缀代码 → tick（无数据的代码不出现在结果里）
        """
        with self._lock:
            self._codes.update(codes)
            return {c: self._ticks[c] for c in codes if c in self._ticks}

    # ---------- 轮询主体 ----------

    def _ensure_xtdata(self):
        """惰性导入 xtdata；失败置永久标记（开发态静默，不再重试刷日志）。"""
        if self._xtdata is not None or self._import_failed:
            return self._xtdata is not None
        try:
            from xtquant import xtdata  # noqa: F401  仅 Windows 决策机可导入
            self._xtdata = xtdata
            log.info("[feed] xtdata 导入成功，L1 行情 feed 启动")
            return True
        except Exception as e:  # noqa: BLE001 — ImportError 之外的环境错也一并降级
            self._import_failed = True
            log.info("[feed] xtdata 不可用（%s），L1 feed 保持停用（开发态属预期）", e)
            return False

    def _loop(self):
        from handler import is_active_trading_session
        while not self._stop.is_set():
            if self._stop.wait(self.poll_sec):
                break
            try:
                if not is_active_trading_session():
                    continue
                self._poll_once()
            except Exception:  # noqa: BLE001 — feed 线程永不因行情异常退出
                log.exception("[feed] 轮询循环异常（已捕获，feed 继续）")

    def _poll_once(self):
        """单轮 get_full_tick：任何失败只记状态不抛出（隔离交易路径）。"""
        with self._lock:
            codes = list(self._codes)
        if not codes:
            return  # 尚无订阅（首轮 /quotes 到达前）
        if not self._ensure_xtdata():
            return
        try:
            raw = self._xtdata.get_full_tick(codes) or {}
        except Exception as e:  # noqa: BLE001
            log.warning("[feed] get_full_tick 失败（新浪链兜底，不影响交易）: %s", e)
            return
        now = time.time()
        normalized = {}
        for code, tk in raw.items():
            parsed = self._normalize(code, tk)
            if parsed is not None:
                normalized[code] = parsed
        with self._lock:
            self._ticks.update(normalized)
            self._last_ok_at = now

    @staticmethod
    def _normalize(code, tk):
        """xtdata 原始 tick → /quotes 契约字段（原样透传数值，不做单位换算）。

        xtdata 昨收字段名为 lastClose；time 字段历史版本有秒/毫秒两种口径，统一归一为毫秒。
        """
        if not isinstance(tk, dict):
            return None
        try:
            t_ms = float(tk.get("time") or tk.get("tickTime") or 0)
        except (TypeError, ValueError):
            t_ms = 0
        if 0 < t_ms < 1e12:
            t_ms *= 1000  # 秒口径 → 毫秒

        def f(key):
            try:
                return float(tk.get(key) or 0.0)
            except (TypeError, ValueError):
                return 0.0

        return {
            "lastPrice": f("lastPrice"),
            "open": f("open"),
            "high": f("high"),
            "low": f("low"),
            "prevClose": f("lastClose"),
            "volume": f("volume"),
            "amount": f("amount"),
            "tickTime": int(t_ms),
        }
