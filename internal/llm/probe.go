package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
//
// §P0 2026-09-20（广州线上实录「股票咨询报错 no response from LLM，响应摘录=<!DOCTYPE html>」）：
// 上面第 1 条**曾经是假的**——旧实现拿到 2xx 就返回 ProbeOK，**从不看响应体**，于是
// 「api_url 填成供应商网页控制台域名（cloud.<vendor>.cn）」这种地址：控制台对任意路径都
// 307 跳到 account.<vendor>.cn/login，客户端跟随重定向拿到登录页 HTML + HTTP 200 →
// 探测报告"8/8 可用 verified=true" → 热更新照常生效 → 运行时每次咨询都拿到一页 HTML，
// 数据分片为 0。**本该拦住坏配置的那道闸，恰好成了放行它的那道门。**
// 现在 2xx 也必须通过响应体校验（ProbeNotEndpoint），"地址形态/模型名"才算真的验过。
// English: a 2xx is no longer sufficient — the body must actually be a chat-completion
// response, otherwise a provider *console* URL (307 → login page HTML + 200) sails through.
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
	// ProbeNotEndpoint 2xx，但响应体不是 LLM 接口响应（HTML 网页 / 非 JSON / 无 choices）：
	// 地址本身就不是 API 端点（典型：填了供应商网页控制台域名，被 307 重定向到登录页）。
	//
	// 归入 ConfigInvalid：这条**是确凿证据**——一个能用的 OpenAI 兼容端点不可能对
	// chat/completions 回一页 HTML。留着它不拦，等于把每次调用都送去解析 HTML。
	ProbeNotEndpoint ProbeKind = "not_endpoint"
	// ProbeBudget 这次探测**没来得及判定**：一次探测的总时长预算（ProbeMaxTotalBudget）
	// 已经用尽，这把 key 还没轮到 / 或刚发出就被总预算掐断。
	//
	// 为什么单独开一类而不是并进 ProbeNetwork：network 的说法是"对端不可达"，那是**对端**的事；
	// 这条是**我们自己**的预算不够（key 太多、上游太慢），把它说成"网络不可达"会让用户
	// 跑去查网络、甚至以为 key 坏了。§2026-09-20 广州实测：同一把 key 一次报超时、一次
	// 报可用（18:08 轮 key#2 超时、18:09 轮 key#2 18958ms 可用）——结论本身就是噪声，
	// 措辞必须诚实。
	//
	// **不进 ConfigInvalid**：预算用尽完全不能说明配置有错，据此剔除同样会误删好 key。
	ProbeBudget ProbeKind = "budget"
)

