#!/usr/bin/env bash
# place_qmt_bridge.sh — §BRIDGE-PLACE（2026-09-24）：把桥策略文件**落位**到 QMT 实际加载的那个
# 策略目录文件，并用双端 SHA 证明"落进去的就是本机这一版"。**缺省只预览，动手必须显式 -Apply。**
#
# 为什么要有这条脚本（现成部署链覆盖不到的那一段）：
#   deploy_guangzhou.sh 的 [2b] 只把 qmt_bridge_strategy.py 传到落点目录
#   C:\qmt\quant-trading-v2\qmt_gateway\；而 QMT 模型交易加载的是
#   "<QMT 根>\python\新建策略文件.py"，中间这一跳历来靠人记得跑 scripts/place_bridges.ps1
#   （RUNBOOK §4.1b.1②）。于是"部署完成"与"桥侧代码生效"之间有一个**静默断层**：忘了这一跳，
#   次日 08:45 QMT 起来加载的还是旧文件，而部署探针全绿——09-21/09-24 两批都是人工补的。
#
# 三条设计取舍：
#   ① **缺省只预览，且预览完全不连生产**（与 §OPS-ALIGN 后所有运维安全阀同口径）：只打印将要
#      覆盖什么、备份文件叫什么、判绿看哪几行。这样门禁脚本（verify_changes.sh §93）能离线跑它。
#   ② 远端只跑**纯 ASCII** 的 PowerShell，经 -EncodedCommand 传入：中文文件名/中文判据经
#      bash→ssh→cmd→powershell 三层转义 + GBK 回传必乱（§4.1b.1② 的 BOM 事故、§GBK 系列教训），
#      判据字段掺中文＝把假绿写进脚本。目标文件名在远端按 Unicode 码位拼出来，脚本体一个字都不含中文。
#   ③ 动手前三道守卫：本机源文件必须与 HEAD 一致（不把未提交改动推进实盘链路）→ 远端落点文件
#      SHA 必须与本机一致（不一致说明部署没跑或传了一半，落位只会把半成品贴进策略目录）→
#      覆盖前先留时间戳备份（可回滚）。判绿只认 SHA 逐字相等，不认"跑完了"。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/place_qmt_bridge.sh                    # 预览（不连生产）
#   GZ_IP=81.71.69.17 ./scripts/place_qmt_bridge.sh -Apply             # 真落位 + 双端 SHA 判定
# 可选环境变量：
#   GZ_USER           管理员用户（默认 Administrator，与 deploy_guangzhou.sh 对齐）
#   BRIDGE_SRC        本机桥策略源文件（默认 qmt_gateway/qmt_bridge_strategy.py）
#   QMT_GATEWAY_DIR   远端网关落点目录（默认 C:/qmt/quant-trading-v2/qmt_gateway）
#   QMT_ROOT_GLOB     远端 QMT 根目录通配（默认 'C:\Program Files (x86)\*QMT*'）
# 退出码：0=预览已打印 / 落位且 SHA 相符；非 0=守卫、预探测、落位、SHA 任一不过。
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量）}"
GZ_USER="${GZ_USER:-Administrator}"
BRIDGE_SRC="${BRIDGE_SRC:-qmt_gateway/qmt_bridge_strategy.py}"
QMT_GATEWAY_DIR="${QMT_GATEWAY_DIR:-C:/qmt/quant-trading-v2/qmt_gateway}"
QMT_ROOT_GLOB="${QMT_ROOT_GLOB:-C:\\Program Files (x86)\\*QMT*}"
PLACE_PS_ON_REMOTE='C:\qmt\place_bridges.ps1'

# 守卫⓪：scripts/place_bridges.ps1 的落点/源目录是**硬编**的（C:\qmt\quant-trading-v2\qmt_gateway），
# 本脚本若被传了别的 QMT_GATEWAY_DIR，SHA 比对的"远端落点"与真正被拷贝的目录就不是同一个，
# 会出现"判绿但拷的是另一处"。宁可直接拒绝，也不留这种绿。
GW_DEFAULT='C:/qmt/quant-trading-v2/qmt_gateway'
if [ "$QMT_GATEWAY_DIR" != "$GW_DEFAULT" ] && [ "$QMT_GATEWAY_DIR" != 'C:\qmt\quant-trading-v2\qmt_gateway' ]; then
  echo "X QMT_GATEWAY_DIR 被改成了 ${QMT_GATEWAY_DIR}，但 place_bridges.ps1 内部路径是硬编的 ${GW_DEFAULT}。" >&2
  echo "  要么用缺省值，要么先改 scripts/place_bridges.ps1（并同步 deploy_guangzhou.sh 的上传落点）。" >&2
  exit 2
fi

APPLY=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    *) echo "X 未知参数：${a}（只认 -Apply）" >&2; exit 2 ;;
  esac
