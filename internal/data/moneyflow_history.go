// moneyflow_history.go — §FLOW-HIST 个股资金流**日线历史**解析（2026-10-25）。
//
// 背景：5 交易日净流入是 dynamic 判断新因子（docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.2），
// 但既有 GetStockMoneyFlow 只取当日值——东财 fflow daykline 的 klines 数组里**历史行本来就在
// 响应里，旧解析器只读最后一行**。本文件把日线序列解析出来，供引擎按 (code, 交易日) 缓存后
// 挂到信号上（净流入/流通市值归一化）。
//
// 实测钉死（2026-10-25，600519）：
//
//	GET https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get
//	  ?secid=1.600519&fields1=f1..f6&fields2=f51..f56&klt=101&lmt=6
//	→ HTTP 200，klines 逐日 6 列：日期,主力,小单,中单,大单,超大单（元）：
//	  "2026-10-16,468661168.0,-454626.0,-468206560.0,425044128.0,43617040.0"
//	  …（周末/休市无行；行宽与既有 parseMoneyFlow 的 f51..f56 逐列同构）
//
// 缺数纪律（与 §FIX-9e 一致）：任何一行字段短缺/非数字 → 整体返回错误（缺一行即整体弃权，
// 不半缺拼凑）；上游行数不足请求天数时按已有行返回（新股票/断档是合法形态），由消费方定夺。
//
// English: per-stock daily capital-flow history. The historical rows were always in the
// EastMoney fflow daykline response; the old parser only read the last row. Any malformed
// row fails the whole call (never half-fabricate); fewer rows than requested is legal.
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
)

// emFFlowDayKLType klt=101 → 日线（与 klt=1 当日分时同端点不同周期）。
// English: klt=101 selects the daily capital-flow series.
const emFFlowDayKLType = 101

// FlowDay 一日资金流快照（单位=元；Date 保留上游原样，形如 "2026-10-16"）。
// English: one day of capital flow (CNY); Date kept verbatim from upstream.
type FlowDay struct {
	Date          string  // 上游日期（"2026-10-16"）
	MainNet       float64 // 主力净额（元）= 大单+超大单（与 CapitalFlow.NetInflow 同口径）
	SmallNet      float64 // 小单净额（元）
	MediumNet     float64 // 中单净额（元）
	LargeNet      float64 // 大单净额（元）
	SuperLargeNet float64 // 超大单净额（元）
}

// SumMainNet 近 N 日主力净额累计（元）。行数不足按已有行计（消费方自行判定是否够用）。
// English: cumulative main-force net over the given rows (CNY).
func SumMainNet(days []FlowDay) float64 {
	var sum float64
	for _, d := range days {
		sum += d.MainNet
	}
	return sum
}

// parseMoneyFlowHistory 解析东财日线资金流 JSON 为逐日序列。
// 与 parseMoneyFlow（当日值）**并存不替代**：那条路径的既有回归一行不动。
// English: parses the daily capital-flow JSON into a per-day series; coexists with (does
// not replace) the existing single-day parser.
func parseMoneyFlowHistory(body []byte, code string) ([]FlowDay, error) {
	var raw struct {
		Data struct {
			KLines []string `json:"klines"` // 资金流 CSV 行数组（逐日）
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow-history json: %v", err)
	}
	if len(raw.Data.KLines) == 0 {
		return nil, fmt.Errorf("eastmoney: no moneyflow-history data for %s", code)
	}
	out := make([]FlowDay, 0, len(raw.Data.KLines))
	for i, line := range raw.Data.KLines {
		parts := strings.Split(line, ",")
		if len(parts) < emFFlowRowFields {
			// 缺一行即整体弃权：不半缺拼凑（§FIX-9e 缺数语义）。
			return nil, fmt.Errorf("eastmoney: moneyflow-history row %d fields too short (%d, want >=%d)",
				i, len(parts), emFFlowRowFields)
		}
		date := strings.TrimSpace(parts[0])
		if date == "" {
			return nil, fmt.Errorf("eastmoney: moneyflow-history row %d empty date", i)
		}
		main, ok1 := parseFlowNum(parts[1])
		small, ok2 := parseFlowNum(parts[2])
		medium, ok3 := parseFlowNum(parts[3])
		large, ok4 := parseFlowNum(parts[4])
		superLarge, ok5 := parseFlowNum(parts[5])
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			// 非数字（上游 null/占位）→ 整体弃权，绝不把缺数当 0。
			return nil, fmt.Errorf("eastmoney: moneyflow-history row %d (%s) non-numeric field", i, date)
		}
		out = append(out, FlowDay{
			Date:          date,
			MainNet:       main,
			SmallNet:      small,
			MediumNet:     medium,
			LargeNet:      large,
			SuperLargeNet: superLarge,
		})
	}
	return out, nil
}

// parseFlowNum 严格数字解析：空串/非数字/null 一律 ok=false（缺数不当 0）。
// English: strict numeric parse; empty/non-numeric/null → ok=false (never 0).
func parseFlowNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// getEastMoneyMoneyFlowHistory 东财日线资金流历史主源（§FLOW-HIST）。
// 主机用 push2his（data.eastmoney.com 资金流向页同款历史主机；2026-10-25 实测可达，
// 同族 push2 在沙盒外探不可达——生产主机可达性由调用方监控弃权率，R8/R9 已入文档）。
// **没有第二源**：同花顺 capital-flow 只有当日快照，拿它累加伪造历史等于造数据（方案明令禁止），
// 故失败即返回错误，由上层走弃权。
// English: daily capital-flow history from push2his. No second source exists (THS is
// snapshot-only); failure returns an error and the caller abstains — never fabricate.
func (m *MarketAPI) getEastMoneyMoneyFlowHistory(code string, days int) ([]FlowDay, error) {
	if days <= 0 {
		days = 5
	}
	sid := secID(code)
	url := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6&fields2=%s&klt=%d&lmt=%d",
		sid, emFFlowFields2, emFFlowDayKLType, days)
	EastMoneyLimiter.Wait() // §FLOW-HIST R8：日线端点纳入同一限速器，超限即弃权不硬打
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow-history http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow-history read: %v", err)
	}
	days5, err := parseMoneyFlowHistory(body, code)
	if err != nil {
		log.Printf("[market] 日线资金流历史解析失败(将弃权): %s %v", code, err)
		return nil, err
	}
	return days5, nil
}
