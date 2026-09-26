#!/usr/bin/env bash
# rotate_qmt_token_guangzhou.sh — §QMT-TOKENROT-CLI（2026-09-26，owner 令「门禁和规则摸牌后，你来补上网关口令令牌」）：
# 把「在网关机上手敲管理员 PowerShell 跑轮换脚本」这一运维动作收编成仓库里的正规通道。
# 现网入口只有那台 Windows 上的 deploy/qmt-win/rotate_qmt_token.ps1（随部署链上传，§ENH-5 教训：
# 仓库里有、现网也必须有的那条清单腿已由 deploy_guangzhou.sh 收编），此前**没有任何 Mac 侧驱动**，
# 于是轮换被记成「等 owner 令牌」——但脚本本身根本不吃 admin 会话（写侧全在服务器本地：
# 落时间戳副本→原子写 config.xt.json→nssm 并集写服务 env），卡住的只是"没人有一条合规的远程执行腿"。
# 本脚本补的正是这一跳，且口径比手写更紧：
#
# 三条安全口径（每条都有代码落点，不是口号）：
#   ① 缺省**零连接**：不带 -DryRun/-Apply 时一次 SSH 都不发，只打印计划与后续人工步
#      （§CAND-PUSH/§FINA-Q3 同族缺省——预览态在离网机器上也必须能跑通，§102 反证锁就测这个）。
#   ② 密钥纪律优先于便利：**任何模式的回传都先过「40 位以上连续十六进制＝疑似明文口令」闸**，
#      命中即停手、非 0 退出、原始日志留档但不重复打印；判定行只认 sha256 前 8 位指纹
#      （rotate_qmt_token.ps1 的既有铁律：只打 FpLabel 绝不打明文，本脚本原样继承）。
#   ③ -Apply（轮换）只动机器可写侧（源1 网关配置文件 + 源2 服务 env 冗余腿）；源3 引擎侧
#      rules.qmt.token 的权威写入方是设置页、源4 是 config.bridge.json。§0926ROT-ALIGN（owner 令
#      「我授权你来统一改」）把这两条腿收编成第三个方向 **-Align**：不生成新 token，以源1
#      （网关鉴权真源）现值为准在服务器本地复制到源3/源4（桥不在位只如实报 absent，落位时
#      由同一动作补齐）——值全程只在远端函数栈里过，回传仍只有 8 位指纹；写法是锚点原文
#      外科替换 + 三重拒判（解析不过/提案自证不命中/邻近字段被改动拒写），绝不整文件重排。
#      两方向成功收尾都原样转述仍欠的人工步，并强调「网关重启必须晚于三侧同值」时序红线。
#
# 转义与回传口径（§GBK/§CRLF 两族的合集）：远端只是 `-File 现成ps1 开关`，没有内联拼装；
# 解析前回传一律 tr -d '\r'（行尾最后字段的 CRLF 坑，09-24 真跑锤过）；ps1 的 DRY-RUN 汇总行
# 尾部带全角括号中文（Write-Host 走 GBK 回传会变乱码），因此判据只锚该行的 ASCII 前缀
# 「[rot] DRY-RUN」——前缀锁定，不赌整行编码。
#
# 用法（本地 macOS，与 deploy/verify/survey 同一入口变量）：
#   GZ_IP=81.71.69.17 ./scripts/rotate_qmt_token_guangzhou.sh              # 预览（零连接）
#   GZ_IP=81.71.69.17 ./scripts/rotate_qmt_token_guangzhou.sh -DryRun      # 远端只读：四源指纹读数
#   GZ_IP=81.71.69.17 ./scripts/rotate_qmt_token_guangzhou.sh -Apply       # 真轮换（只动源1+源2）
#   GZ_IP=81.71.69.17 ./scripts/rotate_qmt_token_guangzhou.sh -Align       # §0926ROT-ALIGN 远端只读：对齐计划（源3/源4 会动什么）
#   GZ_IP=81.71.69.17 ./scripts/rotate_qmt_token_guangzhou.sh -Align -Apply # 对齐落地：源1 现值→源3/源4（不生成新 token）
# 可选环境变量：
#   GZ_USER      默认 Administrator
#   ROTATE_PS1   远端 ps1 路径，默认 C:/opt/quant/qmt-win/rotate_qmt_token.ps1（部署清单落点）
# 退出码：0=预览已打印 / 干跑读数完整 / 轮换或对齐落地且判定行齐；非 0=预探测、路径、执行、
#         判定、明文闸任一环节失败（显式判红，不把"跑完了"当成功）。
set -u

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量）}"
GZ_USER="${GZ_USER:-Administrator}"
ROTATE_PS1="${ROTATE_PS1:-C:/opt/quant/qmt-win/rotate_qmt_token.ps1}"

