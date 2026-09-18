// Package llmcfg LLM 启动配置的单一权威解析器（§UI-AUTHORITATIVE 统一接入运营配置源）。
// 引擎（cmd/quant）、回测 CLI（cmd/backtest）、链路延迟工具（cmd/news_signal_latency）
// 全部经 Resolve 取配置——优先级与线上一致：
// ① 运营账号设置页保存（userRules + 按账号 auth 密钥）
// ② 环境变量（部署 bootstrap，仅补 UI 未保存的字段）
// ③ 全局 auth 配置项（历史 "" 键）→ ④ 全局 config.json rules.llm → ⑤ 代码默认。
// 修复前 CLI 各自裸读 LLM_* 环境变量：设置页换 key/地址后，夜间回测等子进程仍走旧供应商，
// 与引擎行为分叉（2026-09-14 kiraai 切换时暴露）。
// English: the single source-of-truth resolver for LLM startup config, shared by the engine
// and the CLI tools so a settings-page change takes effect everywhere (env is bootstrap-only).
package llmcfg

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/llm"
)

// DefaultDataDir 运营数据目录（显式 > QUANT_DATA_DIR > ~/.quant-trading-v2），
// CLI 未传 -data 时据此定位运营配置，与引擎 getDataDir 同口径。
// English: the operator data directory used by the engine; CLIs fall back to this when unset.
func DefaultDataDir() string {
	if v := os.Getenv("QUANT_DATA_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2")
}

// Resolve 按上述五级优先级解析 LLM 配置。入参配置/认证管理器可来自任意数据目录，
// 内部会确保 store/operator 挂接幂等完成（CLI 往往只建了 config.Manager）。
// English: resolves the effective LLM config; idempotently wires the store/operator so
// even bare CLIs get the operator (UI-saved) values.
func Resolve(cfgMgr *config.Manager, authMgr *auth.Manager) llm.Config {
	adminID := ""
	if authMgr != nil {
		adminID = authMgr.AdminID()
		cfgMgr.SetStore(authMgr)      // 幂等：userRules KV 存储即 auth 管理器
		cfgMgr.SetOperatorID(adminID) // 运营归属账号（StoredLLMConfig 解析用）
	}
	llmCfg := llm.Config{}
	// ① 设置页保存的运营配置（URL/模型/超时/流式/并发/分类模型 + 按账号密钥）
	if saved, ok := cfgMgr.StoredLLMConfig(adminID); ok && saved != nil {
		llmCfg.APIURL = saved.APIURL
		llmCfg.Model = saved.Model
		llmCfg.Timeout = time.Duration(saved.TimeoutSec) * time.Second
		llmCfg.Streaming = saved.StreamingEnabled()
		llmCfg.BatchConcurrency = saved.BatchConcurrency
		llmCfg.ClassifierModel = saved.ClassifierModel
		// §FIX-7(20260919) 成本熔断接线：预算/空闲阈值必须随启动装配与热重建抵达
		// llm.New——此前 Resolve 从不透出这三项，客户端永远 0=不限（preFlight 形同虚设）。
		llmCfg.StreamIdleTimeout = time.Duration(saved.StreamIdleTimeoutSec) * time.Second
		llmCfg.DailyCallBudget = saved.DailyCallBudget
		llmCfg.DailyTokenBudget = saved.DailyTokenBudget
		llmCfg.ConsultDailyCalls = saved.ConsultDailyCalls
		// 运营账号的多 key（设置页保存形态：逗号分隔存 auth 配置）
		if v, ok := authMgr.GetConfig(adminID, "llm_api_keys"); ok && v != "" {
			llmCfg.APIKeys = splitKeys(v)
		} else if v, ok := authMgr.GetConfig(adminID, "llm_api_key"); ok && v != "" {
			llmCfg.APIKeys = append(llmCfg.APIKeys, v)
		}
	}
	// ② 环境变量兜底（仅补①留空的字段，不覆盖已保存值）
	if len(llmCfg.APIKeys) == 0 {
		if rawKeys := os.Getenv("LLM_API_KEYS"); rawKeys != "" {
			llmCfg.APIKeys = splitKeys(rawKeys)
		} else if k := os.Getenv("LLM_API_KEY"); k != "" {
			llmCfg.APIKeys = append(llmCfg.APIKeys, k)
		}
	}
	if llmCfg.APIURL == "" {
		llmCfg.APIURL = os.Getenv("LLM_API_URL")
	}
	if llmCfg.Model == "" {
		llmCfg.Model = os.Getenv("LLM_MODEL")
	}
	// ③ 全局 auth 配置项（历史单账号 "" 形态）
	if len(llmCfg.APIKeys) == 0 && authMgr != nil {
		if v, ok := authMgr.GetConfig("", "llm_api_keys"); ok && v != "" {
			llmCfg.APIKeys = splitKeys(v)
		} else if v, ok := authMgr.GetConfig("", "llm_api_key"); ok && v != "" {
			llmCfg.APIKeys = append(llmCfg.APIKeys, v)
		}
	}
	if llmCfg.APIURL == "" && authMgr != nil {
		if v, ok := authMgr.GetConfig("", "llm_api_url"); ok {
			llmCfg.APIURL = v
		}
	}
	// ④ 全局 config.json（无运营保存时的旧默认链）
	if llmCfg.APIURL == "" {
		llmCfg.APIURL = cfgMgr.Rules.LLM.APIURL
	}
	if llmCfg.Model == "" {
		llmCfg.Model = cfgMgr.Rules.LLM.Model
	}
	if llmCfg.Timeout == 0 {
		llmCfg.Timeout = time.Duration(cfgMgr.Rules.LLM.TimeoutSec) * time.Second
	}
	if llmCfg.BatchConcurrency == 0 {
		llmCfg.BatchConcurrency = cfgMgr.Rules.LLM.BatchConcurrency
	}
	if llmCfg.ClassifierModel == "" {
		llmCfg.ClassifierModel = cfgMgr.Rules.LLM.ClassifierModel
	}
	// §FIX-7(20260919) 预算/空闲阈值的④级兜底：运营没在设置页保存过这些项时，
	// 沿用 config.json rules.llm 的同名字段（与超时/并发同一口径的逐级回退链）。
	if llmCfg.StreamIdleTimeout <= 0 {
		llmCfg.StreamIdleTimeout = time.Duration(cfgMgr.Rules.LLM.StreamIdleTimeoutSec) * time.Second
	}
	if llmCfg.DailyCallBudget <= 0 {
		llmCfg.DailyCallBudget = cfgMgr.Rules.LLM.DailyCallBudget
	}
	if llmCfg.DailyTokenBudget <= 0 {
		llmCfg.DailyTokenBudget = cfgMgr.Rules.LLM.DailyTokenBudget
	}
	if llmCfg.ConsultDailyCalls <= 0 {
		llmCfg.ConsultDailyCalls = cfgMgr.Rules.LLM.ConsultDailyCalls
	}
	// 主 key 兼容字段：日志脱敏等单 key 语义仍指向首把
	if len(llmCfg.APIKeys) > 0 {
		llmCfg.APIKey = llmCfg.APIKeys[0]
	}
	return llmCfg
}

// splitKeys 逗号分隔密钥串 → 去空白去空项列表。
func splitKeys(raw string) []string {
	var out []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}
