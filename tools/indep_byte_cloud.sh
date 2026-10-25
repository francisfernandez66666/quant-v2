#!/usr/bin/env bash
# ── 独立字节级探针 · 现网公开发布面（2026-10-23）──
# 定位：不复用本项目任何断言函数/测试代码，纯 curl + python3 从零实现同一验证目标，
#   与 tools/pub_uat（项目自带 Playwright 套件）互为交叉验证。一条红另一条绿的分歧本身就是发现。
# 目标：https://quant-trading.top（Caddy → 127.0.0.1:8081 引擎）
# 覆盖：安全头/深链/资产缓存/压缩/APK 分发/信封矩阵/404-405 语义/setup 不暴露/版本指纹
# 退出码：FAIL>0 → exit 1（§S7 教训：缺陷数必须进退出码，不许两分支都 return 0）
set -u
BASE="${BASE:-https://quant-trading.top}"
TS="$(date +%Y%m%d%H%M%S)"
OUT="tools/indep_cloud_evidence_${TS}"
mkdir -p "$OUT"
PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf 'PASS|%s|%s\n' "$1" "$2" | tee -a "$OUT/results.txt"; }
bad(){ FAIL=$((FAIL+1)); printf 'FAIL|%s|%s\n' "$1" "$2" | tee -a "$OUT/results.txt"; }
# 取响应头（-D）与body（-o）分Files，状态码单独拿
hdr(){ curl -sS --connect-timeout 10 --max-time 30 -D "$OUT/$1.hdr" -o "$OUT/$1.body" -w '%{http_code}' "$2" "${@:3}"; }

echo "[indep-cloud] BASE=$BASE OUT=$OUT"

# ── 1. 安全头四条等值 + Server 摘除（§N-06/G1）──
code=$(hdr root "$BASE/")
[ "$code" = "200" ] && ok "sec.root-200" "http=$code" || bad "sec.root-200" "http=$code"
for kv in 'Strict-Transport-Security: max-age=31536000' 'X-Content-Type-Options: nosniff' 'Referrer-Policy: same-origin' 'X-Frame-Options: DENY'; do
  k="${kv%%:*}"; v="${kv#*: }"
  got=$(grep -i "^$k:" "$OUT/root.hdr" | head -1 | sed "s/^[^:]*: *//I" | tr -d '\r')
  [ "$got" = "$v" ] && ok "sec.hdr.$k" "exact=$v" || bad "sec.hdr.$k" "expect=[$v] got=[$got]"
done
srv=$(grep -i '^Server:' "$OUT/root.hdr" | tr -d '\r')
[ -z "$srv" ] && ok "sec.no-server-header" "absent" || bad "sec.no-server-header" "present: $srv"

# ── 2. 深链 /dashboard 200 且是入口 HTML（非 0 字节）──
code=$(hdr deeplink "$BASE/dashboard")
sz=$(wc -c < "$OUT/deeplink.body" | tr -d ' ')
if [ "$code" = "200" ] && [ "$sz" -gt 500 ] && grep -q '<div id="app">' "$OUT/deeplink.body"; then
  ok "spa.deeplink-dashboard" "http=$code bytes=$sz has-app-div"
else bad "spa.deeplink-dashboard" "http=$code bytes=$sz app-div=$(grep -c 'id="app"' "$OUT/deeplink.body")"; fi

