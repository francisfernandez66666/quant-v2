// sizing_global_migration.go — §DYN-GLOBAL（2026-10-24 owner 裁决一「dynamic 全局，legacy 开关
// 账户级别」）的一次性配置迁移。
//
// 要解决的问题：裁决一把出厂默认抬成下发态（enabled=true / budget_mode=dynamic / shadow=false），
// 但**存量载荷里躺着显式的旧安全值**。旧 `Enabled bool` 无 omitempty，任何被面板物化过的账号
// 都带 `"enabled":false`；`budget_mode` 也常被写成 `"legacy"`。只改代码默认值的话，这些账号
// 会静默留在关闭态——正是本项目反复吃亏的"以为开了其实没开"。
//
// 迁移动哪几处（按 effective 读取优先级 sizingBaseRaw 逐层对齐，不越过读侧自己发明顺序）：
//  1. 全局 `rules.sizing`（config.json）；
//  2. 账号专用键 `quant_config_sizing_v1`；
//  3. 账号**自己**的 `rules.sizing`（存于 `quant_config_json_v1` 内）——只把修正后的三键写进
//     专用键，不改写整份账号规则文档（面板首次保存本来就会物化专用键，这是既有 §A0/R2 语义）。
//
// 只迁"从未被碰过"的形状：enabled 显式 false 且 budget_mode 空/legacy 且 shadow 缺省/true 且
// 没有 policy_version 标记。三键任一不满足就说明有人**显式表过态**（例如 shadow=false 想跑灰度、
// 或 enabled=false 是裁决之后自己关的），一律不动，只登记成 hold 让人看一眼。
// 迁完写 policy_version=1，重启不重跑；之后账号再改回 legacy 也永远不会被再次抬走。
//
// 不动的一层：历史版本把账号级配置写到 userID="" 的"系统级键"下（读侧至今保留该回退）。
// 整份文档重写属于另一件事，本迁移只把它**点名报出来**，不静默改写、也不假装已归一。
//
// English: one-time §DYN-GLOBAL migration. Existing payloads carry the *old* safe defaults
// explicitly ("enabled":false, "budget_mode":"legacy"), so flipping the factory default alone would
// silently leave those accounts undelivered. This rewrites only the triple when it is byte-for-byte
// the untouched pre-ruling shape, stamps policy_version=1 so it never runs twice, lists everything
// else as a hold instead of guessing, and only names the legacy system-level ("") document.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// sizingPolicyVersion §DYN-GLOBAL 迁移标记值。写进 sizing 的 policy_version 字段，
// 语义是"该载荷已按裁决一归一过一次"。
// English: the §DYN-GLOBAL marker meaning "this payload was normalized once".
const sizingPolicyVersion = 1

// SizingMigrationHold 一条"看见但没动"的读数（含账号、层级与原因）。
// English: one inspected-but-untouched payload, with the layer and the reason.
type SizingMigrationHold struct {
	UserID string `json:"user_id"`
	Layer  string `json:"layer"`
	Reason string `json:"reason"`
}

// SizingMigrationFailed 一条写入失败（迁移失败必须出声，不能折进"没迁但也没坏"）。
// English: a write failure during migration — never folded into "skipped".
type SizingMigrationFailed struct {
	UserID string `json:"user_id"`
	Layer  string `json:"layer"`
	Err    string `json:"err"`
}

// SizingMigrationReport 迁移总账，供启动日志与部署后 verify 探针消费。
// 每个计数都必须能在日志里找到对应行——"扫过 N 个"这种对不上账的读数本项目不收。
// Global 取值：migrated / held / absent / failed / written-but-readback-mismatch。
// English: the migration ledger. Every count has a matching log line; Global is one of
// migrated/held/absent/failed/written-but-readback-mismatch.
type SizingMigrationReport struct {
	Scanned   int                     `json:"scanned"`
	Migrated  int                     `json:"migrated"`
	Global    string                  `json:"global"`
	Holds     []SizingMigrationHold   `json:"holds,omitempty"`
	Failures  []SizingMigrationFailed `json:"failures,omitempty"`
	Followers []string                `json:"followers,omitempty"`
}

