#!/usr/bin/env bash
# backup_gz_cold.sh — 广州 <DataDir> 的**整目录冷备 → 本机封存基线**（S0–S3）与 S3b 增量补备的唯一入口
#
# 为什么要有这条脚本（docs/PLAN_GZ_REINSTALL_BACKUP_20261104.md §1.2 / §5 / §6.6 工单②）：
#   现网两条备份腿都是固定名单（deploy/qmt-win/backup_snapshot.ps1 的 9 个 JSON +
#   scripts/backup.sh 的 2 JSON + 4 个 applied_*），而 Go 侧会在 <DataDir> 写几十种运行期落盘项，
#   11-04 批又新增 manual_positions/manual_account 两张表与 accounts/<uid>/leg_status.json。
#   照名单挑文件 = 必然漏，而且漏下来长得跟"备份成功"一模一样。本脚本的输入是**当场枚举出来的
#   真实文件全集**（§5 S0 第 3 条：枚举是包体的输入，不是事后核对项），名单只用来标注"哪些是有意排除"。
#   它是本次迁移的基线腿，不是日常备份腿的替代者 —— 日常那条 scripts/backup.sh 继续跑它的固定名单。
#
# 锚点（owner 读这几行，不读段落；判据行全 ASCII —— 中文字面量经 PS→SSH→bash 会被码页折成 '?'，
#   11-02 那次就是靠这个把守卫判成永不匹配的摆设，AGENTS.md「运维口径」写死了这条）：
#   COLD_BACKUP_RESULT=OK baseline=<path> files=<n> bytes=<n> manifest=v<N>
#   COLD_BACKUP_RESULT=ABORT step=<n> reason=<code>
#   COLD_BACKUP_RESULT=DRY files=<n> bytes=<n> baseline=<path>
#   COLD_BACKUP_RESULT=PREFLIGHT files=<n> bytes=<n> capacity_factor=<f> writes=0
#   COLD_BACKUP_RESULT=SKIP reason=no_delta_since_frozen
#   VERIFY_TARGET / VERIFY_VOLUMES planned=<n> present=<n> / VERIFY_MARKER
#   S0_TRANSPORT mode=ssh ... gate=explicit--host（现网形态只能被 --host 显式点名）
#   S2_SECRETS container=<p> sha256=<h> bytes=<n> mode=600 entries=<n> values_logged=0
#   S3_SEAL_CONTAINER file=<n> mode=600 / S3B_DELETED_CHAIN / S3B_BASELINE_BLESSED
#   转发的读数行：ENUM_SUMMARY| / BUILD_RESULT= / V|name= / V_ARRIVED| / MANIFEST_RESULT= /
#   L1_RESULT= / L2_RESULT= / SCAN_SUMMARY| / snapshot ok src=
#
# 失败形态（每一条都有 step 与 reason，绝不"跑完了就算成"）：
#   step=0 ssh_batchmode_failed        探活不通即停（本仓有过 ssh 静默回退密码认证挂死 30 分钟）
#   step=0 capacity_shortfall          本地余量 < 包体 x factor（规划时盘只剩 11 Gi，R1）
#   step=0 close_state_unreadable / close_state_not_today   收盘对账读数拿不到或不是今天（R14）
#   step=1 freeze_unconfirmed          未显式确认写面已停就不冻结 —— 本脚本从不停服务
#   step=1 db_snapshot_failed          在线 .backup 不成立；**绝不退化成裸 cp**（R2 铁律一）
#   step=2 volume_verify_failed        卷落地复算 sha 不符 → 单独重传该卷（不是整份重跑）
#   step=2 volume_count_mismatch       清单卷数 != 落地卷数（漏一卷就是漏基线）
#   step=2 secrets_*                   B 类口令取不到 / 容器写不出（R20）；值一律不落日志
#   step=2 leak_guard_hit              回传里出现 40+ 位连续十六进制 = 疑似明文口令，停手
#   step=3 l1_failed / l2_failed       逐文件点名（缺/多/内容不同/权限不同）
#   step=3 baseline_target_protected   落点/删除目标撞 §4.5 不可再生资产
#   step=3b v1_never_verified（基线是 vN 时点名 v<N>_never_verified） 上一版没过校验就不许出下一版
#   step=3b config_change_unregistered 冻结后云端配置动过又没显式登记（§5 S3b「不许差不多就行」）
#   step=3b baseline_pointer_bad / baseline_manifest_absent 基线指针指不到东西（半装态当场拒）
#   step=0  source_undetermined 既没 --local-source 也没 --host —— 不猜、更不默认连生产
#   step=0  verify_* --verify 指到没有清单/没有卷的目录
#
# 安全轨（写在代码里，不在注释里）：
#   * 远端只跑只读命令（枚举/在线快照/取件）；**不停服务、不改配置、不删任何远端文件**；
#     本地也只删自己的 temp，删前先过 protected_target 闸（~/backups/quant/restic、~/backups/quant、
#     ~/.quant-trading-v2、仓库 docs/、tools/pub_uat/、AGENTS.md 等 §4.5 项一律拒绝）
#   * B 类凭证走单独加密容器：明文只在自己 0700 的 temp 里过一遍，随即删除；普通包里没有它，
#     manifest 里只有路径 + 容器哈希（§4.6 三条硬动作）；口令沿用仓库既有纪律（macOS 钥匙串，
#     与 deploy/mac/restic_pull_backup.sh、scripts/backup_restic_pass_offline.sh 同一条通道），
#     不新造密钥库，任何路径都不 echo/cat 值
#   * SQLite 一律走 deploy/qmt-win/backup_snap.py 那把尺子（sqlite3 .backup + integrity_check）
#
# 用法（一个入口，四种模式；模式互斥，缺省 --full）：
#   --preflight          只读预检：收盘态/容量/落点三道硬闸 + 体量读数，零写入，不要 --frozen-confirmed
#   --dry-run            只读 + 出计划（分卷上限、B 类形态、包体构成）；收盘态只报数不拦
#   --full               S0–S3 全量：冻结确认 → 在线快照 → 枚举 → 加密容器 → 分卷 → manifest → L1/L2 → 封存
#   --incremental --base=<baseline 目录>   S3b 增量补备：出 v(N+1)，v(N) 与更早的每一版一个字节都不重写
#   --verify <baseline 目录>（或 --verify=<目录>）  照基线里在册的清单把 L1/L2 再跑一遍；只读、不连源端
#   源端二选一（**都必须显式点名，缺省不猜**）：
#     --local-source=<目录>   本机/演练数据根（今晚的自证、日后复刻都走这条）
#     --host=<ssh 别名|ip>    现网模式；远端只跑只读枚举/在线快照/取件，不停服务、不改配置、不删远端文件
#   其它开关：--frozen-confirmed（写面已停的显式确认，S1/S3b 都要）--skip-secrets
#             --out-dir --capacity-factor --volume-max-bytes --big-file-min-bytes
#             --close-state-file --status-file --snapshot-ok-file --config-change-registered
#   环境变量：GZ_USER ADMIN_TOKEN GZ_BASE_URL REMOTE_PY
#             SRC_ROOT STAGE_ROOT WIRING_ROOT WIRING_DIRS WIRING_FILES APK_DIR DB_NAMES OUT_ROOT
#             SECRET_QUANT_ENV SECRET_GATEWAY_CONFIG KEYCHAIN_ITEM COLD_B_PASS_CMD CAPACITY_FACTOR
#             VOLUME_MAX_BYTES BIG_FILE_MIN_BYTES PROTECTED_EXTRA SNAP_MAX_WAIT
#
# 今晚 owner 裁决（裁决 5 + 本批口头令）：基线只落**本机磁盘一份**，"复制到不同介质"那半条
#   §5 S3 第 4 步由 owner 明确取消 —— 所以收尾必须如实打一条 SINGLE_COPY 提醒：这份基线不是灾难
#   恢复副本。异地那条 restic 链保留不动，它是第二重保险，不是本基线的替代品。
set -uo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
HELPER="$APP_DIR/scripts/backup_gz_cold_helper.py"
SNAP_PY="$APP_DIR/deploy/qmt-win/backup_snap.py"

SRC_ROOT="${SRC_ROOT:-C:/var/lib/quant-trading-v2}"
STAGE_ROOT="${STAGE_ROOT:-C:/var/lib/quant-coldbackup-stage}"
WIRING_ROOT="${WIRING_ROOT:-C:/opt/quant}"
WIRING_DIRS="${WIRING_DIRS:-web}"
WIRING_FILES="${WIRING_FILES:-Caddyfile}"
APK_DIR="${APK_DIR:-C:/opt/quant/apk}"
SECRET_QUANT_ENV="${SECRET_QUANT_ENV:-C:/etc/quant.env}"
SECRET_GATEWAY_CONFIG="${SECRET_GATEWAY_CONFIG:-C:/qmt/quant-trading-v2/qmt_gateway/config.xt.json}"
DB_NAMES="${DB_NAMES:-trading.db live.db}"
OUT_ROOT="${OUT_ROOT:-$HOME/backups/quant-cold}"
GZ_BASE_URL="${GZ_BASE_URL:-https://quant-trading.top}"
KEYCHAIN_ITEM="${KEYCHAIN_ITEM:-quant-coldbackup-bclass-pass}"
CAPACITY_FACTOR="${CAPACITY_FACTOR:-1.5}"
VOLUME_MAX_BYTES="${VOLUME_MAX_BYTES:-1073741824}"
BIG_FILE_MIN_BYTES="${BIG_FILE_MIN_BYTES:-536870912}"
REMOTE_PY="${REMOTE_PY:-python}"
SNAPSHOT_MARK_REMOTE="${SNAPSHOT_MARK_REMOTE:-C:/var/lib/quant-snapshot/SNAPSHOT_OK}"

MODE="full"
DRY_RUN=0
LOCAL_SOURCE=""
BASE_DIR=""
BUILD_COMMIT_OVERRIDE=""
WEB_COMMIT_OVERRIDE=""
CLOSE_STATE_FILE=""
STATUS_FILE=""
SNAPSHOT_OK_FILE=""
CONFIG_REGISTERED=""
SKIP_SECRETS=0
FROZEN_CONFIRMED=0
# 这两枚必须在**全局默认区**初始化，而不是在 S2 的 B 类段落里：
# --incremental 走的是 S3b 分支，根本不经过 S2，而它的 v2 manifest 段落要引用 SECRETS_SPEC，
# `set -u` 下"从未赋值"直接 unbound variable 中止（今天的第二个根雷，表现是一句 bash 内部错，
# 看不出跟 B 类有什么关系）。S2 那段照旧重新赋值，语义不变。
SECRETS_SPEC=""
CONTAINER_NAME=""
# 基线此刻是"已解封、尚未封回"吗（seal/unseal 各自翻转；退出时 cleanup 兜底封回）
UNSEALED=0
HOST_ARG=""
VERIFY_DIR=""
PENDING=""