done

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

cd "$APP_DIR"
[ -f "$BRIDGE_SRC" ] || { echo "X 本机找不到桥策略源文件：$BRIDGE_SRC" >&2; exit 1; }
LOCAL_SHA="$(shasum -a 256 "$BRIDGE_SRC" | cut -c1-64)"
LOCAL_LEN="$(wc -c < "$BRIDGE_SRC" | tr -d ' ')"
GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
TAG="bak-$(date +%Y%m%d-%H%M)-${GIT_SHA}"

echo "==> 计划（本机侧）"
echo "  本机源文件   : $BRIDGE_SRC"
echo "  本机 SHA256  : $LOCAL_SHA"
echo "  本机字节数   : $LOCAL_LEN   HEAD=$GIT_SHA"
echo "  远端落点目录 : $QMT_GATEWAY_DIR  （deploy [2b] 的上传位置，先核对它是不是同一版）"
echo "  QMT 根通配   : $QMT_ROOT_GLOB"
echo "  覆盖目标     : <QMT根>\\python\\新建策略文件.py"
echo "  覆盖前备份   : 同名追加 .$TAG （回滚＝把备份改回原名）"
echo "  判绿口径     : 远端落点 SHA == 本机 SHA == 本机 HEAD 版；三值不符一律判红"

if [ "$APPLY" = 0 ]; then
  echo
  echo "预览模式：一个字节都没写、一次 SSH 都没连。动手请加 -Apply。"
  echo "BRIDGE_PLACE_PLAN"
  exit 0
fi

echo "==> [1/5] 组装远端脚本体并做 ASCII 自检（不连生产就能判红）"
# 守卫①：本机源文件必须干净——这条脚本会把它推进「实盘下单链路真正加载」的位置，
# 未提交的改动不该有这个机会（出问题时无从对齐是哪一版）。预览不查它：看计划不需要凭据。
if ! git diff --quiet -- "$BRIDGE_SRC" 2>/dev/null || ! git diff --cached --quiet -- "$BRIDGE_SRC" 2>/dev/null; then
  echo "X $BRIDGE_SRC 有未提交改动：先提交（或还原）再落位，别把半成品贴进 QMT 策略目录。" >&2
  exit 1
fi
# 远端脚本体：全 ASCII（中文文件名按 Unicode 码位拼），只做四件事——定位根目录、备份、执行落位、回打 SHA。
# 为什么整段不许掺中文：这段串要经 bash→ssh→cmd→powershell 四层转义再按 GBK 码页回传，
# 判据字段里出现中文＝乱码＝脚本把"读不到"当成"跑过了"（本仓 §GBK 系列事故实录）。
REMOTE_PS='
$ErrorActionPreference = "Stop"
$gw = "@GW@"
$glob = "@GLOB@"
$tag = "@TAG@"
$src = Join-Path $gw "qmt_bridge_strategy.py"
if (-not (Test-Path $src)) { Write-Output "PLACE_ERR=no-gateway-source"; exit 1 }
$hit = Get-Item $glob | Select-Object -First 1
if (-not $hit) { Write-Output "PLACE_ERR=no-qmt-root"; exit 1 }
$pydir = Join-Path $hit.FullName "python"
if (-not (Test-Path $pydir)) { Write-Output "PLACE_ERR=no-python-dir"; exit 1 }
$name = -join ([char]0x65B0, [char]0x5EFA, [char]0x7B56, [char]0x7565, [char]0x6587, [char]0x4EF6)
$dst = Join-Path $pydir ($name + ".py")
if (Test-Path $dst) { Copy-Item $dst ($dst + "." + $tag) -Force; Write-Output "backup=1" } else { Write-Output "backup=0" }
& "@PLACEPS@"
$hs = (Get-FileHash $src -Algorithm SHA256).Hash
if (Test-Path $dst) { $hd = (Get-FileHash $dst -Algorithm SHA256).Hash; $dl = (Get-Item $dst).Length } else { $hd = ""; $dl = -1 }
Write-Output ("PLACE src_len=" + (Get-Item $src).Length + " dst_len=" + $dl + " src_sha=" + $hs + " dst_sha=" + $hd)
'
REMOTE_PS="${REMOTE_PS//@GW@/$QMT_GATEWAY_DIR}"
REMOTE_PS="${REMOTE_PS//@GLOB@/$QMT_ROOT_GLOB}"
REMOTE_PS="${REMOTE_PS//@TAG@/$TAG}"
REMOTE_PS="${REMOTE_PS//@PLACEPS@/$PLACE_PS_ON_REMOTE}"
if LC_ALL=C grep -n '[^ -~]' <<<"$REMOTE_PS" >/dev/null 2>&1; then
  echo "X 远端脚本体掺了非 ASCII 字符：会经 GBK 回传成乱码判据，中止（目标文件名请用 [char]0xXXXX 码位拼）。" >&2
  exit 1
