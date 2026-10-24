package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// TestResearchProgressSnapshotServed 有落盘快照时，新 Server 首请求先返回快照（stale=true），
// 不等全表统计；这是冷缓存超时缺陷的核心回归。
func TestResearchProgressSnapshotServed(t *testing.T) {
	s, db, dir := newTestResearchServer(t)
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), []map[string]any{
		{"ts_code": "600580.SH", "name": "卧龙电驱"},
	}); err != nil {
		t.Fatalf("insert stock: %v", err)
	}
	// 先让第一个 Server 算一次并落盘快照。
	rr := httptest.NewRecorder()
	s.handleResearchProgress(rr, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr.Code != 200 {
		t.Fatalf("首算状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(s.researchProgressPath()); err != nil {
		t.Fatalf("快照未落盘: %v", err)
	}

	// 新 Server（模拟重启）没有内存缓存，但同目录有快照 → 必须立即返回旧值。
	s2 := &Server{researchDB: db, researchDir: dir}
	rr2 := httptest.NewRecorder()
	s2.handleResearchProgress(rr2, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr2.Code != 200 {
		t.Fatalf("快照命中状态码=%d body=%s", rr2.Code, rr2.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析快照响应失败: %v", err)
	}
	if body["stocks"] != float64(1) {
		t.Fatalf("快照 stocks=%v 期望 1", body["stocks"])
	}
	if body["stale"] != true {
		t.Fatalf("快照响应应标 stale=true，实到=%v", body["stale"])
	}
}

// TestResearchProgressSnapshotCorruptFallback 快照损坏时回退计算，不返回 500。
func TestResearchProgressSnapshotCorruptFallback(t *testing.T) {
	s, _, _ := newTestResearchServer(t)
	if err := os.WriteFile(s.researchProgressPath(), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("写入损坏快照: %v", err)
	}
	rr := httptest.NewRecorder()
	s.handleResearchProgress(rr, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr.Code != 200 {
		t.Fatalf("损坏快照应回退计算 200，实到=%d body=%s", rr.Code, rr.Body.String())
	}
}

// TestResearchProgressFreshCacheNoRecompute 新鲜内存缓存直接返回，不触发后台重算。
func TestResearchProgressFreshCacheNoRecompute(t *testing.T) {
	s, _, _ := newTestResearchServer(t)
	body := map[string]any{"stocks": float64(7), "ready_stocks": float64(0), "db_attached": true}
	s.researchProgressBody = body
	s.researchProgressAt = time.Now()
	rr := httptest.NewRecorder()
	s.handleResearchProgress(rr, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr.Code != 200 {
		t.Fatalf("新鲜缓存状态码=%d", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if got["stocks"] != float64(7) || got["stale"] != false {
		t.Fatalf("新鲜缓存读数异常: %s", rr.Body.String())
	}
	if s.researchProgressComputing {
		t.Fatal("新鲜缓存不应触发重算")
	}
}
