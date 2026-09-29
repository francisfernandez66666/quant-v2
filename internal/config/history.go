// history.go 配置变更历史/回滚（§WS-K 维4 + §0929CFG-HIST）：**两本账**。
//
// ① 全局账：config.json 在写前快照到 config_history/rules/<ts>.json（原子写）。
// 属于这本账的配置面：调度器（scheduler）、无账号覆盖时的 D1、以及任何 setter 走
// "无 store 回退"分支的写入。
// ② 账号账：账号级配置文档在写前快照到 config_history/accounts/<安全化账号>/<前缀>_<ts>.json。
// 属于这本账的配置面：战法参数与 LLM 与实盘 QMT 与模拟盘 paper（都在 KVStore 的 perUserKey
// 主配置文档里）、多空开关（独立键 perUserLongShortKey）、D1 账号覆盖（独立键 perUserD1Key）。
// 变更后做字段级 diff 记 opslog；回滚端点按快照名前缀恢复到**它自己那本账**，走既有加载机制。
//
// §0929CFG-HIST 口径校正（09-29 全量审计批 P1-2）：文件头旧版本只声明了"全局 config.json
// 每次保存/热更前快照"，而全仓当时**真的只有一个生产调用点**（实盘配置那条通道）——那句
// 不变量声明与实现不符，正是这条缝一直没被怀疑的原因。本轮把每条配置写通道都接进单入口
// （SnapshotBeforeWrite / AuditConfigWrite），声明才第一次成立；同一轮还锤出两条分组错账：
// D1 与 QMT 都不是无条件的全局写入（有账号覆盖/有 store 时落账号账），多空开关落在独立键上。
// 新增或改动写通道时必须经这两本账之一、并在 resolveWriteScope 登记真实落点，
// 别再留第三条无快照的路，也别把快照翻到没人读的那本账上（假留痕比无留痕更坏）。
//
// English: two config history ledgers — the global one for config.json (the scheduler surface and
// every no-store fallback write) and a per-account one for KVStore-backed documents (strategy /
// LLM / QMT / paper live in the per-user rules document, long-short and the D1 override each live in
// their own KV key). Both are snapshotted before the write and audited with a field-level diff.
package config

import (
	"encoding/json"
	"fmt"
	"log"
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

// accountHistoryDir 账号级配置的历史目录（config_history/accounts/<安全化账号>/）。
// §0929CFG-HIST：账号 ID 直接来自调用方参数，落进路径前必须消毒——目录段只保留
// [A-Za-z0-9._-]，其余字符换成 '_'，且不含 "."/".." 的裸点段（防把快照写到目录外去）。
// 消毒是单向的：不同原始 ID 可能撞进同一个安全名，这只影响"历史目录合并展示"，
// 绝不会影响配置读写本身（那走 KVStore 的原始账号键）。
// English: per-account history directory; the account id is sanitized before it becomes a
// path segment so a caller-supplied id can never escape the history root.
func accountHistoryDir(configPath, userID string) string {
	return filepath.Join(filepath.Dir(configPath), "config_history", "accounts", sanitizeHistorySegment(userID))
}

// sanitizeHistorySegment 把任意字符串消毒成单个安全的路径段（空串归一为 "_"）。
func sanitizeHistorySegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "_"
	}
	// 前导点同样危险（".""隐藏段"、"../" 变体已被逐字替换挡掉，这里再兜一层）。
	for strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	return out
}

