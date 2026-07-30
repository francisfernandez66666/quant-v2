// Package data — 聚宽 (JoinQuant) 数据 API 客户端。
// 提供板块列表、成分股查询能力，作为东财/Tushare 之后的数据源降级。
// API 地址：https://dataapi.joinquant.com/apis
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JQClient 聚宽数据 API 客户端。
// 通过手机号+密码获取 token，调用各类数据接口。
type JQClient struct {
	client   *http.Client
	mobile   string
	password string
	mu       sync.RWMutex
	token    string
	tokenAt  time.Time
}

// NewJQClient 创建聚宽客户端。
// mobile/password 从环境变量或配置文件读取，可留空（此时所有方法不可用）。
func NewJQClient(mobile, password string) *JQClient {
	return &JQClient{
		client:   &http.Client{Timeout: 15 * time.Second},
		mobile:   mobile,
		password: password,
	}
}

// apiURL 聚宽数据 API 端点（v2，旧版 /apis 已停服）。
const jqAPIURL = "https://dataapi.joinquant.com/v2/apis"

// call 发送 POST JSON 请求到聚宽 API，返回响应体字符串。
func (jc *JQClient) call(params map[string]string) (string, error) {
	body, _ := json.Marshal(params)
	resp, err := jc.client.Post(jqAPIURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("jq http: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("jq read: %v", err)
	}
	return string(b), nil
}

// getToken 获取聚宽访问令牌，缓存 1 小时。
func (jc *JQClient) getToken() (string, error) {
	jc.mu.RLock()
	if jc.token != "" && time.Since(jc.tokenAt) < time.Hour {
		t := jc.token
		jc.mu.RUnlock()
		return t, nil
	}
	jc.mu.RUnlock()

	if jc.mobile == "" || jc.password == "" {
		return "", fmt.Errorf("jq: mobile/password not set")
	}

	resp, err := jc.call(map[string]string{
		"method": "get_token",
		"mob":    jc.mobile,
		"pwd":    jc.password,
	})
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(resp)
	if token == "" || len(token) < 10 {
		return "", fmt.Errorf("jq: invalid token response")
	}

	jc.mu.Lock()
	jc.token = token
	jc.tokenAt = time.Now()
	jc.mu.Unlock()
	return token, nil
}

// JQSectorInfo 聚宽行业板块信息。
type JQSectorInfo struct {
	Code string // 行业代码（如 "SW1"）
	Name string // 行业名称（如 "银行"）
}

// JQSectorStock 聚宽板块成分股。
type JQSectorStock struct {
	Code string // 股票代码（如 "000001"）
	Name string // 股票名称
}

// GetIndustries 获取申万行业列表。
func (jc *JQClient) GetIndustries() ([]JQSectorInfo, error) {
	token, err := jc.getToken()
	if err != nil {
		return nil, err
	}
	resp, err := jc.call(map[string]string{
		"method": "get_industries",
		"token":  token,
	})
	if err != nil {
		return nil, err
	}
	return parseJQCSV(resp)
}

// GetIndustryStocks 获取申万行业成分股。
// code 为行业代码（如 "SW1"），date 为日期（空则使用当日）。
func (jc *JQClient) GetIndustryStocks(code, date string) ([]JQSectorStock, error) {
	token, err := jc.getToken()
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"method": "get_industry_stocks",
		"token":  token,
		"code":   code,
	}
	if date != "" {
		params["date"] = date
	}
	resp, err := jc.call(params)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) < 2 {
		return nil, nil
	}
	// 第一行是表头，跳过
	var result []JQSectorStock
	for _, line := range lines[1:] {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		code := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(fields[0]), ".XSHE"), ".XSHG")
		code = strings.TrimSuffix(strings.TrimSuffix(code, ".SZ"), ".SH")
		name := ""
		if len(fields) > 1 {
			name = strings.TrimSpace(fields[1])
		}
		if code != "" {
			result = append(result, JQSectorStock{Code: code, Name: name})
		}
	}
	return result, nil
}

// parseJQCSV 解析聚宽 CSV 格式响应（首行表头，之后每行 code,name）。
func parseJQCSV(resp string) ([]JQSectorInfo, error) {
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) < 2 {
		return nil, nil
	}
	var result []JQSectorInfo
	for _, line := range lines[1:] {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		code := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[1])
		if code != "" && name != "" {
			result = append(result, JQSectorInfo{Code: code, Name: name})
		}
	}
	return result, nil
}
