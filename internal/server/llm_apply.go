package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/llm"
)

// ── LLM 配置热更新：单一实现 + 前置校验 + 可回滚 ──────────────────────────────
//
// 这个文件存在的理由（2026-09-18）：
//
// LLM 配置有**三个写入入口**（设置页 / 管理端 / 咨询页），而"配置写进磁盘"与"运行中的客户端
// 真的能用"之间**没有任何反馈回路**。客户端实例只活在进程内存里，于是盘中换 key 这件事：
//
//  1. 一旦写进一把坏的（过期 / 欠费 / 模型名错 / 地址形态错），客户端被就地换坏 → 新闻归因、
//     D1 评分、咨询**全部当场哑掉**；
//  2. 更糟的是坏配置**已经落库** → 重启也救不回来，必须人工再改回来；
//  3. 而用户之所以要热更新，往往正是因为 LLM 已经出问题了 —— 也就是说**救命的动作本身可能
//     把系统锁死**，这是最不能接受的一点。
//
// 所以热更新的正确语义不是"保存即生效"，而是：
//
//	**先证明候选配置真的能调通 → 再切换运行时 → 最后才落库。**
//
// 三条不变量（改动本文件前请先读）：
//
//	① **拒绝必须有确凿证据**：只有探测明确指向"配置本身有错"（密钥无效 / 模型或地址不存在 /
//	   余额不足 / **地址不是 API 端点**）才拒绝；网络不可达、供应商 5xx、请求体被拒都**不能**
//	   当成配置错的证据，否则供应商抖动时用户会被"保存都不让"堵死在坏状态里。此时给出原文 +
//	   提供 force 显式覆盖。（§2026-09-20 新增第四类 not_endpoint，依据见
//	   docs/BUGFIX_LLM_CONSOLE_URL_20260920.md：它是"地址配错"的直接证据，比 401/404 更硬。）
//	② **落库晚于切换**：任何情况下都不允许出现"库里是坏的、运行时也是坏的"这种重启救不回的
//	   状态。落库失败只影响重启后的自愈，不回滚运行时（盘中先保证能用）。
//	③ **三个入口共用这一份实现**：口径分叉正是历次"改了不生效"的根源
//	   （同一件事两处各写一遍 → 迟早有一处漏）。
//
// English: single implementation for every LLM-config write path — probe the candidate config with a
// real call, swap the runtime client only when it works, and persist last. Refusing a hot update
// requires positive evidence of a bad config; when we merely cannot verify, we say so and let the
// operator force it.

// llmSnapshot 一份 LLM 运行时配置快照（含明文密钥，仅存内存，不落任何日志/响应）。
type llmSnapshot struct {
	Keys             []string
	APIURL           string
	Model            string
	TimeoutSec       int
	Stream           *bool
	BatchConcurrency int
	ClassifierModel  string
	D1MaxTokens      int
	At               time.Time
	// Verified 该快照是否经过真实探测确认可用（false = 只是"采用了"，未验证）。
	Verified bool
}

// streamingOn 流式开关的生效值（nil = 默认开启，与 config.LLMConfig 的语义一致）。
func (s llmSnapshot) streamingOn() bool { return streamingEnabled(s.Stream) }

// clone 值拷贝一份（切片要复制，避免与调用方的切片共享底层数组）。
func (s llmSnapshot) clone() llmSnapshot {
	c := s
	c.Keys = append([]string(nil), s.Keys...)
	return c
}

