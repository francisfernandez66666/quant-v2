package newsagent

import (
	"log"
	"sync"

	"quant-trading-v2/internal/data"
)

// maxEnrichPerRound 单轮正文抓取条数上限，避免追回大包时对文章页洪峰请求。（Max articles to fetch per round, avoiding request floods on article pages.）
const maxEnrichPerRound = 40

// minEnrichLen 已有正文摘要达到该长度即跳过抓取，减少重复请求。（Existing digest longer than this skips fetching to reduce duplicate requests.）
const minEnrichLen = 200

// EnrichContents 并发抓取新闻原文正文回填 Content（保留原摘要兜底，失败不阻断流水线）。
// （EnrichContents concurrently fetches article bodies into Content; keeps existing digests as fallback and never blocks the pipeline.）
func (a *Agent) EnrichContents(items []data.NewsItem) []data.NewsItem {
	if a.marketAPI == nil {
		return items
	}
	// 统计本轮需要抓正文的条数：有 URL 且当前摘要过短（不足 minEnrichLen）
	todo := 0
	for i := range items {
		if items[i].URL != "" && len(items[i].Content) < minEnrichLen {
			todo++
		}
	}
	// 单轮抓取上限，防止追回大包时对文章页产生洪峰请求
	if todo > maxEnrichPerRound {
		todo = maxEnrichPerRound
	}
	if todo == 0 {
		return items
	}

	// 信号量限制并发抓取数为 4，避免瞬时大量并发请求
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	done := 0
	for i := range items {
		// 跳过无需抓取（无 URL 或摘要已足够）以及超过本轮上限的条目
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
				// 抓取失败保留原摘要，仅记日志，不阻断流水线
				log.Printf("[newsagent] 正文抓取失败 %s: %v", items[i].URL, err)
				return
			}
			// 仅当抓取到的正文更长时才覆盖摘要，避免降级替换
			if len(body) > len(items[i].Content) {
				items[i].Content = body
			}
		}(i)
	}
	wg.Wait()
	return items
}
