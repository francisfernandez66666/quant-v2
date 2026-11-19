// flow_money.go — §FLOW-MONEY 云端大盘/板块资金流取数（2026-11-19，owner 裁决改云端实现）。
//
// 背景：原方案资金流腿等 QMT 原生探针（zjbyNetInflow），但物理机 Python 3.15 无 cp315 扩展、
// 内置构建无 downloader，探针跑不通。owner 裁决改云端实现——东财 fflow 接口已实测：
//   - 大盘 fflow daykline secid=1.000001 klt=101：逐日主力净额（元），有历史（2026-11-19 实测 10 天）；
//     klt=1：当日分时累计，最后一根=当日累计主力净额（元）。
//   - 板块 fflow daykline secid=90.BKxxxx klt=101：逐日主力净额（元），有历史。
//
// 纪律（改动前必读）：
//   - secid 必须显式：大盘 1.000001（indexSecID），板块 90.BKxxxx。绝不裸用 000001
//     （secID 会拼成 0.000001=平安银行，§INDEX-BLIND / §BUG-INDEX）。
//   - 单位=元，当日累计（klt=1）或逐日增量（klt=101），与 parseMoneyFlowHistory 逐列同构，直接复用解析。
//   - 失败即弃权（返回 error），绝不拿 0 或旧值冒充资金流。
//
// English: cloud-side index/sector money-flow fetch from EastMoney fflow (secid explicit, CNY).
package data

import (
	"fmt"
	"io"
)

// MarketMoneyFlow 按显式 secid 取 fflow 资金流序列（主力净额，元）。
// secid 由调用方给全：大盘 indexSecID("000001")=1.000001、板块 "90.BK0420"。
// klt=1 → 当日分时（末尾一根=当日累计额）；klt=101 → 逐日历史（每根=当日净额）。
// n=请求根数（klt=1 时给 0=当日全部点；klt=101 时给天数）。
// 解析复用 parseMoneyFlowHistory（逐列 f51..f56、缺数整体弃权），失败即返回 error。
func (m *MarketAPI) MarketMoneyFlow(secid string, klt int, n int) ([]FlowDay, error) {
	if secid == "" {
		return nil, fmt.Errorf("market money flow: empty secid")
	}
	url := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6&fields2=%s&klt=%d&lmt=%d",
		secid, emFFlowFields2, klt, n)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("market money flow http (%s): %v", secid, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("market money flow read (%s): %v", secid, err)
	}
	return parseMoneyFlowHistory(body, secid)
}

// IndexMoneyFlowToday 大盘（上证）当日累计主力净额（元）。取 klt=1 分时序列末尾一根。
func (m *MarketAPI) IndexMoneyFlowToday() (*FlowDay, error) {
	series, err := m.MarketMoneyFlow(indexSecID("000001"), emFFlowKLType, emFFlowLimitAll)
	if err != nil || len(series) == 0 {
		if err == nil {
			err = fmt.Errorf("empty index money flow series")
		}
		return nil, err
	}
	last := series[len(series)-1]
	return &last, nil
}

// IndexMoneyFlowHistory 大盘（上证）逐日主力净额历史（元），最近 n 个交易日。
func (m *MarketAPI) IndexMoneyFlowHistory(n int) ([]FlowDay, error) {
	return m.MarketMoneyFlow(indexSecID("000001"), emFFlowDayKLType, n)
}

// SectorMoneyFlowHistory 板块（BK 代码，如 BK0420）逐日主力净额历史（元），最近 n 个交易日。
// secid 显式 "90.<bkcode>"——板块在市场 90，不能走个股/指数映射。
func (m *MarketAPI) SectorMoneyFlowHistory(bkCode string, n int) ([]FlowDay, error) {
	if bkCode == "" {
		return nil, fmt.Errorf("sector money flow: empty bk code")
	}
	return m.MarketMoneyFlow("90."+bkCode, emFFlowDayKLType, n)
}
