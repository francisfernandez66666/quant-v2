// Package data — D1 事件匹配引擎。
// 基于 YAML 规则文件，对新闻标题/正文进行正则匹配，
// 按事件影响层级（top_impact/indirect/medium_impact/low_impact）打分，
// 用于板块事件驱动的选股逻辑。
package data

import (
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EventsConfig D1 事件匹配规则配置。
// 由 YAML 文件反序列化加载，支持热更新。
type EventsConfig struct {
	Meta           MetaConfig          `yaml:"meta"`            // 元信息
	TopImpact      map[string][]string `yaml:"top_impact"`      // 顶级影响事件（分行业组）
	Indirect       map[string][]string `yaml:"indirect"`        // 间接影响事件（分行业组）
	MediumImpact   map[string][]string `yaml:"medium_impact"`   // 中等影响事件（分行业组）
	LowImpact      []string            `yaml:"low_impact"`      // 低影响关键词列表
	NegativeFilter []string            `yaml:"negative_filter"` // 负面过滤关键词（命中即阻断）
}

// MetaConfig 规则元信息。
type MetaConfig struct {
	Version    string `yaml:"version"`    // 规则版本
	Maintainer string `yaml:"maintainer"` // 维护人
}

// D1Result D1 事件匹配结果。
type D1Result struct {
	Score        int      `json:"score"`                  // 事件评分（0/20/30/40）
	Level        string   `json:"level"`                  // 等级：none/low_impact/medium_impact/indirect/top_impact/blocked
	MatchedRules []string `json:"matched_rules"`          // 匹配到的规则列表
	Blocked      bool     `json:"blocked"`                // 是否被负面过滤阻断
	BlockReason  string   `json:"block_reason,omitempty"` // 阻断原因
}

// EventMatcher D1 事件匹配器。
// 将 YAML 规则预编译为正则，对输入文本进行层级匹配。
type EventMatcher struct {
	cfg             *EventsConfig
	topRegexps      []*regexpRule // 顶级影响预编译正则
	indirectRegexps []*regexpRule // 间接影响预编译正则
	mediumRegexps   []*regexpRule // 中等影响预编译正则
	lowKeywords     []string      // 低影响关键词（字符串匹配）
	negRegexps      []*regexpRule // 负面过滤预编译正则
}

// regexpRule 预编译正则规则及其所属分组。
type regexpRule struct {
	pattern *regexp.Regexp
	group   string // 分组名（如 "policy"、"earnings"）
}

// LoadEvents 从 YAML 文件加载事件规则配置。
func LoadEvents(path string) (*EventsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg EventsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// NewEventMatcher 根据配置创建事件匹配器，预编译所有正则规则。
// 规则格式：纯字符串自动转义，含 .* 或 [ 的作为原始正则编译。
func NewEventMatcher(cfg *EventsConfig) *EventMatcher {
	m := &EventMatcher{cfg: cfg}

	for group, keywords := range cfg.TopImpact {
		for _, kw := range keywords {
			re, err := compilePattern(kw)
			if err == nil {
				m.topRegexps = append(m.topRegexps, &regexpRule{pattern: re, group: group})
			}
		}
	}

	for group, keywords := range cfg.Indirect {
		for _, kw := range keywords {
			re, err := compilePattern(kw)
			if err == nil {
				m.indirectRegexps = append(m.indirectRegexps, &regexpRule{pattern: re, group: group})
			}
		}
	}

	for group, keywords := range cfg.MediumImpact {
		for _, kw := range keywords {
			re, err := compilePattern(kw)
			if err == nil {
				m.mediumRegexps = append(m.mediumRegexps, &regexpRule{pattern: re, group: group})
			}
		}
	}

	m.lowKeywords = cfg.LowImpact

	for _, kw := range cfg.NegativeFilter {
		re, err := compilePattern(kw)
		if err == nil {
			m.negRegexps = append(m.negRegexps, &regexpRule{pattern: re})
		}
	}

	return m
}

// MatchD1 对输入文本执行 D1 事件匹配。
// 匹配顺序：负面过滤 → top_impact → indirect → medium_impact → low_impact
// 一旦命中较高层级（>=40 / >=30）即提前返回。
func (m *EventMatcher) MatchD1(text string) *D1Result {
	result := &D1Result{Score: 0, Level: "none"}

	// P1: 负面过滤 — 命中即阻断
	for _, rule := range m.negRegexps {
		if rule.pattern.MatchString(text) {
			return &D1Result{
				Score:       0,
				Level:       "blocked",
				Blocked:     true,
				BlockReason: "negative_filter:" + rule.pattern.String(),
			}
		}
	}

	// P2: 顶级影响 — 评分 40
	for _, rule := range m.topRegexps {
		if rule.pattern.MatchString(text) {
			result.Score = 40
			result.Level = "top_impact"
			result.MatchedRules = append(result.MatchedRules, rule.group+":"+rule.pattern.String())
		}
	}

	if result.Score >= 40 {
		return result
	}

	// P3: 间接影响 — 评分 30
	for _, rule := range m.indirectRegexps {
		if rule.pattern.MatchString(text) {
			result.Score = 30
			result.Level = "indirect"
			result.MatchedRules = append(result.MatchedRules, rule.group+":"+rule.pattern.String())
		}
	}

	if result.Score >= 30 {
		return result
	}

	// P4: 中等影响 — 业绩/认证类 30 分，其余 20 分
	for _, rule := range m.mediumRegexps {
		if rule.pattern.MatchString(text) {
			if rule.group == "earnings" || rule.group == "certification" {
				result.Score = 30
			} else {
				result.Score = 20
			}
			result.Level = "medium_impact"
			result.MatchedRules = append(result.MatchedRules, rule.group+":"+rule.pattern.String())
		}
	}

	if result.Score > 0 {
		return result
	}

	// P5: 低影响 — 关键词字符串匹配
	textLower := strings.ToLower(text)
	for _, kw := range m.lowKeywords {
		if strings.Contains(textLower, strings.ToLower(kw)) {
			result.Score = 0
			result.Level = "low_impact"
			return result
		}
	}

	return result
}

// compilePattern 编译规则字符串为正则表达式。
// 含 .* 或 [ 的作为原始正则编译；纯文本自动调用 QuoteMeta 避免意外匹配。
func compilePattern(s string) (*regexp.Regexp, error) {
	if strings.HasSuffix(s, ".*") || strings.HasPrefix(s, ".*") || strings.Contains(s, "[") {
		return regexp.Compile(s)
	}
	return regexp.Compile(regexp.QuoteMeta(s))
}
