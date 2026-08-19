#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# 导出本地研究库的财务数据（fina_indicator / income）为 SQL INSERT 语句，
# 供上传到云端 trading.db 补齐财务因子数据。
# 用法：python3 scripts/export_fina_sql.py <本地db> <输出.sql>
import sqlite3
import sys


def esc(v):
    """值转 SQL 字面量：None→NULL，数字→原样，字符串→单引号转义。"""
    if v is None:
        return "NULL"
    if isinstance(v, (int, float)):
        return repr(v)
    return "'" + str(v).replace("'", "''") + "'"


def dump_table(cur, table, cols, out):
    """把某表全部行写成 INSERT OR REPLACE 语句。"""
    col_sql = ",".join(cols)
    rows = cur.execute("SELECT %s FROM %s" % (col_sql, table)).fetchall()
    n = 0
    for r in rows:
        vals = ",".join(esc(v) for v in r)
        out.write("INSERT OR REPLACE INTO %s (%s) VALUES (%s);\n" % (table, col_sql, vals))
        n += 1
    return n


def main():
    src_db = sys.argv[1] if len(sys.argv) > 1 else "/Users/zhangzifei/.quant-trading-v2/trading.db"
    out_sql = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fina_migration.sql"
    conn = sqlite3.connect("file:%s?mode=ro" % src_db, uri=True, timeout=10)
    cur = conn.cursor()
    with open(out_sql, "w", encoding="utf-8") as f:
        f.write("BEGIN;\n")
        n_fina = dump_table(cur, "fina_indicator",
                            ["ts_code", "end_date", "ann_date", "eps", "roe", "roe_waa", "roa", "roe_dt",
                             "grossprofit_margin", "netprofit_margin", "debt_to_assets", "yoy_or",
                             "yoy_net_profit", "or_yoy", "netprofit_yoy"], f)
        n_inc = dump_table(cur, "income",
                           ["ts_code", "end_date", "n_income_attr_p", "revenue", "total_revenue"], f)
        f.write("COMMIT;\n")
    conn.close()
    print("导出 fina_indicator %d 行, income %d 行 → %s" % (n_fina, n_inc, out_sql))
    import os
    print("SQL 文件大小: %.1f KB" % (os.path.getsize(out_sql) / 1024))


if __name__ == "__main__":
    main()