MODE="preview"
ALIGN=0
for a in "$@"; do
  case "$a" in
    -DryRun) [ "$MODE" = "apply" ] && { echo "X -DryRun 与 -Apply 互斥（ps1 同规）。" >&2; exit 2; }; MODE="dryrun" ;;
    -Apply)  [ "$MODE" = "dryrun" ] && { echo "X -DryRun 与 -Apply 互斥（ps1 同规）。" >&2; exit 2; }; MODE="apply" ;;
    -Align)  ALIGN=1 ;;
    *) echo "X 未知参数：${a}（只认 -DryRun / -Apply / -Align，缺省=零连接预览）" >&2; exit 2 ;;
  esac
done
# §0926ROT-ALIGN（owner 令「我授权你来统一改」）：-Align 是对齐轴，与读/写轴正交。
# 两轴合成四个生效方向（preview/dryrun/apply + aligndry/alignapply）：
#   -Align          = 远端只读对齐计划（src3/src4 会动什么，逐一列出；ps1 缺省即 dry-run）
#   -Align -Apply   = 对齐落地（源1 现值复制到源3/源4，**不生成新 token**）
#   -Align -DryRun  = 同 -Align（显式只读）。-DryRun/-Apply 单独出现仍是原轮换读/写方向。
# 这里直接折叠进 MODE 单一命名空间（不另设 R_MODE：少一个变量就少一处"哪个才是真模式"的漂移）。
if [ "$ALIGN" = "1" ]; then
  if [ "$MODE" = "apply" ]; then MODE="alignapply"; else MODE="aligndry"; fi
fi

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"
ROTATE_LOCAL_PS1="$APP_DIR/deploy/qmt-win/rotate_qmt_token.ps1"
LOG="/tmp/rotate_qmt_token_guangzhou.log"

# leak_guard：明文口令闸。回传里出现 40+ 位连续十六进制即视为疑似 token 明文（新 token 是 64 位 hex，
# 指纹只有 8 位）——命中就停手退出，日志留在 $LOG 供人工核对，本函数不重复打印可疑内容。
# 键名与用途写清楚，值永远不该到这层。
leak_guard() {
  if printf '%s' "$1" | grep -qiE '[0-9a-f]{40,}'; then
    echo "X 回传里出现 40 位以上连续十六进制串——按密钥纪律按疑似明文口令处置：已停手，原始输出留档 ${LOG}（不再重复打印）。请人工核对后再决定下一步。" >&2
    exit 4
  fi
}

# run_remote：唯一的远端执行腿。$2 是传给 ps1 的开关（可为空串）；回传去 CR 后落日志、过明文闸。
# 退出码由调用方按模式分别断言（ps1 用 exit 1 表达 Die，这里如实透传）。
run_remote() {
  local flag="$1" out rc
  if [ -n "$flag" ]; then
    out="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${ROTATE_PS1} ${flag}" 2>&1)"
  else
    out="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${ROTATE_PS1}" 2>&1)"
  fi
  rc=$?
  out="$(printf '%s' "$out" | tr -d '\r')"
  printf '%s\n' "$out" > "$LOG"
  leak_guard "$out"
  LAST_OUT="$out"
  LAST_RC=$rc
}

echo "==> [1/4] 模式：${MODE}（缺省预览＝零连接；预览态在任何网络状态下都应退出 0）"

