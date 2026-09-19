// 文件概述（internal/server/a7_build_commit_test.go）
// §A7（20260918 审计批）GET /api/status 的 build_commit 字段契约测试：
// 后端把构建期 git 指纹（SetBuildCommit 注入）原样下发，供 APK 内嵌前端比对本地构建版本。
// 覆盖：注入后字段可见、未注入时为空串（前端约定空/unknown/dev 不参与比对）。
// English: §A7 — /api/status must report the build-time git commit injected via SetBuildCommit,
// so the APK-bundled frontend can detect stale embedded assets; empty when not injected.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/display"
)

// TestStatusBuildCommitField statusBuildCommitField。
// /api/status 响应应带 build_commit 字段。
func TestStatusBuildCommitField(t *testing.T) {
	// 构造最小可运行的 Server：agg 用零值聚合器（Current 返回 nil，走 dashboard 空分支）
	s := &Server{startTime: time.Now(), agg: &display.Aggregator{}}
	// 未注入时应下发空串而不是缺字段
	rr := httptest.NewRecorder()
	s.handleFixStatus(rr, httptest.NewRequest("GET", "/api/status", nil))
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	v, ok := out["build_commit"]
	if !ok {
		t.Fatalf("响应缺少 build_commit 字段")
	}
	if v != "" {
		t.Fatalf("未注入时 build_commit 应为空串，实际 %v", v)
	}
	// 注入后原样透传（部署脚本以 -ldflags -X main.buildCommit 注入同一 checkout 指纹）
	s.SetBuildCommit("ab12cd3")
	rr2 := httptest.NewRecorder()
	s.handleFixStatus(rr2, httptest.NewRequest("GET", "/api/status", nil))
	var out2 map[string]interface{}
	if err := json.Unmarshal(rr2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	if out2["build_commit"] != "ab12cd3" {
		t.Fatalf("build_commit 未透传，实际 %v", out2["build_commit"])
	}
}
