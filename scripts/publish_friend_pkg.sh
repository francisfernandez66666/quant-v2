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
# /opt/quant 由 root 属主：目录本身的删除/重建必须走 sudo，推完收权限给 caddy 可读。
# §CUTOVER 2026-11-09：carrier/ 是**每日自动更新的产物通道**，由 publish_carrier.sh 独立维护，
# 常规包发布绝不能把它 rm 掉（实测 10:24 一次发布把 /leg/carrier/* 打成 404）。
# 做法：先移出受保护目录 → 清建主目录 → 移回 → 发布后校验产物仍在，缺一即判红。
ssh "$GZ_HOST" "sudo rm -rf /tmp/friend-pkg-carrier-keep; \
  if [ -d $GZ_DIR/carrier ]; then sudo mv $GZ_DIR/carrier /tmp/friend-pkg-carrier-keep; fi; \
  sudo rm -rf $GZ_DIR && sudo mkdir -p $GZ_DIR && sudo chown \$(id -un): $GZ_DIR" || { echo "X prepare failed" >&2; exit 1; }
scp -q -r "$PAYLOAD/." "$GZ_HOST:$GZ_DIR/" || { echo "X scp failed" >&2; exit 1; }
ssh "$GZ_HOST" "if [ -d /tmp/friend-pkg-carrier-keep ]; then sudo mv /tmp/friend-pkg-carrier-keep $GZ_DIR/carrier; fi; \
  sudo chmod -R a+rX $GZ_DIR" || { echo "X restore/chmod failed" >&2; exit 1; }
# 每日更新通道的完整性是硬判据：期望清单里那枚 URL 必须仍可取到 200
CARRIER_URL="https://quant-trading.top/leg/carrier/${CARRIER_ARTIFACT:-qmt_internal_strategy_v90-20261109.py}"
CARRIER_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' "$CARRIER_URL" || true)
[[ "$CARRIER_HTTP" = "200" ]] || { echo "X carrier channel broken: $CARRIER_URL http=$CARRIER_HTTP" >&2; exit 1; }

FILES=$(python3 -c "import json,sys;print(len(json.load(open('$PAYLOAD/MANIFEST.json'))['files']))")
echo "PUBLISH_OK files=$FILES url=https://quant-trading.top/leg/MANIFEST.json"
echo "== verify =="
curl -sS -o /dev/null -w "manifest_http=%{http_code}\n" https://quant-trading.top/leg/MANIFEST.json
