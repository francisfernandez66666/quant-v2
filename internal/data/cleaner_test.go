// cleaner_test.go — §D1 护栏1 个股归因链路对清洗器的两种标签格式的解析锁。
//
// 钉死两件事：
//
//	① 「名称(代码)」（板块成分股传播注入格式）可被 Clean 解析——否则板块级事件清洗后
//	   CleanedStocks 永久为空，个股分流断流（e2e 曾实测复现）；
//	② 「名称|代码」（CleanBatch 自身输出格式）幂等可再解析——护栏1 会把已清洗条目合并回
//	   RelatedStocks，下一轮 Stage2 再次 CleanBatch 时必须原样保留而非丢弃。
//
// English: locks for the two label formats the guardrail-1 attribution chain feeds back into
// Clean — "name(code)" constituent labels and "name|code" already-cleaned entries (idempotency).
package data

import "testing"

func newTestCleaner() *StockCleaner {
	// 直接构造（不经过 NewStockCleaner 的外呼初始化）；api=nil 使 CleanBatch 空表重试分支短路
	return &StockCleaner{
		nameToCode: map[string]string{"宁德时代": "300750", "中际旭创": "300308"},
		codeToName: map[string]string{"300750": "宁德时代", "300308": "中际旭创"},
	}
}

// TestCleanNameParenCode 「名称(代码)」：代码命中优先，代码不在清单时回退名称。
func TestCleanNameParenCode(t *testing.T) {
	c := newTestCleaner()
	name, code, err := c.Clean("中际旭创(300308)")
	if err != nil || name != "中际旭创" || code != "300308" {
		t.Fatalf("名称(代码)应解析成功，得 (%q,%q,%v)", name, code, err)
	}
	// 名称与括号代码不一致时以代码为硬事实
	if name, code, _ := c.Clean("瞎写名(300308)"); name != "中际旭创" || code != "300308" {
		t.Fatalf("应以括号内代码为准，得 (%q,%q)", name, code)
	}
	// 两查都落空 → 丢弃
	if _, _, err := c.Clean("不存在的股(999999)"); err == nil {
		t.Fatal("双落空应报错丢弃")
	}
}

// TestCleanNamePipeCodeIdempotent 「名称|代码」幂等：CleanBatch 输出再进 Clean 不被丢弃。
func TestCleanNamePipeCodeIdempotent(t *testing.T) {
	c := newTestCleaner()
	name, code, err := c.Clean("宁德时代|300750")
	if err != nil || name != "宁德时代" || code != "300750" {
		t.Fatalf("名称|代码 应幂等解析，得 (%q,%q,%v)", name, code, err)
	}
	// 管道右侧带交易所前后缀也认
	if name, code, err := c.Clean("宁德时代|SZ300750"); err != nil || name != "宁德时代" || code != "300750" {
		t.Fatalf("名称|SZ代码 应解析，得 (%q,%q,%v)", name, code, err)
	}
	// 代码不在清单时回退按名称查
	if name, code, err := c.Clean("中际旭创|999999"); err != nil || name != "中际旭创" || code != "300308" {
		t.Fatalf("代码落空应回退名称，得 (%q,%q,%v)", name, code, err)
	}
	// CleanBatch 全链路：混合格式（裸代码/名称/已清洗）一次清洗后全部保留
	got := c.CleanBatch([]string{"300750", "中际旭创", "宁德时代|300750", "中际旭创(300308)"})
	if len(got) != 4 {
		t.Fatalf("混合格式应 4 条全清洗成功，得 %v", got)
	}
	for _, s := range got {
		if s != "宁德时代|300750" && s != "中际旭创|300308" {
			t.Fatalf("输出应为标准 名称|代码 格式，得 %q", s)
		}
	}
}
