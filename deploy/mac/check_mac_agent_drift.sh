#!/usr/bin/env bash
# check_mac_agent_drift.sh — 定时任务真正执行的那份字节 vs 仓库源 的漂移探测器（§MAC-DRIFT 2026-10-10）
#
# 存在理由（今天实锚出来的缺陷，不是假想敌）：10-07 波 3 把拉取腿的告警通道做了硬化
#   （ntfy 主题出仓改走钥匙串单实现 + 空主题必须落 ALERT-NOT-SENT），仓库版三连改到 10-07 00:53，
#   而 launchd 每天 07:00/10:30 执行的是 ~/backups/quant/bin/ 那份**稳定副本**——它的 mtime 停在
#   09-30 00:41。于是门禁 113 段全绿、Playwright 全绿、双注释闸全绿，全部绿在"仓库那份文件"上，
#   定时任务吃的是另一份字节；且这一族**没有"下一次部署"会把它带上**——广州侧至少还有 -s 发版
#   这条通道会把脚本面推平，Mac 侧的 -Apply 只有人坐在前面的时候才会跑。
#   同一次排摸里夜间七腿的镜像副本也停在 09-30，10-09 §W7-D 的"两张表量纲抽检"从没在夜里跑过。
#   ⇒ 这是"有代码无生效"的第三种形态：前两种是「有脚本无调度」和「有调度无读数」，本条是
#   「有调度、也有读数，但读的是旧代码」。广州侧同类问题靠 verify 探针回显指纹自证，
#   Mac 侧此前没有任何面把"副本 == 源"说出来，所以门禁里连一行相关读数都不存在（§0929DRILL 锤的就是这种"没人看"）。
#
# 用法：
#   deploy/mac/check_mac_agent_drift.sh              # 逐对打印状态 + 汇总，漂移即非零退出
#   deploy/mac/check_mac_agent_drift.sh -Json        # 只打汇总行（给外层脚本/日志用）
#   REPO_ROOT=/path/to/quant-trading-v2 deploy/mac/check_mac_agent_drift.sh
#
# 退出码（语义写死，外层靠它决定要不要吵）：
#   0 = 每一对都 same（副本与源字节一致）
#   1 = 存在 diff / missing-mirror / missing-repo / unresolved（要么源改完没重装，要么副本被铲了）
#   2 = 派生本身坏了（解析出的对数低于下限）——红必须能来自探测器自己，
#       否则"清单读不到"会和"没有漂移"共用一个绿（本仓反复锤的空 glob 静默空转）。
#
# 纪律对齐：
#   - 清单**按安装器文件名形状从 cp 行派生**（install_mac_ 前缀安装器全集），不另写第二份文件清单：
#     写死清单的必然结局是"改安装器忘了改这里"，漏掉的那一对会永远显示"不在检查面内"
#     （§0929 收尾批把锁面从写死清单改派生，同一取向；2026-10-10 第四个安装器上场时这条派生救了这一面）。
#   - 只读：本脚本一个字节都不写。修法在对应安装器的 -Apply（带副作用的正规通道，
#     按 Mac 侧同样条款：先跑一次缺省预览看它打算做什么，再 -Apply）。
#   - 不内嵌公网 IP / 32-hex 主题串（deploy/mac 文件面的派生负锁扫的是整个目录，本文件也在面内）。
set -uo pipefail

QUIET=0
[ "${1:-}" = "-Json" ] && QUIET=1

# 仓库根：缺省按"本脚本在 deploy/mac/ 下"反推两次；外层要指别的检出目录就传 REPO_ROOT。
# 不把 BASH_SOURCE 的目录当唯一依据：稳定副本目录里也可能被拷一份本脚本，那种情况下反推出来的是
# ~/backups/quant/bin，会得到"仓库里没有 scripts/…"的假红——所以留 env 口子，且读数里回显 repo_root。
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${SELF_DIR}/../.." && pwd)}"
export REPO_ROOT QUIET

# 解析逻辑放临时 python（三个安装器的赋值/cp 形态用正则展开比 shell 可靠）。
# 为什么不把 heredoc 塞进 "$( ... )"：macOS 的系统 bash 是 3.2，双引号命令替换里的带引号 heredoc
# 会把体内的单引号当成外层引号去配对（本批实踩：unexpected EOF while looking for matching `'`），
# 换成先落临时文件再执行，引号归属只由 python 自己管。
DRIFT_PY="$(mktemp /tmp/check_mac_agent_drift_XXXXXX.py)"
[ -n "$DRIFT_PY" ] || { echo "DRIFT-SUMMARY|pairs=0 state=mktemp-failed" ; exit 2; }
trap 'rm -f "$DRIFT_PY"' EXIT
cat > "$DRIFT_PY" <<'PY'
# 一次性解析各安装器：先收 NAME=value 形态的变量赋值，再把 cp 的两端展开成实际路径。
# 只做"够用且不骗人"的展开：值里含命令替换 $( ) 或反引号的赋值跳过（静态展不开），
# 展开后仍留 ${ 的 cp 行归入 unresolved 并点名——**不静默丢弃**，丢弃＝把覆盖缺口伪装成通过。
import glob, hashlib, os, re, sys

