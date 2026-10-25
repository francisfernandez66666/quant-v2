// state.go — §SIZING-EVO 生命周期状态库（灰度 / 已驳回 / 生效 / 降级记录）。
//
// 独立性铁律（§2.7.0）：本库是 sizing_evo 自己的状态文件（单 JSON、原子重写），
// 与战法库（applied_rules.json / 灰度库 / 候选库）零共享——不读、不写、不参照。
// 晋升落地形态（§2.7.3）：候选配置写入 Applied（生效条目），引擎侧按 TTL 读取热加载；
// auto_promote=false 时候选只进 Grayscale 留痕，不进 Applied。
//
// 驳回去重：Rejected 存 ComboKey 指纹集合（与 candidate.Dedup 同口径），同组合
// 被驳回后不再重复进灰度——"进化不能把同一个错误每周重犯"。
//
// English: the sizing_evo lifecycle state store — grayscale / rejected / applied /
// demotion records, in its own atomically-rewritten JSON file (zero tactic-library sharing).
package sizing_evo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// evoStateFile 状态文件名（单文件，原子重写）。
// English: the state file name (single file, atomically rewritten).
const evoStateFile = "evo_state.json"

// CfgVerOf 生效配置定位戳：账号 + "#" + 候选 ID（无生效候选时传 "base"）。
// 快照的 CfgVer 用它写，反事实重算/灰度/降级都按它归组。
// English: config-version stamp — account + "#" + candidate ID ("base" when none applied).
func CfgVerOf(account, candID string) string {
	if strings.TrimSpace(candID) == "" {
		candID = "base"
	}
	if strings.TrimSpace(account) == "" {
		account = "global"
	}
	return account + "#" + candID
}

// GrayscaleEntry 一条灰度观测中的候选（shadow 通道照算留痕，N 交易日后判晋升/回拒）。
// English: one candidate under grayscale observation (shadow-channel snapshots).
type GrayscaleEntry struct {
	CandID    string             `json:"cand_id"`
	Params    map[string]float64 `json:"params"`     // 相对基线的参数覆盖（Apply 用）
	EnteredAt string             `json:"entered_at"` // 进入灰度日 YYYYMMDD
	Note      string             `json:"note,omitempty"`
}

// AppliedEntry 当前生效的进化配置（nil=出厂默认）。晋升落地的唯一位置。
// English: the currently applied evolution config (nil = factory default).
type AppliedEntry struct {
	CandID    string             `json:"cand_id"`
	Params    map[string]float64 `json:"params"`
	AppliedAt string             `json:"applied_at"` // YYYYMMDD
	CfgVer    string             `json:"cfg_ver"`    // = CfgVerOf(account, CandID)
	Auto      bool               `json:"auto"`       // true=自动晋升；false=人工审批晋升
	Account   string             `json:"account,omitempty"`
}

// CandidateOf 还原为候选（灰度/生效条目 → Candidate，供 Apply 重放）。
// English: rebuilds the Candidate for replay through Apply.
func (e GrayscaleEntry) CandidateOf() Candidate {
	return Candidate{ID: e.CandID, Origin: "grayscale", Params: cloneParams(e.Params)}
}

// CandidateOf 还原为候选（生效条目 → Candidate）。
// English: rebuilds the Candidate for the applied entry.
func (e AppliedEntry) CandidateOf() Candidate {
	return Candidate{ID: e.CandID, Origin: "applied", Params: cloneParams(e.Params)}
}

// DemoteRecord 一条降级历史（留痕：谁、何时、为何被回默认）。
// English: one demotion record (audit trail: who, when, why reverted to default).
type DemoteRecord struct {
	CandID string `json:"cand_id"`
	At     string `json:"at"` // YYYYMMDD
	Reason string `json:"reason"`
}

// EvoState 进化回路生命周期状态（单文件内容）。
// English: the evolution loop's lifecycle state (single-file content).
type EvoState struct {
	Version   int              `json:"version"`
	Grayscale []GrayscaleEntry `json:"grayscale,omitempty"`
	Rejected  [][]string       `json:"rejected,omitempty"` // ComboKey 指纹集合
	Applied   *AppliedEntry    `json:"applied,omitempty"`  // nil=出厂默认
	Demoted   []DemoteRecord   `json:"demoted,omitempty"`
}

// evoStateVersion 状态结构版本（结构变更时递增，旧文件按缺省兼容读）。
// English: state schema version (bump on breaking changes; old files read leniently).
const evoStateVersion = 1

// EvoStateStore 生命周期状态库（线程安全；单文件原子重写）。
// English: the lifecycle state store (thread-safe; single-file atomic rewrite).
type EvoStateStore struct {
	dir string
	mu  sync.Mutex
}

// NewEvoStateStore 建库（空 dir = 纯内存禁用：各写路径静默跳过，读返回空状态）。
// English: creates the store; an empty dir disables persistence (in-memory only).
func NewEvoStateStore(dir string) *EvoStateStore {
	if strings.TrimSpace(dir) == "" {
		return &EvoStateStore{}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &EvoStateStore{dir: dir} // 建目录失败不 panic：旁路库绝不阻断主流程
	}
	return &EvoStateStore{dir: dir}
}

