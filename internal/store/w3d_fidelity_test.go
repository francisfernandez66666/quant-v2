// w3d_fidelity_test.go — §0926E2E-12A（2026-09-26 三波）候选输入保真水印行为用例。
//
// 缺陷 12 定性：回放与实盘的分叉在**输入侧**（日K近似下尾盘 14:57 门控结构性无法触发、
// 交易分钟数本地副本），回放报告是审批依据——裁决=选项A：水印进数据结构终身随行，
// 而不是只写文档。本文件钉：
//
//	① SaveCandidate→ListCandidates/CandidateByID fidelity 往返一致（列集/扫描序不错位）；
//	② 不传 fidelity 的存量形态写空串、读回不炸（旧候选无水印=合法）；
//	③ 列集错位回归锁：同一条记录里 params 与 fidelity 各归各位（candidateCols 追加列
//	   最容易错的就是 scan 顺序，§批量改动完整性）。
package store

import (
	"strings"
	"testing"
)

func TestW3dFidelityRoundTrip(t *testing.T) {
	db := testDB(t)
	id, err := db.SaveCandidate(&Candidate{
		Kind: "factor", Factors: `["Mom20"]`, Weights: "{}", Reason: "水印往返测试",
		Params: `{"start":"20260101"}`, Fidelity: FidelityDailyKApprox,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CandidateByID(id)
	if err != nil || c == nil {
		t.Fatalf("回读失败：%v %+v", err, c)
	}
	if c.Fidelity != FidelityDailyKApprox {
		t.Fatalf("fidelity 往返不一致：%q", c.Fidelity)
	}
	list, err := db.ListCandidates("proposed")
	if err != nil || len(list) != 1 {
		t.Fatalf("列表腿异常：%v n=%d", err, len(list))
	}
	if list[0].Fidelity != FidelityDailyKApprox {
		t.Fatalf("ListCandidates fidelity 丢失：%q", list[0].Fidelity)
	}
	// 水印文案自身的信息量锁：三个关键断言词必须在（防"近似输入"章被改成无意义占位）。
	for _, want := range []string{"日K回放", "14:57", "回放结论≠实盘预期"} {
		if !strings.Contains(FidelityDailyKApprox, want) {
			t.Fatalf("标准水印缺关键词 %q：%s", want, FidelityDailyKApprox)
		}
	}
}

func TestW3dLegacyCandidateWithoutFidelity(t *testing.T) {
	db := testDB(t)
	id, err := db.SaveCandidate(&Candidate{
		Kind: "weights", Factors: "[]", Weights: "{}", Params: `{"a":1}`, Reason: "无水印存量形态",
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := db.CandidateByID(id)
	if c.Fidelity != "" {
		t.Fatalf("未打水印的候选必须读回空串，实得 %q", c.Fidelity)
	}
	// 列集错位回归：params 不能被 fidelity 的扫描位顶掉（追加列的失配形态）。
	if c.Params != `{"a":1}` {
		t.Fatalf("params 串位：%q", c.Params)
	}
}
