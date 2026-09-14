package engine

import (
	"testing"

	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
)

// TestRegistrySetLLMClientHotSwap 验证 §UI-AUTHORITATIVE 修复：注册表热替换 LLM 客户端时，
// 依赖模板（决定后续懒加载新建引擎的客户端）与共享新闻归因代理必须同时被替换——
// 此前 main 回调只刷存活引擎，空注册表保存配置后归因链路仍走旧模型。
// English: hot-swap must update the shared template (future lazy engines) and the shared
// news agent (the real attribution path), not only currently-live engines.
func TestRegistrySetLLMClientHotSwap(t *testing.T) {
	old := llm.New(llm.Config{APIKeys: []string{"sk-old"}, APIURL: "https://old.example/v1/chat/completions", Model: "m-old"})
	na := newsagent.New(nil, old, nil, t.TempDir())
	r := &Registry{opts: EngineOptions{NewsAgent: na, LLMClient: old}}

	fresh := llm.New(llm.Config{APIKeys: []string{"kira-test"}, APIURL: "https://kiraai.vn/v1/chat/completions", Model: "qwen3.8-flash-free"})
	r.SetLLMClient(fresh)

	if r.opts.LLMClient != fresh {
		t.Fatalf("注册表模板未更新：新引擎将带旧客户端")
	}
	if na.LLMClient() != fresh {
		t.Fatalf("共享新闻归因代理未热替换：保存后归因仍走旧模型")
	}
}
