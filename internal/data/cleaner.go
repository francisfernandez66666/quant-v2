// Package data 提供股票数据清洗、查询和跟踪功能。
// StockCleaner 负责将用户输入的股票名称或代码标准化为统一的名称+代码格式。
// Package data provides stock data cleaning, querying and tracking.
// StockCleaner normalizes user input (names or codes) into a unified name+code form.
package data

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
)

// StockCleaner 股票名称/代码清洗器。
// 维护名称↔代码的双向映射表，支持各种输入格式（纯代码、带交易所前缀、带点号后缀等）。
// 线程安全（使用 sync.RWMutex 保护映射表）。
// StockCleaner maintains a bidirectional name↔code mapping and cleans the various
// input formats; thread-safe via sync.RWMutex.
type StockCleaner struct {
	nameToCode map[string]string // 名称 → 代码 映射
	codeToName map[string]string // 代码 → 名称 映射
	mu         sync.RWMutex
	api        *MarketAPI // 行情 API，用于映射表为空时重新拉取
}

// NewStockCleaner 创建 StockCleaner 实例，并立即从 MarketAPI 拉取全量股票列表初始化映射表。
// marketAPI: 市场数据 API 客户端，用于获取股票列表。
// NewStockCleaner creates a StockCleaner and eagerly loads the full stock list
// from MarketAPI to initialize the mapping table.
func NewStockCleaner(marketAPI *MarketAPI) *StockCleaner {
	c := &StockCleaner{
		nameToCode: make(map[string]string),
		codeToName: make(map[string]string),
		api:        marketAPI,
	}
	if err := c.Refresh(marketAPI); err != nil {
		log.Printf("[cleaner] 初始拉取失败: %v", err)
	}
	return c
}

