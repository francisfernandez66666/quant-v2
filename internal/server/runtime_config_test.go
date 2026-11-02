// runtime_config_test.go — §SUBSCRIPTION-A（2026-11-01 裁决 4/12）runtime 行情开关写面的行为锁。
//
// 四条各对一个真实风险：
//
//	① 稀疏 merge：只发一个键，别的键必须原样留着。旧式"typed struct 整体解码再保存"会把
//	   没传的键折成零值——那正是"保存一个开关顺手把别的参数清零"那一族缺陷的成因。
//	② 非法值整批拒收且不落盘：夹到边界保存会让设置页显示的数字和你填的不是一个数。
//	   量纲系数量级错 100 倍直接进 VWAP，所以这里连"只接受 1/100/0"都是刻意的窄。
//	③ 生效值与原始值两栏并列：0 的含义是"没填"（实际按出厂 80 判），只显示原值会骗人。
//	④ 热更与重启如实区分：保存即生效的键和"要重启才生效"的键分开报，前端不许自己猜。
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/data"
)

// runtimeGet 读一次 GET /api/config/runtime。
func runtimeGet(t *testing.T, s *Server, admin *auth.User) map[string]any {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/runtime", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/config/runtime → %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
	}
	return out
}

// runtimePost 发一次 POST，返回 (状态码, 响应体)。
func runtimePost(t *testing.T, s *Server, admin *auth.User, body string) (int, map[string]any) {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/runtime", body))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out) // 400/500 的响应体可能不是完整视图，解不动就算了
	return rr.Code, out
}

// newRuntimeTestServer 带一个可注入的采集器（热更断言要看它身上的策略）。
func newRuntimeTestServer(t *testing.T) (*Server, *auth.User, *data.Fetcher) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	f := data.NewFetcher([]string{"600000"}, &data.MarketAPI{}, nil)
	s.SetFetcher(f)
	return s, admin, f
}

// TestRuntimeConfigGetShape 出厂态：原始值为 0 时也必须给出**生效值**；四个行情开关出厂为开
// （owner 2026-11-01 改判）；restart_required 点名那两个"保存≠生效"的键。
func TestRuntimeConfigGetShape(t *testing.T) {
	s, admin, _ := newRuntimeTestServer(t)
	got := runtimeGet(t, s, admin)
	for _, k := range []string{"qmt_feed_enabled", "qmt_feed_primary", "quote_per_code_source", "quote_hub_consult"} {
		if got[k] != true {
			t.Fatalf("出厂态 %s 应为 true（缺键=开；显式 false 才关）, got %v", k, got[k])
		}
	}
	if got["qmt_feed_coverage_min_pct"] != float64(0) {
		t.Fatalf("原始值应如实回 0（没填过）, got %v", got["qmt_feed_coverage_min_pct"])
	}
	if got["coverage_min_pct_effective"] != float64(80) {
		t.Fatalf("生效值必须是出厂 80，只显原始值会骗人, got %v", got["coverage_min_pct_effective"])
	}
	if got["volume_to_shares_effective"] != float64(1) {
		t.Fatalf("量纲生效值应为 1（股）, got %v", got["volume_to_shares_effective"])
	}
	if got["monitor_pool_hot_max_effective"] != float64(400) {
		t.Fatalf("池上限生效值应为出厂 400, got %v", got["monitor_pool_hot_max_effective"])
	}
	rs, _ := got["restart_required"].([]any)
	joined := ""
	for _, v := range rs {
		joined += toString(v) + ","
	}
	if !strings.Contains(joined, "qmt_feed_enabled") {
		t.Fatalf("qmt_feed_enabled 属重启类（决定启动时是否建轮询协程），必须点名: %v", got["restart_required"])
	}
	if got["feed_state"] == nil {
		t.Fatal("通道现状必须一并回显：改完键要能当场看到有没有连着")
	}
}

