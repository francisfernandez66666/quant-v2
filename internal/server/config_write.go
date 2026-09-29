// config_write.go — 配置写通道的"写前快照 + 写后字段级审计"复用件（§0929CFG-HIST，09-29 全量审计批）。
//
// 背景（缺陷报告的原话级证据）：`config.SnapshotRules` 在全仓**曾经只有一个生产调用点**（实盘配置那条
// 通道），而 history.go 文件头声明的是"每次保存/热更前快照"。其余配置写通道——战法参数、LLM、调度开关、
// 多空开关、模拟盘参数与战法白名单——改坏了既没有写前快照（回滚回不到这次变更），也没有字段级 diff
// 审计（说不出谁在什么时候改了哪个字段）。这正是 §ROBUST 一路在消灭的"声明与实现不符"型缝。
//
// 本文件把这些通道剩下的重复动作收成一个方法：调用方拿一次"写前痕迹"（快照名 + 落的是哪本账 +
// 写前文档原文），落库成功后一行审计。为什么不做成"包一层的 withConfigWriteSnapshot(fn)"：
// 各通道在写前写后夹着各自的校验、400 分支、热同步与告警，函数式包装会把这些迫到回调里、
// 反而更难读；留两步式（取快照 / 记审计），中间怎么走由处理器自己决定。
//
// 关键约定：**快照失败不阻断保存**。磁盘/历史目录抖动时运维必须还能改配置（换 key、拉闸熔断都是
// 时效动作），但"这一笔没有快照"必须以 [P1] 日志 + opslog 审计行的形式留下证据——不可回滚的变更
// 至少要是**可发现**的不可回滚。
//
// English: shared pre-write snapshot + post-write field-level audit for the config write channels.
// Snapshot failure never blocks the save but is itself logged and audited, so an unrollbackable
// change remains at least discoverable.
package server

import (
	"log"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
)

// configWriteTrace 一次配置写入的"写前痕迹"：快照名、落账描述、写前文档原文，以及审计需要的
// 操作者/目标账号/配置面。由 snapshotConfigWrite 产出、由 audit 在落库成功后消费。
// 字段全部只在本包内使用，故意不做 getter——多一层访问器只会让人以为它可以跨包持有。
// English: the pre-write trace of one config write (snapshot name, ledger, prior bytes) plus the
// identity fields the post-write audit line needs.
type configWriteTrace struct {
	actor   string // 真实操作者（管理员代配时与 uid 不同，绝不能混用）
	uid     string // 本次写入落在哪个账号的作用域上
	surface string // 配置面名：d1 / strategy / llm / longshort / qmt / paper / scheduler
	name    string // 写前快照文件名（ts）；快照失败时为空串
	ledger  string // 落的是哪本账：global / account:rules / account:d1 / account:longshort
	before  []byte // 写前文档原文，供写后字段级 diff
	err     error  // 快照本身的失败原因；nil 表示这次变更可回滚
}

// snapshotConfigWrite 在配置落库**之前**取一次写前痕迹（快照 + 原文）。
// 顺序要求：必须在任何会改到配置内存态/磁盘态的动作之前调用，否则 before 与 after 同源、
// diff 恒为空，审计退化成"只证明调用过"。校验失败直接回 400 的分支提前 return 也没关系——
// 多一个未使用的快照文件只是历史目录里的一次空转，不会产生错误记录。
// English: capture the pre-write trace (snapshot + prior document bytes) before any mutation;
// calling it after the write would make the diff permanently empty.
func (s *Server) snapshotConfigWrite(actor, uid, surface string) configWriteTrace {
	t := configWriteTrace{actor: actor, uid: uid, surface: surface}
	// 单入口解析"这次写的是哪本账"并落快照；未知配置面在这里就报错，逼新增通道登记表面名，
	// 避免出现第三条"改了但无账"的野路（resolveWriteScope 的分组必须与 setter 实际落点同构）。
	name, ledger, err := config.SnapshotBeforeWrite(s.cfg, uid, surface)
	t.name, t.ledger, t.err = name, ledger, err
	if err != nil {
		// 不阻断：如实留 [P1] 告警 + 审计行，让"不可回滚的写入"在 opslog 里可查。
		log.Printf("[P1][config] %s 配置写前快照失败（本次变更不可回滚，落库照常执行）actor=%s uid=%s: %v",
			surface, actor, uid, err)
		opslog.Audit("config_snapshot_failed", actor, surface+"("+uid+")", err.Error())
	}
	// 写后 diff 的另一侧基线：与快照同一作用域解析，两侧同构才可比；取不到就留空字节，
	// AuditConfigWrite 会把这一笔如实写成 "unavailable"（残缺证据也比静默强）。
	before, _, _ := config.EffectiveConfigDoc(s.cfg, uid, surface)
	t.before = before
	return t
}

// audit 在落库**成功之后**把本次变更以字段级 diff 落到 opslog（经 config 层单入口）。
// 调用位置约定：放在持久化成功判定之后、任何"保存即生效"副作用（kill-switch 翻转、金额帽热同步）
// 之前或之后都可，但绝不能在失败分支里调用——§0926E2E-W1B 之后各通道都如实回 500，
// 未落盘的变更不留"已变更"记录，否则审计会变成假账。
// English: emit the field-level diff after a successful persist, through the config layer's
// single audit entry point; never call it on a failure branch.
func (t configWriteTrace) audit(s *Server) {
	if s == nil || s.cfg == nil {
		return
	}
	config.AuditConfigWrite(s.cfg, t.actor, t.uid, t.surface, t.ledger, t.before)
}

// detail 给通道自己的 opslog/日志行补一段"落到哪本账、快照叫什么"的可回溯描述。
// 快照失败时 snapshot 字段显式写 none，配合 err 让读者一眼看出这一笔不可回滚。
// English: a short "ledger=… snapshot=…" suffix for channel-specific audit lines; "none" marks
// the unrollbackable case explicitly instead of hiding it.
func (t configWriteTrace) detail() string {
	if t.err != nil {
		return "ledger=" + t.ledger + " snapshot=none(写前快照失败)"
	}
	return "ledger=" + t.ledger + " snapshot=" + t.name
}