if [ "$MODE" = "preview" ]; then
  echo "ROTATE_PLAN mode=preview connect=0"
  echo "  将执行的远端命令（现在**没有**执行）："
  echo "    -DryRun 读四源指纹：powershell -File ${ROTATE_PS1} -DryRun"
  echo "    -Apply  只动源1+源2：powershell -File ${ROTATE_PS1} -Apply"
  echo "    -Align  只读对齐计划：powershell -File ${ROTATE_PS1} -Align"
  echo "    -Align -Apply 源1现值→源3/源4（不生成新 token）：powershell -File ${ROTATE_PS1} -Align -Apply"
  echo "  安全口径（ps1 既有铁律，本通道原样继承）："
  echo "    写侧①先落 config.xt.json 时间戳副本再原子写；写侧②服务 env 并集写不整体替换；"
  echo "    全程只打 sha256 前 8 位指纹；回传过本脚本的 40+hex 明文闸。"
  echo "  轮换（-Apply）之后仍欠的源3/源4 两条腿——§0926ROT-ALIGN 后有一条机器腿："
  echo "    跑 -Align -Apply 把源1 现值在服务器本地复制到引擎 config.json（rules.qmt.token）"
  echo "    与在位的 config.bridge.json；设置页仍是源3 的权威写入方，之后若从设置页改值需再对齐一次。"
  echo "    owner 手工形态依旧可用：[a] 设置页改 rules.qmt.token；[b] 桥侧 config.bridge.json --token 同步。"
  echo "  时序红线不变：三侧同值之前**不要重启网关进程**。"
  echo "  复核腿：verify_deploy_guangzhou.sh 第 20 探针（token fp agree across readable sources）。"
  exit 0
fi

echo "==> [2/4] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

# §N-5 同课（09-26 首跑实录）：这条 ssh 腿上 OpenSSH 会往 stderr 打三行后量子告警，
# 2>&1 合并后混进判定文本——按"整串等值"读会恒假（True 前面永远顶着噪声行）。
# 读法改成**逐行等值**：只认单独一行的 True/False，噪声行一律不进判据。
TP="$($SSH "powershell -NoProfile -Command \"Test-Path '${ROTATE_PS1}'\"" 2>&1 | tr -d '\r' | { grep -E '^(True|False)$' | tail -1 || true; })"
if [ "$TP" != "True" ]; then
  echo "X 现网找不到轮换脚本（Test-Path 读到 [${TP}]，期望 True）：${ROTATE_PS1}" >&2
  echo "  这正是 §ENH-5 的形态——仓库里有、现网没有。先补跑部署链（ps1 随 deploy 上传），不在这里猜路径。" >&2
  exit 1
fi
echo "ok - 现网轮换脚本在位"

# ── [2b] 现网脚本对齐（2026-09-26 实录教训）：上一轮部署走的是 `-s` 仅同步模式，
# 只重传二进制+前端**不传 ps1** ⇒ 现网这份 rotate_qmt_token.ps1 停留在旧版，而旧版恰好
# 带着一条从未被执行过的语法坏行（干跑首跑时远端 ParserError 抓出，仓库侧已修）。
# "部署清单里有"≠"现网那一份＝仓库这一份"——本通道执行前按 sha256 指纹现场比对，
# 不一致就远端先落 `.stale-<ts>` 副本再 scp 覆盖，并**写后复读**等值才放行。
# 文件哈希是元数据不是密钥材料，但为了和明文闸同一口径：全 64 位串只出现在管道里，
# 判定行/日志只落 12 位前缀；本腿不过 leak_guard（文件哈希必然 40+hex，过了必假阳）。
LOCAL_FP="$( { shasum -a 256 "$ROTATE_LOCAL_PS1" 2>/dev/null || sha256sum "$ROTATE_LOCAL_PS1" 2>/dev/null; } | cut -d' ' -f1 | cut -c1-12 | tr 'A-F' 'a-f' )"
if [ ${#LOCAL_FP} -ne 12 ]; then
  echo "X 本地仓库副本算不出 sha256 前缀（得到 [${LOCAL_FP}]）——判据本身不可信，中止。" >&2
  exit 1
fi
remote_fp12() {
  $SSH "powershell -NoProfile -Command \"(Get-FileHash -Algorithm SHA256 -LiteralPath '$1').Hash\"" 2>&1 \
    | tr -d '\r' | { grep -E '^[0-9A-Fa-f]{64}$' | tail -1 || true; } | cut -c1-12 | tr 'A-F' 'a-f'
}
REMOTE_FP="$(remote_fp12 "$ROTATE_PS1")"
if [ "$REMOTE_FP" = "$LOCAL_FP" ]; then
  echo "ok - 现网轮换脚本与仓库副本同源（sha256:${LOCAL_FP}…）"
else
  echo "   指纹不一致（本地 sha256:${LOCAL_FP} / 远端 sha256:${REMOTE_FP:-读不到}）——同步覆盖"
  STAMP="$(date +%Y%m%d-%H%M%S)"
  $SSH "powershell -NoProfile -Command \"Copy-Item -LiteralPath '${ROTATE_PS1}' -Destination '${ROTATE_PS1}.stale-${STAMP}' -Force\"" >/dev/null 2>&1 \
    || { echo "X 远端 .stale 副本没落地——备份不成就不覆盖（现网仍是旧版，本轮未执行任何轮换动作）。" >&2; exit 1; }
  $SCP "$ROTATE_LOCAL_PS1" "${GZ_USER}@${GZ_IP}:${ROTATE_PS1}" >/dev/null 2>&1 \
    || { echo "X scp 上传失败——判失败，不猜『哪一半到了』；旧版备份在 ${ROTATE_PS1}.stale-${STAMP}。" >&2; exit 1; }
  AFTER_FP="$(remote_fp12 "$ROTATE_PS1")"
  if [ "$AFTER_FP" != "$LOCAL_FP" ]; then
    echo "X 覆盖后复读指纹 ${AFTER_FP:-读不到} != 仓库 ${LOCAL_FP}——半态嫌疑，本轮不执行轮换。" >&2
    exit 1
  fi
  echo "ROTATE_PS1_SYNCED fp=${LOCAL_FP} stale_backup=.stale-${STAMP}"
fi

# 开关映射按合成后的单一 MODE：ps1 侧 -Align 缺省即 dry-run（-not -Apply ⇒ DryRun=true），
# 因此 aligndry 只送 -Align、alignapply 送 "-Align -Apply"（-File 后两段开关合法）。
FLAG=""
case "$MODE" in
  dryrun)     FLAG="-DryRun" ;;
  apply)      FLAG="-Apply" ;;
  aligndry)   FLAG="-Align" ;;
  alignapply) FLAG="-Align -Apply" ;;
