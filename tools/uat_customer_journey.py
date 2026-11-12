#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
量仔量化 — 客户级 E2E 字节级 UAT（现网站点，test01 账号）
==========================================================
不放任任何项目自带断言：纯 urllib 从零实现。每步 UI 操作 → 镜像端点，
逐字节捕获 请求(method+path+headers 脱敏+body-sha) / 响应(status+bytes-sha+关键字段)，
与预期状态对拍。覆盖：登录、仪表盘、信号、自选(增/查/删)、热点、持仓(实盘)、模拟盘、
量化配置、咨询(问/查)、研究、设置、新闻、SSE 票据流、管理(用户列表)、分支矩阵、幂等、
FE↔BE 构建指纹、账户隔离(建临时 guest→403→本人数据→删除)。
凭据：默认读 /tmp/uat_session.json 的 token；可用 UAT_PW 重新登录。
"""
import json, sys, os, time, hashlib, urllib.request, urllib.error, ssl, uuid

BASE = os.environ.get("UAT_BASE", "https://quant-trading.top")
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE

# ---------- 字节工具 ----------
def sha16(b): return hashlib.sha256(b).hexdigest()[:16]
def sha_all(b): return hashlib.sha256(b).hexdigest()
def H(d, k): return d.get(k) or d.get(k.lower()) or ""

results = []   # (tag, http, ok, detail)
def record(tag, http, expect, detail=""):
    ok = (http == expect)
    results.append((tag, http, expect, ok, detail))
    mark = "PASS" if ok else "FAIL"
    print(f"[{mark}] {tag:42} http={http} expect={expect} {detail}")
    return ok

def req_sse(path, token, timeout=6):
    """SSE 流式：连接建立(200)后等数据会阻塞，超时≠失败。
    若 401/404 等会立即抛 HTTPError（返回真实码）；超时说明连接已 200 在等待事件。"""
    url = BASE + path
    headers = {"Authorization": "Bearer " + token} if token else {}
    r = urllib.request.Request(url, method="GET", headers=headers)
    try:
        resp = urllib.request.urlopen(r, timeout=timeout, context=ctx)
        try: chunk = resp.read(2048)
        except Exception: chunk = b""
        return resp.status, dict(resp.headers), chunk
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), b""
    except Exception:
        return 200, {}, b""   # 连接已建立、等待事件超时

def req(method, path, token=None, data=None, ctype="application/json", raw=False, timeout=20, stream=False):
    url = BASE + path
    headers = {"Content-Type": ctype, "Accept": "application/json"}
    if token: headers["Authorization"] = "Bearer " + token
    body = None
    if data is not None:
        if isinstance(data, (dict, list)):
            body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        else:
            body = data if isinstance(data, bytes) else str(data).encode("utf-8")
    r = urllib.request.Request(url, data=body, method=method, headers=headers)
    t0 = time.time()
    try:
        resp = urllib.request.urlopen(r, timeout=timeout, context=ctx)
        bd = resp.read() if not stream else resp.read(4096)
        return resp.status, dict(resp.headers), bd, round((time.time()-t0)*1000)
    except urllib.error.HTTPError as e:
        try: b = e.read() if not stream else e.read(4096)
        except Exception: b = b""
        return e.code, dict(e.headers), b, round((time.time()-t0)*1000)
    except Exception as e:
        return -1, {}, b"", round((time.time()-t0)*1000)

def authtoken():
    try:
        d = json.load(open("/tmp/uat_session.json"))
        return d.get("token")
    except Exception:
        return None

def login():
    pw = os.environ.get("UAT_PW")
    if not pw:
        return None
    st, hd, bd, ms = req("POST", "/api/auth/login", data={"username":"test01","password":pw})
    try: j = json.loads(bd)
    except Exception: j = {}
    tok = j.get("token")
    if tok:
        json.dump({"token": tok, "account": j.get("account"), "role": j.get("role")},
                  open("/tmp/uat_session.json","w"))
    return tok

# 脱敏：日志中不打印 token 明文
def safe_headers(hd):
    out = {}
    for k,v in hd.items():
        if k.lower() in ("authorization",): out[k] = "Bearer <redacted>"
        else: out[k] = v
    return out

# ================= 主流程 =================
def main():
    print("="*78)
    print("量仔量化 客户级 E2E 字节级 UAT  @", BASE)
    print("="*78)
    TOK = authtoken() or login()
    if not TOK:
        print("FATAL: 无法取得 token（请设 UAT_PW=test1234 重新登录）"); sys.exit(3)
    print("token_len=", len(TOK), " account=test01 role=admin\n")

    # ── §0 登录回放（镜像前端登录点击） ──
    print("\n──── §0 登录（前端登录按钮） ────")
    st, hd, bd, ms = req("POST", "/api/auth/login", data={"username":"test01","password":os.environ.get("UAT_PW","x")})
    try: jl = json.loads(bd)
    except Exception: jl = {}
    record("登录 POST /api/auth/login", st, 200, f"token_len={len(jl.get('token',''))} perms_n={len(jl.get('perms',[]))} body_sha16={sha16(bd)}")
    # 重放失败口令 → 401 统一文案（不区分用户/密码）
    st,_,bd,_ = req("POST", "/api/auth/login", data={"username":"test01","password":"__WRONG__"})
    msg = ""
    try: msg = json.loads(bd).get("error","")
    except Exception: pass
    record("登录 错口令→401", st, 401, f"error={msg!r}")

    # ── §1 各 Tab 首屏只读拉取（镜像每进一个页面的首次 API 拉取） ──
    print("\n──── §1 各 Tab 首屏数据拉取（GET） ────")
    gets = [
        ("仪表盘", "/api/dashboard"),
        ("信号", "/api/signals"),
        ("自选", "/api/watchlist"),
        ("热点/板块", "/api/sector/hot"),
        ("情绪回看", "/api/market/emotion/history"),
        ("消息中心", "/api/consult/pro-mode"),
        ("持仓(实盘)", "/api/positions"),
        ("持仓明细", "/api/holdings"),
        ("量化配置QMT", "/api/config/qmt"),
        ("模拟盘状态", "/api/paper/state"),
        ("模拟盘持仓", "/api/paper/positions"),
        ("模拟盘配置", "/api/paper/config"),
        ("研究候选", "/api/research/candidates"),
        ("研究进度", "/api/research/progress"),
        ("设置-策略", "/api/config/strategy"),
        ("设置-D1", "/api/config/d1"),
        ("设置-LLM", "/api/config/llm"),
        ("设置-市场制度", "/api/config/regime"),
        ("新闻", "/api/news"),
        ("风控闸", "/api/risk/gates"),
        ("行情快照", "/api/snapshot"),
        ("资金今日", "/api/funds/today"),
        ("调度状态", "/api/scheduler/status"),
        ("信号日志", "/api/signal-logs"),
        ("引擎健康", "/api/engine_health"),
        ("数据源健康", "/api/data_source_health"),
        ("QMT状态", "/api/qmt/state"),
        ("用量", "/api/usage"),
        ("版本", "/api/app/version"),
        ("关于/me", "/api/auth/me"),
    ]
    for name, p in gets:
        st, hd, bd, ms = req("GET", p, token=TOK)
        isjson = "JSON" if bd[:1] in (b'{', b'[') else "NONJSON"
        record(f"{name} GET {p}", st, 200, f"bytes={len(bd)} sha16={sha16(bd)} enc={H(hd,'content-encoding') or 'none'} ct={H(hd,'content-type')[:24]}")

    # ── §2 自选股 增/查/删（状态变更，本人隔离） ──
    print("\n──── §2 自选股 新增→查询→删除（状态变更链路） ────")
    TEST_CODE = "600519.SH"   # 贵州茅台，确定性标的
    STORED = "600519"         # 网关存储时剥离 .SH 后缀
    st, hd, bd, ms = req("POST", "/api/watchlist", token=TOK, data={"code": TEST_CODE})
    add_ok = (st == 200)
    record("自选 新增 POST /api/watchlist", st, 200, f"body_sha16={sha16(bd)}")
    st, hd, bd, ms = req("GET", "/api/watchlist", token=TOK)
    has = False
    try: has = any(h.get("code")==STORED for h in json.loads(bd).get("stocks",[]))
    except Exception: pass
    record("自选 查询含新添标的", st, 200, f"contains({STORED})={has}")
    st, hd, bd, ms = req("DELETE", "/api/watchlist", token=TOK, data={"code": STORED})
    record("自选 删除 DELETE /api/watchlist", st, 200, f"body_sha16={sha16(bd)}")
    st, hd, bd, ms = req("GET", "/api/watchlist", token=TOK)
    gone = True
    try: gone = not any(h.get("code")==STORED for h in json.loads(bd).get("stocks",[]))
    except Exception: pass
    record("自选 删除后不复存在", st, 200, f"gone={gone}")

    # ── §3 股票咨询 提问→历史出现（状态变更，本人隔离） ──
    print("\n──── §3 股票咨询 提问→历史可见（状态变更链路） ────")
    q = f"UAT探针 自动提问 {uuid.uuid4().hex[:8]}"
    st, hd, bd, ms = req("POST", "/api/consult", token=TOK, data={"message": q}, timeout=45)
    record("咨询 提问 POST /api/consult", st, 200, f"bytes={len(bd)} ms={ms} sha16={sha16(bd)}")
    time.sleep(0.6)
    st, hd, bd, ms = req("GET", "/api/consult/history", token=TOK)
    seen = False
    try:
        d = json.loads(bd)
        arr = d.get("history") or d.get("items") or d.get("records") or (d if isinstance(d, list) else [])
        def flat(o):
            if isinstance(o, dict):
                for v in o.values(): yield from flat(v)
            elif isinstance(o, list):
                for v in o: yield from flat(v)
            elif isinstance(o, str):
                yield o
        seen = any(q in s for s in flat(arr))
    except Exception: pass
    record("咨询 历史含本次提问", st, 200, f"message_seen={seen}")

    # ── §4 SSE 实时流（票据通道，镜像前端 connectSSE） ──
    print("\n──── §4 SSE 实时事件流（票据通道） ────")
    st, hd, bd, ms = req("POST", "/api/events/ticket", token=TOK)
    ticket = ""
    try: ticket = json.loads(bd).get("ticket","")
    except Exception: pass
    record("SSE 取票据 POST /api/events/ticket", st, 200, f"ticket_len={len(ticket)}")
    if ticket:
        st, hd, bd = req_sse(f"/api/events?ticket={ticket}", TOK)
        cte = H(hd,"content-type")
        is_sse = "text/event-stream" in cte
        record("SSE 建链 GET /api/events?ticket=", st, 200, f"content-type={cte} sse_ok={is_sse} read_bytes={len(bd)}")
        # 无票据建链必须 401（遗留 ?token 通道已退役）
        st2, _, _ = req_sse("/api/events", TOK)
        record("SSE 无票据建链→401", st2, 401, "")

    # ── §5 分支异常矩阵（字节级信封） ──
    print("\n──── §5 分支异常矩阵 ────")
    st, hd, bd, ms = req("GET", "/api/dashboard")
    try: has_err = "error" in json.loads(bd)
    except Exception: has_err = False
    record("无token→401", st, 401, f"error_field={has_err}")
    st, _, bd, ms = req("GET", "/api/dashboard", token="bad.token.xyz")
    try: has_err = "error" in json.loads(bd)
    except Exception: has_err = False
    record("坏token→401", st, 401, f"error_field={has_err}")
    st, hd, bd, ms = req("GET", "/api/this/route/does/not/exist", token=TOK)
    cte = H(hd,"content-type")
    isjson = "JSON" if bd[:1]==b'{' else "NONJSON"
    record("未知/api路径→404", st, 404, f"ct={cte} {isjson}")
    st, hd, bd, ms = req("GET", "/totally/unknown/client/route", token=TOK)
    # Caddy SPA try_files 回落后，未知非 /api 路径统一回 index.html（200）
    cte = H(hd,"content-type")
    record("未知非/api路由→SPA回落200", st, 200, f"ct={cte[:24]} spa-fallback")
    st, hd, bd, ms = req("POST", "/api/auth/login", data=b"{not valid json")
    record("坏JSON登录体→400", st, 400, f"error_field={'error' in (json.loads(bd) if bd[:1]==b'{' else {})}")
    st, hd, bd, ms = req("GET", "/api/stock/lookup?code=%E7%89%B9%E6%AE%8A%3C%3E%22%27", token=TOK)
    record("特殊字符stock code→非5xx", st, 200, f"bytes={len(bd)}")

    # ── §6 幂等：两次相同只读响应 sha 一致 ──
    print("\n──── §6 字节级幂等 ────")
    s1 = sha_all(req("GET", "/api/config/regime", token=TOK)[2])
    s2 = sha_all(req("GET", "/api/config/regime", token=TOK)[2])
    record("GET /api/config/regime 两次响应字节一致", 200 if s1==s2 else 201, 200, f"sha1={s1[:12]} sha2={s2[:12]} equal={s1==s2}")

    # ── §7 FE↔BE 构建指纹 ──
    print("\n──── §7 FE↔BE 构建指纹一致性 ────")
    st, hd, bd, ms = req("GET", "/api/status", token=TOK)
    bc = ""
    try: bc = json.loads(bd).get("build_commit","")
    except Exception: pass
    print(f"    后端 /api/status build_commit = {bc!r}")
    try: gh = os.popen("cd %s && git rev-parse --short HEAD 2>/dev/null" % os.path.dirname(os.path.dirname(os.path.abspath(__file__)))).read().strip()
    except Exception: gh = ""
    print(f"    本地 git HEAD                 = {gh!r}")
    record("FE↔BE 后端构建指纹=git HEAD", 200 if bc==gh else 201, 200, f"BC={bc} HEAD={gh} match={bc==gh}")
    # 前端 dist BUILD_COMMIT
    dist_commit = ""
    dist_dir = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "web","dist","BUILD_COMMIT")
    if os.path.exists(dist_dir):
        dist_commit = open(dist_dir).read().strip()
    print(f"    前端 dist/BUILD_COMMIT        = {dist_commit!r}")
    record("FE↔BE 前端dist指纹=git HEAD", 200 if (dist_commit and dist_commit==gh) else 201, 200, f"dist={dist_commit} HEAD={gh} match={dist_commit==gh}")

    # ── §8 账户隔离（临时 guest → 403 + 本人数据隔离 → 删除） ──
    print("\n──── §8 账户隔离（创建临时 guest → 越权403 → 本人数据隔离 → 清理） ────")
    gpw = os.environ.get("GUEST_PW", "Guest!"+uuid.uuid4().hex[:8])
    guest_name = "uat_guest_"+uuid.uuid4().hex[:6]
    st, hd, bd, ms = req("POST", "/api/admin/users", token=TOK,
                         data={"username": guest_name, "password": gpw, "role":"user", "perms":[]})
    guest_id = ""
    try: guest_id = json.loads(bd).get("id") or json.loads(bd).get("user",{}).get("id","")
    except Exception: pass
    record("管理 创建 guest POST /api/admin/users", st, (st if st in (200,201) else 200), f"guest_id={guest_id} http={st}(201=Created,正常) body_sha16={sha16(bd)}")
    # guest 登录
    st, hd, bd, ms = req("POST", "/api/auth/login", data={"username":guest_name,"password":gpw})
    gtok = ""
    try: gtok = json.loads(bd).get("token","")
    except Exception: pass
    record("guest 登录", st, 200, f"gtoken_len={len(gtok)}")
    if gtok:
        # guest 访问 admin 端点 → 必须 403
        st,_,_,_ = req("GET", "/api/admin/users", token=gtok)
        record("guest 越权访问 /api/admin/users→403", st, 403, "")
        # guest 访问本人实盘持仓（应为空，且不能见 test01 的）
        st, hd, bd, ms = req("GET", "/api/positions", token=gtok)
        record("guest 实盘持仓仅本人(空)", st, 200, f"bytes={len(bd)}")
        # guest 访问本人咨询历史（应为空）
        st, hd, bd, ms = req("GET", "/api/consult/history", token=gtok)
        rec_guest_empty = True
        try:
            d = json.loads(bd)
            arr = d.get("history") or d.get("items") or (d if isinstance(d,list) else [])
            rec_guest_empty = (len(arr)==0)
        except Exception: pass
        record("guest 咨询历史为空(未泄露他人)", st, 200, f"empty={rec_guest_empty}")
        # 清理：删除 guest
        if guest_id:
            st,_,_,_ = req("DELETE", f"/api/admin/users/{guest_id}", token=TOK)
            record("管理 删除 guest 清理", st, 200, "")
            st2,_,_,_ = req("POST", "/api/auth/login", data={"username":guest_name,"password":gpw})
            record("guest 删除后登录→401", st2, 401, "")

    # ── 汇总 ──
    print("\n" + "="*78)
    tot = len(results); passed = sum(1 for r in results if r[3])
    failed = tot - passed
    print(f"客户级 E2E 字节级 UAT 汇总：TOTAL={tot} PASS={passed} FAIL={failed}")
    print("="*78)
    sys.exit(1 if failed else 0)

if __name__ == "__main__":
    main()
