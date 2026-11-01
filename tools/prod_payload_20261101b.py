#!/usr/bin/env python3
# §INDEP-CLOUD-20261101b 现网受保护面「压缩对照」字节账
#
# 与 20261101 版的差别（这是本轮唯一目的）：
#   每条端点测**两遍**——① 不发 Accept-Encoding（= 旧脚本口径）② 发 Accept-Encoding: gzip。
#   旧脚本只有 ①，把「客户端没要求压缩」误读成「服务端没开压缩」（P0-C 假阳性）。
#
# 纪律：
#   · 只发 GET，**不调用任何写面**（不 buy/sell/reset/adjust），避免污染现网账本；
#   · 凭据只从 env 进，不打印口令、不打印 token；
#   · 输出同时给「未压缩字节」「压缩后传输字节」「服务端耗时」，三者分开，不再混为一谈。
import os, sys, json, time, gzip, hashlib, ssl, urllib.request, urllib.error

BASE = os.environ.get("PUB_BASE_URL", "https://quant-trading.top").rstrip("/")
USER = os.environ.get("PUB_USER", "")
PASS = os.environ.get("PUB_PASS", "")
OUT = os.environ.get("INDEP_OUT", "tools/pub_uat/prod_payload_20261101b.log")

ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
L = []


def log(s):
    L.append(str(s))
    print(s)


def call(method, path, headers=None, data=None, timeout=120):
    """返回 (status, headers_dict, raw_body_bytes, elapsed_ms, wire_bytes)。

    wire_bytes 在 gzip 分支下是**压缩后的实际传输量**；未压缩分支等于 len(body)。
    """
    url = BASE + path
    r = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    t0 = time.time()
    try:
        resp = urllib.request.urlopen(r, timeout=timeout, context=ctx)
        raw = resp.read()
        ms = round((time.time() - t0) * 1000)
        hd = dict(resp.headers)
        enc = (hd.get("Content-Encoding") or "").lower()
        wire = len(raw)
        if enc == "gzip":
            try:
                body = gzip.decompress(raw)
            except Exception:
                body = raw
        else:
            body = raw
        return resp.status, hd, body, ms, wire
    except urllib.error.HTTPError as e:
        try:
            raw = e.read()
        except Exception:
            raw = b""
        ms = round((time.time() - t0) * 1000)
        return e.code, dict(e.headers), raw, ms, len(raw)
    except Exception as e:
        return -1, {}, str(e).encode(), 0, 0


def sha16(b):
    return hashlib.sha256(b).hexdigest()[:16]


# ── 0. 登录 ────────────────────────────────────────────────────────────
log("=" * 88)
log(f"现网压缩对照字节账  |  目标={BASE}  |  账号={USER}  |  {time.strftime('%Y-%m-%d %H:%M:%S')}")
log("=" * 88)

body = json.dumps({"username": USER, "password": PASS}).encode()
st, hd, bd, ms, wire = call(
    "POST", "/api/auth/login",
    headers={"Content-Type": "application/json", "Accept-Encoding": "gzip"},
    data=body,
)
log(f"\n登录 -> HTTP {st}  ({ms} ms)")
if st != 200:
    log(f"  登录失败，body={bd[:200]!r}")
    log("  ⇒ 后续受保护面无法取证，退出。")
    open(OUT, "w").write("\n".join(L) + "\n")
    sys.exit(2)

try:
    lj = json.loads(bd)
except Exception:
    log("  登录响应非 JSON，退出")
    sys.exit(2)

TOKEN = lj.get("token") or ""
PERMS = lj.get("perms") or []
ROLE = lj.get("role") or ""
EXPIRES = lj.get("expires_at") or ""
log(f"  role={ROLE}  perms={PERMS}  expires_at={EXPIRES}")
log(f"  token 长度={len(TOKEN)}（值不打印）")
if not TOKEN:
    log("  未取到 token，退出")
    sys.exit(2)

AUTH = {"Authorization": "Bearer " + TOKEN}
AUTH_GZ = dict(AUTH)
AUTH_GZ["Accept-Encoding"] = "gzip"


# ── 1. 静态产物：压缩对照（决定 P0-C 真伪）────────────────────────────
log("\n── 1. 静态产物：压缩对照（P0-C 判据）──")
log(f"{'资产':<34}{'未压缩':>10}{'gzip传输':>11}{'压缩比':>8}{'enc(无头)':>11}{'enc(带头)':>11}")
st0, h0, b0, _, _ = call("GET", "/")
js = []
import re as _re
for m in _re.findall(rb'/assets/[^"\'<>]+\.js', b0):
    u = m.decode()
    if u not in js:
        js.append(u)
total_raw = total_gz = 0
for u in js[:6]:
    _, h1, b1, _, w1 = call("GET", u)                       # 不带头
    _, h2, b2, _, w2 = call("GET", u, headers={"Accept-Encoding": "gzip"})  # 带头
    e1 = (h1.get("Content-Encoding") or "none")
    e2 = (h2.get("Content-Encoding") or "none")
    ratio = (w2 / len(b1) * 100) if b1 else 0
    total_raw += len(b1)
    total_gz += w2
    log(f"{u.split('/')[-1]:<34}{len(b1):>10,}{w2:>11,}{ratio:>7.1f}%{e1:>11}{e2:>11}")
