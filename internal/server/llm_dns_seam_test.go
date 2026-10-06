// §P2-K（2026-10-07 修复批 波 4）：外呼地址校验（SSRF 闸）的 DNS 注入点行为证据。
//
// 这个文件存在的理由是把两件事从「形状」变成「读数」：
//
//  1. **矩阵不再钉在这台机器的 DNS 上**。以前 llm 热更新用例的正例路径要真去解析
//     api.siliconflow.cn，断网或解析抖动就判红——10-05 全量审计实录：同一份代码第二轮
//     `lookup api.siliconflow.cn: no such host` 判红、一分钟后单跑 PASS 17.99s。红来自环境
//     而不是来自被测代码，这种红会把真缺陷埋掉。TestMain 装上 hermeticResolver 之后，
//     域名解析结果由测试自己给，出不出网结论都不变。
//  2. **SSRF 判据第一次有行为证据**。「拒绝内网/保留地址」「解析失败即拒」两条分支以前在测试
//     里根本不可达（要可达就得真解析出一个内网地址），于是安全阀只有注释、没有读数。现在逐类
//     注入地址并断言**拒绝文案的归属**：环回/私网/链路本地（含云元数据 169.254.169.254）/
//     未指定/组播 ⇒ 「禁止指向内网/保留地址」；解析错误 ⇒ 「主机解析失败」（fail-closed）；
//     公网 ⇒ 通过。谁把调用点改回裸 net.LookupIP，注入的答案就送不到判据里，这些用例当场判红。
//
// English: behavioural evidence for the outbound-URL SSRF gate. The matrix no longer depends on
// this machine's resolver, and every reserved-address / unresolvable branch is now reachable and
// attributable through the injected seam.
package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
)

// hermeticStubIP 是「解析成功」时返回的公网地址（example.com 的历史公开 IP，只作夹具，不拨它）。
// 取值本身不重要，重要的是它**不属于**任何被拒类别：一旦换成 127.0.0.1，
// 正例腿会立刻判红，等于把「公网必须放行」这条也钉住了。
const hermeticStubIP = "93.184.216.34"

// hermeticResolver 是 TestMain 装进 llmURLResolver 的确定性解析桩（§P2-K）。
//
// 三条语义，一条比一条容易被忽略，所以逐条写清：
//   - 字面 IP 原样返回（**不伪装成公网**）：127.0.0.1 这类地址走真实 LookupIP 时就是本机解析、
//     不出网，桩照此返回才能保住「填内网地址被拒」这条既有用例的语义不被洗绿。
//   - `.invalid`（RFC 2606 保留 TLD，真 DNS 必然 NXDOMAIN）返回解析错误：让 fail-closed 分支
//     在**不改代码、不注入**的情况下也能被 HTTP 入口测到。
//   - 其余主机名一律返回 hermeticStubIP：正例路径与本机 DNS 彻底脱钩。
//
// English: literal IPs pass through untouched, .invalid fails to resolve, everything else maps
// to one public fixture address — so the matrix is offline-deterministic.
func hermeticResolver(host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if strings.HasSuffix(strings.ToLower(host), ".invalid") {
		return nil, &net.DNSError{Err: "no such host（hermetic 桩，未出网）", Name: host, IsNotFound: true}
	}
	return []net.IP{net.ParseIP(hermeticStubIP)}, nil
}

// stubResolver 本次用例改用给定解析结果，结束自动还原为 hermeticResolver。
// answers 为 nil 表示「解析失败」（返回 error，而不是空结果——空结果与失败在语义上不同：
// 空结果会被 for 循环跳过＝放行，正是这个闸最不该有的形态）。
// English: per-test resolver stub with automatic restore.
func stubResolver(t *testing.T, ipStrings []string, resolveErr error) {
	t.Helper()
	orig := llmURLResolver
	llmURLResolver = func(_ string) ([]net.IP, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		ips := make([]net.IP, 0, len(ipStrings))
		for _, s := range ipStrings {
			ip := net.ParseIP(s)
			if ip == nil {
				// 夹具写错地址时**必须炸**：静默少一个 IP 会让「多地址里藏一个内网」这类腿变成空跑。
				t.Fatalf("解析桩夹具里的地址不合法: %q", s)
			}
			ips = append(ips, ip)
		}
		return ips, nil
	}
	t.Cleanup(func() { llmURLResolver = orig })
}