// llmApplyResult 热更新结果的完整回告：前端据此显示"到底生效了没有、哪把 key 为什么没生效"。
// 此前接口只返回 {"status":"ok"}，而 UI 无论真假都弹"已保存并热生效"——用户没有任何手段确认。
type llmApplyResult struct {
	// Applied 运行时客户端是否已被替换（这是用户真正关心的那一项）。
	Applied bool `json:"applied"`
	// Persisted 是否已落库（决定重启后能否自愈）。
	Persisted bool `json:"persisted"`
	// PersistError 落库失败原因（Applied=true 而 Persisted=false 时必须告警：重启会回退）。
	PersistError string `json:"persist_error,omitempty"`
	// Rejected 是否因探测未通过而拒绝（运行时与磁盘都保持原样）。
	Rejected bool `json:"rejected,omitempty"`
	// Reason 拒绝原因（人类可读，含逐把密钥的结论）。
	Reason string `json:"reason,omitempty"`
	// Warning 已生效但有保留意见（部分 key 被剔除 / 未经证实 / 落库失败）。
	Warning string `json:"warning,omitempty"`
	// Verified 生效的配置是否经过真实调用确认。
	Verified bool `json:"verified"`
	// Probes 逐把密钥的探测结论（位次即设置页输入框行号）。
	Probes []llm.KeyProbe `json:"probes,omitempty"`
	// EffectiveKeys 本次实际进入运行时轮询池（且落库）的密钥数。
	EffectiveKeys int `json:"effective_keys"`
	// DroppedKeys 因**确凿不可用**而被剔除的密钥数（未能判定的不算，见 RuntimeKeys）。
	DroppedKeys int `json:"dropped_keys"`
	// ProbeMS 探测总耗时。
	ProbeMS int64 `json:"probe_ms"`
	// APIURL / Model 本次实际生效的地址与模型。
	APIURL string `json:"api_url"`
	Model  string `json:"model"`
}

// ── 快照存取 ──────────────────────────────────────────────────────────────────

// rememberLLMSnapshot 记录一份运行时快照。
//
// 维护两份：lastApplied（最后一次生效的，用于展示实际状态）与 lastGood（最后一次**经验证**
// 可用的，用于一键回滚）。二者必须分开——正因为存在"用 force 强行应用了一个没验证过的配置"
// 这条路径，才会需要"回到上一个确实能用的时候"这个动作。
func (s *Server) rememberLLMSnapshot(snap llmSnapshot, verified bool) {
	snap = snap.clone()
	snap.Verified = verified
	if snap.At.IsZero() {
		snap.At = time.Now()
	}
	s.llmMu.Lock()
	s.llmLastApplied = &snap
	if verified {
		good := snap.clone()
		s.llmLastGood = &good
	}
	s.llmMu.Unlock()
}

// SeedLLMSnapshot 启动时把进程真正加载的那份配置记入快照，作为初始的"上一个可用配置"。
//
// 不这么做的话，进程刚起来时回滚点是空的——而"刚重启完、还没保存过任何配置"恰恰是
// 用户最需要回滚的时候（启动预检失败 → 他想回到上一次能用的那份）。
func (s *Server) SeedLLMSnapshot(keys []string, apiURL, model string, timeoutSec int, streaming bool, batchConcurrency int, classifierModel string) {
	st := streaming
	s.rememberLLMSnapshot(llmSnapshot{
		Keys:             keys,
		APIURL:           apiURL,
		Model:            model,
		TimeoutSec:       timeoutSec,
		Stream:           &st,
		BatchConcurrency: batchConcurrency,
		ClassifierModel:  classifierModel,
	}, false) // 启动快照未验证：它只是"进程当前加载的"，不等于"确认可用"
}

// lastGoodLLM 取回滚目标：优先"上一个已验证可用的"，退化为"最后一次生效的"。
func (s *Server) lastGoodLLM() (llmSnapshot, bool) {
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	if s.llmLastGood != nil {
		return s.llmLastGood.clone(), true
	}
	if s.llmLastApplied != nil {
		return s.llmLastApplied.clone(), true
	}
	return llmSnapshot{}, false
}

// ── 候选配置构造（三入口共用）──────────────────────────────────────────────────

