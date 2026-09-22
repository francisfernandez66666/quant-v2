#!/bin/bash
# check_deploy_static_locks.sh — 部署面/移动端静态锁（§M7 + §M12，2026-09-22 修复批新增）。
#
# 定位：纯本地静态断言，不 SSH、不构建、不跑服务——可被主代理接入 scripts/verify_changes.sh
# 或 CI（本文件自身不改 verify_changes.sh）。任一 FAIL 退出码非 0。
#
# 锁清单：
#   L1 §M7a  init_default_config.ps1 生成的默认 config.json：qmt 必须挂在 rules 下、根级不得再有 qmt，
#            且 rules/qmt 的键路径与 internal/config/config.go 的 struct json tag 抽查一致
#            （Rules wrapper 根段 = rules/d1；Rules.QMT tag = qmt；QMTConfig 字段 tag 覆盖生成键）。
#   L2 §M7c  config.xt.template.json 可解析、核心键被 qmt_gateway/gateway.py DEFAULT_CONFIG 消费；
#            ensure_gateway_config.ps1 引用 §H8 $ProbeGatewayPort 且以无 BOM UTF-8 落盘。
#   L3 §H8   deploy_guangzhou.sh 收编 register_web_service.ps1 / ensure_gateway_config.ps1（M7b/c 下发锁）；
#            Caddy 站点端口与 service_probe_config.ps1 单源一致（guangzhou.conf :8080 块 / 反代 8081）。
#   L4 §M12  MainActivity.kt：远程调试必须 BuildConfig.DEBUG 门控；注入 JS 走 JSONObject.quote 完整转义、
#            旧「只转义单引号」写法不得复发；localStorage token 风险注释在位。
#
# 用法：./scripts/check_deploy_static_locks.sh   （在仓库任意位置运行均可，内部按脚本自身定位）
set -uo pipefail
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"

command -v python3 >/dev/null 2>&1 || { echo "FAIL python3 不可用（本锁依赖 python3 解析 JSON/Go tag）"; exit 1; }

