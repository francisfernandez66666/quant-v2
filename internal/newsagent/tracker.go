package newsagent

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
)

// tracker 新闻去重记账器：基于文件持久化的已见标题/来源同步时间，避免跨轮重复处理。
// （tracker is a news dedup ledger persisting seen titles and per-source sync times to files.）
// English: tracker is a news dedup ledger persisting seen titles and per-source sync times to files, avoiding duplicate processing across rounds.
type tracker struct {
	mu sync.RWMutex // 读写锁：保护 data 的并发访问
	// English: read/write lock protecting concurrent access to data
	data *TrackerData // 记账数据（已见标题、各来源同步时间）
	// English: ledger data (seen titles, per-source sync times)
	filePath string // 持久化文件路径（news_tracker.json）
	// English: persistence file path (news_tracker.json)
}

// newTracker 创建记账器并从文件加载历史数据（文件不存在时初始化为空记账）。
// （newTracker creates a ledger and loads history from file, starting empty when missing.）
// English: newTracker creates a ledger and loads history from file, starting empty when missing.
func newTracker(dataDir string) *tracker {
	t := &tracker{
		data: &TrackerData{
			SeenTitles: make(map[string]string),
			LastSync:   make(map[string]string),
		},
		filePath: filepath.Join(dataDir, "news_tracker.json"),
	}
	t.load()
	return t
}

// maxPendingItems 未归因队列上限：防止 LLM 长期失败时 pending 无限膨胀。
// 超限按"加入时间"淘汰最旧（保新弃旧），保证重试窗口聚焦近期新闻。
// （maxPendingItems caps the unattributed queue so a long LLM outage cannot grow it unboundedly;
// excess items are evicted by insertion order (keep newest), keeping retries focused on recent news.）
// English: maxPendingItems caps the unattributed queue so a long LLM outage cannot grow it unboundedly; excess items are evicted by insertion order (keep newest, drop oldest), keeping the retry window focused on recent news.
const maxPendingItems = 300

// tradingDayStart 计算当前时刻所属"交易日窗口"起点：交易日下午 15:00 之后到
// 次日 15:00 之前属于同一交易日（周五 15:00 后算下周一交易日的开头，跳过周末）。
// 用于裁剪 pending 队列，确保只重试当前交易日窗口内的未归因新闻。
// （tradingDayStart returns the start of the current trading-day window: news between yesterday's
// 15:00 and today's 15:00 belongs to the same trading day (Friday 15:00 → Monday window, skipping weekends).）
// English: tradingDayStart returns the start of the current trading-day window: from a trading day's 15:00 until the next 15:00 belongs to the same trading day (after Friday 15:00 it counts as the start of Monday's window, skipping weekends). Used to prune the pending queue so only unattributed news within the current trading-day window is retried.
func tradingDayStart(now time.Time) time.Time {
	// 向前找最近一个 15:00（含今天），作为窗口起点
	// English: walk backwards to the most recent 15:00 (including today) as the window start
	start := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
	for {
		if now.Before(start) {
			start = start.AddDate(0, 0, -1)
			continue
		}
		// 15:00 落在非交易日（周末/节假日）时继续回退，直至落在交易日
		// English: keep stepping back while 15:00 falls on a non-trading day (weekend/holiday), until it lands on a trading day
		if start.Weekday() == time.Saturday || start.Weekday() == time.Sunday {
			start = start.AddDate(0, 0, -1)
			continue
		}
		break
	}
	return start
}