// TestRuntimeConfigSparseMergeAndHotApply 只发覆盖率一个键：其余键原样保留，且策略即时灌给采集器。
func TestRuntimeConfigSparseMergeAndHotApply(t *testing.T) {
	s, admin, f := newRuntimeTestServer(t)
	code, resp := runtimePost(t, s, admin, `{"qmt_feed_coverage_min_pct":70}`)
	if code != http.StatusOK {
		t.Fatalf("合法保存应 200, got %d resp=%v", code, resp)
	}
	if got := f.FeedPolicyNow().CoverageMinPct; got != 70 {
		t.Fatalf("未热更：装配层阈值仍是 %d（保存要下一轮生效，不必重启）", got)
	}
	again := runtimeGet(t, s, admin)
	if again["qmt_feed_coverage_min_pct"] != float64(70) {
		t.Fatalf("没落盘：读回 %v", again["qmt_feed_coverage_min_pct"])
	}
	// 出厂即开（owner 2026-11-01 改判"券商信息源默认开"）：没发这个键时它必须仍是 true。
	if again["qmt_feed_primary"] != true {
		t.Fatalf("稀疏 merge 动了没发的键：qmt_feed_primary=%v（缺省应为 true，只有显式 false 才关）", again["qmt_feed_primary"])
	}
	if again["monitor_pool_hot_max"] != float64(0) {
		t.Fatalf("稀疏 merge 把没发的键改了：monitor_pool_hot_max=%v", again["monitor_pool_hot_max"])
	}
	changed, _ := resp["changed"].([]any)
	if len(changed) != 1 || toString(changed[0]) != "qmt_feed_coverage_min_pct" {
		t.Fatalf("审计要如实说这次只动了一个键, got %v", resp["changed"])
	}
	// 主源档热更（默认开之后更要能**关得掉**）：显式 false 必须即时停用券商改写价格，
	// 这是"回滚只翻一个键、不必回退发版"的兑现点。
	if _, _ = runtimePost(t, s, admin, `{"qmt_feed_primary":false}`); f.FeedPolicyNow().Primary {
		t.Fatal("显式 false 没关掉主源档（指针位退化成了零值语义）")
	}
	if _, _ = runtimePost(t, s, admin, `{"qmt_feed_primary":true}`); !f.FeedPolicyNow().Primary {
		t.Fatal("qmt_feed_primary 热更没生效")
	}
	if got := runtimeGet(t, s, admin)["qmt_feed_coverage_min_pct"]; got != float64(70) {
		t.Fatalf("第二次保存把上一次的键冲掉了：%v", got)
	}
}

// TestRuntimeConfigRejectsIllegalValues 非法值整批拒收、一个键都不落盘（部分成功比全部失败更难排查）。
func TestRuntimeConfigRejectsIllegalValues(t *testing.T) {
	s, admin, f := newRuntimeTestServer(t)
	for _, body := range []string{
		`{"qmt_feed_coverage_min_pct":101}`,                              // 阈值越界
		`{"qmt_feed_coverage_min_pct":-5}`,                               // 只有 -1 才是"关闭判断"
		`{"qmt_feed_volume_to_shares":5}`,                                // 量纲只认 1/100/0：错一档 VWAP 差 100 倍
		`{"monitor_pool_hot_max":30}`,                                    // 低于降级档常数 60
		`{"monitor_pool_hot_max":5000}`,                                  // 上限之外后备链没实测过
		`{"qmt_feed_interval_sec":600}`,                                  // 轮询节拍越界
		`{"qmt_feed_coverage_min_pct":90,"qmt_feed_volume_to_shares":7}`, // 混合批次里有非法项 ⇒ 整批不落
	} {
		code, _ := runtimePost(t, s, admin, body)
		if code != http.StatusBadRequest {
			t.Fatalf("非法值必须 400：%s → %d", body, code)
		}
	}
	// 关键判据：整批都拒了，装配层就一个键都不该被碰过。
	// 注意出厂态是**零值策略**（阈值兜底发生在 Effective* 读数处，不预写进 policy），
	// 所以这里断的是"仍是零值"，不是"等于 80"——写成 80 会把"兜底在读数侧"这个事实误当成回归。
	st := f.FeedPolicyNow()
	if st.CoverageMinPct != 0 || st.HotPoolCap != 0 || st.VolumeToShares != 0 || st.Primary {
		t.Fatalf("非法批次仍然改到了运行态: %+v", st)
	}
	got := runtimeGet(t, s, admin)
	if got["qmt_feed_coverage_min_pct"] != float64(0) {
		t.Fatalf("非法批次仍然落盘了: %v", got["qmt_feed_coverage_min_pct"])
	}
}

