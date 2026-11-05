// trade_knobs_api_20261105_test.go — §SIGNAL-GONE / §BUY-REPRICE 两枚钱路径旋钮在配置 API 面的契约。
//
// 为什么单独锁这三条，而不是"能保存就算完"：
//  1. **指针语义**：POST 没带这个键时必须＝不改。如果结构体用值类型，任何一次无关保存
//     （改心跳超时、改战法白名单）都会把 owner 盘中按下过的宽限秒数/偏离阈值悄悄清零——
//     那是"设置页自己会关闸"的形状，比没有开关更坏；
//  2. 非法值整批 400 不落盘：宽限写成 5 秒就是把打分抖动当信号消失，阈值写成 0.05%
//     就是拿柜台撤单额度做高频；
//  3. GET 生效值点名：这两条腿关着与"今天没发生任何触发"在屏上同形，读面不说清就没人知道闸在不在。
//
// English: contract for the two money-path knobs added on 2026-11-05 — pointer semantics
// (absent key = unchanged), 400 on invalid values with nothing written, and the effective value
// named in the GET view so "off" and "silent today" stay distinguishable.
package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// knobCtrl 起一个带真实控制器的测试服务（控制器的 cfg 就是写面要改的那份生效配置）。
func knobCtrl(t *testing.T, s *Server, adminID string) *trading.Controller {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "knob.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exec := trading.NewQMTClient("http://127.0.0.1:1", "", time.Second, 0) // 本用例不下单，仅占位
	ctrl := trading.NewController(exec, db, adminID, config.DefaultQMTConfig(), nil)
	s.ctrl = &capFakeEngine{qmt: ctrl}
	return ctrl
}

// knobView GET /api/config/qmt 并取两枚生效值（缺键即 fatal——读面点名是契约的一部分）。
func knobView(t *testing.T, s *Server, admin *auth.User) (int, float64) {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	if rr.Code != 200 {
		t.Fatalf("GET 期望 200, got %d", rr.Code)
	}
	var view map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	sg, ok1 := view["signal_gone_cancel_sec"]
	vp, ok2 := view["vwap_reprice_pct"]
	if !ok1 || !ok2 {
		t.Fatalf("读面必须点名两枚生效值, got keys=%v", view["rules"])
	}
	// 生效值必须与控制器里那份**同一个数**（读面不能展示配置文件的愿望而不是实际闸的读数）
	sgNum, _ := sg.(float64)
	vpNum, _ := vp.(float64)
	return int(sgNum), vpNum
}

// TestTradeKnobInvalidValuesRejected 非法值整批 400 且一个字段都不落（控制器保持原读数）。
func TestTradeKnobInvalidValuesRejected(t *testing.T) {
	s, admin := newAdminTestServer(t)
	ctrl := knobCtrl(t, s, admin.ID)
	before := ctrl.Config()
	for _, body := range []string{
		`{"signal_gone_cancel_sec":-5}`,     // 负数不是"关"（0 才是关），挡下来免得读成"倒着宽限"
		`{"signal_gone_cancel_sec":5}`,      // 低于一轮打分+回报的量级，等于拿抖动撤单
		`{"signal_gone_cancel_sec":999999}`, // 超出一小时就不是宽限而是"永不过期"
		`{"vwap_reprice_pct":-1}`,
		`{"vwap_reprice_pct":0.05}`, // 小于 0.2% 的偏离天天发生，撤单额度会被噪声吃光
		`{"vwap_reprice_pct":50}`,
	} {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", body))
		if rr.Code != 400 {
			t.Fatalf("body=%s 期望 400, got %d body=%s", body, rr.Code, rr.Body.String())
		}
	}
	after := ctrl.Config()
	if after.SignalGoneCancelSec != before.SignalGoneCancelSec || after.VWAPRepricePct != before.VWAPRepricePct {
		t.Fatalf("非法请求必须一个字段都不改: before=%+v after=%+v", before, after)
	}
}

// TestTradeKnobPointerSemantics 本文件最要紧的一条：**没带键＝不改**。
// 一次只改心跳超时的保存，绝不能把按下的宽限秒数与改价阈值清零。
func TestTradeKnobPointerSemantics(t *testing.T) {
	s, admin := newAdminTestServer(t)
	ctrl := knobCtrl(t, s, admin.ID)
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"signal_gone_cancel_sec":300,"vwap_reprice_pct":1.5}`)); rr.Code != 200 {
		t.Fatalf("合法按下期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"miss_heartbeat_sec":120}`)); rr.Code != 200 {
		t.Fatalf("无关保存期望 200, got %d", rr.Code)
	}
	cfg := ctrl.Config()
	if cfg.SignalGoneCancelSec != 300 || cfg.VWAPRepricePct != 1.5 {
		t.Fatalf("没带这两个键的保存把它们改了（必须是指针语义）: %+v", cfg)
	}
	sg, vp := knobView(t, s, admin)
	if sg != 300 || vp != 1.5 {
		t.Fatalf("读面生效值应与控制器一致, got (%d,%.2f)", sg, vp)
	}
	// 0 是显式关闭，必须能写进去（关与"没带"是两件事）
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"signal_gone_cancel_sec":0,"vwap_reprice_pct":0}`)); rr.Code != 200 {
		t.Fatalf("显式关闭期望 200, got %d", rr.Code)
	}
	if sg, vp = knobView(t, s, admin); sg != 0 || vp != 0 {
		t.Fatalf("显式 0 必须落进生效值, got (%d,%.2f)", sg, vp)
	}
}