const (
	// DefaultProbeTimeout 单把密钥的探测超时。推理模型即使只要几十个 token 也要先走思维链，
	// 给足 45s；探测只影响保存动作的响应时间，不阻塞交易主链路。
	//
	// §2026-09-20 由 30s 上调到 45s（**实测依据，不是保守加码**）：广州机上对同一地址/同一密钥
	// 用 curl 实测（gz_llm_probe5.ps1），串行 3 次 = 9.5s / 17.7s / 21.8s，4 路并发 =
	// 24.7s / 12.1s / 22.9s / 30.0s（全部 HTTP 200）。即**正常**调用就能跑到 30s ——
	// 旧值恰好压在延迟分布最右端，于是 8 把 key 一轮探测里有 3~5 把被误报"网络不可达"，
	// 而下一版旧代码会据此把它们从池里与库里删掉（2026-09-20 生产实录：8 把变 4 把）。
	// 45s 相对实测 p100（30.0s）留 1.5 倍余量。
	DefaultProbeTimeout = 45 * time.Second
	// ProbeMaxTimeout 探测超时上限：账号配置了 240s 这类长超时时不能让保存动作跟着等 4 分钟。
	ProbeMaxTimeout = 60 * time.Second
	// ProbeMinTimeout 探测超时下限：账号超时配得过小时（如 5s）也不能把好配置误判成超时。
	// 30s 之上的理由同 DefaultProbeTimeout —— 低于实测正常延迟的下限只会制造假阴性。
	ProbeMinTimeout = 30 * time.Second
	// ProbeMaxTotalBudget 一次探测（不论提交多少把 key）消耗的**总时长上界**。
	//
	// §2026-09-20 为什么需要有这个上界（这不是保守加码，是修一个真实故障）：
	// 后端真正出网逐把探测时，单把最长 ProbeMaxTimeout=60s、并发 probeConcurrency=4 路，
	// 于是 N 把 key 的最坏耗时 = ceil(N/4) × 单把超时 —— **随 key 数线性增长、无上界**。
	// 而调用方（设置页"测试连接"/保存）是**同步等待**的 HTTP 请求：它必须先知道
	// "最长等多久"才能把超时设成够大。没有上界 ⇒ 前端超时永远可能不够 ⇒
	// 用户看到的是「探测失败: 请求超时」，而后端那次探测其实**成功了**
	// （2026-09-20 生产实录：后端 probe=53975ms / 58214ms / 59421ms，前端 10s 就放弃）。
	//
	// 90s = 两波（8 把） × 45s，正好覆盖当前池规模；更大的池子里排在后面的 key 会报
	// ProbeBudget（"未完成判定"，**保留在池里**），而不是把用户无限期挂在页面上。
	ProbeMaxTotalBudget = 90 * time.Second

	// probeMaxTokens 探测请求的 max_tokens。够短（成本≈0）但有意留余量：个别推理模型/
	// 网关对"过小的 max_tokens"直接回 400，那会把好配置误判成坏配置。
	probeMaxTokens = 32
	// probeRetryMaxTokens 命中「max_tokens 太小」类 400 时的二次探测上限。
	probeRetryMaxTokens = 256
	// probeConcurrency 逐把密钥并发探测的最大并行度。
	//
	// §2026-09-20 实测判定**不要**上调（有人会想"8 把 key 一趟并发完更省时间"）：
	// 广州机上同一地址/同一密钥实测（gz_llm_probe5/6.ps1）——
	//   4 路并发：4/4 全部 HTTP 200，耗时 12.1s / 22.9s / 24.7s / 30.0s；
	//   8 路并发：6/8 成功（4.9s~56.7s），**2 路 60s 内 0 字节超时**。
	// 也就是上游会拖挂高并发下的部分连接 —— 加到 8 只会把"假超时"从 1/4 变成 2/8，
	// 反而更像"探测不可靠"。默认 4 是当前上游条件下的安全点。
	probeConcurrency = 4
	// probeBodyLimit 供应商响应体截断上限（只用于错误摘要，避免把整页 HTML 灌进日志/响应）。
	probeBodyLimit = 2048
	// probeShapeBodyLimit 「响应体是不是 LLM 响应」这条判定的读取上限：探测响应只有
	// max_tokens=32，64KB 足以读全任何正常回包，同时又不至于被一页巨型 HTML 拖住。
	probeShapeBodyLimit = 64 << 10
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
//
// §2026-09-20 增补 not_endpoint：上游回 2xx 但响应体是网页/非 LLM 结构，这是**地址配错**的
// 直接证据（比 401/404 更硬），故与 auth/model/quota 同列。这是对原「只有三类可拒绝」口径的
// 有意扩展，理由与依据见 docs/BUGFIX_LLM_CONSOLE_URL_20260920.md。
//
// **不要**把 ProbeBudget / ProbeNetwork / ProbeServer / ProbeBadRequest 加进来：它们的
// 共同点是"错的一方不是配置"（是我们预算不够 / 对端抖动 / 我们构造的请求体不被某个网关认），
// 拿它们当拒绝依据等于把供应商抖动中的用户堵死在坏状态里 —— 详见文件头不变量②。
func (p KeyProbe) ConfigInvalid() bool {
	switch p.Kind {
	case ProbeAuth, ProbeModel, ProbeQuota, ProbeNotEndpoint:
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
	case ProbeBudget:
		return "未完成判定"
	case ProbeNotEndpoint:
		return "地址不是 API 端点"
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

// ProbeTotalBudget 一次探测的总时长上界：按 key 数与并发度估出"波数 × 单把超时"，
// 再夹到 ProbeMaxTotalBudget。
//
// 存在的意义是**给调用方一个可依赖的等待上界**。设置页的"测试连接/保存"是同步 HTTP 请求，
// 它的前端超时必须 ≥ 这个值：否则会出现"后端还在探测、浏览器已经放弃"，用户看到
// 「探测失败: 请求超时」，而后端日志里那次探测其实是成功的（2026-09-20 生产实录）。
// English: a hard upper bound on one probe run — the caller's request timeout must be ≥ this,
// otherwise the browser gives up while the server is still probing.
func ProbeTotalBudget(timeout time.Duration, keyCount int) time.Duration {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if keyCount <= 0 {
		return timeout
	}
	// 并发 probeConcurrency 路 ⇒ ceil(N/4) 波，每波最坏等一把 key 的超时。
	waves := (keyCount + probeConcurrency - 1) / probeConcurrency
	total := time.Duration(waves) * timeout
	if total > ProbeMaxTotalBudget {
		total = ProbeMaxTotalBudget
	}
	return total
}

// ProbeConfig 用候选配置对**每一把密钥**做一次真实最小调用，逐把给出可用性结论。
//
// 逐把并发（最多 probeConcurrency 路）：一把 key 吃满自己的超时不会拖住其它把。
// 返回值与入参密钥列表**同序同长**，位次即可用于回告"第几把坏了"。
//
// 整个探测过程受 ProbeTotalBudget 约束：到点仍未判定的 key 报 ProbeBudget（保留在池里），
// 绝不会让调用方无限期等待——这是前端能把超时设成确定值的**前提**。
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
	// 总预算落成 ctx：整个探测（含排队等并发位）都在这个 deadline 内结束。
	// 有意以 context.Background() 为父，而不是挂到触发它的 HTTP 请求 ctx 上 ——
	// 浏览器/代理提前断开只应影响"响应能否送达"，不该把服务端的判定一起取消；
	// 否则日志里连"这次探测其实是好的"都看不到，排查时会把责任错判给上游。
	budget := ProbeTotalBudget(timeout, len(keys))
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return probeKeys(ctx, &http.Client{
		Timeout: timeout,
		// 与正式客户端同口径：禁 HTTP2，规避连接复用类偶发问题（探测结论要与线上一致）。
		Transport: &http.Transport{ForceAttemptHTTP2: false},
	}, apiURL, model, keys, timeout, budget)
}

// probeKeys 在给定 ctx 下并发探测 keys；ctx 到点后仍未判定的 key 报 ProbeBudget。
//
// 单独成函数是为了**这条路径可测**：若它埋在 ProbeConfig 里，要触发总预算用尽就真的得等
// 90s（或塞 9 把以上 key），单测没法在毫秒级覆盖。现在把"已经到点的 ctx"直接喂进来即可，
// 见 probe_test.go::TestProbeKeysBudgetExhaustedIsNotNetwork。
func probeKeys(ctx context.Context, hc *http.Client, apiURL, model string, keys []string, timeout, budget time.Duration) []KeyProbe {
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
			// 排到并发位时总预算可能已经用尽（前面的波次吃满了）：直接报"未轮到"，
			// 不要发一个注定被立刻掐断的请求、再把它说成"网络不可达"。
			if err := ctx.Err(); err != nil {
				out[i] = budgetExhaustedProbe(i, timeout, budget, err)
				return
			}
			out[i] = probeOne(ctx, hc, apiURL, model, k, i, timeout, budget)
		}(i, k)
	}
	wg.Wait()
	return out
}

