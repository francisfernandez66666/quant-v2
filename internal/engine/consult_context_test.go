package engine

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/newsagent"
)

// TestBuildConsultContextNoStock 消息中没有股票名称/代码时应返回空串（调用方提示指明股票）。
func TestBuildConsultContextNoStock(t *testing.T) {
	e := &Engine{}
	if ctx := e.buildConsultContext("今天大盘怎么样"); ctx != "" {
		t.Fatalf("无股票消息应返回空串, got: %s", ctx)
	}
}

// TestBuildConsultContextHasPureCode 消息含 6 位代码时应构建上下文（含代码与数据头）。
// 不校验网络字段是否成功，仅确保框架与股票条目生成。
func TestBuildConsultContextHasPureCode(t *testing.T) {
	e := &Engine{
		marketAPI: nil,
		newsAgent: newsagent.New(nil, nil, nil, ""),
	}
	ctx := e.buildConsultContext("帮我分析 600580 今天怎么样")
	if ctx == "" {
		t.Fatal("含 6 位代码的消息应生成上下文")
	}
	if !strings.Contains(ctx, "600580") {
		t.Fatalf("上下文应包含股票代码 600580, got: %s", ctx)
	}
	if !strings.Contains(ctx, "数据获取时间") {
		t.Fatal("上下文应包含数据获取时间头")
	}
	if !strings.Contains(ctx, "严禁编造") {
		t.Fatal("上下文应包含禁止编造数字的约束")
	}
}

// TestConsultNoStockPrompt 提示词应引导用户指明股票并禁止编造数字。
func TestConsultNoStockPrompt(t *testing.T) {
	if !strings.Contains(consultNoStockPrompt, "600580") {
		t.Fatal("提示词应示例股票格式")
	}
	if !strings.Contains(consultNoStockPrompt, "没有任何该股的实时行情数据") {
		t.Fatal("提示词应说明当前无个股数据")
	}
	if !strings.Contains(consultNoStockPrompt, "严禁编造") {
		t.Fatal("提示词应禁止编造具体数字")
	}
}

// TestAuditNumbersFabricated 模型编造的金额/成交量应被替换为数据缺失标注。
func TestAuditNumbersFabricated(t *testing.T) {
	trusted := collectTrustedNumbers(
		"现价 36.86元 涨跌幅-3.50% 主力净流入 -22200.00万元 成交量 45580000股 成交额 601000000元",
		"用户描述：全天振幅12个点",
	)
	reply := "主力净流入仅0.52万元，成交额61.9亿元，撤单达2.3万笔，明日量能应大于1.2亿股。现价36.86元。"
	got := auditNumbers(reply, trusted)
	for _, want := range []string{"[数据缺失]", "现价36.86元"} {
		if !strings.Contains(got, want) {
			t.Errorf("期望含 %q, got: %s", want, got)
		}
	}
	for _, banned := range []string{"0.52万元", "61.9亿元", "2.3万笔", "1.2亿股"} {
		if strings.Contains(got, banned) {
			t.Errorf("编造数字应被替换, 仍出现 %q: %s", banned, got)
		}
	}
}

// TestAuditNumbersKeepsGrounded 有出处的数字（注入数据/用户描述）应原样保留。
func TestAuditNumbersKeepsGrounded(t *testing.T) {
	trusted := collectTrustedNumbers(
		"主力净流入 -22200.00万元 成交量 45580000股",
		"用户描述：振幅12个点",
	)
	reply := "主力净流入-22200万元，振幅12个点，换手率1.2%"
	got := auditNumbers(reply, trusted)
	if !strings.Contains(got, "-22200万元") {
		t.Errorf("有出处的净流入应保留, got: %s", got)
	}
	if !strings.Contains(got, "振幅12个点") {
		t.Errorf("用户描述的数字应保留, got: %s", got)
	}
	if strings.Contains(got, "1.2%") {
		t.Errorf("无出处的百分比应被替换, got: %s", got)
	}
}

// TestAuditNumbersFiltersPercentTime 编造的百分比/倍数应被替换，时间/日期/股票代码不应被替换。
func TestAuditNumbersFiltersPercentTime(t *testing.T) {
	// 可信来源只有现价36.86元与振幅12%
	trusted := collectTrustedNumbers("现价 36.86元 振幅12.00%")
	reply := "涨跌幅-3.5%，板块60%个股同向，量比放大3倍，14:30拉升，代码600580，振幅12%"
	got := auditNumbers(reply, trusted)
	if strings.Contains(got, "60%") {
		t.Errorf("编造的百分比应被替换, got: %s", got)
	}
	if strings.Contains(got, "3倍") {
		t.Errorf("编造的倍数应被替换, got: %s", got)
	}
	if !strings.Contains(got, "14:30") || !strings.Contains(got, "600580") {
		t.Errorf("时间/代码不应被替换, got: %s", got)
	}
	if !strings.Contains(got, "振幅12%") {
		t.Errorf("有出处的百分比应保留, got: %s", got)
	}
}
