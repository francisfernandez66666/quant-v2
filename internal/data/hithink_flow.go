// hithink_flow.go — §ENH-2(2026-09-19) 个股资金流向第二源：同花顺官方 capital-flow/snapshot。
// 背景：GetStockMoneyFlow 长期东财单源（注释自认"无第二源，需补第二源"）；咨询链
// GetRealtimeQuoteWithFlow 的 NetInflow 也只有东财（f62），东财熔断即 HasFlow=false 缺数。
// 真网侦察结论（同日）：同花顺数据中心公开页有 chameleon/hexin-v 反爬（本机直抓返空/502），
// 不可靠；改用官方 API GET /api/a-share/capital-flow/snapshot（单票 thscode，
// 超大/大/中/小四档 inflow/outflow/net，官方口径"资金金额单位为元、缺数返回 null 不转 0"）。
// 主力口径对齐：NetInflow = 超大单净额 + 大单净额，与东财 f62 的"主力=超大+大"一致；
// 但大中小单分档阈值两家各自定义，跨源核对允许小幅差异（§FIX-5 审计容差内）。
// English: second source for per-stock money flow via the official THS (hithink) capital-flow
// snapshot endpoint. Main-force net = super-large + large net (same semantics as EastMoney f62);
// amounts are in CNY; null leg nets mean "no data" and surface as errors, never 0.
package data

import (
	"fmt"
	"log"
	"net/url"
	"time"
)

// HithinkFlowLeg 单档资金三元组（单位=元；指针区分上游 null=缺数与 0=真零，§FIX-9e 口径）。
// HithinkFlowLeg is one order-size bucket (CNY); pointers keep upstream null (no data) distinct from 0.
type HithinkFlowLeg struct {
	InflowAmount  *float64 `json:"inflow_amount"`  // 流入金额（元），缺数 null
	OutflowAmount *float64 `json:"outflow_amount"` // 流出金额（元），缺数 null
	NetAmount     *float64 `json:"net_amount"`     // 净额（元），缺数 null
}

// HithinkCapitalFlowSnapshot capital-flow/snapshot 的 data 容器（timestamp 毫秒 + 四档资金）。
type HithinkCapitalFlowSnapshot struct {
	Timestamp  int64          `json:"timestamp"` // 最新有效数据时间（毫秒），可为 0/null
	SuperLarge HithinkFlowLeg `json:"super_large"`
	Large      HithinkFlowLeg `json:"large"`
	Medium     HithinkFlowLeg `json:"medium"`
	Small      HithinkFlowLeg `json:"small"`
}

// StockMoneyFlow 拉取同花顺官方个股资金流快照并装配为 CapitalFlow（全部字段单位=元）。
// code 为裸码或带后缀码均可（内部统一补交易所后缀）；超大/大两档净额任一为 null
// 即视为缺数返回错误（主力净流入不可半缺拼凑），由调用方继续降级；中小单档
// null→0 与东财解析既有口径一致（消费方以 in-out 差值展示，不据此判"有无"）。
// StockMoneyFlow fetches the official THS capital-flow snapshot as a CapitalFlow (all values in CNY).
// Missing super-large/large nets are treated as no-data errors (never half-fabricated totals).
func (c *HithinkClient) StockMoneyFlow(code string) (*CapitalFlow, error) {
	p := url.Values{}
	p.Set("thscode", ExchangeSuffix(stripSuffix(code)))
	var out HithinkCapitalFlowSnapshot
	if err := c.get("/api/a-share/capital-flow/snapshot", p, &out); err != nil {
		return nil, err
	}
	if out.SuperLarge.NetAmount == nil || out.Large.NetAmount == nil {
		return nil, fmt.Errorf("hithink moneyflow: %s 主力两档净额缺数(上游 null)", code)
	}
	cf := &CapitalFlow{
		Code:      stripSuffix(code),
		NetInflow: *out.SuperLarge.NetAmount + *out.Large.NetAmount, // 主力=超大+大（同东财 f62 口径）
		// 四档明细直读上游"元"值：null→0 仅供展示差值，判缺数逻辑不经过这里。
		SuperLargeIn:  derefFlow(out.SuperLarge.InflowAmount),  // 超大单流入（元）
		SuperLargeOut: derefFlow(out.SuperLarge.OutflowAmount), // 超大单流出（元）
		LargeIn:       derefFlow(out.Large.InflowAmount),       // 大单流入（元）
		LargeOut:      derefFlow(out.Large.OutflowAmount),      // 大单流出（元）
		MediumIn:      derefFlow(out.Medium.InflowAmount),      // 中单流入（元）
		MediumOut:     derefFlow(out.Medium.OutflowAmount),     // 中单流出（元）
		SmallIn:       derefFlow(out.Small.InflowAmount),       // 小单流入（元）
		SmallOut:      derefFlow(out.Small.OutflowAmount),      // 小单流出（元）
		// §修复 EM-FFLOW(20260920)：本源同时有 in/out，落库时把四档净额一并算好，
		// 与东财 fflow（只给净额）口径统一——消费方一律读 *Net，无需区分来源。
		SuperLargeNet: derefFlow(out.SuperLarge.InflowAmount) - derefFlow(out.SuperLarge.OutflowAmount),
		LargeNet:      derefFlow(out.Large.InflowAmount) - derefFlow(out.Large.OutflowAmount),
		MediumNet:     derefFlow(out.Medium.InflowAmount) - derefFlow(out.Medium.OutflowAmount),
		SmallNet:      derefFlow(out.Small.InflowAmount) - derefFlow(out.Small.OutflowAmount),
		Time:          time.Now(), // 兜底为本地抓取时间
	}
	// 上游给出有效数据时间戳（毫秒）时以它为准，避免把"数据时刻"混同为"抓取时刻"。
	if out.Timestamp > 0 {
		cf.Time = time.UnixMilli(out.Timestamp)
	}
	return cf, nil
}

// derefFlow 解引用资金流字段：null→0（仅供明细展示档使用，判缺数逻辑不经过这里）。
func derefFlow(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// hithinkStockMoneyFlow 便捷入口：每次按环境变量 key 建客户端再取数（key 未配置快速失败）。
// 咨询/资金流均为低频路径，逐次建客户端代价可忽略；限流由 hithink 客户端内建限速器覆盖。
func hithinkStockMoneyFlow(code string) (*CapitalFlow, error) {
	hc, err := NewHithinkClient()
	if err != nil {
		return nil, err
	}
	cf, err := hc.StockMoneyFlow(code)
	if err != nil {
		return nil, err
	}
	log.Printf("[market] 资金流走同花顺官方第二源: %s 主力净流入 %.0f 元", cf.Code, cf.NetInflow)
	return cf, nil
}
