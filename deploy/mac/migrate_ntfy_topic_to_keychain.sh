#!/bin/bash
# migrate_ntfy_topic_to_keychain.sh — 把 ntfy 告警主题从「仓库里的字面缺省值」迁进 macOS 钥匙串
# （§KUMA-SECREDTO 2026-10-07 修复批 波 3；本机操作，不碰生产）。
#
# 为什么需要这一步（不是卫生改动，是断链修复的前置）：
#   波 3 把 ntfy 主题的缺省值从 deploy/mac 的三个文件里拿掉了（kuma_seed.js /
#   restic_pull_backup.sh / verify_restore.sh 以前各抄同一份 32-hex）。拿掉之后，取值口径变成
#   「env NTFY_TOPIC > 钥匙串 quant-ntfy-topic」——**如果钥匙串里没有这一条，安装器会拒绝安装，
#   而已装上的拉取腿会把告警降成"只落日志"**（那正是 09-16~09-25 异地备份静默断更 10 天最难
#   发现的形态：失败发生了，喊不出声）。所以先迁值、再重装代理。
#
# 姿势沿用本仓既有家族（与 rotate_qmt_token.ps1 / harden_snapshot_acl.ps1 同一条纪律）：
#   缺省只预览，动手必须显式 -Apply；**任何时候不回显明文**，只报长度与 sha256 前 8 位指纹；
#   钥匙串里已经有值时不覆盖（要覆盖必须再给 -Force），并且把两侧指纹都打出来让人先对齐。
#
# 用法：
#   ./migrate_ntfy_topic_to_keychain.sh                      # 预览：报告现网状态 + 将从历史取到的值的长度/指纹
#   ./migrate_ntfy_topic_to_keychain.sh -Apply               # 写入钥匙串 quant-ntfy-topic（已存在则拒绝）
#   ./migrate_ntfy_topic_to_keychain.sh -Apply -Force        # 覆盖已有条目（轮换主题时用）
#   printf '%s' '<新主题>' | ./migrate_ntfy_topic_to_keychain.sh --from-stdin -Apply
#       ↑ 轮换后的新主题走 stdin，绝不进 argv（argv 会落进 shell 历史与 ps 输出，等于二次泄露）
#
# 关于「已入库的那份旧值」：它在 git 历史里，改文件洗不掉 ⇒ 按**已泄露**处理，
# 真正的安全动作是 owner 择窗轮换一个新主题（然后 --from-stdin 写入、重跑 kuma_seed.js 与代理安装，
# 并在 ntfy 侧确认新主题能收到一条测试推送）。本脚本只负责把旧值搬进钥匙串、把链路先接活，
# 它不代替轮换，也不主张"搬进钥匙串就安全了"。
#
# English: moves the ntfy alert topic out of tracked files into the macOS keychain. Dry-run by
# default, -Apply to write, -Force to overwrite. Never prints the secret — length plus sha256 prefix
# only. The value that was committed stays in git history, so it must be treated as leaked: rotation
# is a separate owner action, and new topics must be fed over stdin so they never reach argv.
set -uo pipefail

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
# 复用同一个取用口径，避免"迁移器自己实现一遍读钥匙串"＝第四份实现（§0929DRILL 判据函数单实现纪律）
# shellcheck source=ntfy_topic.sh
. "$SELF_DIR/ntfy_topic.sh"

APPLY=0
FORCE=0
FROM_STDIN=0
for a in "$@"; do
	case "$a" in
		-Apply) APPLY=1 ;;
		-Force) FORCE=1 ;;
		--from-stdin) FROM_STDIN=1 ;;
		-h | --help) sed -n '1,40p' "$0"; exit 0 ;;
		*) echo "未知参数：${a}（只认 -Apply / -Force / --from-stdin / --help）" >&2; exit 1 ;;
	esac
done

ITEM="${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic}"
OWNER="${USER:-$(id -un)}"

fp8() { # 只回显 sha256 前 8 位；两个调用方（新值/旧值）共用这一份实现
	local v="$1"
	[ -n "$v" ] || { printf 'empty'; return 0; }
	if command -v shasum >/dev/null 2>&1; then printf '%s' "$v" | shasum -a 256 | cut -c1-8;
	else printf '%s' "$v" | sha256sum | cut -c1-8; fi
}

