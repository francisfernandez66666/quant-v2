// snapshot.go — §SIZING-EVO 影子落盘库（R1 结构化快照，向前积累）。
//
// 背景（R1，owner 已批准）：L1 反事实重算需要"历史信号在新配置下会拿到多少 scale"，
// 但 Decision 只落文本 SizingReasons、Signal 的 QualityWeight/FlowRatio* 是 json:"-"
// 不落盘、Regime 不随 Decision 落盘——输入不完整。处置：本库每轮把**结构化**的
// Regime+Signal 快照追加进自己的 JSONL（按日一分文件），向前积累，攒够 MinSamples
// 再进化；历史段（本库上线前）用可还原维度跑简化 L1（净流入率按信号日期回溯日线，
// QualityWeight 弃权，Regime 部分维度弃权）。
//
// 纪律：
//   - 影子库与战法库完全隔离（铁律 2）：自己的目录、自己的格式，不读 applied_rules /
//     候选库 / 灰度库任何一个字节；
//   - 追加只增不改（Settle 回填前向收益是唯一写路径，且整日原子重写）；
//   - 缺数不当 0：未结算条目 FwdRet 保持缺（指针 nil），绝不做假收益；
//   - Prune 防无界增长（保留最近 keepDays 个交易日文件）。
//
// English: the sizing_evo shadow store — per-day JSONL of structured Regime+Signal
// snapshots, forward-accumulated; the only mutation is settling forward returns.
package sizing_evo

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/signalctl"
)

// ForwardDays 前向收益 horizon（交易日口径由调用方按日历裁剪；5 与信号质量前瞻窗同口径）。
// English: forward-return horizon in days (5, same as the signal-quality forward window).
const ForwardDays = 5

// evoPruneKeepDays 影子库默认保留交易日文件数（约半年）。
// English: default retention for the shadow store (~half a year of day files).
const evoPruneKeepDays = 120

// SignalProfile 复现 scale 所需的信号侧字段（显式落盘——与 Signal 上 json:"-" 互补）。
// 这是 ComputeSizing 个股组因子的全部输入：信号强度/波动率/质量/LLM利空/两个净流入率。
// English: the signal-side inputs needed to recompute a scale (explicitly persisted).
type SignalProfile struct {
	Confidence     float64 `json:"confidence"`
	Price          float64 `json:"price,omitempty"`
	ATR            float64 `json:"atr,omitempty"`
	QualityWeight  float64 `json:"quality_weight,omitempty"`   // 0=未解析/历史段弃权（R1）
	D1Blocked      bool    `json:"d1_blocked,omitempty"`       // LLM 利空（llmFactor）
	FlowRatio5D    float64 `json:"flow_ratio_5d,omitempty"`    // 0=弃权（§FLOW-FACTOR）
	FlowRatioToday float64 `json:"flow_ratio_today,omitempty"` // 0=弃权（当日=收盘值近似，R2）
}

// ToSignal 重建供 signalctl.ComputeSizing 消费的信号（只有画像字段，无展示字段）。
// English: rebuilds the signal consumed by ComputeSizing (profile fields only).
func (p SignalProfile) ToSignal(code string) combat_agent.Signal {
	return combat_agent.Signal{
		Code: code, Price: p.Price, ATR: p.ATR, Confidence: p.Confidence,
		QualityWeight: p.QualityWeight, D1Blocked: p.D1Blocked,
		FlowRatio5D: p.FlowRatio5D, FlowRatioToday: p.FlowRatioToday,
	}
}

