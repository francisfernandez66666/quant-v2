# -*- coding: utf-8 -*-
"""§REPORT-STAMP 历史行回填脚本（scripts/backfill_report_stamp_20261119.py）回归锁。

这条脚本会**写生产库**，所以它的每一条"不动"都比"动"更重要：
  * 证据不唯一（同裸号跨两天）⇒ 必须跳过，不许按时间猜；
  * 码/方向对不上 ⇒ 必须跳过（裸号跨日复用 + 换票时，只按裸号匹配会补错日子）；
  * 缺省 dry-run ⇒ 不加 `--apply` 一个字节都不许写；
  * 写前必须留 JSON 备份，写后必须幂等（再跑一次 ready=0）。
生产 2026-11-19 13:09 首次实跑就是按这套判据：13 条成交（今日 7 笔已成 + 5 笔拆单 + 1 笔）
先补，随后"成交→委托"反向再补 7 条委托；余下 32/30 条因证据缺失原样保留。

用例用的是**临时 sqlite**，只建脚本真正读写的四个列族（fills/orders 的 id、时间戳、码、方向），
不复制生产 schema——脚本只依赖这几列，多建的列会让用例以为自己在测别的东西。
"""

import importlib.util
import os
import sqlite3

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
SCRIPT = os.path.join(ROOT, "scripts", "backfill_report_stamp_20261119.py")


