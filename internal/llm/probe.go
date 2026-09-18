package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── LLM 配置探测：热更新的前置校验 ────────────────────────────────────────────
//
// 为什么必须存在这一层：LLM 客户端实例只活在进程内存里，「配置写进磁盘」与「客户端真的能
// 调通」之间**没有任何反馈回路**。盘中换 key 时一旦写进一把坏的（过期 / 欠费 / 模型名错 /
// 地址形态错），客户端会被就地换坏 —— 整条 LLM 链路（新闻归因 / D1 评分 / 咨询）当场哑掉，
// 而且**重启也救不回来**，因为坏配置已经落库。
//
// 所以热更新的正确语义是：**先证明候选配置真能调通，再切换**。本文件提供这个证明手段。
//
// English: a pre-flight probe for hot updates. The in-memory client has no feedback loop with the
// persisted config, so a bad key written mid-session bricks the whole LLM chain *and* survives a
// restart. Probe the candidate with one real minimal call before swapping.
//
// 为什么用「真实最小 chat 调用」而不是 GET /v1/models：
//  1. 一次请求同时验证三件事 —— 密钥鉴权、地址形态（base URL vs 完整 endpoint）、模型名是否存在；
//     /v1/models 只能验第一件。
//  2. 不少自建网关 / 聚合网关根本不实现 /v1/models。
//  3. 成本约等于 0（max_tokens 32，实测 SiliconFlow 上 0.1s 级）。
type ProbeKind string

const (
	// ProbeOK 2xx：密钥、地址、模型三者全部验证通过。
	ProbeOK ProbeKind = "ok"
	// ProbeAuth 401/403：密钥无效、被吊销或无权访问该模型。
	ProbeAuth ProbeKind = "auth"
	// ProbeModel 404 或明确提到 model 的 400：地址形态错或模型名不存在。
	ProbeModel ProbeKind = "model"
	// ProbeQuota 402/额度类 403/400：欠费、余额不足、套餐用尽。
	ProbeQuota ProbeKind = "quota"
	// ProbeRateLimited 429：限流。**密钥是有效的**（429 证明鉴权已通过），只是当前被限速。
	ProbeRateLimited ProbeKind = "rate_limited"
	// ProbeBadRequest 其余 4xx：上游拒绝了这个请求。
	// 归这里**不阻断热更新**——请求体是我们构造的，400 有可能是网关不认某个字段
	// （如只认 max_completion_tokens 的网关），把这类误判成"配置错"会拦住用户合法换 key。
	ProbeBadRequest ProbeKind = "bad_request"
	// ProbeServer 5xx：供应商侧故障，与配置无关。
	ProbeServer ProbeKind = "server"
	// ProbeNetwork 连接失败/超时/DNS：无法判定配置对错。
	ProbeNetwork ProbeKind = "network"
	// ProbeNoKey 根本没有可探测的密钥。
	ProbeNoKey ProbeKind = "no_key"
)

const (
	// DefaultProbeTimeout 单把密钥的探测超时。推理模型即使只要几十个 token 也要先走思维链，
	// 给足 30s；探测只影响保存动作的响应时间，不阻塞交易主链路。
	DefaultProbeTimeout = 30 * time.Second
	// ProbeMaxTimeout 探测超时上限：账号配置了 240s 这类长超时时不能让保存动作跟着等 4 分钟。
	ProbeMaxTimeout = 60 * time.Second
	// ProbeMinTimeout 探测超时下限：账号超时配得过小时（如 5s）也不能把好配置误判成超时。
	ProbeMinTimeout = 15 * time.Second

	// probeMaxTokens 探测请求的 max_tokens。够短（成本≈0）但有意留余量：个别推理模型/
	// 网关对"过小的 max_tokens"直接回 400，那会把好配置误判成坏配置。
	probeMaxTokens = 32
	// probeRetryMaxTokens 命中「max_tokens 太小」类 400 时的二次探测上限。
	probeRetryMaxTokens = 256
	// probeConcurrency 逐把密钥并发探测的最大并行度。
	probeConcurrency = 4
	// probeBodyLimit 供应商响应体截断上限（只用于错误摘要，避免把整页 HTML 灌进日志/响应）。
	probeBodyLimit = 2048
)