log(f"{'首屏合计':<34}{total_raw:>10,}{total_gz:>11,}"
    f"{(total_gz/total_raw*100) if total_raw else 0:>7.1f}%")

# Cache-Control 现状
_, hjs, _, _, _ = call("GET", js[0] if js else "/")
log(f"  /assets Cache-Control = {hjs.get('Cache-Control') or '(无)'}")
log(f"  /  (index.html) Cache-Control = {h0.get('Cache-Control') or '(无)'}")

# ── 2. API 面：压缩对照 + 字节 + 耗时 ────────────────────────────────
EPS = [
    ("/api/auth/me", True),
    ("/api/dashboard", True),
    ("/api/signals", True),
    ("/api/status", True),
    ("/api/positions", True),
    ("/api/holdings", True),
    ("/api/watchlist", True),
    ("/api/sector/hot", True),
    ("/api/alerts", True),
    ("/api/news/showall", True),
    ("/api/paper/state", True),
    ("/api/config/regime", True),
    ("/api/consult/pro-mode", True),
    ("/api/engine_health", True),
    ("/api/data_source_health", True),
    ("/api/scheduler/status", True),
    ("/api/market/emotion/history", True),
    ("/api/consult/history", True),
    ("/api/app/version", True),
    ("/api/risk/gates", True),
    ("/api/qmt/state", True),
    ("/api/funds/today", True),
    ("/api/usage", True),
    ("/api/qmt/orders", True),
    ("/api/qmt/trades", True),
]

log("\n── 2. API 面：未压缩 vs gzip 传输（同一会话、连续请求）──")
log(f"{'端点':<34}{'HTTP':>5}{'未压缩':>12}{'gzip':>11}{'压缩比':>8}{'ms(无头)':>10}{'ms(带头)':>10}")
api_rows = []
for path, _ in EPS:
    s1, h1, b1, m1, _ = call("GET", path, headers=AUTH)
    s2, h2, b2, m2, w2 = call("GET", path, headers=AUTH_GZ)
    ratio = (w2 / len(b1) * 100) if b1 else 0
    api_rows.append((path, s1, len(b1), w2, ratio, m1, m2, b1))
    log(f"{path:<34}{s1:>5}{len(b1):>12,}{w2:>11,}{ratio:>7.1f}%{m1:>10}{m2:>10}")

# ── 3. signal-logs 四档（NEW-1 量级复核）─────────────────────────────
log("\n── 3. /api/signal-logs 四档：量级复核（NEW-1）──")
log(f"{'参数':<24}{'HTTP':>5}{'未压缩':>13}{'gzip':>12}{'压缩比':>8}{'ms(无头)':>11}{'ms(带头)':>11}{'批次数':>8}")
for d in (1, 2, 5, 30):
    p = f"/api/signal-logs?days={d}"
    s1, h1, b1, m1, _ = call("GET", p, headers=AUTH)
    s2, h2, b2, m2, w2 = call("GET", p, headers=AUTH_GZ)
    ratio = (w2 / len(b1) * 100) if b1 else 0
    nb = ""
    try:
        arr = json.loads(b2)
        if isinstance(arr, list):
            nb = str(len(arr))
    except Exception:
        pass
    log(f"days={d:<20}{s1:>5}{len(b1):>13,}{w2:>12,}{ratio:>7.1f}%{m1:>11}{m2:>11}{nb:>8}")

# ── 4. alerts 分页头（NEW-2 判据）────────────────────────────────────
log("\n── 4. /api/alerts 分页头（NEW-2 判据）──")
for p in ("/api/alerts", "/api/alerts?page=1&page_size=50"):
    s, h, b, m, w = call("GET", p, headers=AUTH_GZ)
    n = 0
    try:
        arr = json.loads(b)
        n = len(arr) if isinstance(arr, list) else -1
    except Exception:
        pass
    log(f"{p:<40} HTTP {s}  bytes={len(b):>9,}  gzip={w:>8,}  ms={m:>5}  数组条数={n}")
    log(f"    X-Total-Count={h.get('X-Total-Count') or '(无)'}  X-Page={h.get('X-Page') or '(无)'}  "
        f"X-Page-Size={h.get('X-Page-Size') or '(无)'}  X-Has-More={h.get('X-Has-More') or '(无)'}")

# ── 5. 幂等复核（同一端点两次摘要）───────────────────────────────────
log("\n── 5. 幂等复核（同会话两次）──")
for p in ("/api/dashboard", "/api/auth/me", "/api/holdings"):
    _, _, x1, _, _ = call("GET", p, headers=AUTH_GZ)
    _, _, x2, _, _ = call("GET", p, headers=AUTH_GZ)
    log(f"{p:<24} sha16 {sha16(x1)} vs {sha16(x2)}  "
        f"{'IDEMPOTENT_OK' if sha16(x1) == sha16(x2) else 'DRIFT'}")

open(OUT, "w").write("\n".join(L) + "\n")
log(f"\n日志已写 {OUT}")