// parsePendingTime 解析新闻 Datetime 字符串，失败时返回零时间（不参与窗口裁剪）。
// （parsePendingTime parses a news Datetime string; on failure returns zero time so it survives window pruning.）
// English: parsePendingTime parses a news Datetime string; on failure it returns zero time so the item survives window pruning.
func parsePendingTime(dt string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", dt, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// load 从文件加载记账数据；文件缺失或解析失败时保留空记账。（Loads ledger data from file, keeping empty on failure.）
// English: load reads ledger data from file, keeping an empty ledger on missing/corrupt file.
func (t *tracker) load() {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	json.Unmarshal(data, t.data)
	// 确保两个 map 非空，避免后续写入 panic
	// English: ensure both maps are non-nil to avoid panics on later writes
	if t.data.SeenTitles == nil {
		t.data.SeenTitles = make(map[string]string)
	}
	if t.data.LastSync == nil {
		t.data.LastSync = make(map[string]string)
	}
	if t.data.PendingItems == nil {
		t.data.PendingItems = nil
	}
}

// save 将记账数据序列化并写回文件。（Serializes and writes the ledger data back to file.）
// English: save serializes and writes the ledger data back to file.
func (t *tracker) save() error {
	// 读锁下序列化快照，避免长时间占用写锁
	// English: snapshot under a read lock to avoid holding the write lock for long
	t.mu.RLock()
	data, err := json.MarshalIndent(t.data, "", "  ")
	t.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

// titleHash 对标题做 MD5 摘要并取前 8 字节十六进制，作为去重键。（MD5 digest of a title, first 8 bytes hex, as dedup key.）
// English: titleHash takes an MD5 digest of the title, first 8 bytes in hex, as the dedup key.
func titleHash(title string) string {
	h := md5.Sum([]byte(title))
	return fmt.Sprintf("%x", h[:8])
}

// IsSeen 判断标题是否已处理过。（Reports whether the title has already been processed.）
// English: reports whether the title has already been processed.
func (t *tracker) IsSeen(title string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.data.SeenTitles[titleHash(title)]
	return ok
}

// MarkSeen 记录标题已处理及对应时间。（Marks a title as processed with its datetime.）
// English: marks a title as processed with its datetime.
func (t *tracker) MarkSeen(title, datetime string) {
	t.mu.Lock()
	t.data.SeenTitles[titleHash(title)] = datetime
	t.mu.Unlock()
}

// BulkMarkSeen 批量记录标题已处理，datetimes 与 titles 等长时逐项写入。（Batch-marks titles as seen when datetimes align.）
// English: batch-marks titles as seen, writing each item when datetimes aligns with titles.
func (t *tracker) BulkMarkSeen(titles []string, datetimes []string) {
	t.mu.Lock()
	for i := range titles {
		// datetimes 不足时跳过该条（保留旧值），保证不越界
		// English: skip an item when datetimes is too short (keep the old value) to stay in bounds
		if i < len(datetimes) {
			t.data.SeenTitles[titleHash(titles[i])] = datetimes[i]
		}
	}
	t.mu.Unlock()
}

// LastSync 返回指定来源最近同步时间。（Returns the last sync time for a source.）
// English: returns the last sync time for a source.
func (t *tracker) LastSync(source string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.data.LastSync[source]
}

// SetLastSync 记录指定来源的同步时间。（Records the sync time for a source.）
// English: records the sync time for a source.
func (t *tracker) SetLastSync(source, datetime string) {
	t.mu.Lock()
	t.data.LastSync[source] = datetime
	t.mu.Unlock()
}

// Pending 返回未归因队列副本（已按交易日窗口裁剪）。
// （Pending returns a copy of the unattributed queue, pruned to the current trading-day window.）
// English: Pending returns a copy of the unattributed queue, pruned to the current trading-day window.
func (t *tracker) Pending() []data.NewsItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prunePendingLocked(time.Now())
	out := make([]data.NewsItem, len(t.data.PendingItems))
	copy(out, t.data.PendingItems)
	return out
}

// IsPending 判断标题是否仍在未归因队列。
// （IsPending reports whether the title is still in the unattributed queue.）
// English: IsPending reports whether the title is still in the unattributed queue.
func (t *tracker) IsPending(title string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pendingIndexLocked(title) >= 0
}

// pendingIndexLocked 返回标题在未归因队列中的下标，不存在返回 -1。
// （pendingIndexLocked returns the index of title in the queue, or -1 when absent.）
// English: pendingIndexLocked returns the index of title in the queue, or -1 when absent.
func (t *tracker) pendingIndexLocked(title string) int {
	h := titleHash(title)
	for i, it := range t.data.PendingItems {
		if titleHash(it.Title) == h {
			return i
		}
	}
	return -1
}

// AddPending 把抓取到的新闻加入未归因队列：跳过已 seen 或已在队列中的标题，
// 裁剪到当前交易日窗口并执行条数上限（保新弃旧）。
// （AddPending enqueues fetched news into the unattributed queue, skipping seen/already-queued titles,
// pruning to the current trading-day window and enforcing the item cap keeping the newest.）
// English: AddPending enqueues fetched news into the unattributed queue, skipping titles already seen or already queued, pruning to the current trading-day window and enforcing the item cap (keep newest, drop oldest).
func (t *tracker) AddPending(items []data.NewsItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.prunePendingLocked(now)
	for _, it := range items {
		if it.Title == "" {
			continue
		}
		if _, ok := t.data.SeenTitles[titleHash(it.Title)]; ok {
			continue
		}
		if t.pendingIndexLocked(it.Title) >= 0 {
			continue
		}
		t.data.PendingItems = append(t.data.PendingItems, it)
	}
	// 超限：按加入时间淘汰最旧（队列头部为最早加入）
	// English: over the cap: evict the oldest by insertion order (queue head is the earliest)
	if len(t.data.PendingItems) > maxPendingItems {
		t.data.PendingItems = t.data.PendingItems[len(t.data.PendingItems)-maxPendingItems:]
	}
}

// RemovePending 把成功归因的新闻从未归因队列移除并标记为已见（标题 → datetime）。
// 同时保存记账文件，避免成功归因后进程重启导致重复重试。
// （RemovePending drops successfully-attributed items from the queue, marks them seen (title → datetime)
// and saves the ledger so a restart does not re-retry them.）
// English: RemovePending drops successfully-attributed items from the queue, marks them seen (title -> datetime) and saves the ledger so a restart does not re-retry them.
func (t *tracker) RemovePending(items []data.NewsItem) {
	t.mu.Lock()
	for _, it := range items {
		idx := t.pendingIndexLocked(it.Title)
		if idx < 0 {
			continue
		}
		dt := it.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		t.data.PendingItems = append(t.data.PendingItems[:idx], t.data.PendingItems[idx+1:]...)
		t.data.SeenTitles[titleHash(it.Title)] = dt
	}
	t.mu.Unlock()
	_ = t.save()
}

// prunePendingLocked 裁剪未归因队列：移除不在当前交易日窗口内的旧新闻。
// 锁内调用（调用方须已持有 t.mu）。零时间（解析失败）的条目保留。
// （prunePendingLocked prunes the queue to the current trading-day window; items with unparseable time survive.）
// English: prunePendingLocked prunes the queue to the current trading-day window, removing old news outside it. Called with the lock held (caller must already hold t.mu); items with unparseable (zero) time are kept.
func (t *tracker) prunePendingLocked(now time.Time) {
	start := tradingDayStart(now)
	kept := t.data.PendingItems[:0]
	for _, it := range t.data.PendingItems {
		dt := parsePendingTime(it.Datetime)
		if dt.IsZero() || !dt.Before(start) {
			kept = append(kept, it)
		}
	}
	t.data.PendingItems = kept
}

// SortPendingNewestFirst 按发布时间倒序（最新在前）对队列排序，供盘前优先处理最新新闻。
// 解析失败（零时间）的条目视为最新排在最前。
// （SortPendingNewestFirst sorts the queue by publish time descending (newest first) for premarket
// prioritization; unparseable entries rank newest.）
// English: SortPendingNewestFirst sorts the queue by publish time descending (newest first) so premarket processes the newest news first; unparseable (zero-time) entries rank as newest.
func (t *tracker) SortPendingNewestFirst() {
	t.mu.Lock()
	defer t.mu.Unlock()
	sort.SliceStable(t.data.PendingItems, func(i, j int) bool {
		ti := parsePendingTime(t.data.PendingItems[i].Datetime)
		tj := parsePendingTime(t.data.PendingItems[j].Datetime)
		return ti.After(tj)
	})
}
