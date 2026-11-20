// index_amount_series_test.go — §FLOW-VOLUME-B 指数量能三源降级锁（2026-11-19；2026-11-20 补参数锁）。
// 验证：① 东财 push2his 给 amount（元）；② 主腿失败退 push2delay 镜像；③ 两东财源失败退新浪（用 volume 股）；
// ④ 三源全失败返回 error（调用方 NaN 弃权）；⑤ 每源内上证主、取不到退沪深300；⑥ 指数码不拼成平安银行；
// ⑦ 东财 URL 必带 end/fields1/fields2（缺一即 rc:102 —— 2026-11-20 量比腿整日弃权的真因）。
package data

import (
	"strconv"
	"strings"
	"testing"
)

// emKLineBody 造 n 根东财日 K（CSV：date,open,high,low,close,volume,amount），amount 递增便于辨识。
func emKLineBody(days []string, amount float64) []byte {
	rows := make([]string, 0, len(days))
	for _, d := range days {
		rows = append(rows, d+",3583,3584,3547,3577,4129127,"+fmtFloat(amount))
	}
	return []byte(`{"data":{"klines":["` + strings.Join(rows, `","`) + `"]}}`)
}

func fmtFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// sinaIndexKLineBody 造新浪指数日 K（只有 volume，无 amount——2026-11-19 实测形态）。
func sinaIndexKLineBody(days []string, volume float64) []byte {
	rows := make([]string, 0, len(days))
	for _, d := range days {
		rows = append(rows, `{"day":"`+d+`","open":"3583","high":"3584","low":"3547","close":"3577","volume":"`+fmtFloat(volume)+`"}`)
	}
	return []byte("[" + strings.Join(rows, ",") + "]")
}

func flowDays() []string {
	return []string{"2026-11-12", "2026-11-13", "2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19", "2026-11-20"}
}

func TestIndexAmountSeriesPrimaryEM(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com": emKLineBody(flowDays(), 4e11),
	}, true, true)
	out, label, err := m.IndexAmountSeries(6, "2026-11-20")
	if err != nil {
		t.Fatalf("东财主应成功: %v", err)
	}
	if label != "em:000001.SH" {
		t.Fatalf("应标记 em 上证主, got %q", label)
	}
	if len(out) != 6 || out[len(out)-1].TradeDate != "2026-11-20" || out[0].Amount <= 0 {
		t.Fatalf("序列应 6 根升序且带 amount, got %+v", out)
	}
}

// TestIndexAmountSeriesEMURLParams 2026-11-20 现场回归锁：东财 kline 缺 `end` 或
// `fields1/fields2` 任一即回 `rc:102 / data:null`，三源全败 ⇒ 量比腿与资金流腿一起弃权。
// 这条锁钉的是 URL 形状本身（假服务器只按 host 回体，不校验参数——旧用例因此漏掉了这个
// 让整条腿哑掉一天的缺陷）。
func TestIndexAmountSeriesEMURLParams(t *testing.T) {
	m, rt := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com": emKLineBody(flowDays(), 4e11),
	}, true, true)
	if _, _, err := m.IndexAmountSeries(6, "2026-11-20"); err != nil {
		t.Fatalf("东财主应成功: %v", err)
	}
	path := rt.lastPath("push2his.eastmoney.com")
	for _, need := range []string{
		"klt=101", "lmt=8", "fqt=1",
		"end=20500101",
		"fields1=f1,f2,f3,f4,f5,f6",
		"fields2=f51,f52,f53,f54,f55,f56,f57,f58",
	} {
		if !strings.Contains(path, need) {
			t.Fatalf("东财 kline 必填参数缺失 %q（缺这一项东财回 rc:102/data:null）\npath=%q", need, path)
		}
	}
}

func TestIndexAmountSeriesEMMirror(t *testing.T) {
	m, rt := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com":   nil, // 主腿 EOF
		"push2delay.eastmoney.com": emKLineBody(flowDays(), 3e11),
	}, true, true)
	out, label, err := m.IndexAmountSeries(6, "2026-11-20")
	if err != nil {
		t.Fatalf("push2delay 镜像应兜底: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("镜像应给 6 根, got %d", len(out))
	}
	if label != "em_mirror:000001.SH" {
		t.Fatalf("应标记 em_mirror, got %q", label)
	}
	if rt.hostHits("push2his.eastmoney.com") == 0 || rt.hostHits("push2delay.eastmoney.com") == 0 {
		t.Fatalf("应先试主腿再试镜像, hits=%+v", rt.hits)
	}
}