// sizingUntouchedLegacyShape 判断一个 sizing 值是否**逐字等于**裁决一之前的安全默认。
// 返回 (true, "") 表示可以迁；返回 (false, reason) 表示有人显式表过态，不许猜。
// English: true only when the value is byte-for-byte the pre-ruling safe shape; otherwise the
// reason names which key says "this was touched".
func sizingUntouchedLegacyShape(sc SizingConfig) (bool, string) {
	if sc.PolicyVersion >= sizingPolicyVersion {
		return false, fmt.Sprintf("已带迁移标记 policy_version=%d", sc.PolicyVersion)
	}
	if sc.Enabled == nil {
		return false, "enabled 键缺失（本就跟随新默认，无需迁移）"
	}
	if *sc.Enabled {
		return false, "enabled 已显式为 true"
	}
	if v := strings.TrimSpace(sc.BudgetMode); v != "" && !strings.EqualFold(v, "legacy") {
		return false, fmt.Sprintf("budget_mode=%q 不是认得的旧写法", sc.BudgetMode)
	}
	if sc.Shadow != nil && !*sc.Shadow {
		return false, "shadow 已显式为 false（灰度/下发态是有人自己拨过的）"
	}
	return true, ""
}

// sizingAfterMigration 用出厂三键覆盖旧安全形状，其余因子逐字保留。
// English: overwrite the three delivery keys with the factory values, keep every other factor.
func sizingAfterMigration(sc SizingConfig) SizingConfig {
	d := DefaultSizingConfig()
	sc.Enabled = sizingBool(true)
	sc.BudgetMode = d.BudgetMode
	sc.Shadow = sizingBool(false)
	sc.PolicyVersion = sizingPolicyVersion
	return sc
}

