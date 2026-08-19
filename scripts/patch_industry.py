#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# 行业分类补全脚本（"获取行业分类的规则"）：直连 baostock query_stock_industry，
# 读取全市场股票所属行业，写入研究库 stocks.industry 字段。
#
# 用途：形态战法回测与因子环境分组依赖"板块共振"，而板块按行业聚合（sector-rebuild）。
# 若 stocks.industry 为空，sector-rebuild 按行业聚合返回 0 行，导致形态战法无法验证板块共振。
# 该脚本补全行业字段后，sector-rebuild 才能正常重建板块历史。
#
# 数据来源：baostock 免费行业分类接口（"证监会行业分类"，如 J66货币金融服务）。
# 用法：python3 scripts/patch_industry.py <trading.db>
import sqlite3
import sys

import baostock as bs

def main():
    db_path = sys.argv[1] if len(sys.argv) > 1 else "/Users/zhangzifei/.quant-trading-v2/trading.db"
    # 登录 baostock（匿名）
    lg = bs.login()
    if lg.error_code != "0":
        print("baostock login failed:", lg.error_code, lg.error_msg)
        return 1
    # 查询全市场行业分类
    rs = bs.query_stock_industry()
    if rs.error_code != "0":
        print("industry query failed:", rs.error_code, rs.error_msg)
        bs.logout()
        return 1
    # 收集 code -> industry（baostock code 形如 sh.600000）
    industry = {}
    while rs.error_code == "0" and rs.next():
        row = rs.get_row_data()
        code, ind = row[1], row[3]
        if ind:
            industry[code] = ind
    bs.logout()
    print("baostock industry rows:", len(industry))

    conn = sqlite3.connect(db_path)
    cur = conn.cursor()
    # 归一代码：stocks.ts_code 形如 600000.SH，baostock code 形如 sh.600000
    def to_ts(code):
        if code.startswith("sh."):
            return code[3:] + ".SH"
        if code.startswith("sz."):
            return code[3:] + ".SZ"
        if code.startswith("bj."):
            return code[3:] + ".BJ"
        return code
    # 逐只更新行业字段
    updated = 0
    for code, ind in industry.items():
        ts = to_ts(code)
        cur.execute("UPDATE stocks SET industry=? WHERE ts_code=?", (ind, ts))
        updated += cur.rowcount
    conn.commit()
    print("已更新行业:", updated)
    # 校验：统计有行业的股票数
    filled = cur.execute("SELECT COUNT(*) FROM stocks WHERE industry IS NOT NULL AND industry!=''").fetchone()[0]
    print("stocks 有行业数:", filled)
    conn.close()
    return 0

if __name__ == "__main__":
    sys.exit(main())
