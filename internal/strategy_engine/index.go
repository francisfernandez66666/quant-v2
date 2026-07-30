package strategy_engine

import (
	"log"
	"strings"

	"quant-trading-v2/internal/newsagent"

	"quant-trading-v2/internal/llm"
)

// rebuildIndex 从事件板块重建个股→板块倒排索引
func (e *Engine) rebuildIndex(bullEvents, bearEvents []newsagent.NewsEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 重建索引
	idx := make(map[string][]string)
	for _, ev := range bullEvents {
		for _, s := range ev.Sectors {
			codes, _ := llm.ResolveStocks(ev.Stocks)
			for _, code := range codes {
				if !contains(idx[code], s) {
					idx[code] = append(idx[code], s)
				}
			}
		}
	}
	for _, ev := range bearEvents {
		for _, s := range ev.Sectors {
			codes, _ := llm.ResolveStocks(ev.Stocks)
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

// rebuildD1Scores 从事件重建D1评分矩阵，端口v1 rebuildLLMD1Scores。
// 利好→score原值计入l1Score；利空→l1Blocked标记
func (e *Engine) rebuildD1Scores(events []newsagent.NewsEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.l1Gen++
	blocked := make(map[string]bool)
	scores := make(map[string]float64)

	for _, ev := range events {
		if len(ev.Sectors) == 0 {
			continue
		}
		dir := ev.Direction
		switch {
		case strings.HasPrefix(dir, "利空"):
			dir = "利空"
		case strings.HasPrefix(dir, "中性"):
			dir = "中性"
		default:
			dir = "利好"
		}

		affectedStocks := make(map[string]bool)

		// LLM指名个股（最高优先级）
		if len(ev.Stocks) > 0 {
			codes, _ := llm.ResolveStocks(ev.Stocks)
			for _, code := range codes {
				affectedStocks[code] = true
			}
		}

		// 板块→个股映射
		for _, secField := range ev.Sectors {
			for _, sec := range strings.Split(secField, "/") {
				sec = strings.TrimSpace(sec)
				if sec == "" {
					continue
				}
				codes := e.sectorStocks(sec)
				for _, code := range codes {
					affectedStocks[code] = true
				}
			}
		}

		for code := range affectedStocks {
			switch dir {
			case "利空":
				blocked[code] = true
			case "中性":
				if s := ev.Score * 0.5; s > scores[code] {
					scores[code] = s
				}
			default:
				if ev.Score > scores[code] {
					scores[code] = ev.Score
				}
			}
		}
	}

	e.l1Blocked = blocked
	e.l1Score = scores

	log.Printf("[strategy_engine] D1评分重建: %d个股有评分, %d个股利空阻塞", len(scores), len(blocked))
}

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

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
