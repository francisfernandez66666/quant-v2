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

// TestConsultNoStockPrompt 提示词应引导用户指明股票。
func TestConsultNoStockPrompt(t *testing.T) {
	if !strings.Contains(consultNoStockPrompt, "600580") {
		t.Fatal("提示词应示例股票格式")
	}
	if !strings.Contains(consultNoStockPrompt, "专业模式") {
		t.Fatal("提示词应说明专业模式")
	}
}