// KeyProbe 单把密钥的探测结论。
type KeyProbe struct {
	// Index 该密钥在提交列表中的位次（与设置页输入框的行号一一对应）。
	Index int `json:"index"`
	// Kind 结论分类。
	Kind ProbeKind `json:"kind"`
	// Status 上游 HTTP 状态码；0 表示未拿到响应（网络层错误）。
	Status int `json:"status"`
	// Detail 可直接展示给用户的原因（含供应商原文截断）。**不含密钥本身**。
	Detail string `json:"detail"`
	// Latency 本次探测耗时（含二次重试）。
	Latency time.Duration `json:"-"`
	// LatencyMS 耗时毫秒（JSON 用）。
	LatencyMS int64 `json:"latency_ms"`
}

// Usable 该密钥是否可以进运行时轮询池。
//
// 429（限流）算可用：它证明密钥通过了鉴权，只是当前被限速，而客户端本身有按键冷却规避
// （pickKey/markKeyStatus）。把它判成不可用会让"供应商临时限流"被误读成配置错误。
func (p KeyProbe) Usable() bool { return p.Kind == ProbeOK || p.Kind == ProbeRateLimited }

// ConfigInvalid 该结论是否**指向配置本身有错**（而非供应商/网络侧问题）。
//
// 只有这一类才允许作为拒绝热更新的依据：拒绝必须有确凿证据。网络不可达、供应商 5xx、
// 我们自己的请求体被拒（400）都不算证据 —— 详见文件头的设计说明。
func (p KeyProbe) ConfigInvalid() bool {
	switch p.Kind {
	case ProbeAuth, ProbeModel, ProbeQuota:
		return true
	}
	return false
}

// Summary 单行人类可读摘要（日志与错误文案共用，保证两处口径一致）。
func (p KeyProbe) Summary() string {
	switch p.Kind {
	case ProbeOK:
		return fmt.Sprintf("key#%d 可用（HTTP %d, %dms）", p.Index+1, p.Status, p.LatencyMS)
	case ProbeRateLimited:
		return fmt.Sprintf("key#%d 密钥有效但被限流（HTTP 429），已纳入轮询池", p.Index+1)
	case ProbeNoKey:
		return fmt.Sprintf("key#%d 缺失", p.Index+1)
	}
	if p.Status == 0 {
		return fmt.Sprintf("key#%d %s：%s", p.Index+1, kindLabel(p.Kind), p.Detail)
	}
	return fmt.Sprintf("key#%d %s（HTTP %d）：%s", p.Index+1, kindLabel(p.Kind), p.Status, p.Detail)
}

// kindLabel 结论分类的中文名（错误文案用）。
func kindLabel(k ProbeKind) string {
	switch k {
	case ProbeAuth:
		return "密钥无效/无权限"
	case ProbeModel:
		return "地址或模型不可用"
	case ProbeQuota:
		return "余额/额度不足"
	case ProbeRateLimited:
		return "被限流"
	case ProbeBadRequest:
		return "请求被拒"
	case ProbeServer:
		return "供应商故障"
	case ProbeNetwork:
		return "网络不可达"
	case ProbeNoKey:
		return "未提供密钥"
	}
	return string(k)
}

// ProbeTimeoutFor 由账号配置的单次请求超时推出探测超时：夹在 [ProbeMinTimeout, ProbeMaxTimeout]
// 之间。配置 240s 时探测最多等 60s（保存动作不能跟着等 4 分钟）；配置过小时抬到 15s
// （避免把"模型首字慢"误判成"探测失败"而拦住用户的热更新）。
func ProbeTimeoutFor(configuredSec int) time.Duration {
	d := DefaultProbeTimeout
	if configuredSec > 0 {
		d = time.Duration(configuredSec) * time.Second
	}
	if d > ProbeMaxTimeout {
		d = ProbeMaxTimeout
	}
	if d < ProbeMinTimeout {
		d = ProbeMinTimeout
	}
	return d
}

