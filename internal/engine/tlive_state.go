// tlive_state.go — 实盘日内回转（正反 T）的状态账本：类型定义、原子持久化与查询面。
//
// 为什么执行层（§1.4 的目标仓位模型）必须先有它：`CurStrat` 的口径是
// 「Σ底仓市值 + Σ战略在途 − Σ正T买腿 + Σ反T卖腿」，T 腿既不能算进战略仓位（否则一笔日内低吸
// 会把目标空间吃掉、下一笔战略买入被误拦），也不能完全不算（否则打满仓的老缺陷换个入口复现）。
// 所以本文件先把"谁在途、多少钱"这件事做成可查的账，编排（开腿/配对/收摊）在同族的
// intraday_t_live.go 里消费同一份状态。
//
// 关键不变量：
//   - 落盘原子写（tmp+rename）+ 文件锁内读写，重启后必须先与 orders 表对账再消费（§8.6）；
//   - 跨日不复活：`Day` 与当日不一致的记录只用于审计，不参与在途查询；
//   - 幂等键 `t0:<code>:<dir>:<YYYYMMDD>:<seq>` 唯一，重复开腿必被拒；
//   - 查询面对上层暴露"净额"而不是内部结构，调用方不需要懂状态机。
//
// English: the live intraday round-trip ledger. The target-position model needs to know which
// exposure is a T leg (excluded from strategic CurStrat) before any orchestration exists, so the
// state, its atomic persistence and the query face live here.
package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TRoundLive 一笔实盘日内回转的完整状态（两腿 + 结局）。
type TRoundLive struct {
	// ID 幂等键 t0:<code>:<dir>:<YYYYMMDD>:<seq>。
	ID string `json:"id"`
	// Day 归属交易日（YYYYMMDD）；跨日不复活。
	Day string `json:"day"`
	// Code/Name 标的。
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
	// Dir "buy_first"＝正 T（先买后卖）/ "sell_first"＝反 T（先卖后买）。
	Dir string `json:"dir"`
	// Status idle 不会入库；open＝一腿在途；done/unwound/converted 为终态。
	Status string `json:"status"`
	// Qty 计划数量（股）。
	Qty int `json:"qty"`
	// BuyFilled/SellFilled 各腿**已成交**数量（对账后的真值，不是委托量）。
	BuyFilled int `json:"buy_filled"`
	// BuyPrice/SellPrice 各腿成交加权均价。
	BuyPrice float64 `json:"buy_price,omitempty"`
	// SellFilled 已成交卖出数量。
	SellFilled int     `json:"sell_filled"`
	SellPrice  float64 `json:"sell_price,omitempty"`
	// AnchorPrice 开腿时的卖出裁决锚点（T 腿不得改动它，仅留证）。
	AnchorPrice float64 `json:"anchor_price,omitempty"`
	// BaseQty 开腿时的底仓数量（单票 50% 帽与"卖后留 ≥50%"的复核基准）。
	BaseQty int `json:"base_qty,omitempty"`
	// FrozenCash 反 T 冻结回购资金（含费用缓冲）；正 T 为 0。
	FrozenCash float64 `json:"frozen_cash,omitempty"`
	// UsedSignalID 本次消费掉的买入信号 ID（§8.4.3 一次性消费；空＝反 T 或无信号腿）。
	UsedSignalID string `json:"used_signal_id,omitempty"`
	// VWAP/Vmin/DevPct/High/Low/AmplitudePct 开腿时刻的日内读数快照（灰度期复盘"发起点对不对"）。
	VWAP         float64 `json:"v,omitempty"`
	Vmin         float64 `json:"v_min,omitempty"`
	DevPct       float64 `json:"dev_pct,omitempty"`
	High         float64 `json:"high,omitempty"`
	Low          float64 `json:"low,omitempty"`
	AmplitudePct float64 `json:"amplitude_pct,omitempty"`
	AmountWan    float64 `json:"amount_wan,omitempty"`
	TurnoverPct  float64 `json:"turnover_pct,omitempty"`
	// MarketScaleAt 开腿时刻的 MarketScale（事后核"当时那道环境门是多少"——MS 每轮重算，
	// 不留在回转记录里就没人能回答"那天为什么开/不开"）。
	MarketScaleAt float64 `json:"market_scale_at,omitempty"`
	LastBlockStep string  `json:"last_block_step,omitempty"`
	// TPnlGross/TPnlNet/QMatched/FeeEst 做 T 收益（§8.9a）；仅 done 轮有值，不并入整仓成本。
	TPnlGross float64 `json:"t_pnl_gross,omitempty"`
	TPnlNet   float64 `json:"t_pnl_net,omitempty"`
	QMatched  int     `json:"q_matched,omitempty"`
	FeeEst    bool    `json:"t_fee_est,omitempty"`
	// UnwindReason 收摊/转换原因（loss_breaker / no_signal / insufficient_sellable / manual…）。
	UnwindReason string `json:"unwind_reason,omitempty"`
	// OpenedAt/UpdatedAt 时间戳（RFC3339）。
	OpenedAt string `json:"opened_at,omitempty"`
	// ClosedAt 终态时刻。
	ClosedAt string `json:"closed_at,omitempty"`
}

