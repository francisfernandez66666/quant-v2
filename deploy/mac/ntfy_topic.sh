#!/bin/bash
# ntfy_topic.sh — Mac 侧告警主题的**单一取用口径**（§KUMA-SECREDTO 2026-10-07 修复批 波 3）。
#
# 为什么必须有这个文件（成因，别只当卫生改动）：
#   ntfy 的口径是「知道主题就能往那个主题发帖」⇒ 主题串本身就是凭据。而 0929 之前
#   deploy/mac 下**三个文件各写了一份同一个 32-hex 缺省值**（kuma_seed.js:10、
#   restic_pull_backup.sh:19、verify_restore.sh:71）。三份并存的结局就是 §0929DRILL 那条
#   纪律说的形态：改一处、漏两处，下一轮又冒出第四处。更要紧的是它已经进了 git 历史，
#   **改文件洗不掉历史** ⇒ 这个主题按「已泄露」处理（轮换＝owner 的现网动作，不在本批自动执行面内）。
#   本批把「值」从仓库里彻底拿掉，换成外置来源；仓库里只留取用口径。
#
# 取用优先级（与 restic 仓库密码、kuma admin 口令同一条通道＝钥匙串）：
#   1) 环境变量 NTFY_TOPIC —— 手工调试/临时改道用，显式给出就按它；
#   2) macOS 钥匙串条目 quant-ntfy-topic（账号＝$USER）—— launchd 常驻形态的正式来源。
#   两者都取不到时**返回失败但不静默**：调用方必须把「没配主题」打在日志里（告警发不出去
#   是运维失明，属于"降级不得报成功"那一族，见 §N-6/§M2）。
#
# 契约（只有两个函数；任何文件想推 ntfy 都必须走这里，别在本地再拼一遍 security 命令）：
#   ntfy_topic_resolve        成功：stdout＝主题（无换行）、rc=0；取不到：stdout 空、rc=1
#   ntfy_topic_report [前缀]  只回显「len=长度 fp=sha256 前 8 位」，**任何时候不回显明文**
#
# English: single source of truth for the ntfy topic on the Mac side. The topic is a credential
# (anyone who knows it can publish to that channel), it used to be hard-coded in three files, and
# it is already in git history — so the value now lives outside the repo (env NTFY_TOPIC or the
# macOS keychain item quant-ntfy-topic) and this file is the only reader. Absence is reported
# loudly by the caller, never swallowed.
ntfy_topic_resolve() {
	local t=""
	if [ -n "${NTFY_TOPIC:-}" ]; then
		t="$NTFY_TOPIC"
	elif command -v security >/dev/null 2>&1; then
		# -w 只打印口令值；条目不存在时 rc 非 0，用 || true 收掉（本函数在 set -e 的调用方里也会被 source）
		t="$(security find-generic-password -a "${USER:-$(id -un)}" -s "${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic}" -w 2>/dev/null || true)"
	fi
	[ -n "$t" ] || return 1
	printf '%s' "$t"
	return 0
}

# ntfy_topic_report：把「有没有配、配的是哪一份」变成可读数的证据，同时守住不回显明文这条线。
# 只报长度与指纹前 8 位——与 §0929SECKEY 的 restic 口令离线托管、§QMT-TOKENROT 的四源指纹比对
# 同一个姿势：排查时要能判断"两处的值是不是同一个"，但不能因此把值本身送进日志/终端回滚缓冲。
ntfy_topic_report() {
	local t label fp
	label="${1:-ntfy}"
	t="$(ntfy_topic_resolve || true)"
	if [ -z "$t" ]; then
		echo "$label topic=ABSENT source=env:NTFY_TOPIC|keychain:${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic}"
		return 1
	fi
	if command -v shasum >/dev/null 2>&1; then
		fp="$(printf '%s' "$t" | shasum -a 256 2>/dev/null | cut -c1-8 || true)"
	else
		fp="$(printf '%s' "$t" | sha256sum 2>/dev/null | cut -c1-8 || true)"
	fi
	echo "$label topic_len=${#t} topic_fp=${fp:-unreadable}"
	return 0
}
