#!/usr/bin/env bash
# backfill_minute_guangzhou.sh — §MINUTE-OPS（2026-09-24）：生产侧（广州 Windows）分钟 K
# 一次性回填的**正规通道**。**缺省只预览（连一次 SSH 都不发），动手必须显式 -Apply。**
#
# 为什么要这条脚本：分钟 K 落库（§MINUTE-K）的取数与建表全在 dataload 里，而部署链只负责
# 把 dataload.exe 传上去，夜间研究链的 minute_sync 步在**表还是空的时候被门控摘掉**
# （internal/api/nightly 那边 removeStep + opslog），所以"生产库有分钟数据"这一步历来没有
# 入口——只能人手敲 ssh。人手敲 ssh 就是本仓纪律明令禁止的（生产访问只准走仓库现成脚本），
# 而且长任务挂在 ssh 上：断管即中断，还读不到结论（09-24 部署实录的 scp 半态就是同一类）。
#
# 五条设计取舍：
#   ① **缺省纯预览**：只打印将要执行的确切远端命令、判绿口径、时间窗告警。门禁（verify_changes.sh §93）
#      因此能离线跑它，不需要网络也不需要凭据。
#   ② 长任务交给**一次性计划任务**（schtasks /SC ONCE，跑完不再自触），而不是挂在 ssh 会话上：
#      回填是小时级，会话一断就成了半截活。触发时间取"远端现在 +1 分钟"且**不额外 /Run**，
#      否则 ONCE 任务会在 /Run 之后又在计划时刻再跑一遍（同一份清单拉两次＝白挤带宽与上游配额）。
#   ③ 判绿只看**纯 ASCII 锚点行**（dataload 的 MINUTE-SYNC START/PROGRESS/SUMMARY，见
#      cmd/dataload/minute_sync.go）：中文日志经 PowerShell→SSH 回传会按 GBK 码页打乱，
#      拿中文当判据＝把假绿写进脚本（§GBK 系列教训）。锚点行缺字段一律判"未收尾"，不猜成功。
#   ④ 时间窗硬闸：凌晨 03:30~05:30 拒发（quant-backup-snap 每晚 04:00 打快照，回填是几十分钟级
#      IO/CPU 负载，且那台机只有 2 核——把重活按在快照头上是本仓 §P0-B 明令避免的事故形态）。
#   ⑤ 不新增任何凭据：复用 deploy_guangzhou.sh 同一套 SSH 口径（BatchMode 预探测 + IdentitiesOnly
#      + $HOME/.ssh/id_rsa），私钥不可读就立刻失败，绝不落到可能挂起的密码认证路径。
#
# 用法（本地 macOS）：
#   GZ_IP=81.71.69.17 ./scripts/backfill_minute_guangzhou.sh                 # 预览（不连生产）
#   GZ_IP=81.71.69.17 ./scripts/backfill_minute_guangzhou.sh -Apply          # 装一次性任务并返回
#   GZ_IP=81.71.69.17 MODE=status  ./scripts/backfill_minute_guangzhou.sh    # 随时查进度/结论（只读）
#   GZ_IP=81.71.69.17 MODE=cleanup -Apply ./scripts/backfill_minute_guangzhou.sh  # 删任务（只读日志不删）
# 可选环境变量：
#   GZ_USER        管理员用户（默认 Administrator，与 deploy_guangzhou.sh 对齐）
#   DEPLOY_DIR     远端二进制目录（默认 C:/opt/quant）
#   DATA_DIR       远端数据目录（默认 C:/var/lib/quant-trading-v2，日志也落这里）
#   MINUTE_SCALE / MINUTE_COUNT / MINUTE_LIMIT / MINUTE_SINCE / MINUTE_MAX_FAIL_PCT
#                  装载入参（缺省 5 / 5025 / 500 / 空=按池近 90 日 / 10）——先 --limit 500 跑一桶，
#                  确认上游没封 IP 再谈放量，别一上来就把全市场按在 2 核机上。
# 退出码：0=预览已打印 / 任务已装载 / 状态为成功或在跑；非 0=守卫、预探测、时间窗、
#         锚点行判红（含"任务在但日志没收尾"这种半态）。
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量）}"
GZ_USER="${GZ_USER:-Administrator}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
MINUTE_SCALE="${MINUTE_SCALE:-5}"
MINUTE_COUNT="${MINUTE_COUNT:-5025}"
MINUTE_LIMIT="${MINUTE_LIMIT:-500}"
MINUTE_SINCE="${MINUTE_SINCE:-}"
MINUTE_MAX_FAIL_PCT="${MINUTE_MAX_FAIL_PCT:-10}"
MODE="${MODE:-plan}"
TASK_NAME="quant-minute-backfill"
LOG_GLOB='backfill-minute-*.log'