# redact_32：把外部命令的 stderr 摘要里疑似凭据的十六进制串遮掉再打印。
# 为什么要有它：`security` 报错文案里可能带上服务名/参数回显，而本脚本的参数里就含条目名；
# 一旦哪天把值也放进 argv（正是本次改掉的那个写法），错误摘要就会把凭据打进日志。
# 遮而不是不打印：报错原因（exit code、权限、条目冲突）对排查是必需的，全吞掉就等于
# "失败了但没人知道为什么"（§N-6 降级不得报成功族里"反向的那半"：不得报失败而无痕）。
redact_32() {
	local s="${1:-}"
	s="$(printf '%s' "$s" | tr -d '\r\n' | sed -E 's/[0-9a-fA-F]{24,}/<REDACTED>/g')"
	printf '%s' "${s:-<empty>}"
}

# 1) 现网状态：钥匙串里有没有、env 里有没有（只报指纹，绝不报值）
OLD_KC="$(security find-generic-password -a "$OWNER" -s "$ITEM" -w 2>/dev/null || true)"
# 1b) 条目**在位性**必须用元数据查（find 不带 -w），不能拿"读回来的值非空"当存在性判据。
#     2026-10-10 真机实测：坏写入会在钥匙串里留下一条 svce/acct 都对、口令字段为空、
#     `find -w` 照样退 0 的条目（见下面 §KUMA-SECREDTO-W2 的那次读数）。若按"值非空＝存在"选分支，
#     本脚本会对这种残骸走**不带 -U** 的 add，而真机那条退 45 already exists
#     （"写入钥匙串条目失败"——报的是失败，但没人看得出来是残骸挡路，只能上机再看一遍）。
ITEM_LIVE=0
security find-generic-password -a "$OWNER" -s "$ITEM" >/dev/null 2>&1 && ITEM_LIVE=1
echo "==> 目标钥匙串条目 : ${ITEM}（账号 ${OWNER}）"
if [ -n "$OLD_KC" ]; then
	echo "    现网状态 : 已存在 len=${#OLD_KC} fp=$(fp8 "$OLD_KC")"
elif [ "$ITEM_LIVE" = "1" ]; then
	echo "    现网状态 : 条目在位但口令为空（上一次坏写入的残骸）——这次会带 -U 覆写它"
else
	echo "    现网状态 : 不存在"
fi
echo "    env 腿   : $(ntfy_topic_report resolve-via-lib)"

# 2) 待写入的值从哪来
NEW=""
SRC=""
if [ "$FROM_STDIN" = "1" ]; then
	# stdin 整行读入并剥首尾空白/CR：从别处粘贴主题时最常带的就是换行与 \r，
	# 带脏值的 ntfy URL 会 404，而现象与"网络坏了"一模一样（§CRLF 同族）。
	NEW="$(head -n 1 | tr -d '\r\n' || true)"
	SRC="stdin"
else
	# 缺省来源＝git 历史里的仓库旧值。取法刻意"窄"：只看这三个已知文件的 blob，
	# 抓第一处 32-hex；扫全历史容易被无关十六进制串命中，抓到错的值会把告警发到别处。
	for p in deploy/mac/restic_pull_backup.sh deploy/mac/verify_restore.sh deploy/mac/kuma_seed.js; do
		for rev in HEAD HEAD~1 HEAD~2; do
			cand="$(git -C "$SELF_DIR/.." show "$rev:$p" 2>/dev/null \
				| grep -oE '[0-9a-f]{32}' | head -n 1 || true)"
			if [ -n "$cand" ]; then NEW="$cand"; SRC="git:$rev:$p"; break 2; fi
		done
	done
fi

if [ -z "$NEW" ]; then
	echo "X 没取到可写入的主题（git 历史里那三个文件都没搜到 32-hex，或本仓不是 git 工作树）。" >&2
	echo "  这种情况下请显式用 stdin 提供：printf '%s' '<主题>' | $0 --from-stdin -Apply" >&2
	exit 1
