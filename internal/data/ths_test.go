package data

import (
	"encoding/json"
	"testing"
)

func toJSONArray(v []interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestParseTHSQuotePrevClose 昨收推算（realhead 数组索引 9）：
// 用昨收 35.67、昨收与现价之比在 (0.5, 5) 区间内时，正确推出 Close 与 ChangePct。
func TestParseTHSQuotePrevClose(t *testing.T) {
	// items 数值按 parseTHSQuote 约定布局：
	// arr[1]=code, [2]=name, [3]=open, [4]=high, [5]=low, [6]=price, [7]=volume, [8]=amount, [9]=prev_close
	item := []interface{}{
		"", "hs_1.600580", "卧龙电驱", 35.00, 39.08, 34.67, 36.86, 127275327.0, 4558378206.0, 35.67,
	}
	body := []byte(`{"data":{"items":{"1":` + toJSONArray(item) + `}}}`)
	si, err := parseTHSQuote(body, "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	if si.Close != 35.67 {
		t.Errorf("昨收应=35.67, got %.2f", si.Close)
	}
	// (36.86-35.67)/35.67*100 = 3.3364...
	expect := (36.86 - 35.67) / 35.67 * 100
	if diff := si.ChangePct - expect; diff > 0.01 || diff < -0.01 {
		t.Errorf("ChangePct 应≈%.4f, got %.4f", expect, si.ChangePct)
	}
}

// TestParseTHSQuotePrevCloseNaN 昨收异常（0 或比值越界）不应污染 Close/ChangePct。
func TestParseTHSQuotePrevCloseNaN(t *testing.T) {
	item := []interface{}{
		"", "hs_1.600580", "卧龙电驱", 35.00, 39.08, 34.67, 36.86, 127275327.0, 4558378206.0, 0.0,
	}
	body := []byte(`{"data":{"items":{"1":` + toJSONArray(item) + `}}}`)
	si, err := parseTHSQuote(body, "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	if si.Close != 0 || si.ChangePct != 0 {
		t.Errorf("昨收为0时不应推算, got Close=%.2f ChangePct=%.2f", si.Close, si.ChangePct)
	}
}