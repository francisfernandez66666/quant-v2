// box_heartbeat_test.go — §BOX-HEARTBEAT（2026-11-23）的云端落盘/读面用例。
//
// 四条边界（与实现文件同源）：
//  1. 一次心跳覆盖一次，readout 的 age_sec 用**云端收到时刻**算（盒子钟差只进 box_at）；
//  2. 未收到心跳 ⇒ known=false + 原因，绝不折成"客户端 stopped"；
//  3. 脏值归一：client_proc 只认三态，strategy_log_at 必须 RFC3339；
//  4. 空 uid / 无账号目录 ⇒ 不落盘、读面报 account_unavailable（绝不落进别人的桶）。
package server

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func heartbeatTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{cacheDir: t.TempDir()}
}

// TestBoxHeartbeatRoundTrip 落一次再读回：版本/进程/日志三枚读数逐字段透传，
// age_sec 用云端收到时刻算（盒子自报时刻只进 box_at）。
func TestBoxHeartbeatRoundTrip(t *testing.T) {
	s := heartbeatTestServer(t)
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	age := int64(42)
	logAt := "2026-11-23T14:59:18+08:00"
	ev := qmtReportEvent{
		Type:              "heartbeat",
		At:                "2026-11-23T15:00:01+08:00",
		StrategyVersion:   "v165-20261123",
		ClientProc:        "running",
		StrategyLogAt:     logAt,
		StrategyLogAgeSec: &age,
	}
	s.recordBoxHeartbeat("u_1", ev, now)
	out := s.boxHeartbeatReadout("u_1", now.Add(5*time.Second))
	if !out.Known {
		t.Fatalf("刚写入的心跳读不到: %+v", out)
	}
	if out.ClientProc != "running" || out.StrategyVersion != "v165-20261123" {
		t.Fatalf("读数没透传: %+v", out)
	}
	if out.StrategyLogAt != logAt {
		t.Fatalf("日志时间被改写: %q", out.StrategyLogAt)
	}
	if out.StrategyLogAgeSec == nil || *out.StrategyLogAgeSec != age {
		t.Fatalf("日志年龄被改写: %+v", out.StrategyLogAgeSec)
	}
	if out.AgeSec == nil || *out.AgeSec != 5 {
		t.Fatalf("age_sec 必须按云端收到时刻算（5s），got %+v", out.AgeSec)
	}
}

func TestBoxHeartbeatAbsentIsUnknownNotStopped(t *testing.T) {
	s := heartbeatTestServer(t)
	out := s.boxHeartbeatReadout("u_1", time.Now())
	if out.Known {
		t.Fatal("没收到过心跳却报 known=true")
	}
	if out.ClientProc != "" {
		t.Fatalf("没收到过心跳却给出了进程结论: %q", out.ClientProc)
	}
	if out.UnknownReason != "heartbeat_absent" {
		t.Fatalf("原因必须点名 heartbeat_absent，got %q", out.UnknownReason)
	}
}

func TestBoxHeartbeatNormalizesDirtyValues(t *testing.T) {
	s := heartbeatTestServer(t)
	now := time.Now()
	s.recordBoxHeartbeat("u_1", qmtReportEvent{
		Type:            "heartbeat",
		ClientProc:      "RUNNING!",
		StrategyLogAt:   "2026-11-23 14:59:18", // 非 RFC3339 ⇒ 整键丢弃
		StrategyVersion: "v165-20261123",
	}, now)
	out := s.boxHeartbeatReadout("u_1", now)
	if out.ClientProc != "unknown" {
		t.Fatalf("脏进程值必须归 unknown（不许读成确认在跑），got %q", out.ClientProc)
	}
	if out.StrategyLogAt != "" {
		t.Fatalf("非 RFC3339 的日志时间不许落盘，got %q", out.StrategyLogAt)
	}
}

func TestBoxHeartbeatRefusesEmptyUID(t *testing.T) {
	s := heartbeatTestServer(t)
	s.recordBoxHeartbeat("", qmtReportEvent{Type: "heartbeat", ClientProc: "running"}, time.Now())
	if _, err := os.Stat(s.boxHeartbeatPath("")); err == nil {
		t.Fatal("空 uid 竟然落了盘（会串到别人的桶）")
	}
	out := s.boxHeartbeatReadout("", time.Now())
	if out.Known || out.UnknownReason != "account_unavailable" {
		t.Fatalf("空 uid 的读面必须 unknown/account_unavailable，got %+v", out)
	}
}

func TestBoxHeartbeatFutureStampClampsAge(t *testing.T) {
	s := heartbeatTestServer(t)
	now := time.Now()
	raw := boxHeartbeat{At: now.Add(2 * time.Minute).Format(time.RFC3339), ClientProc: "running"}
	path := s.boxHeartbeatPath("u_1")
	if err := writeBoxHeartbeat(path, &raw); err != nil {
		t.Fatal(err)
	}
	out := s.boxHeartbeatReadout("u_1", now)
	if out.AgeSec == nil || *out.AgeSec != 0 {
		t.Fatalf("未来时刻（钟差）必须夹成 0，got %+v", out.AgeSec)
	}
}

func TestBoxHeartbeatBadJSONIsUnknown(t *testing.T) {
	s := heartbeatTestServer(t)
	path := s.boxHeartbeatPath("u_1")
	if err := os.MkdirAll(path[:len(path)-len(boxHeartbeatFileName)], 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := s.boxHeartbeatReadout("u_1", time.Now())
	if out.Known || out.UnknownReason != "heartbeat_bad_json" {
		t.Fatalf("坏 JSON 必须 unknown/heartbeat_bad_json，got %+v", out)
	}
}

// TestBoxHeartbeatJSONKeys 钉读面的线上键名（前端按这些键渲染，改名必须同批改前端）。
func TestBoxHeartbeatJSONKeys(t *testing.T) {
	age := int64(10)
	out := boxHeartbeatReadout{
		Known: true, At: "2026-11-23T15:00:00+08:00", AgeSec: &age,
		BoxAt: "2026-11-23T15:00:01+08:00", ClientProc: "running",
		StrategyLogAt: "2026-11-23T14:59:50+08:00", StrategyLogAgeSec: &age,
		StrategyVersion: "v165-20261123",
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"known", "at", "age_sec", "box_at", "client_proc",
		"strategy_log_at", "strategy_log_age_sec", "strategy_version"} {
		if _, ok := m[k]; !ok {
			t.Errorf("读面缺键 %q（前端契约）", k)
		}
	}
}
