// concept_sync.go — §C1-V2 概念库每日同步（概念列表 → 成分股 → 清洗 → 全量替换）。
//
// 数据源：同花顺板块列表/成分（GetBoardList / GetBoardStocks）。同花顺概念代码以 308 开头，
// 行业板块以 881 开头；本任务只同步概念板块。成分接口单次最多约 1000 只，已足以覆盖大概念；
// 仍失败/截断的概念按状态表 error 留痕，覆盖率会自然下降，绝不把失败当“0 成员成功”。
//
// 依赖方向：data 不 import store；落库能力通过 ConceptStore 接口注入（*store.DB 实现）。
// English: §C1-V2 daily concept sync via THS (concept boards start with 308). Failures are recorded
// in the state table and lower coverage — never counted as a successful empty sync. The store is
// injected through an interface to keep data free of a store dependency.
package data

import (
	"fmt"
	"strings"
	"time"
)

// ConceptStore 概念库落库接口（由 *store.DB 实现）。
// English: persistence interface implemented by *store.DB.
type ConceptStore interface {
	ReplaceConceptMembers(conceptCode, conceptName, source string, tsCodes []string, updatedAt string) error
	UpsertConceptSyncState(conceptCode, conceptName, lastSuccess string, memberCount int, status, errMsg string) error
}

// ConceptSyncResult 一轮同步的汇总读数。
// English: summary of one sync run.
type ConceptSyncResult struct {
	Total       int    `json:"total"`
	Synced      int    `json:"synced"`
	Failed      int    `json:"failed"`
	Members     int    `json:"members"`
	LastSuccess string `json:"last_success"`
}

// SyncConcepts 拉取概念列表并逐概念全量替换成分。limit<=0 表示不限概念数（测试可传小值）。
// English: fetches the concept list and fully replaces each concept's members; limit<=0 = no cap.
func (dc *DataCoordinator) SyncConcepts(st ConceptStore, limit int) (ConceptSyncResult, error) {
	res := ConceptSyncResult{}
	if st == nil {
		return res, fmt.Errorf("概念库存储未注入")
	}
	hk := dc.ths
	if hk == nil {
		return res, fmt.Errorf("概念库同步需要同花顺客户端，当前不可用")
	}
	boards, err := hk.GetBoardList()
	if err != nil {
		return res, fmt.Errorf("概念列表拉取失败: %w", err)
	}
	concepts := make([]SectorInfo, 0, len(boards))
	for _, b := range boards {
		if strings.HasPrefix(strings.TrimSpace(b.Code), "308") && strings.TrimSpace(b.Name) != "" {
			concepts = append(concepts, b)
		}
	}
	if len(concepts) == 0 {
		// 兼容数据源调整：没有 308 前缀时退化为全量板块（仍只写有成员的板块）。
		concepts = boards
	}
	res.Total = len(concepts)
	if limit > 0 && len(concepts) > limit {
		concepts = concepts[:limit]
	}
	for _, b := range concepts {
		updatedAt := time.Now().Format(time.RFC3339)
		list, berr := hk.GetBoardStocks(b.Code, 1000)
		if berr != nil {
			res.Failed++
			_ = st.UpsertConceptSyncState(b.Code, b.Name, "", 0, "error", berr.Error())
			continue
		}
		codes := make([]string, 0, len(list))
		for _, s := range list {
			code := strings.TrimSpace(s.Code)
			if code == "" {
				continue
			}
			if !strings.Contains(code, ".") {
				code += ExchangeSuffix(code)
			}
			if code != "" {
				codes = append(codes, code)
			}
		}
		if err := st.ReplaceConceptMembers(b.Code, b.Name, "ths", codes, updatedAt); err != nil {
			res.Failed++
			_ = st.UpsertConceptSyncState(b.Code, b.Name, "", 0, "error", err.Error())
			continue
		}
		res.Synced++
		res.Members += len(codes)
		res.LastSuccess = updatedAt
		_ = st.UpsertConceptSyncState(b.Code, b.Name, updatedAt, len(codes), "ok", "")
	}
	return res, nil
}