esac

echo "==> [3/4] 远端执行（${MODE}）"
run_remote "$FLAG"

echo "==> [4/4] 判定行复核"
case "$LAST_OUT" in
  *'[fail]'*)
    echo "X 远端打出 [fail]——ps1 自身判死（详见 ${LOG}）。本轮**未确认成功**。" >&2
    exit 1 ;;
esac

if [ "$MODE" = "dryrun" ]; then
  for anchor in '[rot] src1 ' '[rot] src2 ' '[rot] src3 ' '[rot] src4 ' '[rot] DRY-RUN'; do
    if ! printf '%s\n' "$LAST_OUT" | grep -qF "$anchor"; then
      echo "X 干跑读数缺锚点行「${anchor}」——四源读数不完整，判失败（不把"看起来跑完了"当成功）。" >&2
      exit 1
    fi
  done
  if [ "$LAST_RC" -ne 0 ]; then
    echo "X 干跑退出码非 0（rc=${LAST_RC}），详见 ${LOG}" >&2
    exit 1
  fi
  printf '%s\n' "$LAST_OUT" | grep -E '^\[(rot|ok|warn)\] ' || true
  echo "ROTATE_READOUT ok src1-4=complete applied=0 log=${LOG}"
  echo "ok - 四源指纹读数已回显（只有 sha256 前 8 位；下一步是否 -Apply 按读数与裁决走）"
  exit 0
fi

# ── aligndry（-Align 单独/配 -DryRun）：对齐计划读数，四条锚行缺一判红、绝不写 ──
if [ "$MODE" = "aligndry" ]; then
  for anchor in '[rot] src1 ' 'align src1 truth fp=' 'align src3 plan ' 'align src4 plan ' 'ALIGN_DRY'; do
    if ! printf '%s\n' "$LAST_OUT" | grep -qF "$anchor"; then
      echo "X 对齐计划缺锚点行「${anchor}」——计划读数不完整，判失败（只读方向没读到就停，不带病落地）。" >&2
      exit 1
    fi
  done
  if [ "$LAST_RC" -ne 0 ]; then
    echo "X 对齐计划退出码非 0（rc=${LAST_RC}），详见 ${LOG}" >&2
    exit 1
  fi
  printf '%s\n' "$LAST_OUT" | grep -E '^\[ ?(rot|ok|warn) ?\] ' || true
  echo "ALIGN_READOUT ok plan=complete applied=0 log=${LOG}"
  echo "ok - 对齐计划已回显（只有指纹；will_write/action 决定 -Align -Apply 会动哪几条腿）"
  exit 0