// SizingSnapshot 一轮定权的结构化快照（L1 的最小训练单元）。
//
// Regime 直接复用 signalctl.Regime（本包允许 import signalctl——ComputeSizing 正是
// 被优化对象）；CfgVer 是生效配置的定位戳（账号+配置哈希，由调用方填），供反事实
// 重算时区分"当时用的哪套配置"——快照本身不存整包配置（寻优是在**候选**配置下重算，
// 不是复读当时配置）。
// English: one round's structured sizing snapshot — the minimal L1 training unit.
type SizingSnapshot struct {
	Day       string           `json:"day"`     // 北京交易日 YYYYMMDD
	At        time.Time        `json:"at"`      // 定权时刻
	Channel   string           `json:"channel"` // live / paper
	Account   string           `json:"account"` // 账号（R7 首期全局单回路，仍留痕归因）
	Code      string           `json:"code"`    // 纯数字代码
	Sig       SignalProfile    `json:"sig"`     // 信号画像
	Regime    signalctl.Regime `json:"regime"`  // 市场环境快照（结构化）
	CfgVer    string           `json:"cfg_ver"` // 生效配置定位戳（账号+哈希）
	Scale     float64          `json:"scale"`   // 当时生效的 BuyScale（0=未定权/硬拦，不入样本）
	Reasons   []string         `json:"reasons,omitempty"`
	FwdRet    *float64         `json:"fwd_ret,omitempty"`    // 前向收益（结算后回填；nil=未结算）
	SettledAt *time.Time       `json:"settled_at,omitempty"` // 结算时刻
}

// Usable 是否可入 L1 样本：已结算 + 当时真定过权（Scale>0）。
// 硬拦降级（Downgrade → Scale=Floor）与未定权（0）都不进——前者没有"买入"这个事实，
// 后者没有加权信息；把两者当样本等于让进化器学习不存在的数据。
// English: usable as an L1 sample — settled and actually scaled (>0).
func (s SizingSnapshot) Usable() bool {
	return s.FwdRet != nil && s.Scale > 0
}

// ICSample 投影为 IC 截面观测（State 取 Regime.MarketState，R10 分层用）。
// English: projects the snapshot onto an IC cross-section observation.
func (s SizingSnapshot) ICSample() ICSample {
	ret := nan()
	if s.FwdRet != nil {
		ret = *s.FwdRet
	}
	return ICSample{Date: s.Day, Code: s.Code, Scale: s.Scale, Ret: ret, State: s.Regime.MarketState}
}

// SnapshotStore 影子落盘库（线程安全；按日 JSONL）。
// English: the per-day-JSONL shadow store (thread-safe).
type SnapshotStore struct {
	dir string
	mu  sync.Mutex
}

// NewSnapshotStore 建库（目录不存在则创建；空 dir = 禁用落盘，各写路径静默跳过）。
// English: creates the store (mkdir -p); an empty dir disables persistence.
func NewSnapshotStore(dir string) *SnapshotStore {
	if strings.TrimSpace(dir) == "" {
		return &SnapshotStore{}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// 建目录失败不 panic：影子库是旁路观测面，绝不阻断主流程（与 record 落盘同纪律）。
		return &SnapshotStore{dir: dir}
	}
	return &SnapshotStore{dir: dir}
}

