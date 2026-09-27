#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# 独立字节级 UAT — 不复用项目任何断言函数，纯裸 curl + 字节指纹验证。
# 验证对象：running UAT 栈（quant @18080 / vite @5173 / qmt-mock @18789）。
# 三条链路：BE↔BE(特殊字符双向字节往返 + 幂等 sha) / FE↔BE(构建指纹对拍) / 分支异常(401/403/404/400/信封)
# 用法：bash tools/indep_byte_uat.sh [BASE_URL] [FRONT_URL]
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail
export PATH="/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin"
BASE="${1:-http://127.0.0.1:18080}"
FRONT="${2:-http://127.0.0.1:5173}"
DATA_DIR="${QUANT_DATA_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.uat-data}"
PASS=0; FAIL=0
jget(){ /Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin/python3 -c "import sys,json
try:
    d=json.load(sys.stdin); v=eval(sys.argv[1],{'d':d}); print('' if v is None else v)
except Exception: print('')" "$1"; }
sha(){ printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
# 统一错误信封字段 = error（实测 /api/status 401/404/400 均返回 {"error":"..."}）
err_of(){ printf '%s' "$1" | jget "d.get('error') if isinstance(d,dict) else None"; }
is_json(){ printf '%s' "$1" | /Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin/python3 -c "import sys,json;sys.exit(0 if json.loads(sys.stdin.read()) is not None else 1)" 2>/dev/null; }
chk(){ if [ "$2" = "$3" ]; then echo "  PASS  $1"; PASS=$((PASS+1)); else echo "  FAIL  $1  (expect=$3 got=$2)"; FAIL=$((FAIL+1)); fi; }
rawchk(){ if [ -n "$2" ]; then echo "  PASS  $1"; PASS=$((PASS+1)); else echo "  FAIL  $1  (empty)"; FAIL=$((FAIL+1)); fi; }

echo "########## 0. 栈可达性 ##########"
hc=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$BASE/setup"); chk "GET /setup 可达(200/405均算活)" "${hc:-000}" "200"
[ "${hc:-000}" = "200" ] || [ "${hc:-000}" = "405" ] || { echo "栈未就绪，退出"; exit 1; }

echo "########## 1. 获取令牌（独立路径，不读项目测试夹具） ##########"
ADMIN_TOKEN="$(cat "$DATA_DIR/admin.token" 2>/dev/null)"
[ -z "$ADMIN_TOKEN" ] && ADMIN_TOKEN=$(curl -s -X POST "$BASE/setup" -H 'Content-Type: application/json' -d '{"username":"admin","password":"Nightly!2026"}' | jget "d['token']")
rawchk "admin token 取得" "$ADMIN_TOKEN"
AH="Authorization: Bearer $ADMIN_TOKEN"
# tester token：走登录端点（authMiddleware）
TESTER_TOKEN=$(curl -s -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' -d '{"username":"tester","password":"Tester!2026"}' | jget "d['token']")
rawchk "tester token 取得" "$TESTER_TOKEN"

echo "########## 2. 统一错误信封（分支矩阵，字节级） ##########"
R=$(curl -s -w '\n%{http_code}' "$BASE/api/status"); C=$(printf '%s' "$R" | tail -1); BODY=$(printf '%s' "$R" | sed '$d')
chk "无 token → 401" "$C" "401"; rawchk "401 响应为合法 JSON" "$(is_json "$BODY" && echo x)"; rawchk "401 信封含 error 字段" "$(err_of "$BODY")"
R=$(curl -s -w '\n%{http_code}' -H 'Authorization: Bearer bad.token.here' "$BASE/api/status"); C=$(printf '%s' "$R" | tail -1); BODY=$(printf '%s' "$R" | sed '$d')
chk "坏 token → 401" "$C" "401"
R=$(curl -s -w '\n%{http_code}' -H "$AH" "$BASE/api/this/does/not/exist"); C=$(printf '%s' "$R" | tail -1); BODY=$(printf '%s' "$R" | sed '$d')
CT=$(curl -s -D - -o /dev/null -H "$AH" "$BASE/api/this/does/not/exist" | /usr/bin/grep -i '^content-type' | tr -d '\r')
chk "未知 /api 路径 → 404" "$C" "404"
if printf '%s' "$CT" | /usr/bin/grep -qi 'application/json'; then echo "  PASS  404 走统一 JSON 信封（非 text/plain）"; PASS=$((PASS+1)); else echo "  FAIL  404 未走 JSON 信封 ($CT)"; FAIL=$((FAIL+1)); fi
rawchk "404 信封含 error 字段" "$(err_of "$BODY")"
R=$(curl -s -w '\n%{http_code}' -X POST -H "$AH" -H 'Content-Type: application/json' -d '{bad json' "$BASE/api/holdings"); C=$(printf '%s' "$R" | tail -1); BODY=$(printf '%s' "$R" | sed '$d')
chk "非法 JSON body → 400" "$C" "400"; rawchk "400 信封含 error 字段" "$(err_of "$BODY")"
# 403：tester（user 角色）访问 admin 端点
R=$(curl -s -w '\n%{http_code}' -H "Authorization: Bearer $TESTER_TOKEN" "$BASE/api/admin/users"); C=$(printf '%s' "$R" | tail -1); BODY=$(printf '%s' "$R" | sed '$d')
chk "tester 访问 admin 端点 → 403" "$C" "403"; rawchk "403 信封含 error 字段" "$(err_of "$BODY")"

echo "########## 3. BE↔BE 特殊字符字节往返（唯一 code 防撞种子） ##########"
TS="$(date +%s)"; CODE="IBVXYZ${TS}.SH"; NAME="IBV-$TS-特殊字符<&\"'>与中文é€😀"
PAYLOAD=$(/Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin/python3 -c "import json,sys;print(json.dumps({'holdings':[{'code':'$CODE','name':sys.argv[1],'costPrice':1500.0,'quantity':100,'takeProfitPct':8,'stopLossPct':5}]},ensure_ascii=False))" "$NAME")
C1=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$AH" -H 'Content-Type: application/json' -d "$PAYLOAD" "$BASE/api/holdings")
chk "POST 特殊字符持仓 → 200" "$C1" "200"
GETBODY=$(curl -s -H "$AH" "$BASE/api/holdings")
GOT=$(printf '%s' "$GETBODY" | /Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin/python3 -c "
import sys,json
d=json.load(sys.stdin)
for h in d.get('holdings',[]):
    if h.get('code')=='$CODE': print(h.get('name','')); break
")
if [ "$NAME" = "$GOT" ]; then echo "  PASS  BE↔BE 特殊字符双向字节一致 (sha=$(sha "$NAME"))"; PASS=$((PASS+1)); else echo "  FAIL  BE↔BE 字节不一致"; echo "    want=$NAME"; echo "    got =$GOT"; FAIL=$((FAIL+1)); fi

echo "########## 4. 字节级幂等：同请求两次响应 sha 相同 ##########"
S1=$(curl -s -H "$AH" "$BASE/api/holdings" | shasum -a 256 | cut -d' ' -f1)
S2=$(curl -s -H "$AH" "$BASE/api/holdings" | shasum -a 256 | cut -d' ' -f1)
chk "两次只读响应 sha 一致" "$S1" "$S2"

echo "########## 5. FE↔BE 构建指纹对拍 ##########"
BC=$(curl -s -H "$AH" "$BASE/api/status" | jget "d['build_commit']")
echo "    后端 /api/status build_commit = ${BC:-<空>}"
GH=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && git rev-parse --short HEAD 2>/dev/null)
echo "    本地 git HEAD                 = ${GH:-<空>}"
# §0927AUDIT 脚本自纠（2026-09-28）：旧实现 else 分支只打 INFO 但仍 PASS+1，
# 该检查结构上永不为红——"19/19"的分母里混着一枚必绿项。不一致就是缺陷，计 FAIL。
if [ "${BC:-x}" = "${GH:-y}" ]; then echo "  PASS  FE↔BE 后端构建指纹=git HEAD（ldflags 已注入，§F6 已修复）"; PASS=$((PASS+1)); else echo "  FAIL  build_commit 与 git HEAD 不一致(BC=$BC vs HEAD=$GH)——二进制不是当前树构建的"; FAIL=$((FAIL+1)); fi
# 前端构建产物 dist 内应含 __BUILD_COMMIT__ 注入的 git HEAD（vite define 替换为字面量）
DIST=web/dist; FOUND=""
if [ -d "$DIST" ]; then
  FOUND=$(cd "$DIST" && /usr/bin/grep -rl "$GH" . 2>/dev/null | head -1)
fi
if [ -n "$FOUND" ]; then echo "  PASS  FE↔BE 前端 dist 含构建指纹($GH)，与后端一致"; PASS=$((PASS+1)); else echo "  FAIL  前端 dist 未找到构建指纹($GH)（dist 未构建或指纹缺失）"; FAIL=$((FAIL+1)); fi

echo
echo "########## 汇总 ##########"
echo "PASS=$PASS  FAIL=$FAIL"
[ "$FAIL" = "0" ] && echo "结论：独立字节级 UAT 全部通过" || echo "结论：存在未登记缺陷，见上"