// tLiveState 单账号的实盘 T 账本（内存态 + 落盘）。
//
// Blocks/BlockDay 为什么也要落盘而不只留在内存：
//  1. 灰度期只看当日分布形状就会误判——上午 10:30 重启一次，内存计数清零，下午的读数会把
//     "上午拦了 40 次"抹掉，owner 据此放宽阈值等于对着半天的样本做决定；
//  2. 只读接口（/api/t-live/rounds）刻意读文件而不引入 engine 依赖（import 环），
//     内存态的分布对接口永远不可见，那 §8.9「为什么没做 T」就只剩日志一种出口。
//
// 两者都要求"当天的分布跨重启、跨进程边界仍然在"。
type tLiveState struct {
	Day      string         `json:"day"`
	Rounds   []*TRoundLive  `json:"rounds"`
	BlockDay string         `json:"block_day,omitempty"`
	Blocks   map[string]int `json:"blocks,omitempty"` // 键＝"step|code"，与纸面侧 tBlock 同形
}

// tLiveStore 按账号持有的 T 账本。锁刻意独立于 Engine.mu：
// 查询面会被目标仓位计算（每信号一次）与 T 编排（5s 一轮）同时调用，
// 挂进 Engine.mu 会把文件 IO 带进主锁临界区。
type tLiveStore struct {
	mu   sync.RWMutex
	path string // <dataDir>/accounts/<uid>/t_rounds_live.json
	byID map[string]*TRoundLive
	day  string
	// legacy 非当日的历史记录（跨日审计与收摊的落点）。
	// 为什么必须单独留一份而不是一删了之：`load` 只把当日记录放进 byID，而 `save` 写的是内存态——
	// 没有这个切片的话，当日第一次落账就会把昨日整段历史（含未收摊的 open 腿）从文件里抹掉，
	// §8.6「跨日记录必须显式留痕」当场失去载体，事后排查"仓位为什么少了一块"会没有出处。
	legacy []*TRoundLive
	// 被拦留痕（§8.9 观测形状）：step 编号 + 代码 → 次数，日志按 (step,code) 每日一次防刷屏。
	// 与纸面侧 tBlock 同构，刻意不落盘：它是"这一天的判定分布"，不是资金事实。
	blockDay    string
	blockCount  map[string]int
	blockLogged map[string]bool
	// ledgerLogged 每日总账的一行落点（按日一次性，跨日由 load 自然重置）。
	// 刻意只在内存：它防的是"同一轮/同一天的重复刷屏"，不是需要审计的事实本身。
	ledgerLogged bool
}

// dayLedger 当日回转的结局分布（做 T 每日总账的一行素材）：只数当日记录，历史留档不参与。
// English: today's round outcomes for the once-a-day ledger line.
func (s *tLiveStore) dayLedger() (opened, done, unwound, converted, openNow int, pnlNet, pnlGross float64) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.byID {
		if r == nil {
			continue
		}
		opened++
		switch r.Status {
		case "done":
			done++
			pnlNet += r.TPnlNet
			pnlGross += r.TPnlGross
		case "unwound":
			unwound++
		case "converted":
			converted++
		case "open":
			openNow++
		}
	}
	return opened, done, unwound, converted, openNow, pnlNet, pnlGross
}