for a in "$@"; do
  # PENDING 表达"上一个参数是带值形式（--verify / --host），这一枚就是它的值"。
  # 判据行的取值本身是路径/主机名，可能带空格以外的任何字符，所以两种写法都收：
  #   --verify=<dir> / --verify <dir> / --host=gz / --host gz
  if [ -n "$PENDING" ]; then
    case "$PENDING" in
      verify) VERIFY_DIR="$a" ;;
      host) HOST_ARG="$a" ;;
    esac
    PENDING=""
    continue
  fi
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --full) MODE="full" ;;
    --preflight) MODE="preflight" ;;
    --incremental) MODE="incremental" ;;
    --verify) PENDING="verify"; VERIFY_DIR="__pending__"; MODE="verify" ;;
    --verify=*) VERIFY_DIR="${a#*=}"; MODE="verify" ;;
    --host) PENDING="host"; HOST_ARG="__pending__" ;;
    --host=*) HOST_ARG="${a#*=}" ;;
    --local-source=*) LOCAL_SOURCE="${a#*=}" ;;
    --base=*) BASE_DIR="${a#*=}" ;;
    --out-dir=*) OUT_ROOT="${a#*=}" ;;
    --build-commit=*) BUILD_COMMIT_OVERRIDE="${a#*=}" ;;
    --web-build-commit=*) WEB_COMMIT_OVERRIDE="${a#*=}" ;;
    --close-state-file=*) CLOSE_STATE_FILE="${a#*=}" ;;
    --status-file=*) STATUS_FILE="${a#*=}" ;;
    --snapshot-ok-file=*) SNAPSHOT_OK_FILE="${a#*=}" ;;
    --config-change-registered=*) CONFIG_REGISTERED="${a#*=}" ;;
    --capacity-factor=*) CAPACITY_FACTOR="${a#*=}" ;;
    --volume-max-bytes=*) VOLUME_MAX_BYTES="${a#*=}" ;;
    --big-file-min-bytes=*) BIG_FILE_MIN_BYTES="${a#*=}" ;;
    --frozen-confirmed) FROZEN_CONFIRMED=1 ;;
    --skip-secrets) SKIP_SECRETS=1 ;;
    -h|--help) sed -n '1,73p' "$0"; exit 0 ;;
    *) printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=bad_argument arg=%s\n' "$a"; exit 1 ;;
  esac
