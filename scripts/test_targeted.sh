#!/usr/bin/env bash
# ── 定向测试入口（§TEST-SPEED 2026-11-21）──
# 为什么要有这个脚本：全量 verify_changes.sh 有 228 段、282 次 go test + 20 次 npm test，
# 单轮动辄几十分钟——日常改一两行就跑全量，绝大部分时间花在与本次改动无关的段上。
# 本脚本把"只跑我改的那块"做成默认姿势：给定 Go 包/测试名或前端测试文件名，只跑指定目标。
# 全量验收仍交给 verify_changes.sh（默认模式 / -full），本脚本绝不替代它——
# 快速通道绿灯不等于门禁通过，发版前必须跑一次全量（与 §TEST-QUICK 同一纪律）。
#
# 用法：
#   ./scripts/test_targeted.sh go <包路径> [测试名正则]
#       ./scripts/test_targeted.sh go ./internal/store                    # 整包
#       ./scripts/test_targeted.sh go ./internal/scheduler TestNightcap   # 包 + 名字过滤
#       ./scripts/test_targeted.sh go ./internal/config ./internal/paper  # 多个包
#   ./scripts/test_targeted.sh web <测试文件名片段> [更多片段...]
#       ./scripts/test_targeted.sh web utils.test                         # 单个 vitest 文件
#       ./scripts/test_targeted.sh web m13_forbidden_poll native_auth     # 多个文件
#   ./scripts/test_targeted.sh nightcap        # 专门跑此前最慢的 nightcap 两条腿（现亚秒级）
#
# 环境变量：
#   GO_TEST_FLAGS   追加给 go test 的额外参数（默认 -count=1 禁结果缓存，与门禁一致）
#   VITEST_MAX_WORKERS  透传给 vitest 的并发度（默认取 vitest.config.js 的 4，低内存机设 1）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

usage() {
	sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}

# go/web 至少要模式+目标两个参数；nightcap 是内置快捷方式，只需模式本身一个参数。
[ "$#" -ge 1 ] || usage
if [ "$#" -eq 1 ] && [ "$1" != "nightcap" ]; then usage; fi
MODE="$1"
shift

case "$MODE" in
go)
	# 第一个参数是包路径（可多个），剩余可选参数是 -run 的测试名正则（用 | 连接）。
	packages=()
	patterns=()
	for arg in "$@"; do
		case "$arg" in
		./[!-]*) packages+=("$arg") ;;
		*) patterns+=("$arg") ;;
		esac
	done
	[ "${#packages[@]}" -gt 0 ] || { echo "必须给至少一个 Go 包路径（./internal/...）" >&2; exit 2; }
	flags="${GO_TEST_FLAGS:--count=1}"
	if [ "${#patterns[@]}" -gt 0 ]; then
		run_re=$(IFS='|'; echo "${patterns[*]}")
		echo "==> 定向 Go 测试：包 ${packages[*]}，-run /${run_re}/"
		# shellcheck disable=SC2086
		go test $flags "${packages[@]}" -run "$run_re"
	else
		echo "==> 定向 Go 测试：包 ${packages[*]}（整包，无 -run 过滤）"
		# shellcheck disable=SC2086
		go test $flags "${packages[@]}"
	fi
	;;
web)
	[ "$#" -gt 0 ] || { echo "web 模式至少要一个测试文件名片段" >&2; exit 2; }
	echo "==> 定向前端测试：vitest -- $*"
	(cd web && npm test -- "$@")
	;;
nightcap)
	echo "==> nightcap 两条腿（注入秒级预算，应 <3s 跑完）"
	go test -count=1 ./internal/scheduler/ -run 'TestNightcapBudgetCapYieldsTask|TestNightcapHeadBudgetDefersAndReleasesQueue'
	;;
*)
	echo "未知模式 ${MODE}（可选 go / web / nightcap）" >&2
	usage
	;;
esac
