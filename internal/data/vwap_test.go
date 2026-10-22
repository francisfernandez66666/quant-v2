// vwap_test.go — §C2 VWAP 计算锁：Amount/Volume 缺失时弃权，不伪造成 0。
package data

import "testing"

func TestVWAPOf(t *testing.T) {
	if v, ok := VWAPOf(&StockInfo{Amount: 12_000_000, Volume: 1_000_000}); !ok || v != 12 {
		t.Fatalf("正常快照应得 VWAP=12, got %v ok=%v", v, ok)
	}
	if _, ok := VWAPOf(&StockInfo{Amount: 1000, Volume: 0}); ok {
		t.Fatal("Volume=0 应弃权")
	}
	if _, ok := VWAPOf(&StockInfo{Amount: 0, Volume: 1000}); ok {
		t.Fatal("Amount=0 应弃权")
	}
	if _, ok := VWAPOf(nil); ok {
		t.Fatal("nil 快照应弃权")
	}
}
