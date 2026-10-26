// sizing_v2_migration.go — §DYN-SIZE v4 一次性配置迁移（policy_version 1 → 2，方案 §1.7）。
//
// 要解决的问题：v4 把两个**被旧 DefaultSizingConfig 物化过**的键语义改了，出厂默认也随之改了：
//   - `sizing.mode`：product → power_mean（目标模型要求市场组/个股组可分离，惩罚不叠加）；
//   - `sizing.max_total_position_pct`：0.80（逐笔乘子）→ 1.00（目标仓位天花板）。
//
// 只改代码默认值不够：面板首次保存会把当时的默认**逐字写进**账号专用键，这些账号从此与出厂默认
// 脱钩——旧载荷里的 `"mode":"product"` 与 `0.8` 会永远压住新默认，正是 owner 报的"一笔打满"里
// 最隐蔽的一条（读数看起来一切正常，因为读侧确实读到了配置，只不过那是上一代的默认）。
//
// 迁移条件刻意写成"落盘值逐字等于旧物化默认"，而不是"归一化之后等于旧口径"：
//   - `mode` 只在**显式写着 product** 时改；键缺失本来就已经解析成新的 power_mean（写一次反而把
//     它物化死，日后改默认再也传不下去——`mode`/`max_total_position_pct` 被物化坑过一次）；
//   - `max_total_position_pct` 只在**显式写着 0.80**（旧默认）时抬到 1.00；owner 手调过的
//     0.60/0.70 一律不动（那是选择，不是默认）；
//   - 两者都不需要改的载荷**一个字节都不写**，只在报告里列成 unchanged（幂等：第二次跑还是 unchanged）。
//
// 签字门（T4-8/B8，owner 裁决）：**dry-run 未签字不许落盘**。
// `apply=false` 时只算不写，报告逐账号给出前后值与目标仓位上限位移；启动链默认就是 dry-run，
// 只有 owner 显式给 `apply=true` 才真写盘。迁完盖 policy_version=2，重启不再重跑；
// 之后账号自己把 mode 改回 product 也不会被再次抬走（与 v1 迁移同一个"只迁一次"纪律）。
//
// English: one-time v4 migration. It rewrites ONLY payloads whose stored bytes are literally the
// old materialized defaults (mode=product, cap=0.80), never an absent key (that already resolves to
// the new default, and writing it would pin today's default forever). The report is produced in
// dry-run by default; nothing is written until the owner signs it (apply=true). A policy_version=2
// stamp makes it run once, same discipline as the v1 migration.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// sizingPolicyVersionV4 是本次迁移的标记值：payload.PolicyVersion >= 该值就不再重跑。
const sizingPolicyVersionV4 = 2

// 旧一代被 DefaultSizingConfig 物化进账号键的两个默认值。写成常量是为了让"逐字等于旧默认"
// 这条判据只有一处定义——判据错了会把 owner 手调的值一起冲掉，那是资金参数的静默改写。
const (
	legacyMaterializedMode   = "product"
	legacyMaterializedCapPct = 0.80
	legacyMaterializedCapTol = 1e-9 // 浮点比较容差：落盘是 0.8，读回来必须是 0.8，容差只为防序列化噪声
	migratedMode             = "power_mean"
	migratedTotalPositionPct = 1.00
	migratedTotalPositionTol = 1e-9
)

// SizingV2Row 一个载荷（全局或某账号某层）的迁移前后读数。
// Before* 取**落盘原值**（不是 WithDefaults 归一后的值）：报告要回答的是"盘上写着什么、要改成什么"，
// 给归一后的值会把"键缺失"与"显式写了旧默认"两种完全不同的处置混成同一行。
type SizingV2Row struct {
	UserID       string  `json:"user_id"`
	Layer        string  `json:"layer"` // global / dedicated / account_rules
	ModeBefore   string  `json:"mode_before"`
	ModeAfter    string  `json:"mode_after"`
	CapBefore    float64 `json:"cap_before"`
	CapAfter     float64 `json:"cap_after"`
	Changed      bool    `json:"changed"`
	Skipped      bool    `json:"skipped"`
	Reason       string  `json:"reason,omitempty"`
	Written      bool    `json:"written"`
	FailErr      string  `json:"error,omitempty"`
	TargetBefore float64 `json:"target_before"` // 目标仓位上限位移（MS=1 时的天花板），dry-run 签字要看这个数
	TargetAfter  float64 `json:"target_after"`
}

