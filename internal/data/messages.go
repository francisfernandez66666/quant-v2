// Package data — 消息中心持久化存储。
// 按稳定键（ID）去重合并消息，支持删除墓碑（当日不再自动出现）与跨交易日清理。
//
// §B6（2026-11-05 通知增强）新增「新闻」通道（Kind=news）：只留当日、跨交易日即清，
// 日切判据**复用本存储既有的 day-key（TradingDayDate 那一处）**，不另起第二套日界线——
// 本仓 cntime 与本地钟两套日界线反复出事，这里刻意只留一处判据。
package data

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// KindNews §B6：消息中心「新闻」tab 的消息种类（利多且未命中持仓/自选的事件进这里，
// 不占必推通道、也不混进信号流）。
// English: §B6 — the message-center "news" tab kind: bullish items that miss the
// holdings/watchlist land here, taking neither the must-push lane nor the signal stream.
const KindNews = "news"

// MessageItem 消息中心单条消息（与前端 /api/alerts 结构一致）。
// ID 为稳定去重键（如 600519@止盈 / hold@SIGxxx），供手工删除定位。
// §GAP2-W2 账户隔离：Scope 标记消息可见范围——""=公共（交易信号等全账号共享），
// 非空=仅该 userID 可见（持仓提示/止盈止损等由个人持仓派生的私有消息，
// 私有消息 ID 统一带 "u<uid>|" 前缀避免跨账号去重键碰撞）。
// English: §GAP2-W2 account isolation — Scope marks visibility: ""=public (trade signals shared by
// all accounts), non-empty=visible only to that userID (private position-derived alerts; private IDs
// carry a "u<uid>|" prefix to avoid cross-account dedup-key collisions).
type MessageItem struct {
	ID          string    `json:"id"`              // 稳定去重键
	Code        string    `json:"code"`            // 股票代码
	Name        string    `json:"name"`            // 股票名称
	Level       string    `json:"level"`           // 级别（如 止盈/止损/提示）
	Action      string    `json:"action"`          // 动作
	Strategy    string    `json:"strategy"`        // 关联策略
	Time        string    `json:"time"`            // 触发时间字符串
	Title       string    `json:"title"`           // 标题
	Body        string    `json:"body"`            // 正文
	Direction   string    `json:"direction"`       // 方向（利好/利空）
	GeneratedAt time.Time `json:"generated_at"`    // 生成时间
	Scope       string    `json:"scope,omitempty"` // 可见范围（""=公共；uid=私有）
	// Kind §B6 消息种类：空串=既有消息（信号流/持仓提示等，前端按 level 分类），
	// "news"=「新闻」tab 的当日条目。读接口按 ?kind=news 分流，两类互不混排。
	// English: §B6 message kind — empty keeps today's behaviour, "news" marks a news-tab row.
	Kind string `json:"kind,omitempty"`
}

// messageFile 消息中心持久化文件结构。
// TradingDay 记录当前交易日；Messages 为消息列表；DeletedKeys 为当日删除墓碑。
type messageFile struct {
	TradingDay  string        `json:"trading_day"`  // 当前交易日
	Messages    []MessageItem `json:"messages"`     // 消息列表
	DeletedKeys []string      `json:"deleted_keys"` // 当日删除墓碑
}

// MessageStore 消息中心持久化存储。
// 按稳定键（ID）去重合并；已删除键记录墓碑（当日内不再自动出现），跨交易日自动清除墓碑。
type MessageStore struct {
	mu   sync.Mutex // 保护 file 的并发读写
	path string     // 持久化文件路径
	file messageFile
}

// NewMessageStore 创建消息存储并加载本地文件；跨交易日清除墓碑。
func NewMessageStore(path string) *MessageStore {
	s := &MessageStore{path: path}
	if path == "" {
		return s
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &s.file); err != nil {
			log.Printf("[messages] 解析失败: %v", err)
			s.file = messageFile{}
		}
	}
	if s.file.TradingDay != "" {
		// §B6：与 Sync/读面共用同一处日切判据（内存存储也要按日清新闻行）
		// English: §B6 — load shares the single rollover judgement with Sync and the read paths.
		s.rolloverLocked(time.Now())
	}
	return s
}

