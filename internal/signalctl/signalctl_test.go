// signalctl 信号控制器单元测试：白名单准入（含动量严格 opt-in）、个股/板块黑名单与影子模式、
// 持续性确认窗状态机（双通道/双账号隔离、消失重置）、留痕环。
package signalctl

import (
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func sig(action, strategyType, strategy, code string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Name: "测试", Action: action, Direction: "做多",
		Strategy: strategy, StrategyType: strategyType}
}

var base = time.Date(2026, 9, 17, 9, 40, 0, 0, time.Local)

// TestAdmitStrategyDefaultSet 空白名单（前端"全部开启"存量语义）：内置四形态+库规则放行，
// 动量/未知来源拒绝——09-17 动量误交易事故的准入侧回归。
func TestAdmitStrategyDefaultSet(t *testing.T) {
	pol := Policy{}
	for _, k := range []string{"dragon", "double_bump", "n_shape", "dragon_return", "fac_1", "pat_2"} {
		if ok, _ := AdmitStrategy(pol, sig("buy", k, "X", "600000")); !ok {
			t.Fatalf("默认全集应放行 %s", k)
		}
	}
	for _, k := range []string{"momentum", "unknown_tactic"} {
		if ok, _ := AdmitStrategy(pol, sig("buy", k, "X", "600000")); ok {
			t.Fatalf("默认全集不得放行 %s（严格 opt-in）", k)
		}
	}
	// 显式列名后动量放行
	pol.Strategies = []string{"momentum"}
	if ok, _ := AdmitStrategy(pol, sig("buy", "momentum", "动量", "600000")); !ok {
		t.Fatal("显式列名后动量应放行")
	}
	if ok, _ := AdmitStrategy(pol, sig("buy", "dragon", "龙头", "600000")); ok {
		t.Fatal("白名单非空后未列名的内置战法应拒绝（显式即权威）")
	}
}

// TestAdmitStrategyLegacyKeys 存量兼容：白名单条目可能是显示名/规则ID（旧保存形状）。
func TestAdmitStrategyLegacyKeys(t *testing.T) {
	pol := Policy{Strategies: []string{"龙头"}}
	if ok, _ := AdmitStrategy(pol, sig("buy", "dragon", "龙头", "600000")); !ok {
		t.Fatal("显示名条目应命中")
	}
	pol2 := Policy{Strategies: []string{"fac_1"}}
	s := combat_agent.Signal{Code: "600000", Action: "buy", Strategy: "波动突破战法", StrategyID: "fac_1"}
	if ok, _ := AdmitStrategy(pol2, s); !ok {
		t.Fatal("StrategyID 条目应命中")
	}
}

// TestAdmitNoKeySignalsPassThrough 非买入/做空信号直通（不拦退出、watch 不撮合）。
func TestAdmitNoKeySignalsPassThrough(t *testing.T) {
	c := New()
	noDisc := Policy{} // 双窗零值=关闭持续性监测
	for _, s := range []combat_agent.Signal{
		sig("sell", "momentum", "动量", "600000"),
		sig("watch", "momentum", "动量", "600000"),
		{Code: "600000", Action: "buy", Direction: "做空", Strategy: "高位滞涨", StrategyType: "high_churn"},
	} {
		if d := c.Admit(ChannelLive, "admin", s, noDisc, base); d.Verdict != VerdictPass {
			t.Fatalf("非多头买入应直通: %+v", d)
		}
	}
	// 无键买入（未知战法+无显示名）在空名单下被拦
	unk := sig("buy", "", "", "600000")
	if d := c.Admit(ChannelLive, "admin", unk, noDisc, base); d.Verdict != VerdictBlock {
		t.Fatalf("无键买入应拦: %+v", d)
	}
}

