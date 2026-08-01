// Package data — D1 事件匹配规则加载与执行。
// 从 YAML 配置文件加载事件规则，对文本执行负面关键词阻断（负面过滤），
// 供板块扫描 / 新闻归因识别事件驱动型风险。
package data

import (
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EventsConfig D1 事件匹配规则配置。
type EventsConfig struct {
	Meta           MetaConfig          `yaml:"meta"`            // 配置元信息（版本/维护者）
	TopImpact      map[string][]string `yaml:"top_impact"`      // 高影响事件关键词（预留）
	Indirect       map[string][]string `yaml:"indirect"`        // 间接影响事件（预留）
	MediumImpact   map[string][]string `yaml:"medium_impact"`   // 中等影响事件（预留）
	LowImpact      []string            `yaml:"low_impact"`      // 低影响事件（预留）
	NegativeFilter []string            `yaml:"negative_filter"` // 负面阻断关键词列表
}

// MetaConfig 事件配置元信息。
type MetaConfig struct {
	Version    string `yaml:"version"`    // 规则版本号
	Maintainer string `yaml:"maintainer"` // 维护者
}

// D1Result D1 事件匹配结果。
type D1Result struct {
	Blocked     bool   `json:"blocked"`                // 是否被负面规则阻断
	BlockReason string `json:"block_reason,omitempty"` // 阻断原因（命中哪条规则）
	Score       int    `json:"-"`                      // 保留字段，不再用于本地打分
}

// EventMatcher D1 事件匹配器（按负面关键词阻断）。
type EventMatcher struct {
	cfg        *EventsConfig
	negRegexps []*regexpRule // 编译后的负面过滤正则集合
	rawContent string        // YAML 原始内容，作为 LLM prompt 参考
}

// regexpRule 一条已编译的负面过滤规则。
// pattern 为编译后的正则，group 为所属事件分组（当前未使用）。
type regexpRule struct {
	pattern *regexp.Regexp
	group   string
}

// LoadEvents 从 YAML 文件加载事件匹配配置。
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

// NewEventMatcher 创建事件匹配器并编译负面过滤正则。
func NewEventMatcher(cfg *EventsConfig) *EventMatcher {
	m := &EventMatcher{cfg: cfg}
	for _, kw := range cfg.NegativeFilter {
		re, err := compilePattern(kw)
		if err == nil {
			m.negRegexps = append(m.negRegexps, &regexpRule{pattern: re})
		}
	}
	return m
}

// MatchD1 执行 D1 事件匹配 — 仅做负面阻断，不做正面评分。
func (m *EventMatcher) MatchD1(text string) *D1Result {
	for _, rule := range m.negRegexps {
		if rule.pattern.MatchString(text) {
			return &D1Result{
				Blocked:     true,
				BlockReason: "negative_filter:" + rule.pattern.String(),
			}
		}
	}
	return &D1Result{}
}

// compilePattern 将关键词编译为正则表达式。
// 若关键词本身含正则语法（.* 通配或字符类 []），直接作为正则编译；
// 否则使用 QuoteMeta 将普通关键词转义，避免特殊字符误匹配。
func compilePattern(s string) (*regexp.Regexp, error) {
	if strings.HasSuffix(s, ".*") || strings.HasPrefix(s, ".*") || strings.Contains(s, "[") {
		return regexp.Compile(s)
	}
	return regexp.Compile(regexp.QuoteMeta(s))
}
