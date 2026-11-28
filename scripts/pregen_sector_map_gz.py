#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FLOW-SECTOR-JOIN 板块逐票连接键生产者（owner 2026-11-20 裁决 A：扩白名单 + 生产者）。

为什么需要这份文件：板块资金流腿（`_sector_delta_for`）的连接键是 **code -> 东财板块名**，
而云端此前**没有任何生产者**，所以这条腿天天弃权。`industry_map` 不能顶班：它是证监会门类
（79 个值，形如 "C39计算机、通信和其他电子设备制造业"），与 `sector_flow_daily.sector`
（东财板块名）交集为空——拿它当连接键会"看起来接上了、命中率恒 0"。

取数路径（盒子代取，广州对东财在 TLS 层被 RST）：
  1) `m:90+t:2` 行业板块列表（f12=BK 码，f14=板块名）；
  2) `m:90+t:3` 概念板块列表；
  3) 逐板块 `b:BKxxxx` 取成分股（f12=6 位代码，f14=名称）。
连接规则（确定性，两处各排一遍必漂）：
  **行业优先于概念**；同档按 BK 码升序；一只票只认第一个命中的板块。
  概念切片细、重叠多（同一只票挂十几个概念），拿它当主键会让 delta 落在最不稳定的切片上。

三条硬边界（改动前必读）：
  1. **全量或弃权**：任一板块分页没凑齐 `data.total` ⇒ 该板块整体丢弃并计入 failed。
     半张成分表会把一个板块的 delta 贴到错的一半票上，比弃权贵得多。
  2. **覆盖率闸**：成功板块 < 60% 或映射股票 < 3000 只 ⇒ **不覆盖旧文件**、退出码 1，
     由热更链下一轮重试（宁可今天弃权，也不写一份看起来正常的残表）。
  3. **只读**：不碰 orders/fills/持仓，不写 params——它是烘参的输入，不是烘参本身。

输出（BUILD_DIR 内）：`qmt_gateway/local_executor_sector_map.json`，day-keyed（跨日不留宿，
与 precalc 同一纪律）；热更链读它并烘进 `params.sector_map`。

English: produces the per-stock -> EastMoney board-name join key through the box relay,
industry boards first, concept boards second, all-or-nothing per board, day-keyed output.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime

BUILD_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA_DIR = os.environ.get("QUANT_DATA_DIR", "/var/lib/quant-trading-v2")
AUTH = os.environ.get("PREGEN_AUTH", os.path.join(DATA_DIR, "auth.json"))
OWNER_UID = os.environ.get("OWNER_UID", "u_1785377969825355000")
OUT_PATH = os.path.join(BUILD_DIR, "qmt_gateway", "local_executor_sector_map.json")
# §FLOW-SECTOR-JOIN 云端侧（2026-11-20）：**同一份表**也给决策机引擎用。
# 云端的逐票板块腿历史上走 `GetStockIndustry`（东财直连 f127），而广州对东财在 TLS 层被 RST
# ⇒ 恒取不到名字 ⇒ 那条腿恒弃权且读数上无报错。引擎读这份 `<DataDir>/sector_map.json`
# （见 internal/data/sector_map.go），与载体共用同一个生产者，避免"载体接上了、云端没接"。
ENGINE_OUT = os.path.join(DATA_DIR, "sector_map.json")

# 上游分页大小：盒子中继与东财都对 pz 有 100 硬顶（实测 pz=500 仍只回 100），
# 所以一次 100、按 total 逐页翻；页数硬顶防上游 total 异常时的无界请求。
PAGE = 100
MAX_PAGES = 40
REQ_TIMEOUT = 20
SLEEP_MS = int(os.environ.get("SECTOR_MAP_SLEEP_MS", "40"))

# 覆盖率闸（结构常量，不是策略阈值）：低于它就不覆盖旧文件。可用环境变量在演练时放宽。
MIN_BOARD_OK_PCT = float(os.environ.get("SECTOR_MAP_MIN_OK_PCT", "60"))
MIN_STOCKS = int(os.environ.get("SECTOR_MAP_MIN_STOCKS", "3000"))

INDUSTRY_FS = "m:90+t:2"
CONCEPT_FS = "m:90+t:3"
MEMBER_FIELDS = "f12,f14"


def die(reason):
    print("SECTOR_MAP_RESULT=ABORT reason=%s" % reason)
    sys.exit(1)


def describe(exc):
    """异常 → 可读的一行归因（**必须带 HTTP 状态码**）。

    2026-11-27 08:20 现场：`SECTOR_MAP_RESULT=ABORT reason=board_list_failed:HTTPError`
    ——类名把 `code`（401/404/502/503…）整个吞掉了，而"钥匙被拒""路径不存在""上游 503"
    三种情况的处置完全不同（补钥匙 / 换路径 / 等它自己恢复）。这一行是这条腿唯一的失败读数，
    丢掉状态码等于让下一次排障从零开始。
    """
    code = getattr(exc, "code", None)
    if code:
        return "%s:%s" % (exc.__class__.__name__, code)
    return exc.__class__.__name__