// TestLLMURLResolverSeamIsHermetic 证明整包默认拿到的解析结果与本机 DNS 无关（I1 断网腿的等价形态）。
//
// 为什么这条能当「断网必绿」的证据：断言的是**桩的真值**（域名→93.184.216.34、.invalid→失败）。
// 若有人把 TestMain 的装配删掉、退回真实 net.LookupIP，同一个主机名要么返回别的地址、要么直接
// 报错——两种情况都判红，不需要制造断网就能复现「矩阵挂了机器 DNS」这件事。
// English: the package default resolver is the hermetic stub; removing the TestMain wiring makes
// this red without needing a fake offline environment.
func TestLLMURLResolverSeamIsHermetic(t *testing.T) {
	for _, host := range []string{"api.siliconflow.cn", "example.com", "vendor.example"} {
		ips, err := llmURLResolver(host)
		if err != nil {
			t.Fatalf("域名 %q 不该依赖真实 DNS，却解析失败: %v（TestMain 的 hermeticResolver 没装上？）", host, err)
		}
		if len(ips) != 1 || ips[0].String() != hermeticStubIP {
			t.Fatalf("域名 %q 应命中 hermetic 桩地址 %s, got %v（真值变了说明解析走的是本机 DNS）", host, hermeticStubIP, ips)
		}
	}
	if _, err := llmURLResolver("nope.invalid"); err == nil {
		t.Fatal(".invalid 保留域应判解析失败（当前却返回成功＝fail-closed 分支再没有读数）")
	}
	ips, err := llmURLResolver("127.0.0.1")
	if err != nil || len(ips) != 1 || !ips[0].IsLoopback() {
		t.Fatalf("字面 IP 必须原样返回（伪装成公网会把『填内网地址被拒』这条洗绿）, got %v err=%v", ips, err)
	}
}

// TestValidatePublicURLReservedAddressAttribution 逐类保留地址断言**拒绝文案的归属**（SSRF 行为矩阵）。
func TestValidatePublicURLReservedAddressAttribution(t *testing.T) {
	cases := []struct {
		name string
		ips  []string
		want string // 必须出现
	}{
		{"环回 127.0.0.1", []string{"127.0.0.1"}, "禁止指向内网/保留地址"},
		{"环回 IPv6 ::1", []string{"::1"}, "禁止指向内网/保留地址"},
		{"私网 10.x", []string{"10.0.0.1"}, "禁止指向内网/保留地址"},
		{"私网 192.168.x", []string{"192.168.1.20"}, "禁止指向内网/保留地址"},
		{"云元数据 169.254.169.254", []string{"169.254.169.254"}, "禁止指向内网/保留地址"},
		{"未指定 0.0.0.0", []string{"0.0.0.0"}, "禁止指向内网/保留地址"},
		{"组播 224.0.0.1", []string{"224.0.0.1"}, "禁止指向内网/保留地址"},
		// 多地址里藏一个内网：判据是逐 IP 检查（不是"第一个"或"平均"），这条钉住"逐"字。
		{"公网+内网混合仍拒", []string{hermeticStubIP, "10.1.2.3"}, "禁止指向内网/保留地址"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubResolver(t, c.ips, nil)
			err := validatePublicURL("https://vendor.example/v1/chat/completions")
			if err == nil {
				t.Fatalf("%v 必须被拒（当前放行＝闸失效）", c.ips)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("拒绝文案归属错：应含 %q, got %q", c.want, err.Error())
			}
			// 反向归属：内网拒绝不得被写成"解析失败"那套话术（两分支混淆＝谁坏都看不出来）。
			if strings.Contains(err.Error(), "主机解析失败") {
				t.Fatalf("保留地址应走『拒绝内网/保留地址』分支，却报成解析失败: %q", err.Error())
			}
		})
	}
}

// TestValidatePublicURLResolvesPublicAndErrors 正例 + fail-closed + 非法形状三条腿。
func TestValidatePublicURLResolvesPublicAndErrors(t *testing.T) {
	t.Run("公网放行", func(t *testing.T) {
		stubResolver(t, []string{hermeticStubIP}, nil)
		if err := validatePublicURL("https://vendor.example/v1/chat/completions"); err != nil {
			t.Fatalf("公网地址不该被拒: %v", err)
		}
	})
	t.Run("解析失败一律拒（fail-closed）", func(t *testing.T) {
		stubResolver(t, nil, fmt.Errorf("dial up: no such host"))
		err := validatePublicURL("https://vendor.example/v1/chat/completions")
		if err == nil || !strings.Contains(err.Error(), "主机解析失败") {
			t.Fatalf("解析失败必须拒绝且报『主机解析失败』, got %v", err)
		}
	})
	t.Run("解析成功但零地址也拒", func(t *testing.T) {
		// 空结果是本闸最阴的形态：for 循环一次不走＝放行。桩夹具给空切片，判据必须仍然拒。
		// （实现侧靠"零地址放行"吗？不——见 server.go 里 len(ips)==0 的守卫；这条腿就是它的读数。）
		stubResolver(t, []string{}, nil)
		err := validatePublicURL("https://vendor.example/v1/chat/completions")
		if err == nil {
			t.Fatal("解析结果为空却放行＝把『查不到』当『查过了、干净』，必须拒")
		}
	})
	t.Run("非 http/https 协议与缺主机名", func(t *testing.T) {
		if err := validatePublicURL("ftp://vendor.example/x"); err == nil || !strings.Contains(err.Error(), "仅允许 http/https") {
			t.Fatalf("ftp 应被协议白名单拦下, got %v", err)
		}
		if err := validatePublicURL("https:///v1/chat"); err == nil || !strings.Contains(err.Error(), "缺少主机名") {
			t.Fatalf("缺主机名应被拦下, got %v", err)
		}
	})
}

