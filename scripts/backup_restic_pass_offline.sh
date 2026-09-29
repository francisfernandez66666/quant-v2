#!/usr/bin/env bash
# backup_restic_pass_offline.sh — §0929SECKEY-B（2026-09-29）：restic 灾备口令的离线备份器 + 在位复核器。
#
# 为什么单独立一条（09-29 读码 + 现网实测锤实，不是文档转述）：
#   广州夜任务把 restic 口令以明文放在 C:\opt\quant\tools\restic-pass.txt，而它**与 restic 中转仓
#   C:\var\lib\quant-restic-repo 在同一块盘上**（backup_snapshot.ps1 的 $Pass / $RepoDir 两行字面量）。
#   这带来一个此前没人写下来的耦合：盘坏 = 备份副本和"解密它的唯一一把钥匙"一起没了——
#   异地那份（Mac 钥匙串 + 本机 restic 仓库）能救，但**Mac 侧一旦同时重装/钥匙串丢失，
#   全链路就没有任何一份离线副本能重新打开这些快照**。
#   RUNBOOK §0929OPS-⑪-3 原来把"口令文件另有离线备份通道"写成补偿措施，并指向
#   scripts/backup_keystore_pass.sh —— 那条备的是 **Android 签名口令**（mobile/keystore.pass），
#   与 restic 无关，属文档幻觉（本次一并更正）。本脚本就是让那句话变成真的一条命令。
#
# 口令的权威源为什么选 Mac 钥匙串而不是广州那个文件：
#   deploy/mac/restic_pull_backup.sh 用的就是钥匙串条目 quant-restic-repo-pass，
#   并以 RESTIC_FROM_PASSWORD 把同一个值喂给 `restic copy` 读广州中转仓（同口令两仓，
#   §0926ROT 时核过）。所以本机读钥匙串＝读权威源，且**不需要在现网做任何写动作**。
#
# 四条硬规矩（与 §KEYSTORE-PASSBAK 同族，一条都不放松）：
#   ① 目的地必须显式 BACKUP_TARGET_DIR 传入，拒绝猜路径、拒绝 mkdir——"以为备了其实没备"
#      比没备更坏；目的地未挂载时 -Apply 直接退非 0。
#   ② 目的地落在仓库内一律拒写（git toplevel 前缀判定）：.gitignore 只拦已知文件名，
#      日期化文件名会绕过它，口令永远不该出现在任何 commit 候选里。
#   ③ 落盘 mode 600，文件名 quant-restic-pass-<yyyy-MM-dd>.txt。
#   ④ **任何路径都只打指纹（sha256 前 8 位），绝不 echo/cat 口令值**——本脚本连失败分支
#      都不例外；变量只在赋值行与 sha256 管道里出现，不出现在任何 echo/printf 参数里。
#
# 用法：
#   BACKUP_TARGET_DIR=/Volumes/USB-QUANT-BAK ./scripts/backup_restic_pass_offline.sh            # 只复核（零写入）
#   BACKUP_TARGET_DIR=/Volumes/USB-QUANT-BAK ./scripts/backup_restic_pass_offline.sh -Apply     # 写一份离线副本
# set-e 说明：用 `|| true` 兜住所有"允许失败"的读数，绝不让 grep/find 的空结果把脚本静默劈死。
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
KEYCHAIN_ITEM="quant-restic-repo-pass"   # 与 deploy/mac/restic_pull_backup.sh 的 KEYCHAIN_ITEM 同源
MODE="check"
for a in "$@"; do
  case "$a" in
    -Apply) MODE="apply" ;;
    -Check) MODE="check" ;;
    *) echo "未知参数：${a}（只认 -Apply / -Check，缺省 -Check）" >&2; exit 1 ;;
  esac
done

if [ -z "${BACKUP_TARGET_DIR:-}" ]; then
  echo "X 必须显式设置 BACKUP_TARGET_DIR（离线 U 盘/加密盘挂载点）——本脚本拒绝猜测目的地。" >&2
  exit 1
fi
if [ ! -d "$BACKUP_TARGET_DIR" ]; then
  echo "X 目的地不存在：${BACKUP_TARGET_DIR}（未挂载？拒绝 mkdir，防写进打错路径的凭空目录）。" >&2
  exit 1
