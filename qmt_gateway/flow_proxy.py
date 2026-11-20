#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.flow_proxy — §FLOW-MONEY 资金流取数中继（2026-11-19，owner 裁决"资金路选 A"）。

为什么要有这一层（现场实测，不是推断）：
  - 决策机（广州 Ubuntu）对东财全族不可达：push2 / push2his / push2delay 的 /api/qt/*
    一律在 TLS 握手后被 RST（curl 报 Empty reply from server）。同日熔断日志按路径去重
    3495 次，日K 734 / fflow 521 / clist 476+383 / 指数点位 412+373 —— 四条腿全断，
    系统是靠腾讯/新浪兜底在跑。
  - 盒子（Win 物理机）实测可达：EM_HIS=200 / EM_CLIST=200（经 WireGuard 隧道验过）。
  ⇒ 由盒子代取，广州经既有 WG HTTP 通道（10.77.0.2:8789，Bearer 同一把钥匙）来拿。

三条硬边界（改动前必读）：
  1. **只做白名单中继，不是通用代理**：上游 host 与 path 在本模块写死，外部只能传
     secid / klt / lmt / fs / fields / pn / pz 这类受限参数，逐项校验后才拼 URL。
     绝不接受任意 URL —— 否则盒子就成了任何人都能打的出网跳板。
  2. **原样回传上游 body**（包在 upstream_body 字符串里）：广州侧继续用既有解析器
     （parseMoneyFlowHistory / parseSectorList），两边不会各长一份形状而漂开。
  3. **必须缓存**：广州被东财挡住的那一天，请求量是"每轮给几百只票各取一次"的量级；
     这一层若不再兜一层 TTL 缓存，等于把风暴原样搬到盒子上，把盒子也一起打封。
     缓存键含全部受限参数，负结果同样缓存（上游抖动时不让每个调用都去撞一次）。

English: allow-listed, TTL-cached relay of EastMoney fund-flow/clist endpoints, so the
decision host (which EastMoney RSTs) can read flow data through the box (which can reach it).
"""
import json
import logging
import re
import threading
import time
import urllib.error
import urllib.request

log = logging.getLogger("qmt_gateway.flow_proxy")

# 上游写死：外部参数不得影响 host/path（边界 1）。
#
# 两条 fflow 上游，分工是**实测出来的**（2026-11-19 盒子直连）：
#   - push2 的 `fflow/kline/get`（分时）：klt=1&lmt=0 回当日 240 根分钟累计，末根=当日累计。
#     指数与板块**都可用**（90.BK0420 实测 240 根）。这条是"今日腿"的主路。
#   - push2his 的 `fflow/daykline/get`（日K）：klt=101 回逐日历史，但**从盒子打它是间歇性
#     断连**（同一分钟里连续两次 ERR，而指数腿此前刚成功过）；且它不接受 90.BK 板块 secid
#     （直接 connection closed）。所以它只作历史腿，并作今日腿的兜底。
_EM_FFLOW_INTRADAY = "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get"
_EM_FFLOW_DAILY = "https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get"
_EM_CLIST = "https://push2.eastmoney.com/api/qt/clist/get"

# 盒子上是 Windows 客户端，UA/Referer 与生产东财腿保持一致（缺了会被回 403/空体）。
_UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
       "(KHTML, like Gecko) Chrome/120 Safari/537.36")
_REFERER = "https://quote.eastmoney.com/"

# 资金流字段：f51 日期 + f52 主力 / f53 小单 / f54 中单 / f55 大单 / f56 超大单（元）。
_FFLOW_FIELDS = "f51,f52,f53,f54,f55,f56"

# 板块列表只放行两个市场与一组字段；多一个都不认（宁可 400，也不让参数面长出去）。
_CLIST_FS = ("m:90+t:2", "m:90+t:3")  # 行业板块 / 概念板块
# §FLOW-SECTOR-JOIN（owner 2026-11-20 裁决 A）：板块**成分股**查询 `fs=b:BKxxxx`。
# 这是"扩白名单"的唯一新增面：只为 §FLOW-SECTOR-JOIN 的日级生产者提供成分表，
# 不放行任何个股市场（`m:0+t:6` 之类仍然 400）。BK 码逐段校验，理由同 valid_secid。
_CLIST_MEMBER_FS_RE = re.compile(r"^b:BK\d{2,6}$")
_CLIST_FIELDS = ("f12", "f14", "f3", "f6", "f20", "f62", "f104", "f105", "f184")

# 缓存 TTL：当日分时与板块列表 30s（与引擎 60s TTL 对齐，留一半余量）；
# 逐日历史 5min（盘后补数，一天只变一次）。
_TTL_FFLOW_INTRADAY = 30.0
_TTL_FFLOW_HISTORY = 300.0
_TTL_CLIST = 30.0

_cache = {}
_cache_lock = threading.Lock()
_CACHE_MAX = 256


class FlowProxyError(Exception):
    """参数非法（网关转 400）——与"上游不可用"（502）分开，处置方向不同。"""


def valid_secid(value):
    """secid 白名单校验：指数/个股 `1.000001`、`0.399001`，板块 `90.BK0420`。

    为什么逐段校验而不是正则通配：secid 会直接拼进上游 URL，任何能带 `&`/`/`/`?`
    的串都等于把路径控制权交出去（边界 1）。
    """
    if not isinstance(value, str) or value.count(".") != 1:
        return False
    market, code = value.split(".", 1)
    if market in ("0", "1"):
        return len(code) == 6 and code.isdigit()
    if market == "90":
        return code.startswith("BK") and code[2:].isdigit() and 2 <= len(code[2:]) <= 6
    return False


def build_fflow_intraday_url(secid):
    """当日分时（push2 fflow/kline）：klt=1&lmt=0 → 240 根分钟累计，末根=当日累计。"""
    if not valid_secid(secid):
        raise FlowProxyError("bad secid: %r" % (secid,))
    return ("%s?secid=%s&klt=1&lmt=0&fields1=f1,f2,f3,f4,f5,f6&fields2=%s"
            % (_EM_FFLOW_INTRADAY, secid, _FFLOW_FIELDS))


def build_fflow_daily_url(secid, klt, lmt):
    """逐日历史（push2his fflow/daykline）：klt=101 → 每根=当日净额。"""
    if not valid_secid(secid):
        raise FlowProxyError("bad secid: %r" % (secid,))
    if klt not in (1, 101):
        raise FlowProxyError("bad klt: %r" % (klt,))
    if not isinstance(lmt, int) or lmt < 0 or lmt > 2000:
        raise FlowProxyError("bad lmt: %r" % (lmt,))
    return ("%s?secid=%s&fields1=f1,f2,f3,f4,f5,f6&fields2=%s&klt=%d&lmt=%d"
            % (_EM_FFLOW_DAILY, secid, _FFLOW_FIELDS, klt, lmt))


def build_fflow_url(secid, klt, lmt):
    """拼 fflow 上游 URL（参数全校验；任一非法即 FlowProxyError）。

    按 klt 分流：1 = 当日分时（今日腿主路，指数/板块都可用）；101 = 逐日历史（历史腿）。
    """
    if klt not in (1, 101):
        raise FlowProxyError("bad klt: %r" % (klt,))
    if klt == 1:
        return build_fflow_intraday_url(secid)
    return build_fflow_daily_url(secid, klt, lmt)


def valid_clist_fs(fs):
    """clist 的 fs 白名单：两档板块列表 + 一条板块成分（`b:BKxxxx`）。"""
    if fs in _CLIST_FS:
        return True
    return bool(_CLIST_MEMBER_FS_RE.match(fs or ""))


def build_clist_url(fs, fields, pn, pz):
    """拼 clist 上游 URL（fs 必须命中白名单，fields 必须是允许字段的子集）。

    为什么**不带** `np=1`：实测（2026-11-19，盒子直连）带 np=1 时上游把 data.diff 回成
    **数组**，不带时回成**索引 map**——而 Go 侧 parseSectorList 只认 map 形态（生产两条
    板块腿都不带 np=1）。参数面照抄生产腿，广州侧才不会拿到一份自己解析不了的 body。
    另：上游对 pz 有 100 的硬顶（pz=500 仍只回 100 条），所以分页必须按 100 逐页翻。
    """
    if not valid_clist_fs(fs):
        raise FlowProxyError("bad fs: %r" % (fs,))
    wanted = [f.strip() for f in (fields or "").split(",") if f.strip()]
    if not wanted:
        raise FlowProxyError("empty fields")
    for name in wanted:
        if name not in _CLIST_FIELDS:
            raise FlowProxyError("field not allowed: %r" % (name,))
    if not isinstance(pn, int) or pn < 1 or pn > 20:
        raise FlowProxyError("bad pn: %r" % (pn,))
    if not isinstance(pz, int) or pz < 1 or pz > 500:
        raise FlowProxyError("bad pz: %r" % (pz,))
    # 参数面**逐字照抄生产腿**（getSectorListPrimary / getSectorListFromMirror）：
    # 只有 pn/pz/fs/fields 四个。绝不加 fltt=2 / invt=2 / po / fid——
    # fltt=2 会让上游把 f3（涨跌幅）从"整数基点"换成"已是百分数"，而 Go 侧
    # parseSectorList 固定 ÷100，于是涨跌幅会整体小 100 倍；f184 同理错档。
    # 这条是 2026-11-19 实测踩出来的：加了 fltt=2 后 f184=1.42 而 f62/f6=1.42%，
    # 与生产腿"万分比"口径对不上。
    return ("%s?pn=%d&pz=%d&fs=%s&fields=%s"
            % (_EM_CLIST, pn, pz, fs, ",".join(wanted)))


def fetch_upstream(url, timeout=8.0):
    """取上游原始 body。返回 (status, text)；HTTP 非 200 由调用方判失败，不在这里抛。"""
    req = urllib.request.Request(url, headers={
        "User-Agent": _UA,
        "Referer": _REFERER,
        "Accept": "application/json, text/plain, */*",
    })
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:  # 4xx/5xx 也带上 body，便于判"被封"还是"参数错"
        try:
            body = exc.read().decode("utf-8", "replace")
        except Exception:  # noqa: BLE001 — 读 body 失败不该盖掉原始状态码
            body = ""
        return exc.code, body


