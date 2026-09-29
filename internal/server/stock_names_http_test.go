// §0929FILL-NAME 代码→股票名旁证（FIX_PLAN_20260929 ⑦ P2-2）：HTTP 面四把锁。
//
// GET /api/stock/names?codes=a,b,c 是「交易流水」页名称列的唯一数据来源。锁的是它的降级语义，
// 因为这条链是**旁证**——它坏了不许把主链路（成交簿）一起拖坏，也不许把"没查到"说成"查到了但没有"：
//
//	N1 命中形状：names 只含查到的代码（缺失＝少键，前端据此显示「—」，空串键一律不许出现）；
//	N2 空参数：200 + names={}（首屏无成交时前端根本不发请求，这里的空集合是显式问询的合法答案）；
//	N3 未接库：200 + names={} 并落日志——名称列整列退化成「—」是**可见的缺失**，
//	   而 500 会让前端 catch 后同样显示「—」却把一次配置问题伪装成网络抖动；
//	   真正的查询失败（DB 报错）必须 500，不许 200+空 map 把故障粉饰成「这个市场没有名字」（§M-8 形态）；
//	N4 超限：>MaxStockNamesPerQuery 个代码 → 400（防前端拼参数失控后打出一条无界 IN）。
//
// English: HTTP locks for the read-only code→name side-evidence endpoint — hits only, empty
// parameter, "no DB wired" versus "query failed" (the former degrades visibly, the latter must
// not be dressed up as an empty result), and the per-request cap.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
)

// newStockNamesServer 组装带研究库（stocks 表所在库）的测试服务，返回服务与管理员。
func newStockNamesServer(t *testing.T) (*Server, *auth.User, *store.DB) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetResearch(db, t.TempDir())
	return s, admin, db
}

