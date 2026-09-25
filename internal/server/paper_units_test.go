// paper_units_test.go — §FIX-1(20260919) 模拟盘手动买卖"手/股"单位收敛的 HTTP 层契约测试。
// 钉死三件事：①POST /api/paper/buy|sell 的 qty 对外口径=股数（前端把"手数×100"作为唯一换算点，
// 后端等价载荷 300 股 → 引擎持仓必须恰好 ±300 股）；②买入非 100 股整数倍 → 400；
// ③部分减仓非整手会留下零股尾仓 → 400，而 qty>=持仓 的清仓形态允许零股随仓平掉。
// English: §FIX-1 HTTP-layer contract test — qty on /api/paper/buy|sell is SHARES (frontend does
// the lots×100 conversion); a buy that isn't a whole multiple of 100 is rejected; a partial trim
// that would strand an odd lot is rejected, while a full-close-sized sell may carry odd remainders.
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
)

// newUnitsPaperServer 构造带真实 paper 引擎的最小服务：总开关直开（不借道 config 端点），
// 返回服务与 paper.json 路径（供用例改写 filled_at 绕过 T+1）。
func newUnitsPaperServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paper.json")
	cfg := paper.ConfigFromRules(config.NewManager("").Get().Paper)
	cfg.Enabled = true
	cfg.AutoSell = false // 卖出断言不受自动卖出干扰
	s := &Server{}
	s.SetPaper(paper.New(cfg, path))
	return s, path
}

// postPaper 调用买入/卖出端点的便捷 Act：返回状态码与响应体字符串。
func postPaper(s *Server, route, body string) (int, string) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
	if route == "/api/paper/buy" {
		s.handlePaperBuy(rr, req)
	} else {
		s.handlePaperSell(rr, req)
	}
	return rr.Code, rr.Body.String()
}

// backdatePaperFills 把 paper.json 所有持仓的 filled_at 改写为 2 个自然日前并重建引擎，
// 模拟"昨日建仓"（server 包测试无法触达引擎内部 t1Ready，只能走持久化文件绕开 T+1）。
func backdatePaperFills(t *testing.T, s *Server, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 paper.json 失败: %v", err)
	}
	var all map[string]interface{}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("解析 paper.json 失败: %v", err)
	}
	positions, _ := all["positions"].(map[string]interface{})
	if len(positions) == 0 {
		t.Fatal("backdate 前置条件：应已有持仓")
	}
	old := time.Now().AddDate(0, 0, -2).Format(time.RFC3339)
	for _, v := range positions {
		if p, ok := v.(map[string]interface{}); ok {
			p["filled_at"] = old
		}
	}
	out, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("回写 paper.json 失败: %v", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		t.Fatalf("回写 paper.json 失败: %v", err)
	}
	// 重建引擎触发 load()：持仓以隔日 filled_at 恢复
	rules := config.NewManager("").Get().Paper
	cfg := paper.ConfigFromRules(rules)
	cfg.Enabled = true
	cfg.AutoSell = false
	s.SetPaper(paper.New(cfg, path))
}

// posQty 读取引擎持仓股数（未持仓返回 -1）。
func posQty(s *Server, code string) int {
	for _, p := range s.paperEngine().Positions() {
		if p.Code == code {
			return p.Qty
		}
	}
	return -1
}

// TestPaperBuyUnitsContract 买入契约：3 手换算后的等价载荷 qty=300 → 持仓恰 300 股；
// 非整百（把"手数"误当"股数"或直接填零股）→ 400 中文错误且不落账。
func TestPaperBuyUnitsContract(t *testing.T) {
	s, _ := newUnitsPaperServer(t)
	code, body := postPaper(s, "/api/paper/buy", `{"code":"600000.SH","name":"浦发","strategy":"手动","price":10,"qty":300}`)
	if code != 200 {
		t.Fatalf("300 股买入应 200: %d %s", code, body)
	}
	p := posQty(s, "600000.SH")
	if p != 300 {
		t.Fatalf("持仓应为 300 股（引擎按股记账，换算在前端）, got %d", p)
	}
	for _, q := range []int{1, 50, 150} { // 历史上 UI 传"手"即踩此坑：1 手被记成 1 股
		code, body = postPaper(s, "/api/paper/buy", `{"code":"000001.SZ","name":"平安","strategy":"手动","price":10,"qty":`+strconv.Itoa(q)+`}`)
		if code != 400 || !strings.Contains(body, "100 股整数倍") {
			t.Fatalf("qty=%d 应 400 整手纪律错误: %d %s", q, code, body)
		}
		if posQty(s, "000001.SZ") != -1 {
			t.Fatalf("qty=%d 被拒后不应建仓", q)
		}
	}
}

// TestPaperSellUnitsContract 卖出契约：隔日持仓下——部分减仓非整手 400、整手减仓成功、
// qty>=持仓（含非整百）按清仓放行（零股尾仓必须平得掉）。
func TestPaperSellUnitsContract(t *testing.T) {
	s, path := newUnitsPaperServer(t)
	if code, body := postPaper(s, "/api/paper/buy", `{"code":"600000.SH","name":"浦发","strategy":"手动","price":10,"qty":500}`); code != 200 {
		t.Fatalf("建仓 500 股失败: %d %s", code, body)
	}
	backdatePaperFills(t, s, path)

	if code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":250}`); code != 400 || !strings.Contains(body, "100 股整数倍") {
		t.Fatalf("部分减仓 250 股应 400 零股守卫: %d %s", code, body)
	}
	if code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":200}`); code != 200 {
		t.Fatalf("整手减仓 200 股应 200: %d %s", code, body)
	}
	if p := posQty(s, "600000.SH"); p != 300 {
		t.Fatalf("减仓后应余 300 股, got %d", p)
	}
	// qty=350 > 持仓 300（非整百）→ 清仓形态，允许零股随仓平掉
	if code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":350}`); code != 200 {
		t.Fatalf("qty>=持仓的清仓形态应放行零股: %d %s", code, body)
	}
	if p := posQty(s, "600000.SH"); p != -1 {
		t.Fatalf("清仓后不应有持仓, got %d", p)
	}
}