done
[ -n "$PENDING" ] && \
  { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=missing_option_value option=--%s\n' "$PENDING"; exit 1; }
[ "$VERIFY_DIR" != "__pending__" ] || VERIFY_DIR=""
[ "$HOST_ARG" != "__pending__" ] || HOST_ARG=""

[ -f "$HELPER" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=helper_absent\n'; exit 1; }
[ -f "$SNAP_PY" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=snap_py_absent\n'; exit 1; }
command -v python3 >/dev/null 2>&1 || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=python3_absent\n'; exit 1; }
command -v openssl >/dev/null 2>&1 || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=openssl_absent\n'; exit 1; }

BACKUP_ID="cold-$(TZ=Asia/Shanghai date +%Y%m%d-%H%M%S)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/quant-cold-XXXXXX")" || \
  { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=workdir_failed\n'; exit 1; }
mkdir -p "$WORK/spec" "$WORK/meta" "$WORK/volumes" "$WORK/reports" "$WORK/logs" "$WORK/stage"
chmod 700 "$WORK"

say() { printf '%s %s\n' "$(TZ=Asia/Shanghai date '+%F %T')" "$*"; }
abort() { # $1=step $2=reason [$3=extra]
  printf 'COLD_BACKUP_RESULT=ABORT step=%s reason=%s %s\n' "$1" "$2" "${3:-}"
  say "X 冷备中止：step=$1 reason=$2 ${3:-}"
  exit 1
}
sha_file() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  else sha256sum "$1" | cut -d' ' -f1; fi
}
file_mode() { # 八进制权限位（BSD stat 与 GNU stat 两种写法都认；取不到就给 unknown）
  stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1" 2>/dev/null || printf 'unknown'
}
field() { # $1=管道读数（key=value|key=value）$2=键
  printf '%s' "$1" | tr '|' '\n' | { grep -E "^$2=" || true; } | head -1 | cut -d= -f2-
}
json_field() { # $1=json $2=点路径 -> 标量（解析不出给空串，绝不猜）
  printf '%s' "$1" | JSON_KEY="$2" python3 -c '
import json, os, sys
try:
    body = json.load(sys.stdin)
except Exception:
    print("")
    raise SystemExit(0)
cur = body
for part in os.environ["JSON_KEY"].split("."):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        print("")
        raise SystemExit(0)
print("" if isinstance(cur, (dict, list)) else cur)
' 2>/dev/null
}

# protected_target —— §4.5 不可再生资产闸：基线落点、任何写/删目标都要过它。
# 按"路径分段"而不是字符串前缀判定：默认基线根 $HOME/backups/quant-cold 与被保护的
# $HOME/backups/quant 只差一个连字符，前缀判定会把合法落点判成违规（也会放过 quantx）。
# 两种错都会让人不再信这条闸。
PROTECTED_PATHS=()
build_protected() {
  PROTECTED_PATHS=("$HOME/backups/quant/restic" "$HOME/backups/quant" "$HOME/.quant-trading-v2"
                  "$APP_DIR/docs" "$APP_DIR/tools/pub_uat" "$APP_DIR/bt_jq" "$APP_DIR/.git"
                  "$APP_DIR/AGENTS.md" "$APP_DIR/PROJECT_DOCUMENT.md" "$APP_DIR/README.md")
  local extra
  for extra in ${PROTECTED_EXTRA:-}; do PROTECTED_PATHS+=("$extra"); done
}
protected_target() { # $1=path -> 0 = 受保护（拒绝）
  local p="${1%/}" one
  [ -n "$p" ] || return 1
  case "$p" in "$APP_DIR"|"$HOME") return 0 ;; esac
  for one in "${PROTECTED_PATHS[@]}"; do
    one="${one%/}"
    [ -n "$one" ] || continue
    case "$p" in "$one"|"$one"/*) return 0 ;; esac
  done
  return 1
}
cleanup() {
  # 半途退出（abort、信号）不许把基线留在**可写**状态：只读封存才是它的默认形态。
  # 增量补备一进来就 unseal，后面任何一步失败都会直接 exit —— 没有这一条，
  # "失败的那一轮"顺手就把封存好的基线变成可改写的目录了（下一轮之前没人会发现）。
  [ "${UNSEALED:-0}" = "1" ] && seal
  # 只清自己的 temp；万一 WORK 被指到受保护面（不会，但这条闸不该只活在推理里），宁可留垃圾。
  protected_target "$WORK" && return 0
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

# ── 传输形态装配（远端必须**显式点名**，缺省不再"顺手连生产"）────────────────────────
# 为什么要有这条闸（§5 S0 第 2 条 + 本仓 11-02 那次 ssh 静默回退密码认证挂死 30 分钟）：
# 旧形态是"没给 --local-source 就走 ssh"，于是 `--dry-run` 单独一敲也会去连现网——
# 名字说着"干跑"，脚已经踏进别人家机器。现在远端只认 --host 这一把钥匙，谁点谁负责。
if [ -n "$LOCAL_SOURCE" ]; then
  TRANSPORT="local"
  [ -d "$LOCAL_SOURCE" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=local_source_absent path=%s\n' "$LOCAL_SOURCE"; exit 1; }
  ABS_ROOT="$LOCAL_SOURCE"
  STAGE="$WORK/stage"
  WIRING_ABS_ROOT="${LOCAL_SOURCE%/}-wiring"
  SSH_HOST=""
elif [ "$MODE" = "verify" ]; then
  # 校验模式只读**本机已经封存好的基线**：既不连现网，也不碰源端目录。
  # 周六想确认"那份基线还成立吗"不该被 close_state 或一根 ssh 挡住。
  TRANSPORT="baseline"
  ABS_ROOT="(baseline-read-only)"
  STAGE="$WORK/stage"
  WIRING_ABS_ROOT=""
  SSH_HOST=""
elif [ -n "$HOST_ARG" ]; then
  TRANSPORT="ssh"
  SSH_HOST="$HOST_ARG"
  GZ_USER="${GZ_USER:-Administrator}"
  SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes ${GZ_USER}@${SSH_HOST}"
  SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes"
  ABS_ROOT="$SRC_ROOT"
  STAGE="$STAGE_ROOT/$BACKUP_ID"
  WIRING_ABS_ROOT="$WIRING_ROOT"
  printf 'S0_TRANSPORT mode=ssh host=%s read_only_remote=1 remote_writes=none gate=explicit--host\n' "$SSH_HOST"
  say "WARN - 现网模式：目标 host=${SSH_HOST}（只读枚举/在线快照/取件；本脚本不停服务、不改配置、不删远端文件）。演练请用 --local-source。"
else
  printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=source_undetermined need=--local-source=<dir>_or_--host=<name>\n'
  say "X 没点名数据源：演练用 --local-source=<目录>，真现网用 --host=<ssh 别名>。缺省不猜、不默认连生产。"
  exit 1
fi
OUT_ABS="$OUT_ROOT/$BACKUP_ID"
if [ "$MODE" = "incremental" ]; then
  [ -n "$BASE_DIR" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=3b reason=base_required\n'; exit 1; }
  OUT_ABS="$BASE_DIR"
fi
if [ "$MODE" = "verify" ]; then
  [ -n "$VERIFY_DIR" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=verify_baseline_required\n'; exit 1; }
  [ -d "$VERIFY_DIR" ] || { printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=verify_baseline_absent path=%s\n' "$VERIFY_DIR"; exit 1; }
  OUT_ABS="$VERIFY_DIR"
fi
build_protected
if protected_target "$OUT_ABS"; then
  printf 'COLD_BACKUP_RESULT=ABORT step=0 reason=baseline_target_protected path=%s\n' "$OUT_ABS"
  say "X 基线落点撞上 §4.5 不可再生资产，拒绝写入：$OUT_ABS"
  exit 1
fi

remote_run() { # $1=远端命令（只读）；回传去 CR
  local out rc
  out="$($SSH "$1" 2>&1)"; rc=$?
  printf '%s' "$out" | tr -d '\r'
  return $rc
}
LEAK_STEP="-"
leak_check() { # 回传文本里 40+ 位连续十六进制 = 疑似明文口令：命中即停手，值不打印
  # 但先剥掉"带标签的哈希读数"（sha256=<64hex> 这类）：本工具的每卷/每文件读数都是 64 位
  # 十六进制，不按标签剥掉就会把自家的度量当成泄露，每次都红——红到没人看的那条锁等于没锁。
  # 剩下的裸 40+ hex（尤其带 token/pass/key 字样或无标签出现的）才是这条闸要拦的东西。
  printf '%s' "$1" | sed -E 's/(sha256|sha|fp|fingerprint)[=:][0-9a-fA-F]{8,64}//g' |
    grep -qiE '[0-9a-f]{40,}'
}
helper_run() { # $1=spec $2=label -> stdout=读数；rc 0 成功 / 3 明文闸 / 4 spec 编译失败 / 其它=该步失败
  # rc=3 与"这一步自己失败"必须分开：混成一个非零值会把失败原因写反——
  # "疑似明文口令"会让人去查钥匙，"枚举失败"会让人去查路径（11-02 那两次假象同源）。
  local spec="$1" label="$2" out rc job
  # 判别只看"是不是 ssh 现网形态"，而不是"是不是 local"：
  # 本工具现在有 local（演练/本机数据根）、baseline（只读校验已封存基线）、ssh 三种形态，
  # 按 "!= local 就走远端" 写会把 baseline 当成现网去 scp（$SSH 那时根本就是空的）。
  if [ "$TRANSPORT" != "ssh" ]; then
    out="$(python3 "$HELPER" --spec="$spec" 2>&1)"; rc=$?
  else
    job="$(python3 "$HELPER" --mode=spec --spec="$spec" 2>/dev/null | { grep -E '^JOB_B64\|' || true; } | head -1 | cut -d'|' -f2)"
    if [ -z "$job" ]; then
      say "WARN - [$label] spec 未能编成 job（先修 spec 再看远端）"
      return 4
    fi
    # 远端只跑一条只读的 python 取数/打包命令；脚本原文走 stdin，参数走一枚 base64（无 shell 元字符）
    out="$($SSH "$REMOTE_PY - --job=$job" < "$HELPER" 2>&1 | tr -d '\r')"; rc=$?
  fi
  printf '%s\n' "$out" > "$WORK/logs/$label.log"
  if leak_check "$out"; then
    say "WARN - [$label] 回传里出现 40 位以上连续十六进制串（疑似明文口令）；已停手，未打印内容"
    printf '%s\n' "$out"
    return 3
  fi
  printf '%s\n' "$out"
  return $rc
}
LAST_OUT=""
# secrets_spec_lines —— 把 B 类容器那几行 manifest 输入打给 spec 生成器，**保证行尾有换行**。
# 为什么单独写成一函数：SECRETS_SPEC 在加密成功后要过一道 `$(printf '%s' … | sed …)`，
# 命令替换会**吃掉结尾的换行**（bash 的一贯行为），于是 `printf '%s' "$SECRETS_SPEC"` 紧跟
# `printf 'header.backup_id=%s\n'` 时得到的是 `secret=…|18464header.backup_id=cold-…` 这一行
# —— parse_spec 把 18464header 当成 container_bytes 去 int()，manifest 步当场 bad_spec，
# 整条 v1 链没有产物（今天 27/64 的那颗根雷就是这个）。
secrets_spec_lines() { [ -n "$SECRETS_SPEC" ] && printf '%s\n' "$SECRETS_SPEC"; return 0; }
run_step() { # $1=spec $2=label $3=step $4=失败时的 reason
  # 读数留在全局 LAST_OUT：这条腿**不能**被 $( ) 包起来调用——命令替换会开子 shell，
  # 子 shell 里的 exit 只杀死自己，父脚本带着"没产物"继续往下跑，最后在一句不相干的
  # cp 上崩，真实成因（这一步失败）被埋掉。今天真踩：明文闸误报后就是这副样子。
  local spec="$1" label="$2" step="$3" reason="$4" rc=0
  LAST_OUT="$(helper_run "$spec" "$label")"; rc=$?
  if [ "$rc" = "3" ]; then abort "$step" leak_guard_hit "label=$label"; fi
  if [ "$rc" != "0" ]; then
    printf '%s\n' "$LAST_OUT" | { grep -E '^HELP_RESULT=FAIL' || true; }
    abort "$step" "$reason" "label=$label rc=$rc see=logs/$label.log"
  fi
  printf '%s\n' "$LAST_OUT" | { grep -q 'HELP_RESULT=OK' || abort "$step" helper_failed "label=$label"; }
}

fetch_to() { # $1=stage 相对名 $2=本地落点
  if [ "$TRANSPORT" != "ssh" ]; then
    cp "$STAGE/$1" "$2"
  else
    $SCP "${GZ_USER}@${SSH_HOST}:$STAGE/$1" "$2" >/dev/null 2>&1
  fi
}
push_to_stage() { # $1=本地文件 $2=stage 相对名（增量补备要把 v1 manifest 送回去给 scan 当尺子）
  if [ "$TRANSPORT" != "ssh" ]; then
    cp "$1" "$STAGE/$2"
  else
    $SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path '$STAGE' | Out-Null\"" >/dev/null 2>&1
    $SCP "$1" "${GZ_USER}@${SSH_HOST}:$STAGE/$2" >/dev/null 2>&1
  fi
}
ensure_stage() {
  [ "$TRANSPORT" != "ssh" ] && { mkdir -p "$STAGE"; return 0; }
  $SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path '$STAGE' | Out-Null\"" >/dev/null 2>&1 ||
    abort 2 stage_create_failed "path=$STAGE"
}
# 数据根的排除清单（§4.7 C 类 + B 类改道 + 在线快照替换裸库）。每条必须带理由类别：
# 没有理由的排除，日后看起来就跟"它本来就不在"一模一样。
DATA_EXCLUDES=(
  "trading.db=db_raw_replaced_by_online_snapshot"
  "live.db=db_raw_replaced_by_online_snapshot"
  "qmt_gateway/*=bridge_gateway"
  "*bridge_report*.jsonl=bridge_gateway"
  "bridge_cmd.json=bridge_gateway"
  "*.exe=windows_deploy"
  "*.ps1=windows_deploy"
  "__pycache__/*=test_junk"
  "*.pyc=test_junk"
  ".pytest-tmp/*=test_junk"
  "node_modules/*=test_junk"
  ".uat-data/*=test_junk"
  "backups/*=rebuildable_snapshot"
  "config_history/rules/*=secret_class_b"
  "admin_session_token=secret_class_b"
  "quant.env=secret_class_b"
  "config.xt.json=secret_class_b"
)
enum_spec() { # $1=tag $2=hash(0|1) $3=spec 路径 $4=manifest（可选，给 scan）$5=since（可选）
  local tag="$1" hash="$2" path="$3" man="${4:-}" since="${5:-0}" db name
  {
    if [ -n "$man" ]; then
      printf 'mode=scan\nwork=%s\nmanifest=%s\nsince=%s\n' "$STAGE" "$STAGE/$man" "$since"
      [ -n "$CONFIG_REGISTERED" ] && printf 'config_registered=%s\n' "$CONFIG_REGISTERED"
    else
      printf 'mode=enumerate\ntag=%s\nwork=%s\n' "$tag" "$STAGE"
    fi
    printf 'root=%s||data|%s\n' "$ABS_ROOT" "$hash"
    for pair in "${DATA_EXCLUDES[@]}"; do
      printf 'exclude=%s|%s\n' "${pair%%=*}" "${pair#*=}"
    done
    # 以下四条例外在 scan（增量）形态下**同样要带**：两库的在线快照、接线件都在 <DataDir> 之外，
    # 少了它们，增量就会把"库变了"读成"什么都没变"——正是 §5 S3b 要防的那件事。
    # 快照进包时**前缀必须是空**：相对路径要跟原库同名（回传侧直接对位），
    # 把库名当前缀写出来会得到 trading.dbtrading.db 这种谁也对不上的路径。
    for db in $DB_NAMES; do
      # 第 5 段 content_only=1：快照的 mtime 是我们自己打的，不能当"冻结后有变化"的证据，
      # 否则每次增量都会把两本账当新件重传一遍（§5 S3b 要的是只挑真变化的那几条）。
      printf 'root=%s||snapshot|%s|1\n' "$STAGE" "$hash"
      printf 'only=%s\n' "$db"
    done
    # 服务确实停过时 -wal/-shm 也一并带走（快照已含其内容，这份是现场证据，落在 wal-sidecar/ 前缀下）
    printf 'root=%s|wal-sidecar/|wal_sidecar|%s\n' "$ABS_ROOT" "$hash"
    for db in $DB_NAMES; do
      for name in "-wal" "-shm"; do printf 'only=%s%s\n' "$db" "$name"; done
    done
    # §4.4 A4 接线件：前端 dist（含 BUILD_COMMIT）与 Caddyfile 现网落盘版
    for d in ${WIRING_DIRS//,/ }; do
      printf 'root=%s/%s|wiring/%s/|wiring|%s\n' "$WIRING_ABS_ROOT" "$d" "$d" "$hash"
    done
    for f in ${WIRING_FILES//,/ }; do
      printf 'root=%s|wiring/|wiring|%s\n' "$WIRING_ABS_ROOT" "$hash"
      printf 'only=%s\n' "$f"
    done
    if [ -n "$APK_DIR" ]; then
      if [ "$TRANSPORT" = "ssh" ] || [ -d "$APK_DIR" ]; then
        printf 'root=%s|wiring/apk/|wiring|%s\n' "$APK_DIR" "$hash"
      fi
    fi
  } > "$path"
}

seal() { # 封存 = 只读；此后只做增量补备，不再改基线（§5 S3 第 4 步）
  protected_target "$OUT_ABS" && return 1
  chmod -R a-w "$OUT_ABS" 2>/dev/null || true
  UNSEALED=0
  # 唯一例外：B 类加密容器按 §4.6 的口径保持 **0600**。
  # `a-w` 会把它从 0600 悄悄改成 0400 —— 两件同名东西给两个答案，回传侧照 0600 验钥匙件就会
  # 在"封存好的基线"上判失败；而容器要的从来不是"只读"，是**除属主谁都不能读**。
  # 逐件点名打印，不让人靠推理确认这条例外真的落到了盘上。
  local sealed_file
  for sealed_file in "$OUT_ABS"/secrets/*.enc; do
    [ -e "$sealed_file" ] || continue
    chmod 600 "$sealed_file" 2>/dev/null || true
    printf 'S3_SEAL_CONTAINER file=%s mode=%s\n' "$(basename "$sealed_file")" "$(file_mode "$sealed_file")"
  done
}
unseal() {
  protected_target "$OUT_ABS" && return 1
  chmod -R u+w "$OUT_ABS" 2>/dev/null || true
  UNSEALED=1
}
VOL_LANDED=0
fetch_volumes() { # $1=volumes.json(本地) $2=落地目录 $3=stage 内子目录名
  # 逐卷 sha 复算；不符只重传**该卷**（三次后判红）。计数写文件而不是走 stdout，
  # 因为 V_ARRIVED 读数必须让 owner 直接看到，而 $( ) 取返回值会把它们吞进变量里。
  local jf="$1" dest="$2" subdir="$3" name want_sha want_bytes landed attempt got_sha got_bytes
  mkdir -p "$WORK/volumes" "$dest" || abort 2 baseline_write_failed "dir=$dest"
  python3 - "$jf" > "$WORK/meta/volume_names.txt" <<'PY'
import json, sys
for v in json.load(open(sys.argv[1]))["volumes"]:
    print("%s\t%s\t%s" % (v["name"], v["sha256"], v["bytes"]))
PY
  VOL_LANDED=0
  while IFS=$'	' read -r name want_sha want_bytes; do
    [ -n "$name" ] || continue
    landed="$WORK/volumes/$name"
    attempt=1
    while :; do
      fetch_to "$subdir/$name" "$landed" || say "WARN - 卷取回失败：$name attempt=$attempt"
      got_sha="$(sha_file "$landed" 2>/dev/null || echo none)"
      got_bytes="$(wc -c < "$landed" 2>/dev/null | tr -d ' ' || echo 0)"
      if [ "$got_sha" = "$want_sha" ] && [ "$got_bytes" = "$want_bytes" ]; then
        printf 'V_ARRIVED|name=%s|bytes=%s|sha256=%s|attempt=%d\n' "$name" "$got_bytes" "${got_sha:0:16}" "$attempt"
        cp "$landed" "$dest/$name" || abort 2 baseline_write_failed "vol=$name"
        VOL_LANDED=$((VOL_LANDED + 1))
        break
      fi
      attempt=$((attempt + 1))
      if [ "$attempt" -gt 3 ]; then
        abort 2 volume_verify_failed "name=$name want=${want_sha:0:16} got=${got_sha:0:16}"
      fi
      say "WARN - 卷校验不符，**单独重传该卷**（不是整份重跑）：$name attempt=$attempt"
      rm -f "$landed"
    done
  done < "$WORK/meta/volume_names.txt"
  printf '%s' "$VOL_LANDED" > "$WORK/meta/vol_landed.txt"
}

# db_snapshot —— 在线 .backup（backup_snap.py 那把尺子，禁止裸 cp）。
# 定义位置必须在 S0 之前：S3b 增量分支比 S1 早执行，而 bash 的函数是"运行到定义那一行才注册"的，
# 把函数写在 S1 段落里，增量腿就会 `command not found` —— 今天的形状正是这样：两条库根本没做快照，
# scan 把 live.db/trading.db 读成"冻结后被删除"，v2 的删除清单里凭空多出两本账（§5 S3b 最坏的形态：
# 增量把"我们的快照腿没接上"上报成"源端把库删了"）。
db_snapshot() { # $1=db 名
  local db="$1" out rc
  if [ "$TRANSPORT" != "ssh" ]; then
    out="$(python3 "$SNAP_PY" "$ABS_ROOT/$db" "$STAGE/$db" 2>&1)"; rc=$?
  else
    # 脚本原文走 stdin、两个路径走参数：现网不要求这台机上有第二份副本，也不落任何临时脚本文件
    out="$($SSH "$REMOTE_PY - $ABS_ROOT/$db $STAGE/$db" < "$SNAP_PY" 2>&1 | tr -d '\r')"; rc=$?
  fi
  printf '%s\n' "$out" > "$WORK/reports/db-$db.log"
  if [ $rc -ne 0 ]; then
    abort 1 db_snapshot_failed "db=$db rc=$rc see=reports/db-$db.log"
  fi
  printf 'S1_DB %s\n' "$out"
}

# ── --verify <baseline>：照**存好的清单**把 L1/L2 再跑一遍（只读，一个字节都不写基线）──
# 为什么要单独一条（§5 S3 与 §9 R1）：基线封存后到腾讯云重置之间有好几个小时，
# 期间"那份基线还成立吗"必须能随时问一句就有答案，而不是靠"当时跑绿过"的记忆。
# 它也不看收盘态、不估容量、不连源端 —— 周六晚上八点照样能验。
if [ "$MODE" = "verify" ]; then
  CUR_NAME="$(cat "$OUT_ABS/BASELINE_MANIFEST" 2>/dev/null | tr -d '\r\n' || true)"
  [ -n "$CUR_NAME" ] || CUR_NAME="BACKUP_MANIFEST.json"
  case "$CUR_NAME" in
    */*|*..*) abort 0 verify_pointer_bad "name=$CUR_NAME" ;;
  esac
  VER_MAN="$OUT_ABS/$CUR_NAME"
  [ -f "$VER_MAN" ] || abort 0 verify_manifest_absent "path=$VER_MAN"
  printf 'VERIFY_TARGET baseline=%s manifest=%s\n' "$OUT_ABS" "$CUR_NAME"
  # 卷名 -> 路径：普通卷 + 每一轮的增量卷都扫进去。清单里在册的卷少一本，
  # 就等于"拿一份不全的卷集去证明这份清单成立"——那是假绿里最贵的一种。
  VPATH_LINES=""
  VOL_PRESENT=0
  for VOLDIR in volumes volumes-delta; do
    for VOLFILE in "$OUT_ABS/$VOLDIR"/*.tar.gz "$OUT_ABS/$VOLDIR"/*.tar; do
      [ -e "$VOLFILE" ] || continue
      VPATH_LINES="${VPATH_LINES}vpath=$(basename "$VOLFILE")|${VOLFILE}"$'\n'
      VOL_PRESENT=$((VOL_PRESENT + 1))
    done
  done
  PLANNED_VOLS="$(python3 -c '
import json, sys
print(len(json.load(open(sys.argv[1]))["volumes"]))
' "$VER_MAN" 2>/dev/null || echo 0)"
  printf 'VERIFY_VOLUMES planned=%s present=%s\n' "${PLANNED_VOLS:-0}" "$VOL_PRESENT"
  [ "${PLANNED_VOLS:-0}" != "0" ] || abort 0 verify_manifest_has_no_volumes "manifest=$VER_MAN"
  [ "${PLANNED_VOLS:-0}" -le "$VOL_PRESENT" ] ||
    abort 3 verify_volumes_missing "planned=${PLANNED_VOLS} present=$VOL_PRESENT dir=$OUT_ABS/volumes"
  # accounts/ 交叉读数的参照：清单头部当时记下的那个数。
  # 现网那份 SNAPSHOT_OK 是夜间快照的产物，隔一天就是**另一天的账**，拿它校验昨天的基线
  # 会把两个交易日的账号数当成差异——参照物必须是基线自己。
  HDR_ACCOUNTS="$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1]))["header"].get("accounts_files", ""))
' "$VER_MAN" 2>/dev/null || true)"
  MARKER_ARG=""
  if [ -n "$HDR_ACCOUNTS" ]; then
    printf '{"accounts_files":%s}\n' "$HDR_ACCOUNTS" > "$WORK/meta/verify_marker.json"
    MARKER_ARG="$WORK/meta/verify_marker.json"
    printf 'VERIFY_MARKER accounts_files_from_manifest=%s\n' "$HDR_ACCOUNTS"
  else
    printf 'VERIFY_MARKER accounts_files_from_manifest=absent crosscheck_skipped=1\n'
  fi
  SPEC_VL1="$WORK/spec/verify-l1.spec"
  {
    printf 'mode=verify\nmanifest=%s\nvolumes_dir=%s\ncheck_excluded=1\n' "$VER_MAN" "$OUT_ABS/volumes"
    printf '%s' "$VPATH_LINES"
  } > "$SPEC_VL1"
  vl1_out="$(helper_run "$SPEC_VL1" verify-l1)"; vl1_rc=$?
  [ "$vl1_rc" = "3" ] && abort 2 leak_guard_hit "label=verify-l1"
  printf '%s\n' "$vl1_out" | { grep -E '^L1_' || true; }
  [ $vl1_rc -eq 0 ] || abort 3 l1_failed "see=logs/verify-l1.log manifest=$CUR_NAME"
  SPEC_VL2="$WORK/spec/verify-l2.spec"
  {
    printf 'mode=l2\nmanifest=%s\nvolumes_dir=%s\n' "$VER_MAN" "$OUT_ABS/volumes"
    printf '%s' "$VPATH_LINES"
    [ -n "$MARKER_ARG" ] && printf 'snapshot_ok=%s\n' "$MARKER_ARG"
  } > "$SPEC_VL2"
  vl2_out="$(helper_run "$SPEC_VL2" verify-l2)"; vl2_rc=$?
  [ "$vl2_rc" = "3" ] && abort 2 leak_guard_hit "label=verify-l2"
  printf '%s\n' "$vl2_out" | { grep -E '^L2' || true; }
  [ $vl2_rc -eq 0 ] || abort 3 l2_failed "see=logs/verify-l2.log manifest=$CUR_NAME"
  printf 'COLD_BACKUP_RESULT=OK baseline=%s verify=%s l1=OK l2=OK writes=0\n' "$OUT_ABS" "$CUR_NAME"
  say "ok - 校验通过：$CUR_NAME 与卷集逐文件/逐结构都对得上（基线一个字节没动）"
  exit 0
fi

# ── S0 预检（全只读）──────────────────────────────────────────────────────────────
say "==> [S0] 预检 transport=$TRANSPORT root=$ABS_ROOT"
if [ "$TRANSPORT" = "ssh" ]; then
  probe="$(remote_run "echo ok")" || abort 0 ssh_batchmode_failed "host=$SSH_HOST"
  printf '%s' "$probe" | { grep -q 'ok' || abort 0 ssh_batchmode_failed "unexpected_probe_output"; }
  say "ok - BatchMode 通道可用（不通就已经停在这里；本脚本不使用密码认证）"
fi

TODAY_BJ="$(TZ=Asia/Shanghai date +%F)"
CLOSE_STATE="not_evaluated"
close_guard() {
  local body="" day="" at="" src=""
  if [ -n "$CLOSE_STATE_FILE" ] && [ -f "$CLOSE_STATE_FILE" ]; then
    body="$(cat "$CLOSE_STATE_FILE")"; src="file"
  elif [ -n "${ADMIN_TOKEN:-}" ]; then
    body="$(curl -fsS --max-time 20 -H "Authorization: Bearer ${ADMIN_TOKEN}" \
      "${GZ_BASE_URL}/api/qmt/close-snapshot" 2>/dev/null || true)"; src="http"
  fi
  [ -n "$body" ] || { CLOSE_STATE="unreadable"; return 1; }
  day="$(json_field "$body" close_snapshot.day)"; [ -n "$day" ] || day="$(json_field "$body" day)"
  at="$(json_field "$body" close_snapshot.at)"; [ -n "$at" ] || at="$(json_field "$body" at)"
  if [ "$day" != "$TODAY_BJ" ] || [ -z "$at" ]; then
    CLOSE_STATE="fail day=${day:-empty} expected=$TODAY_BJ at=${at:-empty} src=$src"
    return 1
  fi
  CLOSE_STATE="OK day=$day at=$at src=$src"
  return 0
}
if ! close_guard; then
  if [ "$DRY_RUN" = "1" ]; then
    say "INFO - 收盘态读数：${CLOSE_STATE}（干跑不拦；真跑这一步就是 ABORT，R14）"
  else
    case "$CLOSE_STATE" in
      unreadable*) abort 0 close_state_unreadable "need=--close-state-file_or_ADMIN_TOKEN" ;;
      *) abort 0 close_state_not_today "$CLOSE_STATE" ;;
    esac
  fi
fi
printf 'S0_CLOSE_STATE %s\n' "$CLOSE_STATE"

FROZEN_AT="$(TZ=Asia/Shanghai date '+%Y-%m-%dT%H:%M:%S%z')"
FROZEN_EPOCH="$(date +%s)"
GIT_HEAD="$(git -C "$APP_DIR" rev-parse HEAD 2>/dev/null || echo unknown)"
BUILD_COMMIT="${BUILD_COMMIT_OVERRIDE:-unreachable}"
if [ -z "$BUILD_COMMIT_OVERRIDE" ]; then
  status_body=""
  if [ -n "$STATUS_FILE" ] && [ -f "$STATUS_FILE" ]; then status_body="$(cat "$STATUS_FILE")"
  elif [ -n "${ADMIN_TOKEN:-}" ]; then
    status_body="$(curl -fsS --max-time 20 -H "Authorization: Bearer ${ADMIN_TOKEN}" \
      "${GZ_BASE_URL}/api/status" 2>/dev/null || true)"
  fi
  got_bc="$(json_field "$status_body" build_commit)"
  [ -n "$got_bc" ] && BUILD_COMMIT="$got_bc"
fi
WEB_COMMIT="${WEB_COMMIT_OVERRIDE:-unreadable}"
if [ -z "$WEB_COMMIT_OVERRIDE" ]; then
  if [ "$TRANSPORT" = "local" ]; then
    [ -f "$WIRING_ABS_ROOT/web/BUILD_COMMIT" ] && WEB_COMMIT="$(tr -d '\r\n' < "$WIRING_ABS_ROOT/web/BUILD_COMMIT")"
  else
    got="$(remote_run "type ${WIRING_ABS_ROOT}/web/BUILD_COMMIT" 2>/dev/null || true)"
    got="$(printf '%s' "$got" | tr -d '\r\n' | tr -d '[:space:]')"
    [ -n "$got" ] && WEB_COMMIT="$got"
  fi
fi
printf 'S0_ANCHORS frozen_at=%s build_commit=%s web_build_commit=%s git_head=%s source_root=%s\n' \
  "$FROZEN_AT" "$BUILD_COMMIT" "$WEB_COMMIT" "$GIT_HEAD" "$ABS_ROOT"

# SNAPSHOT_OK：夜间快照自己的产物标记（backup_snapshot.ps1 的七字段）。accounts/ 的文件数
# 拿它交叉一遍——两个数各说各话就是"备份链某条腿悄悄停了"的形状，所以 S0 就取，增量也用它。
MARKER_ARG=""
if [ -n "$SNAPSHOT_OK_FILE" ] && [ -f "$SNAPSHOT_OK_FILE" ]; then
  MARKER_ARG="$SNAPSHOT_OK_FILE"
elif [ "$TRANSPORT" = "ssh" ]; then
  remote_run "type $SNAPSHOT_MARK_REMOTE" > "$WORK/meta/SNAPSHOT_OK" 2>/dev/null || true
  [ -s "$WORK/meta/SNAPSHOT_OK" ] && MARKER_ARG="$WORK/meta/SNAPSHOT_OK"
fi
if [ -n "$MARKER_ARG" ]; then
  printf 'S0_SNAPSHOT_MARKER file=%s accounts_files=%s\n' "$MARKER_ARG" \
    "$(json_field "$(cat "$MARKER_ARG")" accounts_files)"
else
  printf 'S0_SNAPSHOT_MARKER file=none accounts_files=crosscheck_skipped\n'
fi

# 枚举是包体的输入（§5 S0 第 3 条）。hash=0 这一趟只为算体量与文件全集。
enum_spec "estimate" 0 "$WORK/spec/enum-estimate.spec"
run_step "$WORK/spec/enum-estimate.spec" enum-estimate 0 enumeration_failed
est_out="$LAST_OUT"
printf '%s\n' "$est_out" | { grep -E '^ENUM_SUMMARY\||^ENUM_NOTE' || true; }
EST_LINE="$(printf '%s\n' "$est_out" | { grep -E '^ENUM_SUMMARY\|' || true; } | head -1)"
[ -n "$EST_LINE" ] || abort 0 enumeration_failed "no_summary_reading"
PAYLOAD_BYTES="$(field "$EST_LINE" bytes)"
PAYLOAD_FILES="$(field "$EST_LINE" files)"
EXCLUDED_N="$(field "$EST_LINE" excluded)"

# 体量估算必须把两库自己加回去：枚举把裸库标成"由在线快照替换"，而快照此刻还没生成，
# 于是 ENUM_SUMMARY 的 bytes **不含**最大的那两枚文件（3.5-7.2 GB 量级）。
# 拿它直接做容量判定等于"永远通过"——那正是 R1（盘满留半份却看起来跑完了）的成因。
DB_EXTRA_BYTES=0
for db in $DB_NAMES; do
  if [ "$TRANSPORT" = "local" ]; then
    one="$(wc -c < "$ABS_ROOT/$db" 2>/dev/null | tr -d ' ' || echo 0)"
  else
    one="$(remote_run "powershell -NoProfile -Command \"(Get-Item '${SRC_ROOT}/${db}').Length\"" | { grep -E '^[0-9]+$' || true; } | head -1)"
  fi
  [ -n "${one:-}" ] || one=0
  DB_EXTRA_BYTES=$((DB_EXTRA_BYTES + one))
done
PAYLOAD_BYTES=$((PAYLOAD_BYTES + DB_EXTRA_BYTES))
printf 'S0_ESTIMATE payload_bytes=%s of_which_dbs=%s files=%s\n' "$PAYLOAD_BYTES" "$DB_EXTRA_BYTES" "$PAYLOAD_FILES"

# 容量：包体 x factor 装得下才开拉。不复核就开拉，大概率中途失败并留下半份"看起来跑过了"的备份（R1）。
# df 的目标取 **最近一个真实存在的祖先目录**：基线目录此刻本来就还没建（第一次跑就是
# --out-dir=~/backups/quant-cold/cold-<stamp>，父目录也可能不在），对着不存在的 path 问 df
# 会得到空读数 ⇒ capacity_probe_failed，把"今晚第一次跑"这条路自己堵死。
# 同一块盘上祖先与目标的余量一致，判据强度没变；探测点如实打出来，不让人猜用的是哪个。
DF_PROBE="$OUT_ROOT"
while [ ! -d "$DF_PROBE" ] && [ "$DF_PROBE" != "/" ]; do DF_PROBE="$(dirname "$DF_PROBE")"; done
FREE_KB="$(df -kP "$DF_PROBE" 2>/dev/null | { tail -1 || true; } | awk '{print $4}')"
[ -n "${FREE_KB:-}" ] || abort 0 capacity_probe_failed "df_returned_nothing probe_dir=$DF_PROBE"
if ! awk -v free="$FREE_KB" -v need="$PAYLOAD_BYTES" -v f="$CAPACITY_FACTOR" \
    'BEGIN{exit !(free*1024 >= need*f)}'; then
  abort 0 capacity_shortfall "free_kb=$FREE_KB probe_dir=$DF_PROBE payload_bytes=$PAYLOAD_BYTES factor=$CAPACITY_FACTOR"
fi
printf 'S0_CAPACITY free_kb=%s payload_bytes=%s factor=%s probe_dir=%s verdict=OK\n' \
  "$FREE_KB" "$PAYLOAD_BYTES" "$CAPACITY_FACTOR" "$DF_PROBE"
need_kb="$(awk -v b="$PAYLOAD_BYTES" -v f="$CAPACITY_FACTOR" 'BEGIN{printf "%d", b*f/1024}')"
printf 'S0_SINGLE_COPY_NOTE baseline=1copy_on_this_disk disaster_recovery=no offsite_chain=deploy/mac/restic_pull_backup.sh(owner_kept)\n'
printf 'S0_PLAN need_kb_estimate=%s out=%s stage=%s\n' "$need_kb" "$OUT_ABS" "$STAGE"

if [ "$MODE" = "preflight" ]; then
  # 预检＝"今天到底能不能开跑"这一句话的答复：全部只读，走到这里已经过了
  # 收盘态（不是今天就 ABORT）、容量（装不下就 ABORT）、§4.5 落点闸三道硬判据；
  # 剩下的（体量/文件数/排除数）只报数。零写入：基线目录一个字节都不碰，也不要求 --frozen-confirmed。
  printf 'COLD_BACKUP_RESULT=PREFLIGHT files=%s bytes=%s excluded=%s dbs=%s capacity_factor=%s free_kb=%s close_state=%s writes=0 out=%s\n' \
    "$PAYLOAD_FILES" "$PAYLOAD_BYTES" "$EXCLUDED_N" "$DB_NAMES" \
    "$CAPACITY_FACTOR" "$FREE_KB" "$(printf '%s' "$CLOSE_STATE" | tr ' ' '_')" "$OUT_ABS"
  say "ok - 预检通过（只读）。要出基线：加 --frozen-confirmed 跑 --full；演练请带 --local-source"
  exit 0
fi

if [ "$DRY_RUN" = "1" ]; then
  say "==> [DRY] 计划已打印；未建卷、未冻结、未写基线（真跑才会触碰网络与基线目录）"
  printf 'COLD_BACKUP_PLAN files=%s bytes=%s excluded=%s dbs=%s\n' \
    "$PAYLOAD_FILES" "$PAYLOAD_BYTES" "$EXCLUDED_N" "$DB_NAMES"
  printf 'COLD_BACKUP_PLAN volume_max=%s big_min=%s capacity_factor=%s\n' \
    "$VOLUME_MAX_BYTES" "$BIG_FILE_MIN_BYTES" "$CAPACITY_FACTOR"
  printf 'COLD_BACKUP_PLAN secrets_container=%s wiring_root=%s dirs=%s files=%s\n' \
    "$([ "$SKIP_SECRETS" = 1 ] && echo skipped_by_flag || echo encrypted_container)" \
    "$WIRING_ABS_ROOT" "$WIRING_DIRS" "$WIRING_FILES"
  printf 'COLD_BACKUP_RESULT=DRY files=%s bytes=%s baseline=%s\n' \
    "$PAYLOAD_FILES" "$PAYLOAD_BYTES" "$OUT_ABS"
  exit 0
fi

# ── S3b 增量补备（针对"新版本还在补充"；最新一版才是回传基线，更早的留作审计链）──────
if [ "$MODE" = "incremental" ]; then
  say "==> [S3b] 增量补备 base=$OUT_ABS"
  # 基线指针 BASELINE_MANIFEST 写的是"当前可回传的那一份 manifest 的文件名"。
  # 第一次补备时它还不存在（指针是 v2  blessed 时才落的）→ 缺省 v1。
  # 为什么必须跟着指针走而不是硬写 BACKUP_MANIFEST.json：第二次补备若仍以 v1 为尺，
  # 它会把 v2 已经收走的改动再搬一遍、并且**覆盖 v2**（§5 S3「只做增量补备，不再改基线」
  # 的反面），而且 v2 的卷会被留在原地没人引用。指针缺失但基线里另有 vN 时同样不许悄悄退回 v1。
  CUR_NAME="$(cat "$OUT_ABS/BASELINE_MANIFEST" 2>/dev/null | tr -d '\r\n' || true)"
  [ -n "$CUR_NAME" ] || CUR_NAME="BACKUP_MANIFEST.json"
  case "$CUR_NAME" in */*|*.json) ;; *) abort 3b baseline_pointer_bad "name=$CUR_NAME" ;; esac
  BASE_MAN="$OUT_ABS/$CUR_NAME"
  [ -f "$BASE_MAN" ] || abort 3b baseline_manifest_absent "path=$BASE_MAN pointer=1"
  [ -f "$OUT_ABS/BACKUP_MANIFEST.json" ] || abort 3b v1_manifest_absent "base=$OUT_ABS"
  BASE_VER="$(python3 -c '
