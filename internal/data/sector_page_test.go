// sector_page_test.go — §修复 EM-SECTOR-PAGE(20260920) 回归测试：
// 东财板块列表主站不可达时，经镜像**显式分页**取全 496 个板块。
//
// 核心不变量（本文件锁的就是它）：**要么完整全量，要么报错，绝不返回部分结果**。
// 这与 `emFailoverPaths` 里"clist 仅 pz≤100 才转移"的门槛是同一件事的两面——
// 门槛禁止单页请求顶替全量，分页则把"取全"显式做掉；两者都不允许静默截断。
// English: regression tests for explicit mirror pagination of the sector list. The invariant is
// "the COMPLETE list or an error — never a partial list".
package data

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// emSectorPagedTransport 模拟东财 clist/get 板块列表：主站可按需不可达，
// 镜像按 pn/pz 真实分页返回（含越界页 rc=102 + data:null，与线上实测一致）。
// English: simulates the EastMoney sector clist endpoint with real pagination semantics,
// including the out-of-range page shape (rc=102 + data:null) measured in production.
type emSectorPagedTransport struct {
	primaryDown bool
	total       int // 板块总数
	failPage    int // >0 时该页返回 HTTP 500（模拟单页故障）
	hosts       []string
	pns         []int // 请求过的页码（仅 clist/get）
}

func (t *emSectorPagedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	t.hosts = append(t.hosts, host)
	if !strings.Contains(req.URL.Path, "/api/qt/clist/get") {
		return testResp(404, ""), nil
	}
	if t.primaryDown && host == emPrimaryPush2Host {
		// 实测主站形态：TCP 可建连但 TLS 握手被重置（Go 报 EOF）。
		return nil, fmt.Errorf("EOF")
	}
	pn, _ := strconv.Atoi(req.URL.Query().Get("pn"))
	pz, _ := strconv.Atoi(req.URL.Query().Get("pz"))
	t.pns = append(t.pns, pn)
	if t.failPage > 0 && pn == t.failPage {
		return testResp(500, ""), nil
	}
	lo := (pn-1)*pz + 1
	hi := lo + pz - 1
	if hi > t.total {
		hi = t.total
	}
	if lo > t.total {
		return testResp(200, `{"rc":102,"data":null}`), nil
	}
	diff := make(map[string]map[string]any, hi-lo+1)
	for i := lo; i <= hi; i++ {
		code := fmt.Sprintf("BK%04d", i)
		diff[strconv.Itoa(i-lo)] = map[string]any{
			"f12": code, "f14": "板块" + code,
			"f3": 123.0, "f20": 1.0e11, "f62": 9.2e7, "f104": 1, "f105": 2,
		}
	}
	body, _ := json.Marshal(map[string]any{
		"rc": 0,
		"data": map[string]any{
			"total": t.total,
			"diff":  diff,
		},
	})
	r := testResp(200, string(body))
	r.Request = req
	return r, nil
}

// sectorCodes 提取板块代码集合（用于断言"不重不漏"）。
func sectorCodes(list []SectorInfo) map[string]int {
	m := make(map[string]int, len(list))
	for _, s := range list {
		m[s.Code]++
	}
	return m
}

// TestSectorListPrimaryPathUnchanged 主站可用时行为与改造前**完全一致**：
// 一次 pz=500 请求取全，不碰镜像、不翻页。
func TestSectorListPrimaryPathUnchanged(t *testing.T) {
	tr := &emSectorPagedTransport{total: 496}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err != nil {
		t.Fatalf("主站可用时应成功: %v", err)
	}
	if len(list) != 496 {
		t.Fatalf("应一次取回 496 个板块, got %d", len(list))
	}
	if len(tr.pns) != 1 || tr.pns[0] != 1 {
		t.Fatalf("应只请求一次 pn=1, got %v", tr.pns)
	}
	for _, h := range tr.hosts {
		if h != emPrimaryPush2Host {
			t.Fatalf("主站可用时不得请求镜像, hosts=%v", tr.hosts)
		}
	}
	if got := sectorCodes(list); len(got) != 496 {
		t.Fatalf("代码应 496 个互不相同, got %d", len(got))
	}
}

