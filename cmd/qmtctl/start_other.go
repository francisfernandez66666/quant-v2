//go:build !windows

// 非 Windows：MiniQMT 为 Windows 专有客户端，本平台仅做编译兼容（startMiniQMT 不会被实际调用）。
package main

import "os/exec"

// setDetached 非 Windows 平台空实现：MiniQMT 为 Windows 专有客户端，本平台仅做编译兼容，
// 不设置任何进程属性（startMiniQMT 实际不会在非 Windows 平台被调用）。
func setDetached(cmd *exec.Cmd) {}