import json, sys
print(int(json.load(open(sys.argv[1]))["manifest_version"]))
' "$BASE_MAN" 2>/dev/null || echo 0)"
  case "$BASE_VER" in ''|0) abort 3b baseline_manifest_unreadable "manifest=$BASE_MAN" ;; esac
  NEW_VER=$((BASE_VER + 1))
  NEW_NAME="BACKUP_MANIFEST.json"
  [ "$NEW_VER" = "1" ] || NEW_NAME="BACKUP_MANIFEST_v${NEW_VER}.json"
  BASE_REP="$OUT_ABS/reports/VERIFY_v${BASE_VER}.txt"
  { grep -q 'L1_RESULT=OK' "$BASE_REP" 2>/dev/null && grep -q 'L2_RESULT=OK' "$BASE_REP" 2>/dev/null; } ||
    abort 3b "v${BASE_VER}_never_verified" "report=$BASE_REP"
  V1_FROZEN_EPOCH="$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1]))["header"].get("frozen_at_epoch", 0))
' "$BASE_MAN" 2>/dev/null || echo 0)"
  [ "${V1_FROZEN_EPOCH:-0}" != "0" ] || abort 3b v1_anchor_unreadable "manifest=$BASE_MAN"
  # 增量比对的锚点：优先用上一轮补备的扫描时刻（rescan_at_epoch），没有才回到冻结时刻。
  # 一直用 frozen_at 的话，"已经收进 v2 的那件"因为 mtime 仍 > frozen_at，会被 v3 无限重搬，
  # 而测试要的"COLD_BACKUP_RESULT=SKIP reason=no_delta_since_frozen"永远到不了（§5 S3b）。
  SCAN_SINCE="$(python3 -c '