// TestConfirmWindowPersistence 持续性确认窗：首见 hold、窗内 hold、窗满 pass、消失重置。
func TestConfirmWindowPersistence(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{BuyConfirmMin: 5, BuyConfirmHighSec: 30, HighConfThreshold: 85}}
	s := sig("buy", "dragon", "龙头", "600000") // 低置信 → 5 分钟窗

	if d := c.Admit(ChannelLive, "admin", s, pol, base); d.Verdict != VerdictHold || d.Stage != StageConfirm {
		t.Fatalf("首见应 hold: %+v", d)
	}
	if d := c.Admit(ChannelLive, "admin", s, pol, base.Add(4*time.Minute)); d.Verdict != VerdictHold {
		t.Fatalf("窗内持续应 hold: %+v", d)
	}
	if d := c.Admit(ChannelLive, "admin", s, pol, base.Add(5*time.Minute)); d.Verdict != VerdictPass {
		t.Fatalf("窗满应 pass: %+v", d)
	}
	// pass 后探针清除：再次首见回到 hold（新信号重新走持续性）
	if d := c.Admit(ChannelLive, "admin", s, pol, base.Add(6*time.Minute)); d.Verdict != VerdictHold {
		t.Fatalf("pass 后探针应重置: %+v", d)
	}
	// 消失即重置（Evaluate 以全量活跃集清理探针）
	c2 := New()
	c2.Admit(ChannelLive, "admin", s, pol, base)
	c2.Evaluate(ChannelLive, "admin", nil, pol, base.Add(2*time.Minute))
	if d := c2.Admit(ChannelLive, "admin", s, pol, base.Add(3*time.Minute)); d.Verdict != VerdictHold {
		t.Fatalf("消失后应重置探针: %+v", d)
	}
}

// TestConfirmChannelAndAccountIsolation 双通道（live/paper）与双账号状态互不影响。
func TestConfirmChannelAndAccountIsolation(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{BuyConfirmMin: 5, BuyConfirmHighSec: 30}}
	s := sig("buy", "n_shape", "N形", "600000")
	if d := c.Admit(ChannelLive, "admin", s, pol, base); d.Verdict != VerdictHold {
		t.Fatalf("live 首见应 hold: %+v", d)
	}
	if d := c.Admit(ChannelPaper, "admin", s, pol, base); d.Verdict != VerdictHold {
		t.Fatalf("paper 首见应独立 hold: %+v", d)
	}
	if d := c.Admit(ChannelLive, "bob", s, pol, base); d.Verdict != VerdictHold {
		t.Fatalf("bob 首见应独立 hold: %+v", d)
	}
	// 高置信快车道：30s
	hs := s
	hs.Confidence = 0.9
	if d := c.Admit(ChannelPaper, "admin", hs, pol, base.Add(time.Second)); d.Verdict == VerdictPass {
		t.Fatal("1s < 30s 高置信窗不应 pass")
	}
	if d := c.Admit(ChannelPaper, "admin", hs, pol, base.Add(31*time.Second)); d.Verdict != VerdictPass {
		t.Fatalf("31s ≥ 30s 高置信窗应 pass: %+v", d)
	}
}

// TestBlacklistsAndShadow 个股/板块黑名单硬拦与影子模式。
func TestBlacklistsAndShadow(t *testing.T) {
	c := New()
	pol := Policy{CodeBlacklist: []string{"600519.SH"}, SectorBlacklist: []string{"半导体"}}
	s := sig("buy", "dragon", "龙头", "600519") // 裸码命中带后缀条目（config.CodeInBlacklist 归一口径）
	if d := c.Admit(ChannelLive, "admin", s, pol, base); d.Verdict != VerdictBlock || d.Stage != StageCodeBlacklist {
		t.Fatalf("个股黑名单应硬拦: %+v", d)
	}
	s2 := sig("buy", "dragon", "龙头", "600000")
	s2.Sector = "半导体"
	if d := c.Admit(ChannelLive, "admin", s2, pol, base); d.Verdict != VerdictBlock || d.Stage != StageSectorBlack {
		t.Fatalf("板块黑名单应硬拦: %+v", d)
	}
	// 影子模式：留痕放行
	pol.ShadowBlacklist = true
	if d := c.Admit(ChannelLive, "admin", s, pol, base.Add(time.Minute)); d.Verdict != VerdictPass || !d.Shadow {
		t.Fatalf("影子模式应 pass+留痕: %+v", d)
	}
}

