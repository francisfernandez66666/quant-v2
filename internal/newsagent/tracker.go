package newsagent

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// tracker 新闻去重记账器：基于文件持久化的已见标题/来源同步时间，避免跨轮重复处理。
// （tracker is a news dedup ledger persisting seen titles and per-source sync times to files.）
type tracker struct {
	mu       sync.RWMutex // 读写锁：保护 data 的并发访问
	data     *TrackerData // 记账数据（已见标题、各来源同步时间）
	filePath string       // 持久化文件路径（news_tracker.json）
}

// newTracker 创建记账器并从文件加载历史数据（文件不存在时初始化为空记账）。
// （newTracker creates a ledger and loads history from file, starting empty when missing.）
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

// load 从文件加载记账数据；文件缺失或解析失败时保留空记账。（Loads ledger data from file, keeping empty on failure.）
func (t *tracker) load() {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	json.Unmarshal(data, t.data)
	// 确保两个 map 非空，避免后续写入 panic
	if t.data.SeenTitles == nil {
		t.data.SeenTitles = make(map[string]string)
	}
	if t.data.LastSync == nil {
		t.data.LastSync = make(map[string]string)
	}
}

// save 将记账数据序列化并写回文件。（Serializes and writes the ledger data back to file.）
func (t *tracker) save() error {
	// 读锁下序列化快照，避免长时间占用写锁
	t.mu.RLock()
	data, err := json.MarshalIndent(t.data, "", "  ")
	t.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

// titleHash 对标题做 MD5 摘要并取前 8 字节十六进制，作为去重键。（MD5 digest of a title, first 8 bytes hex, as dedup key.）
func titleHash(title string) string {
	h := md5.Sum([]byte(title))
	return fmt.Sprintf("%x", h[:8])
}

// IsSeen 判断标题是否已处理过。（Reports whether the title has already been processed.）
func (t *tracker) IsSeen(title string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.data.SeenTitles[titleHash(title)]
	return ok
}

// MarkSeen 记录标题已处理及对应时间。（Marks a title as processed with its datetime.）
func (t *tracker) MarkSeen(title, datetime string) {
	t.mu.Lock()
	t.data.SeenTitles[titleHash(title)] = datetime
	t.mu.Unlock()
}

// BulkMarkSeen 批量记录标题已处理，datetimes 与 titles 等长时逐项写入。（Batch-marks titles as seen when datetimes align.）
func (t *tracker) BulkMarkSeen(titles []string, datetimes []string) {
	t.mu.Lock()
	for i := range titles {
		// datetimes 不足时跳过该条（保留旧值），保证不越界
		if i < len(datetimes) {
			t.data.SeenTitles[titleHash(titles[i])] = datetimes[i]
		}
	}
	t.mu.Unlock()
}

// LastSync 返回指定来源最近同步时间。（Returns the last sync time for a source.）
func (t *tracker) LastSync(source string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.data.LastSync[source]
}

// SetLastSync 记录指定来源的同步时间。（Records the sync time for a source.）
func (t *tracker) SetLastSync(source, datetime string) {
	t.mu.Lock()
	t.data.LastSync[source] = datetime
	t.mu.Unlock()
}
