package strategy_engine

import (
	"quant-trading-v2/internal/newsagent"

	"quant-trading-v2/internal/llm"
)

// rebuildIndex 从事件板块重建个股→板块倒排索引
func (e *Engine) rebuildIndex(events []newsagent.NewsEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	idx := make(map[string][]string)
	for _, ev := range events {
		allSectors := append([]string{}, ev.Sectors...)
		allSectors = append(allSectors, ev.UpstreamSectors...)
		allSectors = append(allSectors, ev.DownstreamSectors...)
		for _, s := range allSectors {
			codes, _ := llm.ResolveStocks(ev.RelatedStocks)
			for _, code := range codes {
				if !contains(idx[code], s) {
					idx[code] = append(idx[code], s)
				}
			}
		}
	}

	if len(idx) > 0 {
		e.stockSectorIdx = idx
	}
}

// sectorStocks 根据板块名称查询成分股列表，从 scanner 获取板块代码后调用市场 API。
func (e *Engine) sectorStocks(sectorName string) []string {
	if e.scanner == nil {
		return nil
	}
	sectors := e.scanner.FindSectorsByNames([]string{sectorName})
	for _, s := range sectors {
		if s.Name == sectorName {
			// 从scanner获取成分股
			stocks, err := e.marketAPI.GetSectorStocks(s.Code, 30)
			if err != nil {
				return nil
			}
			codes := make([]string, len(stocks))
			for i, st := range stocks {
				codes[i] = st.Code
			}
			return codes
		}
	}
	return nil
}

// contains 检查字符串切片中是否包含指定元素。
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
