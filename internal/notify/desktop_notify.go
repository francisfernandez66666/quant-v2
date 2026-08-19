// Package notify 桌面通知出口：目前仅记录日志（后续可接入系统通知/飞书等渠道）。
package notify

import "log"

// PushDesktop 发送桌面通知（当前仅记录日志）。（Sends a desktop notification; currently only logs.）
func PushDesktop(title, body string) {
	log.Printf("[桌面通知] %s: %s", title, body)
}