APPLY=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    *) echo "X 未知参数：${a}（只认 -Apply）" >&2; exit 2 ;;
  esac
done
case "$MODE" in
  plan|apply|status|cleanup) ;;
  *) echo "X MODE 只认 plan/apply/status/cleanup，收到 $MODE" >&2; exit 2 ;;
esac
# MODE=apply 必须同时给 -Apply：两个开关一个是"要干的事"、一个是"授权我干"，
# 少一个都不动手（缺省方向统一＝预览，见 §OPS-ALIGN）。
if [ "$MODE" = "plan" ] && [ "$APPLY" = 1 ]; then MODE=apply; fi
if { [ "$MODE" = "apply" ] || [ "$MODE" = "cleanup" ]; } && [ "$APPLY" = 0 ]; then
  echo "X MODE=$MODE 会动生产（装/删计划任务），必须同时显式传 -Apply。当前只打印计划。" >&2
  MODE=plan
fi

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

cd "$APP_DIR"
# 远端命令行：路径都不带空格，所以整条串里一个引号都不需要（引号经 bash→ssh→cmd→powershell
# 四层转义是本项目翻车率最高的写法）。--since 留空时不传，交给 dataload 的"缺省近 90 日"。
EXE="${DEPLOY_DIR}/dataload.exe"
DB="${DATA_DIR}/trading.db"
REMOTE_CMD="${EXE} -db ${DB} minute-sync --scale ${MINUTE_SCALE} --count ${MINUTE_COUNT} --limit ${MINUTE_LIMIT} --max-fail-pct ${MINUTE_MAX_FAIL_PCT}"
[ -n "$MINUTE_SINCE" ] && REMOTE_CMD="${REMOTE_CMD} --since ${MINUTE_SINCE}"

echo "==> 计划（§MINUTE-OPS 生产侧分钟 K 回填）"
echo "  装载器       : $EXE   （deploy_guangzhou.sh 每次 [3/5] 都会重传，先确认已部署过）"
echo "  研究库       : $DB"
echo "  子命令       : $REMOTE_CMD"
echo "  落库口径     : 不复权 5 分钟；主键 (ts_code,scale,ts) 幂等 ⇒ 重复跑不产生双行，可断点续跑"
echo "  清单来源     : 池排名（--limit ${MINUTE_LIMIT} 截断）。生产夜间链的 minute_sync 步在表为空时被门控摘掉，"
echo "                 所以**回填必须由本脚本干一次**，之后日增才会自己入队（scale=5 count=60）。"
echo "  远端承载     : 一次性计划任务 ${TASK_NAME}（跑完不再自触），日志 $DATA_DIR/$LOG_GLOB"
echo "  判绿口径     : 日志里最后一条 MINUTE-SYNC SUMMARY 的 exit=0（纯 ASCII 锚点行）；"
echo "                 缺 SUMMARY 行一律按「未收尾」判红，不看「任务跑过了」"
echo "  时间窗       : 远端 03:30~05:30 拒发（quant-backup-snap 04:00 打快照，别把重活按上去）"
echo "  上游现实     : 分钟源只有「最近 N 根」窗口，5025 根≈100+ 交易日；--count 拉再大也量不出三年历史"

if [ "$MODE" = "plan" ]; then
  echo
  echo "预览模式：一个字节都没写、一次 SSH 都没连。动手请加 -Apply；查进度用 MODE=status。"
  echo "MINUTE_OPS_PLAN"
  exit 0