// prepareLLMCandidate 把一次提交解析成候选运行时配置。**无任何副作用**（不落库、不切换），
// 因此可以被探测端点复用：用户点"测试连接"时走的就是这一份解析逻辑，
// 保证"测的"与"存的"绝对是同一个东西。
//
// 三条解析口径（每条都对应一次真实事故，改动前先读）：
//
//  1. **留空 = 保持原值**（api_url / model / timeout / stream / 数值项）。旧实现把空串直接写进
//     配置，等于**清掉**已存的地址与模型——咨询页只改 Key 时（它不发送 model/api_url）必然发生，
//     客户端随后回落内置默认供应商，配着别家的 Key 就是一片 401，表现为"LLM 挂了"。
//  2. **脱敏哨兵 → 库中原值**（密钥）。设置页 GET 回显的是 `sk-…1234`，用户只改别的字段再保存，
//     提交回来的就是哨兵串。把掩码当成真钥去建客户端 = 全部 401，而库里那把好钥还在、
//     页面回读还是老样子，用户看到的就是"改了没生效、改不了"。
//  3. **数值项的 0 = 保持原值**：TimeoutSec/BatchConcurrency/D1MaxTokens 都是"<=0 走下游默认"
//     语义，若照抄 0 落库，会把用户显式设过的 240s 超时静默清成 0（与第 1 条同型的另一扇门）。
func (s *Server) prepareLLMCandidate(uid string, req setLLMConfigReq) (llmSnapshot, int, string) {
	// §别名警告：GetLLMConfigFor 返回内部结构指针，必须立刻取值拷贝（见其注释）。
	prev := *s.cfg.GetLLMConfigFor(uid)

	// 地址与模型「留空即保持原值」：这里先合并，再交给下面的 SSRF 校验，
	// 避免咨询页只提交密钥时把已存的供应商地址冲成空串。
	apiURL := strings.TrimSpace(req.APIURL)
	if apiURL == "" {
		apiURL = prev.APIURL
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = prev.Model
	}
	// §GAP2-W2 出呼地址校验（SSRF 面收口）：非法直接拒绝，不落任何配置。
	if apiURL != "" {
		if err := validatePublicURL(apiURL); err != nil {
			return llmSnapshot{}, http.StatusBadRequest, "api_url " + err.Error()
		}
	}

	keys := s.resolveCandidateKeys(uid, req)
	if len(keys) == 0 {
		return llmSnapshot{}, http.StatusBadRequest,
			"未提供任何可用密钥：提交为空或全部是脱敏占位值且库中没有原值可映射（保持现有客户端不变）"
	}

	// 未提交的字段一律沿用上一份配置：Stream 为指针（nil=未提交），三个数值项用 <=0 表示未提交
	// （它们的结构体注释本就写明"<=0 走下游默认"）。照抄 0 落库会把用户显式设过的
	// 240s 超时静默清成 0——与"空 api_url 清空供应商"同型的另一扇门。
	stream := req.Stream
	if stream == nil {
		stream = prev.Stream
	}
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = prev.TimeoutSec
	}
	batchConcurrency := req.BatchConcurrency
	if batchConcurrency <= 0 {
		batchConcurrency = prev.BatchConcurrency
	}
	d1MaxTokens := req.D1MaxTokens
	if d1MaxTokens <= 0 {
		d1MaxTokens = prev.D1MaxTokens
	}
	classifierModel := strings.TrimSpace(req.ClassifierModel)
	if classifierModel == "" {
		classifierModel = prev.ClassifierModel
	}

	// 合并完成的候选快照：status/code 均为零值表示校验通过，由调用方决定落库并热重建 LLM 客户端。
	return llmSnapshot{
		Keys:             keys,
		APIURL:           apiURL,
		Model:            model,
		TimeoutSec:       timeoutSec,
		Stream:           stream,
		BatchConcurrency: batchConcurrency,
		ClassifierModel:  classifierModel,
		D1MaxTokens:      d1MaxTokens,
	}, 0, ""
}

