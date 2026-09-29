#!/bin/bash
# ==============================================================================
# scripts/audit_big_funcs.sh — 巨函数统计的仓库内入口（口径见 tools/bigfuncs/main.go 文件头）
#
# 为什么有这条脚本（§0927AUDIT 复核批 09-29 定正第 1 条）：那份修改文档的 §〇 表替审计报告背书了
# 一句「≥120 行函数实测 59 个」，09-29 逐口径复跑都对不上——同一棵 `51752b3` 树按不同写法分别得到
# 66／57／77／80／89，而"59"的来源口径没人写过。**统计类结论一旦没有口径，下一轮审计就只能把
# 上一个数字当事实抄**，这正是本仓 §DEADGAUGE／§ROBUST 一路在清的"读数不可追"形态。
# 本脚本把口径钉成唯一实现：真正的计数在 `tools/bigfuncs`（go/ast 取函数声明到闭括号的行距，
# 不受字符串/注释里的花括号扰动——naive 花括号计数实测会把 internal/llm/probe.go 的
# llmBodyShape 虚报成 311 行），本脚本只做三件事：定位仓库根、校验作用域目录真实存在
# （路径写错会让统计恒为 0，而"0 个巨函数"看起来像好消息）、把参数原样透传。
#
# 用法：
#   scripts/audit_big_funcs.sh                      # 默认：internal cmd，阈值 120，Top 10
#   scripts/audit_big_funcs.sh --min 80 --top 20    # 换阈值与榜单长度
#   scripts/audit_big_funcs.sh --include-tests      # 把 _test.go 一并计入
#   scripts/audit_big_funcs.sh --only internal      # 换作用域（逗号分隔，如 internal,cmd）
#   scripts/audit_big_funcs.sh --baseline           # 只出一行机器可读读数（门禁/文档留痕用）
#   scripts/audit_big_funcs.sh --root /tmp/pre0928 --baseline  # 对历史检出树复跑同一口径
#
# 只读脚本：不写任何文件、不参与部署清单（Mac 侧审计工具，现网无执行体）。
# English: repo entry point for the fixed-rule big-function counter implemented in
# tools/bigfuncs (go/ast based). This wrapper resolves the repo root, rejects a
# missing scope instead of reporting zero, and forwards flags verbatim.
# Read-only; never part of any deploy manifest.
# ==============================================================================
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || { echo "FAIL 无法进入仓库根（REPO_ROOT=${REPO_ROOT}）"; exit 1; }

if ! command -v go >/dev/null 2>&1; then
	echo "FAIL 本机无 go 工具链：巨函数口径依赖 go/ast，无法用文本猜测替代"
	exit 1
fi

# --only 的存在性校验在这一层做（go 侧同样会硬停，这里提前失败只为给出更短的报错）。
# 未显式给 --only 时按 go 侧默认 internal,cmd 校验，保持两侧口径一致。
# 给了 --root（对历史检出树复跑同一口径）时跳过本地校验——那棵树不在本仓路径下，
# 存在性由 go 侧按 --root 拼接后硬判，这里误报反而会把合法的历史复跑拦死。
SCOPES="internal,cmd"
HAS_ROOT=0
prev=""
for a in "$@"; do
	[ "$prev" = "--only" ] && SCOPES="$a"
	case "$a" in
	--root | --root=*) HAS_ROOT=1 ;;
	esac
	prev="$a"
done
if [ "$HAS_ROOT" = "0" ]; then
	IFS=',' read -r -a SCOPE_ARR <<<"$SCOPES"
	for s in "${SCOPE_ARR[@]}"; do
		s="$(printf '%s' "$s" | tr -d ' ')"
		[ -n "$s" ] || continue
		[ -d "$s" ] || { echo "FAIL 作用域目录不存在：${s}（拒绝按 0 计数收口）"; exit 1; }
	done
fi

go run ./tools/bigfuncs "$@"
rc=$?
[ "$rc" = "0" ] || { echo "FAIL 统计过程异常（rc=${rc}）"; exit "$rc"; }
