// 文件概述（internal/server/decode_strict.go）
// §F2-20261114 写面「未知字段拒绝 + 回报」通用解码器（批次 2.1）。
//
// 为什么不用 encoding/json 的 DisallowUnknownFields 全局开启：配置写面依赖
// "没传=保留旧值"的稀疏 merge 合法用法，一刀切会把全部配置面打断。正确姿势是
// "先原始键集合、再合并、未知键收集回报"（与 handleSetD1Config 的 RawMessage
// 合并同构，2026-09-29 同类事故——未知键静默生效改了配置——的根治面）。
//
// 两种写面形态（v3 订正：不能"一次改动覆盖全部写面"）：
//   - typed struct：本解码器直接接管，未知键进 ignored_keys 回报；
//   - map 解码（handleSetStrategyConfig 等）：没有 json tag 可比，各 handler 显式
//     声明键白名单（map[string]bool）后用 ReportIgnoredKeys 回报——白名单登记在
//     verify_changes.sh §184 B3。
//
// English: §F2-20261114 strict JSON decoding for write surfaces — unknown keys are
// collected and reported back as ignored_keys instead of being silently applied.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// decodeStrictJSON 严格解码 JSON 对象到 dst（必须 *struct）：
//   - 空体 / 非 JSON / 非对象 → error（调用方回 400）；
//   - 未知键不参与合并，收集进返回的 ignored 列表（调用方回报给调用端，不静默吞）。
//
// English: strict JSON decode; empty/malformed body errors, unknown keys returned in ignored.
func decodeStrictJSON(r *http.Request, dst interface{}) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 {
		return nil, fmt.Errorf("empty request body")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(trim, &keys); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if err := json.Unmarshal(trim, dst); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	t := reflect.TypeOf(dst)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("decodeStrictJSON: dst must be a struct pointer")
	}
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		allowed[name] = true
	}
	var ignored []string
	for k := range keys {
		if !allowed[k] {
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)
	return ignored, nil
}

// reportIgnoredKeys 把未知键并入响应（len==0 时不加字段，保持既有响应形态不变）。
// English: merges ignored_keys into the response payload when non-empty.
func reportIgnoredKeys(payload map[string]interface{}, ignored []string) map[string]interface{} {
	if len(ignored) > 0 {
		payload["ignored_keys"] = ignored
	}
	return payload
}

// decodeStrictB 是 B 类写面 handler 的接线助手：解码失败直接回 400 并返回 false，
// 成功返回 (ignored, true)。未知键经 stashIgnoredKeys 存进请求上下文，
// 由 withIgnoredKeys 中间件在响应阶段并入 ignored_keys（handler 的响应形态无需改动）。
func decodeStrictB(w http.ResponseWriter, r *http.Request, dst interface{}) ([]string, bool) {
	ignored, err := decodeStrictJSON(r, dst)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return nil, false
	}
	stashIgnoredKeys(r, ignored)
	return ignored, true
}

// ── ignored_keys 上下文注入（§F2-20261114 批次 2.3 的响应回报腿）──────────────────
//
// 为什么用中间件而不是逐 handler 改 writeJSON：B 类写面的响应形态各异
// （map[string]bool / map[string]string / 自定义 struct），逐个改响应既容易漏也容易
// 改坏前端契约。中间件在 handler 之后对响应体做一次「JSON 对象 + ignored_keys」合并，
// 没有未知键时零改动透传（字节级等价，现有契约不受影响）。

type ignoredKeysCtxKey struct{}

type ignoredKeysHolder struct{ keys []string }

func stashIgnoredKeys(r *http.Request, ignored []string) {
	if len(ignored) == 0 {
		return
	}
	if h, ok := r.Context().Value(ignoredKeysCtxKey{}).(*ignoredKeysHolder); ok {
		h.keys = append(h.keys, ignored...)
	}
}

// unknownMapKeys 计算 map 解码写面的白名单外键：白名单 = typ（可空）结构体的 json tag 全集
// + extras 逐个点名。map 写面（配置 merge 面）按设计接受任意键，不能改成拒绝，
// 但白名单外的键必须如实回报（§F2 批次 2.1 的 v3 订正：这类写面 reflect 白名单无从比对，
// 只能每个 handler 显式点名）。English: whitelist-of-keys reporting for map-decoded write faces.
func unknownMapKeys(body map[string]json.RawMessage, typ reflect.Type, extras ...string) []string {
	allowed := map[string]bool{}
	for _, e := range extras {
		allowed[e] = true
	}
	if typ != nil && typ.Kind() == reflect.Struct {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = true
			}
		}
	}
	var ignored []string
	for k := range body {
		if !allowed[k] {
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)
	return ignored
}

// newsReanalyzeDedup §F2-20261114 D 类幂等：uid+client_id 在窗口内只放行一次补推。
// 进程内小账即可（补推本就是单机手动动作，重启丢账 = 最多多推一次，无害）。
func (s *Server) newsReanalyzeDedup(uid, clientID string) bool {
	s.reanalyzeMu.Lock()
	defer s.reanalyzeMu.Unlock()
	if s.reanalyzeSeen == nil {
		s.reanalyzeSeen = map[string]time.Time{}
	}
	key := uid + "\x00" + clientID
	if t, ok := s.reanalyzeSeen[key]; ok && time.Since(t) < 5*time.Minute {
		return true
	}
	// 顺手清过期，防长驻进程键集无界增长
	for k, t := range s.reanalyzeSeen {
		if time.Since(t) >= 5*time.Minute {
			delete(s.reanalyzeSeen, k)
		}
	}
	s.reanalyzeSeen[key] = time.Now()
	return false
}

// ignoredInjector 缓冲响应体，flush 时按需并入 ignored_keys。
type ignoredInjector struct {
	http.ResponseWriter
	buf  bytes.Buffer
	code int
}

// WriteHeader 记录下游 handler 写入的状态码并透传给真实 ResponseWriter（注入只改 body，不改状态码）。
func (w *ignoredInjector) WriteHeader(code int) { w.code = code; w.ResponseWriter.WriteHeader(code) }

// Write 缓冲下游 handler 的响应体，flush 前不向真实连接写任何字节（防"原体+注入体"重复输出）。
func (w *ignoredInjector) Write(b []byte) (int, error) { return w.buf.Write(b) }

// withIgnoredKeys 包装 handler：未知键在册时把 ignored_keys 并入 JSON 响应对象；
// 响应不是 JSON 对象或没有未知键时逐字节透传。
func withIgnoredKeys(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := &ignoredKeysHolder{}
		inj := &ignoredInjector{ResponseWriter: w, code: 200}
		next(inj, r.WithContext(context.WithValue(r.Context(), ignoredKeysCtxKey{}, h)))
		if len(h.keys) == 0 || inj.buf.Len() == 0 {
			_, _ = w.Write(inj.buf.Bytes())
			return
		}
		var m map[string]interface{}
		if err := json.Unmarshal(inj.buf.Bytes(), &m); err != nil {
			_, _ = w.Write(inj.buf.Bytes()) // 非 JSON 对象（数组/空）不注入，透传
			return
		}
		sort.Strings(h.keys)
		m["ignored_keys"] = h.keys
		b, err := json.Marshal(m)
		if err != nil {
			_, _ = w.Write(inj.buf.Bytes())
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	}
}