FAIL=0
run_py() {
  out="$(python3 - "$APP_DIR" <<'PY'
import json, os, re, sys

root = sys.argv[1]
fails = []

def rd(p):
    with open(os.path.join(root, p), encoding="utf-8-sig") as f:
        return f.read()

def check(name, ok, detail=""):
    print(("PASS|" if ok else "FAIL|") + name + (("|" + detail) if detail else ""))
    if not ok:
        fails.append(name)

# ── L1 §M7a：init_default_config.ps1 生成 JSON 键路径 ↔ config.go struct tag ──────────────
ps1 = rd("deploy/qmt-win/init_default_config.ps1")
m = re.search(r"\$defaultJson\s*=\s*(.+)", ps1)
check("L1.defaultJson 行存在", bool(m))
cfg_json = None
if m:
    # 拼接 PowerShell 单引号字面量；`' + $gwPort + '` 端口段以 §H8 单源值代入
    port = "8789"
    pm = re.search(r"\$ProbeGatewayPort\s*=\s*(\d+)", rd("deploy/qmt-win/service_probe_config.ps1"))
    if pm:
        port = pm.group(1)
    parts = re.findall(r"'([^']*)'|(\$\w+)", m.group(1))
    s = "".join(p if p else port for p, _ in parts)  # $gwPort 等变量统一代 §H8 端口值
    try:
        cfg_json = json.loads(s)
    except Exception as e:
        check("L1.defaultJson 可解析", False, str(e))
    if cfg_json is not None:
        check("L1.根级不得有 qmt 键", "qmt" not in cfg_json, "根级 qmt 会被 Go wrapper(rules/d1) 静默忽略——§M7 缺陷本体")
        check("L1.根级 = rules(+d1) 段", set(cfg_json) <= {"rules", "d1"}, "got=" + ",".join(cfg_json))
        check("L1.rules.qmt 在位", isinstance(cfg_json.get("rules"), dict) and "qmt" in cfg_json.get("rules", {}))

go = rd("internal/config/config.go")
rules_tags = set(re.findall(r'`json:"([^",]+)',
                 go[go.index("type Rules struct {"):go.index("\n}", go.index("type Rules struct {"))]))
qmt_tags = set(re.findall(r'`json:"([^",]+)',
               go[go.index("type QMTConfig struct {"):go.index("\n}", go.index("type QMTConfig struct {"))]))
check("L1.config.go: Rules 有 qmt tag", "qmt" in rules_tags)
check("L1.config.go: Load wrapper 根段 rules/d1",
      bool(re.search(r'Rules\s+\*Rules\s+`json:"rules"`', go)) and bool(re.search(r'D1\s+\*D1Config\s+`json:"d1"`', go)))
if cfg_json is not None:
    rk = set(cfg_json.get("rules", {}))
    check("L1.rules.* 键 ⊆ Rules struct tags", rk <= rules_tags, "漂移键: " + ",".join(sorted(rk - rules_tags)))
    qk = set(cfg_json.get("rules", {}).get("qmt", {}))
    check("L1.rules.qmt.* 键 ⊆ QMTConfig tags", qk <= qmt_tags, "漂移键: " + ",".join(sorted(qk - qmt_tags)))

# ── L2 §M7c：config.xt.json 生成链 ────────────────────────────────────────────────────────
try:
    tpl = json.loads(rd("deploy/qmt-win/config.xt.template.json"))
    check("L2.config.xt.template.json 可解析", True)
except Exception as e:
    tpl = {}
    check("L2.config.xt.template.json 可解析", False, str(e))
gwpy = rd("qmt_gateway/gateway.py")
blk = gwpy[gwpy.index("DEFAULT_CONFIG = {"):gwpy.index("\n}", gwpy.index("DEFAULT_CONFIG = {"))]
gw_keys = set(re.findall(r'^\s*"([a-z0-9_]+)":', blk, re.M))
need = {"listen", "token", "broker", "account", "db", "report_url", "report_token"}
check("L2.模板核心键被网关 DEFAULT_CONFIG 消费", need <= gw_keys and need <= set(tpl),
      "网关缺:" + ",".join(sorted(need - gw_keys)) + " 模板缺:" + ",".join(sorted(need - set(tpl))))
probe = rd("deploy/qmt-win/service_probe_config.ps1")
pm = re.search(r"\$ProbeGatewayPort\s*=\s*(\d+)", probe)
tpl_port = re.search(r":(\d+)$", str(tpl.get("listen", "")))
check("L2.模板 listen 口 = §H8 $ProbeGatewayPort", bool(pm) and bool(tpl_port) and pm.group(1) == tpl_port.group(1),
      f"template={tpl.get('listen')} probe={pm.group(1) if pm else 'N/A'}")
ens = rd("deploy/qmt-win/ensure_gateway_config.ps1")
check("L2.ensure 脚本引用 ${ProbeGatewayPort}（§H8 同源）", "$ProbeGatewayPort" in ens)
check("L2.ensure 无 BOM 落盘（网关 json.load 不容 BOM）", "UTF8Encoding($false)" in ens)
check("L2.ensure 已存在不覆盖", "保留现有 config.xt.json" in ens)

# ── L3 §M7b/§H8：部署清单收编 + Caddy 端口同源 ────────────────────────────────────────────
dep = rd("scripts/deploy_guangzhou.sh")
check("L3.deploy 上传并执行 register_web_service.ps1（M7b）",
      "register_web_service.ps1" in dep and "guangzhou.conf" in dep)
check("L3.deploy 上传并执行 ensure_gateway_config.ps1 + 模板（M7c）",
      "ensure_gateway_config.ps1" in dep and "config.xt.template.json" in dep)
check("L3.deploy 继续上传 §H8 探针单源（不得回退）", "service_probe_config.ps1" in dep)
webps = rd("deploy/qmt-win/register_web_service.ps1")
check("L3.register_web_service 引用 ${ProbeCaddyPort}（§H8 同源）", "$ProbeCaddyPort" in webps)
# §M7b-1（2026-09-22 部署实录，双重根因）：①caddy validate 的 INFO 日志走 stderr，PS 5.1 在
# ErrorActionPreference=Stop + `2>&1 | ForEach-Object` 合并下把每行 stderr 变终止性错误——
# [2d] 曾在替换 Caddyfile 前死亡且被外层告警吞掉；②暂存文件名不带 "Caddyfile" 字样时 caddy
# 按 JSON 解析假阴性。锁：validate 必须「临时降 Continue + --adapter caddyfile + 认 $LASTEXITCODE」。
check("L3.register validate stderr 撕裂+适配器已修（M7b-1）",
      '$eapPrev' in webps and '--adapter caddyfile --config $CaddyConfSrc' in webps
      and '2>&1 | ForEach-Object { Write-Host "  $_" }' not in webps)
conf = rd("deploy/caddy/guangzhou.conf")
cm = re.search(r"\$ProbeCaddyPort\s*=\s*(\d+)", probe)
qm = re.search(r"\$ProbeQuantPort\s*=\s*(\d+)", probe)
check("L3.guangzhou.conf 静态口 = $ProbeCaddyPort",
      bool(cm) and bool(re.search(r":" + cm.group(1) + r"\s*\{", conf)), "未找到监听块 :" + (cm.group(1) if cm else "?"))
check("L3.guangzhou.conf 反代目标 = 127.0.0.1:$ProbeQuantPort",
      bool(qm) and ("reverse_proxy 127.0.0.1:" + qm.group(1)) in conf)
check("L3.register_engine_services 仍 dot-source 探针单源（§H8 不得破）",
      'Join-Path $PSScriptRoot "service_probe_config.ps1"' in rd("deploy/qmt-win/register_engine_services.ps1"))
check("L3.all_service_watchdog 仍 dot-source 探针单源（§H8 不得破）",
      "service_probe_config.ps1" in rd("deploy/qmt-win/all_service_watchdog.ps1"))

# ── L4 §M12：移动端凭据暴露面 ─────────────────────────────────────────────────────────────
kt = rd("mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt")
lines = kt.splitlines()
dbg_idx = [i for i, l in enumerate(lines) if "setWebContentsDebuggingEnabled(true)" in l]
check("L4.setWebContentsDebuggingEnabled(true) 仅一处", len(dbg_idx) == 1, f"got={len(dbg_idx)}")
gated = any("BuildConfig.DEBUG" in lines[j] for i in dbg_idx for j in range(max(0, i - 3), i))
check("L4.调试开关受 BuildConfig.DEBUG 门控", gated)
old_weak = "replace(\"'\", \"\\\\\'\")"   # Kotlin 旧写法原文：replace("'", "\\'")
check("L4.旧「只转义单引号」写法不得复发", old_weak not in kt)
check("L4.注入 JS 走 JSONObject.quote（jsQuote）", "JSONObject.quote" in kt)
n_item = len(re.findall(r"setItem\('liangzai_server_url',\s*\" \+", kt))
n_quote = len(re.findall(r"jsQuote\(", kt))
check("L4.两处 localStorage.setItem 注入均走 jsQuote", n_item >= 2 and n_quote >= 3, f"setItem={n_item} jsQuote={n_quote}")
check("L4.localStorage token 风险注释在位（§M12c 台账）", "§M12c" in kt and "残余风险" in kt)

for line in sorted(set()):
    pass  # no-op placeholder（保持尾部结构清晰）
sys.exit(1 if fails else 0)
PY
)"
  code=$?
  echo "$out"
  if [ $code -ne 0 ]; then
    FAIL=1
  fi
}
run_py