// resolveCandidateKeys 解析出候选密钥列表 —— 落库、热切换、探测**共用这一份结果**。
//
// 旧实现里"落库侧"与"热重建侧"各算各的，正是"库是对的、运行中的客户端被掩码打坏"这个
// 事故的缝。任何新增的密钥来源都必须并进这里，不要再在别处解析一遍。
func (s *Server) resolveCandidateKeys(uid string, req setLLMConfigReq) []string {
	// ① 多 key 槽位（含哨兵回填）
	resolved, _ := s.resolveSubmittedLLMKeys(uid, req.APIKeys)
	if len(resolved) > 0 {
		return resolved
	}
	// ② 旧版单 key 字段（明文才采信；哨兵由 ① 的库中回填覆盖）
	if k := strings.TrimSpace(req.APIKey); k != "" && !isMaskedSecret(k) {
		return []string{k}
	}
	// ③ 库中兜底：本次没提交密钥（只改地址/模型）时沿用已存密钥
	if v, ok := s.auth.GetConfig(uid, "llm_api_keys"); ok && v != "" {
		if ks := splitLLMKeys(v); len(ks) > 0 {
			return ks
		}
	}
	if v, ok := s.auth.GetConfig(uid, "llm_api_key"); ok && v != "" {
		return []string{v}
	}
	return nil
}

// llmProber 探测实现的注入点：默认就是真实的最小调用探测（llm.ProbeConfig）。
//
// 为什么留这个点：探测会真的出网。单元测试若也出网，就会变慢、变不确定，还会在无网环境下
// 假失败（既有用例把 api_url 写成 example.com 就会变成真实请求）。因此：
//   - 探测**分类逻辑**的正确性由 internal/llm/probe_test.go 用 httptest 真实验证；
//   - 本包的用例改注入确定性结论，专注验证"结论 → 是否切换/落库"的决策链。
//
// 生产路径不做任何替换（注入点只在测试里被赋值）。
// English: seam for the prober so this package's tests stay hermetic; the real prober's
// classification is covered by internal/llm/probe_test.go against httptest servers.
var llmProber = llm.ProbeConfig

// ── 应用（探测 → 切换 → 落库）────────────────────────────────────────────────