// ProbeConfig 用候选配置对**每一把密钥**做一次真实最小调用，逐把给出可用性结论。
//
// 逐把并发（最多 probeConcurrency 路）：一把 key 的 30s 超时不会拖住其它把，总耗时≈最慢那把。
// 返回值与入参密钥列表**同序同长**，位次即可用于回告"第几把坏了"。
//
// 不做任何落库/切换：纯查询，供上层决定是否切换运行时客户端。
// English: probes every candidate key with one real minimal call, concurrently, in input order.
// Read-only — the caller decides whether to swap.
func ProbeConfig(cfg Config, timeout time.Duration) []KeyProbe {
	keys := normalizeKeys(cfg)
	if len(keys) == 0 {
		return []KeyProbe{{Kind: ProbeNoKey, Detail: "未配置任何密钥"}}
	}
	apiURL := normalizeAPIURL(cfg.APIURL)
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	hc := &http.Client{
		Timeout: timeout,
		// 与正式客户端同口径：禁 HTTP2，规避连接复用类偶发问题（探测结论要与线上一致）。
		Transport: &http.Transport{ForceAttemptHTTP2: false},
	}

	// 逐把并发探测：一把 key 吃满 30s 超时不会拖住其它把，总耗时≈最慢那把。
	// out 与 keys 同序同长，位次即"第几把"，上游据此回告用户去改哪一行。
	out := make([]KeyProbe, len(keys))
	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)
	for i, k := range keys {
		wg.Add(1)
		go func(i int, k string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = probeOne(hc, apiURL, model, k, i)
		}(i, k)
	}
	wg.Wait()
	return out
}

