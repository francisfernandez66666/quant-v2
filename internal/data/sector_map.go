// sector_map.go — §FLOW-SECTOR-JOIN 云端逐票板块连接键（决策机侧，2026-11-20）。
//
// 为什么要有它（现场实测，不是理论）：云端把「个股 → 板块 delta」的解析写成
// `GetStockIndustry(code)`（东财 `push2/api/qt/stock/get?fields=f127`），而广州对东财
// **全族在 TLS 层被 RST**（11-19/11-20 两次实测：`push2 / push2his / push2delay` 一律
// `Empty reply`）。于是这条腿的返回值恒为空 ⇒ `sectorDeltaFor` 恒弃权 ⇒ blend 的板块项
// 永远缺项、renormalize 到「大盘+量能」两项。读面上它"没有报错"，只是永远不参与。
//
// 连接键从哪来：`scripts/pregen_sector_map_gz.py` 每天经**盒子中继**（盒子可达东财）逐板块
// 取成分，行业板块优先、概念板块其次，产出 `code -> 东财板块名`（实测 5662/5546 只、
// 板块成功率 99.5%）。它本来只喂载体；本文件让**云端**也吃同一份表 —— 一个生产者、两个消费者，
// 就不会出现"载体接上了、云端没接"的半条腿。
//
// 三条纪律（改动前必读）：
//  1. **只做连接键，不做判据**：查不到就返回 false（调用方弃权），绝不返回空串当"没有板块"。
//  2. **跨日不留宿**：文件里的 `day` 必须等于北京今日，否则整表视为不可用（与 precalc 同一纪律）。
//     ⚠️ 日期形态是 `YYYYMMDD`（生产者按载体口径写），与 `flow_factor_daily.trade_date`
//     的 `YYYY-MM-DD` **不同**——查错形态会得到"永远今天没有表"。
//  3. **按需重载**：文件 mtime/size 变化才重读（生产者每天一跑，进程要跑好几天）。
//
// 刻意**不复用** `industryFn`（证监会门类，供集中度桶用）：两套命名交集为空，
// 拿它当连接键会得到"看起来接上了、命中率恒 0"。
//
// English: day-keyed reader for the per-stock EastMoney board-name join key produced by the
// box-side relay; lookup-only (miss = abstain), reloads on file change, never leaks across days.
package data

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// sectorMapFile 生产者写出的文件形状（只取本文件需要的三个键）。
// 多出的字段（industry_boards / concept_boards / boards_ok ...）是给运维看的读数，
// 这里刻意不解析：连接键的形状越窄，越不容易被上游改字段带崩。
type sectorMapFile struct {
	Day       string            `json:"day"`
	Generated int64             `json:"generated_at"`
	SectorMap map[string]string `json:"sector_map"`
}

// SectorMapCache 当日板块连接键（code → 东财板块名）的进程内缓存。
// 并发安全；`path` 为空即"未装配"，Lookup 恒 false（调用方按弃权处理）。
type SectorMapCache struct {
	mu sync.RWMutex
	// path 生产者产出的 JSON 路径（通常 <DataDir>/sector_map.json）。
	path string
	// loaded 是否成功载入过（区分数值不可用与从未装配）。
	loaded bool
	day    string
	mtime  time.Time
	size   int64
	table  map[string]string
}

// NewSectorMapCache 构造一个连接键缓存（不立即读盘：首次 Lookup 时才读，
// 避免把引擎启动绑在生产者是否跑过这件事上）。
func NewSectorMapCache(path string) *SectorMapCache {
	return &SectorMapCache{path: strings.TrimSpace(path)}
}

// Path 当前装配的文件路径（空串＝未装配）。
func (c *SectorMapCache) Path() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.path
}

// Stats 只读读数（供启动清单/观测面）：今日表命中数、文件日期、是否可用。
// known=false 时 count 恒 0 —— 绝不把"没读到"报成"0 只票"。
//
// 注意它自己也会触发一次载入：启动清单可能在第一次 Lookup 之前就打这条读数，
// 不载入就会把"表在这儿、5662 只"报成"未装配 / 0 只"——那正是本仓库最贵的
// "读数说谎"形状（看着像没配，实际是没人读）。
func (c *SectorMapCache) Stats(today string) (count int, day string, known bool) {
	if c == nil {
		return 0, "", false
	}
	c.mu.RLock()
	path := c.path
	c.mu.RUnlock()
	if path == "" || today == "" {
		return 0, "", false
	}
	c.maybeReload(today)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded || c.day != today {
		return 0, c.day, false
	}
	return len(c.table), c.day, true
}

// Lookup 取一只票的东财板块名。today 为北京今日的 YYYYMMDD。
// 返回 false 表示"不知道"（未装配 / 文件缺失 / 跨日 / 这只票不在表里），调用方必须弃权。
func (c *SectorMapCache) Lookup(code, today string) (string, bool) {
	if c == nil || today == "" {
		return "", false
	}
	c.mu.RLock()
	path := c.path
	c.mu.RUnlock()
	if path == "" {
		return "", false
	}
	c.maybeReload(today)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded || c.day != today {
		return "", false
	}
	name, ok := c.table[normalizeSectorCode(code)]
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// maybeReload 文件变了就重读（每天一次的生产者产出，进程要跨天活着）。
// 读失败不抛错：保留上一次可用的表（若它仍是今日），否则退化为"不知道"。
func (c *SectorMapCache) maybeReload(today string) {
	st, err := os.Stat(c.path)
	if err != nil {
		return
	}
	c.mu.RLock()
	same := c.loaded && st.Size() == c.size && st.ModTime().Equal(c.mtime) && c.day == today
	c.mu.RUnlock()
	if same {
		return
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var doc sectorMapFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	// 跨日表绝不留宿：宁可整天弃权，也不拿昨天的成分表去配今天的 delta
	// （板块成分每天会变，错配的 delta 会贴到不相干的票上）。
	if strings.TrimSpace(doc.Day) != today {
		c.mu.Lock()
		c.loaded = false
		c.day = strings.TrimSpace(doc.Day)
		c.table = nil
		c.size, c.mtime = st.Size(), st.ModTime()
		c.mu.Unlock()
		return
	}
	table := make(map[string]string, len(doc.SectorMap))
	for code, name := range doc.SectorMap {
		code = normalizeSectorCode(code)
		name = strings.TrimSpace(name)
		if len(code) != 6 || name == "" {
			continue
		}
		table[code] = name
	}
	c.mu.Lock()
	c.loaded = true
	c.day = doc.Day
	c.table = table
	c.size, c.mtime = st.Size(), st.ModTime()
	c.mu.Unlock()
}

// normalizeSectorCode 统一成 6 位纯数字（剥 .SH/.SZ/.BJ 与大小写/空白差异）。
// 生产者写的是 6 位，但调用方可能拿到带后缀的 ts_code —— 连接键必须归一，
// 否则"同一只票两种写法"会让一半的票静默 miss。
// 归不出来（位数不对 / 含非数字）一律返回空串：空串永远查不到，调用方按弃权处理。
func normalizeSectorCode(code string) string {
	s := strings.TrimSpace(strings.ToUpper(code))
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		s = strings.TrimSuffix(s, suf)
	}
	s = strings.TrimSpace(s)
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}
