// sector_map_test.go — §FLOW-SECTOR-JOIN 云端连接键的读取纪律（2026-11-20）。
//
// 为什么这条腿值得单独锁：广州对东财在 TLS 层被 RST，`GetStockIndustry`（f127 直连）恒返回空，
// 于是「个股 → 板块 delta」这条腿在云端**恒弃权而没有任何报错**——blend 静默 renormalize 到两项，
// 读面上看起来一切正常。修法是从生产者产出的 `sector_map` 里取连接键（同一份表载体也在用）。
//
// 本文件钉五件事：
//  1. 命中：6 位码直查、带 .SH/.SZ/.BJ 后缀同样命中（连接键必须归一，否则一半票静默 miss）；
//  2. **跨日整表弃权**（板块成分每天变，拿昨天的表配今天的 delta 会把钱贴到错的票上）；
//  3. 缺文件/坏 JSON/未装配 ⇒ 一律 false（弃权），绝不返回空串冒充"没有板块"；
//  4. 文件变了才重载（生产者每天一跑、进程跨天活）；
//  5. 非 6 位码 / 空名字在载入时就被剔掉（残行不许进表）。
package data

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSectorMap 造一份生产者格式的文件（day 用 YYYYMMDD，与 local_executor_sector_map.json 同形）。
func writeSectorMap(t *testing.T, path, day string, mapping map[string]string) {
	t.Helper()
	doc := map[string]interface{}{
		"day":          day,
		"generated_at": 1763600000,
		"count":        len(mapping),
		"sector_map":   mapping,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSectorMapLookupNormalizesCodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sector_map.json")
	writeSectorMap(t, path, "20261120", map[string]string{
		"600519": "酿酒行业",
		"000001": "银行",
		"300750": "电池",
	})
	c := NewSectorMapCache(path)

	for _, code := range []string{"600519", "600519.SH", "600519.sh", " 600519 "} {
		name, ok := c.Lookup(code, "20261120")
		if !ok || name != "酿酒行业" {
			t.Fatalf("code=%q got (%q,%v) want (酿酒行业,true)", code, name, ok)
		}
	}
	if name, ok := c.Lookup("300750.SZ", "20261120"); !ok || name != "电池" {
		t.Fatalf("SZ 后缀未归一: %q %v", name, ok)
	}
	if _, ok := c.Lookup("601988", "20261120"); ok {
		t.Fatal("表里没有的票必须 miss（不许猜一个板块出来）")
	}
	if _, ok := c.Lookup("", "20261120"); ok {
		t.Fatal("空代码必须 miss")
	}
}

// 跨日是这条腿最容易"看起来正常"的失败：文件在、格式对、命中率也高，只是昨天那份。
func TestSectorMapStaleDayAbstainsWholesale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sector_map.json")
	writeSectorMap(t, path, "20261119", map[string]string{"600519": "酿酒行业"})
	c := NewSectorMapCache(path)

	if name, ok := c.Lookup("600519", "20261120"); ok {
		t.Fatalf("昨日表不许留宿，却命中 %q", name)
	}
	if count, day, known := c.Stats("20261120"); known || count != 0 || day != "20261119" {
		t.Fatalf("Stats 应如实报不可用: count=%d day=%q known=%v", count, day, known)
	}
	// 同一天的表照常命中（跨日弃权是因为日期，不是因为文件"只能用一次"）。
	writeSectorMap(t, path, "20261120", map[string]string{"600519": "酿酒行业"})
	if name, ok := c.Lookup("600519", "20261120"); !ok || name != "酿酒行业" {
		t.Fatalf("当日表应命中，got %q %v", name, ok)
	}
}

func TestSectorMapMissingOrBrokenIsAbstain(t *testing.T) {
	dir := t.TempDir()

	// ① 未装配（path 为空）
	empty := NewSectorMapCache("")
	if _, ok := empty.Lookup("600519", "20261120"); ok {
		t.Fatal("未装配必须弃权")
	}
	// ② 文件不存在
	missing := NewSectorMapCache(filepath.Join(dir, "nope.json"))
	if _, ok := missing.Lookup("600519", "20261120"); ok {
		t.Fatal("文件不存在必须弃权")
	}
	// ③ 坏 JSON
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := NewSectorMapCache(broken)
	if _, ok := bc.Lookup("600519", "20261120"); ok {
		t.Fatal("坏 JSON 必须弃权（不许 panic、不许猜）")
	}
	// ④ nil 接收者不 panic
	var nilCache *SectorMapCache
	if _, ok := nilCache.Lookup("600519", "20261120"); ok {
		t.Fatal("nil cache 必须弃权")
	}
	if nilCache.Path() != "" {
		t.Fatal("nil cache 的 Path 应为空串")
	}
}

func TestSectorMapReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sector_map.json")
	writeSectorMap(t, path, "20261120", map[string]string{"600519": "旧板块"})
	c := NewSectorMapCache(path)
	if name, _ := c.Lookup("600519", "20261120"); name != "旧板块" {
		t.Fatalf("首读 got %q", name)
	}

	// 生产者重跑：同一只票换了板块（板块成分每天会动）。mtime 精度不足时用显式 touch 兜住。
	writeSectorMap(t, path, "20261120", map[string]string{"600519": "新板块"})
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if name, ok := c.Lookup("600519", "20261120"); !ok || name != "新板块" {
		t.Fatalf("文件变了应重载，got %q %v", name, ok)
	}
	if count, _, known := c.Stats("20261120"); !known || count != 1 {
		t.Fatalf("Stats 应反映新表: count=%d known=%v", count, known)
	}
}

func TestSectorMapDropsMalformedRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sector_map.json")
	writeSectorMap(t, path, "20261120", map[string]string{
		"600519": "酿酒行业",
		"60051":  "五位数不是票",
		"60051X": "带字母不是票",
		"000001": "",
	})
	c := NewSectorMapCache(path)
	if count, _, known := c.Stats("20261120"); !known || count != 1 {
		t.Fatalf("残行必须被剔除: count=%d known=%v", count, known)
	}
	if _, ok := c.Lookup("60051", "20261120"); ok {
		t.Fatal("5 位码不该进表")
	}
	if _, ok := c.Lookup("000001", "20261120"); ok {
		t.Fatal("空板块名不该进表")
	}
}
