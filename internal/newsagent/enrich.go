package newsagent

import (
	"log"
	"sync"

	"quant-trading-v2/internal/data"
)

// maxEnrichPerRound 单轮正文抓取条数上限，避免追回大包时对文章页洪峰请求。
const maxEnrichPerRound = 40

// minEnrichLen 已有正文摘要达到该长度即跳过抓取，减少重复请求。
const minEnrichLen = 200

// EnrichContents 并发抓取新闻原文正文回填 Content（保留原摘要兜底，失败不阻断流水线）。
func (a *Agent) EnrichContents(items []data.NewsItem) []data.NewsItem {
	if a.marketAPI == nil {
		return items
	}
	todo := 0
	for i := range items {
		if items[i].URL != "" && len(items[i].Content) < minEnrichLen {
			todo++
		}
	}
	if todo > maxEnrichPerRound {
		todo = maxEnrichPerRound
	}
	if todo == 0 {
		return items
	}

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	done := 0
	for i := range items {
		if items[i].URL == "" || len(items[i].Content) >= minEnrichLen {
			continue
		}
		if done >= todo {
			continue
		}
		done++
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := a.marketAPI.GetArticle(items[i].URL)
			if err != nil {
				log.Printf("[newsagent] 正文抓取失败 %s: %v", items[i].URL, err)
				return
			}
			if len(body) > len(items[i].Content) {
				items[i].Content = body
			}
		}(i)
	}
	wg.Wait()
	return items
}