fi

# ── alignapply（-Align -Apply）：源1 现值→源3/源4 落地复核，不生成新 token ──
if [ "$MODE" = "alignapply" ]; then
  for anchor in 'align src3 done token_fp=' 'align src4 done ' 'ALIGN_APPLIED'; do
    if ! printf '%s\n' "$LAST_OUT" | grep -qF "$anchor"; then
      echo "X 对齐判定行缺失「${anchor}」——源3/源4 写腿（外科替换+回读自证）没有全过，" >&2
      echo "  按半态处置：不要重启网关，config.json 旁有 .pre-align-* 副本可回滚，详见 ${LOG}。" >&2
      exit 1
    fi
  done
  if [ "$LAST_RC" -ne 0 ]; then
    echo "X -Align -Apply 退出码非 0（rc=${LAST_RC}）——判定行虽在也按失败处置，详见 ${LOG}" >&2
    exit 1
  fi
  # 提取腿同轮换口径：先锚 'token_fp=sha256:' 再截到空格（禁裸 hex 抓，09-26 实录自伤教训）。
  ALIGN3_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 'align src3 done token_fp=' || true)"
  [ -n "$ALIGN3_LINE" ] || { echo "X 取不到 src3 判定行——判失败。" >&2; exit 1; }
  REST="${ALIGN3_LINE#*token_fp=sha256:}"
  NEWFP="${REST%% *}"
  case "$NEWFP" in
    "")   echo "X src3 判定行里取不到对齐指纹——判据本身不可信，判失败。" >&2; exit 1 ;;
    [!0-9a-f]*|*[!0-9a-f]*) echo "X 对齐指纹形态异常（${NEWFP}，应为纯小写十六进制）——判失败。" >&2; exit 1 ;;
  esac
  BR_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 'align src4 done ' || true)"
  BR_ACT="$(printf '%s' "$BR_LINE" | { grep -oE 'action=[a-z]+' || true; })"
  printf '%s\n' "$LAST_OUT" | grep -E '^\[ ?(rot|ok|warn) ?\] ' || true
  echo "ALIGN_APPLIED token_fp=${NEWFP} src4_${BR_ACT:-action=unset}"
  # 写后自动复跑只读干跑：src3 行必须吃到该指纹（同源复读；src4 absent 时本就无行可复读，
  # 如实以落地判定行的 action 为准，不假造读数）。
  echo "==> [4b] 写后复读（远端只读干跑，比对 src3 与对齐指纹）"
  run_remote "-DryRun"
  case "$LAST_OUT" in
    *'[fail]'*) echo "X 写后复读远端判死——详见 ${LOG}。" >&2; exit 1 ;;
  esac
  SRC3_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 '\[rot\] src3 ' || true)"
  [ -n "$SRC3_LINE" ] || { echo "X 写后复读拿不到 src3 行——复核没做到，判失败。" >&2; exit 1; }
  if ! printf '%s' "$SRC3_LINE" | grep -qF "sha256:${NEWFP}"; then
    echo "X 写后复读 src3 指纹 != 本轮对齐值 sha256:${NEWFP}——引擎配置可能被并发改写/回滚，判失败。" >&2
    echo "  行内容：${SRC3_LINE}"
    exit 1
  fi
  SRC1_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 '\[rot\] src1 ' || true)"
  if [ -z "$SRC1_LINE" ] || ! printf '%s' "$SRC1_LINE" | grep -qF "sha256:${NEWFP}"; then
    echo "X 写后复读 src1 指纹 != 对齐指纹——真源在落地后被改动，判失败（先取证再动任何重启）。" >&2
    echo "  行内容：${SRC1_LINE:-取不到}"
    exit 1
  fi
  echo "ok - 写后复读一致：源1==源3==sha256:${NEWFP}（桥腿 ${BR_ACT:-unknown}，absent=未落位、落位时同法补齐）"
  echo ""
  echo "  ── 对齐后的收尾口径 ──"
  echo "  [1] 设置页此后仍是源3 的权威写入方：若再从设置页改 token，改完必须重跑本通道 -Align -Apply 复对齐；"
  echo "  [2] 桥落位（config.bridge.json 出现）后重跑一次 -Align -Apply 即补齐源4；"
  echo "  [3] 时序红线：网关/引擎进程重启必须晚于四侧同值确认——复核腿 verify 第 20 探针 token_fp_agree。"
  exit 0
