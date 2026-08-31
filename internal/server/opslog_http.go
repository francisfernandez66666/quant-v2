// opslog_http.go — §DAILY_OPSLOG 每日系统运行日志的前端只读接口（仅管理员）。
//
// 路由（均经 adminMiddleware，只读、不产生任何写入副作用）：
//
//	GET /api/opslog/dates            → {"dates":[{"date":"20260831","size":2048},...]}（日期倒序）
//	GET /api/opslog?date=YYYYMMDD    → {"date":"20260831","lines":[...],"total":N,"truncated":bool}
//	                                    date 缺省 = 本地今天；tail 参数可选（默认 2000，上限 5000，取最后 N 行）
//
// 安全：date 严格校验 8 位数字后与固定前缀/后缀拼名——绝无路径穿越面；文件只读打开。
// 目录：s.researchDir（数据目录）下的 opslog/ 子目录，与 internal/opslog 包写入侧约定一致。
// English: read-only admin endpoints exposing the daily ops journal; the date parameter is
// strictly validated (8 digits) so file access is whitelist-shaped — no traversal surface.
package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// opslogDateRe 严格 8 位数字（YYYYMMDD），杜绝路径注入。
var opslogDateRe = regexp.MustCompile(`^\d{8}$`)

// opslogDirPath 运行日志目录；数据目录未接入时返回空串（接口按 503 语义报"未配置"）。
func (s *Server) opslogDirPath() string {
	if s.researchDir == "" {
		return ""
	}
	return filepath.Join(s.researchDir, "opslog")
}

// handleOpslogDates 列出已有日志的日期与大小（倒序），前端做日期选择器数据源。
func (s *Server) handleOpslogDates(w http.ResponseWriter, r *http.Request) {
	dir := s.opslogDirPath()
	if dir == "" {
		writeError(w, 503, "opslog dir not configured")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]interface{}{"dates": []interface{}{}})
			return
		}
		writeError(w, 500, "read opslog dir: "+err.Error())
		return
	}
	type dateItem struct {
		Date string `json:"date"`
		Size int64  `json:"size"`
	}
	dates := make([]dateItem, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "opslog-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		d := strings.TrimSuffix(strings.TrimPrefix(name, "opslog-"), ".log")
		if !opslogDateRe.MatchString(d) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dates = append(dates, dateItem{Date: d, Size: info.Size()})
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Date > dates[j].Date }) // 新日期在前
	writeJSON(w, 200, map[string]interface{}{"dates": dates})
}

// handleOpslog 读取某天的日志内容（缺省今天），tail 取最后 N 行（新事件在文件尾部）。
func (s *Server) handleOpslog(w http.ResponseWriter, r *http.Request) {
	dir := s.opslogDirPath()
	if dir == "" {
		writeError(w, 503, "opslog dir not configured")
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = time.Now().Format("20060102") // 缺省今天（本地时区）
	}
	if !opslogDateRe.MatchString(date) {
		writeError(w, 400, "date must be YYYYMMDD")
		return
	}
	// tail 行数上限：默认 2000，钳制 1..5000（策划性低频日志正常远小于此）
	tail := 2000
	if v := strings.TrimSpace(r.URL.Query().Get("tail")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}
	if tail > 5000 {
		tail = 5000
	}

	data, err := os.ReadFile(filepath.Join(dir, "opslog-"+date+".log"))
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]interface{}{"date": date, "lines": []string{}, "total": 0, "truncated": false})
			return
		}
		writeError(w, 500, "read opslog: "+err.Error())
		return
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = nil
	}
	total := len(all)
	truncated := total > tail
	if truncated {
		all = all[total-tail:]
	}
	writeJSON(w, 200, map[string]interface{}{"date": date, "lines": all, "total": total, "truncated": truncated})
}
