#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""backfill_trade_evidence_20261119.py — 用券商交割单回填成交证据（只补日期，不改金额）。

为什么需要它
------------
现网 `live.db.fills` 有一批成交的 `traded_at` 只有 `HH:MM:SS`（§REPORT-STAMP 历史残留）：
2026-11-19 实测 **32 笔**（买 16 / 卖 16）。它们的 `trade_id`（券商成交编号）是齐的，
但日期没了 ⇒ 逐笔复盘无法把它们配进任何一天的回合。

可用的回填证据**只有券商交割单**。本脚本先做过一轮"系统内部能不能自证"，结论是**不能**：
  * `trade_results.trade_id` 与这 32 条的 `trade_id` **零交集**（0/32）；
  * 对应 `orders` 行的 `created_at` 同样是 `HH:MM:SS`（同一个 bug 写进去的），拿不到日期；
  * `fills.id` 不是时间序（208–212 是 2026-11-19、175–207 无日期，id 大小与时间不一致）
    ⇒ 用"夹在两个已知日期之间的 id"推日期是**猜**，本脚本不做。
所以：**缺的日期只能从交割单来**，本脚本负责把交割单安全地贴回去。

两条硬纪律
----------
1. **只写 `traded_at`**：价格、数量、金额、手续费一个字都不动。交割单与库里有金额差异时，
   本脚本只报告不修正——改金额是记账纠错，不是补证据，两件事必须分开走。
2. **匹配必须唯一**：先按 `trade_id`（券商成交编号）精确配对；配不上才退到
   `(代码, 方向, 数量, 价格±0.011, 金额±1.0)` 的**唯一**候选。任何"0 个候选"或"多个候选"
   一律不动，计入报告——宁可少补一条，也不许把 A 笔的日期贴到 B 笔上。

用法
----
    # ① 出缺口清单（只读，不需要交割单）
    sudo -n -u quant python3 backfill_trade_evidence_20261119.py \
        --inventory --out-dir /tmp/evidence-20261119

    # ② 拿交割单做演练（默认 dry-run，一个字节都不写）
    ... --apply --file /tmp/jgdd.csv --db /var/lib/quant-trading-v2/live.db

    # ③ 确认演练输出无误后才真写（写前自动备份 live.db）
    ... --apply --file /tmp/jgdd.csv --db /var/lib/quant-trading-v2/live.db --commit

交割单 CSV 的列名认这些别名（大小写与空格无关，缺列不算错，只是少一条匹配途径）：
    成交日期 | 日期 | 发生日期 | 成交时间
    证券代码 | 股票代码 | 代码
    买卖标志 | 操作 | 摘要 | 委托方向          （值含「买」/「卖」即可）
    成交数量 | 成交股数 | 数量
    成交均价 | 成交价格 | 价格
    成交金额 | 发生金额 | 金额
    成交编号 | 合同编号 | 成交序号 | trade_id
    （手续费/印花税/佣金/印花税只读来做差异报告，不写回）

退出码
------
    0 = 正常（dry-run 或 commit 成功）
    2 = 参数/输入问题（文件不存在、列认不出、日期非法）
    3 = 有候选但存在歧义（不清空、不写；看报告里的 ambiguous 清单）