// writeSnapshotBytes 把内容以「前缀_纳秒时间戳」为唯一名原子写入 dir，返回快照名（不含 .json）。
// 全局账用空前缀（历史文件名保持裸 ts，兼容既有快照与前端）；账号账按 KV 键带前缀。
// 两本账共用这一条命名/落盘口径，避免各自演化出"一个会覆盖一个不会"的差别。
//
// §0929CFG-SECRET（本批新落码直接带出的面，顺手收口）：快照目录 0700、快照文件 0600，
// 并在写入时把目录内**既有的** 0644 历史快照一并收紧。原因很直白——config.json 里带着网关令牌
// （本轮实测该键在位），而快照就是它的一份完整副本：0644 等于把令牌从"仅本机用户可读"抬成
// "同机任意进程可读"，多账号账开出来之后副本还会成倍增长。收紧只在 config_history 目录内做，
// 不碰配置文件本身（那是部署文档的权限口径范围）。
// English: snapshots are secret copies of config.json (it carries the gateway token), so the
// history dir is 0700 and every snapshot 0600 — legacy 0644 copies in that dir are tightened too.
func writeSnapshotBytes(dir string, content []byte, prefix string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tightenHistoryDir(dir)
	ts := time.Now().Format("20060102_150405.000000000")
	base := ts
	if prefix != "" {
		base = prefix + "_" + ts
	}
	path := filepath.Join(dir, base+".json")
	n := 2
	for fileExists(path) {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", base, n))
		n++
	}
	if err := fileutil.AtomicWrite(path, content, 0o600); err != nil {
		return "", err
	}
	return filepath.Base(path[:len(path)-len(".json")]), nil
}

// tightenHistoryDir 把目录内权限宽于 0600 的快照文件收紧（一次性自愈，失败只 log 不阻断写入：
// 权限收紧失败不该反过来让配置改不动）。
// English: best-effort chmod of over-permissive legacy snapshots inside the history dir.
func tightenHistoryDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode().Perm()&0o077 == 0 {
			continue // 已是 0600/0700 系，跳过（稳态下这条分支恒中，开销只在首两次写）
		}
		p := filepath.Join(dir, e.Name())
		if cerr := os.Chmod(p, 0o600); cerr != nil {
			log.Printf("[config] 历史快照权限收紧失败（不影响写入）: %s: %v", p, cerr)
		}
	}
}

// SnapshotRules 把当前 config.json 复制到 config_history/rules/<ts>.json，返回快照名。
// 写前调用保证"上一版可回滚"。缺文件返回错误（调用方应跳过快照而非阻断保存）。
// English: copies the current config.json into the global history dir and returns the snapshot
// name. Call before writing so the previous version stays rollback-able.
func SnapshotRules(m *Manager) (string, error) {
	if m == nil || m.path == "" {
		return "", fmt.Errorf("配置路径为空")
	}
	// 读取当前 config.json 原文，快照目录与配置同目录（config_history/rules/）。
	b, err := os.ReadFile(m.path)
	if err != nil {
		return "", err
	}
	return writeSnapshotBytes(rulesHistoryDir(m.path), b, "")
}

// SnapshotAccountRules §0929CFG-HIST：账号级主配置（KVStore 的 perUserKey 载荷）写前快照，
// 等价于 SnapshotAccountKey(m, userID, perUserKey)。
// English: convenience wrapper snapshotting the account's main rules document.
func SnapshotAccountRules(m *Manager, userID string) (string, error) {
	return SnapshotAccountKey(m, userID, perUserKey)
}

// kvHistoryPrefix 账号级 KV 各键在历史目录里的文件名前缀。
// 前缀进文件名（而不是再开一层目录）有两个理由：① 列历史时一眼看出改的是哪本配置；
// ② 回滚端点可以从前缀直接反推该写回哪个 KV 键，杜绝"把 D1 快照灌回主配置"的跨册误滚。
// English: the filename prefix per KV key — the prefix is part of the name so a rollback can
// derive which KV slot to restore, making cross-ledger mis-rollback impossible.
func kvHistoryPrefix(key string) string {
	switch key {
	case perUserKey:
		return "rules"
	case perUserD1Key:
		return "d1"
	case perUserLongShortKey:
		return "longshort"
	default:
		// 未知键：消毒后原样作前缀，绝不落到裸 ts 名（裸 ts 名与主配置快照撞车）。
		return "key_" + sanitizeHistorySegment(key)
	}
}

