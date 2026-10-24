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
	return newUnitsPaperServerMode(t, "")
}

// newUnitsPaperServerMode 同上，但显式指定预算口径（§DYN-TRIM 要证"dynamic 下也是真实减仓"，
// 而默认夹具走的是空口径＝旧支路，两者必须各证一次，否则这条锁只覆盖一半）。
// English: same fixture with an explicit budget mode — the §DYN-TRIM lock needs the dynamic branch
// specifically, since the default fixture exercises the legacy branch only.
func newUnitsPaperServerMode(t *testing.T, budgetMode string) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paper.json")
	cfg := paper.ConfigFromRules(config.NewManager("").Get().Paper)
	cfg.Enabled = true
	cfg.AutoSell = false // 卖出断言不受自动卖出干扰
	cfg.BudgetMode = budgetMode
	s := &Server{}
	s.SetPaper(paper.New(cfg, path))
	return s, path
}

// seedPaperPosition 直接用引擎 API 建仓（指定价+股数），绕过 HTTP 买入入口。
// 为什么必须绕：dynamic 口径下 /api/paper/buy 与 fillLocked 都刻意忽略显式股数（§DYN-SIZE-v3
// 走池权益×缩放），而单测没有行情源也算不出动态量 ⇒ 种子仓只能在 **非 dynamic 口径**下建。
// 只做夹具，不测买入语义；建完用 newDynamicPaperServerFrom 切回被测口径。
// English: seeds a position through the engine API because dynamic mode deliberately ignores an explicit
// share count (v3 sizes from pool equity × scale) and unit tests have no quotes. Seeding therefore runs
// on the non-dynamic branch; newDynamicPaperServerFrom then switches to the mode under test.
func seedPaperPosition(t *testing.T, s *Server, code, name string, price float64, qty int) {
	t.Helper()
	if err := s.paperEngine().BuyExInPool(code, name, "手动", "", 0, price, qty, nil); err != nil {
		t.Fatalf("种子建仓失败 %s %d股: %v", code, qty, err)
	}
}

// newDynamicPaperServerFrom 用同一份 paper.json 重建引擎并切到 dynamic 预算口径。
// §DYN-TRIM 的被测对象正是"dynamic 口径下 qty>0 是不是真减仓"，所以断言必须在 dynamic 引擎上跑。
// English: rebuilds the engine from the same paper.json under dynamic budget mode — the mode under test
// for §DYN-TRIM.
func newDynamicPaperServerFrom(t *testing.T, path string) *Server {
	t.Helper()
	cfg := paper.ConfigFromRules(config.NewManager("").Get().Paper)
	cfg.Enabled = true
	cfg.AutoSell = false
	cfg.BudgetMode = "dynamic"
	s := &Server{}
	s.SetPaper(paper.New(cfg, path))
	return s
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
// §T1-PARTIAL（2026-10-24 裁决三）：T+1 判据已从"整仓一个 filled_at"改成"按批次层账逐批判日"，
// 所以**必须连 lots[].filled_at 一起回拨**——只改整仓时间时，引擎按层账批次（仍是当日）判"一股
// 都不可卖"，用例会被新判据的漏放形态顶红。这是"夹具随判据换锚"的同族第 4 处
// （另三处：paper_test.go 的 t1Ready、dynbudget_v3_test.go、short_test.go）。
// 存量持仓没有 lots 字段＝层账为空，走回落路径，无需回填。
// English: §T1-PARTIAL — the T+1 judgement moved to the per-batch ledger, so the fixture must also
// rewind lots[].filled_at; rewinding only the whole-position stamp would leave every batch "bought
// today" and make the engine refuse to sell. A position without a lots field is a legacy one and
// falls back to the old binary rule, so it needs no backfill.
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
			// 层账批次同样回拨：T+1 判日读的是批次时间，不是整仓时间（§T1-PARTIAL）。
			if lots, ok := p["lots"].([]interface{}); ok {
				for _, lv := range lots {
					if lot, ok := lv.(map[string]interface{}); ok {
						lot["filled_at"] = old
					}
				}
			}
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

// TestPaperSellDynamicIsRealTrim §DYN-TRIM（2026-10-24 owner 裁决）：**dynamic 口径下 qty>0 必须是
// 真实减仓，不能退化成清仓**。改前的形态是"点减仓 → 整仓被清掉"，且零股守卫在 dynamic 下被跳过
// （想减仓既不可控也不设防）。本用例把三件事分别钉死：
//
//	① qty=200 卖完余 300（真减仓，且响应回报 sold/remaining/sellable 三个读数）；
//	② qty=250 非整手仍被零股守卫拦（dynamic 下不再豁免）；
//	③ qty=0 才是清仓（旧口径保留）。
//
// English: §DYN-TRIM — under dynamic mode a positive qty must really trim (not close), the odd-lot
// guard applies there too, and only qty<=0 closes. The response reports sold/remaining/sellable.
func TestPaperSellDynamicIsRealTrim(t *testing.T) {
	s, path := newUnitsPaperServer(t)
	seedPaperPosition(t, s, "600000.SH", "浦发", 10, 500)
	backdatePaperFills(t, s, path)         // 底仓变"昨日买入"
	s = newDynamicPaperServerFrom(t, path) // 切到被测口径（dynamic）

	code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":200}`)
	if code != 200 {
		t.Fatalf("dynamic 下减仓 200 股应 200: %d %s", code, body)
	}
	if p := posQty(s, "600000.SH"); p != 300 {
		t.Fatalf("dynamic 减仓后应余 300 股（不是清仓）, got %d", p)
	}
	var resp struct {
		Sold      int `json:"sold"`
		Remaining int `json:"remaining"`
		Sellable  int `json:"sellable"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("响应应可解析: %v %s", err, body)
	}
	if resp.Sold != 200 || resp.Remaining != 300 {
		t.Fatalf("响应读数 sold/remaining 应为 200/300, got %d/%d", resp.Sold, resp.Remaining)
	}
	if resp.Sellable != 500 {
		t.Fatalf("卖出前可卖量应回显 500（供前端解释截断）, got %d", resp.Sellable)
	}

	if code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":250}`); code != 400 || !strings.Contains(body, "100 股整数倍") {
		t.Fatalf("dynamic 下非整手减仓仍须 400: %d %s", code, body)
	}
	// 清仓形态：qty >= 持仓（这里 300）走整笔平掉。qty=0 那条支路要求实时行情（Sell 不接手输价），
	// 单测没有行情源，故此处用"足量减仓"覆盖清仓语义；qty=0 的行为由 handlePaperSell 的实时价回退
	// 逻辑保证，不属于本用例的被测面。
	// English: the close form is covered via qty >= position; the qty=0 branch needs a live quote (Sell
	// takes no typed price) and is out of scope for this hermetic test.
	if code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":300}`); code != 200 {
		t.Fatalf("足量减仓（清仓形态）应 200: %d %s", code, body)
	}
	if p := posQty(s, "600000.SH"); p != -1 {
		t.Fatalf("清仓后不应有持仓, got %d", p)
	}
}