"""

import argparse
import csv
import os
import re
import shutil
import sqlite3
import sys
import time
from collections import defaultdict

_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")

# 交割单列名别名（券商导出的表头各家不同，按语义归并；不做模糊匹配以免认错列）
_COL_ALIASES = {
    "date": ("成交日期", "日期", "发生日期", "成交时间", "交易日期"),
    "code": ("证券代码", "股票代码", "代码", "证券编码"),
    "side": ("买卖标志", "操作", "摘要", "委托方向", "业务名称"),
    "qty": ("成交数量", "成交股数", "数量", "成交笔数数量"),
    "price": ("成交均价", "成交价格", "成交价", "价格"),
    "amount": ("成交金额", "发生金额", "金额", "成交额"),
    "trade_id": ("成交编号", "合同编号", "成交序号", "trade_id"),
    "fee": ("手续费", "佣金", "净佣金"),
    "stamp": ("印花税",),
}


def ro_connect(path):
    """只读打开（清单与演练都走这里；真写只在 commit 分支另开连接）。"""
    if not os.path.isfile(path):
        sys.exit("DB_MISSING %s" % path)
    conn = sqlite3.connect("file:%s?mode=ro" % path, uri=True)
    conn.execute("PRAGMA query_only=1")
    return conn


def norm_header(name):
    """表头归一：去空白、去 BOM、全角括号转半角（只做字符级，不做模糊匹配）。"""
    s = (name or "").replace("\ufeff", "").strip()
    return s.replace("（", "(").replace("）", ")")


def pick_columns(header):
    """把表头映射成 {语义: 列序号}；同一个语义出现多列时取第一个。"""
    idx = {}
    norm = [norm_header(h) for h in header]
    for key, aliases in _COL_ALIASES.items():
        for i, h in enumerate(norm):
            if h in aliases:
                idx[key] = i
                break
    return idx


def to_float(v, default=0.0):
    try:
        s = str(v).replace(",", "").replace("¥", "").strip()
        if s in ("", "--", "—", "None", "null"):
            return default
        return float(s)
    except (TypeError, ValueError):
        return default


def seg6(code):
    return str(code or "").split(".")[0].split(" ")[0].strip()


def side_of(raw):
    """把交割单里的方向词归一到 买入/卖出；认不出返回空串（不猜）。"""
    s = str(raw or "")
    if "买" in s:
        return "买入"
    if "卖" in s:
        return "卖出"
    return ""


def load_undated(conn):
    """所有缺日期的成交（本脚本只碰这些行；有日期的行不会被改写）。"""
    cur = conn.cursor()
    rows = cur.execute(
        """SELECT id, code, side, qty, price, amount, traded_at, trade_id, order_id, user_id
             FROM fills
            WHERE length(COALESCE(traded_at,'')) < 10
            ORDER BY id"""
    ).fetchall()
    cols = [d[0] for d in cur.description]
    return [dict(zip(cols, r)) for r in rows]


def load_dated_ids(conn):
    """已带日期的 trade_id 集合：用来识别"这条交割单已经补过了"（幂等）。"""
    cur = conn.cursor()
    return {
        str(r[0])
        for r in cur.execute(
            "SELECT trade_id FROM fills WHERE length(COALESCE(traded_at,'')) >= 10"
        )
        if r[0]
    }


def read_statement(path):
    """读交割单 CSV：输出 [{date, code, side, qty, price, amount, trade_id, fee, stamp}]。

    编码按 utf-8-sig → gbk 顺序试（券商导出在 Windows 上常是 GBK；
    按 GBK 硬读 UTF-8 文件会把中文列名读成乱码，列就再也认不出来）。
    """
    if not os.path.isfile(path):
        sys.exit("STATEMENT_MISSING %s" % path)
    rows = None
    last_err = None
    for enc in ("utf-8-sig", "gbk", "utf-8"):
        try:
            with open(path, newline="", encoding=enc) as fh:
                rd = csv.reader(fh)
                header = next(rd)
                idx = pick_columns(header)
                if not idx.get("date") and not idx.get("trade_id"):
                    continue
                out = []
                for raw in rd:
                    if not any((c or "").strip() for c in raw):
                        continue

                    def cell(k):
                        i = idx.get(k)
                        return raw[i] if i is not None and i < len(raw) else ""

                    out.append(
                        {
                            "date": norm_header(cell("date"))[:10].replace("/", "-"),
                            "code": seg6(cell("code")),
                            "side": side_of(cell("side")),
                            "qty": int(to_float(cell("qty"))),
                            "price": to_float(cell("price")),
                            "amount": to_float(cell("amount")),
                            "trade_id": str(cell("trade_id")).strip(),
                            "fee": to_float(cell("fee")),
                            "stamp": to_float(cell("stamp")),
                        }
                    )
                rows = out
                break
        except (UnicodeDecodeError, StopIteration) as exc:
            last_err = exc
            continue
    if rows is None:
        sys.exit("STATEMENT_UNREADABLE %s (%s)" % (path, last_err))
    return rows


def build_plan(undated, statement, already_dated):
    """为每条缺日期成交找出唯一证据；返回 (plan, unmatched, ambiguous, idempotent, conflicts)。

    plan: [{fill_id, code, side, qty, date, basis, old_at, new_at, td, td_row}]
    """
    by_tid = {}
    for row in undated:
        tid = str(row.get("trade_id") or "").strip()
        if tid:
            by_tid.setdefault(tid, []).append(row)

    # 交割单内部也可能重复同一成交编号（拆笔/汇总导出），先分桶再判唯一
    st_by_tid = defaultdict(list)
    for row in statement:
        if row["trade_id"]:
            st_by_tid[row["trade_id"]].append(row)

    plan, unmatched, ambiguous, idempotent, conflicts = [], [], [], [], []
    used_statement = set()

    for row in undated:
        tid = str(row.get("trade_id") or "").strip()
        cand = None
        basis = ""

        # ① 成交编号（最硬：券商成交编号与库里的 trade_id 同源）
        if tid and len(st_by_tid.get(tid) or []) == 1:
            cand = st_by_tid[tid][0]
            basis = "trade_id"
        elif tid and len(st_by_tid.get(tid) or []) > 1:
            ambiguous.append((row, "trade_id 在交割单里出现 %d 次" % len(st_by_tid[tid])))
            continue
        elif tid and tid in already_dated:
            idempotent.append((row, "同 trade_id 已有带日期的行"))
            continue

        # ② (代码, 方向, 数量, 价格, 金额) 唯一候选
        if cand is None:
            hits = [
                s
                for s in statement
                if seg6(s["code"]) == seg6(row["code"])
                and (not s["side"] or not row["side"] or s["side"] == row["side"])
                and (not s["qty"] or s["qty"] == int(row["qty"] or 0))
                and (not s["price"] or abs(s["price"] - to_float(row["price"])) <= 0.011)
                and (not s["amount"] or abs(s["amount"] - to_float(row["amount"])) <= 1.0)
            ]
            if len(hits) == 1:
                cand = hits[0]
                basis = "fingerprint"
            elif len(hits) > 1:
                ambiguous.append((row, "指纹命中 %d 条（%s）" % (len(hits), "/".join(
                    "%s %s %s@%s" % (h["date"], h["side"] or "?", h["qty"], h["price"]) for h in hits
                ))))
                continue
            else:
                unmatched.append((row, "交割单里没有能对上的行"))
                continue

        if not _DATE_RE.match(cand["date"] or ""):
            unmatched.append((row, "交割单该行日期非法：%r" % (cand["date"],)))
            continue
        if cand["trade_id"] and tid and cand["trade_id"] != tid:
            # 编号对得上却指向另一条：留痕后不动（宁可不动）
            conflicts.append((row, cand, "trade_id 与匹配到的交割单行不一致"))
            continue

        old_at = str(row["traded_at"] or "")
        tail = old_at[10:] if len(old_at) > 10 else ""
        time_part = ""
        if tail:
            m = re.match(r"^[ T](\d{2}:\d{2}:\d{2})", tail)
            if m:
                time_part = " " + m.group(1)
        elif re.match(r"^\d{2}:\d{2}:\d{2}$", old_at):
            time_part = " " + old_at
        new_at = cand["date"] + time_part

        plan.append(
            {
                "fill_id": row["id"],
                "code": seg6(row["code"]),
                "side": row["side"],
                "qty": int(row["qty"] or 0),
                "date": cand["date"],
                "basis": basis,
                "old_at": old_at,
                "new_at": new_at,
                "td": cand["trade_id"],
                "td_row": cand,
            }
        )
        used_statement.add(id(cand))

    return plan, unmatched, ambiguous, idempotent, conflicts, used_statement


def inventory(conn, out_dir):
    """出缺口清单（只读）：缺日期成交逐笔 + 两侧金额不对称 + 卖出侧缺口估算。"""
    os.makedirs(out_dir, exist_ok=True)
    undated = load_undated(conn)
    cur = conn.cursor()

    buys_amt = cur.execute(
        "SELECT COALESCE(SUM(amount),0) FROM fills WHERE side='买入' AND length(COALESCE(traded_at,''))>=10"
    ).fetchone()[0] or 0.0
    sells_amt = cur.execute(
        "SELECT COALESCE(SUM(amount),0) FROM fills WHERE side='卖出' AND length(COALESCE(traded_at,''))>=10"
    ).fetchone()[0] or 0.0
    # 持仓只取**有成交记录**的账号：现网有一户只有手工改账持仓、零 fills，
    # 把它并进来会把"卖出侧缺口"这个读数直接算错（口径与复盘脚本保持一致）。
    fill_users = sorted({str(r[0]) for r in cur.execute("SELECT DISTINCT user_id FROM fills") if r[0]})
    pos_amt = 0.0
    for u in fill_users:
        pos_amt += cur.execute(
            "SELECT COALESCE(SUM(amount),0) FROM real_positions WHERE qty>0 AND user_id=?", (u,)
        ).fetchone()[0] or 0.0
    und_buy = sum(1 for r in undated if r["side"] == "买入")
    und_sell = sum(1 for r in undated if r["side"] == "卖出")
    und_buy_amt = sum(to_float(r["amount"]) for r in undated if r["side"] == "买入")
    und_sell_amt = sum(to_float(r["amount"]) for r in undated if r["side"] == "卖出")

    # 系统内部可自证的比例（trade_results 有 trade_id 且有日期）
    tr = {
        str(r[0]): (r[1] or "")
        for r in cur.execute("SELECT trade_id, day FROM trade_results")
        if r[0]
    }
    self_ok = sum(1 for r in undated if len(str(tr.get(str(r["trade_id"] or ""), ""))) >= 10)

    p_csv = os.path.join(out_dir, "evidence_gap.csv")
    with open(p_csv, "w", newline="", encoding="utf-8-sig") as fh:
        w = csv.writer(fh)
        w.writerow(
            ["fill_id", "成交编号(trade_id)", "order_id", "代码", "方向", "数量",
             "成交价", "成交金额", "现有时间", "账号", "备注"]
        )
        for r in undated:
            w.writerow(
                [r["id"], r["trade_id"], r["order_id"], r["code"], r["side"], r["qty"],
                 r["price"], r["amount"], r["traded_at"], r["user_id"],
                 "缺日期：只有 HH:MM:SS；按成交编号去交割单取日期"]
            )

    p_md = os.path.join(out_dir, "EVIDENCE_GAP.md")
    lines = []
    A = lines.append
    A("# 成交证据缺口清单（只读产出）")
    A("")
    A("## 1. 缺日期的成交")
    A("")
    A("| 方向 | 笔数 | 金额(元) |")
    A("|---|---|---|")
    A("| 买入 | %d | %.2f |" % (und_buy, und_buy_amt))
    A("| 卖出 | %d | %.2f |" % (und_sell, und_sell_amt))
    A("| 合计 | %d | %.2f |" % (len(undated), und_buy_amt + und_sell_amt))
    A("")
    A("## 2. 系统内部能不能自己补（实测）")
    A("")
    A("- `trade_results.trade_id` 与这些成交的成交编号重叠：**%d / %d**" % (self_ok, len(undated)))
    A("- 对应 `orders.created_at` 同样是 `HH:MM:SS`（同一个 §REPORT-STAMP 缺陷写进去的）")
    A("- `fills.id` 不是时间序（实测 208–212 是 2026-11-19、175–207 无日期）⇒ 用 id 夹逼推日期是猜，不做")
    A("")
    A("**结论：缺的日期只能从券商交割单来。** 交割单按「成交编号」逐行给日期即可，")
    A("本目录的 `evidence_gap.csv` 就是可以直接拿去对照的清单。")
    A("")
    A("## 3. 已带日期成交的两侧不对称（说明卖出侧缺口）")
    A("")
    A("| 读数 | 值 |")
    A("|---|---|")
    A("| 已带日期 买入金额 | %.2f |" % buys_amt)
    A("| 已带日期 卖出金额 | %.2f |" % sells_amt)
    A("| 缺日期 买入 / 卖出金额 | %.2f / %.2f |" % (und_buy_amt, und_sell_amt))
    A("| 期末真持仓金额（只算有 fills 的账号） | %.2f |" % pos_amt)
    A("| **缺口 = 全部买入 − 全部卖出 − 期末持仓** | **%.2f** |"
      % (buys_amt + und_buy_amt - sells_amt - und_sell_amt - pos_amt))
    A("")
    A("最后一行不是「赚的钱」，是**卖出成交记录缺口的量级**（上界估计：按持仓成本计、")
    A("不扣已实现盈亏，所以真实缺口只会比它小）：买入的钱要么还在持仓里、要么已经卖出——")
    A("卖出了却查不到记录，就是证据缺口。")
    A("")
    A("## 4. 下一步")
    A("")
    A("```bash")
    A("# 演练（不写库）")
    A("python3 backfill_trade_evidence_20261119.py --apply --file 交割单.csv \\")
    A("    --db /var/lib/quant-trading-v2/live.db")
    A("# 确认后真写（自动备份 live.db）")
    A("... --commit")
    A("```")
    A("")
    with open(p_md, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines) + "\n")

    print(
        "EVIDENCE_INVENTORY undated=%d(买 %d/卖 %d) amounts=%.2f self_provable=%d "
        "buy_amt=%.2f sell_amt=%.2f pos_amt=%.2f out=%s"
        % (len(undated), und_buy, und_sell, und_buy_amt + und_sell_amt, self_ok,
           buys_amt, sells_amt, pos_amt, out_dir)
    )
    return 0


def apply_plan(db_path, plan, commit):
    """执行回填：只 UPDATE traded_at；commit=False 时只打印计划。"""
    if not plan:
        print("PLAN_EMPTY 没有可回填的行")
        return 0

    print("PLAN rows=%d commit=%s" % (len(plan), "yes" if commit else "no(dry-run)"))
    for p in plan:
        print(
            "  fill=%s %s %s %s股 %s  %s -> %s  basis=%s tid=%s"
            % (p["fill_id"], p["code"], p["side"], p["qty"], p["date"],
               p["old_at"], p["new_at"], p["basis"], p["td"] or "-")
        )
    if not commit:
        print("DRY_RUN 未写库（加 --commit 才落盘）")
        return 0

    bak = "%s.bak-evidence-%s" % (db_path, time.strftime("%Y%m%d%H%M%S"))
    src = sqlite3.connect(db_path)
    dst = sqlite3.connect(bak)
    with dst:
        src.backup(dst)
    dst.close()
    src.close()
    print("BACKUP %s" % bak)

    conn = sqlite3.connect(db_path)
    conn.execute("PRAGMA query_only=0")
    updated = 0
    try:
        with conn:
            for p in plan:
                cur = conn.execute(
                    "UPDATE fills SET traded_at=? WHERE id=? AND length(COALESCE(traded_at,''))<10",
                    (p["new_at"], p["fill_id"]),
                )
                updated += cur.rowcount
    finally:
        left = conn.execute(
            "SELECT COUNT(*) FROM fills WHERE length(COALESCE(traded_at,''))<10"
        ).fetchone()[0]
        conn.close()
    print("APPLIED updated=%d remaining_undated=%d backup=%s" % (updated, left, bak))
    if updated != len(plan):
        print("WARN 计划 %d 条、实改 %d 条（其余行可能已被别的进程补过）" % (len(plan), updated))
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default="/var/lib/quant-trading-v2/live.db")
    ap.add_argument("--out-dir", default="/tmp/evidence-20261119")
    ap.add_argument("--file", default="", help="券商交割单 CSV")
    ap.add_argument("--inventory", action="store_true", help="只出缺口清单")
    ap.add_argument("--apply", action="store_true", help="拿交割单做匹配（默认 dry-run）")
    ap.add_argument("--commit", action="store_true", help="真写库（需同时给 --apply）")
    args = ap.parse_args()

    if not args.inventory and not args.apply:
        ap.error("要么 --inventory，要么 --apply --file <交割单.csv>")
    if args.commit and not args.apply:
        ap.error("--commit 必须与 --apply 同用")

    conn = ro_connect(args.db)
    if args.inventory:
        return inventory(conn, args.out_dir)

    undated = load_undated(conn)
    already = load_dated_ids(conn)
    statement = read_statement(args.file)
    conn.close()

    plan, unmatched, ambiguous, idem, conflicts, _used = build_plan(undated, statement, already)

    print(
        "MATCH statement=%d undated=%d plan=%d unmatched=%d ambiguous=%d "
        "idempotent=%d conflicts=%d"
        % (len(statement), len(undated), len(plan), len(unmatched), len(ambiguous),
           len(idem), len(conflicts))
    )
    for row, why in unmatched:
        print("  UNMATCHED fill=%s %s %s %s股 @%s : %s"
              % (row["id"], row["code"], row["side"], row["qty"], row["price"], why))
    for row, why in ambiguous:
        print("  AMBIGUOUS fill=%s %s %s %s股 @%s : %s"
              % (row["id"], row["code"], row["side"], row["qty"], row["price"], why))
    for row, cand, why in conflicts:
        print("  CONFLICT fill=%s : %s" % (row["id"], why))
    for row, why in idem:
        print("  SKIP fill=%s : %s" % (row["id"], why))

    rc = apply_plan(args.db, plan, args.commit)
    if ambiguous or conflicts:
        print("NOTE 存在歧义/冲突行，已按 fail-closed 不动；先把它们查清再重跑")
        return 3
    return rc


if __name__ == "__main__":
    sys.exit(main())
