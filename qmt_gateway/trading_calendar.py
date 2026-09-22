# -*- coding: utf-8 -*-
"""qmt_gateway.trading_calendar — 真实交易日历只读视图（§A5，2026-09-22 PM 批清扫）。

旧现状：handler.is_active_trading_session / qmt_bridge.is_trading_window 各自用
「weekday()<5 即交易日」启发式（注释自认简化），节假日被当成活跃交易时段——该判定
驱动断连上报抑制、failover 翻转（gateway.py）、行情订阅心跳，节假日窗口期会误报/误翻转。
Go 引擎侧有真日历（internal/data/trade_calendar.go）并落盘 trading_calendar.json
（{"saved_at":"YYYYMMDD","closed_days":["YYYYMMDD",...]}，QUANT_DATA_DIR 或 ~/.quant-trading-v2），
本模块读同一文件，Python 侧不再需要网络源。

口径与 Go 完全一致：只消费 closed_days（非周末休市日集合）。
降级语义：文件不存在/解析失败/字段缺失 → closed_days() 返回 None，调用方维持
「周末口径」旧行为——网关被单独部署（沙箱拷单文件）或 Go 端从未落盘时零破坏。
（English: read-only view of the Go engine's on-disk trading calendar; any failure to
load degrades to the legacy weekday-only heuristic, so a gateway deployed without the
Go side is never broken by this module.）
"""
import json
import logging
import os
import threading
import time
from datetime import datetime

log = logging.getLogger("qmt_gateway.trading_calendar")

# §A5 刷新周期：日历每天至多变一次（Go 24h 刷新），5 分钟缓存足够且避免每调用读盘。
_RELOAD_SEC = 300.0

_lock = threading.Lock()
# 缓存三元组：加载时间戳 / closed_days 集合（None=不可得）/ 已见文件签名（mtime_ns+size，变了即重读）
_state = {"ts": 0.0, "closed": None, "sig": None, "warned": False}


def calendar_path():
    """日历文件路径，与 Go calendarCacheFilePath() 同口径：QUANT_DATA_DIR 优先，
    否则 ~/.quant-trading-v2/trading_calendar.json。"""
    d = os.environ.get("QUANT_DATA_DIR", "")
    if not d:
        d = os.path.join(os.path.expanduser("~"), ".quant-trading-v2")
    return os.path.join(d, "trading_calendar.json")


def closed_days(now=None):
    """返回非周末休市日集合 set["YYYYMMDD"]；None=日历不可得（调用方降级周末口径）。

    至多每 _RELOAD_SEC 秒重读一次文件；文件 mtime+size 未变直接命中缓存。
    读失败保持上一次结果（宁可用旧日历也不回退启发式——Go 端日历窗口通常一年，
    短期内 closed_days 只会因刷新而增补，不会失效）。
    """
    now = now or time.time()
    with _lock:
        if _state["closed"] is not None and now - _state["ts"] < _RELOAD_SEC:
            return _state["closed"]
        path = calendar_path()
        try:
            st = os.stat(path)
            sig = (st.st_mtime_ns, st.st_size)
        except OSError:
            # 文件不存在：首次打一条日志即可（每 5 分钟被调一次，不刷屏）
            if not _state["warned"]:
                _state["warned"] = True
                log.warning("[calendar] 交易日历文件不存在（%s），节假日判定降级为周末口径", path)
            _state["ts"] = now
            return _state["closed"]  # 通常为 None=降级
        if _state["closed"] is not None and _state["sig"] == sig and now - _state["ts"] < _RELOAD_SEC * 6:
            _state["ts"] = now
            return _state["closed"]
        try:
            with open(path, "r", encoding="utf-8") as f:
                doc = json.load(f)
            days = doc.get("closed_days")
            saved_at = doc.get("saved_at") or ""
            if not isinstance(days, list) or not saved_at:
                raise ValueError("closed_days/saved_at 字段缺失或类型错误")
            _state["closed"] = {str(x) for x in days}
            _state["sig"] = sig
            _state["ts"] = now
            _state["warned"] = False
            log.info("[calendar] 交易日历已加载（保存于 %s，休市日 %d 天）", saved_at, len(_state["closed"]))
        except Exception as exc:  # noqa: BLE001
            if not _state["warned"]:
                _state["warned"] = True
                log.warning("[calendar] 交易日历解析失败（%s: %s），维持上次口径或降级周末判定", path, exc)
            _state["ts"] = now
        return _state["closed"]


def is_trading_day(dt):
    """dt（北京时间 aware 或 naive 墙钟）是否交易日：周末 False；命中 closed_days False；
    日历不可得时仅按周末判定（=旧启发式，零回退风险）。"""
    if dt.weekday() >= 5:
        return False
    days = closed_days()
    if days is not None and dt.strftime("%Y%m%d") in days:
        return False
    return True
