#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§F2（2026-09-22 修复批）一次性清理脚本：删除 real_positions 表中 ts_code='' 的历史脏行。

背景（docs/FIX_PLAN_20260922.md §6.2）：positions 对账直落 store 层，无字段校验时期
（本次修复前）可能已有 ts_code='' 的垃圾行入库。这种行会让「本地有仓 + 空快照 → 409」
守卫永久误触发，真实全平再也无法经对账通道落账。store 入口校验已拦住新增脏行，
本脚本负责清掉存量。

用法：
    python3 scripts/clean_empty_position_rows.py --db /path/to/live.db            # dry-run（默认，只报告不删除）
    python3 scripts/clean_empty_position_rows.py --db /path/to/live.db --apply    # 真正删除

安全设计：
  - 默认 dry-run，必须显式 --apply 才写入；
  - 只删 ts_code 为空串/NULL/纯空白 的行——非法但非空（如 '600519'）的行只列出供人工研判，
    绝不自动删除（可能是历史遗留有效数据的不同编码形态）；
  - 删除前逐行打印将被删的内容（ts_code/qty/user_id/updated_at），删除后复核计数；
  - 全程单事务，中途异常回滚。
English: one-off cleaner for legacy empty-ts_code rows in real_positions (default dry-run,
only deletes empty/blank ts_code, lists other malformed rows for manual review).
"""
import argparse
import re
import sqlite3
import sys

VALID_RE = re.compile(r"^[0-9]{6}\.(SH|SZ|BJ)$")


def main():
    ap = argparse.ArgumentParser(description="清理 real_positions 中 ts_code='' 脏行（§F2）")
    ap.add_argument("--db", required=True, help="live.db 路径（实盘账本库）")
    ap.add_argument("--apply", action="store_true", help="真正执行删除（默认 dry-run 只报告）")
    args = ap.parse_args()

    conn = sqlite3.connect(args.db)
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()
    try:
        cur.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='real_positions'")
        if cur.fetchone() is None:
            print("[abort] 库中无 real_positions 表，确认这是实盘账本库（live.db）？")
            return 2

        cur.execute("""
            SELECT rowid, ts_code, name, qty, cost_price, updated_at, user_id
            FROM real_positions
            WHERE ts_code IS NULL OR TRIM(ts_code) = ''
            ORDER BY user_id, rowid
        """)
        empty_rows = cur.fetchall()
        # 非空但格式非法的行：只报告，不自动删（留人工研判）
        cur.execute("SELECT ts_code, COUNT(*) AS n FROM real_positions GROUP BY ts_code")
        odd = [r["ts_code"] for r in cur.fetchall()
               if r["ts_code"] and not VALID_RE.match(str(r["ts_code"]).strip())]

        mode = "APPLY" if args.apply else "DRY-RUN"
        print(f"[{mode}] 库={args.db}")
        print(f"[{mode}] ts_code 为空的脏行 {len(empty_rows)} 行：")
        for r in empty_rows:
            print(f"  rowid={r['rowid']} ts_code={r['ts_code']!r} name={r['name']!r} "
                  f"qty={r['qty']} cost={r['cost_price']} user={r['user_id']!r} updated_at={r['updated_at']!r}")
        if odd:
            print(f"[warn] 另有 {len(odd)} 种非空但格式非法的 ts_code（不在本脚本清理范围，请人工核对）：{odd[:10]}")

        if not empty_rows:
            print("[done] 无需清理。")
            return 0
        if not args.apply:
            print(f"[dry-run] 以上 {len(empty_rows)} 行将被删除；确认无误后加 --apply 执行。")
            return 0

        cur.execute("DELETE FROM real_positions WHERE ts_code IS NULL OR TRIM(ts_code) = ''")
        deleted = cur.rowcount
        cur.execute("SELECT COUNT(*) FROM real_positions WHERE ts_code IS NULL OR TRIM(ts_code) = ''")
        left = cur.fetchone()[0]
        conn.commit()
        print(f"[applied] 已删除 {deleted} 行，复核残留 {left} 行（应为 0）。")
        return 0 if left == 0 else 1
    except Exception as e:  # noqa: BLE001
        conn.rollback()
        print(f"[error] 执行失败已回滚: {e}")
        return 1
    finally:
        conn.close()


if __name__ == "__main__":
    sys.exit(main())
