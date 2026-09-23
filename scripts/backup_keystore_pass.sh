#!/usr/bin/env bash
# backup_keystore_pass.sh — §KEYSTORE-PASSBAK（2026-09-23）：Android 签名口令离线备份器（C3）。
#
# 背景：mobile/keystore.pass（chmod 600，已 gitignore）是 release 签名密钥库
#   mobile/keystore-v2.jks 的口令（build_apk.sh release 经 MOBILE_KEYSTORE_PASS 消费）。
#   丢了它 = 此后所有 APK 无法用同一把 key 签名 = 存量安装用户（含强更通道 /dl）永远收不到
#   更新——密钥库本身还有副本，口令却**没有任何备份**。本脚本补这一条。
# 语义（密钥物料备份的三条硬规矩）：
#   ① 目的地必须显式 BACKUP_TARGET_DIR 传入，**拒绝猜**（不设默认路径，避免"以为备了其实没备"）；
#   ② 落盘 mode 600，文件名 quant-keystore-pass-<yyyy-MM-dd>.txt；
#   ③ 复制后 SHA256 双端比对，不一致退非 0；输出**只**打文件名 + 指纹前缀，绝不打口令内容。
#   ④ 目的地若落在仓库内（git toplevel 前缀判定）一律拒绝——口令永远不该出现在任何
#      commit 候选里（.gitignore 只拦已知文件名，日期化文件名会绕过它）。
# 用法：
#   BACKUP_TARGET_DIR=/Volumes/USB-QUANT-BAK ./scripts/backup_keystore_pass.sh
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${APP_DIR}/mobile/keystore.pass"

if [ -z "${BACKUP_TARGET_DIR:-}" ]; then
  echo "X 必须显式设置 BACKUP_TARGET_DIR（如离线 U 盘/加密盘挂载点）——本脚本拒绝猜测目的地。" >&2
  exit 1
fi
if [ ! -f "$SRC" ]; then
  echo "X 口令文件不存在：$SRC" >&2
  exit 1
fi

# 目的地**必须已存在**（典型=U 盘/加密盘挂载点）：不 mkdir——既防把备份写进打错路径的
# 凭空目录（"以为备了其实没备"），也让下面的仓库内前缀拒写不会先在仓库里留下空目录。
if [ ! -d "$BACKUP_TARGET_DIR" ]; then
  echo "X 目的地不存在：${BACKUP_TARGET_DIR}（未挂载？拒绝 mkdir，防写进打错路径的凭空目录）。" >&2
  exit 1
fi
REPO_ROOT="$(git -C "$APP_DIR" rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$REPO_ROOT" ]; then
  echo "X git rev-parse --show-toplevel 失败（不在仓库内？拒绝继续）" >&2
  exit 1
fi
DST_ABS="$(cd "$BACKUP_TARGET_DIR" && pwd -P)"
REPO_ABS="$(cd "$REPO_ROOT" && pwd -P)"
# 两侧都先经 pwd -P 归一化（解 symlink/`..`）再做前缀判定——只比字面量会被 ../ 或软链绕过。
case "${DST_ABS}/" in
  "${REPO_ABS}/"*)
    echo "X 目的地在仓库内部（${DST_ABS}）——口令备份绝不落进 commit 候选，换一个（离线介质）目录。" >&2
    exit 1 ;;
esac
if [ "$DST_ABS" = "$REPO_ABS" ]; then
  echo "X 目的地即仓库根本身，拒绝。" >&2
  exit 1
fi

STAMP="$(date '+%Y-%m-%d')"
DST="${DST_ABS}/quant-keystore-pass-${STAMP}.txt"
# umask 077 保证**创建即 600**，杜绝 cp 落盘与 chmod 之间的可读窗口；再显式 chmod 兜底。
( umask 077; cp "$SRC" "$DST" )
chmod 600 "$DST"

# SHA256（macOS 走 shasum -a 256，Linux 走 sha256sum；只用哈希值本身，不碰内容）。
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "__no_shaid__"
  fi
}
H_SRC="$(hash_file "$SRC")"
H_DST="$(hash_file "$DST")"
if [ "$H_SRC" = "__no_shaid__" ] || [ "$H_DST" = "__no_shaid__" ]; then
  echo "X 系统缺少 sha256sum/shasum，无法验证副本一致性——宁可报失败，不交未验证的备份。" >&2
  exit 1
fi
if [ "$H_SRC" != "$H_DST" ]; then
  echo "X 校验失败：副本与源不一致，已删除坏副本。" >&2
  rm -f "$DST"
  exit 1
fi

# 只打文件名 + 指纹前缀（12 hex），不打目录全量清单、不打任何口令内容。
echo "OK keystore passphrase backed up"
echo "   src = mobile/keystore.pass        sha256=${H_SRC:0:12}... (full hash kept local, never logged)"
echo "   dst = $(basename "$DST")          mode=600 sha256=${H_DST:0:12}... match=yes"
echo "   dir = $DST_ABS"
echo "提示：该介质请离机保存；口令等同 release 签名能力，泄露=可伪造你的 APK 更新。"