// Dir 库目录（观测/测试用）。
func (s *SnapshotStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func (s *SnapshotStore) path(day string) string {
	return filepath.Join(s.dir, day+".jsonl")
}

// Append 追加一轮快照（按日文件 O_APPEND）。day 空按 At 所在北京日推导。
// English: appends snapshots to the day's JSONL file.
func (s *SnapshotStore) Append(day string, snaps ...SizingSnapshot) error {
	if s == nil || s.dir == "" || len(snaps) == 0 {
		return nil
	}
	if day == "" && len(snaps) > 0 {
		day = snaps[0].At.Format("20060102")
	}
	if day == "" {
		return errors.New("sizing_evo: append without day")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path(day), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sizing_evo: open %s: %w", day, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, sn := range snaps {
		b, err := json.Marshal(sn)
		if err != nil {
			return fmt.Errorf("sizing_evo: marshal snapshot: %w", err)
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	return w.Flush()
}

// Load 读取某日全部快照（文件不存在 = 空切片，非错误）。
// English: loads one day's snapshots (a missing file is an empty slice, not an error).
func (s *SnapshotStore) Load(day string) ([]SizingSnapshot, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(day)
}

// loadLocked 持锁读取（Settle 原子重写路径复用）。
func (s *SnapshotStore) loadLocked(day string) ([]SizingSnapshot, error) {
	f, err := os.Open(s.path(day))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sizing_evo: open %s: %w", day, err)
	}
	defer f.Close()
	var out []SizingSnapshot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 单行含 Regime 快照，放宽上限
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var sn SizingSnapshot
		if err := json.Unmarshal([]byte(line), &sn); err != nil {
			// 单行畸形不报废整日：跳过并继续（写面裂行的防御；与"缺一行即整体弃权"不同，
			// 那是上游数据纪律，这是本地库的容错读取）。
			continue
		}
		out = append(out, sn)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sizing_evo: scan %s: %w", day, err)
	}
	return out, nil
}

// Days 列出库内全部交易日（升序）。
// English: lists all trading days in the store (ascending).
func (s *SnapshotStore) Days() ([]string, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sizing_evo: readdir: %w", err)
	}
	var days []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		days = append(days, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	sort.Strings(days)
	return days, nil
}

// Settle 回填前向收益：把 (day, code) 下所有未结算条目的 FwdRet 置为 ret。
// 返回结算条数。整日原子重写（tmp+rename）——追加只增、结算是唯一改路径。
// English: settles forward returns for one (day, code); rewrites the day file atomically.
func (s *SnapshotStore) Settle(day, code string, ret float64, at time.Time) (int, error) {
	if s == nil || s.dir == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snaps, err := s.loadLocked(day)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range snaps {
		if snaps[i].Code != code || snaps[i].FwdRet != nil {
			continue
		}
		r := ret
		snaps[i].FwdRet = &r
		t := at
		snaps[i].SettledAt = &t
		n++
	}
	if n == 0 {
		return 0, nil // 无可结算条目：不重写文件（防空转）
	}
	if err := s.rewriteLocked(day, snaps); err != nil {
		return 0, err
	}
	return n, nil
}

// rewriteLocked 原子重写当日文件（tmp + rename）。
func (s *SnapshotStore) rewriteLocked(day string, snaps []SizingSnapshot) error {
	tmp := s.path(day) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sizing_evo: open tmp %s: %w", day, err)
	}
	w := bufio.NewWriter(f)
	for _, sn := range snaps {
		b, err := json.Marshal(sn)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("sizing_evo: marshal snapshot: %w", err)
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sizing_evo: flush tmp %s: %w", day, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sizing_evo: close tmp %s: %w", day, err)
	}
	if err := os.Rename(tmp, s.path(day)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sizing_evo: rename %s: %w", day, err)
	}
	return nil
}

// Samples 汇总全部已结算且可用（Usable）的快照为 L1 样本（按日升序、日内保序）。
// English: flattens all usable (settled, scaled) snapshots into L1 samples.
func (s *SnapshotStore) Samples() ([]SizingSnapshot, error) {
	days, err := s.Days()
	if err != nil {
		return nil, err
	}
	var out []SizingSnapshot
	for _, d := range days {
		snaps, err := s.Load(d)
		if err != nil {
			return nil, err
		}
		for _, sn := range snaps {
			if sn.Usable() {
				out = append(out, sn)
			}
		}
	}
	return out, nil
}

// FlowRatios 抽取影子库里全部（或指定 day）快照的 5 日净流入率样本（FlowRatio5D != 0 才计入，
// 0 表示弃权中性不进分布）。用于 R4 分布标定——真实横截面净流入率样本。day 空=汇总全部交易日。
// 不写库、不触交易，纯读影子里程碑。English: extracts the cross-sectional 5D net-flow-rate sample
// (skips zero=abstain) for R4 band calibration; read-only.
func (s *SnapshotStore) FlowRatios(day string) ([]float64, error) {
	var days []string
	if day != "" {
		days = []string{day}
	} else {
		var err error
		days, err = s.Days()
		if err != nil {
			return nil, err
		}
	}
	var out []float64
	for _, d := range days {
		snaps, err := s.Load(d)
		if err != nil {
			return nil, err
		}
		for _, sn := range snaps {
			if sn.Sig.FlowRatio5D != 0 {
				out = append(out, sn.Sig.FlowRatio5D)
			}
		}
	}
	return out, nil
}