// tLiveKeepLegacy 历史留档条数上限（账本体积；不影响判定）。
const tLiveKeepLegacy = 300

// tLiveRegistry 按账号管理 T 账本（引擎侧唯一的取用入口，避免各处各自 new 一份内存态）。
type tLiveRegistry struct {
	mu sync.Mutex
	// accountsRoot <dataDir>/accounts——状态文件按账号寻址，与其余账号级私有文件同根。
	accountsRoot string
	byUser       map[string]*tLiveStore
	loadDay      map[string]string
	nowFn        func() time.Time
	dayOfFn      func(time.Time) string
}

func newTLiveRegistry(accountsRoot string) *tLiveRegistry {
	return &tLiveRegistry{
		accountsRoot: accountsRoot,
		byUser:       map[string]*tLiveStore{},
		loadDay:      map[string]string{},
		nowFn:        time.Now,
		dayOfFn:      func(t time.Time) string { return t.Format("20060102") },
	}
}

// get 返回该账号当日的账本；跨日自动重读盘（昨日记录只留文件供审计，不进内存）。
func (rg *tLiveRegistry) get(userID string) *tLiveStore {
	if rg == nil || strings.TrimSpace(userID) == "" || rg.accountsRoot == "" {
		return nil
	}
	day := rg.dayOfFn(rg.nowFn())
	rg.mu.Lock()
	defer rg.mu.Unlock()
	st := rg.byUser[userID]
	if st == nil {
		st = newTLiveStore(rg.accountsRoot, userID)
		rg.byUser[userID] = st
	}
	if rg.loadDay[userID] != day {
		st.load(day)
		rg.loadDay[userID] = day
	}
	return st
}

// oppositeOpenLeg 跨账号同码反向在途查询（§8.4.5 对手方不变量，合规红线：对敲不申请、不做）。
//
// 自有账号组内，A 账号当日在同一 code 上挂着方向相反且未终态的 T 腿时，B 账号不许开腿——
// 两条反向腿在时间上重叠就是"自己的买单碰自己的卖单"的形态，柜台判反向单/自成交都由此起。
// 数据源按**文件**遍历而不是按内存 store：引擎按账号组共享，本进程的 registry 里未必装着
// 其它账号组的账本，而磁盘上的那一份一定是权威的。
//
// fail-closed 口径（缺读数不等于没冲突）：
//   - accountsRoot 空 / 目录读不到 / 某账号的状态文件解析失败 ⇒ 视为互斥命中；
//   - 状态文件**不存在** ⇒ 该账号当日没有回转（与 load 把缺文件当空账本起步同口径），不算命中。
//
// English: cross-account opposite-direction exclusion on the same code, read from each account's
// ledger file; any unreadable source counts as a hit (fail-closed), a missing file means no rounds.
func (rg *tLiveRegistry) oppositeOpenLeg(selfUID, code, dir, day string) (bool, string) {
	if rg == nil || strings.TrimSpace(rg.accountsRoot) == "" {
		return true, "跨账号回转账本根目录不可得，按互斥命中"
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(dir) == "" {
		return true, "互斥查询缺少代码或方向参数（按互斥命中）"
	}
	entries, err := os.ReadDir(rg.accountsRoot)
	if err != nil {
		return true, fmt.Sprintf("跨账号账本目录读取失败（按互斥命中）：%v", err)
	}
	opp := "sell_first"
	if dir == "sell_first" {
		opp = "buy_first"
	}
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		uid := en.Name()
		if uid == selfUID {
			continue // 本账号的反向在途腿由"两腿串行"那条不变量守，不跨文件查自己
		}
		path := filepath.Join(rg.accountsRoot, uid, "t_rounds_live.json")
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue // 该账号当日没有回转账本
			}
			return true, fmt.Sprintf("账号 %s 的回转状态读取失败（按互斥命中）：%v", uid, rerr)
		}
		if strings.TrimSpace(string(b)) == "" {
			continue
		}
		var st tLiveState
		if uerr := json.Unmarshal(b, &st); uerr != nil {
			return true, fmt.Sprintf("账号 %s 的回转状态解析失败（按互斥命中）：%v", uid, uerr)
		}
		for _, r := range st.Rounds {
			if r == nil || r.Status != "open" || r.Day != day {
				continue
			}
			if strings.EqualFold(r.Code, code) && r.Dir == opp {
				return true, fmt.Sprintf("账号 %s 当日在同码 %s 上有未终态的反向腿 %s（对手方不变量：不开腿）", uid, code, r.ID)
			}
		}
	}
	return false, ""
}

