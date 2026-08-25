#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.ids — signal_id 幂等（AUTO_TRADING_PLAN M2）。

同一 signal_id 只允许下一笔单：网关侧以 orders.signal_id UNIQUE 去重。§G1 改造为
「claim 占位 → 下单 → settle 回填」三段式——claim 是 SQLite 原子抢占，天然互斥，
并发重试/断线重发只会有一方抢到；下单失败 release 释放占位；进程崩溃残留的
pending 行永久阻塞该 signal_id（安全侧失效，杜绝重复真实下单）。
（English: one order per signal_id via atomic claim-before-place on the unique key;
release on failure; crash-left pending rows stay blocked by design — fail-safe.）
"""
import logging

log = logging.getLogger("qmt_gateway.ids")


class Idempotency:
    """基于 store.orders 的幂等守卫（占位式）。"""

    def __init__(self, store):
        self.store = store

    def check(self, signal_id):
        """检查 signal_id 是否已处理。返回 (is_new:bool, existing:dict|None)。"""
        if not signal_id:
            return False, None  # 空 signal_id 由网关层 400 拒绝（§G2），此处仅兜底
        row = self.store.order_by_signal(signal_id)
        if row is None:
            return True, None
        return False, dict(row)

    def claim(self, draft):
        """原子占位。返回 (claimed:bool, existing:dict|None)。"""
        sid = draft.get("signal_id", "")
        if not sid:
            return False, None
        claimed, existing = self.store.claim_order(draft)
        if not claimed:
            log.info("[ids] signal_id=%s already claimed (status=%s)",
                     sid, (existing or {}).get("status", ""))
        return claimed, existing

    def settle(self, order):
        """下单成功后回填真实委托号与状态（首个非占位 order_id 固化）。"""
        is_new = self.store.upsert_order(order)
        if not is_new:
            log.info("[ids] signal_id=%s settled onto existing row", order.get("signal_id"))
        return is_new

    # 兼容旧调用名
    record = settle

    def release(self, signal_id):
        """下单失败释放 pending 占位。"""
        self.store.release_pending(signal_id)
