#!/usr/bin/env bash
# win_remote_exec.sh — 云端经 WireGuard 隧道维护 Win 物理机（owner 2026-11-11 需求 3）。
#
# 前置：盒子已跑过 deploy/win/enable_remote_admin.ps1（一次性），gz 持有
# /opt/quant/keys/win_admin。命令用 PowerShell -EncodedCommand 传，避开嵌套引号/中文码页坑。
#
# 用法：
#   scripts/win_remote_exec.sh exec "Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway | Select-Object -First 3 Name"
#   scripts/win_remote_exec.sh push qmt_gateway/report_uplink.py C:/qmt/quant-trading-v2/qmt_gateway/report_uplink.py
#   scripts/win_remote_exec.sh test        # 探活：打印 whoami + 时间，验证通道可用
# 判读锚点：WIN_REMOTE_OK / WIN_REMOTE_ABORT reason=
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BOX_USER="${BOX_USER:-57516}"
BOX_HOST="${BOX_HOST:-10.77.0.2}"
KEY="${KEY:-/home/ubuntu/.ssh/win_admin}"  # 必须是 gz 上 ubuntu 可读的私钥（root 600 会 Load key denied）
MODE="${1:-}"
SSH_OPTS="-o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -i $KEY"

case "$MODE" in
  test) CMD='Write-Output ("WIN_REMOTE_OK whoami=" + (whoami) + " at=" + (Get-Date -Format s))' ;;
  exec) CMD="${2:-}" ; [[ -n "$CMD" ]] || { echo "WIN_REMOTE_ABORT reason=empty_command"; exit 2; } ;;
  push)
    SRC="${2:-}"; DST="${3:-}"
    [[ -n "$SRC" && -n "$DST" ]] || { echo "WIN_REMOTE_ABORT reason=push_needs_src_dst"; exit 2; }
    [[ -f "$ROOT/$SRC" ]] || { echo "WIN_REMOTE_ABORT reason=src_missing path=$SRC"; exit 2; }
    scp -q "$ROOT/$SRC" gz:/tmp/win_push_file || { echo "WIN_REMOTE_ABORT reason=scp_to_gz_failed"; exit 1; }
    ssh gz "scp -q $SSH_OPTS /tmp/win_push_file $BOX_USER@$BOX_HOST:$DST" \
      && echo "WIN_REMOTE_OK pushed=$SRC -> $DST" \
      || { echo "WIN_REMOTE_ABORT reason=scp_to_box_failed"; exit 1; }
    exit 0 ;;
  *) echo "WIN_REMOTE_ABORT reason=bad_mode want=test|exec|push"; exit 2 ;;
esac

B64="$(python3 -c "import sys,base64;print(base64.b64encode(sys.argv[1].encode('utf-16-le')).decode())" "$CMD")" \
  || { echo "WIN_REMOTE_ABORT reason=encode_failed"; exit 1; }
ssh gz "ssh $SSH_OPTS $BOX_USER@$BOX_HOST powershell -NoProfile -EncodedCommand $B64" \
  || { echo "WIN_REMOTE_ABORT reason=ssh_box_failed (has bootstrap been run?)"; exit 1; }
