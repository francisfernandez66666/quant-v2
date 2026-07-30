package newsagent

import (
	"context"
	"log"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

type Agent struct {
	marketAPI  *data.MarketAPI
	llmClient  *llm.Client
	tracker    *tracker
	dataDir    string
}

func New(marketAPI *data.MarketAPI, llmClient *llm.Client, dataDir string) *Agent {
	return &Agent{
		marketAPI: marketAPI,
		llmClient: llmClient,
		tracker:   newTracker(dataDir),
		dataDir:   dataDir,
	}
}

func (a *Agent) Start() error {
	log.Printf("[newsagent] 已启动, tracker=%s", a.tracker.filePath)
	return nil
}

func (a *Agent) Stop() error {
	return a.tracker.save()
}

// Process 完整流程：追回新闻 → Stage1初筛 → Stage2全量分析
func (a *Agent) Process(ctx context.Context, since time.Time) (*AnalysisResult, error) {
	t0 := time.Now()

	rawNews := a.fetchCatchUp()
	if len(rawNews) == 0 {
		log.Printf("[newsagent] 无新新闻 (since=%s)", since.Format("01-02 15:04"))
		return &AnalysisResult{
			CatchUpSince: since,
		}, nil
	}

	log.Printf("[newsagent] 追回 %d 条新闻 (%v)", len(rawNews), time.Since(t0))

	titles := make([]string, len(rawNews))
	for i, n := range rawNews {
		titles[i] = n.Title
	}

	stage1t := time.Now()
	indices, err := a.classifyMaterial(titles)
	if err != nil {
		log.Printf("[newsagent] Stage1失败: %v, 全部视为有价值", err)
		indices = make([]int, len(titles))
		for i := range titles {
			indices[i] = i
		}
	}
	log.Printf("[newsagent] Stage1初筛完成 (%v)", time.Since(stage1t))

	var materialItems []data.NewsItem
	for _, idx := range indices {
		materialItems = append(materialItems, rawNews[idx])
	}

	if a.llmClient == nil || len(materialItems) == 0 {
		events := make([]NewsEvent, len(materialItems))
		for i, item := range materialItems {
			events[i] = NewsEvent{
				Title:      item.Title,
				Content:    item.Content,
				Datetime:   item.Datetime,
				Source:     item.Source,
				IsMaterial: true,
			}
		}
		return &AnalysisResult{
			Events:        events,
			RawCount:      len(rawNews),
			MaterialCount: len(materialItems),
			CatchUpSince:  since,
		}, nil
	}

	stage2t := time.Now()
	events := a.analyzeDeep(materialItems)
	log.Printf("[newsagent] Stage2全量分析完成 (%v)", time.Since(stage2t))

	_ = a.tracker.save()

	log.Printf("[newsagent] 流程完成: %d条原始 → %d条初筛 → %d条分析 (%v)",
		len(rawNews), len(materialItems), len(events), time.Since(t0))

	return &AnalysisResult{
		Events:        events,
		RawCount:      len(rawNews),
		MaterialCount: len(events),
		CatchUpSince:  since,
	}, nil
}