// persist 将当前消息状态序列化并写入本地文件。
// 更新 TradingDay 为当前交易日；path 为空或写文件失败时记录日志并跳过。
func (s *MessageStore) persist() {
	if s.path == "" {
		return
	}
	s.file.TradingDay = TradingDayDate(time.Now())
	raw, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWrite(s.path, raw, 0644); err != nil {
		log.Printf("[messages] 写入失败: %v", err)
	}
}

// rolloverLocked 交易日切换时的**唯一** day-key 判据（调用方须已持有 s.mu）：
// 墓碑清零 + 「新闻」tab 行清零。两处必须同源——新闻只留当日是 §5A.3b 的硬要求，
// 而另起一套"新闻日切"判据正是本仓两套日界线（cntime vs 本地钟）反复出事的形状。
// 返回是否真的发生了切换（调用方据此决定要不要落盘）。
// English: the single trading-day rollover judgement (caller must hold s.mu): tombstones and
// news-tab rows both clear here, so the news tab never grows its own second day-line.
func (s *MessageStore) rolloverLocked(now time.Time) bool {
	td := TradingDayDate(now)
	if s.file.TradingDay == td {
		return false
	}
	// 先把日推进去再清理：path="" 的内存存储不走 persist，不自己推进的话会每次调用都判成"刚换日"，
	// 把当日墓碑清干净（等价于删掉的新闻又复活）。
	// English: advance the stored day here: in-memory stores never reach persist(), so without this
	// every call would look like a fresh rollover and wipe the day's tombstones.
	s.file.TradingDay = td
	s.file.DeletedKeys = nil
	// 新闻行跨日即清；非新闻行（信号流/持仓提示）保持既有行为不动
	// English: news rows vanish on rollover; every other message keeps today's behaviour.
	kept := make([]MessageItem, 0, len(s.file.Messages))
	for _, m := range s.file.Messages {
		if m.Kind == KindNews {
			continue
		}
		kept = append(kept, m)
	}
	s.file.Messages = kept
	return true
}

// PublishNews §B6：写入「新闻」tab 的当日条目（Kind 由本方法统一置为 news，调用方不必记）。
// 走与 Sync 相同的按 ID 去重合并 + 删除墓碑，因此"手工删掉的新闻当日不再复活"，
// 跨交易日则随 rolloverLocked 一起清空（UI 不留历史 ≠ 证据不留：NewsEvent 事件表照旧落库）。
// English: §B6 — publish today's news-tab rows through the same dedup/tombstone path as Sync;
// they clear on the next trading day while the NewsEvent table stays persisted.
func (s *MessageStore) PublishNews(items []MessageItem) {
	if len(items) == 0 {
		return
	}
	out := make([]MessageItem, 0, len(items))
	for _, it := range items {
		it.Kind = KindNews
		out = append(out, it)
	}
	s.Sync(out)
}

// Sync 合并本轮消息：按 ID 去重（已存在则刷新内容），被删除的键不再出现。
func (s *MessageStore) Sync(items []MessageItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// §B6：日切判据收敛到 rolloverLocked 一处（原先这里只清墓碑）
	// English: §B6 — the rollover judgement now lives in one place (it used to clear tombstones only).
	s.rolloverLocked(time.Now())
	deleted := make(map[string]bool, len(s.file.DeletedKeys))
	for _, k := range s.file.DeletedKeys {
		deleted[k] = true
	}
	byKey := make(map[string]int, len(s.file.Messages))
	for i := range s.file.Messages {
		byKey[s.file.Messages[i].ID] = i
	}
	for _, it := range items {
		if it.ID == "" {
			it.ID = it.Code + "@" + it.Level
		}
		if deleted[it.ID] {
			continue
		}
		if idx, ok := byKey[it.ID]; ok {
			kept := s.file.Messages[idx]
			kept.Name = it.Name
			kept.Level = it.Level
			kept.Action = it.Action
			kept.Strategy = it.Strategy
			kept.Time = it.Time
			kept.Title = it.Title
			kept.Body = it.Body
			kept.Direction = it.Direction
			kept.GeneratedAt = it.GeneratedAt
			kept.Scope = it.Scope // §GAP2-W2 刷新时保留最新作用域归属
			// §B6 只有显式带种类才覆盖：常规信号流的 Sync（Kind=""）不许把新闻行的种类抹掉，
			// 否则同 ID 复用时新闻条目会掉回信号流（前端两处都会看不见或两处都看见）。
			// English: §B6 — only an explicit kind overwrites; a plain Sync (Kind="") must not
			// erase a news row's kind, which would drop it back into the signal stream.
			if it.Kind != "" {
				kept.Kind = it.Kind
			}
			s.file.Messages[idx] = kept
			continue
		}
		s.file.Messages = append(s.file.Messages, it)
		byKey[it.ID] = len(s.file.Messages) - 1
	}
	s.persist()
}

