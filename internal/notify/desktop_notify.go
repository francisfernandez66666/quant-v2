package notify

import "log"

// PushDesktop 发送桌面通知（当前仅记录日志）。（Sends a desktop notification; currently only logs.）
func PushDesktop(title, body string) {
	log.Printf("[桌面通知] %s: %s", title, body)
}
