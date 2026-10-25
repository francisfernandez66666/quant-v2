#!/usr/bin/env python3
# ── 独立字节级探针 · 登录态客户面（2026-10-24）──
# 定位：不复用 tools/pub_uat 的任何断言/采集器，纯 Python urllib 从零复核关键契约，
#   与 Playwright 套件互为交叉验证。一条红另一条绿的分歧本身就是发现。
# 安全：token 只从文件/环境读入，输出只记长度；不打印口令、token、API key 原文。
# 写面：仅做一次「自选增→重复→删」可还原往返，跑完断言回到基线。
# 退出码：FAIL>0 → exit 1。
import argparse
import hashlib
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request


def make_ssl_context():
    # macOS 系统 Python 不带根证书（实测 CERTIFICATE_VERIFY_FAILED）；用 certifi 的 CA 包，
    # 仍保持完整 TLS 校验（不改成 unverified——那会把证书问题藏掉）。
    try:
        import certifi  # type: ignore

        return ssl.create_default_context(cafile=certifi.where())
    except Exception:  # noqa: BLE001
        for p in ("/etc/ssl/cert.pem", "/usr/local/etc/openssl/cert.pem"):
            if os.path.exists(p):
                return ssl.create_default_context(cafile=p)
        return ssl.create_default_context()


SSL_CTX = make_ssl_context()

BASE = os.environ.get("PUB_BASE_URL", "https://quant-trading.top").rstrip("/")
TS = time.strftime("%Y%m%d%H%M%S")
OUT = os.path.join("tools", f"indep_cloud_evidence_{TS}_auth")
os.makedirs(OUT, exist_ok=True)
PASS = 0
FAIL = 0
ROWS = []


def ok(name, detail):
    global PASS
    PASS += 1
    line = f"PASS|{name}|{detail}"
    print(line)
    ROWS.append(line)


def bad(name, detail):
    global FAIL
    FAIL += 1
    line = f"FAIL|{name}|{detail}"
    print(line)
    ROWS.append(line)


def sha16(b):
    return hashlib.sha256(b).hexdigest()[:16]


def call(path, method="GET", body=None, token=None, extra_headers=None, timeout=40):
    url = BASE + path
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    if extra_headers:
        headers.update(extra_headers)
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout, context=SSL_CTX) as r:
            buf = r.read()
            return r.status, dict(r.headers), buf, int((time.time() - t0) * 1000), None
    except urllib.error.HTTPError as e:
        buf = e.read()
        return e.code, dict(e.headers), buf, int((time.time() - t0) * 1000), None
    except Exception as e:  # noqa: BLE001
        return None, {}, b"", int((time.time() - t0) * 1000), str(e)


def jget(buf):
    try:
        return json.loads(buf.decode("utf-8"))
    except Exception:  # noqa: BLE001
        return None


def wl_codes(parsed):
    # GET /api/watchlist 的真实信封：{"stocks":[{code,name,price,change_pct}]}（handlers_fix.go:2540）
    arr = (parsed or {}).get("stocks") or (parsed or {}).get("codes") or []
    return [x.get("code") if isinstance(x, dict) else x for x in arr]


