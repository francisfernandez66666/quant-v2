#!/usr/bin/env python3
# backfill_stock_industry_gz.py — 生产库 stocks.industry 缺失回填（§BT-NULL-INDUSTRY）。
#
# 背景（2026-11-28 生产首跑）：5883 行 stocks 中 675 行 industry 为 NULL/空串，
# 其中 338 只在近一个月仍有日线（不是纯退市壳）。B4 事件合成的区间 SQL 直接把
# NULL Scan 进 string，候选 #1 一开跑就整批失败。代码侧已 COALESCE + 跳过空板块，
# 但那只让回测不炸；本脚本负责把**能查到的行业真值写回生产库**，不是绕过缺口。
#
# 多源（与 gen_industry_buckets_gz.py 同源、幂等、只补缺不覆盖）：
#   1) baostock query_stock_industry（申万，约 5.2k，但对退市/老票覆盖很低）
#   2) akshare 东财行业板块成分（覆盖当前活跃票，单板块失败不拖整源）
#   3) --deep 时逐只走雪球/东财个股资料兜底（慢，默认不跑）
# 键统一取 6 位代码；stocks.ts_code 的 .SZ/.SH 后缀只在 UPDATE 的 WHERE 用。
#
# 用法（gz）：
#   sudo /opt/quant/venv/bin/python3 /opt/quant/scripts/backfill_stock_industry_gz.py \
#       --db /var/lib/quant-trading-v2/trading.db [--dry-run] [--deep]
# 锚行：INDUSTRY_BACKFILL_RESULT before=675 filled=N remaining=M sources=baostock,em
import argparse
import os
import sqlite3
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_industry_buckets_gz import src_baostock, src_em, src_xq  # noqa: E402


def base6(ts_code):
    code = str(ts_code or "").split(".")[0]
    return code if len(code) == 6 and code.isdigit() else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", required=True)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--deep", action="store_true", help="逐只兜底（慢，默认关）")
    a = ap.parse_args()

    db = sqlite3.connect(a.db)
    missing = [(r[0], base6(r[0])) for r in db.execute(
        "SELECT ts_code FROM stocks WHERE industry IS NULL OR industry='' ORDER BY ts_code")]
    codes = sorted({c for _, c in missing if c})
    before = len(missing)
    print("INDUSTRY_BACKFILL_BEGIN before=%d unique_codes=%d" % (before, len(codes)))
    if not codes:
        print("INDUSTRY_BACKFILL_RESULT before=0 filled=0 remaining=0 sources=none")
        db.close()
        return 0

    merged = {}
    sources = []
    for fn in (src_baostock, src_em):
        try:
            got, tag = fn()
        except Exception as e:  # 源不可用只告警，不阻断其他源
            print("INDUSTRY_BACKFILL_SOURCE_FAIL source=%s err=%s" % (fn.__name__, e), file=sys.stderr)
            continue
        hit = 0
        for c in codes:
            v = (got.get(c) or "").strip()
            if v and c not in merged:
                merged[c] = v
                hit += 1
        sources.append("%s:%d" % (tag, hit))
        print("INDUSTRY_BACKFILL_SOURCE source=%s usable=%d hit=%d" % (tag, len(got), hit))

    rest = [c for c in codes if c not in merged]
    if a.deep and rest:
        try:
            got, tag = src_xq(rest)
            hit = 0
            for c in rest:
                v = (got.get(c) or "").strip()
                if v and c not in merged:
                    merged[c] = v
                    hit += 1
            sources.append("%s:%d" % (tag, hit))
        except Exception as e:
            print("INDUSTRY_BACKFILL_SOURCE_FAIL source=xq err=%s" % e, file=sys.stderr)

    filled = 0
    if not a.dry_run:
        for ts_code, c in missing:
            v = merged.get(c)
            if not v:
                continue
            cur = db.execute(
                "UPDATE stocks SET industry=? WHERE ts_code=? AND (industry IS NULL OR industry='')",
                (v, ts_code))
            filled += cur.rowcount
        db.commit()
    else:
        filled = sum(1 for _, c in missing if c in merged)

    remaining = db.execute(
        "SELECT count(*) FROM stocks WHERE industry IS NULL OR industry=''").fetchone()[0]
    db.close()
    print("INDUSTRY_BACKFILL_RESULT before=%d filled=%d remaining=%d sources=%s"
          % (before, filled, remaining, ",".join(sources) or "none"))
    return 0 if filled > 0 or before == 0 else 2


if __name__ == "__main__":
    sys.exit(main())
