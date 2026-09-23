// side_unverified_report_test.go — §SIDE-AUTH-2（2026-09-23 夜间批）回报信封端到端回归。
//
// 缺陷链条（docs/FIX_PLAN_20260923NIGHT.md §2）：网关未命中派发行时曾**静默**采用桥/柜台
// 的枚举方向猜测（23/24 vs 1101/1102 vs 48/50 多空间反推，已两次实锤判反）；09-22 一笔
// 真实卖出被记成买入，下游"卖出回款→预算闸→已实现盈亏"全线污染。修法两侧对齐：
// 网关把该笔转「待核对」通道不动本地持仓账，回报信封新增 side_unverified=true；
// Go 侧认这个字段——留痕（opslog 审计行 + 计数指标）但**不入本地账本**（宁可人工核对后
// 勘误，不可让猜测方向进三本账）。
//
// 本文件锁三态：
//
//	① 带 side_unverified 的成交 → HTTP 200（幂等吞掉，不让网关 outbox 进死信丢证据链）、
//	   持仓账不动、fills 零入账、审计留痕行可查、计数指标 +1；
//	② 同一信封不带该标记（或=false）→ 既有入账路径一字不变（防"认字段"顺手改坏正路）；
//	③ 方向不可归一的 unverified 回报仍先走 normalizeReportSide fail-close（400），
//	   标记不得变成绕过方向校验的后门。
//
// English: §SIDE-AUTH-2 end-to-end lock — a side-unverified trade report is journaled
// (opslog audit line + counter metric) and never booked; the verified path is unchanged;
// and the flag must not bypass the existing fail-close side normalization.
package server

import (
	"bytes"
	"encoding/json"
	"expvar"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/opslog"
)

// postReport 直接向 handleQMTReport 投递一条回报信封，返回状态码与响应体。
func postReport(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	return rr.Code, rr.Body.String()
}