def load_script():
    spec = importlib.util.spec_from_file_location("backfill_report_stamp", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture()
def db_path(tmp_path):
    path = str(tmp_path / "live.db")
    con = sqlite3.connect(path)
    con.executescript(
        """
        CREATE TABLE fills (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            order_id TEXT NOT NULL,
            trade_id TEXT DEFAULT '',
            code TEXT NOT NULL,
            side TEXT NOT NULL,
            qty INTEGER NOT NULL DEFAULT 0,
            price REAL NOT NULL DEFAULT 0,
            traded_at TEXT NOT NULL
        );
        CREATE TABLE orders (
            order_id TEXT PRIMARY KEY,
            signal_id TEXT DEFAULT '',
            code TEXT NOT NULL,
            side TEXT NOT NULL,
            qty INTEGER NOT NULL DEFAULT 0,
            created_at TEXT NOT NULL
        );
        """
    )
    con.commit()
    con.close()
    return path


def seed(con, table, rows):
    for row in rows:
        cols = ",".join(row.keys())
        marks = ",".join("?" * len(row))
        con.execute("INSERT INTO %s (%s) VALUES (%s)" % (table, cols, marks), tuple(row.values()))
    con.commit()


def test_resolves_fill_from_dated_order(db_path):
    """同日同码同方向的委托带日期 ⇒ 成交补齐那一天，且时分秒原样保留。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(
        con,
        "orders",
        [
            {"order_id": "1246@20261119", "code": "002093.SZ", "side": "卖出", "qty": 4900, "created_at": "2026-11-19 10:01:20"},
        ],
    )
    seed(
        con,
        "fills",
        [
            {"order_id": "1246", "trade_id": "T1", "code": "002093.SZ", "side": "卖出", "qty": 2500, "price": 6.89, "traded_at": "10:01:21"},
        ],
    )
    ready, unresolved = mod.resolve_fills(con)
    assert len(ready) == 1 and not unresolved
    assert ready[0]["day"] == "2026-11-19"
    assert mod.apply_fills(con, ready) == 1
    con.commit()
    stamp = con.execute("SELECT traded_at FROM fills").fetchone()[0]
    assert stamp == "2026-11-19 10:01:21", "只补日期，时分秒必须原样"
    con.close()


def test_skips_ambiguous_bare_id_across_days(db_path):
    """同一裸号在两天都有委托（柜台号跨日复用）⇒ 证据不唯一，必须跳过。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(
        con,
        "orders",
        [
            {"order_id": "1246@20261118", "code": "002093.SZ", "side": "卖出", "qty": 100, "created_at": "2026-11-18 10:01:20"},
            {"order_id": "1246@20261119", "code": "002093.SZ", "side": "卖出", "qty": 4900, "created_at": "2026-11-19 10:01:20"},
        ],
    )
    seed(
        con,
        "fills",
        [
            {"order_id": "1246", "trade_id": "T1", "code": "002093.SZ", "side": "卖出", "qty": 2500, "price": 6.89, "traded_at": "10:01:21"},
        ],
    )
    ready, unresolved = mod.resolve_fills(con)
    assert not ready, "两个候选日期时不许挑一个（补错日子的账比不补更难查）"
    assert len(unresolved) == 1 and unresolved[0]["reason"] == "no_unique_order_day"
    con.close()


def test_skips_when_code_or_side_mismatch(db_path):
    """裸号对上但码/方向对不上 ⇒ 跳过（裸号跨日复用 + 换票时按裸号匹配会补错）。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(
        con,
        "orders",
        [
            {"order_id": "777@20261119", "code": "000001.SZ", "side": "买入", "qty": 100, "created_at": "2026-11-19 09:31:00"},
        ],
    )
    seed(
        con,
        "fills",
        [
            {"order_id": "777", "trade_id": "T1", "code": "600000.SH", "side": "买入", "qty": 100, "price": 8.0, "traded_at": "09:31:05"},
            {"order_id": "777", "trade_id": "T2", "code": "000001.SZ", "side": "卖出", "qty": 100, "price": 12.0, "traded_at": "09:31:06"},
        ],
    )
    ready, unresolved = mod.resolve_fills(con)
    assert not ready and len(unresolved) == 2
    con.close()


def test_reverse_resolves_order_from_dated_fill(db_path):
    """反向：成交已有完整日期 ⇒ 同裸号同码同方向的缺日期委托也补上。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(
        con,
        "orders",
        [
            {"order_id": "1898", "code": "000690.SZ", "side": "买入", "qty": 300, "created_at": "10:46:50"},
        ],
    )
    seed(
        con,
        "fills",
        [
            {"order_id": "1898", "trade_id": "T1", "code": "000690.SZ", "side": "买入", "qty": 300, "price": 5.87, "traded_at": "2026-11-19 10:47:26"},
        ],
    )
    ready, unresolved = mod.resolve_orders(con)
    assert len(ready) == 1 and not unresolved
    assert mod.apply_orders(con, ready) == 1
    con.commit()
    assert con.execute("SELECT created_at FROM orders").fetchone()[0] == "2026-11-19 10:46:50"
    con.close()


def test_idempotent_second_pass(db_path):
    """补完之后再跑一遍必须 ready=0（幂等：重复跑不会再挑出任何行）。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(con, "orders", [{"order_id": "A@20261119", "code": "000001.SZ", "side": "买入", "qty": 100, "created_at": "2026-11-19 09:31:00"}])
    seed(con, "fills", [{"order_id": "A", "trade_id": "T1", "code": "000001.SZ", "side": "买入", "qty": 100, "price": 12.0, "traded_at": "09:31:05"}])
    ready, _ = mod.resolve_fills(con)
    mod.apply_fills(con, ready)
    con.commit()
    again, _ = mod.resolve_fills(con)
    assert not again
    con.close()


def test_dry_run_writes_nothing(db_path, capsys):
    """缺省 dry-run：库一个字节都不能动（这条脚本是写生产库的，默认必须只读）。"""
    mod = load_script()
    con = sqlite3.connect(db_path)
    seed(con, "orders", [{"order_id": "A@20261119", "code": "000001.SZ", "side": "买入", "qty": 100, "created_at": "2026-11-19 09:31:00"}])
    seed(con, "fills", [{"order_id": "A", "trade_id": "T1", "code": "000001.SZ", "side": "买入", "qty": 100, "price": 12.0, "traded_at": "09:31:05"}])
    con.close()

    code = mod.main.__wrapped__ if hasattr(mod.main, "__wrapped__") else mod.main
    argv_backup = list(os.sys.argv)
    try:
        os.sys.argv = ["backfill", "--db", db_path]
        assert code() == 0
    finally:
        os.sys.argv = argv_backup

    out = capsys.readouterr().out
    assert "BACKFILL_RESULT=DRYRUN" in out
    con = sqlite3.connect(db_path)
    assert con.execute("SELECT traded_at FROM fills").fetchone()[0] == "09:31:05"
    con.close()