// TestSectorListMirrorPaginatedFull 主站不可达 → 镜像分页取全：
// 5 页（100×4 + 96）、496 条、**不重不漏**。
func TestSectorListMirrorPaginatedFull(t *testing.T) {
	tr := &emSectorPagedTransport{primaryDown: true, total: 496}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err != nil {
		t.Fatalf("主站不可达时应经镜像分页成功: %v", err)
	}
	if len(list) != 496 {
		t.Fatalf("应取回全量 496, got %d（部分结果绝不允许被当全量返回）", len(list))
	}
	codes := sectorCodes(list)
	if len(codes) != 496 {
		t.Fatalf("代码应 496 个互不相同, got %d", len(codes))
	}
	for c, n := range codes {
		if n != 1 {
			t.Fatalf("代码 %s 出现 %d 次（分页去重失效）", c, n)
		}
	}
	if want := []int{1, 2, 3, 4, 5}; fmt.Sprint(tr.pns) != fmt.Sprint(want) {
		t.Fatalf("应按 1..5 页翻页, got %v", tr.pns)
	}
	if tr.hosts[0] != emPrimaryPush2Host {
		t.Fatalf("应先试主站, hosts=%v", tr.hosts)
	}
	for _, h := range tr.hosts[1:] {
		if h != emMirrorPush2Host {
			t.Fatalf("主站失败后应走镜像, hosts=%v", tr.hosts)
		}
	}
}

// TestSectorListMirrorSinglePageNoPaging 板块数 ≤ 100 时首页即全量，不额外翻页。
func TestSectorListMirrorSinglePageNoPaging(t *testing.T) {
	tr := &emSectorPagedTransport{primaryDown: true, total: 80}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err != nil {
		t.Fatalf("应成功: %v", err)
	}
	if len(list) != 80 {
		t.Fatalf("应取回 80, got %d", len(list))
	}
	if fmt.Sprint(tr.pns) != fmt.Sprint([]int{1}) {
		t.Fatalf("首页即全量时不应翻页, got %v", tr.pns)
	}
}

// TestSectorListMirrorPartialFailureRejected 核心不变量：**中间某页失败必须报错**，
// 不得把已取到的部分结果当完整列表返回（那正是"静默截断"）。
func TestSectorListMirrorPartialFailureRejected(t *testing.T) {
	tr := &emSectorPagedTransport{primaryDown: true, total: 496, failPage: 3}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err == nil {
		t.Fatalf("第 3 页失败时必须报错，不得返回 %d 条部分结果", len(list))
	}
	if list != nil {
		t.Fatalf("报错时不得同时返回列表, got %d 条", len(list))
	}
	if !strings.Contains(err.Error(), "不返回部分结果") {
		t.Fatalf("错误应说明拒绝返回部分结果, got %v", err)
	}
	if !strings.Contains(err.Error(), "主站") || !strings.Contains(err.Error(), "镜像分页") {
		t.Fatalf("双路皆败时两段原因都应带上（避免上游误标失败源）, got %v", err)
	}
}

// TestSectorListMirrorPageCountGuard 上游 total 异常（如虚高一个量级）时必须**报错并停止**，
// 不得无界翻页——守卫的判据是"只请求了 1 页"。
func TestSectorListMirrorPageCountGuard(t *testing.T) {
	tr := &emSectorPagedTransport{primaryDown: true, total: 5000}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err == nil {
		t.Fatalf("total=5000 需 50 页，超出上限应报错, got %d 条", len(list))
	}
	if list != nil {
		t.Fatalf("报错时不得返回列表, got %d 条", len(list))
	}
	if !strings.Contains(err.Error(), "超出上限") {
		t.Fatalf("错误应说明超出页数上限, got %v", err)
	}
	if fmt.Sprint(tr.pns) != fmt.Sprint([]int{1}) {
		t.Fatalf("守卫应在首页后即停止，仅请求 1 页, got %v", tr.pns)
	}
}

