package data

import (
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EventsConfig D1 事件匹配规则配置。
type EventsConfig struct {
	Meta           MetaConfig          `yaml:"meta"`
	TopImpact      map[string][]string `yaml:"top_impact"`
	Indirect       map[string][]string `yaml:"indirect"`
	MediumImpact   map[string][]string `yaml:"medium_impact"`
	LowImpact      []string            `yaml:"low_impact"`
	NegativeFilter []string            `yaml:"negative_filter"`
}

type MetaConfig struct {
	Version    string `yaml:"version"`
	Maintainer string `yaml:"maintainer"`
}

type D1Result struct {
	Blocked     bool   `json:"blocked"`
	BlockReason string `json:"block_reason,omitempty"`
	Score       int    `json:"-"` // 保留字段，不再用于本地打分
}

type EventMatcher struct {
	cfg        *EventsConfig
	negRegexps []*regexpRule
	rawContent string // YAML 原始内容，作为 LLM prompt 参考
}

type regexpRule struct {
	pattern *regexp.Regexp
	group   string
}

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

func compilePattern(s string) (*regexp.Regexp, error) {
	if strings.HasSuffix(s, ".*") || strings.HasPrefix(s, ".*") || strings.Contains(s, "[") {
		return regexp.Compile(s)
	}
	return regexp.Compile(regexp.QuoteMeta(s))
}