// applyLLMSnapshot 执行一次热更新：探测候选 → 决定是否采用 → 切换运行时 → 落库。
//
// 返回 (结果, HTTP 状态码)。状态码 200 = 已生效；409 = 探测未通过且未加 force（什么都没动）。
//
// hotSwap=false 时只做探测（供"测试连接"复用同一条判定逻辑）。
// English: probes, decides, swaps and persists; 409 means the candidate was rejected and nothing
// was touched (neither the runtime client nor the disk).
func (s *Server) applyLLMSnapshot(uid string, cand llmSnapshot, force, persist, hotSwap bool) (llmApplyResult, int) {
	res := llmApplyResult{APIURL: cand.APIURL, Model: cand.Model}

	// 串行化整个"探测 + 切换 + 落库"：盘中手忙脚乱时连点保存、或保存与回滚并发，
	// 两个流程交错会让"谁最后生效"变得不可预测——而这里预测错就是线上客户端被换错。
	s.llmApplyMu.Lock()
	defer s.llmApplyMu.Unlock()

	if len(cand.Keys) == 0 {
		res.Reason = "未提供任何可用密钥（保持现有客户端不变）"
		res.Rejected = true
		return res, http.StatusBadRequest
	}

	timeout := llm.ProbeTimeoutFor(cand.TimeoutSec)
	// 总预算：单把 timeout × 波数，夹到 ProbeMaxTotalBudget。透出到日志是为了让"这次到底
	// 是单把超时还是总预算到点"能一眼看出来 —— 只报 probe=NNNNms 时两者长得一样。
	budget := llm.ProbeTotalBudget(timeout, len(cand.Keys))
	started := time.Now()
	probes := llmProber(llm.Config{
		APIKeys: cand.Keys,
		APIURL:  cand.APIURL,
		Model:   cand.Model,
		Timeout: timeout,
	}, timeout)
	res.ProbeMS = time.Since(started).Milliseconds()
	res.Probes = probes

	usable := llm.UsableKeys(cand.Keys, probes)
	verified := len(usable) > 0

	switch {
	case verified:
		// 正常路径：至少一把 key 探测通过。进池的集合用 RuntimeKeys（可用 + **未能判定**），
		// 只剔"确凿不可用"的 —— 详见下面对 droppedKeysWarning 的说明与 probe.go::RuntimeKeys。
		//
		// 告警条件用「有 key 没干净通过」而非「有 key 被剔除」：既剔也留，用户都得被告知
		// 那把"网络不可达"的 key 后来怎么了（留着 / 删了），否则探测报告里的红字无从解释。
		if len(usable) < len(cand.Keys) {
			res.Warning = droppedKeysWarning(cand.Keys, probes)
		}
	case llm.AllConfigInvalid(probes) && !force:
		// 拒绝路径：确凿证据表明配置本身有错。若此时采用，会把一个已经能用的运行时
		// 换成一个肯定不能用的，并且把它写进磁盘（重启也救不回）。
		res.Rejected = true
		res.Reason = rejectReason(probes)
		log.Printf("[llm] 热更新被拒绝（探测确认配置不可用, uid=%s url=%s model=%s）: %s",
			uid, cand.APIURL, cand.Model, llm.ProbeSummary(probes))
		return res, http.StatusConflict
	default:
		// 不可判定（网络不可达 / 供应商 5xx / 请求体被拒）或用户显式 force。
		// 这类情况**没有证据说配置是错的**，拒绝会把"运行时已经坏了、想靠换配置救"的用户
		// 堵死在坏状态里，所以采用但如实回告"未经验证"，并留下回滚点。
		res.Warning = unverifiedWarning(probes, force)
	}

	// 进池/落库集合：可用 ∪ 未能判定，只剔"确凿不可用"。
	//
	// §2026-09-20 为什么要这样收口（广州实测）：8 把 key 一轮探测里 4 把报"连接超时"
	// （30s 探测上限 + 2 核小机器并发 4 路推理模型），而同一把 key 单独直连是 200 ——
	// 这些失败**完全是我们自己的探测条件造成的**。若据此把 key 从池里/库里删掉，
	// 用户点一次保存就永久丢掉几把好 key（库里和页面上都不见了），证据却只是"我们超时了"。
	// 这就是本文件不变量①在 key 集合上的延伸：剔除也必须凭确凿证据。
	// 仍然剔除的是 auth/model/quota/not_endpoint 四类（欠费的 key 混在池里 = 每 N 个请求
	// 必然失败一个，那才是要防的）。
	keep := llm.RuntimeKeys(cand.Keys, probes)
	if len(keep) == 0 && len(cand.Keys) > 0 {
		keep = append([]string(nil), cand.Keys...) // 极端兜底：绝不建一个空池
	}
	submitted := len(cand.Keys)
	res.EffectiveKeys = len(keep)
	res.DroppedKeys = submitted - len(keep)
	res.Verified = verified
	cand.Keys = keep

	// ① 切换运行时（先于落库：盘中优先保证"现在能用"；落库只影响重启后能否自愈）。
	// 仅探测（"测试连接"）路径不切换、不更新快照——探测是只读动作，绝不能改变运行态。
	if hotSwap {
		res.Applied = true
		if s.llmRecreate != nil {
			// 下发的必须是 cand.Keys（= keep）：用 usable 会漏掉"未能判定但保留"的 key，
			// 于是"库里有 8 把、运行时只有 4 把"——落库与运行时分叉正是本文件的头号事故形态。
			s.llmRecreate(cand.Keys, cand.APIURL, cand.Model, cand.TimeoutSec, cand.streamingOn(), cand.BatchConcurrency, cand.ClassifierModel)
		}
		s.rememberLLMSnapshot(cand, verified)
		s.SetRuntimeLLM(cand.APIURL, effectiveModelName(cand.Model))
	}

	// ② 落库（决定重启后能否自愈）
	if persist {
		if err := s.persistLLMSnapshot(uid, cand); err != nil {
			res.PersistError = err.Error()
			res.Warning = joinWarning(res.Warning,
				"运行时已生效，但**落库失败**（"+err.Error()+"）：本次重启后会回退到上一次保存的配置")
			log.Printf("[llm] 热更新落库失败（运行时已生效）: uid=%s err=%v", uid, err)
		} else {
			res.Persisted = true
		}
	}

	log.Printf("[llm] 热更新%s: uid=%s keys=%d/%d (探测通过 %d) verified=%v url=%s model=%s probe=%dms timeout=%s budget=%s %s",
		map[bool]string{true: "已生效", false: "仅探测"}[hotSwap],
		uid, len(cand.Keys), submitted, len(usable), verified, cand.APIURL, cand.Model, res.ProbeMS,
		timeout, budget, llm.ProbeSummary(probes))
	return res, http.StatusOK
}