// ForensicHit R-08 取证单条：证明 annotations 已落因子值（非空 flow_ratio_*）。
// English: one R-08 forensic record — evidence that the sizing factor values were persisted.
type ForensicHit struct {
	Day            string  `json:"day"`              // 北京交易日
	Code           string  `json:"code"`             // 纯数字代码
	Channel        string  `json:"channel"`          // live / paper
	CfgVer         string  `json:"cfg_ver"`          // 生效配置定位戳
	FlowRatio5D    float64 `json:"flow_ratio_5d"`    // 5 日净流入率（因子值，注解落点）
	FlowRatioToday float64 `json:"flow_ratio_today"` // 当日净流入率
	Scale          float64 `json:"scale"`            // 当时生效 BuyScale（0=未定权/硬拦）
}

// ForensicRecords 扫描影子库前 N 条含因子值的记录（FlowRatio5D != 0），用于 R-08 取证。
// n<=0 取全部；day 空=全部交易日。只读，不写库。English: scans the first N snapshots that
// actually carry a factor value, confirming annotations landed factor values in the shadow store.
func (s *SnapshotStore) ForensicRecords(day string, n int) ([]ForensicHit, error) {
	var days []string
	if day != "" {
		days = []string{day}
	} else {
		var err error
		days, err = s.Days()
		if err != nil {
			return nil, err
		}
	}
	var out []ForensicHit
	for _, d := range days {
		snaps, err := s.Load(d)
		if err != nil {
			return nil, err
		}
		for _, sn := range snaps {
			if sn.Sig.FlowRatio5D == 0 {
				continue
			}
			out = append(out, ForensicHit{
				Day:            sn.Day,
				Code:           sn.Code,
				Channel:        sn.Channel,
				CfgVer:         sn.CfgVer,
				FlowRatio5D:    sn.Sig.FlowRatio5D,
				FlowRatioToday: sn.Sig.FlowRatioToday,
				Scale:          sn.Scale,
			})
			if n > 0 && len(out) >= n {
				return out, nil
			}
		}
	}
	return out, nil
}

// Counts 库规模观测：总快照数 / 已结算数 / 覆盖交易日数（opslog 与冷启动判定用）。
// English: store size stats — total, settled, and day count (cold-start gating).
type Counts struct {
	Total   int `json:"total"`
	Settled int `json:"settled"`
	Days    int `json:"days"`
}

// Counts 统计库规模（含未结算——冷启动看的是"攒够没有"，不是"结算完没有"）。
// English: counts all snapshots (cold start asks "enough accumulated?", not "settled?").
func (s *SnapshotStore) Counts() (Counts, error) {
	days, err := s.Days()
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	c.Days = len(days)
	for _, d := range days {
		snaps, err := s.Load(d)
		if err != nil {
			return c, err
		}
		for _, sn := range snaps {
			c.Total++
			if sn.FwdRet != nil {
				c.Settled++
			}
		}
	}
	return c, nil
}

// Prune 删除最旧的日文件，只保留最近 keepDays 个（<=0 用默认 120）。
// 返回删除文件数。影子库只增不删是唯一例外——防长跑进程无界增长。
// English: drops the oldest day files, keeping the most recent keepDays.
func (s *SnapshotStore) Prune(keepDays int) (int, error) {
	if s == nil || s.dir == "" {
		return 0, nil
	}
	if keepDays <= 0 {
		keepDays = evoPruneKeepDays
	}
	days, err := s.Days()
	if err != nil {
		return 0, err
	}
	if len(days) <= keepDays {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, d := range days[:len(days)-keepDays] {
		if err := os.Remove(s.path(d)); err != nil && !os.IsNotExist(err) {
			return n, fmt.Errorf("sizing_evo: prune %s: %w", d, err)
		}
		n++
	}
	return n, nil
}