# Kotlin 弱转义复发的独立 grep 锁（双引号内：\" → "，\\\\ → 两个反斜杠，与旧代码字面量逐字对齐）
if grep -Fq "replace(\"'\", \"\\\\'\")" mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt 2>/dev/null; then
  echo "FAIL|L4.grep 兜底: MainActivity.kt 仍残留 replace(\"'\", \"\\\\'\") 弱转义"
  FAIL=1
else
  echo "PASS|L4.grep 兜底: 无单引号弱转义残留"
fi

# ps1 语法静态解析（pwsh 可用才跑；Windows PowerShell 环境由 CI 另测）
if command -v pwsh >/dev/null 2>&1; then
  for f in deploy/qmt-win/init_default_config.ps1 deploy/qmt-win/register_web_service.ps1 deploy/qmt-win/ensure_gateway_config.ps1; do
    if pwsh -NoProfile -Command "\$null = [scriptblock]::Create((Get-Content -Raw '$f'))" >/dev/null 2>&1; then
      echo "PASS|L5.pwsh 语法解析 $f"
    else
      echo "FAIL|L5.pwsh 语法解析 $f"
      FAIL=1
    fi
  done
else
  echo "SKIP|L5.pwsh 不可用，跳过 ps1 语法解析"
fi

if [ $FAIL -eq 0 ]; then
  echo "== 部署面/移动端静态锁全部通过 =="
else
  echo "== 存在 FAIL：修复部署清单/转义口径后再合入 =="
fi
exit $FAIL
