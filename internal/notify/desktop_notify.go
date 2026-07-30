package notify

import "log"

func PushDesktop(title, body string) {
	log.Printf("[桌面通知] %s: %s", title, body)
}
