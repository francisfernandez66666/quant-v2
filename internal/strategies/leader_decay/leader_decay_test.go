// leader_decay 龙头断板战法单元测试：连板前提/仍封板不发/反包保护/门槛65/退潮加分/数据不足。
package leader_decay

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/shortbase"
)

// boardBars 生成 n 根日K，最后 boards+1 根（含今日之前的连板）按主板 10% 涨停推进。
func boardBars(n, boards int, base float64) []data.KLine {
	kl := make([]data.KLine, n)
	c := base
	for i := range kl {
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n), Open: c, High: c, Low: c * 0.99, Close: c, Volume: 1e6}
		if i >= n-2-boards { // 连板段（不含今日）
			nc := c * 1.1
			kl[i].Close, kl[i].High = nc, nc
			c = nc
		}
	}
	return kl
}

func decayData() *shortbase.Data {
	n := 20
	kl := boardBars(n, 3, 10.0) // 此前 3 连板
	prevClose := kl[n-2].Close
	// 今日：断板大面 + 天量
	last := &kl[n-1]
	last.Open, last.High, last.Close, last.Volume = prevClose*1.02, prevClose*1.05, prevClose*0.95, 4e6
	return &shortbase.Data{
		Code: "600XXX.SH", Name: "测试龙头", Price: last.Close, PrevClose: prevClose, ChangePct: -5.0,
		LimitUpPct: 9.9, ConsecBoards: 3,
		TouchedBoardToday: false, SealedToday: false, AfternoonReseal: false,
		Vol5: 1.3e6, TodayVolVs5d: 3.0,
		EmotionPhase:         "退潮",
		SectorLimitUpDropPct: 0.6,
		KLines:               kl,
	}
}

func TestPassFullChain(t *testing.T) {
	s := New(nil)
	ev, err := s.Evaluate("600000.SH", decayData())
	if err != nil || ev == nil {
		t.Fatalf("Evaluate 出错: %v", err)
	}
	if !ev.Pass || ev.TotalScore < 65 {
		t.Fatalf("3板大面+天量+板块退潮应过65闸: total=%.1f", ev.TotalScore)
	}
	sig, _ := s.GenerateSignal("600000.SH", ev)
	if sig == nil || string(sig.Action) != "sell" {
		t.Fatalf("应产出 sell 信号")
	}
}

func TestNotLeader(t *testing.T) {
	s := New(nil)
	d := decayData()
	d.ConsecBoards = 1
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "no_board" || ev.Pass {
		t.Fatalf("非连板股应 no_board: %s", ev.Level)
	}
}

func TestStillSealed(t *testing.T) {
	s := New(nil)
	d := decayData()
	d.SealedToday = true
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "still_sealed" || ev.Pass {
		t.Fatalf("仍封板不应发信号")
	}
}

func TestResealProtection(t *testing.T) {
	s := New(nil)
	d := decayData()
	d.AfternoonReseal = true
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "reseal" || ev.Pass {
		t.Fatalf("14:30后回封应触发反包保护")
	}
}

func TestThreshold65Default(t *testing.T) {
	s := New(nil)
	if got := s.scoreThreshold(); got != 65 {
		t.Fatalf("默认门槛应为65, got %.1f", got)
	}
}

func TestWeakRetreatLowScore(t *testing.T) {
	s := New(nil)
	d := decayData()
	d.EmotionPhase = ""
	d.SectorLimitUpDropPct = 0
	d.TodayVolVs5d = 0.8
	d.TouchedBoardToday = false
	ev, _ := s.Evaluate("x", d)
	if ev.Details["retreat"] > 0 {
		t.Fatalf("无退潮证据时情绪分应为0: %+v", ev.Details["retreat"])
	}
}