import json, sys
h = json.load(open(sys.argv[1]))["header"]
print(int(h.get("rescan_at_epoch") or h.get("frozen_at_epoch") or 0))
' "$BASE_MAN" 2>/dev/null || echo 0)"
  [ "${SCAN_SINCE:-0}" != "0" ] || abort 3b v1_anchor_unreadable "manifest=$BASE_MAN scan_since=0"
  V1_BACKUP_ID="$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1]))["header"].get("backup_id", ""))
' "$BASE_MAN" 2>/dev/null || echo unknown)"
  V1_SHA="$(sha_file "$BASE_MAN")"
  RESCAN_AT="$(TZ=Asia/Shanghai date '+%Y-%m-%dT%H:%M:%S%z')"
  RESCAN_EPOCH="$(date +%s)"
  if [ "$FROZEN_CONFIRMED" != "1" ]; then
    abort 1 freeze_unconfirmed "rerun_with=--frozen-confirmed (S3b 与 S1 同一份冻结纪律)"
  fi
  say "S3b_BASELINE backup_id=$V1_BACKUP_ID version=v$BASE_VER frozen_at_epoch=$V1_FROZEN_EPOCH scan_since_epoch=$SCAN_SINCE manifest_sha256=${V1_SHA:0:16}"
  say "S3b_NEXT manifest=$NEW_NAME version=v$NEW_VER audit_chain=keep_previous_volumes"
  say "S3b_NOTE B 类容器不随增量刷新；钥匙类文件在冻结后变更必须重跑整轮冷备（不是补备）"
  ensure_stage
  unseal || abort 3b baseline_target_protected "path=$OUT_ABS"
  mkdir -p "$OUT_ABS/reports" "$OUT_ABS/meta" "$WORK/volumes-delta" ||
    abort 3b baseline_write_failed "delta dirs"
  for db in $DB_NAMES; do
    [ "$TRANSPORT" = "ssh" ] || [ -e "$ABS_ROOT/$db" ] || abort 1 db_snapshot_failed "db_absent=$db"
    db_snapshot "$db"
  done
  # 卷集：基线 manifest 里在册的那些（v1 的卷 + 之前每一轮的增量卷）+ 本轮新卷。
  # 从 manifest 取而不是读 meta/volumes_v1.json：后者只有 v1 那一轮的卷，第二轮补备会把
  # v90 从卷集里丢掉——L1/L2 就会在"少一本卷"的集合上判 OK，而回传侧照 manifest 搬文件。
  python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
