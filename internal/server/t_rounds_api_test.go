// t_rounds_api_test.go — §DYN-T 做 T 只读接口的行为锁（2026-10-26 批 4/9 收尾）。
//
// 锁四件事，全部是"读侧不能骗人"这一类：
//  1. 实盘账本缺文件 → 200 + state=missing（不是 404、也不是空数组假装正常）；
//  2. 实盘账本损坏 → 200 + state=corrupt，**且原文件字节不变**（接口只读，坏了留给人工核对）；
//  3. 「做 T 收益」只统计 done 轮：unwound/converted 腿即便带着 pnl 数字也绝不进 net；
//  4. limit 非法/越界一律夹回区间，绝不把整份账本灌进响应。
//
// 第 3 条是反证方向最容易写错的一条：如果聚合写成"遍历所有轮求和"，一次跨日收摊就会虚增收益，
// 而且日志上一切正常——这类"看起来在工作"的重复记账是本接口最贵的缺陷。
// English: locks for the read-only §DYN-T endpoints — honest missing/corrupt states (the file is
// never rewritten), done-only P&L aggregation, and clamped limits.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveTRoundsAPI(t *testing.T) {
	s, admin := newAdminTestServer(t)
	dir := t.TempDir()
	s.SetCacheDir(dir)
	path := filepath.Join(dir, "accounts", admin.ID, "t_rounds_live.json")

	read := func(query string) map[string]interface{} {
		t.Helper()
		rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/t-live/rounds"+query, ""))
		var out map[string]interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是合法 JSON：%v (%s)", err, rr.Body.String())
		}
		return out
	}

	// ① 缺文件：200 + missing，rounds 必须是空数组而不是 null（前端直接 map，不给 null 崩一次）。
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("测试前置：账本文件本不该存在，got %v", err)
	}
	out := read("")
	if out["state"] != "missing" {
		t.Fatalf("缺文件应 state=missing，got %v", out["state"])
	}
	if arr, ok := out["rounds"].([]interface{}); !ok || len(arr) != 0 {
		t.Fatalf("缺文件应返回空数组，got %#v", out["rounds"])
	}

	// ② 正常账本：三轮里两轮 done（跨日），unwound 那轮带着 -999 的假数字也一分不许进 net。
	body := `{"day":"20261026","rounds":[
		{"id":"t0:600000:正T:20261026:1","day":"20261026","code":"600000","dir":"buy_first","status":"done","qty":1000,"t_pnl_net":123.45,"t_pnl_gross":130.0,"t_fee_est":true},
		{"id":"t0:600001:反T:20261026:1","day":"20261026","code":"600001","dir":"sell_first","status":"unwound","qty":500,"t_pnl_net":-999,"t_pnl_gross":-999},
		{"id":"t0:600002:正T:20261025:1","day":"20261025","code":"600002","dir":"buy_first","status":"done","qty":300,"t_pnl_net":50,"t_pnl_gross":52}
	]}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out = read("")
	if out["state"] != "ok" {
		t.Fatalf("正常文件应 state=ok，got %v", out["state"])
	}
	pnl, _ := out["pnl"].(map[string]interface{})
	// 173.45 ＝ 123.45（今日 done）+ 50（昨日 done）；unwound 那轮的 -999 一分都不许进。
	if got := pnl["net"]; got != 173.45 {
		t.Fatalf("net 应只计 done 轮（123.45+50=173.45），got %v", got)
	}
	if got := int(pnl["done_rounds"].(float64)); got != 2 {
		t.Fatalf("累计 done 轮应是 2（跨日也算累计），got %d", got)
	}
	if share := pnl["fee_est_share"]; share != 0.5 {
		t.Fatalf("两轮 done 里一轮费用是估算，占比应 0.5，got %v", share)
	}
	if rounds, _ := out["rounds"].([]interface{}); len(rounds) != 3 {
		t.Fatalf("不带 day 时应返回全部三轮，got %d", len(rounds))
	}

	// ③ 按日过滤：只给 20261026 的两轮，但 pnl 仍是**全账本**累计口径
	// （面板同屏要同时看清"今日明细"与"累计收益"；把两者混成一个数会让当天赚 5 块看起来像累计只赚 5 块）。
	out = read("?day=20261026")
	if rounds, _ := out["rounds"].([]interface{}); len(rounds) != 2 {
		t.Fatalf("day 过滤应剩 2 轮，got %d", len(rounds))
	}
	if pnl, _ = out["pnl"].(map[string]interface{}); pnl["net"] != 173.45 {
		t.Fatalf("day 过滤不得改变累计收益，got %v", pnl["net"])
	}

	// ④ limit 越界与非法：夹到区间内，不许把整份账本或负数条数塞进响应。
	out = read("?limit=999999")
	if rounds, _ := out["rounds"].([]interface{}); len(rounds) != 3 {
		t.Fatalf("数据只有 3 轮，limit 夹顶后仍应 3，got %d", len(rounds))
	}
	for _, bad := range []string{"?limit=0", "?limit=-1", "?limit=abc"} {
		out = read(bad)
		if _, ok := out["rounds"].([]interface{}); !ok {
			t.Fatalf("%s 应返回数组，got %#v", bad, out["rounds"])
		}
	}

	// ⑤ 损坏：state=corrupt 且**文件字节不变**（接口只读；改名或删除都属于越权处置）。
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = read("")
	if out["state"] != "corrupt" {
		t.Fatalf("损坏文件应 state=corrupt，got %v", out["state"])
	}
	if keep, err := os.ReadFile(path); err != nil || string(keep) != "{not json" {
		t.Fatalf("接口不得改动损坏文件，got %q err=%v", string(keep), err)
	}

	// ⑥ 非管理员 403：实盘账本属账号私有资金留痕，不对其他登录账号开放。
	member, err := s.auth.CreateUser("member_t_rounds", "memberpw", "user", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/t-live/rounds", "")); rr.Code != http.StatusForbidden {
		t.Fatalf("非管理员应 403，got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPaperTRoundsAPIWithoutEngine 未接入模拟盘引擎时也必须回一个结构完整的空视图：
// 前端拿 null 会直接在 map 上崩，拿 {rounds:[]} 才走"暂无回转"空态。
// English: with no paper engine injected the endpoint still returns a well-formed empty view.
func TestPaperTRoundsAPIWithoutEngine(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/t-rounds", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d: %s", rr.Code, rr.Body.String())
	}
	var out tRoundsView
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应结构应可解析：%v (%s)", err, rr.Body.String())
	}
	if out.Book != "paper" || out.Rounds == nil || out.BlockSteps == nil {
		t.Fatalf("空态也应给出 book 与两个空数组，got %+v", out)
	}
}

// TestPaperTargetReadoutAPI 未接入模拟盘引擎时也必须回一个**结构化的不可得**，
// 而不是 market_scale=0 的"看起来像读数"：零值缩放会被前端渲染成"目标 0%"，
// 而真相是"这一轮根本没定权"——两者在页面上必须长得不一样。
// English: with no paper engine the endpoint returns an explicit unavailable shape,
// never a zero-filled reading that the UI would render as "target 0%".
func TestPaperTargetReadoutAPI(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/target", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["available"] != false {
		t.Fatalf("未接入引擎必须显式 available=false，got %v", out["available"])
	}
	if reason, _ := out["reason"].(string); reason == "" {
		t.Fatalf("不可得必须给原因（前端要能回答「为什么没有读数」）")
	}
	if _, ok := out["readout"]; ok {
		t.Fatalf("不可得时不得回一份零值读数（零值会被渲染成目标 0%%）：%+v", out)
	}
}