fi
echo "==> 待写入         : len=${#NEW} fp=$(fp8 "$NEW") 来源=$SRC"

# 3) 覆盖判定（-Force 才允许换掉已在位的值；两侧指纹先打出来，让人自己确认"是不是同一份"）
if [ -n "$OLD_KC" ] && [ "$(fp8 "$OLD_KC")" = "$(fp8 "$NEW")" ]; then
	echo "ok - 钥匙串里已是这份值，无需改动（同指纹）"
	exit 0
fi
if [ -n "$OLD_KC" ] && [ "$FORCE" != "1" ]; then
	echo "X 钥匙串里已有**不同**的一份（old_fp=$(fp8 "$OLD_KC") new_fp=$(fp8 "$NEW")）——" >&2
	echo "  换掉它等于把告警改道，必须再加 -Force 并确认这次改道是有意的（轮换主题正是这种场合）" >&2
	exit 1
fi

if [ "$APPLY" != "1" ]; then
	echo
	echo "预览模式：钥匙串一个字节没写。动手请加 -Apply${OLD_KC:+（已有条目，还需 -Force）}。"
	echo "MIGRATE_NTFY_PLAN"
	exit 0
fi

# 4) 写入。add 与 -U（更新）分开走，避免"条目存在时 add 报错却被当成写成功"的半态；
#    走哪条按**条目在位性**（上面 1b 的 ITEM_LIVE）判，不按"值非空"判——残骸那条就是栽在这个区别上的。
#
# §KUMA-SECREDTO-W（本批补的一课，写在这里而不是藏在代码里）：**值绝不进 argv**。
#   旧写法是 `security add-generic-password ... -w "$NEW"`，看着无害，实际把主题交给了子进程的
#   命令行参数——`ps -o args` 同机器上任何进程都读得到，等于把凭据从 git 历史搬到运行期明文里
#   （和 --from-stdin 的设计意图自相矛盾：注释写着"绝不进 argv"，代码却正是 argv）。
#
# §KUMA-SECREDTO-W2（2026-10-10 真拨逼出的第二条，顺序就是发现顺序，别倒过来读）：
#   上面那句"改 `-w` 不带值＋管道喂 stdin"在**真机上不成立**。当时写下的是"本机没有真拨过，
#   风险由第 5 步读回兜住"，今天 owner 授权真拨，兜底当场生效：
#     待写入 len=32 fp=6c545a51 → 写入后读回 got=empty，rc 却是 0。
#   钥匙条目确实被建出来了（`security find-generic-password` 不带 -w 能看到 cdat＝那一刻、
#   svce/acct 都对），只是**口令字段为空**。原因写在它自己的 usage 里：
#   "Use of the -p or -w options is insecure. Specify -w as the last option to be prompted."
#   ⇒ 无值时它 prompt 的是**终端**，不是 stdin；管道里那 32 个字符被整串丢弃，条目照样落。
#   这一族的教训不是"参数写错了"，而是**门禁的桩按我以为的语义实现**：§110 F3-5 那个 fake
#   security 里"`-w` 不带值＝从 stdin 读"是我编的，于是 113 段全绿里这条坏通道活得好好的，
#   而它对外的主张是"写入钥匙串这条只在本地，没真拨"（§MAC-DRIFT 同一课的第二次：绿的读数
#   不等于真实现，桩的语义必须由真机读数背书）。
#   修法＝换一条**已真拨验证过**的通道：`security -i`（交互模式，命令行从 stdin 给）。
#   三条实测（探针值各就位后逐条复核，不是推测）：
#     ① 值精确落位——含空格与 `!` 的探针值原样读回；
#     ② 内部命令的退出码会透传（unknown command 退 1、坏参数退 2）⇒ "命令没报错"这条主张从此有意义；
#     ③ 出错时 usage 只打到 stderr 且**不回显参数**（对探针值 grep 命中 0）⇒ 错误摘要不会把值带进日志。
#   值仍在 stdin，不进 argv，所以 §KUMA-SECREDTO-W 那条主张保持不变、只是换了一条真的路。
SEC_ERR="$(mktemp)"
# 交互通道按双引号包值，值里带双引号或反斜杠会被解析器吃掉半截——宁可拒写也不存一个"看起来成功了"
# 的错值（错主题的告警会发到没人看的地方，比空主题更难发现：空主题至少有 ALERT-NOT-SENT 喊出来）。
case "$NEW" in
	*'"'* | *'\'*)
		echo "X 待写入的主题含双引号或反斜杠，交互通道会把它截错（拒写；ntfy 主题本身也不该有这些字符）" >&2
		rm -f "$SEC_ERR"; exit 1 ;;