// TestSectorListMirrorNoNewRowsAcrossPages 分页错位（后续页全是重复数据）时，
// 若仍未凑齐 total 则必须报错——否则会返回一个"看起来 496 条、实际有空洞"的列表。
func TestSectorListMirrorNoNewRowsAcrossPages(t *testing.T) {
	// total=496 但镜像每页都只回第 1 页的内容 → 第 2 页起新增恒为 0。
	tr := &stuckPageTransport{total: 496, mirrored: 100}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err == nil {
		t.Fatalf("分页错位且未凑齐时必须报错, got %d 条", len(list))
	}
	if !strings.Contains(err.Error(), "分页疑似错位") {
		t.Fatalf("错误应指出分页错位, got %v", err)
	}
}

// stuckPageTransport 主站不可达；镜像无论 pn 是多少都返回同一批（第 1 页）数据。
type stuckPageTransport struct {
	total    int
	mirrored int
}

func (t *stuckPageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == emPrimaryPush2Host {
		return nil, fmt.Errorf("EOF")
	}
	diff := make(map[string]map[string]any, t.mirrored)
	for i := 1; i <= t.mirrored; i++ {
		code := fmt.Sprintf("BK%04d", i)
		diff[strconv.Itoa(i-1)] = map[string]any{"f12": code, "f14": "板块" + code, "f3": 1.0}
	}
	body, _ := json.Marshal(map[string]any{"rc": 0, "data": map[string]any{"total": t.total, "diff": diff}})
	r := testResp(200, string(body))
	r.Request = req
	return r, nil
}

// TestSectorListMirrorDedupAcrossOverlappingPages 跨页**重叠**时必须去重。
//
// 这条用锚定"去重"这个动作本身：`emSectorPagedTransport` 返回的各页互不重叠，
// 去掉去重代码也看不出差别——等于去重逻辑没被测到。真实上游在两次翻页之间数据会漂移
// （排序/成分变动），相邻页出现重复行是常见形态，必须按代码去重。
// 构造：total=150、pz=100、相邻页重叠 50 行（第 2 页返回 51..150）。
// 去重后恰好 150 条（完整），不去重则 200 条（含 50 条重复）。
func TestSectorListMirrorDedupAcrossOverlappingPages(t *testing.T) {
	tr := &overlapPageTransport{total: 150, overlap: 50}
	m := NewMarketAPI()
	m.SetTransport(tr)

	list, err := m.GetSectorList()
	if err != nil {
		t.Fatalf("重叠页去重后应能凑齐全量: %v", err)
	}
	if len(list) != 150 {
		t.Fatalf("重叠页必须按代码去重后返回 150 条, got %d", len(list))
	}
	codes := sectorCodes(list)
	if len(codes) != 150 {
		t.Fatalf("代码应 150 个互不相同, got %d", len(codes))
	}
	for c, n := range codes {
		if n != 1 {
			t.Fatalf("代码 %s 出现 %d 次（跨页去重失效）", c, n)
		}
	}
}

// overlapPageTransport 主站不可达；镜像各页按 overlap 行重叠（模拟上游排序/成分漂移）。
type overlapPageTransport struct {
	total   int
	overlap int
}

func (t *overlapPageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == emPrimaryPush2Host {
		return nil, fmt.Errorf("EOF")
	}
	pn, _ := strconv.Atoi(req.URL.Query().Get("pn"))
	pz, _ := strconv.Atoi(req.URL.Query().Get("pz"))
	step := pz - t.overlap
	lo := (pn-1)*step + 1
	hi := lo + pz - 1
	if hi > t.total {
		hi = t.total
	}
	diff := make(map[string]map[string]any, hi-lo+1)
	for i := lo; i <= hi; i++ {
		code := fmt.Sprintf("BK%04d", i)
		diff[strconv.Itoa(i-lo)] = map[string]any{"f12": code, "f14": "板块" + code, "f3": 1.0}
	}
	body, _ := json.Marshal(map[string]any{"rc": 0, "data": map[string]any{"total": t.total, "diff": diff}})
	r := testResp(200, string(body))
	r.Request = req
	return r, nil
}