// accountKeyFromHistoryPrefix 由快照文件名的前缀反推 KV 键（回滚作用域判定用）。
// 返回 ok=false 表示该文件前缀不认识，禁止回滚（宁可不滚，也不能猜着写）。
func accountKeyFromHistoryPrefix(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "rules_"):
		return perUserKey, true
	case strings.HasPrefix(name, "d1_"):
		return perUserD1Key, true
	case strings.HasPrefix(name, "longshort_"):
		return perUserLongShortKey, true
	default:
		return "", false
	}
}

// SnapshotAccountKey 账号级某个 KV 配置键的写前快照。
// 无该键覆盖时快照的是**当前生效值**（读侧的回退结果），语义即"这次写入之前，运行时真正吃到的
// 那份配置"；因此回滚它等于把该账号回到未覆盖前的状态。
// store 未注入或账号为空时返回错误：那意味着这条通道根本没有账号级持久化，
// 调用方必须如实报错，绝不能"跳过快照照样回 200"（§ROBUST 降级报成功族）。
// English: pre-write snapshot of one account-level KV config key, falling back to the currently
// effective (resolved) value when the account has no override for that key.
func SnapshotAccountKey(m *Manager, userID, key string) (string, error) {
	if m == nil || m.path == "" {
		return "", fmt.Errorf("配置路径为空")
	}
	if m.store == nil || userID == "" {
		return "", fmt.Errorf("账号级配置存储不可用，无法快照（账号=%q）", userID)
	}
	content, ok := m.AccountKVRaw(userID, key)
	if !ok {
		v, err := m.effectiveAccountValue(userID, key)
		if err != nil {
			return "", err
		}
		content = v
	}
	name, err := writeSnapshotBytes(accountHistoryDir(m.path, userID), content, kvHistoryPrefix(key))
	if err != nil {
		return "", err
	}
	return name, nil
}