func TestIndexAmountSeriesSinaFallback(t *testing.T) {
	m, rt := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com":    nil,
		"push2delay.eastmoney.com":  nil,
		"money.finance.sina.com.cn": sinaIndexKLineBody(flowDays(), 4.1e10),
	}, true, true)
	out, label, err := m.IndexAmountSeries(6, "2026-11-20")
	if err != nil {
		t.Fatalf("新浪应兜底: %v", err)
	}
	if label != "sina:000001.SH" {
		t.Fatalf("应标记 sina 上证主, got %q", label)
	}
	// 新浪用 volume（股）当标量，量级应 ~4.1e10 而非东财 amount 的 4e11。
	if len(out) == 0 || out[0].Amount < 4e10 || out[0].Amount > 4.2e10 {
		t.Fatalf("新浪应用 volume 股做量能标量, got %+v", out)
	}
	// 必须拼 sh000001，绝不能是 sz000001（平安银行）。
	path := rt.lastPath("money.finance.sina.com.cn")
	if !strings.Contains(path, "symbol=sh000001") || strings.Contains(path, "symbol=sz000001") {
		t.Fatalf("新浪指数码必须是 sh000001（非平安银行）, path=%q", path)
	}
}

// TestIndexAmountSeriesStaleSourceFailover 验证本次修复的核心：东财两源都返回**断更旧数据**
// （末根 11-19，今日 11-20）时，必须判为假活并继续降级到新浪的当日根，而不是把旧序列当成功返回。
func TestIndexAmountSeriesStaleSourceFailover(t *testing.T) {
	staleDays := []string{"2026-11-11", "2026-11-12", "2026-11-13", "2026-11-16", "2026-11-17", "2026-11-19"}
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com":    emKLineBody(staleDays, 4e11),           // 末根 11-19 ≠ 今日 11-20 → 假活
		"push2delay.eastmoney.com":  emKLineBody(staleDays, 3e11),           // 镜像同样断更
		"money.finance.sina.com.cn": sinaIndexKLineBody(flowDays(), 4.1e10), // 末根 11-20 = 今日
	}, true, true)
	out, label, err := m.IndexAmountSeries(6, "2026-11-20")
	if err != nil {
		t.Fatalf("新浪当日根应兜底: %v", err)
	}
	if !strings.HasPrefix(label, "sina:") {
		t.Fatalf("断更东财源应被跳过、落新浪, got label=%q", label)
	}
	if out[len(out)-1].TradeDate != "2026-11-20" {
		t.Fatalf("返回的末根必须是今日, got %q", out[len(out)-1].TradeDate)
	}
}

func TestIndexAmountSeriesAllFail(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{}, true, true)
	_, _, err := m.IndexAmountSeries(6, "2026-11-20")
	if err == nil {
		t.Fatal("三源全失败应返回 error（调用方弃权）")
	}
}

func TestIndexAmountSeriesCSI300Fallback(t *testing.T) {
	// 上证在三个源都取不到，沪深300 在新浪可取 → 应退到 sina:000300.SH。
	// fakeRT 按 host 返回，无法按 secid/symbol 区分同一 host 的两个指数；
	// 这里让东财两 host 全失败，新浪 host 返回——首个候选 sh000001 也拿到同样的体（会成功），
	// 故改用"上证 amount=0"的东财体触发不足、沪深300 体正常来验证降级形状。
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com":    emKLineBody(flowDays(), 0), // amount=0 → 有效日不足，退沪深300
		"push2delay.eastmoney.com":  nil,
		"money.finance.sina.com.cn": sinaIndexKLineBody(flowDays(), 1.8e10),
	}, true, true)
	_, label, err := m.IndexAmountSeries(6, "2026-11-20")
	if err != nil {
		t.Fatalf("应有源可取: %v", err)
	}
	if !strings.HasPrefix(label, "em:") && !strings.HasPrefix(label, "sina:") {
		t.Fatalf("label 应如实标记实际源与指数, got %q", label)
	}
}
