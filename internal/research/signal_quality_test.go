package research

import "testing"

func k(tac, sec, nt, ph string) QualityBucketKey {
	return MakeQualityKey(tac, sec, nt, ph)
}

func TestQualityUnderSample_IsOne(t *testing.T) {
	tb := NewSignalQualityTable(20)
	tb.Update(k("龙头", "AI", "行业", "发酵"), true)
	if w := tb.Weight(k("龙头", "AI", "行业", "发酵")); w != 1.0 {
		t.Errorf("below MinSample weight must stay 1.0, got %.3f", w)
	}
	if g := tb.GateShift(k("龙头", "AI", "行业", "发酵")); g != 0 {
		t.Errorf("below MinSample gate must stay 0, got %.3f", g)
	}
}

func TestQualityRaisesWeightWithGoodHit(t *testing.T) {
	key := k("龙头", "AI", "行业", "发酵")
	tb := NewSignalQualityTable(20)
	// 20 次全部命中（命中率 1.0 >> 0.35）→ 权重上修、门槛放松
	for i := 0; i < 20; i++ {
		tb.Update(key, true)
	}
	if w := tb.Weight(key); w <= 1.0 {
		t.Errorf("high hit-rate should raise weight, got %.3f", w)
	}
	if g := tb.GateShift(key); g >= 0 {
		t.Errorf("high hit-rate should relax gate (negative), got %.3f", g)
	}
}

func TestQualityTrimsWeightWithBadHit(t *testing.T) {
	key := k("双响炮", "新能源", "公司", "退潮")
	tb := NewSignalQualityTable(20)
	for i := 0; i < 20; i++ {
		tb.Update(key, false)
	}
	if w := tb.Weight(key); w >= 1.0 {
		t.Errorf("low hit-rate should trim weight, got %.3f", w)
	}
	if g := tb.GateShift(key); g <= 0 {
		t.Errorf("low hit-rate should raise gate, got %.3f", g)
	}
}

func TestQualityClamped(t *testing.T) {
	key := k("动量", "券商", "宏观", "启动")
	tb := NewSignalQualityTable(5)
	for i := 0; i < 100; i++ {
		tb.Update(key, true)
	}
	if w := tb.Weight(key); w > 1.3 {
		t.Errorf("weight must clamp at 1.3, got %.3f", w)
	}
	if g := tb.GateShift(key); g < -0.2 {
		t.Errorf("gate must clamp at -0.2, got %.3f", g)
	}
}

func TestQualityKeyNormalization(t *testing.T) {
	if got := MakeQualityKey("龙头", "AI", "", "发酵"); len(got) == 0 {
		t.Errorf("key must not be empty, got %q", got)
	}
}