def with_retry(label, fn, attempts=3, base_sleep=20):
    """对**瞬时**失败重试（中继/WG/上游抖一下很常见，见 2026-11-27 08:20 实录）。

    只重试到 attempts 次；最后一次的异常照原样抛出，由调用方 die。
    幂等性由调用方保证（board_rows 只读、clist_pages 要么完整要么抛错）。
    """
    last = None
    for i in range(attempts):
        try:
            return fn()
        except (urllib.error.URLError, RuntimeError, ValueError, OSError) as exc:
            last = exc
            if i + 1 < attempts:
                print("SECTOR_MAP_RETRY step=%s attempt=%d/%d err=%s" % (
                    label, i + 1, attempts, describe(exc)))
                sys.stdout.flush()
                time.sleep(base_sleep * (i + 1))
    raise last


def load_endpoint():
    """从运营账号在册快照取（gateway_url, token）。

    钥匙与地址必须同一个来源：地址跟着钥匙的持有者走（Go 侧 feedEndpoint 同一条纪律）。
    环境变量 QUANT_GATEWAY_URL / QUANT_GATEWAY_TOKEN 只在演练时覆盖。
    """
    base = (os.environ.get("QUANT_GATEWAY_URL") or "").strip()
    token = (os.environ.get("QUANT_GATEWAY_TOKEN") or "").strip()
    if base and token:
        return base.rstrip("/"), token
    try:
        with open(AUTH, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError) as exc:
        die("auth_unreadable:%s" % exc.__class__.__name__)
    cfg = [c for c in (doc.get("configs") or [])
           if c.get("key") == "quant_config_json_v1" and c.get("user_id") == OWNER_UID]
    if not cfg:
        die("owner_config_missing")
    raw = cfg[0].get("value")
    qc = json.loads(raw) if isinstance(raw, str) else (raw or {})
    qmt = qc.get("qmt") or {}
    base = str(qmt.get("gateway_url") or "").strip()
    token = str(qmt.get("token") or "").strip()
    if not base or not token:
        die("gateway_endpoint_missing")
    return base.rstrip("/"), token


def relay(base, token, params):
    """取一次 /flow/money 中继响应，返回上游原始 body 的 dict（失败即异常）。"""
    url = base + "/flow/money?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/json",
    })
    with urllib.request.urlopen(req, timeout=REQ_TIMEOUT) as resp:
        env = json.loads(resp.read().decode("utf-8", "replace"))
    if not env.get("ok"):
        raise RuntimeError("relay_not_ok status=%s err=%s" % (
            env.get("upstream_status"), env.get("err")))
    body = env.get("upstream_body") or ""
    if not body.strip():
        raise RuntimeError("relay_empty_body")
    out = json.loads(body)
    if not isinstance(out, dict):
        raise RuntimeError("relay_body_not_object")
    return out


def rows_of(payload):
    """解出 clist 的行集合与 total。

    东财 diff 有两种形态（实测 2026-11-19）：不带 np=1 回**索引 map**、带 np=1 回数组。
    盒子中继按生产腿只发四个参数（不带 np），所以这里两种都收，map 形态按索引排序
    保证同一份上游在同一台机上每次都排出同样的顺序（排序不稳定会让映射天天小幅漂）。
    """
    data = (payload or {}).get("data") or {}
    diff = data.get("diff")
    rows = []
    if isinstance(diff, dict):
        def idx(item):
            key = str(item[0])
            return int(key) if key.isdigit() else 0
        rows = [item[1] for item in sorted(diff.items(), key=idx)]
    elif isinstance(diff, list):
        rows = list(diff)
    total = data.get("total")
    try:
        total = int(total)
    except (TypeError, ValueError):
        total = -1
    return [r for r in rows if isinstance(r, dict)], total


def clist_pages(base, token, fs, fields):
    """逐页取全一个 clist 查询。**要么完整要么抛错**（边界 1）。"""
    out = []
    total = -1
    for page in range(1, MAX_PAGES + 1):
        payload = relay(base, token, {
            "kind": "clist", "fs": fs, "fields": fields,
            "pn": str(page), "pz": str(PAGE),
        })
        rows, page_total = rows_of(payload)
        if page == 1:
            total = page_total
        out.extend(rows)
        if not rows:
            break
        if total > 0 and len(out) >= total:
            break
        if len(rows) < PAGE:
            if total > 0 and len(out) < total:
                raise RuntimeError("short_page fs=%s page=%d got=%d/%d" % (
                    fs, page, len(out), total))
            break
        if SLEEP_MS > 0:
            time.sleep(SLEEP_MS / 1000.0)
    if total > 0 and len(out) < total:
        raise RuntimeError("incomplete fs=%s %d/%d" % (fs, len(out), total))
    return out


