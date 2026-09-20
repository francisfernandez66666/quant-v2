// 本文件：咨询系统提示词（股票咨询页）的回归锁定。
// §CONSULT-UX(20260921)：针对用户反馈的三类问题（数据丢失 / 结构差 / 枯燥）新增的提示词约束——数字原样照抄、回答结构、缺失字段诚实告知。
// English: regression locks for the consult system prompt after the 2026-09-21 UX fix (data-loss / structure / dryness).
package llm

import "testing"

// TestConsultSystemPromptConsultUX 锁定三条新增约束的存在，防止后续改动把"原样照抄数字"
// "回答结构" "缺失字段诚实告知" 这些关键引导词误删，导致 [数据缺失] 回潮或又变回一大段。
func TestConsultSystemPromptConsultUX(t *testing.T) {
	p := ConsultSystemPrompt()
	mustContain := []string{
		"原样照抄",   // 数字引用铁律：禁止加逗号/换算单位/四舍五入，否则被隐去成[数据缺失]
		"回答结构",   // 推荐 ## 小标题 + 项目符号，避免一大段不分点
		"数据源未返回", // 缺失字段诚实告知：直接说"暂未取到"，不要自己补数字
		"手",      // 成交量同时给了股/手双口径，提示模型任选其一原样照抄
		"300 字",  // 长度约束：信息密度优先，少水话
	}
	for _, want := range mustContain {
		if !containsRune(p, want) {
			t.Errorf("咨询提示词应含 %q，否则三类 UX 问题会回潮", want)
		}
	}
}

// containsRune 子串判定（提示词为普通字符串，无正则需求）。
func containsRune(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