// newTLiveStore 账本文件落在 <dataDir>/accounts/<uid>/t_rounds_live.json（与 TStatePath 同根）。
func newTLiveStore(accountsRoot, userID string) *tLiveStore {
	if accountsRoot == "" || userID == "" {
		return nil
	}
	return &tLiveStore{
		path: filepath.Join(accountsRoot, userID, "t_rounds_live.json"),
		byID: map[string]*TRoundLive{},
	}
}

// load 读盘并只保留**当日**记录进内存；昨日及以前的记录留在文件里供审计与对账，
// 但绝不参与在途查询（跨日不复活是 §8.6 的硬要求）。
func (s *tLiveStore) load(day string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.day = day
	s.byID = map[string]*TRoundLive{}
	s.legacy = nil
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[t-live] 状态文件读取失败（按空账本起步，稍后与 orders 对账）：%v", err)
		}
		return
	}
	var st tLiveState
	if err := json.Unmarshal(b, &st); err != nil {
		// 状态文件损坏时**不静默清空**：改名留证后按空账本起步，避免"账本没了→在途腿被当不存在"
		// 直接导致重复开腿。留证文件由人工复核。
		backup := s.path + ".corrupt-" + time.Now().Format("20060102150405")
		if rerr := os.Rename(s.path, backup); rerr == nil {
			log.Printf("[t-live] 状态文件损坏，已留证 %s 并按空账本起步：%v", filepath.Base(backup), err)
		} else {
			log.Printf("[t-live] 状态文件损坏且留证失败，按空账本起步：%v", rerr)
		}
		return
	}
	for _, r := range st.Rounds {
		if r == nil || r.ID == "" {
			continue
		}
		if r.Day != day {
			// 跨日不复活：进留档集合，不参与在途查询，但必须继续写回文件（见 tLiveStore.legacy 注释）。
			s.legacy = append(s.legacy, r)
			continue
		}
		s.byID[r.ID] = r
	}
}

// save 原子写（tmp + rename）。失败如实返回并由调用方打日志——在途腿的状态丢不起。
func (s *tLiveStore) save() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	st := tLiveState{Day: s.day}
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		st.Rounds = append(st.Rounds, s.byID[id])
	}
	// 留档历史一并写回（跨日收摊的痕迹是唯一能解释"仓位为什么少了一块"的出处）。
	for _, r := range s.legacy {
		if r != nil && r.ID != "" {
			st.Rounds = append(st.Rounds, r)
		}
	}
	st.BlockDay = s.blockDay
	st.Blocks = make(map[string]int, len(s.blockCount))
	for k, v := range s.blockCount {
		st.Blocks[k] = v
	}
	s.mu.RUnlock()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("建目录失败: %w", err)
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("原子替换失败: %w", err)
	}
	return nil
}

// put 登记/更新一条回转（调用方负责只在状态真实变化时调用）。
// 非当日的记录一律进留档集合而不是当日账本：否则一条昨日 open 会被今天的在途查询/净额口径看见，
// 跨日不复活这条硬要求就从"注释"漏成了"依赖调用方自觉"。
func (s *tLiveStore) put(r *TRoundLive) error {
	if s == nil || r == nil || r.ID == "" {
		return nil
	}
	s.mu.Lock()
	if s.day != "" && r.Day != s.day {
		s.upsertLegacyLocked(r)
		s.mu.Unlock()
		return s.save()
	}
	s.byID[r.ID] = r
	if s.day == "" {
		s.day = r.Day
	}
	s.mu.Unlock()
	return s.save()
}

