# 清算 Guard 回归测试（§清算 Guard 2026-09-11 生产实录）。
# 场景：真实账户持有仓位（总值十万级、仓位列表刷新前 broker 会话未同步）。
# 旧行为：连续两次空 positions 快照 → "accepting full clear" → 把满仓账本清零（生产事故）。
# 新行为：接受清空前再校验最近账户资产回报——market_value 显著为正 → 拒绝清空；
#         market_value=0（真卖出清仓）才接受；从未收到资产回报同样保守不清。
from __future__ import annotations

import os
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import handler as handler_mod  # noqa: E402
from handler import ReportHandler  # noqa: E402
from store import Store  # noqa: E402


def _seed_two_positions(store: Store) -> None:
    """播种两个真实持仓（生产实录：600279.SH + 601288.SH）。"""
    for code, name, qty in (("600279.SH", "景业智能", 200), ("601288.SH", "农业银行", 300)):
        store.upsert_position({
            "ts_code": code, "name": name, "qty": qty, "user_id": "u_test",
            "amount": qty * 10.0, "open_price": 10.0,
        })


def _mk_handler(store: Store) -> ReportHandler:
    """构造 ReportHandler：outbox 推送地址指向本地（测试不发送）。"""
    return ReportHandler(store, "http://127.0.0.1:1/report", "tok", user_id="u_test")


def test_full_clear_rejected_when_asset_still_has_market_value():
    """资产回报显示仍有市值 → 连续空快照也不得清账本（生产事故回归）。"""
    with tempfile.TemporaryDirectory() as td:
        store = Store(os.path.join(td, "gw.db"))
        _seed_two_positions(store)
        h = _mk_handler(store)
        # 资产回报先到：市值 106322.80（满足" Dutch" 满仓事实）
        h.on_account({"cash": 84.80, "frozen_cash": 0.0,
                      "total_asset": 106407.60, "market_value": 106322.80})
        h.on_positions([])   # 空 #1
        h.on_positions([])   # 空 #2（旧行为在此步清空账本）
        held = store.list_positions()
        assert len(held) == 2, f"满仓账本不该被空快照清空, got {len(held)} 条"
        assert {p["ts_code"] for p in held} == {"600279.SH", "601288.SH"}


def test_full_clear_rejected_when_no_asset_snapshot_yet():
    """从未收到资产回报（broker 未同步）同样保守不清账本。"""
    with tempfile.TemporaryDirectory() as td:
        store = Store(os.path.join(td, "gw.db"))
        _seed_two_positions(store)
        h = _mk_handler(store)
        h.on_positions([])
        h.on_positions([])
        assert len(store.list_positions()) == 2, "无资产回报时不得清账本（保守取向）"


def test_full_clear_accepted_when_market_value_is_zero():
    """真实卖出清仓：资产回报 market_value=0 → 连续空快照照旧接受清空（旧行为保留）。"""
    with tempfile.TemporaryDirectory() as td:
        store = Store(os.path.join(td, "gw.db"))
        _seed_two_positions(store)
        h = _mk_handler(store)
        h.on_account({"cash": 90000.0, "frozen_cash": 0.0,
                      "total_asset": 90000.0, "market_value": 0.0})
        h.on_positions([])
        h.on_positions([])
        assert store.list_positions() == [], "真清仓（市值=0）后空快照应接受清账本"


def test_nonempty_snapshot_resets_counter_and_reconciles():
    """非空快照回填：持仓正常对账（集合外的旧持仓被剔除、集合内的被 upsert）。"""
    with tempfile.TemporaryDirectory() as td:
        store = Store(os.path.join(td, "gw.db"))
        _seed_two_positions(store)
        h = _mk_handler(store)
        h.on_positions([{"ts_code": "600279.SH", "name": "景业智能", "qty": 200,
                         "amount": 2000.0, "open_price": 10.0}])
        held = store.list_positions()
        # 非空快照语义：只保留快照集合内持仓 → 601288.SH 应被剔除
        assert {p["ts_code"] for p in held} == {"600279.SH"}