// TestPaperSellDynamicTrimCappedByT1 §DYN-TRIM × §T1-PARTIAL 的接缝：当日买入的批次不可卖时，
// 减仓请求必须**按可卖量成交**而不是整单被拒（裁决三），响应里的 remaining 要如实说出没卖完的量。
// 这里构造"昨日 500 股底仓 + 今日再加 500 股"的混合态：请求减 700 股，只能卖 500（底仓），
// 余 500（当日机动仓）；再把可卖量报成 500 供前端说明。
// English: the seam between §DYN-TRIM and §T1-PARTIAL — with a mixed position the trim fills the
// sellable part instead of being rejected, and the response reports the true remaining quantity.
func TestPaperSellDynamicTrimCappedByT1(t *testing.T) {
	s, path := newUnitsPaperServer(t)
	seedPaperPosition(t, s, "600000.SH", "浦发", 10, 500)
	backdatePaperFills(t, s, path) // 底仓变成"昨日买入"
	// 当日再加 500 股（在非 dynamic 口径下加，才能给定显式股数）→ 层账里出现"今日批"。
	seedPaperPosition(t, s, "600000.SH", "浦发", 11, 500)
	s = newDynamicPaperServerFrom(t, path) // 切到被测口径（dynamic）
	if p := posQty(s, "600000.SH"); p != 1000 {
		t.Fatalf("混合持仓应 1000 股, got %d", p)
	}
	code, body := postPaper(s, "/api/paper/sell", `{"code":"600000.SH","price":12,"qty":700}`)
	if code != 200 {
		t.Fatalf("混合态减仓 700 股应减量成交而非整单拒: %d %s", code, body)
	}
	if p := posQty(s, "600000.SH"); p != 500 {
		t.Fatalf("只应卖掉可卖的 500 股底仓、余 500 股当日仓, got %d", p)
	}
	var resp struct {
		Sold      int `json:"sold"`
		Remaining int `json:"remaining"`
		Sellable  int `json:"sellable"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("响应应可解析: %v %s", err, body)
	}
	if resp.Sold != 500 || resp.Remaining != 500 || resp.Sellable != 500 {
		t.Fatalf("读数应 sold=500 remaining=500 sellable=500, got %d/%d/%d", resp.Sold, resp.Remaining, resp.Sellable)
	}
}