// upsertLegacyLocked 在留档集合里按 ID 覆盖或追加（须持锁调用）。
func (s *tLiveStore) upsertLegacyLocked(r *TRoundLive) {
	for i, x := range s.legacy {
		if x != nil && x.ID == r.ID {
			s.legacy[i] = r
			return
		}
	}
	s.legacy = append(s.legacy, r)
	if len(s.legacy) > tLiveKeepLegacy {
		s.legacy = append([]*TRoundLive(nil), s.legacy[len(s.legacy)-tLiveKeepLegacy:]...)
	}
}

// staleOpenRounds 非当日且仍为 open 的记录（跨日收敛的入口；返回指针，收摊后就地改状态）。
func (s *tLiveStore) staleOpenRounds() []*TRoundLive {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	var out []*TRoundLive
	for _, r := range s.legacy {
		if r != nil && r.Status == "open" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	s.mu.RUnlock()
	return out
}

// sealStale 把一条跨日记录写回留档集合并落盘（不进当日内存账本）。
func (s *tLiveStore) sealStale(r *TRoundLive) error {
	if s == nil || r == nil || r.ID == "" {
		return nil
	}
	s.mu.Lock()
	s.upsertLegacyLocked(r)
	s.mu.Unlock()
	return s.save()
}

// openRoundsForCode 该票当日未终态的回转（0 或 1 条——两腿串行的结构性保证）。
func (s *tLiveStore) openRoundsForCode(code string) *TRoundLive {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.byID {
		if r.Status == "open" && strings.EqualFold(r.Code, code) {
			return r
		}
	}
	return nil
}

// hasOpenRound 该票当日是否已有在途腿（开腿前的双保险复查）。
func (s *tLiveStore) hasOpenRound(code string) bool { return s.openRoundsForCode(code) != nil }

// sellFirstNotional 当日反 T 卖腿已占用的市值合计（总量帽分子，§8.4.5 / 六轮 T6-2）。
// 用**成交**量×成交均价而不是计划量：帽约束的是"今天真的卖掉多少底仓"，
// 按委托量计会让一条被撤的卖腿永久占着额度。
func (s *tLiveStore) sellFirstNotional(day string) float64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sum := 0.0
	for _, r := range s.byID {
		if r.Day != day || r.Dir != "sell_first" || r.SellFilled <= 0 {
			continue
		}
		sum += float64(r.SellFilled) * r.SellPrice
	}
	return sum
}

// noteBlock 记一条"被哪一步拦"：日志按 (step,code) 每日一次防刷屏（T 驱动约 5s 一轮），
// 计数每次累加——灰度期看的是分布：step2 最多说明次数帽太紧，step4 最多说明行情不给振幅。
func (s *tLiveStore) noteBlock(step, why, code string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.blockCount == nil {
		s.blockCount = map[string]int{}
		s.blockLogged = map[string]bool{}
	}
	if s.blockDay != s.day {
		s.blockDay = s.day
		s.blockCount = map[string]int{}
		s.blockLogged = map[string]bool{}
	}
	key := step + "|" + code
	s.blockCount[key]++
	logged := s.blockLogged[key]
	s.blockLogged[key] = true
	s.mu.Unlock()
	if logged {
		return
	}
	if code == "" {
		code = "-"
	}
	log.Printf("[t-live] 不开腿 %s 拦在 %s：%s", code, step, why)
	// 每个 (step,code) 第一次出现时落一次盘。为什么必须落：全天一条腿都没开成功是最常见的形态，
	// 而 save 只在回转变化时被调用——不落这一笔，文件里就只有"空账本"，
	// 灰度期看板的 step 分布会整块消失，事后无法回答"那天到底卡在哪一步"。
	if err := s.save(); err != nil {
		log.Printf("[t-live] 被拦分布落盘失败（计数仍在内存，本轮判定不受影响）：%v", err)
	}
}