// persistLLMSnapshot 把已生效的快照落库（账号 LLM 配置 + 密钥）。
//
// 落库内容**只包含已生效的密钥**：库与运行时保持同一份事实，重启后不会把一把已知失败的 key
// 又装回轮询池。被剔除的 key 通过响应逐把回告（用户能看到原因并修正后重存），
// 而不是静默留在库里。
//
// 数值/开关字段从上一份配置**拷贝后再覆盖**（而不是整struct重写）：LLMConfig 里还有
// MaxRetryTimes 等本接口不管理的字段，整struct重写会把它们静默清零。
func (s *Server) persistLLMSnapshot(uid string, cand llmSnapshot) error {
	prev := *s.cfg.GetLLMConfigFor(uid) // 取值拷贝（见 GetLLMConfigFor 的别名警告）
	next := prev
	next.APIURL = cand.APIURL
	next.Model = cand.Model
	next.TimeoutSec = cand.TimeoutSec
	next.Stream = cand.Stream
	next.BatchConcurrency = cand.BatchConcurrency
	next.ClassifierModel = cand.ClassifierModel
	next.D1MaxTokens = cand.D1MaxTokens
	s.cfg.SetLLMConfigFor(uid, &next)

	if len(cand.Keys) == 0 {
		return nil
	}
	// 多 key 池（新形态）
	if err := s.auth.SetConfig(uid, "llm_api_keys", strings.Join(cand.Keys, ",")); err != nil {
		return err
	}
	// 旧版单 key 字段同步为首把：两个读取口径（llmcfg.Resolve 的 plural→singular 回退链）
	// 必须同值，否则库里会留着一把早已轮换掉的旧 key，成为"看不见的第三把"。
	return s.auth.SetConfig(uid, "llm_api_key", cand.Keys[0])
}

// ── 结果文案 ──────────────────────────────────────────────────────────────────

// rejectReason 拒绝原因（人类可读，逐把列出）。这段文字会原样显示给用户，
// 因此必须包含"哪把 key、什么原因、供应商原文"，否则用户只能猜。
func rejectReason(probes []llm.KeyProbe) string {
	var b strings.Builder
	b.WriteString("配置未生效：新配置探测未通过，已保留当前可用配置（未切换、未落库）。")
	for _, p := range probes {
		b.WriteString("\n· ")
		b.WriteString(p.Summary())
	}
	b.WriteString("\n请修正后重存；确认要强制应用请勾选「强制应用」。")
	return b.String()
}