// SizingV2Report 迁移总账（dry-run 报告与落盘结果共用同一结构，避免两套口径漂移）。
type SizingV2Report struct {
	Apply     bool          `json:"apply"` // false＝只读（未签字）；true＝已按报告落盘
	Scanned   int           `json:"scanned"`
	Changed   int           `json:"changed"`
	Written   int           `json:"written"`
	Failed    int           `json:"failed"`
	Skipped   int           `json:"skipped"`   // dry-run 下"需迁但没写"的条数（签字门挡住的量）
	Unchanged int           `json:"unchanged"` // 已是新默认/键缺失/owner 手调过：不动，只点名
	Followers int           `json:"followers"` // 无账号级文档、自动跟随全局的账号数（本次不建键）
	Rows      []SizingV2Row `json:"rows"`
}

// String 把报告压成一行可 grep 的总账（启动日志与部署 verify 探针都读这行）。
func (r SizingV2Report) String() string {
	return fmt.Sprintf("apply=%v 扫描=%d 需迁=%d 已写=%d 未签跳过=%d 不动=%d 自动跟随=%d 失败=%d",
		r.Apply, r.Scanned, r.Changed, r.Written, r.Skipped, r.Unchanged, r.Followers, r.Failed)
}

// sizingV2Needed 判断一个落盘 sizing 是否需要迁，并算出迁移后的值。
// 返回 (next, changed, reason)：changed=false 时 next 无意义，reason 说明为什么不动。
// English: decides whether this on-disk sizing needs the v4 rewrite, and what it becomes.
func sizingV2Needed(sc SizingConfig) (SizingConfig, bool, string) {
	// 已带 v2 标记＝这个载荷已经被本次迁移看过并处理过，重启不重跑（与 v1 同一个"只迁一次"纪律）。
	if sc.PolicyVersion >= sizingPolicyVersionV4 {
		return sc, false, fmt.Sprintf("已带 v4 迁移标记 policy_version=%d", sc.PolicyVersion)
	}
	// 两个判据各自独立：`mode` 与 `max_total_position_pct` 是两次不同裁决物化进来的默认值，
	// owner 只可能显式改过其中一个。合成一个"整块判旧形状"会把只改过一个键的账号整块跳过，
	// 也会把只该动一个键的账号两个键一起抬。
	modeRaw := strings.TrimSpace(sc.Mode)
	capRaw := sc.MaxTotalPositionPct
	needMode := strings.EqualFold(modeRaw, legacyMaterializedMode)
	needCap := capRaw > 0 && absF(capRaw-legacyMaterializedCapPct) < legacyMaterializedCapTol
	switch {
	// 两键都逐字停在旧默认 → 一起抬（这就是面板一次性物化出来的那批存量账号）。
	case needMode && needCap:
		next := sc
		next.Mode = migratedMode
		next.MaxTotalPositionPct = migratedTotalPositionPct
		next.PolicyVersion = sizingPolicyVersionV4
		return next, true, "mode=product→power_mean、max_total_position_pct=0.80→1.00"
	// 只有 mode 停在旧默认：总仓位上限那个数不迁（它已经不是 0.80，说明有人表过态）。
	case needMode:
		next := sc
		next.Mode = migratedMode
		next.PolicyVersion = sizingPolicyVersionV4
		return next, true, "mode=product→power_mean（总仓位上限非旧默认，原样保留）"
	// 只有总仓位上限停在旧默认：合成口径不迁（显式配过 power_mean 或别的写法都不动）。
	case needCap:
		next := sc
		next.MaxTotalPositionPct = migratedTotalPositionPct
		next.PolicyVersion = sizingPolicyVersionV4
		return next, true, "max_total_position_pct=0.80→1.00（合成口径非旧默认，原样保留）"
	case modeRaw == "":
		// 键缺失：读侧已经落到新默认 power_mean/1.00，这里**不许写**。
		// 写一次就是把今天的默认钉成这个账号的显式配置，日后改默认再也传不下去（§1.7 物化陷阱）。
		if capRaw <= 0 {
			return sc, false, "两键均未落盘（读侧已按新默认执行），不写以免物化默认值"
		}
		return sc, false, fmt.Sprintf("mode 键缺失（已是新默认），max_total_position_pct=%.2f 非旧默认，不动", capRaw)
	default:
		return sc, false, "落盘值是显式选择（非旧物化默认），迁移不得覆盖"
	}
}