// Dir 库目录（观测/测试用）。
// English: the store directory (observability/tests).
func (s *EvoStateStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func (s *EvoStateStore) path() string { return filepath.Join(s.dir, evoStateFile) }

// Load 读状态（缺文件/空 dir = 空状态，非错误）。
// English: loads the state (missing file or empty dir = empty state, not an error).
func (s *EvoStateStore) Load() (*EvoState, error) {
	if s == nil || s.dir == "" {
		return &EvoState{Version: evoStateVersion}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked 持锁读取（mutate 的 load-modify-save 骨架复用）。
// English: locked read (shared by the mutate skeleton).
func (s *EvoStateStore) loadLocked() (*EvoState, error) {
	st := &EvoState{Version: evoStateVersion}
	b, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return st, nil
	}
	var disk EvoState
	if err := json.Unmarshal(b, &disk); err != nil {
		// 状态文件畸形不覆盖：返回空状态让上层重写（丢一次生命周期记录的代价 < 卡死）。
		return st, nil
	}
	if disk.Version == 0 {
		disk.Version = evoStateVersion
	}
	return &disk, nil
}

// Save 原子写状态（tmp + rename）。
// English: atomically saves the state (tmp + rename).
func (s *EvoStateStore) Save(st *EvoState) error {
	if s == nil || s.dir == "" || st == nil {
		return nil
	}
	if st.Version == 0 {
		st.Version = evoStateVersion
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// mutate 持锁 load-modify-save（所有变更方法的统一骨架，防丢更新）。
// English: locked load-modify-save skeleton shared by all mutators (no lost updates).
func (s *EvoStateStore) mutate(fn func(st *EvoState) error) error {
	if s == nil || s.dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadLocked()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// AddGrayscale 加/更新一条灰度候选（同 CandID 已存在则更新参数与备注）。
// English: adds or updates a grayscale candidate (same ID updates params/note).
func (s *EvoStateStore) AddGrayscale(e GrayscaleEntry) error {
	return s.mutate(func(st *EvoState) error {
		for i := range st.Grayscale {
			if st.Grayscale[i].CandID == e.CandID {
				st.Grayscale[i].Params = cloneParams(e.Params)
				st.Grayscale[i].Note = e.Note
				if st.Grayscale[i].EnteredAt == "" {
					st.Grayscale[i].EnteredAt = e.EnteredAt
				}
				return nil
			}
		}
		st.Grayscale = append(st.Grayscale, e)
		return nil
	})
}

// RemoveGrayscale 移除一条灰度候选（晋升/回拒时调用；不存在=幂等成功）。
// English: removes a grayscale candidate (on promote/reject; idempotent).
func (s *EvoStateStore) RemoveGrayscale(candID string) error {
	return s.mutate(func(st *EvoState) error {
		out := st.Grayscale[:0]
		for _, g := range st.Grayscale {
			if g.CandID != candID {
				out = append(out, g)
			}
		}
		st.Grayscale = out
		return nil
	})
}

// GrayscaleCombos 当前灰度中的组合指纹集合（L1 去重：已在观测的候选不重复进）。
// English: combo fingerprints currently in grayscale (L1 dedup).
func (s *EvoStateStore) GrayscaleCombos() [][]string {
	st, err := s.Load()
	if err != nil {
		return nil
	}
	out := make([][]string, 0, len(st.Grayscale))
	for _, g := range st.Grayscale {
		out = append(out, ComboKey(g.Params))
	}
	return out
}

// RejectedCombos 已驳回组合指纹集合（L1 生成后剔除 + 灰度晋升评估去重）。
// English: rejected combo fingerprints (dropped from L1 generation).
func (s *EvoStateStore) RejectedCombos() [][]string {
	st, err := s.Load()
	if err != nil {
		return nil
	}
	return st.Rejected
}

// Reject 把组合指纹追加进已驳回集（去重）。
// English: appends a combo fingerprint to the rejected set (deduped).
func (s *EvoStateStore) Reject(fp []string) error {
	return s.mutate(func(st *EvoState) error {
		for _, e := range st.Rejected {
			if comboEqual(fp, e) {
				return nil
			}
		}
		st.Rejected = append(st.Rejected, fp)
		return nil
	})
}

// SetApplied 写入生效配置（nil=回默认）。晋升与人工审批的唯一落点。
// English: sets the applied config (nil = back to default); the single promotion landing.
func (s *EvoStateStore) SetApplied(e *AppliedEntry) error {
	return s.mutate(func(st *EvoState) error {
		st.Applied = e
		return nil
	})
}

// AppliedOf 当前生效条目（nil=出厂默认）。
// English: the current applied entry (nil = factory default).
func (s *EvoStateStore) AppliedOf() *AppliedEntry {
	st, err := s.Load()
	if err != nil {
		return nil
	}
	return st.Applied
}

// AddDemote 追加一条降级记录（留痕；保留最近 50 条防无界）。
// English: appends a demotion record (kept to the most recent 50).
func (s *EvoStateStore) AddDemote(r DemoteRecord) error {
	return s.mutate(func(st *EvoState) error {
		st.Demoted = append(st.Demoted, r)
		if len(st.Demoted) > 50 {
			st.Demoted = st.Demoted[len(st.Demoted)-50:]
		}
		return nil
	})
}
