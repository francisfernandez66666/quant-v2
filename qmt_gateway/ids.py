#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.ids — signal_id 幂等（AUTO_TRADING_PLAN M2）。

同一 signal_id 只允许下一笔单：网关侧以 orders.signal_id 唯一键去重，重试/断线重发时
返回已有 order_id（幂等成功），绝不重复下单。与首尔侧 Controller 的幂等键语义一致。
（English: signal_id idempotency — one order per signal_id. The gateway dedupes on the
orders.signal_id unique key; retries/replays return the existing order_id (idempotent success)
instead of placing a duplicate. Semantics match the Seoul-side Controller idempotency key.）
"""
import logging

log = logging.getLogger("qmt_gateway.ids")


class Idempotency:
    """基于 store.orders 的幂等守卫。"""

    def __init__(self, store):
        self.store = store

    def check(self, signal_id):
        """检查 signal_id 是否已处理。返回 (is_new:bool, existing:dict|None)。"""
        if not signal_id:
            return False, None  # 无 signal_id 的请求按新单处理（调用方负责兜底生成）
        row = self.store.order_by_signal(signal_id)
        if row is None:
            return True, None
        return False, dict(row)

    def record(self, order):
        """登记一笔新单（signal_id 冲突时视为幂等重复，不覆盖原 order_id）。"""
        is_new = self.store.upsert_order(order)
        if not is_new:
            log.info("[ids] duplicate signal_id=%s → return existing order", order.get("signal_id"))
        return is_new