// MigrateSizingToGlobalDefault §DYN-GLOBAL 一次性迁移入口。
//
// 调用纪律：**必须早于任何 sizing 消费者装配**（引擎/模拟盘/实盘控制器都读 GetSizingConfigFor），
// 且只在进程启动时调用一次。它不是热路径、也不做后台轮询——静默改写资金参数只允许发生在
// 启动这一次，并且整轮读数全部进日志。
//
// 失败项不影响进程启动（旧值仍在用，行为等于没迁），但必须出现在 Failures 与日志里。
// English: the one-time entry point. Must run before any sizing consumer is assembled, once per
// process. Failures leave the old value in effect and are reported.
func (m *Manager) MigrateSizingToGlobalDefault(userIDs []string) SizingMigrationReport {
	rep := SizingMigrationReport{Global: "absent"}
	if m == nil {
		return rep
	}

	// ① 全局 rules.sizing（config.json）。
	m.mu.RLock()
	globalRaw := m.rules.Sizing
	m.mu.RUnlock()
	switch ok, reason := sizingUntouchedLegacyShape(globalRaw); {
	case ok:
		next := sizingAfterMigration(globalRaw)
		m.mu.Lock()
		m.rules.Sizing = next
		m.mu.Unlock()
		if err := m.Save(); err != nil {
			rep.Global = "failed"
			rep.Failures = append(rep.Failures, SizingMigrationFailed{Layer: "global", Err: err.Error()})
		} else {
			// 写后复读自证（§0926E2E-W1B 口径）：落盘值必须真是迁移后的值。
			// GetSizingConfig 走 WithDefaults，所以读的是"引擎下次会吃到的那一份"。
			reread := m.GetSizingConfig()
			if !reread.EnabledOn() || reread.BudgetModeOf() != "dynamic" || reread.ShadowOn() {
				rep.Global = "written-but-readback-mismatch"
				rep.Failures = append(rep.Failures, SizingMigrationFailed{Layer: "global",
					Err: fmt.Sprintf("复读 enabled=%v budget_mode=%q shadow=%v",
						reread.EnabledOn(), reread.BudgetModeOf(), reread.ShadowOn())})
			} else {
				rep.Global = "migrated"
				rep.Migrated++
				log.Printf("[config] §DYN-GLOBAL 迁移 层=全局 rules.sizing：enabled=false→true、budget_mode=%q→dynamic、shadow→false、policy_version→%d（其余因子原样）",
					globalRaw.BudgetMode, sizingPolicyVersion)
			}
		}
	case globalRaw.Enabled != nil || globalRaw.PolicyVersion > 0:
		rep.Global = "held"
		rep.Holds = append(rep.Holds, SizingMigrationHold{Layer: "global", Reason: reason})
		log.Printf("[config] §DYN-GLOBAL 全局 rules.sizing 未迁移：%s（现值 enabled=%v budget_mode=%q shadow=%v）",
			reason, globalRaw.EnabledOn(), globalRaw.BudgetMode, globalRaw.ShadowOn())
	}

	// ② 逐账号。KV 存储缺席（单机文件配置）时只会走 ②-c 分支。
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		rep.Scanned++
		// ②-a 专用键（最高优先级，存在即只认它）。
		if raw, exists := m.AccountKVRaw(uid, perUserSizingKey); exists {
			var sc SizingConfig
			if err := json.Unmarshal(raw, &sc); err != nil {
				rep.Failures = append(rep.Failures, SizingMigrationFailed{UserID: uid, Layer: "dedicated",
					Err: fmt.Sprintf("专用键解析失败: %v", err)})
				log.Printf("[config] §DYN-GLOBAL 账号 %s 专用 sizing 键解析失败，跳过迁移（读侧会回退继承值）: %v", uid, err)
				continue
			}
			keep, reason := sizingUntouchedLegacyShape(sc)
			if !keep {
				if sc.Enabled != nil || sc.PolicyVersion > 0 {
					rep.Holds = append(rep.Holds, SizingMigrationHold{UserID: uid, Layer: "dedicated", Reason: reason})
					log.Printf("[config] §DYN-GLOBAL 账号 %s 专用键未迁移：%s", uid, reason)
				} else {
					rep.Followers = append(rep.Followers, uid)
				}
				continue
			}
			if err := m.writeMigratedSizing(uid, sc); err != nil {
				rep.Failures = append(rep.Failures, SizingMigrationFailed{UserID: uid, Layer: "dedicated", Err: err.Error()})
				log.Printf("[config] §DYN-GLOBAL 账号 %s 专用键迁移失败（保持旧值）: %v", uid, err)
				continue
			}
			rep.Migrated++
			log.Printf("[config] §DYN-GLOBAL 迁移 账号=%s 层=专用键：enabled=false→true、budget_mode=%q→dynamic、shadow→false、policy_version→%d（其余因子原样）",
				uid, sc.BudgetMode, sizingPolicyVersion)
			continue
		}
		// ②-b 账号**自己**的旧 rules.sizing：把修正后的三键物化进专用键。
		if own, hasOwn := m.storedUserRules(uid); hasOwn {
			keep, reason := sizingUntouchedLegacyShape(own.Sizing)
			if !keep {
				if own.Sizing.Enabled != nil || own.Sizing.PolicyVersion > 0 {
					rep.Holds = append(rep.Holds, SizingMigrationHold{UserID: uid, Layer: "account_rules", Reason: reason})
					log.Printf("[config] §DYN-GLOBAL 账号 %s rules.sizing 未迁移：%s", uid, reason)
				} else {
					rep.Followers = append(rep.Followers, uid)
				}
				continue
			}
			if err := m.writeMigratedSizing(uid, own.Sizing); err != nil {
				rep.Failures = append(rep.Failures, SizingMigrationFailed{UserID: uid, Layer: "account_rules", Err: err.Error()})
				log.Printf("[config] §DYN-GLOBAL 账号 %s 物化专用键失败（保持旧值）: %v", uid, err)
				continue
			}
			rep.Migrated++
			log.Printf("[config] §DYN-GLOBAL 迁移 账号=%s 层=账号旧rules.sizing→专用键物化：enabled=false→true、budget_mode=%q→dynamic、shadow→false、policy_version→%d（该账号自此独立于全局 sizing，属 §A0 既有语义）",
				uid, own.Sizing.BudgetMode, sizingPolicyVersion)
			continue
		}
		// ②-c 无账号级文档：读侧自然落到全局，全局已归一 ⇒ 自动跟随，不写任何键。
		rep.Followers = append(rep.Followers, uid)
	}

	// ③ 系统级（userID=""）历史回退文档：只点名，不整份重写。
	if sys, hasSys := m.storedUserRules(""); hasSys && sys.Sizing.Enabled != nil && !*sys.Sizing.Enabled {
		rep.Holds = append(rep.Holds, SizingMigrationHold{UserID: "", Layer: "system_rules",
			Reason: "系统级键 rules.sizing 仍是 enabled=false，读侧会把它当作账号继承源（本迁移不整份重写该文档）"})
		log.Printf("[config] §DYN-GLOBAL 挂起 层=系统级键(userID=%q)：rules.sizing enabled=false budget_mode=%q shadow=%v —— 需在账号面板显式写一次 sizing，或直接改该文档；本次未自动改写",
			"", sys.Sizing.BudgetMode, sys.Sizing.ShadowOn())
	}

	log.Printf("[config] §DYN-GLOBAL 迁移总账 全局=%s 扫描账号=%d 迁移=%d 挂起=%d 自动跟随=%d 失败=%d",
		rep.Global, rep.Scanned, rep.Migrated, len(rep.Holds), len(rep.Followers), len(rep.Failures))
	return rep
}

