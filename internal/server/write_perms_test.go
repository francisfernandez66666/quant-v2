// write_perms_test.go — §M-14（2026-09-22 PM 批清扫）写端点收权防回潮全量普查锁。
//
// 旧 h3_perms_test.go 只锤 3 条已知越权端点（审计低危项：90 条写端点仅锁 3 条），
// 本测试把口径升级为「全量普查」：解析本包全部非测试源文件中的 mux.HandleFunc 注册，
// 任何 POST/PUT/DELETE/PATCH 写路由必须满足二选一——
//
//	① 处理函数被任一 *Middleware 包裹（auth/admin/perm 身份闸，或 qmtReportMiddleware
//	这类自带凭证校验的专用闸）；
//	② 路径在引导白名单内（注册/临时票/登录/初始化——匿名可达是本就必要的功能语义）。
//
// 新增写端点忘了挂闸会在 CI 直接红，杜绝「下一个 test-attribution」。
// English: §M-14 — full census lock: every write route in the server package must be wrapped
// by an auth/admin/perm middleware or appear on the bootstrap allowlist; an ungated new write
// endpoint fails CI. (Upgrades the old 3-of-90 spot checks to exhaustive.)
package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeRouteRe 匹配 `mux.HandleFunc("POST /path", <首个标识符>` 形式的注册行。
var writeRouteRe = regexp.MustCompile(`mux\.HandleFunc\("(POST|PUT|DELETE|PATCH) ([^"]+)",\s*(?:s\.)?([A-Za-z_][A-Za-z0-9_]*)`)

// gatedWrapperRe 判断注册行是否挂了某种中间件包裹（authMiddleware/adminMiddleware/
// permMiddleware 身份闸，或 qmtReportMiddleware 这类自带凭证校验的专用闸）。
var gatedWrapperRe = regexp.MustCompile(`[A-Za-z]+Middleware\(`)

// writeBootstrapAllowlist 匿名可达的引导类写端点（无登录态时的唯一入口），逐条注明理由。
var writeBootstrapAllowlist = map[string]string{
	"POST /auth/register":  "账号注册引导（限流+邀请码/初始化态约束在处理器内）",
	"POST /auth/temp":      "临时票据兑换（本身即登录引导链一环）",
	"POST /auth/login":     "登录（旧路径别名，壳端兼容保留）",
	"POST /api/auth/login": "登录（规范路径）",
	"POST /setup":          "一次性初始化（仅未初始化状态可用，处理器内自守）",
}

func TestWriteEndpointsAllGated(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	total := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			// 注释行不参与普查（旧写法留档说明不得误报）。
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
				continue
			}
			m := writeRouteRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			total++
			route := m[1] + " " + m[2]
			if _, ok := writeBootstrapAllowlist[route]; ok {
				if gatedWrapperRe.MatchString(line) {
					t.Fatalf("引导白名单端点 %s（%s）竟带了身份闸：白名单需要同步更新", route, name)
				}
				continue
			}
			if !gatedWrapperRe.MatchString(line) {
				t.Fatalf("§M-14 防回潮：写端点 %s 未挂 auth/admin/perm 闸（%s）：%s", route, name, line)
			}
		}
	}
	// 普查必须真的看到规模（防正则失配导致全绿的假阴性）。
	if total < 70 {
		t.Fatalf("写端点普查数量异常（total=%d < 70），正则可能失配——先修测试再谈通过", total)
	}
}
