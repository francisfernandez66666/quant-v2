#!/usr/bin/env bash
# survey_live_rules.sh — §SURVEY-LIVE（2026-09-23）：现网战法库的复权新基线排摸（**只拉规则、不在服务器上算**）。
#
# 为什么要这条脚本：复权口径修复（§ADJ P0-A）后，内置形态战法的 A/B 排摸已在本地跑完
# （cmd/research/survey.go → strategy-survey），但 fac_*/pat_* 战法库条目**只存在于广州
# 数据目录**的 applied_factors.json / applied_patterns.json 里——本地没有这两个文件，
# 于是「新因子战法在新口径下还成立吗」这一问无从回答。
#
# 设计取舍（三条）：
#   ① 只做**文件拉取**，计算全在本地：回放 5546 只 × 44 个月是几十分钟级 CPU 负载，
#      放在生产机上会挤占实盘引擎/网关的 IO；规则 JSON 只有几 KB，拉取即走。
#   ② 复用部署脚本同一套 SSH/SCP 口径（BatchMode 预探测 + IdentitiesOnly + 指定私钥）。
#      本仓库既有实录教训：agent 挂多把 key 时 ssh 会逐把尝试直至跌落密码认证，
#      表现为「无任何输出的挂死」——所以先 BatchMode 探活，不通就立刻失败，绝不进入可能挂起的路径。
#   ③ 拉来的规则落 /tmp 且 mode 600，**不落仓库工作树**：战法权重/阈值是策略资产，
#      且 *.json 在数据目录之外并不受 .gitignore 保护，掉进工作树就有被 commit 的口子。
#
# 用法（本地 macOS）：
#   GZ_IP=81.71.69.17 ./scripts/survey_live_rules.sh
#   GZ_IP=... SURVEY_START=20230101 SURVEY_END=20260923 ./scripts/survey_live_rules.sh
# 可选环境变量：
#   GZ_USER        管理员用户（默认 Administrator，与 deploy_guangzhou.sh 对齐）
#   DATA_DIR       Windows 数据目录（默认 C:/var/lib/quant-trading-v2）
#   LOCAL_DB       本地研究库（默认 ~/.quant-trading-v2/trading.db，只读使用）
#   SURVEY_START/END  排摸区间（默认 20230101 / 今天）
#   SURVEY_MAXSTOCKS  回放池上限（默认 0=全部；本地库就是全池）
#   SURVEY_MINSPREAD  死成分阈值 pp（默认 0.5）
#   SURVEY_OUT       产物目录（默认 /tmp/survey_live_rules，每次清空重建）
# 退出码：0=排摸表已产出；非 0=预探测/拉取/构建/计算任一环节失败（不回退成"看起来成功"，
#         本仓库 §M8/§N-6 主题即「降级报成功」——这里刻意让失败可见）。
# §LIB-GATE（2026-09-25）加的两道：① 拉取后先打 `LIB_PREMISE <文件> entries=… enabled=… usable=…`，
#      两侧 usable 之和为 0 就直接失败（不进几十分钟回放）；副本解析不了也判失败，不折成"空库"。
#   ② 收尾多核一条 `survey_library gate=… factor_rules=… …` 锚点行——排摸数字必须带着
#      "这轮吃到了哪些战法"这条前提出门（缺该行同样非 0 退出）。
#      真要跑"零条线上战法"的对照口径，得显式给 research 传 --allow-empty-library，本脚本**不代传**。
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP}"
GZ_USER="${GZ_USER:-Administrator}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
LOCAL_DB="${LOCAL_DB:-$HOME/.quant-trading-v2/trading.db}"
SURVEY_START="${SURVEY_START:-20230101}"
SURVEY_END="${SURVEY_END:-$(date +%Y%m%d)}"
SURVEY_MAXSTOCKS="${SURVEY_MAXSTOCKS:-0}"
SURVEY_MINSPREAD="${SURVEY_MINSPREAD:-0.5}"
SURVEY_OUT="${SURVEY_OUT:-/tmp/survey_live_rules}"

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

RULES_DIR="$SURVEY_OUT/applied"

echo "==> [1/4] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

echo "==> [2/4] 拉取现网战法库（只有 JSON，不拉库、不跑算）"
rm -rf "$SURVEY_OUT"
mkdir -p "$RULES_DIR"
chmod 700 "$SURVEY_OUT" "$RULES_DIR"
# 文件名正锁：只认这两个名字，避免把数据目录里的其他 *.json（含 tokens/密钥类）误拉进来。
# 现网可能根本没有该文件（如形态库从未审批过条目）：ListApplied*Rules 对「文件缺失/空」
# 都按空库处理，所以这里刻意不造占位文件——造了反而会走进旧版单对象迁移分支。
pulled=0
for f in applied_factors.json applied_patterns.json; do
  if $SCP "${GZ_USER}@${GZ_IP}:${DATA_DIR}/${f}" "$RULES_DIR/$f" 2>/dev/null && [ -s "$RULES_DIR/$f" ]; then
    chmod 600 "$RULES_DIR/$f"
    pulled=$((pulled + 1))
    echo "ok - 拉取 ${f}（$(wc -c < "$RULES_DIR/$f" | tr -d ' ') 字节）"
  else
    rm -f "$RULES_DIR/$f"
    echo "   （$f 现网不存在或为空 → 该库按空处理）"
  fi
done
if [ "$pulled" = 0 ]; then
  echo "X 两个战法库都没拉到：要么现网确实没有任何已应用战法，要么拉取失败。不当成成功退出。" >&2
  exit 1
fi