# ── 3. 资产缓存：入口 chunk immutable；伪造哈希名 404 + no-store ──
entry_js=$(grep -o '/assets/index-[A-Za-z0-9_-]*\.js' "$OUT/root.body" | head -1)
code=$(hdr entrychunk "$BASE$entry_js")
cc=$(grep -i '^Cache-Control:' "$OUT/entrychunk.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
[ "$code" = "200" ] && echo "$cc" | grep -q 'max-age=31536000, immutable' \
  && ok "asset.entry-immutable" "$entry_js http=$code cc=$cc" \
  || bad "asset.entry-immutable" "$entry_js http=$code cc=[$cc]"
code=$(hdr fakeasset "$BASE/assets/index-deadbeefdeadbeef.js")
cc=$(grep -i '^Cache-Control:' "$OUT/fakeasset.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
sz=$(wc -c < "$OUT/fakeasset.body" | tr -d ' ')
[ "$code" = "404" ] && echo "$cc" | grep -q 'no-store' && ! echo "$cc" | grep -q immutable \
  && ok "asset.fake-404-nostore" "http=$code cc=$cc bytes=$sz" \
  || bad "asset.fake-404-nostore" "http=$code cc=[$cc] bytes=$sz"

# ── 4. 压缩生效（gzip / br）──
curl -sS --max-time 30 -H 'Accept-Encoding: gzip, br' -D "$OUT/gzip.hdr" -o /dev/null "$BASE$entry_js"
ce=$(grep -i '^Content-Encoding:' "$OUT/gzip.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
[ -n "$ce" ] && ok "compress.entry" "content-encoding=$ce" || bad "compress.entry" "no content-encoding header"

# ── 5. APK 分发三头 + Range 206 ──
code=$(curl -sS --max-time 30 -I -o "$OUT/apk.hdr" -w '%{http_code}' "$BASE/dl/quant-latest.apk")
ct=$(grep -i '^Content-Type:' "$OUT/apk.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
cd=$(grep -i '^Content-Disposition:' "$OUT/apk.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
if [ "$code" = "200" ] && echo "$ct" | grep -q 'android.package-archive' && echo "$cd" | grep -q 'attachment'; then
  ok "apk.headers" "http=$code ct=$ct cd=[$cd]"
else bad "apk.headers" "http=$code ct=[$ct] cd=[$cd]"; fi
code=$(curl -sS --max-time 30 -r 0-1023 -D "$OUT/apkrange.hdr" -o "$OUT/apkrange.body" -w '%{http_code}' "$BASE/dl/quant-latest.apk")
cr=$(grep -i '^Content-Range:' "$OUT/apkrange.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
rsz=$(wc -c < "$OUT/apkrange.body" | tr -d ' ')
[ "$code" = "206" ] && [ "$rsz" = "1024" ] && ok "apk.range" "http=$code bytes=$rsz cr=[$cr]" \
  || bad "apk.range" "http=$code bytes=$rsz cr=[$cr]"

# ── 6. favicon / robots / manifest content-type ──
for p in "/favicon.ico:image" "/favicon.svg:svg" "/robots.txt:text/plain"; do
  path="${p%%:*}"; want="${p#*:}"
  code=$(hdr "misc$(echo "$path" | tr -d '/.')" "$BASE$path")
  ct=$(grep -i '^Content-Type:' "$OUT/misc$(echo "$path" | tr -d '/.').hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
  [ "$code" = "200" ] && echo "$ct" | grep -qi "$want" && ok "misc$path" "http=$code ct=$ct" \
    || bad "misc$path" "http=$code ct=[$ct]"
done

# ── 7. /setup 公网 404（Caddy 层，不得被 SPA 兜底）──
code=$(hdr setup "$BASE/setup")
[ "$code" = "404" ] && ok "setup.public-404" "http=$code" || bad "setup.public-404" "http=$code"

# ── 8. /dl 无尾斜杠 301 → /dl/ ──
code=$(curl -sS --max-time 30 -o /dev/null -w '%{http_code}' "$BASE/dl")
loc=$(grep -i '^Location:' "$OUT/setup.hdr" 2>/dev/null) # placeholder to keep shell happy
loc=$(curl -sS --max-time 30 -D - -o /dev/null "$BASE/dl" | grep -i '^Location:' | tr -d '\r')
[ "$code" = "301" ] && echo "$loc" | grep -q '/dl/' && ok "dl.redirect" "http=$code $loc" \
  || bad "dl.redirect" "http=$code loc=[$loc]"

# ── 9. 信封矩阵：401 ×2 / 404 / 400 / 405 ──
code=$(hdr e401a "$BASE/api/status")
jbody=$(python3 -c "import json;d=json.load(open('$OUT/e401a.body'));print('error' if 'error' in d else 'NO-ERROR-FIELD')" 2>/dev/null || echo "NOT-JSON")
[ "$code" = "401" ] && [ "$jbody" = "error" ] && ok "env.401-no-token" "http=$code json-error-field" \
  || bad "env.401-no-token" "http=$code body=$jbody"
code=$(hdr e401b "$BASE/api/auth/me" -H 'Authorization: Bearer bad.token.here')
jbody=$(python3 -c "import json;d=json.load(open('$OUT/e401b.body'));print('error' if 'error' in d else 'NO-ERROR-FIELD')" 2>/dev/null || echo "NOT-JSON")
[ "$code" = "401" ] && [ "$jbody" = "error" ] && ok "env.401-bad-token" "http=$code json-error-field" \
  || bad "env.401-bad-token" "http=$code body=$jbody"
code=$(hdr e404 "$BASE/api/definitely-not-a-route-xyz")
ct=$(grep -i '^Content-Type:' "$OUT/e404.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
jbody=$(python3 -c "import json;d=json.load(open('$OUT/e404.body'));print('error' if 'error' in d else 'NO-ERROR-FIELD')" 2>/dev/null || echo "NOT-JSON")
[ "$code" = "404" ] && echo "$ct" | grep -qi 'application/json' && [ "$jbody" = "error" ] \
  && ok "env.404-json-envelope" "http=$code ct=$ct" || bad "env.404-json-envelope" "http=$code ct=[$ct] body=$jbody"
code=$(curl -sS --max-time 30 -X POST -H 'Content-Type: application/json' -d '{bad json' -D "$OUT/e400.hdr" -o "$OUT/e400.body" -w '%{http_code}' "$BASE/api/auth/login")
jbody=$(python3 -c "import json;d=json.load(open('$OUT/e400.body'));print('error' if 'error' in d else 'NO-ERROR-FIELD')" 2>/dev/null || echo "NOT-JSON")
[ "$code" = "400" ] && [ "$jbody" = "error" ] && ok "env.400-bad-json" "http=$code json-error-field" \
  || bad "env.400-bad-json" "http=$code body=$jbody"
code=$(hdr e405 "$BASE/api/auth/login")   # GET on POST-only route
ct=$(grep -i '^Content-Type:' "$OUT/e405.hdr" | sed 's/^[^:]*: *//I' | tr -d '\r')
[ "$code" = "405" ] && echo "$ct" | grep -qi 'application/json' && ok "env.405-json-envelope" "http=$code ct=$ct" \
  || bad "env.405-json-envelope" "http=$code ct=[$ct]"
code=$(hdr esse "$BASE/api/events")       # SSE 无票据
[ "$code" = "401" ] && ok "sse.no-ticket-401" "http=$code" || bad "sse.no-ticket-401" "http=$code"

# ── 10. 版本指纹：livez build == app/version 在场；login 响应形状（错密码取 401 统一文案）──
code=$(hdr livez "$BASE/livez")
b1=$(python3 -c "import json;print(json.load(open('$OUT/livez.body')).get('build',''))" 2>/dev/null || echo "")
[ "$code" = "200" ] && [ -n "$b1" ] && ok "ver.livez" "http=$code build=$b1" || bad "ver.livez" "http=$code build=[$b1]"
code=$(hdr appver "$BASE/api/app/version")
python3 - "$OUT/appver.body" <<'PY' > "$OUT/appver.keys" 2>/dev/null
import json,sys
d=json.load(open(sys.argv[1]))
print(','.join(sorted(d.keys())))
PY
keys=$(cat "$OUT/appver.keys" 2>/dev/null)
[ "$code" = "200" ] && echo "$keys" | grep -q 'latest_version_code' && echo "$keys" | grep -q 'apk_url' \
  && ok "ver.app-version" "http=$code keys=$keys" || bad "ver.app-version" "http=$code keys=[$keys]"
code=$(curl -sS --max-time 30 -X POST -H 'Content-Type: application/json' -d '{"username":"__indep_probe__","password":"__wrong__"}' -D "$OUT/loginbad.hdr" -o "$OUT/loginbad.body" -w '%{http_code}' "$BASE/api/auth/login")
msg=$(python3 -c "import json;print(json.load(open('$OUT/loginbad.body')).get('error',''))" 2>/dev/null || echo "")
[ "$code" = "401" ] && [ "$msg" = "invalid credentials" ] && ok "auth.uniform-401" "http=$code error=[$msg]（不区分用户不存在/密码错）" \
  || bad "auth.uniform-401" "http=$code error=[$msg]"

# ── 11. index.html 交付字节形态 ──
python3 - "$OUT/root.body" <<'PY'
import sys,re
h=open(sys.argv[1],encoding='utf-8',errors='replace').read()
checks=[('lang="zh-CN"', 'html lang'),('量仔', 'title brand'),('viewport-fit=cover','viewport meta'),('modulepreload','modulepreload hints')]
miss=[n for s,n in checks if s not in h]
print('MISSING:'+','.join(miss) if miss else 'ALL-PRESENT')
PY
shape=$(python3 - "$OUT/root.body" <<'PY'
import sys
h=open(sys.argv[1],encoding='utf-8',errors='replace').read()
checks=[('lang="zh-CN"',),('量仔',),('viewport-fit=cover',),('modulepreload',)]
miss=[n for (s,) in checks if s not in h]
print('MISSING:'+','.join(miss) if miss else 'ALL-PRESENT')
PY
)
[ "$shape" = "ALL-PRESENT" ] && ok "html.shape" "lang/brand/viewport/preload all present" \
  || bad "html.shape" "$shape"

# ── 汇总：退出码按 FAIL 分流（§S7）──
echo "INDEP_CLOUD_SUMMARY PASS=$PASS FAIL=$FAIL OUT=$OUT"
if [ "$FAIL" -gt 0 ]; then exit 1; fi
exit 0