// namesResp 解析 GET /api/stock/names 的响应信封。
func namesResp(t *testing.T, body string) map[string]string {
	t.Helper()
	var out struct {
		Names map[string]string `json:"names"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("解析 names 响应失败: %v body=%s", err, body)
	}
	if out.Names == nil {
		t.Fatalf("§0929FILL-NAME：names 必须是对象（null 会让前端 row[code] 直接抛错），body=%s", body)
	}
	return out.Names
}

// TestStockNamesEndpointHitAndMiss N1：命中给名字、缺失少键、绝不出现空串值。
func TestStockNamesEndpointHitAndMiss(t *testing.T) {
	s, admin, db := newStockNamesServer(t)
	// 走正规写入口 UpsertStockListings（stocks 表的骨架行 INSERT OR IGNORE 带 name），
	// 不用测试专用 SQL 后门——本包不该为用例往 store 开新出口。
	if _, err := db.UpsertStockListings([]store.StockListing{{TsCode: "600000.SH", Name: "浦发银行"}}); err != nil {
		t.Fatalf("植入测试行失败: %v", err)
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes=600000.SH,999999.SZ", ""))
	if rr.Code != 200 {
		t.Fatalf("应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	names := namesResp(t, rr.Body.String())
	if names["600000.SH"] != "浦发银行" {
		t.Fatalf("§0929FILL-NAME：命中的代码没回名字（%v）", names)
	}
	if v, ok := names["999999.SZ"]; ok {
		t.Fatalf("§0929FILL-NAME：未知代码不得出现键（值 %q）——前端靠缺键显示「—」", v)
	}
	for k, v := range names {
		if strings.TrimSpace(v) == "" {
			t.Fatalf("§0929FILL-NAME：%s 回的是空串名，会把「查无此名」渲染成空单元格", k)
		}
	}
}

// TestStockNamesEndpointEmptyAndNoDB N2+N3：空参数与未接库都回 200+{}（可见缺失，不是错误）。
func TestStockNamesEndpointEmptyAndNoDB(t *testing.T) {
	s, admin, _ := newStockNamesServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes=", ""))
	if rr.Code != 200 {
		t.Fatalf("空 codes 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if n := namesResp(t, rr.Body.String()); len(n) != 0 {
		t.Fatalf("空 codes 应给空集合, got %v", n)
	}

	// 未接任何库的服务（研究进程分离/首装）：名称旁证退化成空集合，成交簿照常渲染不受牵连。
	bare, bareAdmin := newAdminTestServer(t)
	rr2 := adminDo(bare, adminReq(bare, bareAdmin, http.MethodGet, "/api/stock/names?codes=600000.SH", ""))
	if rr2.Code != 200 {
		t.Fatalf("§0929FILL-NAME：未接库应 200+空集合（降级可见、不拖垮流水），got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if n := namesResp(t, rr2.Body.String()); len(n) != 0 {
		t.Fatalf("未接库必须回空集合, got %v", n)
	}
}

// TestStockNamesEndpointQueryFailureIs500 N3 的另一半：库接了但查询真失败 → 必须 500。
// 为什么单独钉：把「查询失败」写成 200+空 map，前端看起来就是「这些代码没有名字」，
// 一次 DB 故障被永久伪装成数据缺失（§M-8/§N-6 反复出事的形态）；而 N3 前半段的未接库
// 是**配置事实**、可以如实降级，两者的处置必须不同。
func TestStockNamesEndpointQueryFailureIs500(t *testing.T) {
	s, admin, db := newStockNamesServer(t)
	if _, err := db.UpsertStockListings([]store.StockListing{{TsCode: "600000.SH", Name: "浦发银行"}}); err != nil {
		t.Fatalf("植入测试行失败: %v", err)
	}
	// 先证正常路径可用（否则 500 是搭出来的，不是关库关出来的）。
	if rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes=600000.SH", "")); rr.Code != 200 {
		t.Fatalf("前置失效：关库前应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关库失败: %v", err)
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes=600000.SH", ""))
	if rr.Code != 500 {
		t.Fatalf("§0929FILL-NAME：查询真失败必须 500（不许 200+空 map 粉饰成无名字），got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestStockNamesEndpointCap N4：代码数超限回 400，等于上限放行。
func TestStockNamesEndpointCap(t *testing.T) {
	s, admin, _ := newStockNamesServer(t)
	over := make([]string, 0, store.MaxStockNamesPerQuery+1)
	for i := 0; i <= store.MaxStockNamesPerQuery; i++ {
		over = append(over, fmt.Sprintf("9%05d.SZ", i))
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes="+strings.Join(over, ","), ""))
	if rr.Code != 400 {
		t.Fatalf("§0929FILL-NAME：%d 个代码应 400（防无界 IN），got %d body=%s", len(over), rr.Code, rr.Body.String())
	}
	// 边界：恰好上限必须放行——流水最多 100 笔去重后远小于此值，但不许把边界写歪。
	rr2 := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes="+strings.Join(over[:store.MaxStockNamesPerQuery], ","), ""))
	if rr2.Code != 200 {
		t.Fatalf("§0929FILL-NAME：恰好 %d 个代码应 200，got %d body=%s", store.MaxStockNamesPerQuery, rr2.Code, rr2.Body.String())
	}
}

// TestStockNamesReadsLocalTableNotUpstream 结构性负锁：本端点不得走行情上游。
// 名称列是展示旁证，为最多 100 行去外呼打上游＝把主链路（成交簿）的可用性押在旁证上，
// 正是本仓反复出事的形态（§0925EVE「降级报成功」、§M-8）。判据按取值链写：
// 服务未接 fetcher/数据源（newAdminTestServer 里两者皆 nil）时，命中路径必须照样工作。
func TestStockNamesReadsLocalTableNotUpstream(t *testing.T) {
	s, admin, db := newStockNamesServer(t)
	if s.fetcher != nil || s.market != nil {
		t.Fatalf("前置失效：本用例依赖测试服务未接行情源（fetcher=%v market=%v）", s.fetcher != nil, s.market != nil)
	}
	if _, err := db.UpsertStockListings([]store.StockListing{{TsCode: "000001.SZ", Name: "平安银行"}}); err != nil {
		t.Fatalf("植入测试行失败: %v", err)
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/stock/names?codes=000001.SZ", ""))
	if rr.Code != 200 || namesResp(t, rr.Body.String())["000001.SZ"] != "平安银行" {
		t.Fatalf("§0929FILL-NAME：无行情源时本地 stocks 表必须仍能出名字, got %d body=%s", rr.Code, rr.Body.String())
	}
}