# §LIB-GATE（2026-09-25）前提先落在纸面：拉来的副本里各有几条条目、几条处于启用态，开算之前就打印。
# 为什么这一步就要：判红门在 btreplay 装配适配器处才响（本脚本此时已经花了拉取+构建的时间），
# 而"这台机器/这一份副本到底有没有线上战法"正是这批数字最容易被忽略的前提——2026-09-25 那次
# 动量数字差 2.1× 的根源就在这条前提上。两侧都零条时**直接失败**，不进几十分钟的回放。
lib_scan() {
  python3 - "$1" <<'PY'
import json, sys
try:
    raw = open(sys.argv[1], encoding='utf-8').read().strip()
    d = json.loads(raw) if raw else []
    if isinstance(d, dict):  # 旧版单对象格式：按一条条目算
        d = [d]
    en = sum(1 for e in d if e.get('enabled'))
    usable = sum(1 for e in d if e.get('enabled') and (e.get('factors') or e.get('conds')))
    print(f"entries={len(d)} enabled={en} usable={usable}")
except Exception as exc:  # 读法失败不许伪装成"零条"，那会把"文件坏了"报成"现网没战法"
    print(f"entries=-1 enabled=-1 usable=-1 err={type(exc).__name__}")
PY
}
ENABLED_TOTAL=0
for f in applied_factors applied_patterns; do
  if [ -f "$RULES_DIR/$f.json" ]; then
    SCAN="$(lib_scan "$RULES_DIR/$f.json" || true)"
  else
    SCAN="absent"
  fi
  echo "   LIB_PREMISE $f.json ${SCAN}"
  N="$(printf '%s' "$SCAN" | tr ' ' '\n' | grep '^usable=' | cut -d= -f2 || true)"
  # usable=-1 = 副本读不出（文件坏了/编码不对），绝不能折成 0 计入"现网没战法"——那是把
  # "取回来的东西有问题"报成"线上确实没有战法"，两种情形的修法完全相反。
  if [ "$N" = "-1" ]; then
    echo "X 拉来的副本无法解析（$f.json：${SCAN}）——判失败，不按空库处理。" >&2
    exit 1
  fi
  case "$N" in ''|*[!0-9]*) N=0 ;; esac
  ENABLED_TOTAL=$((ENABLED_TOTAL + N))
done
if [ "$ENABLED_TOTAL" = 0 ]; then
  echo "X 拉来的现网副本里没有任何可用战法条目（两侧 usable 之和为 0）：排摸会走进 btreplay 的库判红门。" >&2
  echo "  先确认现网数据目录里 applied_*.json 的真实内容，再决定是修拉取还是接受"空库口径"（后者需显式 --allow-empty-library，且数字不得当作线上口径引用）。" >&2
  exit 1
fi

echo "==> [3/4] 本地构建当前 HEAD 的 research 二进制（新口径）"
BIN="$SURVEY_OUT/research"
( cd "$APP_DIR" && go build -o "$BIN" ./cmd/research )
echo "ok - $BIN"

echo "==> [4/4] 排摸（内置形态 + 现网全部 fac_*/pat_*，含停用条目）"
if [ ! -f "$LOCAL_DB" ]; then
  echo "X 本地研究库不存在：${LOCAL_DB}（可用 LOCAL_DB= 指定）" >&2
  exit 1
fi
# ⚠ `--out` 必须跟在**子命令之后**：research 的全局 `--out` 会被子命令自己的同名缺省盖回去
# （本轮实测就是这么把含战法权重的 strategy_survey.json 掉进仓库工作树的 ./research_out）。
# 产物落点由 survey.go 的硬闸兜底（解析到 Go 模块根内即拒），这里再机器复核一次：
# 打印路径下没有文件 = 降级报成功，直接非 0 退出。
"$BIN" --db "$LOCAL_DB" strategy-survey \
  --applied "$RULES_DIR" \
  --out "$SURVEY_OUT" \
  --start "$SURVEY_START" --end "$SURVEY_END" \
  --maxstocks "$SURVEY_MAXSTOCKS" --min-spread "$SURVEY_MINSPREAD" \
  2>&1 | tee "$SURVEY_OUT/survey.log"

ART="$SURVEY_OUT/strategy_survey.json"
if [ ! -s "$ART" ]; then
  echo "X 排摸未产出 JSON（期望 ${ART}）——判失败，不按「跑完了」算成功。" >&2
  exit 1
fi
echo "ok - 产物在位：${ART}（$(wc -c < "$ART" | tr -d ' ') 字节）"
# 两条锚点行都要在位：unhealthy=排摸到但有问题的条数；unsurveyable=根本没进表的方法数。
# 后者是"量不到"（当前=1，momentum 无回放适配器），缺了它就等于把盲区又抹平一次。
for anchor in survey_unhealthy survey_unsurveyable; do
  grep -E "^${anchor}=[0-9]+$" "$SURVEY_OUT/survey.log" || {
    echo "X survey.log 里没有 ${anchor}=<n> 锚点行（排摸未正常收尾）" >&2
    exit 1
  }
done
# §LIB-GATE 第三条锚点：本轮**真正吃到了几条库规则**（gate/读数/成因一体一行，缺字段即判失败）。
# 这一行是本脚本存在的理由——引用排摸数字的人必须连"吃到了哪些战法"一起拿走。
grep -E '^survey_library gate=[a-z_]+ factor_rules=[0-9]+ pattern_rules=[0-9]+ entries=[0-9]+/[0-9]+ enabled=[0-9]+/[0-9]+ dir_from=[a-z_]+ zero_reason=' \
  "$SURVEY_OUT/survey.log" || {
    echo "X survey.log 里没有 survey_library 锚点行（库读数没出门＝这批数字的前提不可核）" >&2
    exit 1
  }

echo
echo "产物："
echo "  表 + 机读 JSON：$ART"
echo "  规则副本（mode 600，未落仓库）：$RULES_DIR"
echo "SURVEY_LIVE_DONE"
