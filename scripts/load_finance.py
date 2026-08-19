#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# 财务数据装载脚本（"获取财务数据的规则"）：直连 baostock，多进程并行拉取全市场股票的
# 财务指标，写入研究库 fina_indicator（财务指标表）与 income（利润表）两张表，供因子发现
# （ROE 质量 / 净利同比成长 / SUE 单季净利同比等财务类因子）使用。
#
# 数据来源：baostock 免费接口（与 cmd/pydata/server.py 同源，这里直连以支持多进程并行，
# 绕开 baostock 单连接限制）。
#   - query_profit_data  → 盈利能力（roeAvg 净资产收益率 / gpMargin 毛利率 / npMargin 净利率 /
#                           epsTTM 每股收益 / netProfit 归母净利 / MBRevenue 营业收入）
#   - query_growth_data  → 成长能力（YOYNI 净利同比）
#   - query_balance_data → 偿债能力（liabilityToAsset 资产负债率）
#
# 覆盖区间：默认 2023-2026（近3年），逐年逐季拉取。断点续传：已装载的报告期（ts_code+end_date）
# 跳过，可重复运行补齐增量。多进程并行：每 worker 独立 bs.login() 一个 baostock 连接，提升吞吐。
#
# 用法：python3 scripts/load_finance.py <trading.db> <codes_file> [workers]
#   <trading.db>  研究库路径（默认 ~/.quant-trading-v2/trading.db）
#   <codes_file>  股票代码清单（每行一个 ts_code，如 600000.SH）
#   [workers]     并行进程数（默认 6）
import sqlite3
import sys
from concurrent.futures import ProcessPoolExecutor

import baostock as bs


def norm_date(s):
    """把 baostock 的 YYYY-MM-DD 归一为研究库用的 YYYYMMDD 字符串（报表期末/公告日）。"""
    return s.replace("-", "")


def f(v):
    """baostock 数值字符串 → float 或 None（空串返回 None，写库为 NULL）。
    财务字段可能为空（如中间报告期的 MBRevenue），需统一处理避免类型错误。"""
    if v is None or v == "":
        return None
    try:
        return float(v)
    except ValueError:
        return None


def to_ts(code):
    """baostock 代码（sh.600000）→ 研究库 ts_code（600000.SH）。"""
    if code.startswith("sh."):
        return code[3:] + ".SH"
    if code.startswith("sz."):
        return code[3:] + ".SZ"
    if code.startswith("bj."):
        return code[3:] + ".BJ"
    return code