// writeMigratedSizing 把归一后的 sizing 写入账号专用键，带写后复读自证（§0926E2E-W1B 口径）。
// English: write the normalized sizing into the account's dedicated key with read-back verification.
func (m *Manager) writeMigratedSizing(userID string, base SizingConfig) error {
	data, err := json.Marshal(sizingAfterMigration(base))
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	m.mu.Lock()
	err = m.setConfigVerifiedLocked(userID, perUserSizingKey, data)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// 复读之后必须确认**读侧真的吃到**（§RestartBeforeVerify 同族：落盘对了但读链没到＝白迁）。
	got, _ := m.GetSizingConfigFor(userID)
	if !got.EnabledOn() || got.BudgetModeOf() != "dynamic" || got.ShadowOn() {
		return fmt.Errorf("迁移后读侧仍未进入下发态：enabled=%v budget_mode=%q shadow=%v",
			got.EnabledOn(), got.BudgetModeOf(), got.ShadowOn())
	}
	return nil
}

// LogSizingBudgetDrift 对一批账号做一次脏值体检，把"配置写错却静默按 legacy 处理"的账号点名。
// 只在启动时调用（与迁移同一时刻），不进热路径。
// English: one-shot startup check naming accounts whose budget_mode is neither a recognized
// spelling nor empty — such a value is treated as legacy (conservative) but is a typo, not a choice.
func (m *Manager) LogSizingBudgetDrift(userIDs []string) {
	if m == nil {
		return
	}
	for _, uid := range append([]string{""}, userIDs...) {
		sc, _ := m.GetSizingConfigFor(uid)
		drift := sc.BudgetModeDrift()
		if drift == "" {
			continue
		}
		log.Printf("[config] §DYN-GLOBAL 脏 budget_mode 报警 账号=%q 原值=%q → 按 legacy 保守处理（旧每日闸仍在生效，请改成 dynamic 或 legacy 的正字写法）",
			uid, drift)
	}
}

// SizingDeliveryRead 是三把下发键的一次读数，字段名与日志/告警文案一一对应。
// English: one reading of the three delivery keys.
type SizingDeliveryRead struct {
	Enabled    bool   `json:"enabled"`
	Dynamic    bool   `json:"dynamic"`
	Shadow     bool   `json:"shadow"`
	Delivers   bool   `json:"delivers"`
	BudgetMode string `json:"budget_mode"`
}

// SizingDelivery 返回本账号的三键读数与"是否真下发"。
// English: the three-key reading plus whether the scale actually reaches orders.
func (s SizingConfig) SizingDelivery() SizingDeliveryRead {
	delivers := s.EnabledOn() && !s.ShadowOn()
	return SizingDeliveryRead{Enabled: s.EnabledOn(), Dynamic: s.DynamicBudgetOn(),
		Shadow: s.ShadowOn(), Delivers: delivers, BudgetMode: s.BudgetModeOf()}
}

// SizingBudgetWithoutScale 报告裁决一明令不许静默存在的那个坏组合：预算口径已是 dynamic
// （旧的每日笔数/预算/战法额度/最大持仓闸全部让位），但缩放没下发（总开关关或影子开）——
// 等于**没有任何预算决策者**。模拟盘侧据此每日报警一次。
// English: the combination ruling 1 forbids to sit silent — budget pipeline is dynamic (legacy
// daily gates retired) while the scale is not delivered (switch off or shadow on), i.e. no budget
// authority at all. The paper engine warns once a day on this.
func (s SizingConfig) SizingBudgetWithoutScale() bool {
	return s.DynamicBudgetOn() && !(s.EnabledOn() && !s.ShadowOn())
}