fi

echo "==> [1/4] 组装远端脚本并做 ASCII 自检（本机就能判红，不必先连生产）"
# 远端脚本体：**全 ASCII**（含中文判据一律禁止，理由见文件头③），三条支路共用一份：
#   MODE=apply   预检（exe/库在位？dataload 还在跑？时间窗？）→ 装一次性任务
#   MODE=status  找最新日志 → 回打锚点行 + 进程数 + 任务在位否
#   MODE=cleanup 进程还在跑就拒绝；否则删任务（日志保留，那是唯一的证据）
# 值用 @XX@ 占位后替换：避免在 bash 单引号串里做 PowerShell 变量插值（$ 会被两边同时抢）。
REMOTE_PS='
$ErrorActionPreference = "SilentlyContinue"
$exe = "@EXE@" -replace "/","\"
$db  = "@DB@"  -replace "/","\"
$dir = "@DATA_DIR@" -replace "/","\"
$cmd = "@CMD@" -replace "/","\"
$tn  = "@TN@"
$glob = "@GLOB@"
$mode = "@MODE@"
$now = (Get-Date).AddMinutes(1)
$sd = $now.ToString("yyyy/mm/dd")
$st = $now.ToString("HH:mm")
$stamp = (Get-Date).ToString("yyyyMMdd-HHmm")
$log = Join-Path $dir ("backfill-minute-" + $stamp + ".log")
# Count by process name only, never by .Path: reading .Path of a SYSTEM-owned process from
# an admin session is usually denied, so filtering on it would turn "already running" into a fake 0.
function Procs { return @(Get-Process -Name "dataload").Count }
function NewestLog {
  $f = Get-ChildItem -Path $dir -Filter $glob | Sort-Object LastWriteTime -Descending | Select-Object -First 1
  return $f
}
function Anchors($f) {
  # -Encoding UTF8 is mandatory: the default code page of PS 5.1 is GBK, which can swallow the
  # line terminator of a Chinese line and merge it with the next (ASCII) anchor line.
  Get-Content -Path $f.FullName -Encoding UTF8 | Select-String -Pattern "MINUTE-SYNC (START|PROGRESS|SUMMARY)" | ForEach-Object { $_.Line }
}
if ($mode -eq "status") {
  $t = 0; schtasks /Query /TN $tn /FO LIST | Out-Null; if ($LASTEXITCODE -eq 0) { $t = 1 }
  $f = NewestLog
  if ($null -eq $f) {
    Write-Output ("MINOPS STATE task=" + $t + " procs=" + (Procs) + " log=none dir=" + $dir)
    exit 0
  }
  Write-Output ("MINOPS STATE task=" + $t + " procs=" + (Procs) + " log=" + $f.Name + " bytes=" + $f.Length + " mtime=" + $f.LastWriteTime.ToString("yyyyMMdd-HHmmss"))
  Anchors $f | Select-Object -Last 40 | ForEach-Object { Write-Output $_ }
  exit 0
}
if ($mode -eq "cleanup") {
  if ((Procs) -gt 0) { Write-Output "MINOPS ERR=cleanup-refused-dataload-running"; exit 1 }
  schtasks /Delete /TN $tn /F | Out-Null
  if ($LASTEXITCODE -ne 0) { Write-Output "MINOPS ERR=cleanup-failed"; exit 1 }
  Write-Output ("MINOPS CLEANED task=" + $tn)
  exit 0
}
if (-not (Test-Path $exe)) { Write-Output "MINOPS ERR=no-dataload-exe"; exit 1 }
if (-not (Test-Path $db))  { Write-Output "MINOPS ERR=no-research-db"; exit 1 }
if ((Procs) -gt 0) { Write-Output "MINOPS ERR=dataload-already-running"; exit 1 }
# Refuse 03:30-05:30 (the trigger time is now+1min, so the window is checked on the trigger, not on "now").
$mins = $now.Hour * 60 + $now.Minute
if ($mins -ge 210 -and $mins -lt 330) { Write-Output ("MINOPS ERR=backup-window start=" + $st); exit 1 }
$tr = "cmd /c " + $cmd + " >> " + $log + " 2>&1"
schtasks /Create /F /TN $tn /SC ONCE /SD $sd /ST $st /RU SYSTEM /TR $tr | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Output "MINOPS ERR=schtasks-create-failed"; exit 1 }
schtasks /Query /TN $tn /FO LIST | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Output "MINOPS ERR=task-not-registered"; exit 1 }
Write-Output ("MINOPS STARTED task=" + $tn + " start=" + $sd + " " + $st + " log=" + $log)
'
REMOTE_PS="${REMOTE_PS//@EXE@/$EXE}"
REMOTE_PS="${REMOTE_PS//@DB@/$DB}"
REMOTE_PS="${REMOTE_PS//@DATA_DIR@/$DATA_DIR}"
REMOTE_PS="${REMOTE_PS//@CMD@/$REMOTE_CMD}"
REMOTE_PS="${REMOTE_PS//@TN@/$TASK_NAME}"
REMOTE_PS="${REMOTE_PS//@GLOB@/$LOG_GLOB}"
REMOTE_PS="${REMOTE_PS//@MODE@/$MODE}"
# 一个非 ASCII 字节都不许有：混进中文就会在 GBK 回传里变成乱码判据（本地就把这种改动拦下来）。
if LC_ALL=C grep -n '[^ -~]' <<<"$REMOTE_PS" >/dev/null 2>&1; then
  echo "X 远端脚本体掺了非 ASCII 字符（会造成 GBK 乱码判据），中止。" >&2
  exit 1