// TestEvaluateBatchPruneAndAlign Evaluate 批量：返回等长、下标对齐，清理本轮缺失探针。
func TestEvaluateBatchPruneAndAlign(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{BuyConfirmMin: 5, BuyConfirmHighSec: 30}}
	a := sig("buy", "dragon", "龙头", "600001")
	b := sig("watch", "dragon", "龙头", "600002")
	ds := c.Evaluate(ChannelLive, "admin", []combat_agent.Signal{a, b}, pol, base)
	if len(ds) != 2 {
		t.Fatalf("应等长: %d", len(ds))
	}
	if ds[0].Code != "600001" || ds[0].Verdict != VerdictHold {
		t.Fatalf("买入首见 hold: %+v", ds[0])
	}
	if ds[1].Verdict != VerdictPass || ds[1].Stage != StagePassthrough {
		t.Fatalf("watch 直通: %+v", ds[1])
	}
	// 下一轮 a 消失 → 探针清理；重现重新 hold
	ds2 := c.Evaluate(ChannelLive, "admin", nil, pol, base.Add(time.Minute))
	if len(ds2) != 0 {
		t.Fatal("空集返回空")
	}
	if d := c.Admit(ChannelLive, "admin", a, pol, base.Add(2*time.Minute)); d.Verdict != VerdictHold {
		t.Fatalf("清理后重现应 hold: %+v", d)
	}
}

// TestAuditRing 留痕环：非 pass 与影子命中进环、pass 干净不进、Recent 最新在前。
func TestAuditRing(t *testing.T) {
	c := New()
	pol := Policy{Strategies: []string{"momentum"}}
	m := sig("buy", "momentum", "动量", "600000")
	dragon := sig("buy", "dragon", "龙头", "600001") // 白名单非空且未列 dragon → block
	c.Admit(ChannelPaper, "admin", m, pol, base)   // pass（显式列名+零窗直通）不进环
	c.Admit(ChannelPaper, "admin", dragon, pol, base)
	r := c.Recent(10)
	if len(r) != 1 || r[0].Code != "600001" || r[0].Verdict != VerdictBlock {
		t.Fatalf("留痕环应只含拦截: %+v", r)
	}
}

// TestAuditAttachReplayAndGarbageTolerant §D-2（GAP_VERIFY_20260917_PM）留痕落盘/回灌：
// 拦截裁定写 JSONL → 新控制器（模拟重启）AttachAudit 回灌环；脏行（进程被杀半行）跳过；
// Account 归属随行落盘。
func TestAuditAttachReplayAndGarbageTolerant(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c := New()
	if err := c.AttachAudit(dir); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 白名单只列动量 → dragon 在战法层硬拦并留痕
	pol := Policy{Strategies: []string{"momentum"}}
	d := c.Admit(ChannelPaper, "u_alice", sig("buy", "dragon", "龙头", "600000"), pol, now)
	if d.Verdict != VerdictBlock || d.Stage != StageStrategy {
		t.Fatalf("应战法层拦截, got %+v", d)
	}
	if d.Account != "u_alice" {
		t.Fatalf("裁定应携带账号归属, got %q", d.Account)
	}
	b, err := os.ReadFile(auditFileFor(dir, now))
	if err != nil {
		t.Fatalf("当日审计文件应存在: %v", err)
	}
	if !strings.Contains(string(b), `"account":"u_alice"`) || !strings.Contains(string(b), "600000") {
		t.Fatalf("审计行应含账号与代码, got %s", string(b))
	}
	// 模拟进程被杀留下的半行脏数据 + 再回灌
	f, _ := os.OpenFile(auditFileFor(dir, now), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"channel":"paper","verdict":"block"`) // 无换行无闭合 = 脏行
	f.Close()
	c2 := New()
	if err := c2.AttachAudit(dir); err != nil {
		t.Fatalf("attach2: %v", err)
	}
	rec := c2.Recent(10)
	if len(rec) != 1 || rec[0].Code != "600000" || rec[0].Account != "u_alice" {
		t.Fatalf("回灌应得 1 条有效裁定（脏行跳过）, got %+v", rec)
	}
	// 纯内存模式（未绑定目录）不受影响：直接 New 记录零副作用
	c3 := New()
	c3.Admit(ChannelLive, "u_bob", sig("buy", "momentum", "动量", "000001"), Policy{Strategies: []string{"momentum"}}, now)
	if len(c3.Recent(5)) != 0 {
		t.Fatal("momentum 显式开启后 pass 不留痕（仅非常态入环）")
	}
}
