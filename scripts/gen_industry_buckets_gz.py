#!/usr/bin/env python3
# gen_industry_buckets_gz.py — 载体集中度桶的行业/概念映射回填（§BUCKETS-20261112）。
#
# 背景：industry_map / concept_map 两个烘参键自 2026-11-10 装机以来**没有任何链生成**
# （恒空），载体集中度桶闸（PLAN_CARRIER_RISK_REBUILD §1.2 的 MUST-BAKE 项）对全部
# 买入 fail-closed —— 11-12 盒子实录 64 个买入意图全部 sector_bucket_unknown。
#
# 多源补齐（幂等，只补缺，已有行业的票不重查）：
#   源1 baostock query_stock_industry（申万，~5.2k 覆盖，主源）
#   源2 akshare 东财行业板块（IP 限流时整源跳过，不重试不阻塞）
#   源3 akshare 雪球/东财个股逐只（慢，只处理残余）
# 键写**双形态**：纯6位 + `.SZ|.SH` 后缀 + `SZ|SH` 前缀——监视池/precalc 的键形态
# 混杂（11-12 实录：precalc 92 只带后缀、27 只纯 6 位），单形态必然查空。
#
# 用法：/opt/quant/venv/bin/python3 scripts/gen_industry_buckets_gz.py [--build-dir D]
# 锚行：BUCKETS_RESULT covered=x/y entries=z sources=a,b,c
import argparse
import json
import os
import sys
import time

DEFAULT_BUILD_DIR = os.environ.get("CARRIER_BUILD_DIR", "/opt/quant/carrier_build")


def suffix_shape(c):
    if len(c) == 6 and c.isdigit():
        return c + (".SH" if c.startswith(("6", "9")) else ".SZ")
    return None


def prefix_shape(c):
    if len(c) == 6 and c.isdigit():
        return ("SH" if c.startswith(("6", "9")) else "SZ") + c
    return None


def src_baostock():
    import baostock as bs
    lg = bs.login()
    try:
        rs = bs.query_stock_industry()
        rows = []
        while rs.error_code == "0" and rs.next():
            rows.append(rs.get_row_data())
    finally:
        bs.logout()
    out = {}
    for r in rows:
        if len(r) >= 4:
            code = r[1].split(".")[-1] if "." in r[1] else r[1]
            name = (r[3] or "").strip()
            if len(code) == 6 and code.isdigit() and name:
                out[code] = name
    return out, "baostock"


def src_em():
    import akshare as ak
    df = ak.stock_board_industry_name_em()
    out = {}
    for _, row in df.iterrows():
        board = str(row.get("板块名称") or "")
        try:
            cons = ak.stock_board_industry_cons_em(symbol=str(row.get("板块代码") or ""))
            for _, r in cons.iterrows():
                c = str(r.get("代码") or "")
                if len(c) == 6 and c.isdigit() and board:
                    out[c] = board
        except Exception:
            continue  # 单板块失败不拖整源
    return out, "em"


def src_xq(miss):
    """逐只兜底（慢）：雪球个股资料的行业字段；东财个股接口作第二尝试。"""
    import akshare as ak
    out = {}
    errs = []
    for s in miss:
        sym = prefix_shape(s) or s
        name = ""
        for fn, arg in ((getattr(ak, "stock_individual_basic_info_xq", None), sym),
                        (getattr(ak, "stock_individual_info_em", None), s)):
            fn_name = getattr(fn, "__name__", "?")
            if fn is None:
                continue
            try:
                info = fn(arg)
                kv = {}
                if hasattr(info, "itertuples") or hasattr(info, "columns"):
                    cols = list(getattr(info, "columns", []) or [])
                    if cols and "item" in cols:
                        kv = {str(a): str(b) for a, b in zip(info.get("item") or [],
                                                             info.get("value") or [])}
                    else:
                        for _, r in info.iterrows():
                            kv[str(r.iloc[0])] = str(r.iloc[1])
                name = kv.get("行业") or kv.get("industry") or kv.get("所属行业") or ""
                if name:
                    break
            except Exception as exc:  # noqa: BLE001
                if len(errs) < 3:
                    errs.append("%s:%r" % (fn_name, exc))
                time.sleep(0.2)
        if name:
            out[s] = name.strip()
        time.sleep(0.3)
    if errs:
        print("BUCKETS_XQ_ERRS %s" % " | ".join(errs))
    return out, "xq"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--build-dir", default=DEFAULT_BUILD_DIR)
    args = ap.parse_args()
    params_path = os.path.join(args.build_dir, "qmt_gateway", "local_executor_params.json")
    with open(params_path, "r", encoding="utf-8") as fh:
        doc = json.load(fh)
    params = doc.get("params") or {}
    symbols = [s for s in (params.get("symbols") or []) if len(s) == 6 and s.isdigit()]

    im = dict(params.get("industry_map") or {})
    have = {k for k in im if len(k) == 6 and k.isdigit()}
    miss = [s for s in symbols if s not in have]
    sources = []
    for src_fn in (src_baostock, src_em):
        try:
            data, tag = src_fn()
            sources.append(tag)
            for s in miss:
                if s not in im and s in data:
                    im[s] = data[s]
        except Exception as exc:  # noqa: BLE001 — 单源失败跳过不阻塞
            print("BUCKETS_SRC_FAIL %s %r" % (src_fn.__name__, exc))
    still = [s for s in symbols if s not in im]
    if still:
        try:
            data, tag = src_xq(still)
            sources.append(tag)
            im.update({k: v for k, v in data.items() if k in still and v})
        except Exception as exc:  # noqa: BLE001
            print("BUCKETS_SRC_FAIL xq %r" % (exc,))

    # 双形态展开（只有纯 6 位键才展开；来源键可能已带后缀，先归一到纯 6 位）
    norm = {}
    for k, v in im.items():
        base = k[:6] if len(k) >= 6 and k[:6].isdigit() else None
        if base and v:
            norm[base] = v
    out = {}
    for c, v in norm.items():
        out[c] = v
        for k in (suffix_shape(c), prefix_shape(c)):
            if k:
                out[k] = v
    params["industry_map"] = out
    params["concept_map"] = dict(out)
    json.dump(doc, open(params_path, "w", encoding="utf-8", newline=""),
              ensure_ascii=True, indent=2)
    covered = sum(1 for s in symbols if s in out)
    print("BUCKETS_RESULT covered=%d/%d entries=%d sources=%s"
          % (covered, len(symbols), len(out), ",".join(sources) or "none"))
    return 0 if covered else 2


if __name__ == "__main__":
    sys.exit(main())