// droppedKeysWarning 密钥集合被收窄的告警（§2026-09-20 改：分「剔除」与「暂留」两类）。
//
// 为什么必须分开说：进池/落库用的是 RuntimeKeys —— 只有"确凿不可用"（密钥无效 / 模型或地址
// 不存在 / 欠费 / 地址不是 API 端点）才会被剔；"未能判定"（网络超时 / 供应商 5xx / 我们自己的
// 请求体被拒）**保留**。两类都不说明白，用户就会以为"探测报红的 key 被删了"，而实际是留着的；
// 反过来只报"剔除"，会让"我们超时了"被误读成"key 坏了"。
func droppedKeysWarning(keys []string, probes []llm.KeyProbe) string {
	dropped := make([]string, 0, len(probes))
	keptUnsure := make([]string, 0, len(probes))
	for i, p := range probes {
		if i >= len(keys) {
			continue
		}
		if p.ConfigInvalid() {
			dropped = append(dropped, p.Summary())
		} else if !p.Usable() {
			keptUnsure = append(keptUnsure, p.Summary())
		}
	}
	var b strings.Builder
	if len(dropped) > 0 {
		b.WriteString("已剔除 " + strconv.Itoa(len(dropped)) +
			" 把确认不可用的密钥（未纳入轮询池、未落库）：\n· " + strings.Join(dropped, "\n· "))
	}
	if len(keptUnsure) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("另有 " + strconv.Itoa(len(keptUnsure)) +
			" 把本轮**未能判定**（网络/供应商侧原因，非配置错），已保留在轮询池与配置中：\n· " +
			strings.Join(keptUnsure, "\n· "))
	}
	return b.String()
}

// unverifiedWarning 未经证实即采用的告警。
func unverifiedWarning(probes []llm.KeyProbe, forced bool) string {
	head := "配置已采用，但**未能验证**（探测没有拿到可用响应，无法判定配置对错）："
	if forced {
		head = "已按「强制应用」采用（跳过探测结论）："
	}
	var b strings.Builder
	b.WriteString(head)
	for _, p := range probes {
		b.WriteString("\n· ")
		b.WriteString(p.Summary())
	}
	b.WriteString("\n若随后仍不可用，可用「回滚到上一个可用配置」一键恢复。")
	return b.String()
}

// joinWarning 拼接告警（避免后一条覆盖前一条：例如"剔除了 key"与"落库失败"要同时可见）。
func joinWarning(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "\n" + add
}

// effectiveModelName 生效模型名（空值回落到包级默认，与客户端内部一致）。
func effectiveModelName(model string) string {
	if model == "" {
		return llm.DefaultModel
	}
	return model
}

// ── 逐入口的请求体 → 候选 → 应用 ──────────────────────────────────────────────

// runSetLLMConfig 设置页写入路径：解析 → 探测 → 切换 → 落库。
// 返回 (结果, HTTP 状态码, 前置错误文案)。前置错误（400）非空时结果无意义。
func (s *Server) runSetLLMConfig(uid string, req setLLMConfigReq) (llmApplyResult, int, string) {
	return s.runLLMApplyFor(uid, req, true)
}

// runLLMApplyFor 写入路径的统一实现：设置页与管理端共用。
//
// 唯一差别是 hot：被改的账号配置若**不归属**本机运营账号（多租户下改的是别家运营者的配置），
// 则只落库、不碰本进程的全局运行时客户端——但仍走**同一份解析口径**。这一点很关键：
// 第二个入口若自己解析一遍，"掩码当真钥 / 空值清空"这两个缺陷会原样复活
// （2026-09-18 管理端确实还带着这两个毛病，本次一并收口）。
// English: the single write path. `hot=false` only skips the swap (foreign tenant config), never
// the shared parsing rules.
func (s *Server) runLLMApplyFor(uid string, req setLLMConfigReq, hot bool) (llmApplyResult, int, string) {
	cand, status, msg := s.prepareLLMCandidate(uid, req)
	if status != 0 {
		return llmApplyResult{}, status, msg
	}
	if !hot {
		res := llmApplyResult{APIURL: cand.APIURL, Model: cand.Model, EffectiveKeys: len(cand.Keys)}
		if err := s.persistLLMSnapshot(uid, cand); err != nil {
			return res, http.StatusInternalServerError, "落库失败: " + err.Error()
		}
		res.Persisted = true
		return res, http.StatusOK, ""
	}
	res, code := s.applyLLMSnapshot(uid, cand, req.Force, true, true)
	return res, code, ""
}