// ListVisible 返回指定账号可见的消息：公共（Scope=="")∪ 本人私有（Scope==uid）。
// §GAP2-W2 消息中心账户隔离的读侧收口——朋友看不到 owner 的持仓提醒，反之亦然；
// 交易信号等公共消息全员共享（owner 决策 D3：仪表盘/信号类数据通用）。
// English: §GAP2-W2 read-side isolation — returns public ∪ own-private messages; friends never see
// the owner's position-derived alerts and vice versa, while trade signals stay shared (decision D3).
func (s *MessageStore) ListVisible(userID string) []MessageItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	// §B6 读侧也判日切：引擎 5s 同步轮一旦停摆（重启窗口/非盘中），新闻行仍不许带着昨天的
	// 内容进今天——切日即空的判据不能只挂在写侧。
	// English: §B6 — the read path rolls the day too, so a stalled writer cannot let yesterday's
	// news leak into today; "empty on a new trading day" must not depend on the write side alone.
	if s.rolloverLocked(time.Now()) {
		s.persist()
	}
	out := make([]MessageItem, 0, len(s.file.Messages))
	for _, m := range s.file.Messages {
		if m.Scope == "" || m.Scope == userID {
			out = append(out, m)
		}
	}
	return out
}

// RefreshNameByCode 按代码刷新股票名称：把 Code 匹配的全部消息 Name 覆盖为最新权威名。
// 用于加自选后同步消息中心的旧名/空名；不改变消息去重键与顺序，随后持久化。
func (s *MessageStore) RefreshNameByCode(code, name string) {
	if code == "" || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.file.Messages {
		if s.file.Messages[i].Code == code && s.file.Messages[i].Name != name {
			s.file.Messages[i].Name = name
			changed = true
		}
	}
	if changed {
		s.persist()
	}
}

// Delete 删除单条消息：移除并记录墓碑（当日去重后不再自动出现）。
func (s *MessageStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.file.Messages[:0]
	for _, m := range s.file.Messages {
		if m.ID != key {
			out = append(out, m)
		}
	}
	s.file.Messages = out
	if key != "" {
		s.file.DeletedKeys = append(s.file.DeletedKeys, key)
	}
	s.persist()
}

// ClearAll 清空全部消息：当前消息全部移除并记录墓碑（当日内不再重新出现）。
func (s *MessageStore) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.file.Messages {
		s.file.DeletedKeys = append(s.file.DeletedKeys, m.ID)
	}
	s.file.Messages = nil
	s.persist()
}

// PurgeShortLevel 清除全部做空方向的消息（Level=做空，或做空方向的交易信号/卖点评估），
// 不记录墓碑（deleted_keys）：仅做多开关切回后即时清理残留，再次切回做多+做空时可正常重新同步。
// 返回清除条数。用于做空开关关闭时清理测试/历史残留，避免仅做多界面误展示做空消息。
// English: removes every short-direction message (Level=做空, or trade/sell-side messages with
// Direction=做空) WITHOUT recording a tombstone, so flipping back to long+short can re-sync them.
// Returns how many were removed. Called when the short toggle turns off, to purge stale test/historical
// residue from the long-only view.
func (s *MessageStore) PurgeShortLevel() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.file.Messages[:0]
	for _, m := range s.file.Messages {
		if m.Level == "做空" || m.Direction == "做空" {
			continue
		}
		out = append(out, m)
	}
	removed := len(s.file.Messages) - len(out)
	if removed > 0 {
		s.file.Messages = out
		s.persist()
	}
	return removed
}

// List 返回全部消息（按生成时间倒序）。
// §B6 同 ListVisible：读侧也判日切，新闻行跨日不许留在内存里冒充当日。
// English: §B6 — like ListVisible, the read path rolls the day so news rows never pretend to be today's.
func (s *MessageStore) List() []MessageItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rolloverLocked(time.Now()) {
		s.persist()
	}
	out := make([]MessageItem, len(s.file.Messages))
	copy(out, s.file.Messages)
	sort.Slice(out, func(i, j int) bool {
		return out[i].GeneratedAt.After(out[j].GeneratedAt)
	})
	return out
}