// TestRuntimeRoutesAreConfigAdminOnly 两条路由必须挂 config_admin 严格权限位。
// 这批键管"行情从哪来、多旧能用"，不给 trading_admin：能让成员号自己把券商价设成主源，
// 等于把行情判据的开关撒到执行面账号上，方向不对。
func TestRuntimeRoutesAreConfigAdminOnly(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("读 server.go 失败: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, "/api/config/runtime") {
			continue
		}
		if !strings.Contains(line, `strictPermMiddleware(auth.PermConfigAdmin`) {
			t.Fatalf("runtime 路由没挂 config_admin 严格权限位: %s", strings.TrimSpace(line))
		}
	}
}

// toString 小工具：JSON 解出来是 any，断言里要拿字符串比较。
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestRuntimeConfigIndexKeys §INDEX-BLIND（2026-11-02）三枚指数键的写面契约。
//
// 三条各对一个真实形状：
//
//	① 出厂态必须下发**生效值 true**（指针位原样下发会在前端变 null，开关看起来像坏了——
//	   §161 那条负锁就是为这个立的，这里把它行为化）；
//	② 稀疏 merge：只发一枚，其余两枚必须原样留着（把没传的键折成 false＝悄悄关掉出厂开）；
//	③ 段序读数跟着开关走：关掉降级链时 index_legs_actual 只剩东财——
//	   卡面不许自己列一份，否则两处会漂成"卡上说三条、实际试两条"。
func TestRuntimeConfigIndexKeys(t *testing.T) {
	s, admin, _ := newRuntimeTestServer(t)
	got := runtimeGet(t, s, admin)
	for _, k := range []string{"index_fallback_sources", "index_breaker_scope", "quote_feed_index_enabled"} {
		if got[k] != true {
			t.Fatalf("出厂态 %s 应为 true（缺键=开），got %v", k, got[k])
		}
	}
	// 两栏分职：index_legs 是**声明集**（四条，含 §A7-3 的券商腿），
	// index_legs_actual 是本轮真的会依次试谁（未注入券商 ⇒ 三条，东财在前）。
	declared, _ := got["index_legs"].([]any)
	if len(declared) != 4 || declared[0] != "QMT-L1" {
		t.Fatalf("声明集应为四条且券商腿在首位, got %v", got["index_legs"])
	}
	actual, _ := got["index_legs_actual"].([]any)
	if len(actual) != 3 || actual[0] != "eastmoney" {
		t.Fatalf("实际段序应三条且东财在前（本夹具没注入券商段）, got %v", got["index_legs_actual"])
	}
	hot, _ := got["hot_keys"].([]any)
	var inHot bool
	for _, h := range hot {
		if h == "index_fallback_sources" {
			inHot = true
		}
	}
	if !inHot {
		t.Fatalf("降级链键没进 hot_keys（保存即生效的键必须点名，前端不许自己猜）：%v", hot)
	}

	// 只发一枚 ⇒ changed 只能有这一枚，另两枚保持 true
	code, body := runtimePost(t, s, admin, `{"index_fallback_sources":false}`)
	if code != http.StatusOK {
		t.Fatalf("保存失败 → %d body=%v", code, body)
	}
	changed, _ := body["changed"].([]any)
	if len(changed) != 1 || changed[0] != "index_fallback_sources" {
		t.Fatalf("审计只应点名被改的那一枚, got %v", body["changed"])
	}
	after := runtimeGet(t, s, admin)
	if after["index_fallback_sources"] != false {
		t.Fatalf("降级链没落盘, got %v", after["index_fallback_sources"])
	}
	for _, k := range []string{"index_breaker_scope", "quote_feed_index_enabled"} {
		if after[k] != true {
			t.Fatalf("没发过的键被折成 false 了：%s=%v（稀疏 merge 不许覆盖出厂开）", k, after[k])
		}
	}
	actualOff, _ := after["index_legs_actual"].([]any)
	if len(actualOff) != 1 || actualOff[0] != "eastmoney" {
		t.Fatalf("关掉降级链后实际段序应只剩东财, got %v", after["index_legs_actual"])
	}
}