// absF 浮点绝对值（单独一个函数只为把容差比较写得可读一次）。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// MigrateSizingToV4 §DYN-SIZE v4 迁移入口。**apply=false 时只读不写**（T4-8 签字门）。
//
// 调用纪律与 v1 迁移一致：必须早于任何 sizing 消费者装配（引擎/模拟盘/实盘控制器都读
// GetSizingConfigFor），且只在启动时调用一次。apply=true 必须由 owner 看过 dry-run 报告后显式给出，
// 不允许从配置文件或默认值里推出来——"忘了摘掉一个默认"就是静默改写资金参数。
// English: the v4 migration entry point. apply=false is read-only; apply=true must come from the
// owner's signed dry-run report, never from a config default.
func (m *Manager) MigrateSizingToV4(userIDs []string, apply bool) SizingV2Report {
	rep := SizingV2Report{Apply: apply}
	if m == nil {
		return rep
	}

	// collect 收一次载荷（全局 / 专用键 / 账号旧 rules.sizing 三层，与 v1 迁移同序同层）。
	collect := func(userID, layer string, sc SizingConfig, write func(SizingConfig) error) {
		rep.Scanned++
		next, changed, reason := sizingV2Needed(sc)
		row := SizingV2Row{UserID: userID, Layer: layer,
			ModeBefore: orUnset(sc.Mode), ModeAfter: orUnset(sc.Mode),
			CapBefore: sc.MaxTotalPositionPct, CapAfter: sc.MaxTotalPositionPct,
			TargetBefore: sc.MaxTotalPositionPct, TargetAfter: sc.MaxTotalPositionPct,
			Reason: reason}
		if changed {
			rep.Changed++
			row.Changed = true
			row.ModeAfter, row.CapAfter = orUnset(next.Mode), next.MaxTotalPositionPct
			row.TargetBefore, row.TargetAfter = sc.MaxTotalPositionPct, next.MaxTotalPositionPct
			if !apply {
				row.Skipped = true
				row.Reason = "dry-run（未签字）：" + reason
				rep.Skipped++
			} else if err := write(next); err != nil {
				row.FailErr = err.Error()
				rep.Failed++
				log.Printf("[config] §DYN-SIZE-v4 迁移写盘失败 账号=%q 层=%s：%v（保持旧值，行为等于没迁）", userID, layer, err)
			} else {
				row.Written = true
				rep.Written++
				log.Printf("[config] §DYN-SIZE-v4 迁移 账号=%q 层=%s：%s（mode %s→%s、总仓位上限 %.2f→%.2f）",
					userID, layer, reason, row.ModeBefore, row.ModeAfter, row.CapBefore, row.CapAfter)
			}
		} else {
			rep.Unchanged++
			if sc.PolicyVersion >= sizingPolicyVersionV4 {
				row.Reason = reason + fmt.Sprintf("（已迁过，幂等不再重跑）")
			}
		}
		rep.Rows = append(rep.Rows, row)
	}

	// ① 全局 rules.sizing。
	m.mu.RLock()
	globalRaw := m.rules.Sizing
	m.mu.RUnlock()
	collect("", "global", globalRaw, func(next SizingConfig) error {
		m.mu.Lock()
		m.rules.Sizing = next
		m.mu.Unlock()
		if err := m.Save(); err != nil {
			return fmt.Errorf("全局落盘失败: %w", err)
		}
		// 写后复读自证（与 v1 同口径）：读侧真吃到新值才算迁成，落盘成功但读链没到＝白迁。
		// 断言的是**本次迁移算出来的那一组值**，不是"读侧必须等于出厂值"——
		// owner 手调过的总仓位上限（比如 0.60）会被原样保留，按 1.00 断言会把正确落盘判成失败。
		reread := m.GetSizingConfig()
		if reread.ModeOf() != (SizingConfig{Mode: next.Mode}).ModeOf() ||
			absF(reread.MaxTotalPositionPct-next.MaxTotalPositionPct) > migratedTotalPositionTol {
			return fmt.Errorf("迁移后复读不符：mode=%q max_total_position_pct=%.4f（应为 mode=%q cap=%.4f）",
				reread.ModeOf(), reread.MaxTotalPositionPct, next.Mode, next.MaxTotalPositionPct)
		}
		return nil
	})

	// ② 逐账号（专用键优先；无专用键才看账号自己的 rules.sizing）。
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		if raw, exists := m.AccountKVRaw(uid, perUserSizingKey); exists {
			var sc SizingConfig
			if err := json.Unmarshal(raw, &sc); err != nil {
				rep.Failed++
				rep.Rows = append(rep.Rows, SizingV2Row{UserID: uid, Layer: "dedicated",
					FailErr: fmt.Sprintf("专用键解析失败: %v", err), Reason: "解析失败，未迁（读侧会回退继承值）"})
				log.Printf("[config] §DYN-SIZE-v4 账号 %s 专用 sizing 键解析失败，跳过迁移: %v", uid, err)
				continue
			}
			collect(uid, "dedicated", sc, func(next SizingConfig) error {
				return m.writeMigratedSizingV4(uid, next)
			})
			continue
		}
		if own, hasOwn := m.storedUserRules(uid); hasOwn {
			collect(uid, "account_rules", own.Sizing, func(next SizingConfig) error {
				return m.writeMigratedSizingV4(uid, next)
			})
			continue
		}
		// ②-c 什么账号级文档都没有：读侧自然落到全局，全局已按新默认跑 ⇒ 自动跟随。
		// 这里仍然计一次扫描并列一行，是为了让签字单的账号数与 userIDs 对得上账
		// （"扫过 N 个但报告里只找得到 M 行"这种读数本项目不收），并且**绝不为它凭空建键**——
		// 建键会把"跟随全局"变成"冻结在当前值"，日后改全局默认对它不再生效。
		rep.Scanned++
		rep.Followers++
		rep.Rows = append(rep.Rows, SizingV2Row{UserID: uid, Layer: "follower",
			ModeBefore: orUnset(""), ModeAfter: orUnset(""),
			Reason: "无账号级 sizing 文档，读侧自动跟随全局（本次不建键）"})
	}

	sort.Slice(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].UserID != rep.Rows[j].UserID {
			return rep.Rows[i].UserID < rep.Rows[j].UserID
		}
		return rep.Rows[i].Layer < rep.Rows[j].Layer
	})

	// 逐票一行打进日志：签字单要看的就是这张表（"报告存在但日志里读不到"＝又一件降级报成功的事）。
	for _, row := range rep.Rows {
		if !row.Changed {
			continue
		}
		log.Printf("[config] §DYN-SIZE-v4 %s 账号=%q 层=%s mode %s→%s 总仓位上限 %.2f→%.2f 原因=%s",
			map[bool]string{true: "拟迁", false: "已迁"}[!row.Written], row.UserID, row.Layer,
			row.ModeBefore, row.ModeAfter, row.CapBefore, row.CapAfter, row.Reason)
	}
	log.Printf("[config] §DYN-SIZE-v4 迁移总账 %s", rep.String())
	return rep
}