// budgetExhaustedProbe 构造"时限内未完成判定"的结论。
//
// Detail 里必须写明**这不代表密钥不可用**：用户在设置页看到红字时最容易的误判就是
// "我的 key 坏了"，而这条结论的全部证据只是"我们自己没等到"。
func budgetExhaustedProbe(idx int, timeout, budget time.Duration, err error) KeyProbe {
	if err == nil {
		err = context.DeadlineExceeded
	}
	return KeyProbe{
		Index: idx,
		Kind:  ProbeBudget,
		Detail: fmt.Sprintf(
			"未能在时限内完成判定（单把上限 %s，本次总预算 %s）: %v；上游慢或密钥较多时会出现，"+
				"**不代表密钥不可用**（该密钥仍留在轮询池里）",
			timeout, budget, err),
	}
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
func probeOne(ctx context.Context, hc *http.Client, apiURL, model, key string, idx int, timeout, budget time.Duration) KeyProbe {
	p := KeyProbe{Index: idx, Kind: ProbeOK}
	start := time.Now()
	status, detail, kind := probeRound(ctx, hc, apiURL, model, key, probeMaxTokens)
	if kind == ProbeBadRequest && looksLikeTokenLimit(detail) {
		// 上游可能是"max_tokens 太小"而非配置错：放宽再试一次。
		// 仍不通过则保留首个结论（bad_request），由上层策略决定是否阻断。
		if s2, d2, k2 := probeRound(ctx, hc, apiURL, model, key, probeRetryMaxTokens); k2 == ProbeOK {
			status, detail, kind = s2, d2, k2
		}
	}
	// 请求发出去了、却倒在**我们自己的总预算**上：这不是"对端不可达"，别把结论说错。
	// 判据是父 ctx 已到点（单把超时由内层 ctx 造成，不会让父 ctx 到点）。
	// 两者同时到点（4 把 key 时 单把 45s == 总预算 45s）时归到 ProbeBudget 也更诚实：
	// 我们确实**没能判定**，而不是"测出网络不通"。
	if kind == ProbeNetwork && ctx.Err() != nil {
		bp := budgetExhaustedProbe(idx, timeout, budget, ctx.Err())
		bp.Latency = time.Since(start)
		bp.LatencyMS = bp.Latency.Milliseconds()
		return bp
	}
	p.Status, p.Detail, p.Kind = status, detail, kind
	p.Latency = time.Since(start)
	p.LatencyMS = p.Latency.Milliseconds()
	return p
}

// probeRound 发一次非流式最小请求，返回 (状态码, 原因摘要, 结论分类)。
// 2xx 时状态码为 200 占位（部分网关对成功响应不回 200 而回 201/204，统一归一）。
//
// ctx 是**整个探测过程**的总预算；单把超时在其下再夹一层，两者取更早的 deadline。
// 用 context.Background() 起头是刻意的：探测不能挂到 HTTP 请求的 ctx 上——浏览器/代理
// 提前断开（例如前端超时太短）只应影响"响应能不能送达"，不该把服务端的判定也一起取消，
// 否则日志里会连"这次探测其实是好的"都看不到。
func probeRound(ctx context.Context, hc *http.Client, apiURL, model, key string, maxTokens int) (int, string, ProbeKind) {
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
	// 单把超时夹在总预算之下（context.WithTimeout 取更早的 deadline）。
	rctx, cancel := context.WithTimeout(ctx, hc.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", apiURL, bytes.NewReader(payload))
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
	// 判定用读上限（64KB）：探测响应只有 32 token，够读全；既保证"响应体是不是 LLM 响应"
	// 的判断建立在完整报文上，也不至于被一页巨大的 HTML 拖住。
	body, truncated := readLimited(resp.Body, probeShapeBodyLimit)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// §P0 2026-09-20：2xx 不再等于"可用"——必须确认响应体真的是 chat completion。
		// 见文件头「§P0 2026-09-20」段：网页控制台地址会 307 → 登录页 HTML + 200。
		if ok, isJSON, why := llmBodyShape(body, truncated); !ok {
			if !isJSON {
				// HTML / 空体 / 非 JSON：地址根本不是 API 端点（确凿证据）。
				return resp.StatusCode, notEndpointDetail(apiURL, resp, body, why), ProbeNotEndpoint
			}
			// 是 JSON 但没有 choices（多半是 {"error":{...}} 而状态码却是 2xx）：
			// 沿用 4xx 那套分类，能识别出"模型名错/欠费"就照实说，别一律归成地址错。
			msg := compactProviderMessage(body)
			return resp.StatusCode, msg, classifyProbeStatus(http.StatusBadRequest, msg)
		}
		return 200, "", ProbeOK
	}
	msg := compactProviderMessage(body)
	kind := classifyProbeStatus(resp.StatusCode, msg)
	return resp.StatusCode, msg, kind
}

// readLimited 读至多 limit 字节，返回 (内容, 是否被截断)。
func readLimited(r io.Reader, limit int64) ([]byte, bool) {
	b, _ := io.ReadAll(io.LimitReader(r, limit))
	return b, int64(len(b)) == limit
}

// llmBodyShape 判定一次 2xx 响应体是否真的是「LLM 接口响应」。
// 返回 (是否可用, 是否是结构化 JSON, 不通过的原因)。
//
// 通过的三条形态（覆盖 OpenAI 兼容网关的实际回法）：
//  1. JSON 对象且 choices 非空、无顶层 error —— 标准 chat completion；
//  2. SSE 事件流里带 choices —— 少数网关忽略 stream:false，收到 SSE 也算通；
//  3. 其他一律不通过。
//
// 这里有意**不做**严格 schema 校验：目的只是区分"这是一份模型响应"与"这是一页 HTML"，
// 验得太细会把自建网关的合法变体误杀（与"拒绝必须有确凿证据"同源）。
//
// truncated=true（响应体超过判定上限）：JSON 解析失败时不否定——截断是**我们**造成的，
// 不能拿它当"地址不对"的证据。HTML 判定不受截断影响（只看开头），仍然生效。
func llmBodyShape(body []byte, truncated bool) (ok bool, isJSON bool, why string) {
	t := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})) // 去 BOM
	if len(t) == 0 {
		return false, false, "响应体为空"
	}
	if looksLikeHTMLPage(t) {
		return false, false, "响应体是 HTML 网页（不是 API 响应）"
	}
	if t[0] == '{' || t[0] == '[' {
		var probe struct {
			Choices []json.RawMessage `json:"choices"`
			Error   json.RawMessage   `json:"error"`
		}
		if err := json.Unmarshal(t, &probe); err != nil {
			if truncated {
				return true, true, ""
			}
			return false, false, "响应体不是合法 JSON: " + err.Error()
		}
		switch {
		case len(probe.Error) > 0:
			return false, true, "响应体带 error 字段（状态码却是 2xx）"
		case len(probe.Choices) == 0:
			return false, true, "JSON 里没有 choices 字段"
		}
		return true, true, ""
	}
	if bytes.HasPrefix(t, []byte("data:")) {
		if bytes.Contains(t, []byte(`"choices"`)) {
			return true, false, ""
		}
		return false, false, "SSE 事件流里没有 choices"
	}
	return false, false, "响应体既不是 JSON 也不是 SSE"
}