fi
# -EncodedCommand 吃 UTF-16LE base64：绕开 bash→ssh→cmd 的三层引号转义（本仓唯一可靠口径）。
B64="$(printf '%s' "$REMOTE_PS" | iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n')"
# 走到这里＝远端脚本已组装完且自检通过（还没连生产）。这行是 §93 变异证据的落点：
# 把它删掉，第 4 步探针就会红——所以"探针对红"必须同源存在，不能靠人记。
echo "MINUTE_OPS_ARMED mode=$MODE task=$TASK_NAME"

echo "==> [2/4] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

echo "==> [3/4] 远端执行 MODE=${MODE}（回传字段全 ASCII）"
# `|| true` 兜住 ssh 非 0：否则 set -e 会让脚本静默消失，连判红原因都打不出来。
OUT="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand $B64" 2>&1 || true)"
LOGF="/tmp/backfill_minute_guangzhou.$MODE.log"
printf '%s\n' "$OUT" > "$LOGF"
# 用 case 而不是 printf|grep -q：grep -q 命中即退会让 printf 收到 SIGPIPE，pipefail 下是假红。
case "$OUT" in
  *"MINOPS ERR="*)
    echo "X 远端拒绝：$(printf '%s\n' "$OUT" | grep "MINOPS ERR=" | tail -1 || true)（详见 ${LOGF}）" >&2
    echo "  常见成因：dataload.exe 未部署（先跑 deploy_guangzhou.sh）/ 已有装载在跑 / 撞上 04:00 快照窗。" >&2
    exit 1 ;;
esac

if [ "$MODE" = "cleanup" ]; then
  case "$OUT" in
    *"MINOPS CLEANED"*)
      printf '%s\n' "$OUT" | grep "MINOPS CLEANED" || true
      echo "ok - 一次性任务已删除（日志留在远端数据目录，那是唯一的执行证据，不删）"
      echo "MINUTE_OPS_DONE"
      exit 0 ;;
    *)
      echo "X 远端没回打 MINOPS CLEANED——删除结论不可知，判失败。" >&2
      exit 1 ;;
  esac
fi

if [ "$MODE" = "apply" ]; then
  case "$OUT" in
    *"MINOPS STARTED"*) printf '%s\n' "$OUT" | grep "MINOPS STARTED" || true ;;
    *) echo "X 远端没回打 MINOPS STARTED——装载结论不可知，判失败（不把「命令发出去了」当成功）。" >&2; exit 1 ;;
  esac
  echo "  任务已装到远端，将在一分钟后自触（**不会**重复自触，/SC ONCE）。"
  echo "  回填是小时级：现在就可以收工，随时用 MODE=status 查，不需要挂 ssh 会话。"
  echo "MINUTE_OPS_STARTED"
  exit 0