def load_one(args):
    """装载单只股票近3年财务（独立 baostock login 连接，支持多进程并行）。
    返回 (code, 写入期数, 状态)。"""
    code, start_year, end_year, db_path = args
    # 每个 worker 独立登录 baostock，绕开"单账号单连接"限制以并行提速
    lg = bs.login()
    if lg.error_code != "0":
        return (code, 0, "login fail %s" % lg.error_msg)
    conn = sqlite3.connect(db_path, timeout=60)
    cur = conn.cursor()
    inserted = 0
    try:
        # 归一 baostock 代码：600000.SH → sh.600000
        bs_code = "sh." + code.replace(".SH", "").lower() if code.endswith(".SH") else "sz." + code.replace(".SZ", "").lower()
        for year in range(start_year, end_year + 1):
            for q in range(1, 5):
                # 报表期末日：Q1=0331, Q2=0630, Q3=0930, Q4=1231
                stat = "%d%02d%02d" % (year, q, 31 if q == 1 else (30 if q in (2, 4) else 31))
                # 断点续传：该报告期（股票+期末日）已装载则跳过
                exists = cur.execute(
                    "SELECT 1 FROM fina_indicator WHERE ts_code=? AND end_date=?",
                    (code, stat)).fetchone()
                if exists:
                    continue
                # 拉盈利能力（profit）；查询过晚日期 baostock 返回空，属正常（无该期数据）
                rs = bs.query_profit_data(code=bs_code, year=year, quarter=q)
                if rs.error_code != "0" or not rs.next():
                    continue
                prow = rs.get_row_data()
                pr = dict(zip(list(rs.fields), prow))
                # 拉成长能力（growth，净利同比）
                grow = {}
                gs = bs.query_growth_data(code=bs_code, year=year, quarter=q)
                if gs.error_code == "0" and gs.next():
                    grow = dict(zip(list(gs.fields), gs.get_row_data()))
                # 拉偿债能力（balance，资产负债率）
                bal = {}
                bs_r = bs.query_balance_data(code=bs_code, year=year, quarter=q)
                if bs_r.error_code == "0" and bs_r.next():
                    bal = dict(zip(list(bs_r.fields), bs_r.get_row_data()))
                # 写入 fina_indicator（财务指标表）：baostock 字段 → 研究库列 的映射
                cur.execute(
                    "INSERT OR REPLACE INTO fina_indicator "
                    "(ts_code, end_date, ann_date, eps, roe, grossprofit_margin, netprofit_margin, "
                    "debt_to_assets, yoy_or, yoy_net_profit) "
                    "VALUES (?,?,?,?,?,?,?,?,?,?)",
                    (code, norm_date(pr.get("statDate", "")), norm_date(pr.get("pubDate", "")),
                     f(pr.get("epsTTM")), f(pr.get("roeAvg")), f(pr.get("gpMargin")),
                     f(pr.get("npMargin")), f(bal.get("liabilityToAsset")), None,
                     f(grow.get("YOYNI"))))
                # 写入 income（利润表）：netProfit 归母净利、MBRevenue 营业收入
                cur.execute(
                    "INSERT OR REPLACE INTO income "
                    "(ts_code, end_date, n_income_attr_p, revenue, total_revenue) "
                    "VALUES (?,?,?,?,?)",
                    (code, norm_date(pr.get("statDate", "")), f(pr.get("netProfit")),
                     f(pr.get("MBRevenue")), f(pr.get("MBRevenue"))))
                conn.commit()
                inserted += 1
    except Exception as e:
        conn.close()
        bs.logout()
        return (code, inserted, "err %s" % e)
    conn.close()
    bs.logout()
    return (code, inserted, "ok")


def main():
    # 命令行参数：研究库路径 / 股票清单 / 并行进程数（默认本地路径、研究池、6 进程）
    db_path = sys.argv[1] if len(sys.argv) > 1 else "/Users/zhangzifei/.quant-trading-v2/trading.db"
    codes_file = sys.argv[2] if len(sys.argv) > 2 else "/tmp/research_pool.txt"
    workers = int(sys.argv[3]) if len(sys.argv) > 3 else 6
    # 近3年财务覆盖区间
    start_year, end_year = 2023, 2026
    with open(codes_file) as f:
        codes = [l.strip() for l in f if l.strip()]
    print("装载 %d 只股票财务 %d-%d，workers=%d" % (len(codes), start_year, end_year, workers))
    args = [(c, start_year, end_year, db_path) for c in codes]
    done = inserted = 0
    # 多进程并行装载：每 worker 独立 baostock 连接
    with ProcessPoolExecutor(max_workers=workers) as ex:
        for code, n, status in ex.map(load_one, args):
            done += 1
            inserted += n
            if done % 100 == 0:
                print("进度 %d/%d，累计写入 %d 期" % (done, len(codes), inserted))
            if status != "ok":
                print("  %s: %s" % (code, status))
    print("完成：%d 只，写入 %d 期" % (done, inserted))
    # 校验：统计两表行数
    conn = sqlite3.connect(db_path)
    print("fina_indicator 行数:", conn.execute("SELECT COUNT(*) FROM fina_indicator").fetchone()[0])
    print("income 行数:", conn.execute("SELECT COUNT(*) FROM income").fetchone()[0])
    conn.close()


if __name__ == "__main__":
    main()
