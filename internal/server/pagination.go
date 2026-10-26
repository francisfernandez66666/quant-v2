// pagination.go — 列表接口统一分页（2026-10-26，旧审计 F9）。
//
// 口径：page 从 1 开始；page_size 缺省由调用方给（0=不分页，保持旧行为）；
// 上限 maxSize 防一次拉爆；has_more 由 total/end 推导，绝不靠"返回条数等于 page_size"猜。
// English: shared pagination helpers — 1-based page, size 0 = no slicing (legacy), capped size,
// and has_more derived from total/end rather than guessed from a full page.
package server

import (
	"net/http"
	"strconv"
)

// parsePageParams 解析 page/page_size；size 允许 0（不分页），非正数按 0 处理。
func parsePageParams(r *http.Request, defaultSize, maxSize int) (page, size int) {
	page, size = 1, defaultSize
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			size = n
		}
	}
	if size < 0 {
		size = 0
	}
	if maxSize > 0 && size > maxSize {
		size = maxSize
	}
	return page, size
}

// pageBounds 返回 0 基半开区间 [start,end) 与是否还有下一页；size<=0 表示不分页。
func pageBounds(total, page, size int) (start, end int, hasMore bool) {
	if total <= 0 {
		return 0, 0, false
	}
	if size <= 0 {
		return 0, total, false
	}
	start = (page - 1) * size
	if start >= total {
		return total, total, false
	}
	end = start + size
	if end > total {
		end = total
	}
	return start, end, end < total
}

// setPaginationHeaders 数组型接口用响应头回传总数/页码（不改数组响应体，前端可增量接入）。
func setPaginationHeaders(w http.ResponseWriter, total, page, size int) {
	totalPages := 0
	if size > 0 {
		totalPages = (total + size - 1) / size
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Page-Size", strconv.Itoa(size))
	w.Header().Set("X-Total-Pages", strconv.Itoa(totalPages))
	w.Header().Set("X-Has-More", strconv.FormatBool(size > 0 && page < totalPages))
}