fi

echo "==> [4/4] 状态判读（只认锚点行）"
# 锚点行**不带 ^**：dataload 的 log.SetFlags 会在每行前打时间戳（"2026-09-24 21:53:00.123 MINUTE-SYNC …"），
# 锚点在行中而不是行首，写成 ^ 就是恒不命中⇒永远读成"没收尾"。
SUMMARY="$(printf '%s\n' "$OUT" | grep "MINUTE-SYNC SUMMARY" | tail -1 || true)"
PROGRESS="$(printf '%s\n' "$OUT" | grep "MINUTE-SYNC PROGRESS" | tail -1 || true)"
STATE="$(printf '%s\n' "$OUT" | grep "MINOPS STATE" | tail -1 || true)"
if [ -n "$STATE" ]; then echo "  $STATE"; fi
if [ -n "$PROGRESS" ]; then echo "  $PROGRESS"; fi
if [ -n "$SUMMARY" ]; then echo "  $SUMMARY"; fi

# 判定顺序＝先结论、再过程、最后"什么都没留下"。三条都刻意不做任何猜测性放行：
if [ -z "$STATE" ]; then
  echo "X 远端没回打 MINOPS STATE——状态查询本身失败（PowerShell 报错或通道异常），不给结论。详见 $LOGF" >&2
  exit 1
fi
if [ -n "$SUMMARY" ]; then
  case "$SUMMARY" in
    *"exit=0"*)
      echo "ok - 回填收尾成功。后续日增交给夜间链 minute_sync 步（表已非空，门控自动放行）。"
      echo "MINUTE_OPS_STATE done"
      echo "MINUTE_OPS_DONE"
      exit 0 ;;
    *)
      reason="$(printf '%s' "$SUMMARY" | tr ' ' '\n' | grep "^reason=" | tail -1 || true)"
      echo "X 回填跑完但判失败：${reason:-exit!=0}。原因与修法：" >&2
      echo "   zero_rows     = 上游全失败（多为广州机出口被临时限流/封 IP），隔一会儿重跑 -Apply 即可（幂等）。" >&2
      echo "   fail_rate     = 失败率超上限，属接口改版或批量封 IP，先看单机取数是否正常再放量。" >&2
      echo "   universe_empty= 生产库池表为空（先跑池同步/日 K 装载，回填才有清单）。" >&2
      echo "MINUTE_OPS_STATE failed"
      exit 1 ;;
  esac
fi
case "$STATE" in
  *"procs=0"*) ;;
  *) echo "  → 装载进程仍在跑（dataload 在位），进度看上面 PROGRESS 行；跑完再来查。"
     echo "MINUTE_OPS_STATE running"
     exit 0 ;;
esac
if [ -n "${PROGRESS:-}" ]; then
  # procs=0 且已有 PROGRESS ⇒ 跑到一半进程就没了（被杀/重启）。上面已把最后一条进度打出来，
  # 那就是"停在第几只"的证据；这里绝不把"有过进度"读成"还在推进"。
  echo "X 有进度锚点行但 dataload 已不在跑、也没有 SUMMARY ⇒ 半态停在上面那条 done= 之后。" >&2
  echo "MINUTE_OPS_STATE stalled"
  exit 1
fi
case "$STATE" in
  *"log=none"*)
    echo "X 任务已装但远端找不到日志：要么还没到触发时刻（等一分钟再查），要么 cmd 的重定向没成（看 $LOGF 的 PowerShell 报错）。" >&2
    echo "MINUTE_OPS_STATE pending"
    exit 1 ;;
esac
echo "X 有日志、无 SUMMARY 锚点行、且 dataload 不在跑 ⇒ 半态（进程被杀/机器重启/命令行拼错）。" >&2
echo "  不猜结论：把上面 MINOPS STATE 的 log= 文件名取下来看尾部，确认停在哪一步；重跑 -Apply 即可（落库幂等）。" >&2
echo "MINUTE_OPS_STATE stalled"
exit 1
