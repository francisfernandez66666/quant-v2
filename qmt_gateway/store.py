#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.store — 网关本地 SQLite 账本（AUTO_TRADING_PLAN M2）。

三表与首尔侧 Go store（real_positions/orders/fills）字段对齐，作为断线时的本地缓存与
幂等去重依据；联机后把增量事件（trade/order/positions/disconnect）经 outbox 推给首尔
POST /api/qmt/report。

线程安全：单连接 + 全部读写统一持 RLock（ThreadingHTTPServer 每连接一线程并发访问）。
§R1 幂等占位（G1/G2）：claim_order 以 signal_id UNIQUE 先插 pending 占位再下单——
原子抢占，杜绝 check→place→record 窗口内的重复真实下单与崩溃后重下；
首个非占位 order_id 一旦落库不再被覆盖（回调回报的交易所委托号只做一次替换）。
§G10 成交去重：fills 按 (order_id,side,price,qty) 在时间窗内判重，防通道重放导致持仓翻倍。
（English: local SQLite book aligned with the Seoul-side Go store. Single connection guarded by an
RLock for ALL reads/writes. Claim-before-place idempotency on orders.signal_id UNIQUE prevents
duplicate real orders across concurrent retries and crashes; the first real order_id wins and is
never overwritten. Trade fills are de-duplicated in a sliding time window against channel replays.）
"""
import json
import os
import sqlite3
import threading
import time
from datetime import datetime, timedelta, timezone

# 占位 order_id 前缀：pending:<signal_id>（下单前占位）；真实委托号落库后不可被占位值覆盖
CN_TZ = timezone(timedelta(hours=8))  # 北京时间（UTC+8），用于时间戳字段统一
FILL_DEDUP_WINDOW_SEC = 120  # 成交去重时间窗（秒）


def is_placeholder_order_id(oid):
    """空串/pending:/seq: 视为占位，允许被真实交易所委托号替换。"""
    if not oid:
        return True
    return oid.startswith("pending:") or oid.startswith("seq:")


def order_id_rank(oid):
    """§G4 委托引用等级：pending(0) < seq(1) < 交易所真实委托号(2)。只允许升级替换。"""
    if not oid:
        return 0
    if oid.startswith("pending:"):
        return 0
    if oid.startswith("seq:"):
        return 1
    return 2


class Store:
    """SQLite 账本。"""

    def __init__(self, path):
        """初始化 SQLite 账本：打开数据库连接并建立线程安全锁。

        :param path: 数据库文件路径。库文件尚不存在时新建并建表，否则走迁移逻辑
                     （老库只补新列，保留既有数据）。启动后统一建 outbox 表
                     （§ROBUST 持久化回报队列，旧库也补建）。English: opens the SQLite
                     DB, creates schema on first run or migrates old DBs, guards all
                     access with an RLock for multi-thread safety, and ensures the
                     persistent outbox table exists.
        """
        self.path = path
        # 全部读写统一持 RLock，保证多线程并发安全
        self._lock = threading.RLock()
        # 已存在的库不再建表（保留数据）
        need_init = not os.path.exists(path)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        with self._lock:
            if need_init:
                self._init_schema()
            else:
                self._migrate_schema()
            # §ROBUST 持久化 outbox：回报先落库再发送，崩溃/重启后自动续发（新老库都建）
            self._conn.execute(
                """CREATE TABLE IF NOT EXISTS outbox (
                    id         INTEGER PRIMARY KEY AUTOINCREMENT,
                    payload    TEXT NOT NULL,
                    created_at TEXT
                )""")
            self._conn.commit()

    def _init_schema(self):
        """新建数据库时初始化全量表结构：持仓/委托/成交/幂等/outbox 等核心表。

        首次运行（库文件不存在）时调用，executescript 批量建表；含 real_positions、
        orders、fills、outbox、pending 等，索引一并建立。English: creates the full
        schema (positions/orders/fills/outbox/pending) on first run via executescript.
        """
        cur = self._conn.cursor()
        cur.executescript(
            """
            CREATE TABLE IF NOT EXISTS real_positions (
                ts_code       TEXT PRIMARY KEY,
                name          TEXT,
                qty           INTEGER,
                cost_price    REAL,
                amount        REAL,
                highest_price REAL,
                strategy      TEXT,
                signal_id     TEXT,
                updated_at    TEXT,
                user_id       TEXT           -- P1-9：多账号隔离归属
            );
            CREATE TABLE IF NOT EXISTS orders (
                order_id   TEXT PRIMARY KEY,
                signal_id  TEXT UNIQUE,
                code       TEXT,
                side       TEXT,
                status     TEXT,
                price      REAL,
                qty        INTEGER,
                created_at TEXT,
                user_id       TEXT           -- P1-9：多账号隔离归属
            );
            CREATE TABLE IF NOT EXISTS fills (
                id        INTEGER PRIMARY KEY AUTOINCREMENT,
                order_id  TEXT,
                code      TEXT,
                side      TEXT,
                price     REAL,
                qty       INTEGER,
                amount    REAL,
                traded_at TEXT,
                signal_id TEXT,
                user_id       TEXT,          -- P1-9：多账号隔离归属
                trade_id      TEXT DEFAULT '' -- §G1（2026-08-29）：唯一成交编号，部成去重
            );
            -- §QMT-DUAL 派发队列（QueuedBroker + qmt_bridge.py 兜底路径）：
            -- 量仔下单先入此表，由 QMT 客户端内置策略桥消费执行；结果回填后按现有
            -- orders/fills 账本 + outbox 回报量仔，HTTP 契约不变。
            CREATE TABLE IF NOT EXISTS dispatch (
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                seq         TEXT UNIQUE,          -- "seq:<id>" 不透明引用（量仔撤单锚点）
                signal_id   TEXT DEFAULT '',
                kind        TEXT DEFAULT 'order', -- order | cancel
                code        TEXT DEFAULT '',
                side        TEXT DEFAULT '',
                price_type  TEXT DEFAULT '',
                price       REAL DEFAULT 0,
                qty         INTEGER DEFAULT 0,
                strategy    TEXT DEFAULT '',
                order_id    TEXT DEFAULT '',      -- 交易所委托号（order 结果回填，供撤单）
                status      TEXT DEFAULT 'pending', -- pending|inflight|done
                result      TEXT DEFAULT '',      -- JSON 结果（ok/err/order_id 等）
                created_at  TEXT DEFAULT '',
                user_id     TEXT DEFAULT ''
            );
            CREATE INDEX IF NOT EXISTS idx_dispatch_status ON dispatch(status);
            CREATE INDEX IF NOT EXISTS idx_dispatch_signal ON dispatch(signal_id);
            -- §QMT-DUAL 桥状态：心跳 + 持仓/资产快照（QueuedBroker 只读，桥只写）
            CREATE TABLE IF NOT EXISTS bridge_state (
                key         TEXT PRIMARY KEY,
                value       TEXT,
                updated_at  TEXT
            );
            """
        )
        self._conn.commit()

    def _migrate_schema(self):
        """P1-9：老库兼容——三表补加 user_id 列（多账号隔离）。

        SQLite ALTER TABLE 不支持 IF NOT EXISTS，故先查 PRAGMA 列信息再按需加列。
        """
        for t in ("real_positions", "orders", "fills"):
            cols = [r[1] for r in self._conn.execute(
                "PRAGMA table_info(%s)" % t).fetchall()]
            if "user_id" not in cols:
                self._conn.execute(
                    "ALTER TABLE %s ADD COLUMN user_id TEXT DEFAULT ''" % t)
            if t == "fills" and "trade_id" not in cols:
                # §修复 G1（2026-08-29）：唯一成交编号列，用于去重（避免部成重复丢单）
                self._conn.execute(
                    "ALTER TABLE fills ADD COLUMN trade_id TEXT DEFAULT ''")
        # §QMT-DUAL：老库补建派发队列与桥状态表（dispatch/bridge_state）
        self._conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS dispatch (
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                seq         TEXT UNIQUE,
                signal_id   TEXT DEFAULT '',
                kind        TEXT DEFAULT 'order',
                code        TEXT DEFAULT '',
                side        TEXT DEFAULT '',
                price_type  TEXT DEFAULT '',
                price       REAL DEFAULT 0,
                qty         INTEGER DEFAULT 0,
                strategy    TEXT DEFAULT '',
                order_id    TEXT DEFAULT '',
                status      TEXT DEFAULT 'pending',
                result      TEXT DEFAULT '',
                created_at  TEXT DEFAULT '',
                user_id     TEXT DEFAULT ''
            );
            CREATE INDEX IF NOT EXISTS idx_dispatch_status ON dispatch(status);
            CREATE INDEX IF NOT EXISTS idx_dispatch_signal ON dispatch(signal_id);
            CREATE TABLE IF NOT EXISTS bridge_state (
                key         TEXT PRIMARY KEY,
                value       TEXT,
                updated_at  TEXT
            );
            """
        )
        self._conn.commit()

    # ── orders ──
    def claim_order(self, draft):
        """§G1 原子占位：以 signal_id 抢占一个 pending 行。

        返回 (claimed:bool, existing:dict|None)。已存在时返回既有行（含 pending 占位），
        调用方据此实现幂等或拒绝进行中请求。崩溃残留的 pending 行会永久阻塞该 signal_id
        （安全侧失效：宁可拒绝也不重复真实下单），启动时由 Gateway 打警告日志。
        """
        # 取出幂等键 signal_id，作为占位行的唯一约束
        sid = draft.get("signal_id", "")
        with self._lock:
            # 以 signal_id UNIQUE 做原子 INSERT；冲突则啥也不做（占位失败=已被抢占）
            cur = self._conn.execute(
                """INSERT INTO orders(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
                   VALUES(?,?,?,?,?,?,?,?,?)
                   ON CONFLICT(signal_id) DO NOTHING""",
                (
                    "pending:" + sid,
                    sid,
                    draft.get("code", ""),
                    draft.get("side", ""),
                    "pending",
                    float(draft.get("price", 0) or 0),
                    int(draft.get("qty", 0) or 0),
                    draft.get("created_at", "") or _now_cn(),
                    draft.get("user_id", ""),
                ),
            )
            if cur.rowcount == 0:
                row = self._conn.execute(
                    "SELECT * FROM orders WHERE signal_id = ?", (sid,)
                ).fetchone()
                self._conn.commit()
                return False, (dict(row) if row else None)
            self._conn.commit()
            return True, None

    def release_pending(self, signal_id):
        """下单失败时释放 pending 占位，允许后续重试。仅删未结算的占位行。"""
        with self._lock:
            self._conn.execute(
                "DELETE FROM orders WHERE signal_id = ? AND status = 'pending'",
                (signal_id,),
            )
            self._conn.commit()

    def upsert_order(self, order):
        """插入/更新委托。返回是否新订单。

        §G2 语义：status/price/qty/created_at 随最新事件刷新；order_id 只在「存量是占位
        且新值是真实委托号」时替换一次（pending:→seq:→交易所委托号），真实委托号互不覆盖。
        """
        # 取 signal_id 与待写入的 order_id（可能是占位串或真实委托号）
        sid = order.get("signal_id", "")
        new_oid = str(order.get("order_id", "") or "")
        with self._lock:
            # 查现存行：无则插入新订单，有则按等级规则升级 order_id
            row = self._conn.execute(
                "SELECT * FROM orders WHERE signal_id = ?", (sid,)
            ).fetchone()
            if row is None:
                self._conn.execute(
                    """INSERT INTO orders(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
                       VALUES(?,?,?,?,?,?,?,?,?)""",
                    (
                        new_oid or ("pending:" + sid),
                        sid,
                        order.get("code", ""),
                        order.get("side", ""),
                        order.get("status", ""),
                        float(order.get("price", 0) or 0),
                        int(order.get("qty", 0) or 0),
                        order.get("created_at", ""),
                        order.get("user_id", ""),
                    ),
                )
                self._conn.commit()
                return True
            # 仅在「新引用等级高于存量（占位→seq→真实号）」时才替换 order_id，真实号互不覆盖
            final_oid = row["order_id"]
            if order_id_rank(new_oid) > order_id_rank(final_oid):
                final_oid = new_oid
            self._conn.execute(
                """UPDATE orders SET order_id=?, status=?, price=?, qty=?, created_at=?, user_id=?
                   WHERE signal_id=?""",
                (
                    final_oid,
                    order.get("status", row["status"]),
                    float(order.get("price", 0) or row["price"] or 0),
                    int(order.get("qty", 0) or 0) or row["qty"] or 0,
                    order.get("created_at", "") or row["created_at"],
                    order.get("user_id", row["user_id"]),
                    sid,
                ),
            )
            self._conn.commit()
            return False

    def order_by_signal(self, signal_id):
        """按 signal_id 查委托（幂等键）。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE signal_id = ?", (signal_id,))
            return cur.fetchone()

    def order_by_id(self, order_id):
        """按交易所/模拟委托号查委托（非幂等键，用于反向检索）。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE order_id = ?", (order_id,))
            return cur.fetchone()

    def order_filled_qty(self, order_id):
        """该委托累计成交量（fills 求和，部成累计）。无成交返回 0。"""
        with self._lock:
            cur = self._conn.execute(
                "SELECT COALESCE(SUM(qty),0) AS q FROM fills WHERE order_id = ?",
                (str(order_id),))
            return int(cur.fetchone()["q"])

    def list_orders(self):
        """列出全部委托（按创建时间倒序），供 /state 端点返回。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders ORDER BY created_at DESC")
            return [dict(r) for r in cur.fetchall()]

    def list_pending(self):
        """全部未结算占位行（进程重启后仍 pending = 经历过下单窗口 crash，需人工确认）。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE status = 'pending'")
            return [dict(r) for r in cur.fetchall()]

    def release_stale_pending(self, max_age_sec):
        """§修复 T5（2026-08-29）：启动时清理崩溃残留的超时 pending 占位。

        claim 成功→settle 前崩溃会在 orders 表留下 status='pending' 行，进程重启后若只 warning
        不释放，该 signal_id 永久被 claim 占位阻塞（首尔重试恒得 409 'duplicate in-flight'），
        对应信号永不能再下单。此处删除「创建超过 max_age_sec 秒」的 pending 行，安全解锁。
        """
        import time as _time  # noqa: PLC0415
        threshold = (_time.time() - max_age_sec) if max_age_sec > 0 else 0
        with self._lock:
            cur = self._conn.execute(
                "DELETE FROM orders WHERE status = 'pending' AND created_at <> '' "
                "AND CAST(strftime('%s', created_at) AS INTEGER) < ?",
                (threshold,),
            )
            self._conn.commit()
            return cur.rowcount

    # ── positions ──
    def upsert_position(self, p):
        """upsert 单条持仓，保持 highest_price 单调。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (p["ts_code"],)
            ).fetchone()
            highest = p.get("highest_price", 0) or 0
            if row and row["highest_price"] > highest:
                highest = row["highest_price"]
            self._conn.execute(
                """INSERT INTO real_positions
                     (ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
                    VALUES(?,?,?,?,?,?,?,?,?,?)
                    ON CONFLICT(ts_code) DO UPDATE SET
                      name=excluded.name, qty=excluded.qty, cost_price=excluded.cost_price,
                      amount=excluded.amount, highest_price=excluded.highest_price,
                      strategy=excluded.strategy, signal_id=excluded.signal_id, updated_at=excluded.updated_at,
                      user_id=excluded.user_id""",
                (
                    p["ts_code"], p.get("name", ""), p.get("qty", 0), p.get("cost_price", 0.0),
                    p.get("amount", 0.0), highest, p.get("strategy", ""), p.get("signal_id", ""),
                    p.get("updated_at", ""), p.get("user_id", ""),
                ),
            )
            self._conn.commit()

    def _fill_is_duplicate(self, f, cutoff):
        """§修复 G1（2026-08-29）：优先用唯一成交编号 trade_id 去重——同一委托的多笔部成
        (order_id/side/price/qty 完全相同) 不会再被误判为重放而丢单；无 trade_id 时退回
        原 (order_id,side,price,qty,时间窗) 兼容逻辑。"""
        tid = str(f.get("trade_id", "") or "")
        if tid:
            cur = self._conn.execute(
                "SELECT 1 FROM fills WHERE trade_id = ? LIMIT 1", (tid,)
            )
            return cur.fetchone() is not None
        if not f.get("order_id"):
            return False
        cur = self._conn.execute(
            """SELECT 1 FROM fills
               WHERE order_id = ? AND side = ? AND price = ? AND qty = ? AND traded_at >= ?
               LIMIT 1""",
            (f["order_id"], f.get("side", ""), float(f.get("price", 0) or 0),
             int(f.get("qty", 0) or 0), cutoff),
        )
        return cur.fetchone() is not None

    def apply_fill(self, f):
        """成交应用到持仓（买=加仓加权成本；卖=减仓/清仓删行）。

        返回 (position_dict|None, is_duplicate:bool)。重复回报不改动持仓、不重复入 fills。
        """
        # 取成交方向，并计算去重时间窗下界（早于该时间的重放允许）
        fill_side = f.get("side", "")
        cutoff = (datetime.now(CN_TZ) - timedelta(seconds=FILL_DEDUP_WINDOW_SEC)).strftime(
            "%Y-%m-%dT%H:%M:%S")
        with self._lock:
            # 先判重：窗口内相同 (order_id,side,price,qty) 视为通道重放，直接返回不改动
            if self._fill_is_duplicate(f, cutoff):
                row = self._conn.execute(
                    "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
                ).fetchone()
                return (dict(row) if row else None), True
            row = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
            ).fetchone()
            if fill_side == "买入":
                uid = f.get("user_id", "")
                if row is None:
                    self._conn.execute(
                        """INSERT INTO real_positions
                             (ts_code, name, qty, cost_price, amount, highest_price, updated_at, user_id)
                            VALUES(?,?,?,?,?,?,?,?)""",
                        (f["code"], f.get("name", ""), f.get("qty", 0), f.get("price", 0.0),
                         f.get("amount", 0.0), f.get("price", 0.0), f.get("traded_at", ""), uid),
                    )
                else:
                    old_qty, old_cost = row["qty"], row["cost_price"]
                    new_qty = old_qty + f["qty"]
                    new_cost = (old_qty * old_cost + f["qty"] * f["price"]) / new_qty
                    highest = max(row["highest_price"], f["price"])
                    self._conn.execute(
                        """UPDATE real_positions
                            SET qty=?, cost_price=?, amount=?, highest_price=?, updated_at=?, user_id=?
                            WHERE ts_code=?""",
                        (new_qty, new_cost, new_qty * f["price"], highest, f.get("traded_at", ""), uid, f["code"]),
                    )
            else:
                if row is None:
                    return None, False
                remain = row["qty"] - f["qty"]
                if remain <= 0:
                    self._conn.execute(
                        "DELETE FROM real_positions WHERE ts_code = ?", (f["code"],)
                    )
                else:
                    self._conn.execute(
                        """UPDATE real_positions SET qty=?, amount=?, updated_at=?, user_id=?
                            WHERE ts_code=?""",
                        (remain, remain * f["price"], f.get("traded_at", ""), f.get("user_id", ""), f["code"]),
                    )
            self._conn.execute(
                """INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id, trade_id)
                    VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (f.get("order_id", ""), f["code"], fill_side, f["price"], f["qty"],
                 f.get("amount", 0.0), f.get("traded_at", ""), f.get("signal_id", ""), f.get("user_id", ""),
                 str(f.get("trade_id", "") or "")),
            )
            self._conn.commit()
            cur = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
            )
            row = cur.fetchone()
            return (dict(row) if row else None), False

    def list_positions(self):
        """返回全部持仓（按市值降序）。返回 [{ts_code,name,qty,cost_price,amount,...}] 字典列表。

        供 /state 查询与对账使用；纯读操作，持 RLock 保证并发安全。可能返回空列表
        （未连接/无持仓——调用方必须按不可信快照处理）。English: returns all positions
        ordered by amount descending, for /state queries and reconciliation.
        """
        with self._lock:
            cur = self._conn.execute(
                "SELECT * FROM real_positions ORDER BY amount DESC"
            )
            return [dict(r) for r in cur.fetchall()]

    def reconcile_positions(self, positions):
        """全量对账：upsert 全部 + 删除不在集合内的持仓。返回新持仓数量。

        注意：空集合的删除语义由调用方（handler.on_positions）守卫——连续空快照才接受清空。
        """
        # 收集本次对账中的全部 ts_code，作为“应保留”集合
        codes = set()
        with self._lock:
            for p in positions:
                codes.add(p["ts_code"])
                # 逐条 upsert 持仓
                self.upsert_position(p)
            placeholders = ",".join("?" * len(codes))
            if placeholders:
                self._conn.execute(
                    "DELETE FROM real_positions WHERE ts_code NOT IN (%s)" % placeholders,
                    tuple(codes),
                )
            else:
                self._conn.execute("DELETE FROM real_positions")
            self._conn.commit()
        return len(codes)

    def close(self):
        """关闭 SQLite 连接（进程退出时调用）。"""
        with self._lock:
            self._conn.close()

    # ── §ROBUST 持久化 outbox（回报队列）──
    # 事件先落库再发送：进程崩溃/重启后由 sender 续发，杜绝内存队列丢回报。
    # fills 表幂等 + 首尔 ApplyRealFill 幂等兜底，重发安全（重复投递被唯一键拦截）。

    def outbox_enqueue(self, payload):
        """回报事件入队，返回行 id。payload 为可 JSON 序列化 dict。"""
        with self._lock:
            cur = self._conn.execute(
                "INSERT INTO outbox(payload, created_at) VALUES(?,?)",
                (json.dumps(payload, ensure_ascii=False, default=json_default),
                 time.strftime("%Y-%m-%dT%H:%M:%S+08:00")))
            self._conn.commit()
            return cur.lastrowid

    def outbox_oldest(self):
        """取最旧一条待发回报。返回 (id, payload) 或 None（队空）。坏行返回 (id, None)。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT id, payload FROM outbox ORDER BY id LIMIT 1").fetchone()
            if row is None:
                return None
            try:
                return row["id"], json.loads(row["payload"])
            except ValueError:
                return row["id"], None

    def outbox_delete(self, oid):
        """发送成功后出队。"""
        with self._lock:
            self._conn.execute("DELETE FROM outbox WHERE id=?", (oid,))
            self._conn.commit()

    def outbox_trim(self, cap):
        """超上限删最旧（首尔长期失联的极端保护）。返回删除条数；positions 每 60s 一条，
        cap=20000 约两周量。English: drops oldest rows past cap; returns deleted count."""
        with self._lock:
            n = self._conn.execute("SELECT COUNT(*) AS c FROM outbox").fetchone()["c"]
            if n <= cap:
                return 0
            self._conn.execute(
                "DELETE FROM outbox WHERE id IN "
                "(SELECT id FROM outbox ORDER BY id LIMIT ?)", (n - cap,))
            self._conn.commit()
            return n - cap

    def outbox_count(self):
        """返回 outbox 当前待发条数（用于 sender 空队列等待判断）。"""
        with self._lock:
            return self._conn.execute("SELECT COUNT(*) AS c FROM outbox").fetchone()["c"]

    # ── §QMT-DUAL 派发队列（QueuedBroker 兜底路径）──
    # 量仔 /order 在此入队，QMT 客户端内置策略桥（qmt_bridge.py）经 /dispatch/pending
    # 取单执行、/dispatch/result 回报；网关把结果按现有 handler 协议推量仔，契约不变。

    def dispatch_enqueue_order(self, req, user_id=""):
        """把一笔待执行单写入派发队列，返回 "seq:<id>" 不透明引用。

        幂等由上层 ids.claim（orders.signal_id UNIQUE）保证——同一 signal_id 重复
        入队只会有一方进入（网关 /order 的 claim 段已在入队前完成互斥）。
        """
        with self._lock:
            cur = self._conn.execute(
                """INSERT INTO dispatch(seq, signal_id, kind, code, side, price_type,
                                        price, qty, strategy, status, created_at, user_id)
                   VALUES(?,?,?,?,?,?,?,?,?, 'pending', ?, ?)""",
                ("", req.get("signal_id", ""), "order", req.get("code", ""),
                 req.get("side", ""), req.get("price_type", ""),
                 float(req.get("price", 0) or 0), int(req.get("qty", 0) or 0),
                 req.get("strategy", ""), req.get("created_at", "") or _now_cn(), user_id))
            rowid = cur.lastrowid
            seq = "seq:%d" % rowid
            self._conn.execute("UPDATE dispatch SET seq=? WHERE id=?", (seq, rowid))
            self._conn.commit()
            return seq

    def dispatch_enqueue_cancel(self, order_ref, exchange_order_id, signal_id="",
                                code="", side="", user_id=""):
        """写入撤单请求，返回 "seq:<id>" 引用。

        :param exchange_order_id: 要撤的交易所委托号（QueuedBroker 已解析，缺省则不可撤）。
        """
        with self._lock:
            cur = self._conn.execute(
                """INSERT INTO dispatch(seq, signal_id, kind, code, side, order_id,
                                        status, created_at, user_id)
                   VALUES(?,?,?,?,?,?, 'pending', ?, ?)""",
                ("", signal_id, "cancel", code, side, exchange_order_id, _now_cn(), user_id))
            rowid = cur.lastrowid
            seq = "seq:%d" % rowid
            self._conn.execute("UPDATE dispatch SET seq=? WHERE id=?", (seq, rowid))
            self._conn.commit()
            return seq

    def dispatch_get(self, seq):
        """按 seq 查派发项（含结果）。不存在返回 None。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM dispatch WHERE seq = ?", (seq,))
            row = cur.fetchone()
            return dict(row) if row else None

    def dispatch_by_order_id(self, exchange_order_id):
        """按交易所委托号反查派发项（桥回报成交归因用）。"""
        with self._lock:
            cur = self._conn.execute(
                "SELECT * FROM dispatch WHERE order_id = ? ORDER BY id DESC LIMIT 1",
                (str(exchange_order_id),))
            row = cur.fetchone()
            return dict(row) if row else None

    def dispatch_pending(self, limit=50):
        """原子取出 pending 派发项并标记 inflight（桥取单，防并发双执行）。返回 dict 列表。"""
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM dispatch WHERE status = 'pending' ORDER BY id LIMIT ?",
                (limit,)).fetchall()
            for r in rows:
                self._conn.execute("UPDATE dispatch SET status = 'inflight' WHERE id = ?", (r["id"],))
            self._conn.commit()
            return [dict(r) for r in rows]

    def dispatch_set_result(self, seq, result):
        """结算派发项：status=done，result 合并写回，交易所委托号回填 order_id 列。"""
        with self._lock:
            row = self._conn.execute("SELECT * FROM dispatch WHERE seq = ?", (seq,)).fetchone()
            if row is None:
                return None
            merged = {}
            if row["result"]:
                try:
                    merged.update(json.loads(row["result"]))
                except ValueError:  # noqa: BLE001 — 脏 result 行直接覆盖
                    pass
            merged.update(result)
            oid = str(merged.get("order_id", "") or row["order_id"] or "")
            self._conn.execute(
                "UPDATE dispatch SET status='done', result=?, order_id=? WHERE id=?",
                (json.dumps(merged, ensure_ascii=False, default=json_default), oid, row["id"]))
            self._conn.commit()
            return dict(row)

    def dispatch_stats(self):
        """派发队列统计（/admin/status 观察用）：pending/inflight/done 计数。"""
        with self._lock:
            rows = self._conn.execute(
                "SELECT status, COUNT(*) AS c FROM dispatch GROUP BY status").fetchall()
            return {r["status"]: r["c"] for r in rows}

    # ── §QMT-DUAL 桥状态（心跳 + 快照）──

    def bridge_heartbeat(self):
        """桥心跳落库（epoch 秒，驱动 QueuedBroker.is_connected 新鲜度判定）。"""
        with self._lock:
            self._bridge_set("last_heartbeat", str(time.time()))

    def bridge_connected(self, timeout_sec):
        """桥是否在线：last_heartbeat 距今 ≤ timeout_sec。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT value FROM bridge_state WHERE key='last_heartbeat'").fetchone()
            if row is None:
                return False
            try:
                ts = float(row["value"])
            except (TypeError, ValueError):  # noqa: BLE001
                return False
            return (time.time() - ts) <= float(timeout_sec or 0)

    def bridge_snapshot_set(self, key, value):
        """写桥快照（如 positions/asset 最新一次）。value 为可 JSON 序列化对象。"""
        with self._lock:
            self._bridge_set(key, json.dumps(value, ensure_ascii=False, default=json_default))

    def bridge_snapshot_get(self, key, default=None):
        """读桥快照；不存在/解析失败返回 default。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT value FROM bridge_state WHERE key=?", (key,)).fetchone()
            if row is None:
                return default
            try:
                return json.loads(row["value"])
            except ValueError:  # noqa: BLE001
                return default

    def _bridge_set(self, key, value):
        """桥状态 upsert（调用方须持 _lock）。"""
        self._conn.execute(
            "INSERT INTO bridge_state(key, value, updated_at) VALUES(?,?,?) "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
            (key, value, _now_cn()))
        self._conn.commit()


def _now_cn():
    """返回当前北京时间 ISO 时间戳（东八区），如 "2026-09-01T09:30:00+08:00"。

    用于订单/成交时间落库的统一口径，保证本机（广州）时区语义一致。
    English: returns the current Beijing-time ISO timestamp for consistent order/fill timestamps.
    """
    return time.strftime("%Y-%m-%dT%H:%M:%S+08:00")


def json_default(o):
    """sqlite3.Row → dict 序列化兜底。"""
    if isinstance(o, sqlite3.Row):
        return dict(o)
    raise TypeError("not JSON serializable")


def to_json(obj):
    """将账本对象序列化为 JSON 字符串（ensure_ascii=False 保留中文，sqlite3.Row 走兜底）。"""
    return json.dumps(obj, ensure_ascii=False, default=json_default)