// writeMigratedSizingV4 把迁移后的 sizing 写入账号专用键，带写后复读自证。
// 与 v1 的 writeMigratedSizing 分开写：v1 还要顺手把 enabled/budget_mode 三键抬成下发态，
// v4 只动 mode 与总仓位上限两个键——把两次迁移的写侧混在一个函数里，日后任一改动都会牵到另一件。
// English: writes the migrated sizing into the account's dedicated key with read-back verification;
// deliberately separate from the v1 writer, which normalizes three different delivery keys.
func (m *Manager) writeMigratedSizingV4(userID string, next SizingConfig) error {
	if m.store == nil {
		return fmt.Errorf("账号级配置存储不可用")
	}
	data, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	m.mu.Lock()
	err = m.setConfigVerifiedLocked(userID, perUserSizingKey, data)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	got, _ := m.GetSizingConfigFor(userID)
	if got.ModeOf() != migratedMode {
		return fmt.Errorf("迁移后读侧 mode 仍为 %q（应为 %s）", got.ModeOf(), migratedMode)
	}
	return nil
}

// orUnset 空串在报告里写成 "«未落盘»" 而不是空：
// 签字单上"键缺失"与"显式写了 product"必须一眼分得开，前者本次不写、后者要写。
func orUnset(s string) string {
	if strings.TrimSpace(s) == "" {
		return "«未落盘»"
	}
	return strings.TrimSpace(s)
}
