// versioning.go 策略参数版本化与回滚（§WS-H C2）：applied_rules.json / applied_factors.json /
// applied_patterns.json / grayscale_rules.json 每次写入前自动快照到
// `config_history/strategies/<ts>.json`（data.AtomicWrite 原子落盘）；回滚端点读取历史快照
// 原子恢复全部四类文件并记 opslog 审计。
//
// English: strategy-parameter versioning and rollback (WS-H C2). Every write to the four strategy
// rule files is snapshotted first into config_history/strategies/<ts>.json (atomic write); the
// rollback endpoint restores all four files from a historical snapshot and records an opslog audit.
package research

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
)

// 四个被版本化的策略规则文件（相对 dataDir）。
// English: the four versioned strategy-rule files (relative to dataDir).
var versionedStrategyFiles = []string{
	"applied_rules.json",
	"applied_factors.json",
	"applied_patterns.json",
	"grayscale_rules.json",
}

// strategySnapshot 一次参数快照：全部四类文件的原始内容（json.RawMessage 保持字节不变）。
// English: one parameter snapshot — the raw bytes of all four rule files (RawMessage keeps bytes intact).
type strategySnapshot struct {
	SnapshotTS      string          `json:"snapshot_ts"`
	AppliedRules    json.RawMessage `json:"applied_rules,omitempty"`
	AppliedFactors  json.RawMessage `json:"applied_factors,omitempty"`
	AppliedPatterns json.RawMessage `json:"applied_patterns,omitempty"`
	GrayscaleRules  json.RawMessage `json:"grayscale_rules,omitempty"`
}

// snapshotsDir 快照目录（config_history/strategies）。
func snapshotsDir(dataDir string) string {
	return filepath.Join(dataDir, "config_history", "strategies")
}

// SnapshotStrategies 把当前四类策略规则文件快照到 config_history/strategies/<ts>.json，
// 返回快照时间戳。写前调用保证"旧值可回滚"。缺文件不报错（对应字段留空）。
// English: snapshots the current four strategy-rule files into config_history/strategies/<ts>.json
// and returns the timestamp. Missing files are recorded as absent, not errors.
func SnapshotStrategies(dataDir string) (string, error) {
	snap := strategySnapshot{SnapshotTS: time.Now().Format("20060102_150405.000000000")}
	for _, name := range versionedStrategyFiles {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		switch name {
		case "applied_rules.json":
			snap.AppliedRules = b
		case "applied_factors.json":
			snap.AppliedFactors = b
		case "applied_patterns.json":
			snap.AppliedPatterns = b
		case "grayscale_rules.json":
			snap.GrayscaleRules = b
		}
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	dir := snapshotsDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// 同一纳秒出现并发快照时以 -2/-3… 序号区分（幂等追加，不覆盖）。
	ts := snap.SnapshotTS
	path := filepath.Join(dir, ts+".json")
	n := 2
	for fileExists(path) {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", ts, n))
		n++
	}
	if err := data.AtomicWrite(path, b, 0o644); err != nil {
		return "", err
	}
	return filepath.Base(path[:len(path)-len(".json")]), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// SnapshotInfo 快照列表条目（前端展示用）。
type SnapshotInfo struct {
	SnapshotTS string `json:"snapshot_ts"`
	Path       string `json:"path"`
}

// ListSnapshots 列出全部历史快照（按时间倒序，最新在前）。
// English: lists all historical snapshots, newest first.
func ListSnapshots(dataDir string) ([]SnapshotInfo, error) {
	dir := snapshotsDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SnapshotInfo{}, nil
		}
		return nil, err
	}
	out := make([]SnapshotInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		ts := strings.TrimSuffix(name, ".json")
		out = append(out, SnapshotInfo{SnapshotTS: ts, Path: filepath.Join(dir, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapshotTS > out[j].SnapshotTS })
	return out, nil
}

// RestoreSnapshot 从历史快照原子恢复四类策略规则文件；快照不存在返回错误。
// 恢复成功后记 opslog 审计。English: atomically restores the four strategy-rule files from a
// historical snapshot; errors when the snapshot is missing. Records an opslog audit on success.
func RestoreSnapshot(dataDir, ts string) error {
	name := ts
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	b, err := os.ReadFile(filepath.Join(snapshotsDir(dataDir), name))
	if err != nil {
		return fmt.Errorf("快照不存在: %s", ts)
	}
	var snap strategySnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return fmt.Errorf("快照解析失败: %v", err)
	}
	files := map[string]json.RawMessage{
		"applied_rules.json":    snap.AppliedRules,
		"applied_factors.json":  snap.AppliedFactors,
		"applied_patterns.json": snap.AppliedPatterns,
		"grayscale_rules.json":  snap.GrayscaleRules,
	}
	for _, name := range versionedStrategyFiles {
		content := files[name]
		if len(content) == 0 {
			continue // 快照中该文件缺失 → 保持现状，不误删
		}
		if err := data.AtomicWrite(filepath.Join(dataDir, name), content, 0o644); err != nil {
			return err
		}
	}
	opslog.Audit("strategy_rollback", "admin", ts, "ok")
	return nil
}

// snapshotBeforeWrite 写前快照（WS-H C2）：返回快照名；快照失败仅告警不影响主流程。
// English: pre-write snapshot helper — returns the snapshot name; a snapshot failure is logged and
// ignored so it never blocks the actual strategy write.
func snapshotBeforeWrite(dataDir string) string {
	ts, err := SnapshotStrategies(dataDir)
	if err != nil {
		opslog.Audit("strategy_snapshot", "auto", dataDir, "fail: "+err.Error())
		return ""
	}
	return ts
}