def _cached(key, ttl, producer):
    """按 key 做 TTL 缓存（边界 3）。返回 (payload, "hit"|"miss")。"""
    now = time.time()
    with _cache_lock:
        hit = _cache.get(key)
        if hit is not None and now - hit[0] < ttl:
            return hit[1], "hit"
    payload = producer()
    with _cache_lock:
        _cache[key] = (now, payload)
        if len(_cache) > _CACHE_MAX:
            # 键空间本身有限（secid×klt×lmt 有界），超限只做一次粗粒度修剪防长跑膨胀。
            for stale in list(_cache)[:64]:
                _cache.pop(stale, None)
    return payload, "miss"


def _fetch_fflow(url):
    """取一次 fflow 上游，返回 payload dict（含 ok/upstream_status/upstream_body/src）。"""
    status, body = fetch_upstream(url)
    ok = status == 200 and body.strip() not in ("", "null")
    if ok:
        # 200 但 data 为 null 同样是"没数据"，不能当成功回给广州（那边会解析失败后被
        # 当成一次性抖动反复重试）。判定放在这一层，两种读数的处置方向才能一致。
        try:
            ok = bool(json.loads(body).get("data"))
        except (ValueError, TypeError):
            ok = False
    return {
        "ok": ok,
        "upstream_status": status,
        "upstream_body": body,
        "src": "eastmoney_fflow",
        "upstream_url": url,
    }