// TestValidatePublicURLUsesTheSeamNotRealDNS 判据归属锁：注入的答案必须真的送到闸口。
//
// 这条是「谁把调用点改回裸 net.LookupIP 谁就红」的那枚：桩对**任意**主机名都返回一个私网地址
// （真实 DNS 对 vendor.example 不可能返回 10.0.0.1）。若实现绕过注入点直连真实解析，
// 这条要么报解析失败、要么干脆放行，两种都会判红——拒绝文案必须精确到「禁止指向内网/保留地址」。
// English: proves the gate actually consumes the injected seam (real DNS can never answer 10.0.0.1
// for this host), so reverting to a bare net.LookupIP call at the site turns this red.
func TestValidatePublicURLUsesTheSeamNotRealDNS(t *testing.T) {
	stubResolver(t, []string{"10.0.0.1"}, nil)
	err := validatePublicURL("https://api.siliconflow.cn/v1/chat/completions")
	if err == nil || !strings.Contains(err.Error(), "禁止指向内网/保留地址") {
		t.Fatalf("注入点的答案没送到闸口（应报『禁止指向内网/保留地址』）, got %v——十有八九是调用点改回裸 net.LookupIP 了", err)
	}
}

// TestSetLLMConfigRejectsReservedAddressThroughHTTP 接线腿：SSRF 拒绝必须真的从 HTTP 入口出来。
//
// 为什么单独立一条而不是只测函数：闸住在 llm_apply.go:192 的 prepareLLMCandidate 里，
// 上游还有「api_url 留空即保持原值」的合并、下游有探测/落库。只测 validatePublicURL 的话，
// 有人把 handler 里那行删了、或把错误码从 400 改成 200，函数单测一行都不会红。
// 夹具刻意用一个公网形态的供应商域名 + 注入内网地址：走的是真实 DNS 永远走不到的分支，
// 于是这条既证明接线，又不依赖网络。
// English: end-to-end leg — the SSRF refusal must actually surface as 400 through the route,
// which the unit test alone cannot prove.
func TestSetLLMConfigRejectsReservedAddressThroughHTTP(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t)
	stubResolver(t, []string{"169.254.169.254"}, nil)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-ssrf-leg"],"api_url":"https://vendor.example/v1/chat/completions","model":"m"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("内网地址应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "禁止指向内网/保留地址") {
		t.Fatalf("400 的理由必须是 SSRF 拒绝（换成别的拒绝＝闸被别的前置校验挡住了，读数不成立）: %s", rr.Body.String())
	}
	if cap.calls != 0 {
		t.Fatalf("被拒配置绝不该触发热切换, got calls=%d", cap.calls)
	}
	// 磁盘也不动：拒绝发生在落库之前（"当时没坏、重启后彻底起不来"的形态就出自这里）。
	if got := s.cfg.GetLLMConfigFor(admin.ID).APIURL; got != "" {
		t.Fatalf("SSRF 拒绝后不该落库任何 api_url, got %q", got)
	}
}

// TestSetLLMConfigResolvesUnresolvableHostThroughHTTP fail-closed 的接线腿（.invalid 走 hermetic 桩语义）。
func TestSetLLMConfigResolvesUnresolvableHostThroughHTTP(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-nxdomain"],"api_url":"https://definitely-not-real.invalid/v1/chat/completions","model":"m"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("解析不了的地址应 400（fail-closed）, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "主机解析失败") {
		t.Fatalf("400 的理由应是解析失败, got %s", rr.Body.String())
	}
	if cap.calls != 0 {
		t.Fatalf("解析失败不该热切换, got calls=%d", cap.calls)
	}
}