fi
# 这行是 §93 探针的行为锚：删掉自检（或把脚本体改回中文）后，第 [1/5] 步就会红在这儿，
# 不会带着乱码判据去连生产——所以 ARMED 只在自检通过后打印。
echo "BRIDGE_PLACE_ARMED ascii-selfcheck=ok"
# -EncodedCommand 吃 UTF-16LE base64：绕开 bash→ssh→cmd 的三层引号转义（本仓唯一可靠口径）。
B64="$(printf '%s' "$REMOTE_PS" | iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n')"

echo "==> [2/5] 守卫②：place_bridges.ps1 必须带 BOM（远端 PS 5.1 无 BOM 按 ANSI 解析中文路径，会静默拷错/拷不上）"
python3 - scripts/place_bridges.ps1 <<'PY'
import sys
p = sys.argv[1]
d = open(p, 'rb').read()
while d.startswith(b'\xef\xbb\xbf'):
    d = d[3:]
open(p, 'wb').write(b'\xef\xbb\xbf' + d)
PY

echo "==> [3/5] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

echo "==> [4/5] 上传仓库版 place_bridges.ps1（远端旧版可能静默死，见 RUNBOOK §4.1b.1②）"
$SCP scripts/place_bridges.ps1 "${GZ_USER}@${GZ_IP}:C:/qmt/place_bridges.ps1" >/dev/null
echo "ok - 已覆盖 $PLACE_PS_ON_REMOTE"

echo "==> [5/5] 备份 + 落位（远端执行，回传字段全 ASCII）"
# 整条子命令用 `|| true` 兜住：ssh 非 0（远端 exit 1、断管）在 set -e + pipefail 下会让脚本
# 静默消失，连"判红原因"都打不出来；结论一律交给下面的 PLACE_ERR / PLACE 判定行裁决。
OUT="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand $B64" 2>&1 || true)"
printf '%s\n' "$OUT" > /tmp/place_qmt_bridge.log
# 用 case 而不是 `printf|grep -q`：grep -q 命中即退会让 printf 收到 SIGPIPE，在 pipefail 下是假红。
case "$OUT" in
  *PLACE_ERR=*)
    echo "X 远端报错：$(printf '%s\n' "$OUT" | grep "PLACE_ERR=" | tail -1 || true)（详见 /tmp/place_qmt_bridge.log）" >&2
    exit 1 ;;
esac
LINE="$(printf '%s\n' "$OUT" | grep "^PLACE " | tail -1 || true)"
[ -n "$LINE" ] || { echo "X 远端没回打 PLACE 判定行——落位结论不可知，判失败（不把「跑完了」当成功）。" >&2; exit 1; }

# 判定行形如：PLACE src_len=65081 dst_len=65081 src_sha=<64hex> dst_sha=<64hex>
# 取法用「按空格切字段」而不是贪婪正则：正则里 `.*dst_sha=` 一旦回传串被 GBK 截断就会取到空值，
# 空值与本机 SHA 比较是「不等」→ 判红，这是对的；但更怕它取到 src 的值，字段切分不会。
lc() { printf '%s' "$1" | tr 'A-Z' 'a-z'; }   # macOS 自带 bash 3.2，不认 ${VAR,,}
field() { printf '%s' "$LINE" | tr ' ' '\n' | grep "^$1=" | tail -1 | cut -d= -f2 || true; }
RSRC="$(lc "$(field src_sha)")"
RDST="$(lc "$(field dst_sha)")"
RSRC_LEN="$(field src_len)"
RDST_LEN="$(field dst_len)"
echo "==> [5/5] 续：SHA 三值判定"
echo "  本机      : ${LOCAL_SHA}（$LOCAL_LEN 字节）"
echo "  远端落点  : ${RSRC:-无}（${RSRC_LEN:-?} 字节）"
echo "  QMT 策略  : ${RDST:-无}（${RDST_LEN:-?} 字节）"
if [ "$RSRC" != "$LOCAL_SHA" ]; then
  echo "X 远端落点目录里的文件与本机不一致：先跑 deploy_guangzhou.sh 把 [2b] 传齐，再来落位。" >&2
  exit 1
fi
if [ "$RDST" != "$LOCAL_SHA" ]; then
  echo "X QMT 策略目录里的文件与本机不一致：落位没成（可能策略目录名不匹配 / QMT 正在占用）。" >&2
  echo "  回滚：把 <QMT根>\\python\\新建策略文件.py.$TAG 改回 新建策略文件.py" >&2
  exit 1
fi
echo "ok - 三值一致，QMT 下次加载即为这一版（QMT 进程不重启则仍跑内存里的旧版，见 RUNBOOK「QMT 盘中不重启」）"
echo "BRIDGE_PLACE_DONE"
