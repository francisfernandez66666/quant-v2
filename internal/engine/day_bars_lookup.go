// day_bars_lookup.go —— §KLINE-CHAIN-3（2026-09-23 夜间批）日K链第三级兜底腿的【提供方】实现。
//
// 职责：把研究库（trading.db）里夜里同步好的日K读出来，交给 strategy_engine 的日K链当第三条腿。
// 之所以放在本包：strategy_engine 刻意不 import internal/store（见 SetFinaLookup 的同一纪律），
// 而本包同时依赖 store 与 strategy_engine，装配点 cmd/quant 只需一行接线，不成环。
//
// 本文件【只做读取与形状转换】，不做任何口径判断：
//   - 价格是 store.HfqBars 的**后复权**口径（可能是实际价的 2~3 倍）；
//   - Volume 沿用 store.Bar.Vol 的**手**口径；
//     两者都由 strategy_engine.storeDayKLine 负责归一（锚实时昨收）与 ×100 换算，
//     守卫集中一处才不会出现在两个包里各写一遍、改一处漏一处的情况。
//
// （Provider for the third daily-bar leg: reads locally synced bars from the research DB and hands
// them to strategy_engine, which owns the normalization/freshness guards.）
package engine

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// DayBarsLookup 库内日K读取器：按 ts_code 读后复权日线，带 TTL 缓存。
// 缓存的必要性：只有网络两条复权腿全挂时才会走到这里，而那正是故障期间——5s 打分循环
// 每轮每股一次 SQLite 扫描会把故障放大成本地 IO 风暴。缓存的是**未归一**的后复权原始序列，
// 与实时昨收无关，所以不同昨收之间可以共用同一份缓存。
// （DayBarsLookup reads hfq daily bars from the research DB with a TTL cache; the cached series is
// un-normalized, hence shared across different prev-close anchors.）
type DayBarsLookup struct {
	db *store.DB

	mu    sync.Mutex
	cache map[string]*dayBarsEntry
}

// dayBarsEntry 一条库内日K缓存（bars 为后复权原始序列，量纲仍为手）。
type dayBarsEntry struct {
	bars []data.KLine
	at   time.Time
}

// dayBarsTTL 库内日K缓存有效期。库内数据一天只随夜间同步更新一次，10 分钟足够保守；
// 与日K链自身的 5 分钟 TTL（strategy_engine.cachedKLine）相比不会更陈旧，也不会拖慢故障恢复。
const dayBarsTTL = 10 * time.Minute

// dayBarsLookbackDays 取数窗口（日历天）。打分要 120 根，120 个交易日 ≈ 168 个日历天，
// 再留出长假与停牌缺档的余量取 500 天——多取的尾部由 count 截断，不改变结果。
const dayBarsLookbackDays = 500

// NewDayBarsLookup 创建库内日K读取器。db 为 nil 时返回的读取器恒返回 false（不接入本腿）。
// English: constructor; a nil DB simply disables the leg (lookup reports not-available).
func NewDayBarsLookup(db *store.DB) *DayBarsLookup {
	return &DayBarsLookup{db: db, cache: make(map[string]*dayBarsEntry)}
}

// Lookup 实现 strategy_engine.SetDayBarsLookup 要求的签名。
// code 接受 6 位（600519）或带后缀（600519.SH / sh.600519）写法，统一归一为库内 ts_code。
// 返回 (升序后复权序列, 是否可用)；count 为需要的根数（<=0 视为 120）。
// English: implements the injected lookup signature; normalizes the code, returns ascending hfq bars.
func (l *DayBarsLookup) Lookup(code string, count int) ([]data.KLine, bool) {
	if l == nil || l.db == nil {
		return nil, false
	}
	if count <= 0 {
		count = 120
	}
	ts := dayBarsTSCode(code)
	if ts == "" {
		return nil, false
	}
	key := fmt.Sprintf("%s|%d", ts, count)

	l.mu.Lock()
	if e, ok := l.cache[key]; ok && time.Since(e.at) <= dayBarsTTL {
		bars := e.bars
		l.mu.Unlock()
		return bars, len(bars) > 0
	}
	l.mu.Unlock()

	end := data.TradingDayDate(time.Now())
	start := time.Now().AddDate(0, 0, -dayBarsLookbackDays).Format("20060102")
	rows, err := l.db.HfqBars(ts, start, end)
	if err != nil {
		// 查库失败不写缓存：下一轮重试，不把一次抖动固化 10 分钟（同 finaCache 的处理）。
		log.Printf("[daybars] %s 库内日K查询失败（本轮按不可用处理，不代表库里真没有）: %v", ts, err)
		opslog.DayOnce("dayk-store-query-error", func() {
			opslog.Logf("data", "日K兜底腿查库失败（本轮拒用该腿）：%v", err)
		})
		return nil, false
	}
	if len(rows) > count {
		rows = rows[len(rows)-count:] // 只留最近 count 根（HfqBars 已按 trade_date 升序）
	}
	bars := make([]data.KLine, 0, len(rows))
	for _, b := range rows {
		t, perr := time.ParseInLocation("20060102", b.Date, time.Local)
		if perr != nil {
			continue // 日期非法的行直接丢弃：宁可少一根，也不给下游一根不知何日的K
		}
		bars = append(bars, data.KLine{
			Date:   t,
			Open:   b.Open, // 后复权价（未归一，由 strategy_engine 定锚缩放）
			High:   b.High, // 同上
			Low:    b.Low,  // 同上
			Close:  b.Close,
			Volume: b.Vol, // 手（未换算，由 strategy_engine ×100）
			Amount: b.Amount,
		})
	}
	l.mu.Lock()
	l.cache[key] = &dayBarsEntry{bars: bars, at: time.Now()} // 真缺失也缓存，避免每轮重扫
	l.mu.Unlock()
	return bars, len(bars) > 0
}

// dayBarsTSCode 把打分池里的代码统一成研究库 ts_code（XXXXXX.SH/SZ/BJ）。
// 6 位纯数字按首位判交易所（6/9 沪、4/8 北、其余深）；已带后缀的原样大写化，
// baostock 风格的 sh.600000 换成 600000.SH——同一只票不能在缓存键上分裂成两种形态。
// English: normalizes a pool code into the research ts_code used by the daily tables.
func dayBarsTSCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if len(code) == 6 {
		digit := true
		for i := 0; i < 6; i++ {
			if code[i] < '0' || code[i] > '9' {
				digit = false
				break
			}
		}
		if digit {
			switch code[0] {
			case '6', '9':
				return code + ".SH"
			case '4', '8':
				return code + ".BJ"
			default:
				return code + ".SZ"
			}
		}
	}
	if len(code) >= 9 && code[6] == '.' {
		return strings.ToUpper(code)
	}
	if len(code) >= 9 && (strings.HasPrefix(code, "sh.") || strings.HasPrefix(code, "sz.") || strings.HasPrefix(code, "bj.")) {
		return code[3:] + "." + strings.ToUpper(code[0:2])
	}
	return strings.ToUpper(code)
}