def relay_fflow(secid, klt, lmt):
    """中继 fflow：返回 {ok, upstream_status, upstream_body, src, cache, upstream_url}。

    今日腿（klt=1）主路走 push2 分时，失败落回 push2his 日K末根（两条都给"当日累计"，
    差别只在时间粒度）；两条都失败才算失败——盒子上 push2his 是间歇性断连，单靠它会让
    资金流整天随机缺数。历史腿（klt=101）只有 push2his 一条，失败就如实报失败。
    """
    primary_url = build_fflow_url(secid, klt, lmt)
    ttl = _TTL_FFLOW_INTRADAY if klt == 1 else _TTL_FFLOW_HISTORY

    def produce():
        payload = _fetch_fflow(primary_url)
        if payload["ok"]:
            return payload
        if klt == 1:
            # 兜底：push2his 日K（末根=当日累计）。板块 secid 在这条上游会被拒，
            # 失败也照样回失败——绝不拿"昨日那根"冒充当日。
            fallback_url = build_fflow_daily_url(secid, 101, 2)
            fb = _fetch_fflow(fallback_url)
            if fb["ok"]:
                fb["src"] = "eastmoney_fflow_daily_fallback"
                log.warning("[flow_proxy] fflow 分时失败、日K兜底成功 secid=%s status=%s",
                            secid, payload["upstream_status"])
                return fb
            log.warning("[flow_proxy] fflow 两条上游均失败 secid=%s klt=%s intraday=%s daily=%s",
                        secid, klt, payload["upstream_status"], fb["upstream_status"])
            payload["err"] = "intraday=%s daily=%s" % (
                payload["upstream_status"], fb["upstream_status"])
        else:
            log.warning("[flow_proxy] fflow 历史上游异常 status=%s secid=%s klt=%s",
                        payload["upstream_status"], secid, klt)
        return payload

    payload, how = _cached("fflow|%s|%d|%d" % (secid, klt, lmt), ttl, produce)
    out = dict(payload)
    out["cache"] = how
    return out


def relay_clist(fs, fields, pn, pz):
    """中继板块列表 clist：返回形状同 relay_fflow。

    fs 白名单：`m:90+t:2`（行业）/ `m:90+t:3`（概念）两档列表，以及 `b:BKxxxx`
    （板块成分股，§FLOW-SECTOR-JOIN 的日级生产者用）。
    """
    url = build_clist_url(fs, fields, pn, pz)

    def produce():
        status, body = fetch_upstream(url)
        ok = status == 200 and body.strip() not in ("", "null")
        if not ok:
            log.warning("[flow_proxy] clist 上游异常 status=%s fs=%s pn=%s", status, fs, pn)
        return {
            "ok": ok,
            "upstream_status": status,
            "upstream_body": body,
            "src": "eastmoney_clist",
            "upstream_url": url,
        }

    payload, how = _cached("clist|%s|%s|%d|%d" % (fs, ",".join(fields), pn, pz), _TTL_CLIST, produce)
    out = dict(payload)
    out["cache"] = how
    return out


def parse_body(payload):
    """把 upstream_body 解析成 dict（测试与调用方共用；解析失败返回 None）。"""
    try:
        return json.loads(payload.get("upstream_body") or "")
    except (ValueError, TypeError):
        return None


def clear_cache():
    """清空缓存（测试用；生产无调用点）。"""
    with _cache_lock:
        _cache.clear()
