// history.go 配置变更历史/回滚（§WS-K 维4）：全局 config.json 每次保存/热更前快照到
// `config_history/rules-<ts>.json`（原子写）；变更后做字段级 diff 记 opslog；回滚端点恢复
// 上一版并走既有加载机制。
//
// English: config change history & rollback (WS-K 维4). Before each save/hot-reload the global
// config.json is snapshotted into config_history/rules-<ts>.json (atomic), a field-level diff is
// written to the opslog audit, and the rollback endpoint restores a previous version.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/fileutil"
	"quant-trading-v2/internal/opslog"
)

// rulesHistoryDir config_history 目录（与 config.json 同目录）。
func rulesHistoryDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "config_history", "rules")
}

// SnapshotRules 把当前 config.json 复制到 config_history/rules-<ts>.json，返回快照名。
// 写前调用保证"上一版可回滚"。缺文件返回错误（调用方应跳过快照而非阻断保存）。
// English: copies the current config.json into config_history/rules-<ts>.json and returns the
// snapshot name. Call before writing so the previous version stays rollback-able.
func SnapshotRules(m *Manager) (string, error) {
	if m == nil || m.path == "" {
		return "", fmt.Errorf("配置路径为空")
	}
	// 读取当前 config.json 原文，快照目录与配置同目录（config_history/rules/），
	// 用纳秒时间戳做唯一名，冲突时追加 -N 序号避免覆盖；原子写保证快照完整。
	b, err := os.ReadFile(m.path)
	if err != nil {
		return "", err
	}
	dir := rulesHistoryDir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().Format("20060102_150405.000000000")
	path := filepath.Join(dir, ts+".json")
	n := 2
	for fileExists(path) {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", ts, n))
		n++
	}
	if err := fileutil.AtomicWrite(path, b, 0o644); err != nil {
		return "", err
	}
	return filepath.Base(path[:len(path)-len(".json")]), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// RuleSnapshotInfo 规则快照条目（前端展示用）。
type RuleSnapshotInfo struct {
	SnapshotTS string `json:"snapshot_ts"`
	Path       string `json:"path"`
}

// ListRuleSnapshots 列出 config.json 的全部历史快照（倒序）。
// English: lists all config.json history snapshots, newest first.
func ListRuleSnapshots(m *Manager) ([]RuleSnapshotInfo, error) {
	if m == nil || m.path == "" {
		return []RuleSnapshotInfo{}, nil
	}
	dir := rulesHistoryDir(m.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RuleSnapshotInfo{}, nil
		}
		return nil, err
	}
	out := make([]RuleSnapshotInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, RuleSnapshotInfo{
			SnapshotTS: strings.TrimSuffix(name, ".json"),
			Path:       filepath.Join(dir, name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapshotTS > out[j].SnapshotTS })
	return out, nil
}

// RestoreRulesContent 读取指定快照的 config.json 原始内容（ts 不含 .json 后缀）。
// English: reads the raw config.json bytes from a named snapshot (ts without the .json suffix).
func RestoreRulesContent(m *Manager, ts string) ([]byte, error) {
	if m == nil || m.path == "" {
		return nil, fmt.Errorf("配置路径为空")
	}
	name := ts
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	b, err := os.ReadFile(filepath.Join(rulesHistoryDir(m.path), name))
	if err != nil {
		return nil, fmt.Errorf("快照不存在: %s", ts)
	}
	return b, nil
}

// DiffRules 对两份 config.json 做字段级 diff（JSON 路径逐项），返回可读文本行。
// 变更行形如 `qmt.price_type: market -> limit`；新增/删除带 + / - 前缀。
// English: field-level diff between two config.json documents (JSON paths), returned as readable
// lines like `qmt.price_type: market -> limit`.
func DiffRules(before, after []byte) (string, error) {
	var vb, va map[string]any
	if err := json.Unmarshal(before, &vb); err != nil {
		return "", fmt.Errorf("解析旧配置失败: %v", err)
	}
	if err := json.Unmarshal(after, &va); err != nil {
		return "", fmt.Errorf("解析新配置失败: %v", err)
	}
	var lines []string
	diffJSON("", vb, va, &lines)
	if len(lines) == 0 {
		return "(无变更)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// diffJSON 递归对比两棵 JSON 值，变更路径写入 lines。
// English: recursively diffs two JSON values, appending changed paths to lines.
func diffJSON(path string, b, a any, lines *[]string) {
	prefix := func(p string) string {
		if p == "" {
			return "<root>"
		}
		return p
	}
	switch at := a.(type) {
	case map[string]any:
		bt, bok := b.(map[string]any)
		if !bok {
			*lines = append(*lines, fmt.Sprintf("%s: %v -> %v", prefix(path), b, a))
			return
		}
		keys := map[string]bool{}
		for k := range at {
			keys[k] = true
		}
		for k := range bt {
			keys[k] = true
		}
		for k := range keys {
			np := path + "." + k
			av, aok := at[k]
			bv, bok := bt[k]
			switch {
			case aok && !bok:
				*lines = append(*lines, fmt.Sprintf("+ %s: %v", prefix(np), av))
			case !aok && bok:
				*lines = append(*lines, fmt.Sprintf("- %s: %v", prefix(np), bv))
			default:
				diffJSON(np, bv, av, lines)
			}
		}
	default:
		if fmt.Sprintf("%v", b) != fmt.Sprintf("%v", a) {
			*lines = append(*lines, fmt.Sprintf("%s: %v -> %v", prefix(path), b, a))
		}
	}
}

// RestoreRulesContentCurrent 读取当前 config.json 原文（供 diff/回滚比对）。
// English: reads the current config.json raw bytes (for diff/rollback comparison).
func RestoreRulesContentCurrent(m *Manager) ([]byte, error) {
	if m == nil || m.path == "" {
		return nil, fmt.Errorf("配置路径为空")
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// WriteConfigFile 把给定字节原子写入 config.json（回滚恢复用；后续由 config.Watch 热重载生效）。
// English: atomically writes the given bytes to config.json (used by rollback; the config.Watch
// hot-reload applies it afterwards).
func WriteConfigFile(m *Manager, b []byte) error {
	if m == nil || m.path == "" {
		return fmt.Errorf("配置路径为空")
	}
	if err := fileutil.AtomicWrite(m.path, b, 0o644); err != nil {
		return err
	}
	opslog.Audit("config_rollback", "admin", m.path, "ok")
	return nil
}

// AuditRulesDiff 变更后记 opslog 审计（配合 WS-F C1 审计日志）。
// English: records a config-change audit line to opslog after a save.
func AuditRulesDiff(m *Manager, diff string) {
	opslog.Audit("config_change", "admin", m.path, diff)
}