fi

# ── apply 模式：三条写侧判定行必须在位，缺一判红 ──
for anchor in 'config_file written token_fp=' 'read-back confirmed' 'self-check(a) gateway file source CONFIRMED'; do
  if ! printf '%s\n' "$LAST_OUT" | grep -qF "$anchor"; then
    echo "X 轮换判定行缺失「${anchor}」——写侧三腿（副本+原子写/env 并集/文件回读自证）没有全过，" >&2
    echo "  按半态处置：不要重启网关，先按 ${LOG} 与 config.xt.json.pre-rotate-* 时间戳副本回滚。" >&2
    exit 1
  fi
done
if [ "$LAST_RC" -ne 0 ]; then
  echo "X -Apply 退出码非 0（rc=${LAST_RC}）——判定行虽在也按失败处置，详见 ${LOG}" >&2
  exit 1
fi
# 提取腿按 ps1 实际形态来：判定行是「token_fp=sha256:<8hex> report_token_fp=sha256:<8hex>」，
# 先锚第一处 token_fp=sha256: 再截到空格——09-26 首跑实录：按裸 [0-9a-f]* 抓会把 "sha256:"
# 前缀当成截断点取空，把一次真成功的轮换误判成"判据不可信"。
APPLY_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 'config_file written token_fp=' || true)"
[ -n "$APPLY_LINE" ] || { echo "X 取不到写侧①判定行——判失败。" >&2; exit 1; }
REST="${APPLY_LINE#*token_fp=sha256:}"
NEWFP="${REST%% *}"
case "$NEWFP" in
  "")   echo "X 判定行里取不到新值指纹（token_fp=sha256: 后为空）——判据本身不可信，判失败。" >&2; exit 1 ;;
  [!0-9a-f]*|*[!0-9a-f]*) echo "X 新值指纹形态异常（${NEWFP}，应为纯小写十六进制）——判失败。" >&2; exit 1 ;;
esac
echo "ROTATE_APPLIED token_fp=${NEWFP}"
printf '%s\n' "$LAST_OUT" | grep -E '^\[(rot|ok|warn)\] ' || true

# 写后自动复跑干跑：src1 必须等于新值指纹（同源复读，§C7 自证口径的驱动侧镜像）。
echo "==> [4b] 写后复读（远端只读干跑，比对 src1 与新值指纹）"
run_remote "-DryRun"
case "$LAST_OUT" in
  *'[fail]'*) echo "X 写后复读远端判死——详见 ${LOG}。" >&2; exit 1 ;;
esac
SRC1_LINE="$(printf '%s\n' "$LAST_OUT" | grep -m1 '\[rot\] src1 ' || true)"
[ -n "$SRC1_LINE" ] || { echo "X 写后复读拿不到 src1 行——复核没做到，判失败。" >&2; exit 1; }
if ! printf '%s' "$SRC1_LINE" | grep -qF "sha256:${NEWFP}"; then
  echo "X 写后复读 src1 指纹 != 本轮新值 sha256:${NEWFP}——现网文件可能被并发改写/回滚，判失败。" >&2
  echo "  行内容：${SRC1_LINE}"
  exit 1
fi
echo "ok - 写后复读一致：网关配置文件真源已吃到新值指纹 sha256:${NEWFP}"
echo ""
echo "  ── 仍欠的两条人工腿（只有 owner 能做，顺序即红线）──"
echo "  [a] 设置页把 QMT 配置的 token 改成同一新值——新值从网关机本地读 config.xt.json 取值，"
echo "      不经本对话、不落任何日志；"
echo "  [b] 桥侧 config.bridge.json/--token 同步并重启桥。"
echo "  三侧同值之前**不要重启网关进程**（旧进程拿旧值还在服务；一重启就断）。"
echo "  复核腿：GZ_IP=... COMMIT=<现网 SHA> ./scripts/verify_deploy_guangzhou.sh 第 20 探针。"
exit 0
