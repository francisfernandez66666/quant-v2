package newsagent

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type tracker struct {
	mu        sync.RWMutex
	data      *TrackerData
	filePath  string
}

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

func (t *tracker) load() {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	json.Unmarshal(data, t.data)
	if t.data.SeenTitles == nil {
		t.data.SeenTitles = make(map[string]string)
	}
	if t.data.LastSync == nil {
		t.data.LastSync = make(map[string]string)
	}
}

func (t *tracker) save() error {
	t.mu.RLock()
	data, err := json.MarshalIndent(t.data, "", "  ")
	t.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func titleHash(title string) string {
	h := md5.Sum([]byte(title))
	return fmt.Sprintf("%x", h[:8])
}

// IsSeen 判断标题是否已处理过。
func (t *tracker) IsSeen(title string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.data.SeenTitles[titleHash(title)]
	return ok
}

// MarkSeen 记录标题已处理及对应时间。
func (t *tracker) MarkSeen(title, datetime string) {
	t.mu.Lock()
	t.data.SeenTitles[titleHash(title)] = datetime
	t.mu.Unlock()
}

// BulkMarkSeen 批量记录标题已处理。
func (t *tracker) BulkMarkSeen(titles []string, datetimes []string) {
	t.mu.Lock()
	for i := range titles {
		if i < len(datetimes) {
			t.data.SeenTitles[titleHash(titles[i])] = datetimes[i]
		}
	}
	t.mu.Unlock()
}

// LastSync 返回指定来源最近同步时间。
func (t *tracker) LastSync(source string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.data.LastSync[source]
}

// SetLastSync 记录指定来源的同步时间。
func (t *tracker) SetLastSync(source, datetime string) {
	t.mu.Lock()
	t.data.LastSync[source] = datetime
	t.mu.Unlock()
}