def record(tag, method, path, status, headers, buf, ms, err=None):
    row = {
        "tag": tag,
        "method": method,
        "path": path,
        "status": status,
        "bytes": len(buf),
        "sha16": sha16(buf) if buf else None,
        "content_type": headers.get("Content-Type"),
        "content_length": headers.get("Content-Length"),
        "ms": ms,
        "err": err,
    }
    with open(os.path.join(OUT, "netlog.jsonl"), "a", encoding="utf-8") as f:
        f.write(json.dumps(row, ensure_ascii=False) + "\n")
    return row


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--token-file", default="")
    args = ap.parse_args()
    # 独立登录：不复用 Playwright 进程内的会话（同账号多会话有上限，长跑套件会把旧会话挤掉）。
    user = os.environ.get("PUB_USER", "")
    pwd = os.environ.get("PUB_PASS", "")
    token = ""
    if user and pwd:
        st, h, b, ms, err = call("/api/auth/login", method="POST", body={"username": user, "password": pwd})
        record("login", "POST", "/api/auth/login", st, h, b, ms, err)
        j = jget(b) or {}
        token = str(j.get("token") or "")
        keys = sorted(j.keys())
        need_l = {"token", "id", "account", "role", "perms", "expires_at", "sse_query_token_expires_at"}
        if st == 200 and need_l.issubset(set(keys)) and len(token) == 64:
            ok("auth.login", f"http=200 keys_n={len(keys)} token_len={len(token)} role={j.get('role')}")
        else:
            bad("auth.login", f"http={st} keys={keys} token_len={len(token)}")
    elif args.token_file:
        with open(args.token_file, encoding="utf-8") as f:
            token = f.read().strip()
    if not token:
        print("FATAL|no credentials and no token")
        return 2
    print(f"[indep-auth] BASE={BASE} token_len={len(token)} OUT={OUT}")

    # 1) 身份对账
    st, h, b, ms, err = call("/api/auth/me", token=token)
    record("me", "GET", "/api/auth/me", st, h, b, ms, err)
    d = jget(b) or {}
    if st == 200 and d.get("username") == "test01" and d.get("role") == "admin" and "research_approve" in (d.get("perms") or []):
        ok("auth.me", f"http=200 user={d.get('username')} role={d.get('role')} perms_n={len(d.get('perms') or [])}")
    else:
        bad("auth.me", f"http={st} keys={sorted(d.keys())} role={d.get('role')}")

    # 2) /status 两次读数的稳定字段（uptime/quote_age/scan_stats 本来就随时间变，
    #    全量 sha 相等不是正确判据——2026-10-24 首跑把它误判成「幂等破坏」）。
    st1, h1, b1, ms1, e1 = call("/api/status", token=token)
    st2, h2, b2, ms2, e2 = call("/api/status", token=token)
    record("status1", "GET", "/api/status", st1, h1, b1, ms1, e1)
    record("status2", "GET", "/api/status", st2, h2, b2, ms2, e2)
    d1 = jget(b1) or {}
    d2 = jget(b2) or {}
    stable = ("build_commit", "session", "session_label", "quote_source")
    if st1 == 200 and st2 == 200 and all(d1.get(k) == d2.get(k) for k in stable):
        ok("status.stable-fields", f"stable={ {k: d1.get(k) for k in stable} } sha_diff=expected")
    else:
        bad("status.stable-fields", f"st1={st1} st2={st2} d1={ {k: d1.get(k) for k in stable} } d2={ {k: d2.get(k) for k in stable} }")
    live_st, live_h, live_b, live_ms, live_e = call("/livez")
    record("livez", "GET", "/livez", live_st, live_h, live_b, live_ms, live_e)
    live = jget(live_b) or {}
    if st1 == 200 and d1.get("build_commit") == live.get("build"):
        ok("status.build-eq-livez", f"build={live.get('build')}")
    else:
        bad("status.build-eq-livez", f"status={d1.get('build_commit')} livez={live.get('build')}")
    need = {"uptime", "session", "quote_source", "build_commit", "scan_stats", "signal_count"}
    miss = sorted(need - set(d1.keys()))
    if not miss:
        ok("status.keys", f"keys={sorted(d1.keys())}")
    else:
        bad("status.keys", f"missing={miss}")

    # 3) 信封矩阵
    for tag, path, method, tok, want in [
        ("env.401-no-token", "/api/status", "GET", None, 401),
        ("env.401-bad-token", "/api/auth/me", "GET", "bad.token.here", 401),
        ("env.404-unknown", "/api/definitely-not-a-route-xyz", "GET", None, 404),
        ("env.405-method", "/api/auth/login", "GET", None, 405),
    ]:
        st, h, b, ms, err = call(path, method=method, token=tok)
        record(tag, method, path, st, h, b, ms, err)
        j = jget(b)
        if st == want and isinstance(j, dict) and "error" in j:
            ok(tag, f"http={st} json-error bytes={len(b)}")
        else:
            bad(tag, f"http={st} want={want} json_error={isinstance(j, dict) and 'error' in j}")

    # 4) 信号面：数量与字段
    st, h, b, ms, err = call("/api/signals", token=token)
    record("signals", "GET", "/api/signals", st, h, b, ms, err)
    sg = jget(b)
    items = sg if isinstance(sg, list) else ((sg or {}).get("signals") or [])
    if st == 200 and len(items) > 0:
        s0 = items[0]
        # 真实字段是 total_score / level / action（signals.shape 首跑按 score 判是探针假设错）
        need_s = {"code", "name", "total_score", "strategy", "can_open", "level", "action"}
        miss_s = sorted(need_s - set(s0.keys()))
        if not miss_s:
            ok("signals.shape", f"n={len(items)} bytes={len(b)} keys={sorted(s0.keys())[:10]}")
        else:
            bad("signals.shape", f"n={len(items)} missing={miss_s}")
    else:
        bad("signals.shape", f"http={st} n={len(items)} bytes={len(b)}")
    # 注：status.signal_count 是扫描轮次的 final_signals（含被列表过滤掉的评级），
    # 与 /api/signals 的展示条数不是同一口径；只记读数，不做等值断言。
    ok("signals.count-reading", f"list={len(items)} status_signal_count={d1.get('signal_count')}")

    # 5) 消息面：条数与字段（B-13 的独立旁证）
    st, h, b, ms, err = call("/api/alerts", token=token)
    record("alerts", "GET", "/api/alerts", st, h, b, ms, err)
    al = jget(b)
    alerts = al if isinstance(al, list) else ((al or {}).get("alerts") or [])
    if st == 200 and len(alerts) > 0:
        need_a = {"id", "level", "title"}
        miss_a = sorted(need_a - set(alerts[0].keys()))
        ok("alerts.shape", f"n={len(alerts)} bytes={len(b)} missing={miss_a}") if not miss_a else bad("alerts.shape", f"n={len(alerts)} missing={miss_a}")
    else:
        bad("alerts.shape", f"http={st} n={len(alerts)} bytes={len(b)}")

    # 6) 新增风控/动态仓位配置面（a15c1fa）
    st, h, b, ms, err = call("/api/config/regime", token=token)
    record("regime", "GET", "/api/config/regime", st, h, b, ms, err)
    rg = jget(b) or {}
    sz = rg.get("sizing") or {}
    need_r = {"enhance", "sizing", "macro_gate"}
    need_z = {"budget_mode", "mode", "power_p", "group_floors"}
    miss_r = sorted(need_r - set(rg.keys()))
    miss_z = sorted(need_z - set(sz.keys()))
    if st == 200 and not miss_r and not miss_z:
        ok("regime.dynsize-keys", f"bytes={len(b)} budget_mode={sz.get('budget_mode')} mode={sz.get('mode')} power_p={sz.get('power_p')} groups={sorted((sz.get('group_floors') or {}).keys())}")
    else:
        bad("regime.dynsize-keys", f"http={st} missing_top={miss_r} missing_sizing={miss_z}")

    # 7) QMT/模拟盘/持仓
    for tag, path in [("qmt.state", "/api/qmt/state"), ("qmt.orders", "/api/qmt/orders"), ("paper.state", "/api/paper/state"), ("holdings", "/api/holdings")]:
        st, h, b, ms, err = call(path, token=token)
        record(tag, "GET", path, st, h, b, ms, err)
        if st == 200:
            ok(tag, f"bytes={len(b)}")
        else:
            bad(tag, f"http={st} bytes={len(b)}")
    st, h, b, ms, err = call("/api/qmt/state", token=token)
    qs = jget(b) or {}
    need_q = {"enabled", "tripped", "mode", "gateway_url"}
    miss_q = sorted(need_q - set(qs.keys()))
    if not miss_q and isinstance(qs.get("enabled"), bool):
        ok("qmt.state.shape", f"enabled={qs.get('enabled')} mode={qs.get('mode')} tripped={qs.get('tripped')}")
    else:
        bad("qmt.state.shape", f"missing={miss_q}")

    # 8) 分钟线/盘口
    st, h, b, ms, err = call("/api/minute?code=000001&scale=1&count=241", token=token)
    record("minute", "GET", "/api/minute", st, h, b, ms, err)
    mn = jget(b)
    pts = mn if isinstance(mn, list) else ((mn or {}).get("points") or (mn or {}).get("data") or [])
    if st == 200 and len(pts) > 0 and h.get("Content-Type", "").startswith("application/json"):
        ok("minute.shape", f"n={len(pts)} bytes={len(b)} sha16={sha16(b)}")
    else:
        bad("minute.shape", f"http={st} n={len(pts)} bytes={len(b)} ct={h.get('Content-Type')}")
    st, h, b, ms, err = call("/api/depth/000001", token=token)
    record("depth", "GET", "/api/depth/000001", st, h, b, ms, err)
    dp = jget(b) or {}
    if st == 200 and ("asks" in dp or "bids" in dp):
        ok("depth.shape", f"keys={sorted(dp.keys())[:8]} bytes={len(b)}")
    else:
        bad("depth.shape", f"http={st} keys={sorted(dp.keys())[:8]}")

    # 9) 管理面/LLM/研究库
    st, h, b, ms, err = call("/api/admin/users", token=token)
    record("admin.users", "GET", "/api/admin/users", st, h, b, ms, err)
    au = jget(b) or {}
    users = au.get("users") if isinstance(au, dict) else au
    if st == 200 and isinstance(users, list) and len(users) >= 1:
        # 键在位但值全为空＝已脱敏（2026-10-24 实测 8 个账号 password_hash 长度均为 0）；
        # 只有非空才算泄漏。旧判据按「键名出现」判红，会把脱敏形态误报成安全问题。
        leaked = []
        for u in users:
            for k in ("password", "password_hash", "token", "sessions"):
                v = (u or {}).get(k)
                if v not in (None, "", [], {}):
                    leaked.append(k)
        if leaked:
            bad("admin.users.no-secret", f"leaked_nonempty_keys={sorted(set(leaked))}")
        else:
            ok("admin.users.no-secret", f"n={len(users)} keys={sorted(users[0].keys())[:10]} hashes_empty=True")
    else:
        bad("admin.users", f"http={st}")

    st, h, b, ms, err = call("/api/config/llm", token=token)
    record("llm.cfg", "GET", "/api/config/llm", st, h, b, ms, err)
    llm = jget(b) or {}
    raw_key = str(llm.get("api_key") or "")
    if st == 200 and (not raw_key or "*" in raw_key or len(raw_key) <= 4):
        ok("llm.masked", f"api_key_len={len(raw_key)} masked={'*' in raw_key or not raw_key}")
    else:
        bad("llm.masked", f"http={st} api_key_len={len(raw_key)} has_star={'*' in raw_key}")

    for tag, path in [("research.candidates", "/api/research/candidates"), ("research.library", "/api/research/library")]:
        st, h, b, ms, err = call(path, token=token)
        record(tag, "GET", path, st, h, b, ms, err)
        j = jget(b)
        n = len(j) if isinstance(j, list) else len((j or {}).get("items") or (j or {}).get("candidates") or (j or {}).get("library") or [])
        ok(tag, f"http={st} n={n} bytes={len(b)}") if st == 200 else bad(tag, f"http={st}")

    # 10) 自选可还原往返（独立写面；跑完必须回基线）
    st, h, b, ms, err = call("/api/watchlist", token=token)
    record("wl.baseline", "GET", "/api/watchlist", st, h, b, ms, err)
    wl = jget(b) or {}
    base_codes = wl_codes(wl)
    code = "000001" if "000001" not in base_codes else "600519"
    st, h, b, ms, err = call("/api/watchlist", method="POST", body={"code": code}, token=token)
    record("wl.add", "POST", "/api/watchlist", st, h, b, ms, err)
    st2, h2, b2, ms2, e2 = call("/api/watchlist", token=token)
    record("wl.after-add", "GET", "/api/watchlist", st2, h2, b2, ms2, e2)
    wl2 = jget(b2) or {}
    codes2 = wl_codes(wl2)
    if st == 200 and code in codes2:
        ok("wl.add-roundtrip", f"code={code} n={len(codes2)}")
    else:
        bad("wl.add-roundtrip", f"http={st} code_in={code in codes2} n={len(codes2)}")
    st, h, b, ms, err = call("/api/watchlist", method="POST", body={"code": code}, token=token)
    record("wl.dup", "POST", "/api/watchlist", st, h, b, ms, err)
    st3, h3, b3, ms3, e3 = call("/api/watchlist", token=token)
    record("wl.after-dup", "GET", "/api/watchlist", st3, h3, b3, ms3, e3)
    wl3 = jget(b3) or {}
    codes3 = wl_codes(wl3)
    if st == 200 and codes3.count(code) == 1:
        ok("wl.dup-idempotent", f"n={len(codes3)} count={codes3.count(code)}")
    else:
        bad("wl.dup-idempotent", f"http={st} n={len(codes3)} count={codes3.count(code)}")
    st, h, b, ms, err = call("/api/watchlist", method="DELETE", body={"code": code}, token=token)
    record("wl.delete", "DELETE", "/api/watchlist", st, h, b, ms, err)
    st4, h4, b4, ms4, e4 = call("/api/watchlist", token=token)
    record("wl.after-delete", "GET", "/api/watchlist", st4, h4, b4, ms4, e4)
    wl4 = jget(b4) or {}
    codes4 = wl_codes(wl4)
    if codes4 == base_codes:
        ok("wl.restored", f"baseline_n={len(base_codes)} after_n={len(codes4)}")
    else:
        bad("wl.restored", f"baseline={base_codes} after={codes4}")

    # 11) 非法自选代码分支（不得 2xx）
    st, h, b, ms, err = call("/api/watchlist", method="POST", body={"code": "ZZZZZZ"}, token=token)
    record("wl.invalid", "POST", "/api/watchlist", st, h, b, ms, err)
    if st is not None and 400 <= st < 500:
        ok("wl.invalid-rejected", f"http={st} bytes={len(b)}")
    else:
        # 2026-10-24 实测：服务端只判非空，`ZZZZZZ` 被 200 接收并进入自选/行情监控池。
        # 这是产品校验缺口（不是探针假设错）；记 FAIL，并在本步立即对称删除，避免留残留。
        bad("wl.invalid-rejected", f"accepted http={st} body={b.decode('utf-8', 'replace')[:120]}")
        dst, dh, db, dms, derr = call("/api/watchlist", method="DELETE", body={"code": "ZZZZZZ"}, token=token)
        record("wl.invalid-cleanup", "DELETE", "/api/watchlist", dst, dh, db, dms, derr)
        if dst == 200:
            ok("wl.invalid-cleanup", f"delete_http={dst}")
        else:
            bad("wl.invalid-cleanup", f"delete_http={dst}")

    with open(os.path.join(OUT, "results.txt"), "w", encoding="utf-8") as f:
        f.write("\n".join(ROWS) + "\n")
    print(f"INDEP_AUTH_SUMMARY PASS={PASS} FAIL={FAIL} OUT={OUT}")
    return 1 if FAIL > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