// writeLLMApplyResult 输出热更新结果。失败时 error 字段填人类可读原因（前端统一按 e.message 显示），
// 同时带上结构化 result 供界面做更细的展示（逐把 key 结论）。
func writeLLMApplyResult(w http.ResponseWriter, status int, res llmApplyResult, errMsg string) {
	body := map[string]interface{}{"result": res}
	if errMsg != "" {
		body["error"] = errMsg
		writeJSON(w, status, body)
		return
	}
	body["status"] = "ok"
	writeJSON(w, status, body)
}

// handleProbeLLMConfig 处理 POST /api/config/llm/probe：只探测、不改任何状态。
//
// 盘中排查的第一动作就是"现在到底能不能用"。此前唯一手段是读服务端日志的启动预检，
// 或者重启进程——这两条在盘中都太慢。请求体可带候选配置（与 POST /api/config/llm 同形，
// 支持脱敏哨兵）；不带则探测**当前已生效**的运行时配置。
func (s *Server) handleProbeLLMConfig(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	var req setLLMConfigReq
	if r.Body != nil {
		// 允许空体：探测当前生效配置。
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	probeOnly := req.APIURL == "" && req.Model == "" && len(req.APIKeys) == 0 && req.APIKey == ""
	if probeOnly {
		snap, ok := s.currentLLMSnapshot(uid)
		if !ok {
			writeError(w, 404, "当前没有可探测的 LLM 配置")
			return
		}
		res, _ := s.applyLLMSnapshot(uid, snap, true /*force：只探测，不切换*/, false, false)
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "result": res})
		return
	}
	cand, status, msg := s.prepareLLMCandidate(uid, req)
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	res, _ := s.applyLLMSnapshot(uid, cand, true, false, false)
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "result": res})
}

// currentLLMSnapshot 取当前已生效的运行时配置；没有则从库中现读一份。
func (s *Server) currentLLMSnapshot(uid string) (llmSnapshot, bool) {
	s.llmMu.Lock()
	applied := s.llmLastApplied
	s.llmMu.Unlock()
	if applied != nil {
		return applied.clone(), true
	}
	prev := *s.cfg.GetLLMConfigFor(uid)
	keys := s.resolveCandidateKeys(uid, setLLMConfigReq{})
	if len(keys) == 0 && prev.APIURL == "" {
		return llmSnapshot{}, false
	}
	return llmSnapshot{
		Keys:             keys,
		APIURL:           prev.APIURL,
		Model:            prev.Model,
		TimeoutSec:       prev.TimeoutSec,
		Stream:           prev.Stream,
		BatchConcurrency: prev.BatchConcurrency,
		ClassifierModel:  prev.ClassifierModel,
		D1MaxTokens:      prev.D1MaxTokens,
	}, true
}

// handleRollbackLLMConfig 处理 POST /api/config/llm/rollback：回到上一个**已验证可用**的配置。
//
// 这是"热更新翻车"的兜底动作：一旦用力过猛（例如 force 应用了一个其实不能用的配置），
// 不必重启、不必回忆上次填了什么，一次调用回到已知可用的那一份。
// 回滚本身**不做拒绝判定**（force=true）：它的语义就是"回到那个当时确实能用的状态"，
// 再拦一道就失去兜底意义了；探测结果仍然照常回告，让用户看清回滚后的真实状况。
func (s *Server) handleRollbackLLMConfig(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	snap, ok := s.lastGoodLLM()
	if !ok {
		writeError(w, 404, "没有可回滚的配置（本进程还没有成功应用过任何 LLM 配置）")
		return
	}
	res, code := s.applyLLMSnapshot(uid, snap, true, true, true)
	log.Printf("[llm] 回滚到上一个可用配置: uid=%s at=%s url=%s keys=%d",
		uid, snap.At.Format(time.RFC3339), snap.APIURL, len(snap.Keys))
	writeLLMApplyResult(w, code, res, "")
}

// llmApplyMu / llmLastApplied / llmLastGood 三个字段的声明见 server.go 的 Server 结构体
// （与其余 LLM 运行态字段放在一起，避免同一主题的字段散落两处）。