// looksLikeHTMLPage 响应开头是否是网页（HTML）。运行时的 diagnose 与探测共用这一份判定，
// 避免两处各写一套前缀列表（判据分叉 = 一处认出来、另一处认不出来）。
func looksLikeHTMLPage(t []byte) bool {
	s := bytes.ToLower(bytes.TrimSpace(t))
	for _, p := range [][]byte{
		[]byte("<!doctype"), []byte("<html"), []byte("<head"), []byte("<body"),
		[]byte("<!--"), []byte("<?xml"),
	} {
		if bytes.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// notEndpointDetail 拼装「地址不是 API 端点」的可读原因：含状态码、Content-Type、
// **重定向落点**（这是识破"控制台域名 → 登录页"的关键证据）与响应摘录，最后给一句怎么改。
// 不含任何密钥材料。
func notEndpointDetail(configuredURL string, resp *http.Response, body []byte, why string) string {
	var b strings.Builder
	b.WriteString(why)
	fmt.Fprintf(&b, "；HTTP %d", resp.StatusCode)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		b.WriteString(", Content-Type=" + ct)
	}
	if fin := finalURL(resp); fin != "" && !sameHost(fin, configuredURL) {
		b.WriteString("；请求被跨域重定向到 " + fin)
	}
	if excerpt := htmlOrCompactExcerpt(body); excerpt != "" {
		b.WriteString("；响应摘录: " + excerpt)
	}
	b.WriteString("；提示：网页控制台/登录页地址不是 API 地址，API 端点通常形如 " +
		"https://api.<供应商域名>/v1/chat/completions")
	return b.String()
}

// finalURL 客户端跟随重定向后真正落到的地址（去掉 query/fragment/userinfo，避免把参数里的
// 一次性令牌带进日志与错误文案）；拿不到则返回空串。
func finalURL(resp *http.Response) string {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	u := *resp.Request.URL
	u.RawQuery, u.Fragment, u.RawFragment, u.User = "", "", "", nil
	return u.String()
}

// sameHost 两个 URL 是否同一个主机（host:port）。无法解析时按"视为相同"处理——
// 宁可少报一句重定向，也不要把同主机上的正常跳转报成"被劫持到别处"。
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return true
	}
	return strings.EqualFold(ua.Host, ub.Host)
}

