#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.ids — signal_id 幂等（AUTO_TRADING_PLAN M2）。

同一 signal_id 只允许下一笔单：网关侧以 orders.signal_id UNIQUE 去重。§G1 改造为
「claim 占位 → 下单 → settle 回填」三段式——claim 是 SQLite 原子抢占，天然互斥，
并发重试/断线重发只会有一方抢到；下单失败 release 释放占位；进程崩溃残留的
pending 行永久阻塞该 signal_id（安全侧失效，杜绝重复真实下单）。
（English: one order per signal_id via atomic claim-before-place on the unique key;
release on failure; crash-left pending rows stay blocked by design — fail-safe.）

§M-2（2026-09-22 修复批）补充上述 fail-safe 的**边界**，结论本身不变：
「阻塞」只应发生在**本端尚未取得结果**的窗口内，而不是永久的——旧实现 place_order 抛
异常时既不 release 也没人收尾（release_stale_pending 只在 start() 调一次），
同 signal_id 从此恒 409，比"重复下单"更早发生的是"信号永久死锁"。
现由 gateway._do_order 的 try/finally 释放未 settle 的占位，并加了运行期巡检
（gateway._sweep_stale_pending，默认 600s 超龄）。两者清理的都只是**本地占位行**：
既不自动重发、也不把单子重新排进队列，是否重试仍由调用方决策——
防重复真实下单的物理保证（signal_id 唯一键 + 不重排）一字未改。
"""
import logging

log = logging.getLogger("qmt_gateway.ids")


class Idempotency:
    """基于 store.orders 的幂等守卫（占位式）。"""

    def __init__(self, store):
        """构造幂等守卫。:param store: 本地 SQLite 账本（Store 实例），占位/查询均委托其原子操作。"""
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
        # 委托 store 做 SQLite 原子抢占；抢不到说明已被处理/进行中
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

    # 兼容旧调用名：record 作为 settle 的别名保留
    record = settle

    def release(self, signal_id):
        """下单失败释放 pending 占位。"""
        self.store.release_pending(signal_id)
