// config_history.go §WS-K 维4 + §0929CFG-HIST 配置历史/回滚端点（admin）：
// 查看两本账的历史快照与字段级 diff，并从快照回滚（恢复后立即落盘 + 审计）。
// 两本账 = ① 全局账 config.json（D1 全局值 / 调度 / 实盘配置）② 账号账 KVStore 文档
// （战法参数 / 账号级 D1 覆盖 / 多空开关），后者由 `account=<账号>` 参数进入。
// English: history & rollback endpoints for both config ledgers — the global config.json and
// per-account KVStore documents (strategy params / per-account D1 / long-short toggle).
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
)

// handleConfigHistory 处理 GET /api/config/history（admin）：返回历史快照列表（倒序），
// 并支持 ?diff=<ts> 对比「当前 vs 指定快照」的字段级变更。
//
// §0929CFG-HIST（P1-2 两本账）：新增可选参数 `account=<账号>`——
//   - 不带：列/比的是**全局账**（config.json：D1 全局值、调度、实盘配置），行为与旧版完全一致；
//   - 带：列/比的是**该账号账**（KVStore 里的战法参数主文档 / 账号级 D1 覆盖 / 多空开关，
//     文件名前缀点名是哪本），这样"我在设置页改的战法阈值"才有对应的历史可查。
//
// 租户护栏：带 account 时按 denyOutOfScope 判定调用者是否管得到这个账号，越权一律 403——
// 历史文件里含配置全量（含实盘参数），不能让它成为跨租户读取口。
// English: lists config history; ?account=<id> switches to that account's ledger (strategy /
// per-account D1 / long-short), guarded by the tenant scope check. ?diff=<ts> gives field-level
// diff, with secret-named paths reported by length only.
func (s *Server) handleConfigHistory(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	if account != "" && !s.denyOutOfScope(w, r, account) {
		return
	}
	diffTS := r.URL.Query().Get("diff")
	if diffTS != "" {
		var before, after []byte
		var err error
		if account == "" {
			if before, err = config.RestoreRulesContent(s.cfg, diffTS); err != nil {
				writeError(w, 404, err.Error())
				return
			}
			if after, err = config.RestoreRulesContentCurrent(s.cfg); err != nil {
				writeError(w, 500, err.Error())
				return
			}
		} else {
			// 账号账：快照内容与**该快照所属那本账**的当前生效文档逐字段比
			// （落点由文件名前缀反推，绝不拿主文档去比一份 D1 快照——那样 diff 会整屏假差异）。
			snap, key, kerr := config.RestoreAccountSnapshot(s.cfg, account, diffTS)
			if kerr != nil {
				writeError(w, 404, kerr.Error())
				return
			}
			before = snap
			if after, err = config.EffectiveAccountDoc(s.cfg, account, key); err != nil {
				writeError(w, 500, err.Error())
				return
			}
		}
		d, err := config.DiffRules(before, after)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]interface{}{"snapshot_ts": diffTS, "diff": d, "account": account})
		return
	}
	var list []config.RuleSnapshotInfo
	var err error
	if account == "" {
		list, err = config.ListRuleSnapshots(s.cfg)
	} else {
		list, err = config.ListAccountSnapshots(s.cfg, account)
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"snapshots": list})
}

// handleConfigRollback 处理 POST /api/config/rollback {snapshot_ts, account?}（admin）：
// 从快照恢复配置并记 opslog 审计。
//
// §0929CFG-HIST：两本账各有归宿——
//   - 不带 account：恢复 config.json 原子落盘（走 config.Watch 热重载生效），与旧版一致；
//   - 带 account：恢复**该账号**的 KV 文档，落点由快照文件名前缀反推（rules_/d1_/longshort_），
//     前缀不认识就拒绝——宁可不滚，也不能猜着写。写后走写后复读自证（§0926E2E-W1B 口径），
//     不匹配就报错，绝不"回滚了但没落住"。
//
// 账号账回滚的边界交代：无覆盖时期生成的快照内容是"当时生效的全局副本"，恢复它会为该账号
// 建立一份覆盖（值与当时全局相同，行为等价，但此后全局改动不再自动影响该账号）。
// 这一点必须显式回报，不能让人事后才发现多了一层覆盖。
// English: restores a snapshot; account-scoped rollbacks derive their destination KV slot from the
// snapshot filename prefix and report the restored field diff. A pre-override snapshot restores the
// then-effective global copy as an explicit override, which is reported rather than hidden.
func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotTS string `json:"snapshot_ts"`
		Account    string `json:"account"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SnapshotTS == "" {
		writeError(w, 400, "需要 snapshot_ts")
		return
	}
	actor := userIDFor(r)
	if req.Account != "" {
		if !s.denyOutOfScope(w, r, req.Account) {
			return
		}
		b, key, err := config.RestoreAccountSnapshot(s.cfg, req.Account, req.SnapshotTS)
		if err != nil {
			writeError(w, 404, err.Error())
			return
		}
		before, _ := config.EffectiveAccountDoc(s.cfg, req.Account, key)
		created := !s.cfg.HasAccountOverrideFor(req.Account, key)
		if err := config.WriteAccountRulesRaw(s.cfg, req.Account, b, key); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		// 字段级 diff 审计统一经 AuditAccountWrite（它按写前/写后两份文档算差异，
		// 敏感字段名只报长度），这里不再自己拼一份 d ——两处算 diff 迟早口径分叉。
		opslog.Audit("config_rollback", actor, "account:"+req.Account+"/"+keySuffix(key),
			fmt.Sprintf("snapshot=%s created_override=%v", req.SnapshotTS, created))
		config.AuditAccountWrite(s.cfg, actor, req.Account, key, before)
		log.Printf("[config] 账号 %s 配置回滚到快照 %s（操作者=%s，新建覆盖=%v）", req.Account, req.SnapshotTS, actor, created)
		writeJSON(w, 200, map[string]string{
			"status": "ok", "snapshot_ts": req.SnapshotTS, "scope": "account",
			"created_override": strconv.FormatBool(created),
		})
		return
	}
	b, err := config.RestoreRulesContent(s.cfg, req.SnapshotTS)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	// §AUDIT-UNIFY（owner 裁决 2026-09-26「配置变更审计统一走包装」）：回滚同样是 config.json
	// 的一次变更，审计走 AuditRulesDiff 单入口——写前先取当前内容算字段级 diff（拿不到不阻断
	// 回滚本身，diff 记 "unavailable" 留痕，绝不静默）；target 点名 "rollback:<快照ts>"。
	beforeBytes, beforeErr := config.RestoreRulesContentCurrent(s.cfg)
	if err := config.WriteConfigFile(s.cfg, b); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	d := "unavailable"
	if beforeErr == nil {
		if dd, derr := config.DiffRules(beforeBytes, b); derr == nil {
			d = dd
		}
	}
	config.AuditRulesDiff(s.cfg, actor, "rollback:"+req.SnapshotTS, d)
	log.Printf("[config] 配置回滚到快照 %s（操作者=%s）", req.SnapshotTS, actor)
	writeJSON(w, 200, map[string]string{"status": "ok", "snapshot_ts": req.SnapshotTS, "scope": "global"})
}

// keySuffix 取 KV 键的历史前缀名（审计 target 里用短名，不把内部键串散播到日志面）。
// English: the short history prefix for a KV key, used in audit targets.
func keySuffix(key string) string {
	switch key {
	case config.KVKeyRules():
		return "rules"
	case config.KVKeyD1():
		return "d1"
	case config.KVKeyLongShort():
		return "longshort"
	default:
		return "unknown"
	}
}