// blockStats 当日"被哪一步拦"的计数快照（只读接口与面板用）。
func (s *tLiveStore) blockStats() (string, map[string]int) {
	if s == nil {
		return "", map[string]int{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int, len(s.blockCount))
	for k, v := range s.blockCount {
		out[k] = v
	}
	return s.blockDay, out
}

// allRounds 当日账本 + 留档历史的只读副本（排障接口按 t0: 前缀逐笔核对用）。
func (s *tLiveStore) allRounds() []*TRoundLive {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*TRoundLive, 0, len(s.byID)+len(s.legacy))
	for _, r := range s.byID {
		cp := *r
		out = append(out, &cp)
	}
	for _, r := range s.legacy {
		if r == nil {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// openRounds 返回当日**未终态**的回转（目标仓位与编排共用）。
func (s *tLiveStore) openRounds() []*TRoundLive {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*TRoundLive
	for _, r := range s.byID {
		if r.Status == "open" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// tLiveBook 目标仓位模型要的"T 腿净额"摘要。
type tLiveBook struct {
	// BuyLegValue 已成交的正 T 买腿市值（战略仓位要**减去**它）。
	BuyLegValue float64
	// SellLegValue 已成交的反 T 卖腿市值（战略仓位要**加回**它：那部分底仓还欠着买回）。
	SellLegValue float64
	// FrozenCash 反 T 冻结回购资金合计（战略可用现金要扣除）。
	FrozenCash float64
	// OpenCount 当日未终态回转数（日志与面板用）。
	OpenCount int
}

// bookValue 用当前价把在途 T 腿折成市值。prices 由调用方按同一轮行情传入，
// 缺价时按开腿价估（不伪造为 0——0 会让战略空间被高估，方向危险）。
func (s *tLiveStore) bookValue(prices map[string]float64) tLiveBook {
	var b tLiveBook
	for _, r := range s.openRounds() {
		b.OpenCount++
		px := 0.0
		if prices != nil {
			px = prices[r.Code]
		}
		if r.Dir == "buy_first" {
			v := float64(r.BuyFilled) * px
			if px <= 0 {
				v = float64(r.BuyFilled) * r.BuyPrice
			}
			b.BuyLegValue += v
		} else {
			v := float64(r.SellFilled) * px
			if px <= 0 {
				v = float64(r.SellFilled) * r.SellPrice
			}
			b.SellLegValue += v
			b.FrozenCash += r.FrozenCash
		}
	}
	if math.IsNaN(b.BuyLegValue) {
		b.BuyLegValue = 0
	}
	if math.IsNaN(b.SellLegValue) {
		b.SellLegValue = 0
	}
	return b
}

// tRoundCountFor 该码当日已占用（open 或终态）的回转组数——防抖主力层（§8.4.3 ③）。
// 口径与方案一致：**一组完整的反向动作算一次**，未终态的在途腿同样占额度。
func (s *tLiveStore) tRoundCountFor(code string) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, r := range s.byID {
		if strings.EqualFold(r.Code, code) {
			n++
		}
	}
	return n
}

// lastRoundEndedAtOf 该码上一次回转结束时刻（候选 B 的信号新鲜度基准，§8.4.2）。
// 当日无历史回转 → 返回当日 09:30（＝"今天新出的信号才算数"）。
func (s *tLiveStore) lastRoundEndedAtOf(code string, dayStart time.Time) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	best := dayStart
	for _, r := range s.byID {
		if !strings.EqualFold(r.Code, code) || r.Status == "open" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, r.ClosedAt); err == nil && t.After(best) {
			best = t
		}
	}
	return best
}

// signalConsumed 某个 signal_id 是否已被买入腿消费（§8.4.3 ①）。
func (s *tLiveStore) signalConsumed(signalID string) bool {
	if s == nil || strings.TrimSpace(signalID) == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.byID {
		if r.UsedSignalID != "" && r.UsedSignalID == signalID {
			return true
		}
	}
	return false
}

// dayPnL 当日做 T 收益聚合（§8.9a）：只统计 done 轮，converted/unwound 一律不计。
func (s *tLiveStore) dayPnL() (net, gross float64, doneRounds int, feeEstShare float64) {
	if s == nil {
		return 0, 0, 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	estCnt := 0
	for _, r := range s.byID {
		if r.Status != "done" {
			continue
		}
		doneRounds++
		net += r.TPnlNet
		gross += r.TPnlGross
		if r.FeeEst {
			estCnt++
		}
	}
	if doneRounds > 0 {
		feeEstShare = float64(estCnt) / float64(doneRounds)
	}
	return net, gross, doneRounds, feeEstShare
}