// TestReportSideUnverifiedJournaledNotBooked ①+②：unverified 只留痕不入账；无标记照常入账。
func TestReportSideUnverifiedJournaledNotBooked(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// opslog 重定向到临时目录，断言"留痕"是真实可查的文件行（而不是只打了一行 stderr）。
	logDir := filepath.Join(t.TempDir(), "opslog")
	opslog.Init(logDir, 0)

	// 读 expvar 快照里的计数（metrics 包既有手法：原子计数 + publish 成 JSON 快照，
	// 快照本身是 expvar.String → 外层还带一层 JSON 引号，先解成 string 再解 map）。
	metricsBefore := func() int64 {
		v, ok := expvar.Get("quant.metrics").(interface{ String() string })
		if !ok {
			t.Fatal("quant.metrics 未发布——计数指标丢失（metrics.go publish 回归）")
		}
		var raw string
		if err := json.Unmarshal([]byte(v.String()), &raw); err != nil {
			t.Fatalf("quant.metrics 快照外层非 JSON 字符串: %v", err)
		}
		var snap map[string]int64
		if err := json.Unmarshal([]byte(raw), &snap); err != nil {
			t.Fatalf("quant.metrics 快照非 JSON: %v", err)
		}
		return snap["fills_side_unverified"]
	}
	before := metricsBefore()

	// ① 网关未命中派发行的成交：方向是桥的猜测（此处按猜测值"买入"送进来，真值可能是卖出）。
	body := `{"type":"trade","order_id":"O-SU-1","trade_id":"T-SU-1","name":"贵州茅台","code":"603468.SH",` +
		`"side":"买入","side_unverified":true,"price":22.55,"qty":900,"amount":20295,` +
		`"traded_at":"2026-09-22T10:08:32+08:00","signal_id":""}`
	code, resp := postReport(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("① unverified 回报应 200 幂等吞掉（4xx 会让网关 outbox 死信、证据链断），got %d body=%s", code, resp)
	}
	// 持仓账不动：这笔绝不建仓
	if _, err := db.RealPositionByCode("603468.SH"); err == nil {
		t.Fatal("① side_unverified 成交仍改动了持仓账——fail-open 复活")
	}
	// fills 零入账：猜测方向不进流水（网关「待核对」通道保留证据，首尔侧不重复记账）
	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("list fills: %v", err)
	}
	for _, f := range fills {
		if f.OrderID == "O-SU-1" || f.TradeID == "T-SU-1" {
			t.Fatalf("① unverified 成交流进了本地 fills: %+v", f)
		}
	}
	// 计数指标 +1（§SIDE-AUTH-2 的运维可见性本体）
	if got := metricsBefore(); got != before+1 {
		t.Fatalf("① fills_side_unverified 计数应 +1：before=%d after=%d", before, got)
	}
	// opslog 审计留痕可查：audit-YYYYMMDD.log 含事件名与可复核字段（不含任何密钥值）
	auditPath := filepath.Join(logDir, "audit-"+time.Now().Format("20060102")+".log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("① 读审计文件失败（留痕没落地）: %v", err)
	}
	line := string(data)
	for _, want := range []string{"live_fill_side_unverified", "603468.SH", "O-SU-1", "T-SU-1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("① 审计留痕缺关键字段 %q，行=%s", want, line)
		}
	}

	// ② 对照组：同一信封不带标记（网关命中派发行）→ 既有入账路径一字不变
	body2 := `{"type":"trade","order_id":"O-SU-2","trade_id":"T-SU-2","name":"贵州茅台","code":"603468.SH",` +
		`"side":"买入","price":22.55,"qty":900,"amount":20295,` +
		`"traded_at":"2026-09-22T10:08:33+08:00","signal_id":"SU2"}`
	if code2, resp2 := postReport(t, s, body2); code2 != http.StatusOK {
		t.Fatalf("② 已证成交应照常 200，got %d body=%s", code2, resp2)
	}
	pos, err := db.RealPositionByCode("603468.SH")
	if err != nil || pos.Qty != 900 {
		t.Fatalf("② 已证成交必须照常建仓（对照组被 unverified 改动波及）: pos=%+v err=%v", pos, err)
	}
	if got := metricsBefore(); got != before+1 {
		t.Fatalf("② 已证成交不得推高 unverified 计数：after2=%d", got)
	}
}

// TestReportSideUnverifiedNoGarbageSide ③：unverified 不是绕过方向 fail-close 的后门——
// 方向本身不可归一时仍旧 400（网关恒带推断方向，但契约上不许把空/乱 side 用标记带进账本面）。
func TestReportSideUnverifiedNoGarbageSide(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"trade","order_id":"O-SU-3","trade_id":"T-SU-3","code":"600000.SH",` +
		`"side":"","side_unverified":true,"price":10,"qty":100,"amount":1000}`
	code, _ := postReport(t, s, body)
	if code != http.StatusBadRequest {
		t.Fatalf("③ 方向不可归一 + unverified 仍应 400（标记不是后门），got %d", code)
	}
	fills, _ := db.RealFills()
	for _, f := range fills {
		if f.OrderID == "O-SU-3" {
			t.Fatalf("③ 被拒回报不得入账: %+v", f)
		}
	}
}

// TestReportEnvelopeDecodesSideUnverified 信封真解码：tag 拼错/类型漂移时静态契约测不到行为。
func TestReportEnvelopeDecodesSideUnverified(t *testing.T) {
	var ev qmtReportEvent
	raw := `{"type":"trade","side":"卖出","side_unverified":true}`
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ev.SideUnverified {
		t.Fatal("side_unverified 未解码进 qmtReportEvent——tag 漂移会被 encoding/json 静默丢腿")
	}
	var ev2 qmtReportEvent
	_ = json.Unmarshal([]byte(`{"type":"trade","side":"卖出"}`), &ev2)
	if ev2.SideUnverified {
		t.Fatal("缺键必须为 false：命中派发行的旧回报不得被误标未证")
	}
}