json.dump({"volumes": d.get("volumes", [])}, open(sys.argv[2], "w"), sort_keys=True)
' "$BASE_MAN" "$WORK/meta/volumes_base.json" 2>/dev/null ||
    abort 3b baseline_volumes_unreadable "manifest=$BASE_MAN"
  [ -s "$WORK/meta/volumes_base.json" ] || abort 3b baseline_volumes_empty "manifest=$BASE_MAN"
  push_to_stage "$BASE_MAN" "BACKUP_MANIFEST.json" || abort 3b push_failed "manifest v$BASE_VER"
  enum_spec "delta" 1 "$WORK/spec/scan.spec" "BACKUP_MANIFEST.json" "$SCAN_SINCE"
  scan_out="$(helper_run "$WORK/spec/scan.spec" scan)"; scan_rc=$?
  printf '%s\n' "$scan_out" | { grep -E '^SCAN_|^HELP_RESULT=' || true; }
  if [ "$scan_rc" = "3" ]; then abort 2 leak_guard_hit "label=scan"; fi
  if printf '%s' "$scan_out" | grep -q 'reason=config_change_unregistered'; then
    abort 3b config_change_unregistered "rerun_with=--config-change-registered='<改了什么/为什么>'"
  fi
  [ $scan_rc -eq 0 ] || abort 3b scan_failed "rc=$scan_rc see=logs/scan.log"
  SCAN_LINE="$(printf '%s\n' "$scan_out" | { grep -E '^SCAN_SUMMARY\|' || true; } | head -1)"
  [ -n "$SCAN_LINE" ] || abort 3b scan_failed "no_summary_reading"
  CHANGED="$(field "$SCAN_LINE" changed)"; ADDED="$(field "$SCAN_LINE" added)"
  DELETED="$(field "$SCAN_LINE" deleted)"; CONFIG_TOUCHED="$(field "$SCAN_LINE" config)"
  fetch_to "delta_meta.json" "$WORK/meta/delta_meta.json" || abort 3b transport_unreachable "file=delta_meta.json"
  fetch_to "enum_delta.json" "$WORK/meta/enum_delta.json" || abort 3b transport_unreachable "file=enum_delta.json"
  python3 -c '
import json, sys
for rel in json.load(open(sys.argv[1]))["deleted"]:
    print(rel)
' "$WORK/meta/delta_meta.json" > "$WORK/meta/deleted.txt" 2>/dev/null || true
  # deleted_chain —— 本轮删除 **并上** 基线已经记着的删除，再减去本轮又回来的那些。
  # 为什么必须并：卷集里 v1 的卷一直在（审计链），v(N) 的 manifest 若只记本轮新删的那几条，
  # 上一轮删掉的文件在本轮 L1 眼里就变成"L1_EXTRA：包里有个清单不认的件"——
  # 它既不是丢件也不是多件，是**已经收过账的删除**。清单不接着记，读的人只能重删一次或假红一次。
  python3 -c '
import json, sys
base = json.load(open(sys.argv[1]))
back = {e["rel_path"] for e in json.load(open(sys.argv[2]))["entries"]}
chain = set(base.get("deleted", []))
with open(sys.argv[3]) as fh:
    chain |= {line.strip() for line in fh if line.strip()}
for rel in sorted(chain - back):
    print(rel)
' "$BASE_MAN" "$WORK/meta/enum_delta.json" "$WORK/meta/deleted.txt" \
    > "$WORK/meta/deleted_chain.txt" 2>/dev/null || cp "$WORK/meta/deleted.txt" "$WORK/meta/deleted_chain.txt"
  DELETED_LIST="$WORK/meta/deleted_chain.txt"
  printf 'S3B_DELETED_CHAIN recorded=%s this_round=%s\n' \
    "$(grep -c . "$DELETED_LIST" 2>/dev/null || true)" "$(grep -c . "$WORK/meta/deleted.txt" 2>/dev/null || true)"
  if [ "${CHANGED:-0}" = "0" ] && [ "${ADDED:-0}" = "0" ] && [ "${DELETED:-0}" = "0" ]; then
    printf 'COLD_BACKUP_RESULT=SKIP reason=no_delta_since_frozen baseline=%s manifest=v%s\n' \
      "$OUT_ABS" "$BASE_VER"
    say "ok - 锚点之后没有任何变化：$CUR_NAME 仍是唯一基线（不出空 v${NEW_VER}，也不改封存件）"
    seal
    exit 0
  fi
  # 增量卷的序号从 90 起，并按轮次往上抬一位（v2→90、v3→91）：
  # 两轮都用 90 会让第二轮的卷名与第一轮**同名**，同名即覆盖 —— 基线里 v90 变成第二轮的字节，
  # 而 v(N) 的 manifest 还引用着第一轮那份 sha，卷集从此对不上账（R1 的另一种形状：
  # "看起来只多了一卷，实际少了一卷"）。序号也用来表达顺序：verify/l2 按 seq 递增读，最新的赢；
  # seq>=90 一律是"增量卷"，读清单的人靠这一条分普通包与补备包（v1→89 以下）。
  DELTA_INDEX_START=$((88 + NEW_VER))
  SPEC_DELTA_BUILD="$WORK/spec/build-delta.spec"
  {
    printf 'mode=build\nwork=%s\nvolumes_dir=%s/volumes-delta\nvolume_max_bytes=%s\nbig_file_min_bytes=%s\ncompress=1\nindex_start=%s\n' \
      "$STAGE" "$STAGE" "$VOLUME_MAX_BYTES" "$BIG_FILE_MIN_BYTES" "$DELTA_INDEX_START"
    printf 'enum_file=%s/enum_delta.json\n' "$STAGE"
  } > "$SPEC_DELTA_BUILD"
  run_step "$SPEC_DELTA_BUILD" build-delta 2 volume_build_failed
delta_build="$LAST_OUT"
  printf '%s\n' "$delta_build" | { grep -E '^V\||^BUILD_RESULT=' || true; }
  fetch_to "volumes.json" "$WORK/meta/volumes_delta.json" || abort 2 transport_unreachable "file=volumes.json(delta)"
  # 落地复算的目标是 **自己的 temp**（$WORK/volumes-delta），不是基线目录：
  # §5 S3 的顺序是"校验通过 → 才封存"。旧写法先 cp 进基线再校验，L1/L2 一失败就留下一卷
  # 没有任何清单引用的文件在**只读基线**里（R1 的形状：目录看着比清单多东西）。
  # 现在只有两关都过了才往基线里搬（下面 bless 段），失败的那一轮对基线零写入。
  fetch_volumes "$WORK/meta/volumes_delta.json" "$WORK/volumes-delta" "volumes-delta"
  DELTA_LANDED="$(cat "$WORK/meta/vol_landed.txt" 2>/dev/null || echo 0)"
  DELTA_PLANNED="$(grep -c . "$WORK/meta/volume_names.txt" || true)"
  [ "${DELTA_PLANNED:-0}" = "${DELTA_LANDED:-0}" ] ||
    abort 2 volume_count_mismatch "delta planned=${DELTA_PLANNED} landed=${DELTA_LANDED}"
  printf 'S3B_DELTA changed=%s added=%s deleted=%s config_touched=%s volumes=%s\n' \
    "$CHANGED" "$ADDED" "$DELETED" "$CONFIG_TOUCHED" "$DELTA_LANDED"

  # merge 跑在 stage 那一侧（远端或本地演练目录），所以先把增量件推进 stage，再按 stage 相对名引用
  push_to_stage "$WORK/meta/enum_delta.json" "enum_delta.json" || abort 3b push_failed "enum_delta"
  push_to_stage "$WORK/meta/volumes_base.json" "volumes_base.json" || abort 3b push_failed "volumes_base"
  SPEC_MERGE="$WORK/spec/merge.spec"
  {
    printf 'mode=merge\nwork=%s\nmanifest=%s\ndelta_enum=%s\nvolumes_file=%s\nvolumes_file=%s\n' \
      "$STAGE" "$STAGE/BACKUP_MANIFEST.json" "$STAGE/enum_delta.json" \
      "$STAGE/volumes_base.json" "$STAGE/volumes.json"
    while IFS= read -r dline; do [ -n "$dline" ] && printf 'deleted=%s\n' "$dline"; done < "$DELETED_LIST"
  } > "$SPEC_MERGE"
  run_step "$SPEC_MERGE" merge 3b merge_failed
merge_out="$LAST_OUT"
  printf '%s\n' "$merge_out" | { grep -E '^MERGE' || true; }
  fetch_to "enum_merged.json" "$WORK/meta/enum_merged.json" || abort 3b transport_unreachable "enum_merged"
  fetch_to "volumes_merged.json" "$WORK/meta/volumes_merged.json" || abort 3b transport_unreachable "volumes_merged"
  fetch_to "secrets_merged.json" "$WORK/meta/secrets_merged.json" || abort 3b transport_unreachable "secrets_merged"
  MERGED_FILES="$(python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
print(d["totals"]["files"])
' "$WORK/meta/enum_merged.json")"
  MERGED_BYTES="$(python3 -c '
import json, sys
print(sum(int(e["bytes"]) for e in json.load(open(sys.argv[1]))["entries"]))
' "$WORK/meta/enum_merged.json")"
  MERGED_ACCOUNTS="$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1]))["totals"]["accounts_files"])
' "$WORK/meta/enum_merged.json")"
  FULL_BYTES="$MERGED_BYTES"; FULL_FILES="$MERGED_FILES"; ACCOUNTS_FILES="$MERGED_ACCOUNTS"

  SPEC_MAN2="$WORK/spec/manifest-v${NEW_VER}.spec"
  {
    printf 'mode=manifest\nwork=%s\nversion=%s\nout_name=%s\n' "$WORK" "$NEW_VER" "$NEW_NAME"
    printf 'previous_manifest_sha256=%s\n' "$V1_SHA"
    printf 'enum_file=%s\nvolumes_json=%s\nsecrets_json=%s\n' \
      "$WORK/meta/enum_merged.json" "$WORK/meta/volumes_merged.json" "$WORK/meta/secrets_merged.json"
    while IFS= read -r dline; do [ -n "$dline" ] && printf 'deleted=%s\n' "$dline"; done < "$DELETED_LIST"
    secrets_spec_lines
    printf 'header.backup_id=%s\nheader.frozen_at=%s\nheader.frozen_at_epoch=%s\n' \
      "$V1_BACKUP_ID" "$RESCAN_AT" "$V1_FROZEN_EPOCH"
    printf 'header.rescan_at=%s\nheader.rescan_at_epoch=%s\nheader.previous_manifest=%s\nheader.previous_manifest_sha256=%s\n' \
      "$RESCAN_AT" "$RESCAN_EPOCH" "$CUR_NAME" "$V1_SHA"
    printf 'header.build_commit=%s\nheader.web_build_commit=%s\nheader.git_head=%s\n' "$BUILD_COMMIT" "$WEB_COMMIT" "$GIT_HEAD"
    printf 'header.source_root=%s\nheader.transport=%s\nheader.close_state=%s\n' "$ABS_ROOT" "$TRANSPORT" "$CLOSE_STATE"
    printf 'header.secrets_mode=%s\nheader.baseline_copies=1\nheader.baseline_path=%s\n' \
      "$([ "$SKIP_SECRETS" = 1 ] && echo skipped_by_flag || echo encrypted_container)" "$OUT_ABS"
    printf 'header.data_total_bytes=%s\nheader.file_count=%s\nheader.accounts_files=%s\n' \
      "$MERGED_BYTES" "$MERGED_FILES" "$MERGED_ACCOUNTS"
    printf 'header.delta_changed=%s\nheader.delta_added=%s\nheader.delta_deleted=%s\n' \
      "$CHANGED" "$ADDED" "$DELETED"
    printf 'header.config_registered=%s\n' "${CONFIG_REGISTERED:-none}"
  } > "$SPEC_MAN2"
  run_step "$SPEC_MAN2" "manifest-v${NEW_VER}" 3b manifest_failed