// normalizeKeys 与 llm.New 完全同口径地整理密钥列表（单 key 兜底、去空白、去重）。
//
// 必须同口径：探测结论里的 Index 是要回告"第几把 key 坏了"的，若这里的去重规则与
// llm.New 不一致，位次就会整体错位，用户按提示去改会改错那一把。
// English: same normalization as llm.New so probe indexes line up with the runtime pool.
func normalizeKeys(cfg Config) []string {
	raw := cfg.APIKeys
	if len(raw) == 0 && cfg.APIKey != "" {
		raw = []string{cfg.APIKey}
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, k := range raw {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// probeOne 探测单把密钥；命中「max_tokens 太小」类 400 时放宽上限重试一次。
func probeOne(hc *http.Client, apiURL, model, key string, idx int) KeyProbe {
	p := KeyProbe{Index: idx, Kind: ProbeOK}
	start := time.Now()
	status, detail, kind := probeRound(hc, apiURL, model, key, probeMaxTokens)
	if kind == ProbeBadRequest && looksLikeTokenLimit(detail) {
		// 上游可能是"max_tokens 太小"而非配置错：放宽再试一次。
		// 仍不通过则保留首个结论（bad_request），由上层策略决定是否阻断。
		if s2, d2, k2 := probeRound(hc, apiURL, model, key, probeRetryMaxTokens); k2 == ProbeOK {
			status, detail, kind = s2, d2, k2
		}
	}
	p.Status, p.Detail, p.Kind = status, detail, kind
	p.Latency = time.Since(start)
	p.LatencyMS = p.Latency.Milliseconds()
	return p
}

// probeRound 发一次非流式最小请求，返回 (状态码, 原因摘要, 结论分类)。
// 2xx 时状态码为 200 占位（部分网关对成功响应不回 200 而回 201/204，统一归一）。
func probeRound(hc *http.Client, apiURL, model, key string, maxTokens int) (int, string, ProbeKind) {
	payload, err := json.Marshal(chatCompletionRequest{
		ChatRequest: ChatRequest{
			Model:    model,
			Messages: []Message{{Role: "user", Content: "1"}},
		},
		Stream:    false,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return 0, "构造探测请求失败: " + err.Error(), ProbeBadRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payload))
	if err != nil {
		return 0, "请求地址不合法: " + err.Error(), ProbeNetwork
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return 0, networkDetail(err), ProbeNetwork
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 200, "", ProbeOK
	}
	msg := compactProviderMessage(body)
	kind := classifyProbeStatus(resp.StatusCode, msg)
	return resp.StatusCode, msg, kind
}

// networkDetail 网络层错误的可读摘要。http.Client 的错误里可能带完整 URL，
// 这里做一次收敛，保证不会把任何凭证带出去（Authorization 头不会出现在 err 里）。
func networkDetail(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "Client.Timeout") {
		return "连接超时（" + s + "）"
	}
	return s
}

// compactProviderMessage 供应商响应体 → 单行短摘要（错误展示与日志共用）。
func compactProviderMessage(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "(空响应体)"
	}
	// 优先取 JSON 里的 message/error 字段：原文比整包 JSON 可读得多。
	var probe struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &probe) == nil {
		switch {
		case probe.Error.Message != "":
			s = probe.Error.Message
		case probe.Message != "":
			s = probe.Message
		case probe.Detail != "":
			s = probe.Detail
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// classifyProbeStatus 把上游 HTTP 状态 + 响应摘要归类为探测结论。
func classifyProbeStatus(status int, msg string) ProbeKind {
	switch {
	case status >= 200 && status < 300:
		return ProbeOK
	case status == 401:
		return ProbeAuth
	case status == 403:
		// 403 可能是"无权"也可能是"超额/欠费"，按原文区分：欠费属额度问题，提示语完全不同。
		if looksLikeQuota(msg) {
			return ProbeQuota
		}
		return ProbeAuth
	case status == 402:
		return ProbeQuota
	case status == 404:
		return ProbeModel
	case status == 429:
		return ProbeRateLimited
	case status >= 500:
		return ProbeServer
	case status == 400 || status == 422:
		switch {
		case looksLikeQuota(msg):
			return ProbeQuota
		case looksLikeModelError(msg):
			return ProbeModel
		default:
			return ProbeBadRequest
		}
	default:
		return ProbeBadRequest
	}
}

// looksLikeQuota 判定响应摘要是否指向余额/额度不足。
func looksLikeQuota(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{
		"insufficient", "balance", "quota", "billing", "arrears", "credit",
		"欠费", "余额", "额度", "配额",
	} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// looksLikeModelError 判定响应摘要是否指向模型名/地址不存在。
func looksLikeModelError(msg string) bool {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "model") && !strings.Contains(msg, "模型") {
		return false
	}
	for _, kw := range []string{
		"not exist", "not found", "does not exist", "unknown", "invalid",
		"unsupported", "no such", "不存在", "无效",
	} {
		if strings.Contains(m, kw) || strings.Contains(strings.ToLower(msg), kw) {
			return true
		}
	}
	return false
}

// looksLikeTokenLimit 判定响应摘要是否指向 max_tokens 类参数问题（值得放宽后重试）。
func looksLikeTokenLimit(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{"max_tokens", "max tokens", "maxtokens", "max_completion_tokens", "too small", "length"} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// UsableKeys 从探测结果里挑出可进运行时轮询池的密钥。
// 入参 keys 与 probes 必须同序同长（ProbeConfig 的约定）。
func UsableKeys(keys []string, probes []KeyProbe) []string {
	out := make([]string, 0, len(keys))
	for i, p := range probes {
		if i < len(keys) && p.Usable() {
			out = append(out, keys[i])
		}
	}
	return out
}

// AllConfigInvalid 全部结论（非空）是否都指向配置本身有错。
// 只要有一条"不可判定"（网络/5xx），就不算 —— 拒绝热更新必须有确凿证据。
func AllConfigInvalid(probes []KeyProbe) bool {
	if len(probes) == 0 {
		return false
	}
	for _, p := range probes {
		if !p.ConfigInvalid() {
			return false
		}
	}
	return true
}

// ProbeSummary 把逐把结论汇总成单行（日志用）。保持入参顺序不变——位次即设置页行号。
func ProbeSummary(probes []KeyProbe) string {
	parts := make([]string, 0, len(probes))
	for _, p := range probes {
		parts = append(parts, p.Summary())
	}
	return strings.Join(parts, " | ")
}