fi
REPO_ROOT="$(git -C "$APP_DIR" rev-parse --show-toplevel 2>/dev/null || true)"
if [ -n "$REPO_ROOT" ]; then
  case "$BACKUP_TARGET_DIR" in
    "$REPO_ROOT"/*|"$REPO_ROOT")
      echo "X 目的地在仓库内（${REPO_ROOT}）——拒绝把口令写进任何 commit 候选路径。" >&2
      exit 1 ;;
  esac
fi

command -v security >/dev/null 2>&1 || { echo "X 非 macOS 或 security 不可用：权威源是 Mac 钥匙串。" >&2; exit 1; }

# 指纹 = sha256 前 8 位。函数只接受"值从标准输入进来"，调用点不给它任何会被打印的形态。
fp_of_value() { printf '%s' "$1" | (command -v shasum >/dev/null 2>&1 && shasum -a 256 || sha256sum) | cut -c1-8; }

# 权威源读数：钥匙串里那条口令。**只算长度和指纹，值本身立刻喂给指纹函数，不进任何输出。**
PASS_VALUE="$(security find-generic-password -a "$USER" -s "$KEYCHAIN_ITEM" -w 2>/dev/null || true)"
if [ -z "$PASS_VALUE" ]; then
  echo "X 钥匙串读不到 ${KEYCHAIN_ITEM}（本机没装过拉取腿？按 RUNBOOK_LIVEBACKUP §Mac 侧重建后再备）。" >&2
  echo "  本脚本不会退化成「用离线副本当权威源」——那等于拿一份自己没法验证的备份去验自己。" >&2
  exit 1
fi
SRC_FP="$(fp_of_value "$PASS_VALUE")"
SRC_LEN="${#PASS_VALUE}"

latest_offline=""
for f in $(ls -1t "${BACKUP_TARGET_DIR}"/quant-restic-pass-*.txt 2>/dev/null || true); do
  latest_offline="$f"
  break
done

if [ "$MODE" = "check" ]; then
  # 复核态：零写入，只回答两件事——离线副本在不在？它和钥匙串是不是同一把？
  if [ -z "$latest_offline" ]; then
    echo "X 目的地里没有 quant-restic-pass-*.txt（一份离线副本都没有）：跑本脚本加 -Apply。" >&2
    echo "  src_fp=${SRC_FP} src_len=${SRC_LEN} target=${BACKUP_TARGET_DIR}" >&2
    exit 1
  fi
  OFF_RAW="$(cat "$latest_offline" 2>/dev/null || true)"
  OFF_FP="$(fp_of_value "$OFF_RAW")"
  if [ -z "$OFF_FP" ]; then
    echo "X 离线副本指纹计算失败（${latest_offline}）——拒绝把「算不出来」当「一致」。" >&2
    exit 1
  fi
  if [ "$OFF_FP" != "$SRC_FP" ]; then
    echo "X 离线副本与钥匙串权威源指纹不一致（offline=${OFF_FP} src=${SRC_FP}）" >&2
    echo "  口令换过而没重备＝灾备恢复时打不开仓；两种可能都判红，不自决。" >&2
    exit 1
  fi
  echo "ok - 离线副本在位且指纹与钥匙串同源：file=$(basename "$latest_offline") fp=${SRC_FP} len=${SRC_LEN}"
  exit 0
fi

# 落盘态：先写临时文件、chmod 600、再算双端指纹，不一致就删掉自己刚写的东西并非 0 退出。
TMP_OUT="$(mktemp "${TMPDIR:-/tmp}/quant-restic-pass-XXXXXX")" || { echo "X mktemp 失败" >&2; exit 1; }
( umask 077; printf '%s' "$PASS_VALUE" > "$TMP_OUT" )
chmod 600 "$TMP_OUT" 2>/dev/null || true
DST="${BACKUP_TARGET_DIR}/quant-restic-pass-$(date +%Y-%m-%d).txt"
if [ -f "$DST" ]; then
  DST_FP_EXISTING="$(fp_of_value "$(cat "$DST")")"
  if [ "$DST_FP_EXISTING" = "$SRC_FP" ]; then
    rm -f "$TMP_OUT"
    echo "ok - 今日副本已存在且指纹一致，不重复写：file=$(basename "$DST") fp=${SRC_FP} len=${SRC_LEN}"
    exit 0
  fi
  echo "X 今日副本已存在但指纹不同（offline=${DST_FP_EXISTING} src=${SRC_FP}）——拒绝静默覆盖口令文件。" >&2
  echo "  先人工确认哪一把能打开现网 restic 仓（deploy/mac/verify_restore.sh 只读复核），再决定归档旧份。" >&2
  rm -f "$TMP_OUT"
  exit 1
fi
cp "$TMP_OUT" "$DST" && rm -f "$TMP_OUT"
chmod 600 "$DST" 2>/dev/null || true
DST_FP="$(fp_of_value "$(cat "$DST")")"
if [ "$DST_FP" != "$SRC_FP" ]; then
  echo "X 落盘后指纹与源不一致（dst=${DST_FP} src=${SRC_FP}）——删除刚写的副本，别留半份口令。" >&2
  rm -f "$DST"
  exit 1
fi
# 权限复核按 **八进制模式位** 取数，不解析 `ls -l` 的权限串：macOS 的 ls 会在末尾加 `@`/`+`
# （扩展属性/ACL 标记，scp 落盘和 Time Machine 都会留），拿 "-rw-------" 做等值比较会被那一个
# 尾标撑红——本脚本首跑就踩中（文件其实写对了，红的是断言自己）。`stat -f %Lp` 是 BSD/macOS 形态，
# 取不到再退 `stat -c %a`（Linux），两条都不行就判红而不是跳过（缺判据不等于判据成立）。
PERM="$(stat -f '%Lp' "$DST" 2>/dev/null || stat -c '%a' "$DST" 2>/dev/null || true)"
if [ -z "$PERM" ]; then
  echo "X 权限无法读取（stat -f/-c 都不支持）——拒绝把「读不到」当「是 600」。" >&2
  exit 1
fi
if [ "$PERM" != "600" ]; then
  echo "X 落盘权限不是 600（got=${PERM}）——拒绝报成功。" >&2
  exit 1
fi
echo "ok - 离线副本已写入：file=${DST} fp=${SRC_FP} len=${SRC_LEN}（值从未出现在本输出里）"
echo "下一步（人工，机器代不了）：把这份放到与两台机器都分离的介质上，并按 RUNBOOK §密钥生命周期记录归档日期。"
