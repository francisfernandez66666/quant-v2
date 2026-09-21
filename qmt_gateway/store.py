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
                trade_id      TEXT DEFAULT '', -- §G1（2026-08-29）：唯一成交编号，部成去重
                fee           REAL DEFAULT 0,  -- §UAT-FIX 20260918：经手费/佣金（尽力透传，缺=0）
                stamp_tax     REAL DEFAULT 0   -- 印花税（卖方单边；缺=0）
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
                inflight_at TEXT DEFAULT '',      -- §M16 取单转 inflight 时刻（超时收割龄判据）
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
            -- §P1-8（2026-09-15）回报死信：被首尔侧永久拒绝（4xx，除 401/403/408/425/429）的
            -- outbox 消息不再无限重试卡队首——落死信表留痕供人工排查，队列继续消费。
            CREATE TABLE IF NOT EXISTS outbox_dead (
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                payload     TEXT,
                reason      TEXT,
                created_at  TEXT DEFAULT ''
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
            if t == "fills" and "fee" not in cols:
                # §UAT-FIX 20260918（P2-FEE）：成交费用腿——此前 fills 无费用列，
                # 三方对账 fee_diff 恒 0。回报尽力携带则入库，缺省 0 与旧行为一致。
                self._conn.execute("ALTER TABLE fills ADD COLUMN fee REAL DEFAULT 0")
            if t == "fills" and "stamp_tax" not in cols:
                self._conn.execute("ALTER TABLE fills ADD COLUMN stamp_tax REAL DEFAULT 0")
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
                inflight_at TEXT DEFAULT '',      -- §M16 取单转 inflight 时刻（超时收割龄判据）
                user_id     TEXT DEFAULT ''
            );
            CREATE INDEX IF NOT EXISTS idx_dispatch_status ON dispatch(status);
            CREATE INDEX IF NOT EXISTS idx_dispatch_signal ON dispatch(signal_id);
            CREATE TABLE IF NOT EXISTS bridge_state (
                key         TEXT PRIMARY KEY,
                value       TEXT,
                updated_at  TEXT
            );
            -- §P1-8（2026-09-15）：老库同样补建死信表（永久 4xx 回报留痕，不卡队首）
            CREATE TABLE IF NOT EXISTS outbox_dead (
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                payload     TEXT,
                reason      TEXT,
                created_at  TEXT DEFAULT ''
            );
            """
        )
        # §M16（2026-09-22）：既有 dispatch 表（列建于更早版本）补 inflight_at 列——
        # inflight 超时收割的龄判据；SQLite ALTER 无 IF NOT EXISTS，先查 PRAGMA 再按需加。
        dcols = [r[1] for r in self._conn.execute("PRAGMA table_info(dispatch)").fetchall()]
        if dcols and "inflight_at" not in dcols:
            self._conn.execute("ALTER TABLE dispatch ADD COLUMN inflight_at TEXT DEFAULT ''")
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
                    # 占位行约定：order_id 前缀 "pending:" 标记未报单状态，
                    # 券商回执落地时由 upsert 覆盖为真实委托号。
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
                    # 实参对齐列序 order_id,signal_id,code,side,status,price,qty,
                    # created_at,user_id：拿不到真实委托号时先写 "pending:<sid>" 占位，
                    # 后续事件按 order_id_rank 逐级升级（占位 → seq → 交易所号）
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
                # 实参对齐 SET 顺序：order_id=按等级挑定的最终号，status/price/created_at/
                # user_id 缺省时回退存量值，qty 为 0 时保留存量（通道未报量≠清零）
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
        # 时间戳阈值（Unix 秒）：max_age_sec<=0 表示不限制，阈值取 0 → 一条都不删，
        # 留给下面的 DELETE 语句统一判断，避免两个分支各写一遍清理逻辑
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
                        # 首笔建仓：成本价与最高价都用本笔成交价（没有历史价可比），
                        # amount 取回报给的成交额，user_id 决定这笔挂在哪个租户下
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
                        # 实参对齐 SET 子句：qty=加仓后总量，cost_price=按数量加权的
                        # 新成本，amount 用最新成交额，highest_price 单调不回退
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
            # §P2-FEE 20260918：费用腿随成交同笔入账（回报缺费用字段时落 0，兼容旧通道）
            self._conn.execute(
                """INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id, trade_id, fee, stamp_tax)
                    VALUES(?,?,?,?,?,?,?,?,?,?,?,?)""",
                # 成交流水实参：side 用归一化后的 fill_side（"买入"/"卖出"，与量仔口径
                # 一致而非通道原值），trade_id 缺失落空串（旧通道无此字段，不参与判重）
                (f.get("order_id", ""), f["code"], fill_side, f["price"], f["qty"],
                 f.get("amount", 0.0), f.get("traded_at", ""), f.get("signal_id", ""), f.get("user_id", ""),
                 str(f.get("trade_id", "") or ""),
                 float(f.get("fee") or 0.0), float(f.get("stamp_tax") or 0.0)),
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

    def settlement_trades(self, day):
        """§P0-1a（2026-09-15）按交易日查当日全部成交，供 GET /settlement 三方对账装配。

        day 为北京时 `YYYY-MM-DD`（fills.traded_at 落库口径 `_now_cn()` 即该前缀）。
        返回 [{ts_code,side,price,qty,amount,order_id,traded_at,serial,signal_id,fee,stamp_tax}]；
        serial 用 §G1 唯一成交编号 trade_id（真实网关的成交流水号，此前从不产出 serial
        导致 Go 侧对账关联键塌缩——见 docs/UAT_20260915_FINDINGS.md P0-1）。
        §UAT-FIX 20260918（P2-FEE）：fee/stamp_tax 自 fills 列尽力输出（回报通道未带费用时为 0，
        与旧"费用腿恒 0"口径一致，不构成回归）。
        English: §P0-1a — lists the day's fills for the settlement reconciliation endpoint,
        exposing trade_id as the broker serial and the best-effort fee/stamp_tax columns.
        """
        # 锁内一次性查当日全量：对账端点调用频度低（盘后），一致性优先于并发吞吐。
        # COALESCE 兜底旧格式回报落库时 fee/stamp_tax 为 NULL 的行。
        with self._lock:
            cur = self._conn.execute(
                "SELECT order_id, code, side, price, qty, amount, traded_at, signal_id, trade_id, "
                "COALESCE(fee,0) AS fee, COALESCE(stamp_tax,0) AS stamp_tax "
                "FROM fills WHERE substr(traded_at,1,10) = ? ORDER BY traded_at, order_id",
                (day,),
            )
            out = []
            for r in cur.fetchall():
                out.append({
                    "ts_code": r["code"], "side": r["side"], "price": r["price"],
                    "qty": r["qty"], "amount": r["amount"],
                    "order_id": r["order_id"] or "", "traded_at": r["traded_at"] or "",
                    "serial": r["trade_id"] or "", "signal_id": r["signal_id"] or "",
                    "fee": r["fee"] or 0.0, "stamp_tax": r["stamp_tax"] or 0.0,
                })
            return out

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
                 _now_cn()))  # §TZ 统一显式北京时区，不再裸 strftime 贴假 +08:00
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

    def outbox_dead_enqueue(self, payload, reason):
        """§P1-8（2026-09-15）死信留痕：被首尔永久拒绝（4xx）的回报移入 outbox_dead。

        毒丸防护：首尔 /api/qmt/report 对非法方向/未知 type 返回 400（永久性拒绝），
        旧实现一律无限重试 → 单条毒丸永久卡死 FIFO 队首，阻塞后续全部回报。
        现按状态码分流：永久 4xx 落死信表（payload + reason 留痕供人工排查）继续消费。
        English: §P1-8 — permanently-rejected (4xx) reports are moved to the dead-letter table
        with the failure reason instead of blocking the FIFO head forever.
        """
        with self._lock:
            self._conn.execute(
                "INSERT INTO outbox_dead(payload, reason, created_at) VALUES(?,?,?)",
                (json.dumps(payload, ensure_ascii=False, default=json_default), reason,
                 _now_cn()))  # §TZ 统一显式北京时区
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
                # 实参顺序与上面列名一一对应：seq 先落空串（拿到 rowid 后回填
                # "seq:<id>"），status 固定 'pending' 由 SQL 常量给出，不进参数
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

    def dispatch_enqueue_diag(self, signal_id="", user_id=""):
        """运维诊断项：让桥对交易明细表做一次原始 dump（无 code/qty 语义）。"""
        with self._lock:
            cur = self._conn.execute(
                """INSERT INTO dispatch(seq, signal_id, kind, status, created_at, user_id)
                   VALUES(?,?,?,?, ?, ?)""",
                ("", str(signal_id or ("DIAG-%d" % int(time.time()))), "diag",
                 "pending", _now_cn(), user_id))
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

    def dispatch_by_signal_id(self, signal_id):
        """按 signal_id 反查派发项（§P0 2026-09-18 方向权威化用）。

        为什么需要它：成交回报里 order_id 未必是交易所委托号——文件桥/xt 路径在 remark
        （即 signal_id）非空时会把 order_id 直接填成 remark，此时按委托号反查必然落空；
        signal_id 是本端下单时自己写入派发项的键，命中即说明「这笔成交是本端派的单」，
        其方向以派发项为准（零推断）。取最新一条（同 signal_id 因重试轮换占位单号可能多行）。
        English: §P0 — lookup by signal_id, needed because the fill's order_id field may carry
        the remark (signal_id) rather than the exchange order id, which makes the by-order-id
        lookup miss and silently voids the authoritative-side override.
        """
        if not signal_id:
            return None
        with self._lock:
            cur = self._conn.execute(
                "SELECT * FROM dispatch WHERE signal_id = ? ORDER BY id DESC LIMIT 1",
                (str(signal_id),))
            row = cur.fetchone()
            return dict(row) if row else None

    def dispatch_pending(self, limit=50):
        """原子取出 pending 派发项并标记 inflight（桥取单，防并发双执行）。返回 dict 列表。

        §M16：转 inflight 同时落 inflight_at 取单时刻——收割龄以「被取走的时间」计，
        而非入队时间（在 pending 排到阈值附近才被取走的单不该立刻被收割）。
        """
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM dispatch WHERE status = 'pending' ORDER BY id LIMIT ?",
                (limit,)).fetchall()
            now = _now_cn()
            for r in rows:
                self._conn.execute(
                    "UPDATE dispatch SET status = 'inflight', inflight_at = ? WHERE id = ?",
                    (now, r["id"]))
            self._conn.commit()
            return [dict(r) for r in rows]

    def dispatch_inflight(self, limit=50):
        """inflight（已派发未结算）派发项快照（不改状态）。§2026-09-14 演练③：
        file 桥空推时把 cmd 文件同步为「仅 inflight」，done 残留即清，
        杜绝桥重启重放老单（重复下单），同时保留桥掉线期间未消费单的续跑能力。
        English: read-only inflight snapshot for cmd-file reconciliation.
        """
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM dispatch WHERE status = 'inflight' ORDER BY id LIMIT ?",
                (limit,)).fetchall()
            return [dict(r) for r in rows]

    def dispatch_reap_stale_inflight(self, max_age_sec, reason="inflight 超时未回报，网关收割"):
        """§M16（2026-09-22）：收割超龄 inflight 派发项——inflight 永挂的兜底闭环。

        背景：/dispatch/pending 取单即置 inflight，此后完全依赖桥回报结算。HTTP 桥
        不识别 diag kind、unknown kind 只 warn 不回执、桥崩溃/重启丢回执——该行就永久
        停在 inflight：dispatch_stats 只增不减，orders 侧「已报」无人推进（与 §R4-1
        撤单闭环对真实委托的覆盖不对称）。此处把「取单时刻（inflight_at，老行回退
        created_at）超过 max_age_sec 仍未结算」的行统一判废落 done：
          - 不回 pending：桥可能已真实下单，重新排队 = 重复真实下单（策略桥 :989 同论），
            安全侧判废留痕，由人工/对账兜底；
          - result 落 {ok:false, err:reason, reaped:true}，与桥负回执同构，
            orders 回写「已废」由网关侧（gateway._reap_dispatch_inflight）完成。
        max_age_sec<=0 视为不收割（阈值置 0，一条不删）。返回被收割行的结算前快照列表。
        English: §M16 — reaps dispatch rows stuck in inflight past the timeout and settles
        them as rejected (never re-queued, since the bridge may already have ordered).
        """
        import time as _time  # noqa: PLC0415
        threshold = (_time.time() - max_age_sec) if max_age_sec > 0 else 0
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM dispatch WHERE status = 'inflight' "
                "AND COALESCE(NULLIF(inflight_at, ''), created_at) <> '' "
                "AND CAST(strftime('%s', COALESCE(NULLIF(inflight_at, ''), created_at)) AS INTEGER) < ?",
                (threshold,),
            ).fetchall()
            if not rows:
                return []
            for r in rows:
                merged = {}
                if r["result"]:
                    try:
                        merged.update(json.loads(r["result"]))
                    except ValueError:  # noqa: BLE001 — 脏 result 行直接覆盖
                        pass
                merged.update({"ok": False, "err": reason, "reaped": True})
                self._conn.execute(
                    "UPDATE dispatch SET status='done', result=? WHERE id=?",
                    (json.dumps(merged, ensure_ascii=False, default=json_default), r["id"]))
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

    §TZ（2026-09-22 修复批，LOW「strftime 假 +08:00」根治）：旧实现用**本机钟面**的
    strftime 直接拼出带 +08:00 后缀的字符串 —— 展开的是本地墙钟却硬贴东八区标签，
    部署到非北京时区机器（如首尔 UAT/迁移机）即产出偏移造假的时间串。
    现改为显式北京时区（CN_TZ=UTC+8 定区，不依赖本机 TZ 与 tzdata）取值后再格式化，
    钟面与标签恒一致；本函数是全网关落库时间串的唯一收敛点（§M16 风格的延续）。
    """
    return datetime.now(CN_TZ).strftime("%Y-%m-%dT%H:%M:%S+08:00")


def json_default(o):
    """sqlite3.Row → dict 序列化兜底。"""
    if isinstance(o, sqlite3.Row):
        return dict(o)
    raise TypeError("not JSON serializable")


def to_json(obj):
    """将账本对象序列化为 JSON 字符串（ensure_ascii=False 保留中文，sqlite3.Row 走兜底）。"""
    return json.dumps(obj, ensure_ascii=False, default=json_default)