// Refresh 重新从 MarketAPI 拉取全量股票列表，刷新名称↔代码映射表。
// marketAPI: 市场数据 API 客户端。
// 通常在映射表为空或需要更新时调用。
// Refresh reloads the name↔code mapping from MarketAPI, typically when the
// table is empty or needs updating.
func (c *StockCleaner) Refresh(marketAPI *MarketAPI) error {
	list, err := marketAPI.GetStockList()
	if err != nil {
		return fmt.Errorf("获取股票列表: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, code := range list {
		c.nameToCode[name] = code
		c.codeToName[code] = name
	}
	log.Printf("[cleaner] 加载 %d 只股票名称↔代码映射", len(list))
	return nil
}

var rePureCode = regexp.MustCompile(`^\d{6}$`)              // 纯 6 位数字代码，如 "600519"
var reSHCode = regexp.MustCompile(`^(SH|sh)(\d{6})$`)       // 带沪市前缀，如 "SH600519"/"sh600519"
var reSZCode = regexp.MustCompile(`^(SZ|sz)(\d{6})$`)       // 带深市前缀，如 "SZ000001"/"sz000001"
var reDotCode = regexp.MustCompile(`^(\d{6})\.(SH|SZ|BJ)$`) // 带点号交易所后缀，如 "600519.SH"
// reNameParenCode 匹配「名称(代码)」标签格式（板块成分股传播注入事件 RelatedStocks 的既有口径），
// 如 "中际旭创(300308)"。§D1 护栏1 后 LLM 不再产出个股名单，该标签成为板块级事件唯一的个股来源，
// Clean 必须能解析它，否则成分股会在 CleanBatch 里被整批丢弃。
// English: matches the "名称(代码)" constituent label — after guardrail-1 it is the only per-stock
// source for sector events, so Clean must parse it or all constituents would be dropped.
var reNameParenCode = regexp.MustCompile(`^(.+)\((\d{6})\)$`)

// normalizeCode 将各种格式的股票代码统一为纯 6 位数字代码。
// 支持的格式：
//   - "600519.SH" / "000001.SZ"（带点号和交易所后缀）
//   - "SH600519" / "SZ000001"（带交易所前缀）
//   - "sh600519" / "sz000001"（小写前缀）
//   - "600519"（纯数字）
//
// 返回值：6 位纯数字代码；无法识别则返回原始输入。
// normalizeCode normalizes a code (dot-suffix / SH/SZ-prefixed / plain digits)
// to a pure 6-digit form; unrecognized input is returned unchanged.
func normalizeCode(raw string) string {
	if m := reDotCode.FindStringSubmatch(raw); len(m) == 3 {
		return m[1]
	}
	if m := reSHCode.FindStringSubmatch(raw); len(m) == 3 {
		return m[2]
	}
	if m := reSZCode.FindStringSubmatch(raw); len(m) == 3 {
		return m[2]
	}
	if rePureCode.MatchString(raw) {
		return raw
	}
	return raw
}

// Clean 清洗单只股票的输入，返回标准化的名称和代码。
// nameOrCode: 用户输入的股票名称或代码（支持各种格式）。
// 返回: 股票名称, 标准化代码, 错误信息。
// 匹配优先级：纯代码 → 带前缀代码 → 按名称查找。
// Clean cleans a single stock input into a normalized (name, code). Priority:
// plain code → prefixed code → lookup by name.
func (c *StockCleaner) Clean(nameOrCode string) (string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	raw := strings.TrimSpace(nameOrCode)
	if raw == "" {
		return "", "", fmt.Errorf("空输入")
	}

	code := normalizeCode(raw)
	rePure := rePureCode.MatchString(code)

	// 纯数字代码：先按裸代码查，再依次补 SH / SZ 前缀重试，
	// 兼容清单里以带前缀形式登记的同一只股票；三档都落空才算无效代码。
	if rePure {
		if name, ok := c.codeToName[code]; ok {
			return name, code, nil
		}
		shCode := "SH" + code
		if name, ok := c.codeToName[shCode]; ok {
			return name, shCode, nil
		}
		szCode := "SZ" + code
		if name, ok := c.codeToName[szCode]; ok {
			return name, szCode, nil
		}
		return "", code, fmt.Errorf("代码 %s 未找到对应名称", code)
	}

	// 已带交易所前缀的代码：正查不到时再剥掉前两位按裸代码回查一次，
	// 使用户手敲的 "sh600000"/"600000" 都能落到同一份清单。
	if strings.HasPrefix(code, "SH") || strings.HasPrefix(code, "SZ") {
		if name, ok := c.codeToName[code]; ok {
			return name, code, nil
		}
		pure := code[2:]
		if name, ok := c.codeToName[pure]; ok {
			return name, pure, nil
		}
		return "", code, fmt.Errorf("代码 %s 未找到", code)
	}

	// 「名称(代码)」标签（板块成分股传播注入格式）：优先按括号内代码查（代码是硬事实），
	// 代码不在清单时回退按名称查；两者都落空按未匹配丢弃（与其他非法输入同口径）。
	// English: "名称(代码)" constituent label — resolve by the code first (hard fact),
	// fall back to the name; miss both → drop like any other unmatched input.
	if m := reNameParenCode.FindStringSubmatch(raw); len(m) == 3 {
		name, code := m[1], m[2]
		if mapped, ok := c.codeToName[code]; ok {
			return mapped, code, nil
		}
		if code2, ok := c.nameToCode[name]; ok {
			return name, code2, nil
		}
		return "", "", fmt.Errorf("未匹配到 %q", raw)
	}

	// 「名称|代码」标准输出格式：CleanBatch 的输出会被 Stage2/护栏1 写回 RelatedStocks，
	// 下一轮再次进 Clean 时必须可解析（幂等），否则已清洗条目会被整批丢弃，
	// 导致个股分流/CleanedStocks 链路断流。优先按代码查（硬事实），回退按名称查。
	// English: "name|code" is CleanBatch's own output format which flows back into RelatedStocks
	// (Stage2 / guardrail-1) — re-cleaning must be idempotent or already-cleaned entries are
	// dropped, starving the CleanedStocks/stock-routing chain. Resolve by code first, name fallback.
	if name, codePart, ok := strings.Cut(raw, "|"); ok {
		code := normalizeCode(strings.TrimSpace(codePart))
		if rePureCode.MatchString(code) {
			if mapped, hit := c.codeToName[code]; hit {
				return mapped, code, nil
			}
		}
		name = strings.TrimSpace(name)
		if code2, hit := c.nameToCode[name]; hit {
			return name, code2, nil
		}
		return "", "", fmt.Errorf("未匹配到 %q", raw)
	}

	if code2, ok := c.nameToCode[raw]; ok {
		if name2, ok2 := c.codeToName[code2]; ok2 {
			return name2, code2, nil
		}
		return raw, code2, nil
	}

	return raw, "", fmt.Errorf("未匹配到 %q", raw)
}

// FindStocksInText 在文本中查找出现的股票名称。
// 遍历名称↔代码映射，文本包含某只股票名称即命中，返回命中名称列表。
// 用于新闻标题的 Stage0 归因分类。
// FindStocksInText finds any stock names appearing in a text, used for
// stage-0 attribution of news headlines.
func (c *StockCleaner) FindStocksInText(text string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(text) == 0 {
		return nil
	}
	var hit []string
	for name := range c.nameToCode {
		if name != "" && strings.Contains(text, name) {
			hit = append(hit, name)
		}
	}
	return hit
}

// CleanBatch 批量清洗股票输入列表。
// items: 股票名称或代码列表。
// 返回清洗后的字符串列表，格式为 "名称|代码"；清洗失败的项丢弃（不保留原始输入，杜绝垃圾名透传）。
// 如果映射表为空，会自动尝试重新拉取。
// CleanBatch cleans a list of stock inputs into "name|code" strings, dropping
// unmatched items and auto-refreshing the mapping when empty.
func (c *StockCleaner) CleanBatch(items []string) []string {
	c.mu.RLock()
	empty := len(c.codeToName) == 0
	c.mu.RUnlock()
	if empty && c.api != nil {
		log.Printf("[cleaner] 映射为空, 尝试重新拉取")
		if err := c.Refresh(c.api); err != nil {
			log.Printf("[cleaner] 重试拉取失败: %v", err)
		}
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		name, code, err := c.Clean(item)
		if err != nil {
			log.Printf("[cleaner] 丢弃无法匹配的个股 %q: %v", item, err)
			continue
		}
		out = append(out, fmt.Sprintf("%s|%s", name, code))
	}
	return out
}