// effectiveAccountValue 返回"该键当前生效值"的 JSON 原文（无覆盖时的快照底片）。
// 三个已知键各自的回退口径必须与读侧一致：主配置回退全局 Rules 副本，D1 回退全局 D1，
// 多空开关回退出厂默认——这里若与读侧不一致，快照就成了"看着留痕其实不是旧值"的假证据。
// English: resolves the currently effective JSON for a KV key, mirroring each reader's fallback.
func (m *Manager) effectiveAccountValue(userID, key string) ([]byte, error) {
	switch key {
	case perUserKey:
		b, err := json.Marshal(m.userRules(userID))
		if err != nil {
			return nil, fmt.Errorf("账号 %s 主配置序列化失败: %w", userID, err)
		}
		return b, nil
	case perUserD1Key:
		b, err := json.Marshal(m.GetD1ConfigFor(userID))
		if err != nil {
			return nil, fmt.Errorf("账号 %s D1 配置序列化失败: %w", userID, err)
		}
		return b, nil
	case perUserLongShortKey:
		b, err := json.Marshal(m.GetLongShortConfigFor(userID))
		if err != nil {
			return nil, fmt.Errorf("账号 %s 多空开关序列化失败: %w", userID, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("未知账号级配置键: %s", key)
}

// ListAccountSnapshots 列出某账号级配置的全部历史快照（含各 KV 键，倒序）。
// English: lists one account's history snapshots across all its KV config keys, newest first.
func ListAccountSnapshots(m *Manager, userID string) ([]RuleSnapshotInfo, error) {
	if m == nil || m.path == "" || userID == "" {
		return []RuleSnapshotInfo{}, nil
	}
	return listSnapshotsIn(accountHistoryDir(m.path, userID))
}

// listSnapshotsIn 读取目录内 .json 快照并按 ts 倒序（全局账与账号账共用）。
func listSnapshotsIn(dir string) ([]RuleSnapshotInfo, error) {
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

// RestoreAccountSnapshot 读取某账号指定快照的原始 JSON 内容，并回报它属于哪个 KV 键（ts 不含 .json）。
// 前缀不认识时直接拒绝——回滚必须有确定的落点。
// English: reads an account snapshot's bytes and reports which KV key it belongs to; an unknown
// prefix is refused because a rollback needs a definite destination.
func RestoreAccountSnapshot(m *Manager, userID, ts string) ([]byte, string, error) {
	if m == nil || m.path == "" {
		return nil, "", fmt.Errorf("配置路径为空")
	}
	if userID == "" {
		return nil, "", fmt.Errorf("需要账号")
	}
	safe := strings.TrimSuffix(sanitizeHistorySegment(ts), ".json") + ".json"
	b, err := os.ReadFile(filepath.Join(accountHistoryDir(m.path, userID), safe))
	if err != nil {
		return nil, "", fmt.Errorf("快照不存在: %s", ts)
	}
	key, ok := accountKeyFromHistoryPrefix(strings.TrimSuffix(safe, ".json"))
	if !ok {
		return nil, "", fmt.Errorf("快照 %s 前缀无法判定落点，拒绝回滚", ts)
	}
	return b, key, nil
}

// CurrentAccountRulesRaw 读取账号当前生效的主配置原始 JSON（diff/回滚比对用）。
// 无账号级覆盖时返回当前生效快照的序列化结果，保证 diff 的两侧永远同构。
// English: the account's current raw JSON (for diff/rollback); falls back to the serialized
// effective snapshot when no override exists.
func CurrentAccountRulesRaw(m *Manager, userID string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("配置管理器为空")
	}
	return m.effectiveAccountValue(userID, perUserKey)
}

// WriteAccountRulesRaw 把给定快照字节写回它**自己所属的那本 KV 账**（回滚恢复用），带写后复读自证。
// §0929CFG-HIST：落点由快照名前缀决定（RestoreAccountSnapshot 已判定），这里不再序列化——
// 快照是什么，回滚后就是什么，否则"回滚"本身成了一次改写，字段顺序/缺省省略都会漂移。
// English: restores exact snapshot bytes into the KV slot derived from the snapshot's own prefix,
// with read-back verification (no re-serialization).
func WriteAccountRulesRaw(m *Manager, userID string, b []byte, key string) error {
	if m == nil {
		return fmt.Errorf("配置管理器为空")
	}
	return m.SetAccountKVRaw(userID, key, b)
}

// fileExists 判断路径存在。
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
	return listSnapshotsIn(rulesHistoryDir(m.path))
}

// RestoreRulesContent 读取指定快照的 config.json 原始内容（ts 不含 .json 后缀）。
// §0929CFG-HIST：入参来自 HTTP query，必须先按路径段消毒再拼——旧写法直接 Join，
// 一个带 ../ 的 snapshot_ts 就能把历史目录外的任意文件当"快照"读出来（回滚那条路还会把它
// 当配置写回）。消毒后不可能命中真实快照名以外的路径，故错误语义不变（仍报"快照不存在"）。
// English: the snapshot name comes from a query string, so it is sanitized as a single path
// segment before joining — otherwise a "../" payload would read (and rollback-write) arbitrary files.
func RestoreRulesContent(m *Manager, ts string) ([]byte, error) {
	if m == nil || m.path == "" {
		return nil, fmt.Errorf("配置路径为空")
	}
	name := ts
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	safe := strings.TrimSuffix(sanitizeHistorySegment(name), ".json") + ".json"
	b, err := os.ReadFile(filepath.Join(rulesHistoryDir(m.path), safe))
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
// §0929CFG-SECRET：命中敏感字段名的路径**只报长度不报值**——diff 文本会进 opslog 审计（磁盘上、
// 也会被运维日志采集走），而这份文档里就有网关令牌与通知 webhook；旧写法把两侧原值一起写进
// 审计行，等于"为了留痕又造了一处泄露"。长度足以判断"令牌换过没有"，值一律不落纸。
// English: secret-named paths are reported by length only — the diff lands in the on-disk audit log,
// and the document carries the gateway token, so dumping both sides would trade one gap for a leak.
func diffJSON(path string, b, a any, lines *[]string) {
	prefix := func(p string) string {
		if p == "" {
			return "<root>"
		}
		return p
	}
	// 敏感叶子：不再递归、不打印原值，只给"变/没变 + 各自长度"。
	if path != "" && isSecretPath(path) {
		bs, as := fmt.Sprintf("%v", b), fmt.Sprintf("%v", a)
		if bs == as {
			return // 敏感值未变 → 一条都不记（审计里连"存在这个值"都不额外暴露）
		}
		*lines = append(*lines, fmt.Sprintf("%s: <已变更 旧%d字/新%d字>", prefix(path), len(bs), len(as)))
		return
	}
	switch at := a.(type) {
	case map[string]any:
		bt, bok := b.(map[string]any)
		if !bok {
			*lines = append(*lines, fmt.Sprintf("%s: %v -> %v", prefix(path), b, a))
			// 一侧根本不是对象（整节点被换成标量或数组）：记一条「旧值→新值」就收工，
			// 不再按键集合递归，否则会拿 nil 去比、产出满屏无意义的差异行。
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
				*lines = append(*lines, fmt.Sprintf("+ %s: %s", prefix(np), renderDiffValue(np, av, false)))
			case !aok && bok:
				*lines = append(*lines, fmt.Sprintf("- %s: %s", prefix(np), renderDiffValue(np, bv, true)))
			default:
				diffJSON(np, bv, av, lines)
			}
		}
	default:
		if fmt.Sprintf("%v", b) != fmt.Sprintf("%v", a) {
			*lines = append(*lines, fmt.Sprintf("%s: %s -> %s", prefix(path),
				renderDiffValue(path, b, true), renderDiffValue(path, a, false)))
		}
	}
}

// isSecretPath 判断某个 JSON 路径的叶子字段是否属于敏感凭据类（令牌/密钥/口令/webhook）。
// 判定只看**最后一段字段名**，且用"去掉下划线后含关键字"的宽松匹配：
// 宁可多遮几个（多遮的代价只是审计少一行可读值），也不能漏遮一个（漏遮＝凭据进日志文件）。
// 注意 weight/threshold 这类同族但无害的字段不会被误伤——它们不含下列任何关键字。
// English: leaf-name based secret detector, deliberately over-inclusive: over-masking costs one
// readable audit value, under-masking costs a credential in a log file.
func isSecretPath(path string) bool {
	leaf := path
	if i := strings.LastIndex(leaf, "."); i >= 0 {
		leaf = leaf[i+1:]
	}
	leaf = strings.ToLower(strings.ReplaceAll(leaf, "_", ""))
	for _, kw := range []string{"token", "apikey", "password", "passwd", "secret", "webhook", "accesskey", "privatekey"} {
		if strings.Contains(leaf, kw) {
			return true
		}
	}
	// 裸 key/keys 单独判：它既可能是"第几把密钥"（敏感），也可能是 map 的普通键名（不敏感）。
	// 这里按字段名精确等于 key/keys 才算，避免 ".keys" 之外的路径（如 xxx_key_id）被过度扩张。
	if leaf == "key" || leaf == "keys" {
		return true
	}
	return false
}

// renderDiffValue 渲染差异值：敏感路径只报长度，其余原样。gone=true 表示该侧是被删除的一侧。
// English: renders one diff operand — length only for secret paths, verbatim otherwise.
func renderDiffValue(path string, v any, gone bool) string {
	if path != "" && isSecretPath(path) {
		return fmt.Sprintf("<敏感字段 %d 字>", len(fmt.Sprintf("%v", v)))
	}
	return fmt.Sprintf("%v", v)
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

// KVKeyD1 / KVKeyRules / KVKeyLongShort 把账号级 KV 的键名暴露给服务端代配通道。
// §0929CFG-HIST：包外**不许再自己拼一份键名串**——键名一旦在两处字面存在，改一边就静默分叉，
// 而分叉的表现为"快照留了、值没留"（等于没留）。代配路径要点名落哪本账，一律经这三个出口。
// English: the only sanctioned way for callers outside this package to name an account KV key.
func KVKeyD1() string { return perUserD1Key }

// KVKeyRules 返回账号级主配置（战法参数/LLM 等字段所在的文档）的 KV 键名。
func KVKeyRules() string { return perUserKey }

// KVKeyLongShort 返回账号级多空开关的 KV 键名。
func KVKeyLongShort() string { return perUserLongShortKey }

// EffectiveAccountDoc 返回指定账号某个 KV 键当前生效文档的原始字节（写前/写后 diff 取数口）。
// English: current effective bytes of one account KV document (the diff operand).
func EffectiveAccountDoc(m *Manager, userID, key string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("配置管理器为空")
	}
	return m.effectiveAccountValue(userID, key)
}

// ResolveAccountSnapshotKey 由快照文件名反推它属于账号级的哪本 KV 账（只认三种历史前缀）。
// 返回 (消毒后的文件名, KV 键, error)；前缀不认识时返回 error——回滚必须有确定落点，
// 拿一个不认识的文件去"猜该写哪个键"会把一份 D1 快照灌进主配置文档。
// English: derives which account KV ledger a snapshot belongs to; unknown prefixes error out.
func ResolveAccountSnapshotKey(ts string) (string, string, error) {
	name := strings.TrimSuffix(sanitizeHistorySegment(ts), ".json")
	key, ok := accountKeyFromHistoryPrefix(name)
	if !ok {
		return "", "", fmt.Errorf("快照 %s 前缀无法判定落点", ts)
	}
	return name + ".json", key, nil
}

// AuditAccountWrite 账号级配置写后的统一审计出口：拿写前文档与当前文档做字段级 diff，
// 经 AuditRulesDiff 单入口落账，target 形如 "d1(account:<uid>)"——审计行自己说清改了谁的哪本账。
// 与 AuditConfigWrite 的分工：那条按"配置面"自动解析落点（设置页只关心自己改的配置），
// 本条由调用方**指名账号与键**（管理员代配别的账号时不能用 ownerOf 归并）。
// English: post-write audit for an explicitly named account KV key (admin proxy edits bypass ownerOf).
func AuditAccountWrite(m *Manager, actor, userID, key string, before []byte) {
	if m == nil {
		return
	}
	target := key + "(account:" + userID + ")"
	after, err := m.effectiveAccountValue(userID, key)
	if err != nil {
		AuditRulesDiff(m, actor, target, "unavailable")
		return
	}
	d := "unavailable"
	if len(before) > 0 {
		if dd, derr := DiffRules(before, after); derr == nil {
			d = dd
		}
	}
	AuditRulesDiff(m, actor, target, d)
}

// GetD1EffectiveSource 不在本文件——见 config.go 的同名方法（D1 读侧口径与写侧落点判定放一起）。

// configSurfaces §0929CFG-HIST：配置写通道的**单入口快照/审计包装**认识的表面名。
// 每条 HTTP 配置写通道都必须报出自己的表面名，快照才知道该翻哪本账；新增写通道却不在
// 这张表里 = 无快照写入（门禁 §106 的"每条配置写通道都要留痕"行为锁会把它拦下）。
// 名单与落账分组见 resolveWriteScope——加一个表面名就必须同时在那里登记它的账，
// 否则它会把快照翻到错的文档上（假留痕比无留痕更坏）。
// English: the registry of config surfaces understood by the single-entry snapshot wrapper;
// a new write channel missing here means an untracked write, which a behavior lock rejects.
var configSurfaces = map[string]bool{
	"d1": true, "strategy": true, "llm": true, "longshort": true,
	"scheduler": true, "qmt": true, "paper": true, "global": true,
}

// resolveWriteScope 判定"这次写入替换的是哪本文档"，返回 (账号, KV 键, 目标描述)。
// key=="" 表示落全局 config.json。**这张表必须与各 setter 的实际落点逐字同构**——快照翻错账
// 等于伪造留痕（回滚会把没被改过的那本滚回旧态、真正被改的原封不动），比没有快照更坏。
// 逐条对照的出处（09-29 全量审计批 §0929CFG-HIST 自查锤实）：
//   - d1：跟着运行时读侧走（D1WriteTargetFor），有账号覆盖就写覆盖键——写读同源是 §0929CFG-D1 的核心；
//   - strategy / llm / qmt / paper：字段住在账号级**主配置文档**（saveUserRules → perUserKey），
//     store 未注入或无归属账号时这些 setter 一律回退写全局文件；
//     其中 "qmt" 原先被错列在全局组：SetQMTConfigFor 在 store 在场时走 saveUserRules，于是这条
//     唯一"声称有快照"的通道快照的是全局账、写后 DiffRules 恒为"(无变更)"，审计行被那个判据
//     永久压掉——留的是假记录，分组按 setter 实际落点重写；
//   - longshort：SetLongShortConfigFor 写的是**独立 KV 键** perUserLongShortKey（整份 JSON 就是
//     LongShortConfig 本身，不是主配置文档里的一个字段），必须单独一档；曾与 strategy 并列在
//     perUserKey 组里，那会给多空开关配上战法文档的快照，正是要消灭的假留痕形态；
//   - scheduler / global：本来就写全局文件。
//
// English: resolves which document a write replaces, mirroring each setter exactly — d1 follows the
// runtime reader, strategy/llm/qmt/paper live in the per-user rules document, long-short lives in its
// own KV key, and scheduler/global live in config.json.
func (m *Manager) resolveWriteScope(userID, surface string) (oid string, key string, target string) {
	oid = m.ownerOf(userID)
	switch surface {
	case "d1":
		k := ""
		oid, k = m.D1WriteTargetFor(userID)
		if k != "" {
			return oid, k, "account:d1"
		}
		return oid, "", "global"
	case "longshort":
		if m.store == nil || oid == "" {
			return oid, "", "global"
		}
		return oid, perUserLongShortKey, "account:longshort"
	case "strategy", "llm", "qmt", "paper":
		if m.store == nil || oid == "" {
			return oid, "", "global"
		}
		return oid, perUserKey, "account:rules"
	default: // scheduler / global
		return oid, "", "global"
	}
}

// SnapshotBeforeWrite 配置写通道统一写前快照入口（§0929CFG-HIST 修法第 2 条）。
// 返回 (快照名, 目标描述, error)；目标描述同时进审计，让"改了哪本账"在 opslog 里可读。
// 调用方策略：快照失败**不阻断保存**（磁盘抖动时运维必须还能改配置），但必须把失败如实
// 记成告警+审计行——"没快照的写入"要留得下证据，而不是静默变成不可回滚的事故。
// English: the single pre-write snapshot entry for every config write channel. Snapshot failure
// does not block the save (operators must still be able to change config during a disk hiccup)
// but is audited as an explicit untracked write.
func SnapshotBeforeWrite(m *Manager, userID, surface string) (string, string, error) {
	if m == nil || !configSurfaces[surface] {
		return "", "", fmt.Errorf("未知配置面: %s", surface)
	}
	oid, key, target := m.resolveWriteScope(userID, surface)
	if key == "" {
		name, err := SnapshotRules(m)
		return name, target, err
	}
	name, err := SnapshotAccountKey(m, oid, key)
	return name, target, err
}

// EffectiveConfigDoc 返回某配置面**当前生效文档**的原始字节（写后 diff 的另一侧）。
// 与 SnapshotBeforeWrite 用同一个作用域解析口径，保证 diff 两侧同构、可比。
// English: current bytes of the document a surface writes into — the diff side of the same scope.
func EffectiveConfigDoc(m *Manager, userID, surface string) ([]byte, string, error) {
	if m == nil || !configSurfaces[surface] {
		return nil, "", fmt.Errorf("未知配置面: %s", surface)
	}
	oid, key, target := m.resolveWriteScope(userID, surface)
	if key == "" {
		b, err := RestoreRulesContentCurrent(m)
		return b, target, err
	}
	b, err := m.effectiveAccountValue(oid, key)
	return b, target, err
}

// AuditConfigWrite 配置写通道的统一审计出口：拿写前快照内容与本函数读的当前内容做字段级 diff，
// 经 AuditRulesDiff 单入口落账（§AUDIT-UNIFY 的账号账延伸——target 形如 "strategy(account:rules)"）。
// before 取不到时记 "unavailable"，与回滚那条路的既有姿势一致：宁可留一条残缺证据也不静默。
// English: unified post-write audit — field-level diff between the pre-write snapshot document and
// the current one, emitted through the single AuditRulesDiff entry point.
func AuditConfigWrite(m *Manager, actor, userID, surface, target string, before []byte) {
	if m == nil {
		return
	}
	after, _, err := EffectiveConfigDoc(m, userID, surface)
	if err != nil || len(after) == 0 {
		AuditRulesDiff(m, actor, surface+"("+target+")", "unavailable")
		return
	}
	d := "(无变更)"
	if len(before) > 0 {
		if dd, derr := DiffRules(before, after); derr == nil {
			d = dd
		}
	} else {
		d = "unavailable"
	}
	AuditRulesDiff(m, actor, surface+"("+target+")", d)
}

// WriteConfigFile 把给定字节原子写入 config.json（回滚恢复用；后续由 config.Watch 热重载生效）。
// §AUDIT-UNIFY（owner 裁决 2026-09-26「配置变更审计统一走包装」）：这里**不再自带审计行**——
// 本层拿不到真实操作者（旧版硬编 "admin" 且只有 "ok"、没有字段级 diff），审计统一交回
// 处理器层经 AuditRulesDiff 单入口落账；写手就是写手，别在写手里藏一本假账。
// English: atomically writes the given bytes to config.json (used by rollback; config.Watch applies
// it). Since §AUDIT-UNIFY this helper no longer emits its own audit line — the handler audits via the
// single entry point AuditRulesDiff with the real actor and a field-level diff.
func WriteConfigFile(m *Manager, b []byte) error {
	if m == nil || m.path == "" {
		return fmt.Errorf("配置路径为空")
	}
	return fileutil.AtomicWrite(m.path, b, 0o644)
}

// AuditRulesDiff 是 config.json **变更审计的唯一入口**（§AUDIT-UNIFY，owner 裁决 2026-09-26）。
// 旧形态硬编 actor="admin"（谁操作都记成 admin，等于没记）且全仓零生产调用——实盘配置那条路
// 各自直写 opslog、回滚那条路只留了句普通运行日志。现在各条路统一走这里：真实操作者进参、
// 变更面（target）点名是哪条路（如 "qmt"、"rollback:<快照ts>"）、diff 文本必须是字段级行。
// 门禁锁：本函数生产调用点 ≥2（元闸判红组成员），opslog.Audit("config_change") 直写不得复活。
// English: the single entry point for config.json change auditing; takes the real actor, the changed
// surface, and the field-level diff. Direct opslog.Audit writes on this kind are locked out.
func AuditRulesDiff(m *Manager, actor, target, diff string) {
	if m == nil {
		return
	}
	if strings.TrimSpace(actor) == "" {
		actor = "unknown" // 读数不可得也要留痕，绝不冒充 admin（§N-5 姿势同款）
	}
	opslog.Audit("config_change", actor, target, diff)
}
