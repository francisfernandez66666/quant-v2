package newsagent

import (
	"log"
	"time"

	"quant-trading-v2/internal/data"
)

// maxCatchUpPages 最多追回页数（同花顺20条/页 × 25页 = 500条）
const maxCatchUpPages = 25

// fetchCatchUp 追回未读新闻：多源拉取（同花顺分页 + 财联社 + 新浪兜底），
// 统一去重并标记为已见，最后并发抓取正文回填。返回去重后的新闻列表。
// force=true 时忽略历史已见标题（仅按本轮 seen 打重），用于"手动 LLM 补推"重跑分析。
func (a *Agent) fetchCatchUp(force bool) []data.NewsItem {
	var all []data.NewsItem
	// seen 记录本轮已见过的标题（截断到60字符），跨源去重
	seen := make(map[string]bool)

	// 主源：同花顺（支持分页，追回量大）
	thsItems := a.fetchTHSPages(seen, force)
	all = append(all, thsItems...)

	// 主源：财联社电报（自带正文，覆盖标题党短板）
	clsItems := a.fetchCLSOnce(seen, force)
	all = append(all, clsItems...)

	// 新浪（只1页，去重）：补充视角，始终尝试拉取（不再受主源数量门槛限制）
	sinaItems := a.fetchSinaOnce(seen, force)
	all = append(all, sinaItems...)

	// 标记所有追回的新闻为"已见"，防止下次轮询重复拉取
	titles := make([]string, len(all))
	times := make([]string, len(all))
	for i, n := range all {
		titles[i] = n.Title
		times[i] = n.Datetime
	}
	a.tracker.BulkMarkSeen(titles, times)

	// 并发抓取原文正文（失败保留摘要，不阻断流水线）
	a.EnrichContents(all)

	return all
}

// FetchForce 强制拉取最近新闻（忽略 tracker 历史去重），供"手动 LLM 补推"重跑分析使用。
func (a *Agent) FetchForce() []data.NewsItem {
	return a.fetchCatchUp(true)
}

// fetchTHSPages 从同花顺逐页拉取新闻（每页 20 条），只保留未读条目。
// 当前页全部已读则停止追页，标识已达到上次已见位置。
// force=true 时忽略历史去重（补推场景），仅按本轮 seen 打重，并追完所有页。
func (a *Agent) fetchTHSPages(seen map[string]bool, force bool) []data.NewsItem {
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

		// 页内去重：跳过本轮已见或（非force时）历史已处理的标题
		var fresh []data.NewsItem
		for _, item := range items {
			key := truncateStr(item.Title, 60)
			if seen[key] || (!force && a.tracker.IsSeen(item.Title)) {
				continue
			}
			seen[key] = true
			fresh = append(fresh, item)
		}

		all = append(all, fresh...)

		// 如果当前页没有未读新闻了，说明应该停止追页（force 时仍继续追完所有页）
		if !force && len(fresh) == 0 {
			break
		}

		// 页间隔，避免对同花顺造成请求压力
		time.Sleep(200 * time.Millisecond)
	}

	log.Printf("[newsagent] 同花顺追回 %d 条 (共 %d 页)", len(all), (len(all)+19)/20)
	return all
}

// fetchSinaOnce 拉取一页新浪新闻（20 条），去重后返回。
func (a *Agent) fetchSinaOnce(seen map[string]bool, force bool) []data.NewsItem {
	items, err := a.marketAPI.GetSinaNews(20)
	if err != nil {
		log.Printf("[newsagent] 新浪 err: %v", err)
		return nil
	}
	var fresh []data.NewsItem
	for _, item := range items {
		key := truncateStr(item.Title, 60)
		if seen[key] || (!force && a.tracker.IsSeen(item.Title)) {
			continue
		}
		seen[key] = true
		fresh = append(fresh, item)
	}
	return fresh
}

// fetchCLSOnce 拉取一页财联社电报（正文自带），去重后返回。
func (a *Agent) fetchCLSOnce(seen map[string]bool, force bool) []data.NewsItem {
	items, err := a.marketAPI.GetCLSNews(20)
	if err != nil {
		log.Printf("[newsagent] 财联社 err: %v", err)
		return nil
	}
	var fresh []data.NewsItem
	for _, item := range items {
		key := truncateStr(item.Title, 60)
		if seen[key] || (!force && a.tracker.IsSeen(item.Title)) {
			continue
		}
		seen[key] = true
		fresh = append(fresh, item)
	}
	log.Printf("[newsagent] 财联社追回 %d 条", len(fresh))
	return fresh
}

// truncateStr 按字符数截断字符串（最多 maxLen 个字符），用于标题去重键归一。
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}
