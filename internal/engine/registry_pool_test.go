package engine

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/paper"
)

// TestRegistryAllPaperHeldCodesEagerPin 验证 allPaperHeldCodes 锁外预创建 auto-paper 账号的
// 懒加载纸面引擎并从磁盘恢复持仓聚合：重启后纸面持仓不依赖前端轮询 /api/paper/* 即可钉入
// 5s 监控池 base（持仓池/监控池分离的纸面侧根支护）。
func TestRegistryAllPaperHeldCodesEagerPin(t *testing.T) {
	dir := t.TempDir()
	// 全局模板纸面引擎（生产上为 "0仓" 模板，仅提供配置，不贡献持仓）
	tmpl := paper.New(paper.Config{}, "")
	// 构造一个带持仓的账号 paper.json（模拟磁盘恢复路径 accounts/<uid>/paper.json）
	accountPaper := `{"cash":100000,"initial_capital":100000,"positions":{` +
		`"600096":{"code":"600096","name":"云天化","qty":300,"cost_price":31.46,"cost":9438,"mark":31.9},` +
		`"002815":{"code":"002815","name":"崇达技术","qty":1100,"cost_price":17.3,"cost":19030,"mark":17.59}}}`
	paperDir := filepath.Join(dir, "accounts", "u_test")
	if err := os.MkdirAll(paperDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paperDir, "paper.json"), []byte(accountPaper), 0644); err != nil {
		t.Fatalf("write paper.json: %v", err)
	}

	r := NewRegistry(EngineOptions{Paper: tmpl, DataDir: dir})
	// 手动登记共享引擎的账号列表（coreUsers 驱动 allPaperHeldCodes 的账号枚举）
	r.coreUsers[&Engine{}] = []string{"u_test"}

	codes := r.allPaperHeldCodes()
	got := map[string]bool{}
	for _, c := range codes {
		got[c] = true
	}
	if !got["600096"] || !got["002815"] {
		t.Fatalf("allPaperHeldCodes 应含账号纸面持仓 600096/002815（磁盘恢复+预创建聚合）, got %v", codes)
	}
	if len(codes) != 2 {
		t.Fatalf("期望恰好 2 个纸面持仓代码, got %v", codes)
	}
	// 预创建生效：GetPaper 幂等返回已缓存引擎，且持仓来自磁盘（非模板）
	pe := r.GetPaper("u_test")
	if pe == nil || len(pe.Positions()) != 2 {
		t.Fatalf("GetPaper 应返回已预创建且含 2 持仓的账号引擎")
	}
}