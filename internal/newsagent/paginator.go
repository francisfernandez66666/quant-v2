package newsagent

import (
	"log"
	"time"

	"quant-trading-v2/internal/data"
)

// maxCatchUpPages 最多追回页数（同花顺20条/页 × 25页 = 500条）
const maxCatchUpPages = 25

func (a *Agent) fetchCatchUp() []data.NewsItem {
	var all []data.NewsItem
	seen := make(map[string]bool)

	// 主源：同花顺（支持分页）
	thsItems := a.fetchTHSPages(seen)
	all = append(all, thsItems...)

	// 兜底：新浪（只1页，去重）
	if len(all) < 20 {
		sinaItems := a.fetchSinaOnce(seen)
		all = append(all, sinaItems...)
	}

	// 标记所有追回的新闻为"已见"
	titles := make([]string, len(all))
	times := make([]string, len(all))
	for i, n := range all {
		titles[i] = n.Title
		times[i] = n.Datetime
	}
	a.tracker.BulkMarkSeen(titles, times)

	return all
}

func (a *Agent) fetchTHSPages(seen map[string]bool) []data.NewsItem {
	var all []data.NewsItem

	for page := 1; page <= maxCatchUpPages; page++ {
		items, err := a.marketAPI.GetTonghuashunNewsPage(page, 20)
		if err != nil {
			log.Printf("[newsagent] 同花顺 page %d err: %v", page, err)
			break
		}
		if len(items) == 0 {
			break
		}

		var fresh []data.NewsItem
		for _, item := range items {
			key := truncateStr(item.Title, 60)
			if seen[key] || a.tracker.IsSeen(item.Title) {
				continue
			}
			seen[key] = true
			fresh = append(fresh, item)
		}

		all = append(all, fresh...)

		// 如果当前页没有未读新闻了，说明已经追到上次已见位置
		if len(fresh) == 0 {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	log.Printf("[newsagent] 同花顺追回 %d 条 (共 %d 页)", len(all), (len(all)+19)/20)
	return all
}

func (a *Agent) fetchSinaOnce(seen map[string]bool) []data.NewsItem {
	items, err := a.marketAPI.GetSinaNews(20)
	if err != nil {
		log.Printf("[newsagent] 新浪 err: %v", err)
		return nil
	}
	var fresh []data.NewsItem
	for _, item := range items {
		key := truncateStr(item.Title, 60)
		if seen[key] || a.tracker.IsSeen(item.Title) {
			continue
		}
		seen[key] = true
		fresh = append(fresh, item)
	}
	return fresh
}

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}