def board_rows(base, token, fs):
    """板块列表 → [(bk_code, board_name)]，按 BK 码升序（确定性）。"""
    rows = clist_pages(base, token, fs, MEMBER_FIELDS)
    out = []
    for row in rows:
        code = str(row.get("f12") or "").strip()
        name = str(row.get("f14") or "").strip()
        if not code.startswith("BK") or not code[2:].isdigit() or not name:
            continue
        out.append((code, name))
    return sorted(out, key=lambda item: item[0])


def members_of(base, token, bk_code):
    """一个板块的成分股 6 位代码（完整或抛错）。"""
    rows = clist_pages(base, token, "b:" + bk_code, MEMBER_FIELDS)
    codes = []
    for row in rows:
        code = str(row.get("f12") or "").strip()
        if len(code) == 6 and code.isdigit():
            codes.append(code)
    return codes


def build_map(base, token, industry, concept):
    """行业优先、同档按码升序、先到先得；返回 (map, stats)。"""
    mapping = {}
    stats = {"boards_ok": 0, "boards_failed": 0, "failed_codes": []}
    for tier, boards in (("industry", industry), ("concept", concept)):
        for bk_code, name in boards:
            try:
                codes = members_of(base, token, bk_code)
            except (urllib.error.URLError, RuntimeError, ValueError, OSError) as exc:
                stats["boards_failed"] += 1
                if len(stats["failed_codes"]) < 12:
                    stats["failed_codes"].append("%s:%s" % (bk_code, describe(exc)))
                continue
            if not codes:
                stats["boards_failed"] += 1
                continue
            stats["boards_ok"] += 1
            stats["%s_boards" % tier] = stats.get("%s_boards" % tier, 0) + 1
            for code in codes:
                if code not in mapping:
                    mapping[code] = name
            if SLEEP_MS > 0:
                time.sleep(SLEEP_MS / 1000.0)
    return mapping, stats


def main():
    day = datetime.now().astimezone().strftime("%Y%m%d")
    base, token = load_endpoint()
    try:
        industry = with_retry("board_list_industry", lambda: board_rows(base, token, INDUSTRY_FS))
        concept = with_retry("board_list_concept", lambda: board_rows(base, token, CONCEPT_FS))
    except (urllib.error.URLError, RuntimeError, ValueError, OSError) as exc:
        die("board_list_failed:%s" % describe(exc))
    if not industry and not concept:
        die("board_list_empty")
    print("SECTOR_MAP_BOARDS industry=%d concept=%d" % (len(industry), len(concept)))
    mapping, stats = build_map(base, token, industry, concept)
    attempted = stats["boards_ok"] + stats["boards_failed"]
    ok_pct = (100.0 * stats["boards_ok"] / attempted) if attempted else 0.0
    print("SECTOR_MAP_FETCH ok=%d failed=%d ok_pct=%.1f stocks=%d %s" % (
        stats["boards_ok"], stats["boards_failed"], ok_pct, len(mapping),
        " ".join(stats["failed_codes"])))
    # 覆盖率闸（边界 2）：残表不覆盖旧文件，交给热更链下一轮重试。
    if ok_pct < MIN_BOARD_OK_PCT or len(mapping) < MIN_STOCKS:
        die("coverage_too_low ok_pct=%.1f<%s stocks=%d<%d" % (
            ok_pct, MIN_BOARD_OK_PCT, len(mapping), MIN_STOCKS))
    doc = {
        "day": day,
        "generated_at": int(time.time()),
        "count": len(mapping),
        "industry_boards": len(industry),
        "concept_boards": len(concept),
        "boards_ok": stats["boards_ok"],
        "boards_failed": stats["boards_failed"],
        "industry_mapped": stats.get("industry_boards", 0),
        "concept_mapped": stats.get("concept_boards", 0),
        "sector_map": mapping,
    }
    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    tmp = OUT_PATH + ".tmp"
    with open(tmp, "w", encoding="ascii") as fh:
        json.dump(doc, fh, ensure_ascii=True, sort_keys=True)
    os.replace(tmp, OUT_PATH)
    # 云端引擎那份（§FLOW-SECTOR-JOIN）：写失败只警告，绝不因为数据目录权限把载体那份也拖掉
    # （载体那份才是执行腿的输入；引擎那份是让云端逐票板块 delta 也活过来）。
    try:
        os.makedirs(os.path.dirname(ENGINE_OUT), exist_ok=True)
        etmp = ENGINE_OUT + ".tmp"
        with open(etmp, "w", encoding="ascii") as fh:
            json.dump(doc, fh, ensure_ascii=True, sort_keys=True)
        os.replace(etmp, ENGINE_OUT)
    except OSError as exc:
        print("SECTOR_MAP_ENGINE_WRITE_FAIL %s path=%s" % (exc.__class__.__name__, ENGINE_OUT))
    print("SECTOR_MAP_RESULT=OK day=%s count=%d boards_ok=%d boards_failed=%d path=%s" % (
        day, len(mapping), stats["boards_ok"], stats["boards_failed"], OUT_PATH))
    return 0


if __name__ == "__main__":
    sys.exit(main())