repo_root = os.environ["REPO_ROOT"]
quiet = os.environ.get("QUIET", "0") == "1"
mac_dir = os.path.join(repo_root, "deploy", "mac")
# 安装器清单**按文件名形状派生**（按 install_mac_ 前缀排序），不写死名字列表：
# 首版写死三个名字，2026-10-10 §MAC-WATCHDOG 补第四个安装器时当场现形——新安装器的 cp 行
# 不在检查面内，它镜像的文件漂了探测器也不响（清单式锁失明的第 N 次复发，§BOM-REPO-DERIVE 同族）。
# 派生之后"新增第 N 个安装器"自动进面，本文件不用跟着改；glob 空转由下面的下限退 2 兜住。
installers = sorted(os.path.basename(p) for p in glob.glob(os.path.join(mac_dir, "install_mac_*_agent.sh")))

# 预置：REPO_ROOT / REPO_MAC_DIR 由调用方给真值（安装器里那两个是命令替换，静态推不出）；
# HOME 用当前用户主目录（副本一律落在 ~/backups/quant/ 下，非 TCC 保护目录，见 §MAC-TCC）。
env = {
    "REPO_ROOT": repo_root,
    "REPO_MAC_DIR": mac_dir,
    "HOME": os.path.expanduser("~"),
}

VAR_RE = re.compile(r"(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$")
CP_RE = re.compile(r"(?m)^[ \t]*cp[ \t]+(\S+)[ \t]+(\S+)")


def unquote(s):
    """剥掉整对首尾引号（只在两侧同字符时剥，避免把 cp 的尾参数误伤）。"""
    s = s.strip()
    if len(s) >= 2 and s[0] == s[-1] and s[0] in ('"', "'"):
        return s[1:-1]
    return s


def expand(s, table):
    """一次展开带大括号的写法与省大括号的裸写法，两轮（安装器里有 A="$B/x" 这种引用前一个变量的写法）。"""
    def rep(m):
        key = m.group(1) or m.group(2)
        return table.get(key, m.group(0))
    prev = None
    cur = s
    for _ in range(2):
        cur = re.sub(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)", rep, cur)
        if cur == prev:
            break
        prev = cur
    return cur


def sha12(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for blk in iter(lambda: fh.read(1 << 16), b""):
            h.update(blk)
    return h.hexdigest()[:12]


pairs = []
unresolved = []
for ins in installers:
    ipath = os.path.join(mac_dir, ins)
    if not os.path.isfile(ipath):
        unresolved.append("%s:installer-missing" % ins)
        continue
    txt = open(ipath, encoding="utf-8").read()
    table = dict(env)
    for m in VAR_RE.finditer(txt):
        name, raw = m.group(1), m.group(2).rstrip()
        if "$(" in raw or "`" in raw:
            continue
        table[name] = expand(unquote(raw), table)
    for m in CP_RE.finditer(txt):
        src, dst = unquote(m.group(1)), unquote(m.group(2))
        # 带命令替换（备份旧产物那种 .bak-$(date …)）或通配的目标不是"副本 == 源"的一对，跳过。
        if "$(" in src or "$(" in dst or "*" in src or "*" in dst:
            continue
        esrc, edst = expand(src, table), expand(dst, table)
        if "${" in esrc or "${" in edst or "$" in esrc or "$" in edst:
            unresolved.append("%s:%s->%s" % (ins, esrc, edst))
            continue
        pairs.append((ins, os.path.normpath(esrc), os.path.normpath(edst)))

counts = {"same": 0, "diff": 0, "missing-mirror": 0, "missing-repo": 0}
for ins, src, dst in pairs:
    if not os.path.isfile(src):
        st = "missing-repo"
    elif not os.path.isfile(dst):
        st = "missing-mirror"
    elif sha12(src) == sha12(dst):
        st = "same"
    else:
        st = "diff"
    counts[st] = counts.get(st, 0) + 1
    if not quiet:
        print("DRIFT|state=%s|installer=%s|repo=%s|mirror=%s" % (st, ins, src, dst))

# 下限 6：三个安装器当前各自镜像的脚本件（backup 2 + nightly 2 + drill 4，去掉 plist 行后仍 ≥6）。
# 低于下限要么"文件变少了"要么"解析法跟不上安装器写法"，两种都必须吵——没有"少几对也算过"的余地。
n_pairs = len(pairs)
bad = counts.get("diff", 0) + counts.get("missing-mirror", 0) + counts.get("missing-repo", 0)
print("DRIFT-SUMMARY|pairs=%d same=%d diff=%d missing_mirror=%d missing_repo=%d unresolved=%d repo_root=%s"
      % (n_pairs, counts.get("same", 0), counts.get("diff", 0), counts.get("missing-mirror", 0),
         counts.get("missing-repo", 0), len(unresolved), repo_root))
for u in unresolved[:5]:
    print("DRIFT-UNRESOLVED|%s" % u)

if n_pairs < 6:
    sys.exit(2)
sys.exit(1 if (bad or unresolved) else 0)
PY

python3 "$DRIFT_PY"
rc=$?
exit "$rc"
