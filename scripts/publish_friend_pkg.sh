#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# publish_friend_pkg.sh — 朋友执行腿自动更新通道的**发布腿**（2026-11-09）。
#
# 职责：跑 build_friend_package.sh 出包 → 把 payload 目录（含 MANIFEST.json）
#       推到广州机 /opt/quant/friend-pkg/（Caddy /leg/* 静态直出）。
#       朋友机器 update_leg.ps1 从 https://quant-trading.top/leg/MANIFEST.json
#       拉清单、逐文件验 SHA256、备份换文件（缺省 dry-run，-Apply 才动手）。
#
# 用法：
#   scripts/publish_friend_pkg.sh                # 构建 + 发布
#   scripts/publish_friend_pkg.sh --skip-build   # 跳过构建，直接发现有 payload
# 判读锚点（纯 ASCII）：PUBLISH_OK files=NN url=https://quant-trading.top/leg/MANIFEST.json
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GZ_HOST="${GZ_HOST:-gz}"
GZ_DIR="${GZ_DIR:-/opt/quant/friend-pkg}"
PKG_OUT="${PKG_OUT:-${TMPDIR:-/tmp}/qmt-friend-pkg}"

cd "$ROOT"
PKG_ZIP=""
if [[ "${1:-}" == "--skip-build" ]]; then
    PKG_ZIP=$(ls -t "$PKG_OUT"/leg-*.zip 2>/dev/null | head -1)
else
    echo "== build =="
    scripts/build_friend_package.sh || { echo "X build failed" >&2; exit 1; }
    PKG_ZIP=$(ls -t "$PKG_OUT"/leg-*.zip 2>/dev/null | head -1)
fi
[[ -n "$PKG_ZIP" && -f "$PKG_ZIP" ]] || { echo "X package zip missing in $PKG_OUT" >&2; exit 1; }

# 构建器只留 zip、清掉 staging：解出 payload 目录
STAGE=$(mktemp -d)
unzip -q "$PKG_ZIP" -d "$STAGE" || { echo "X unzip failed" >&2; exit 1; }
PAYLOAD="$STAGE/payload"
[[ -d "$PAYLOAD" ]] || { echo "X payload dir missing: $PAYLOAD" >&2; exit 1; }
[[ -f "$PAYLOAD/MANIFEST.json" ]] || { echo "X MANIFEST.json missing" >&2; exit 1; }

echo "== publish to $GZ_HOST:$GZ_DIR =="
# /opt/quant 由 root 属主：目录本身的删除/重建必须走 sudo，推完收权限给 caddy 可读
ssh "$GZ_HOST" "sudo rm -rf $GZ_DIR && sudo mkdir -p $GZ_DIR && sudo chown \$(id -un): $GZ_DIR" || { echo "X prepare failed" >&2; exit 1; }
scp -q -r "$PAYLOAD/." "$GZ_HOST:$GZ_DIR/" || { echo "X scp failed" >&2; exit 1; }
# 收权限：caddy 用户要能读
ssh "$GZ_HOST" "sudo chmod -R a+rX $GZ_DIR" || { echo "X chmod failed" >&2; exit 1; }

FILES=$(python3 -c "import json,sys;print(len(json.load(open('$PAYLOAD/MANIFEST.json'))['files']))")
echo "PUBLISH_OK files=$FILES url=https://quant-trading.top/leg/MANIFEST.json"
echo "== verify =="
curl -sS -o /dev/null -w "manifest_http=%{http_code}\n" https://quant-trading.top/leg/MANIFEST.json