// htmlOrCompactExcerpt 响应体 → 单行短摘录：HTML 只报标题（整页 HTML 灌进错误文案没有
// 任何信息量，还会把 200 字符的前端截断额度吃光）；其余沿用 compactProviderMessage。
func htmlOrCompactExcerpt(body []byte) string {
	t := bytes.TrimSpace(body)
	if int64(len(t)) > probeBodyLimit {
		t = t[:probeBodyLimit]
	}
	if looksLikeHTMLPage(t) {
		if title := htmlTitle(t); title != "" {
			return "HTML 网页（<title>" + title + "</title>）"
		}
		return "HTML 网页"
	}
	return compactProviderMessage(body)
}

// htmlTitle 从 HTML 里抠出 <title>…</title>（截断 120 字符）；没有则返回空串。
func htmlTitle(body []byte) string {
	lower := bytes.ToLower(body)
	i := bytes.Index(lower, []byte("<title"))
	if i < 0 {
		return ""
	}
	j := bytes.IndexByte(lower[i:], '>')
	if j < 0 {
		return ""
	}
	rest := body[i+j+1:]
	k := bytes.Index(bytes.ToLower(rest), []byte("</title"))
	if k < 0 {
		return ""
	}
	t := strings.Join(strings.Fields(string(rest[:k])), " ")
	if len(t) > 120 {
		t = t[:120] + "…"
	}
	return t
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

// RuntimeKeys 运行时轮询池（同时也是落库集合）的密钥：**只剔除"确凿不可用"的**。
//
// 与 UsableKeys 的分工（§2026-09-20）：
//   - UsableKeys 回答"这次探测谁明确通过了"——用于判断 verified，不能拿它决定删 key；
//   - RuntimeKeys 回答"谁有资格留在池子里"——可用 / 限流 / **未能判定**（network、5xx、
//     请求被拒）都留，只有 auth、model、quota、not_endpoint 这四类确凿证据才剔。
//
// 为什么必须分开：广州实测一轮 8 key 探测里有 4 把报"连接超时"（30s 探测上限 + 2 核小机器
// 并发 4 路推理模型），而同一把 key 单独直连是 200。若按 UsableKeys 落库，用户点一次保存
// 就会**永久删掉 4 把好 key**（库里与页面列表里都没有了），证据却只是"我们自己的超时"。
func RuntimeKeys(keys []string, probes []KeyProbe) []string {
	out := make([]string, 0, len(keys))
	for i, p := range probes {
		if i < len(keys) && !p.ConfigInvalid() {
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