esac
# write_kc <0|1>：$1=1 走 -U（更新已在位条目）。写口令这一条只留一个入口，两处调用共用口径。
# 为什么不用数组传参：macOS 自带 bash 3.2 在 `set -u` 下展开**空数组** "${extra[@]}" 会报
# unbound variable（3.2 的老 bug），而本脚本正是 set -u；分支写白比玩数组安全，也更好读。
write_kc() {
	local upd="$1" line
	if [ "$upd" = "1" ]; then
		line="add-generic-password -U -a \"${OWNER}\" -s \"${ITEM}\" -w \"${NEW}\""
	else
		line="add-generic-password -a \"${OWNER}\" -s \"${ITEM}\" -w \"${NEW}\""
	fi
	printf '%s\n' "$line" | security -i >/dev/null 2>"$SEC_ERR"
}
if [ "$ITEM_LIVE" = "1" ]; then
	write_kc 1 || {
		echo "X 更新钥匙串条目失败（security -i 交互通道把内部命令的退出码透传回来，非 0＝真没写成）" >&2
		echo "  security stderr 摘要：$(redact_32 "$(head -c 200 "$SEC_ERR" 2>/dev/null || true)")" >&2
		rm -f "$SEC_ERR"; exit 1; }
else
	write_kc 0 || {
		echo "X 写入钥匙串条目失败（security -i 交互通道把内部命令的退出码透传回来，非 0＝真没写成）" >&2
		echo "  security stderr 摘要：$(redact_32 "$(head -c 200 "$SEC_ERR" 2>/dev/null || true)")" >&2
		rm -f "$SEC_ERR"; exit 1; }
fi
rm -f "$SEC_ERR"

# 5) 写后真拨一次：读回来比指纹，而不是相信"命令没报错"。
#    （§0926ROT/§RESTORE 的同一课：装入口必须真拨一次，写侧自证一致 ≠ 消费者能读到。）
#    这一条在本批**真把一次假成功拦下来了**（见上面 §KUMA-SECREDTO-W2 的读数），所以它不是兜底装饰：
#    凡是"命令 rc=0 但值可能没进去"的写入，判据都放在读回那一侧，而不是放在写入侧的退出码。
VERIFY="$(security find-generic-password -a "$OWNER" -s "$ITEM" -w 2>/dev/null || true)"
if [ "$(fp8 "$VERIFY")" != "$(fp8 "$NEW")" ]; then
	echo "X 写入后读回指纹不一致（want=$(fp8 "$NEW") got=$(fp8 "$VERIFY")）——按失败处理，别继续装代理" >&2
	if [ -z "$VERIFY" ]; then
		echo "  got=empty 的已知成因：条目建出来了但口令字段是空的（值没进写入口，例如走「-w 不带值」那条读终端的路）。" >&2
		echo "  清掉空条目再重跑：security delete-generic-password -a \"\$USER\" -s ${ITEM}" >&2
	fi
	exit 1
fi
echo "ok - 已写入 ${ITEM}：len=${#VERIFY} fp=$(fp8 "$VERIFY")（读回一致）"
echo "下一步：重跑 deploy/mac/install_mac_backup_agent.sh -Apply（拉取腿）与 install_mac_drill_agent.sh -Apply（演练腿），"
echo "        并按 RUNBOOK 用新的两份参数重跑 kuma_seed.js；轮换主题时还要确认旧主题的订阅全部改掉。"
echo "MIGRATE_NTFY_DONE"