man2_out="$LAST_OUT"
  printf '%s\n' "$man2_out" | { grep -E '^MANIFEST_RESULT=' || true; }
  # 到这里为止，v(N+1) 的清单与增量卷都还在 $WORK 里。基线的落笔点只有一个：两关（L1/L2）全过。
  # 校验用的清单路径就是 $WORK 那一份（清单里记的是**卷名**，路径由 vpath 映射给），
  # 所以"验的就是最后交出去的"这句话仍然成立，而不必先写进基线再验。

  # L1/L2 对 v(N)：卷集 = 基线里全部在册的卷 + 本轮增量卷（增量在后，"最新一份就是基线"
  # 由顺序表达，不靠命名约定）。卷名到路径的映射**扫目录**生成而不是只扫本轮的 volume_names：
  # 第二轮补备时第一轮的 v90 还在 volumes-delta/ 里、且被 manifest 引用，只登记本轮那几张
  # 会让 resolve_volume 回落到 volumes/ 去找不到它，L1 以 archive_unreadable 收场。
  # 本轮的新卷放在最后登记（指向 $WORK）——parse_spec 按名字建表，后写的覆盖先写的。
  VPATH_LINES=""
  for VOLDIR in volumes volumes-delta; do
    for VOLFILE in "$OUT_ABS/$VOLDIR"/*.tar.gz "$OUT_ABS/$VOLDIR"/*.tar; do
      [ -e "$VOLFILE" ] || continue
      VPATH_LINES="${VPATH_LINES}vpath=$(basename "$VOLFILE")|${VOLFILE}"$'\n'
    done
  done
  while IFS=$'	' read -r dname _dsha _dbytes; do
    [ -n "$dname" ] || continue
    VPATH_LINES="${VPATH_LINES}vpath=${dname}|${WORK}/volumes-delta/${dname}"$'\n'
  done < "$WORK/meta/volume_names.txt"
  SPEC_L12="$WORK/spec/l1-v${NEW_VER}.spec"
  {
    printf 'mode=verify\nmanifest=%s\nvolumes_dir=%s\ncheck_excluded=1\n' \
      "$WORK/$NEW_NAME" "$OUT_ABS/volumes"
    printf '%s' "$VPATH_LINES"
  } > "$SPEC_L12"
  l1b_out="$(helper_run "$SPEC_L12" "l1-v${NEW_VER}")"; l1b_rc=$?
  [ "$l1b_rc" = "3" ] && abort 2 leak_guard_hit "label=l1-v${NEW_VER}"
  printf '%s\n' "$l1b_out" > "$WORK/reports/L1_v${NEW_VER}.txt"
  printf '%s\n' "$l1b_out" | { grep -E '^L1_' || true; }
  [ $l1b_rc -eq 0 ] || abort 3b l1_failed "see=logs/l1-v${NEW_VER}.log"
  SPEC_L22="$WORK/spec/l2-v${NEW_VER}.spec"
  {
    printf 'mode=l2\nmanifest=%s\nvolumes_dir=%s\n' "$WORK/$NEW_NAME" "$OUT_ABS/volumes"
    printf '%s' "$VPATH_LINES"
    [ -n "$MARKER_ARG" ] && printf 'snapshot_ok=%s\n' "$MARKER_ARG"
  } > "$SPEC_L22"
  l2b_out="$(helper_run "$SPEC_L22" "l2-v${NEW_VER}")"; l2b_rc=$?
  [ "$l2b_rc" = "3" ] && abort 2 leak_guard_hit "label=l2-v${NEW_VER}"
  printf '%s\n' "$l2b_out" > "$WORK/reports/L2_v${NEW_VER}.txt"
  printf '%s\n' "$l2b_out" | { grep -E '^L2' || true; }
  [ $l2b_rc -eq 0 ] || abort 3b l2_failed "see=logs/l2-v${NEW_VER}.log"

  # ── 到这里两关都过了，才允许动基线（一次做完，不留半态）：卷 → 清单 → 报告 → 指针 → 封存
  mkdir -p "$OUT_ABS/volumes-delta" || abort 3b baseline_write_failed "delta dir"
  for BLESS_VOL in "$WORK"/volumes-delta/*.tar.gz "$WORK"/volumes-delta/*.tar; do
    [ -e "$BLESS_VOL" ] || continue
    cp "$BLESS_VOL" "$OUT_ABS/volumes-delta/$(basename "$BLESS_VOL")" ||
      abort 3b baseline_write_failed "vol=$(basename "$BLESS_VOL")"
  done
  cp "$WORK/$NEW_NAME" "$OUT_ABS/$NEW_NAME" || abort 3b baseline_write_failed "manifest v$NEW_VER"
  cp "$WORK/meta/volumes_delta.json" "$OUT_ABS/meta/volumes_delta_v${NEW_VER}.json" 2>/dev/null || true
  cp "$WORK/meta/enum_merged.json" "$OUT_ABS/meta/enum_merged_v${NEW_VER}.json" 2>/dev/null || true
  cp "$WORK/meta/delta_meta.json" "$OUT_ABS/meta/delta_meta_v${NEW_VER}.json" 2>/dev/null || true
  cp "$WORK/reports/L1_v${NEW_VER}.txt" "$OUT_ABS/reports/" 2>/dev/null || true
  cp "$WORK/reports/L2_v${NEW_VER}.txt" "$OUT_ABS/reports/" 2>/dev/null || true
  {
    printf 'backup_id=%s\nmanifest_version=%s\nrescan_at=%s\nfrozen_at_epoch=%s\n' \
      "$V1_BACKUP_ID" "$NEW_VER" "$RESCAN_AT" "$V1_FROZEN_EPOCH"
    printf 'previous_manifest=%s\nprevious_manifest_sha256=%s\n' "$CUR_NAME" "$V1_SHA"
    printf 'changed=%s added=%s deleted=%s config_touched=%s\n' "$CHANGED" "$ADDED" "$DELETED" "$CONFIG_TOUCHED"
    printf 'files=%s bytes=%s accounts_files=%s delta_volumes=%s\n' \
      "$MERGED_FILES" "$MERGED_BYTES" "$MERGED_ACCOUNTS" "$DELTA_LANDED"
    grep -hE '^(L1_RESULT|L2_RESULT)' "$WORK/reports/L1_v${NEW_VER}.txt" "$WORK/reports/L2_v${NEW_VER}.txt" || true
    printf 'restore_baseline=%s audit_chain=%s(v%s)\n' "$NEW_NAME" "$CUR_NAME" "$BASE_VER"
    printf 'baseline_copies=1 media=single_copy_on_this_disk\n'
    printf 'disaster_recovery=no (owner dropped the second-medium step tonight)\n'
    printf 'offsite_second_belt=deploy/mac/restic_pull_backup.sh (unchanged)\n'
  } > "$OUT_ABS/reports/VERIFY_v${NEW_VER}.txt"
  # 指针改口是**最后一步**：卷、清单、报告都在位了才说"回传认这一份"。
  # 中途任何一次 cp 失败，指针仍指向上一版完整校验过的基线，回传侧拿不到半份东西。
  printf '%s\n' "$NEW_NAME" > "$OUT_ABS/BASELINE_MANIFEST"
  seal
  printf 'COLD_BACKUP_RESULT=OK baseline=%s files=%s bytes=%s manifest=v%s\n' \
    "$OUT_ABS" "$MERGED_FILES" "$MERGED_BYTES" "$NEW_VER"
  printf 'S3B_BASELINE_BLESSED restore_manifest=%s previous_manifest=%s previous_sha256=%s v1_kept_as_audit=1 sealed=1\n' \
    "$NEW_NAME" "$CUR_NAME" "${V1_SHA:0:16}"
  say "ok - v${NEW_VER} 增量补备完成（回传只认 ${NEW_NAME}；v1..v$BASE_VER 留作审计链，一份都没重写）"
  exit 0
fi

# ── S1 冻结确认：本脚本从不下令停服务，必须由 owner 显式确认写面已停 ────────────────
if [ "$FROZEN_CONFIRMED" != "1" ]; then
  abort 1 freeze_unconfirmed "rerun_with=--frozen-confirmed"
fi
ensure_stage
mkdir -p "$OUT_ABS" "$OUT_ABS/volumes" "$OUT_ABS/reports" "$OUT_ABS/meta" "$OUT_ABS/secrets" ||
  abort 1 baseline_write_failed "path=$OUT_ABS"
say "==> [S1] 冻结已确认；基线目录 $OUT_ABS"

for db in $DB_NAMES; do
  [ -e "$ABS_ROOT/$db" ] || { [ "$TRANSPORT" = "ssh" ] && abort 1 db_snapshot_failed "db_absent=$db"; }
  db_snapshot "$db"
done

# ── S2 全量枚举（带哈希）+ B 类容器 + 分卷构建 + 逐卷落地复算 ──────────────────────
say "==> [S2] 全量枚举（hash=1）"
enum_spec "data" 1 "$WORK/spec/enum-full.spec"
run_step "$WORK/spec/enum-full.spec" enum-full 2 enumeration_failed
full_out="$LAST_OUT"
printf '%s\n' "$full_out" | { grep -E '^ENUM_SUMMARY\||^ENUM_NOTE' || true; }
SUM_LINE="$(printf '%s\n' "$full_out" | { grep -E '^ENUM_SUMMARY\|tag=data' || true; } | head -1)"
FULL_BYTES="$(field "$SUM_LINE" bytes)"; FULL_FILES="$(field "$SUM_LINE" files)"
ACCOUNTS_FILES="$(field "$SUM_LINE" accounts_files)"
[ -n "$FULL_BYTES" ] || abort 2 enumeration_failed "no_bytes_reading"
fetch_to "enum_data.json" "$WORK/meta/enum_data.json" || abort 2 transport_unreachable "file=enum_data.json"
printf 'S2_ENUM files=%s bytes=%s accounts_files=%s\n' "$FULL_FILES" "$FULL_BYTES" "$ACCOUNTS_FILES"

# B 类（§4.6）：明文只在自己 0700 的 temp 里过一遍 → tar → 加密容器 → 立刻删明文。
SECRETS_SPEC=""
CONTAINER_NAME=""
if [ "$SKIP_SECRETS" = "1" ]; then
  say "INFO - 按 --skip-secrets 跳过 B 类；manifest 里 secrets=[]、header.secrets_mode=skipped_by_flag"
else
  BPLAIN="$WORK/bplain"; mkdir -p "$BPLAIN"; chmod 700 "$BPLAIN"
  B_LIST=""
  if [ "$TRANSPORT" = "local" ]; then
    for entry in "$LOCAL_SOURCE/quant.env" "$LOCAL_SOURCE/config.xt.json" \
                 "$ABS_ROOT/config_history/rules" "$ABS_ROOT/admin_session_token"; do
      [ -e "$entry" ] && B_LIST="$B_LIST$entry"$'\n'
    done
  else
    B_LIST="$SECRET_QUANT_ENV"$'\n'"$SECRET_GATEWAY_CONFIG"$'\n'
  fi
  COLLECTED=0
  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    base="$(basename "$entry")"
    if [ "$TRANSPORT" = "local" ]; then
      cp -R "$entry" "$BPLAIN/$base" || abort 2 bclass_copy_failed "path=$entry"
    else
      $SCP -r "${GZ_USER}@${SSH_HOST}:${entry}" "$BPLAIN/$base" >/dev/null 2>&1 || {
        say "INFO - B 类一项不在位（首次部署/mock 机属正常；现网机出现此行要人工核对钥匙）：$entry"
        continue; }
    fi
    COLLECTED=$((COLLECTED + 1))
    SECRETS_SPEC="${SECRETS_SPEC}secret=${entry}|b_pending|pending|pending|0"$'\n'
  done <<EOF
$B_LIST
EOF
  if [ "$COLLECTED" = "0" ]; then
    say "INFO - 没有收到任何 B 类文件：manifest 里 secrets=[]，回传侧钥匙要另证"
  else
    PASS_VALUE=""
    if [ -n "${COLD_B_PASS_CMD:-}" ]; then
      PASS_VALUE="$(sh -c "$COLD_B_PASS_CMD" 2>/dev/null || true)"
    elif command -v security >/dev/null 2>&1; then
      PASS_VALUE="$(security find-generic-password -a "$USER" -s "$KEYCHAIN_ITEM" -w 2>/dev/null || true)"
    fi
    [ -n "$PASS_VALUE" ] || abort 2 secrets_pass_unavailable "keychain_item=$KEYCHAIN_ITEM or COLD_B_PASS_CMD"
    BPLAIN_TAR="$WORK/bplain.tar"
    protected_target "$BPLAIN_TAR" && abort 2 baseline_target_protected "bplain_tar=$BPLAIN_TAR"
    ( cd "$BPLAIN" && tar -cf "$BPLAIN_TAR" . ) || abort 2 bclass_tar_failed
    CONTAINER_NAME="${BACKUP_ID}-bclass.tar.enc"
    CONTAINER="$OUT_ABS/secrets/$CONTAINER_NAME"
    protected_target "$CONTAINER" && abort 2 baseline_target_protected "container=$CONTAINER"
    export COLD_B_PASS="$PASS_VALUE"
    unset PASS_VALUE
    openssl enc -aes-256-cbc -salt -pbkdf2 -in "$BPLAIN_TAR" -out "$CONTAINER" \
      -pass env:COLD_B_PASS >/dev/null 2>&1 || { unset COLD_B_PASS; abort 2 secrets_encrypt_failed; }
    unset COLD_B_PASS
    chmod 600 "$CONTAINER" 2>/dev/null || true
    CONTAINER_SHA="$(sha_file "$CONTAINER")"
    CONTAINER_BYTES="$(wc -c < "$CONTAINER" | tr -d ' ')"
    CONTAINER_MODE="$(file_mode "$CONTAINER")"
    # manifest 的 secrets[]：路径 + 容器哈希 + 容器权限，**没有值**（§4.6 R3）。
    # 权限位要进清单：回传侧照清单验"钥匙件除属主不可读"，靠现场 stat 会在封存后的 0400/0600
    # 两种读数之间反复打架（seal 的例外就是为这条口径做的）。
    SECRETS_SPEC="$(printf '%s' "$SECRETS_SPEC" | sed \
      -e "s#|b_pending|pending|pending|0#|b_only|secrets/$CONTAINER_NAME|$CONTAINER_SHA|$CONTAINER_BYTES|$CONTAINER_MODE#")"
    # 明文 temp 就地删除：自己的临时区，且已过 protected_target（这条闸不许只活在推理里）
    protected_target "$BPLAIN" && abort 2 baseline_target_protected "bplain=$BPLAIN"
    rm -rf "$BPLAIN"
    protected_target "$BPLAIN_TAR" && abort 2 baseline_target_protected "bplain_tar=$BPLAIN_TAR"
    rm -f "$BPLAIN_TAR"
    printf 'S2_SECRETS container=secrets/%s sha256=%s bytes=%s mode=%s entries=%s values_logged=0\n' \
      "$CONTAINER_NAME" "${CONTAINER_SHA:0:16}" "$CONTAINER_BYTES" "$CONTAINER_MODE" "$COLLECTED"
  fi
fi

say "==> [S2] 分卷构建（stage 内）"
spec_build="$WORK/spec/build.spec"
{
  printf 'mode=build\nwork=%s\nvolumes_dir=%s/volumes\nvolume_max_bytes=%s\nbig_file_min_bytes=%s\ncompress=1\n' \
    "$STAGE" "$STAGE" "$VOLUME_MAX_BYTES" "$BIG_FILE_MIN_BYTES"
  printf 'enum_file=%s/enum_data.json\n' "$STAGE"
} > "$spec_build"
run_step "$spec_build" build 2 volume_build_failed
build_out="$LAST_OUT"
printf '%s\n' "$build_out" | { grep -E '^V\||^BUILD_RESULT=' || true; }
fetch_to "volumes.json" "$WORK/meta/volumes.json" || abort 2 transport_unreachable "file=volumes.json"
cp "$WORK/meta/volumes.json" "$OUT_ABS/meta/volumes_v1.json" 2>/dev/null || true
fetch_volumes "$WORK/meta/volumes.json" "$OUT_ABS/volumes" "volumes"
LAND="$(cat "$WORK/meta/vol_landed.txt" 2>/dev/null || echo 0)"
PLAN_VOLS="$(grep -c . "$WORK/meta/volume_names.txt" || true)"
[ "${PLAN_VOLS:-0}" = "${LAND:-0}" ] || abort 2 volume_count_mismatch "planned=${PLAN_VOLS} landed=${LAND}"
printf 'S2_VOLUMES planned=%s landed=%s\n' "$PLAN_VOLS" "$LAND"

# ── manifest + S3 校验（SNAPSHOT_OK 读数已在 S0 取过：MARKER_ARG）──────────────────
build_manifest_spec() { # $1=version $2=out_name $3=enum 文件 $4=volumes.json $5=deleted 行数文件 $6=额外 header 行
  local ver="$1" name="$2" enum="$3" vols="$4" dele="$5" extra_hdr="$6" out="$WORK/spec/manifest-v$1.spec"
  {
    printf 'mode=manifest\nwork=%s\nversion=%s\nout_name=%s\n' "$WORK" "$ver" "$name"
    printf 'enum_file=%s\nvolumes_json=%s\n' "$enum" "$vols"
    [ -f "$dele" ] && while IFS= read -r dline; do [ -n "$dline" ] && printf 'deleted=%s\n' "$dline"; done < "$dele"
    secrets_spec_lines
    printf 'header.backup_id=%s\nheader.frozen_at=%s\nheader.frozen_at_epoch=%s\n' "$BACKUP_ID" "$FROZEN_AT" "$FROZEN_EPOCH"
    printf 'header.build_commit=%s\nheader.web_build_commit=%s\nheader.git_head=%s\n' "$BUILD_COMMIT" "$WEB_COMMIT" "$GIT_HEAD"
    printf 'header.source_root=%s\nheader.transport=%s\nheader.close_state=%s\n' "$ABS_ROOT" "$TRANSPORT" "$CLOSE_STATE"
    printf 'header.secrets_mode=%s\nheader.baseline_copies=1\nheader.baseline_path=%s\n' \
      "$([ "$SKIP_SECRETS" = 1 ] && echo skipped_by_flag || echo encrypted_container)" "$OUT_ABS"
    printf 'header.data_total_bytes=%s\nheader.file_count=%s\nheader.accounts_files=%s\n' "$FULL_BYTES" "$FULL_FILES" "$ACCOUNTS_FILES"
    printf '%s\n' "$extra_hdr"
  } > "$out"
  printf '%s' "$out"
}
: > "$WORK/meta/deleted.txt"
printf '{"files":[],"deleted":[],"totals":{"files":0,"bytes":0}}' > "$WORK/meta/empty_enum.json"
printf '{}' > "$WORK/meta/empty_volumes.json"
MAN_SPEC="$(build_manifest_spec 1 BACKUP_MANIFEST.json "$WORK/meta/enum_data.json" "$WORK/meta/volumes.json" "$WORK/meta/deleted.txt" "")"
run_step "$MAN_SPEC" manifest-v1 3 manifest_failed
man_out="$LAST_OUT"
printf '%s\n' "$man_out" | { grep -E '^MANIFEST_RESULT=' || true; }
cp "$WORK/BACKUP_MANIFEST.json" "$OUT_ABS/BACKUP_MANIFEST.json" || abort 3 baseline_write_failed "manifest"
cp "$WORK/meta/enum_data.json" "$OUT_ABS/meta/enum_data.json" 2>/dev/null || true
cp "$WORK/meta/volumes.json" "$OUT_ABS/meta/volumes_v1.json" 2>/dev/null || true

L1_SPEC="$WORK/spec/l1.spec"
printf 'mode=verify\nmanifest=%s\nvolumes_dir=%s\ncheck_excluded=1\n' \
  "$OUT_ABS/BACKUP_MANIFEST.json" "$OUT_ABS/volumes" > "$L1_SPEC"
l1_out="$(helper_run "$L1_SPEC" l1-v1)"; l1_rc=$?
[ "$l1_rc" = "3" ] && abort 2 leak_guard_hit "label=l1-v1"
printf '%s\n' "$l1_out" > "$OUT_ABS/reports/L1_v1.txt"
printf '%s\n' "$l1_out" | { grep -E '^L1_' || true; }
[ $l1_rc -eq 0 ] || abort 3 l1_failed "see=reports/L1_v1.txt"

L2_SPEC="$WORK/spec/l2.spec"
{
  printf 'mode=l2\nmanifest=%s\nvolumes_dir=%s\n' "$OUT_ABS/BACKUP_MANIFEST.json" "$OUT_ABS/volumes"
  [ -n "$MARKER_ARG" ] && printf 'snapshot_ok=%s\n' "$MARKER_ARG"
} > "$L2_SPEC"
l2_out="$(helper_run "$L2_SPEC" l2-v1)"; l2_rc=$?
[ "$l2_rc" = "3" ] && abort 2 leak_guard_hit "label=l2-v1"
printf '%s\n' "$l2_out" > "$OUT_ABS/reports/L2_v1.txt"
printf '%s\n' "$l2_out" | { grep -E '^L2' || true; }
[ $l2_rc -eq 0 ] || abort 3 l2_failed "see=reports/L2_v1.txt"

{
  printf 'backup_id=%s\nmanifest_version=1\nfrozen_at=%s\nfrozen_at_epoch=%s\n' "$BACKUP_ID" "$FROZEN_AT" "$FROZEN_EPOCH"
  printf 'build_commit=%s\nweb_build_commit=%s\ngit_head=%s\n' "$BUILD_COMMIT" "$WEB_COMMIT" "$GIT_HEAD"
  printf 'files=%s bytes=%s volumes=%s accounts_files=%s\n' "$FULL_FILES" "$FULL_BYTES" "$PLAN_VOLS" "$ACCOUNTS_FILES"
  grep -hE '^(L1_RESULT|L2_RESULT)' "$OUT_ABS/reports/L1_v1.txt" "$OUT_ABS/reports/L2_v1.txt" || true
  printf 'baseline_copies=1 media=single_copy_on_this_disk\n'
  printf 'disaster_recovery=no (owner dropped the second-medium step tonight)\n'
  printf 'offsite_second_belt=deploy/mac/restic_pull_backup.sh (unchanged, still the daily restic copy)\n'
} > "$OUT_ABS/reports/VERIFY_v1.txt"

# ── 封存（此后只做增量补备，不再改基线；§5 S3 第 4 步；seal/unseal 定义在工具区）──────
seal
printf 'COLD_BACKUP_RESULT=OK baseline=%s files=%s bytes=%s manifest=v1\n' "$OUT_ABS" "$FULL_FILES" "$FULL_BYTES"
printf 'S3_SEALED readonly=1 path=%s single_copy=yes disaster_recovery=no\n' "$OUT_ABS"
say "ok - v1 基线封存：$OUT_ABS"
[ "$MODE" = "incremental" ] && say "INFO - 增量补备请对同一基线跑 --incremental --base=$OUT_ABS"
exit 0
