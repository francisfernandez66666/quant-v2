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
# 契约（四个函数；任何文件想推 ntfy 都必须走这里，别在本地再拼一遍 security 命令或 curl）：
#   ntfy_topic_resolve          成功：stdout＝主题（无换行）、rc=0；取不到：stdout 空、rc=1
#   ntfy_topic_report [前缀]    只回显「len=长度 fp=sha256 前 8 位」，**任何时候不回显明文**
#   ntfy_priority_normalize v   把 Priority 值收敛到 ntfy 认的词：合法值原样回显（rc=0），
#                               非法值回显 high 并 rc=1（调用方必须把原值打在日志里，见下）
#   ntfy_attempt …              单次发布的传输层动作＋失效分类（见函数头注的返回码语义）
#
# §NTFY-PRI（2026-10-15 批）为什么把 Priority 与"一次拨号"也搬进这个文件：
#   看门狗与拉取腿的 alert() 各自写了 `Priority: $pri`，默认值/传参都写的是 **medium**。
#   ntfy 的 Priority 头只认 min/low/default/high/urgent 与 1–5；`medium` 是英语里"中"的直觉词，
#   上游直接回 HTTP 400 {"code":40007,"error":"invalid priority parameter"}。后果不是"降级"而是
#   **这两条腿的每一条告警从装机那天起就 100% 发不出去**（10-10 首跑的失败被读成「ntfy.sh 间歇断连」，
#   10-12 批的对策是给必然 400 的请求加三次重试——同一条错误请求再发三遍，还把文案写成「网络？」，
#   于是"配置错误"被伪装成"网络抖动"整整五天，直到今天用零凭据探针逐个优先级拨上游才锤出来：
#   high/default/3/urgent → 200，medium → 400）。
#   收进同一个文件的理由与主题值同源同一条：同一纪律写在三处＝改一处漏两处（§0929DRILL 那一课），
#   白名单和分类只留一份实现，门禁才能用**派生取数**钉"两个发送方读的是同一套词表"。
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

# ntfy_priority_normalize：Priority 头的白名单闸（§NTFY-PRI）。
#   用法  pri="$(ntfy_priority_normalize "$raw")" || 调用方自己打一句 WARN 点名 $raw
#   合法值原样回显并退 0；非法值（含空串）回显 high 并退 1 ⇒ 调用方**既能把告警发出去**
#   （不再因为一个拼错的词把整条通知打死），又能在日志里看见"谁传了非法值"。
#   为什么兜到 high 而不是 default：本仓的 Mac 侧告警唯一的用途就是"没人看屏幕时叫醒人"，
#   分不出该降级的时候按高处理比按低处理更接近 owner 的真实期望（§N-6 降级不得报成功同族）。
ntfy_priority_normalize() {
	local v="${1:-}"
	case "$v" in
	min | low | default | high | urgent | 1 | 2 | 3 | 4 | 5)
		printf '%s' "$v"
		return 0
		;;
	esac
	printf 'high'
	return 1
}

# ntfy_attempt：一次 ntfy 发布 + **失效分类**。这是"拨号"动作的单实现，两个发送方都调它。
#   用法 ntfy_attempt <日志函数名> <完整 URL> <主题> <标题> <合法优先级> <标签> <正文> <归因前缀>
#   返回码语义（三条腿各走各的路，这正是本枚改造要分开的三件事）：
#     0 = **已送达**（上游回了 2xx/3xx；也包括"回了 200 之后连接被重置"那一形）
#     1 = **可重试**（拿不到状态码＝纯传输层失败，或上游 5xx）——网络抖动那一族才配重试
#     2 = **不可重试**（上游回 4xx）：请求本身不合格，再发三遍只是把同一条错误请求重放三遍；
#         本函数已把 http 码与响应体摘要交给调用方的日志函数打出去了，绝不静默折算成 1。
#   为什么 4xx 必须单列：10-12 那次给 medium 加重试，就是把配置错误当网络抖动治，
#   治完仍然 100% 发不出去，而日志里只留下一句「网络？」——下次真断网没人再信这条。
#   ★为什么"状态码优先于 curl 退出码"（10-15 实测把这条逼出来的）：
#     零凭据只读预演（拿无效 priority 打公开 topic）实测到的是 `curl_rc=56`（接收失败）＋
#     `http=400`＋**响应体拿不到**——上游把状态行发完就断了连接。也就是说：
#       · curl 非 0 不代表没送达，状态码才代表；
#       · 响应体会缺，日志的"响应="那一栏必须能读成"无body"而不是把整条读数带崩。
#     ⇒ 若只按 curlrc 判成功，"200 后被重置"会被当成失败重试，重复告警轰炸手机；
#       若只按 curlrc 判失败而不看状态码，400 会被归进可重试那一支——正是这次要治的形。
#   桩安全：门禁的 curl 桩不回显状态码（http 为空串）也不报错（rc=0），落到最后那一支
#     ⇒ 仍按 curlrc 判定：rc=0 记 0、rc≠0 记 1（可重试），既有 WD2「成功路径恰一次外呼」与
#     WD8「失败路径三次」的读数口径一字不动。
ntfy_attempt() {
	local logfn="$1" url="$2" topic="$3" title="$4" pri="$5" tags="$6" body="$7" src="$8"
	local http="" curlrc=0 bodyfile="" snippet=""
	# 响应体落临时文件只为了 4xx 时把上游的 error 文案带出来（否则日志里只有"发送失败"四个字，
	# 分不出是优先级词写错还是主题写错）。mktemp 失败就不带响应体，不因 diagnostics 把告警本身挡掉。
	bodyfile="$(mktemp 2>/dev/null || true)"
	http="$(curl -fs -o "${bodyfile:-/dev/null}" -w '%{http_code}' -H "Title: $title" -H "Priority: $pri" \
		-H "Tags: $tags" -d "$body" "$url/$topic" 2>/dev/null)" && curlrc=0 || curlrc=$?
	case "$http" in
	2[0-9][0-9] | 3[0-9][0-9])
		# 送达（含"200 之后被重置"：curlrc 只说明响应体没读全，帖子已经进了主题）。
		if [ -n "$bodyfile" ]; then rm -f "$bodyfile"; fi
		return 0
		;;
	4[0-9][0-9])
		# 一律 if 形，不写 `[ -n X ] && cmd`：条件为假时整句返回 1，而本文件会被 set -e 的调用方 source。
		if [ -n "$bodyfile" ] && [ -s "$bodyfile" ]; then
			snippet="$(head -c 200 "$bodyfile" 2>/dev/null | tr -d '\r\n' | tr -d '"\\')"
		fi
		"$logfn" "ntfy 发送失败 http=${http} curl_rc=${curlrc} src=${src} 响应=${snippet:-无body}（4xx＝请求本身不合格，不重试；Priority 只认 min/low/default/high/urgent 与 1-5，主题与 URL 见启动行指纹）"
		if [ -n "$bodyfile" ]; then rm -f "$bodyfile"; fi
		return 2
		;;
	5[0-9][0-9])
		if [ -n "$bodyfile" ]; then rm -f "$bodyfile"; fi
		return 1
		;;
	esac
	# 走到这里＝没拿到可判定的状态码（真断网、DNS 失败、或门禁桩）。按 curl 退出码定成败。
	if [ -n "$bodyfile" ]; then rm -f "$bodyfile"; fi
	if [ "$curlrc" -eq 0 ]; then
		return 0
	fi
	return 1
}
