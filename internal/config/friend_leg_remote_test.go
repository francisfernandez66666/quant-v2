// friend_leg_remote_test.go — §FRIEND-LEG（2026-11-02）远端执行腿观测键 `remote_leg` 的解码回归锁。
//
// 这个键的整个存在理由都是"缺省必须等于今天"：它只把一条腿标成"在远端机器上"，
// 从而让 Controller.FriendLegState 产出读数；不标就一个字节都不变。
// 因此本文件钉三端：
//  1. **缺键** = false（现网 config.json / 账号快照里从来没有这个键，这是 equals-legacy 的源头）；
//  2. 显式 true 才生效（读的是键本身，不是"没配就当开"那种出厂档——方向是加读数，缺省必须保守）；
//  3. false 时序列化**不带这个键**（omitempty）：配置回写/快照落盘不得把新键铺到每个账号上，
//     否则一次保存就让所有账号显式长出 remote_leg=false，与"没配"在字面上再也分不清
//     （§TOKEN-PERAACCT 那批"一次保存抄走凭证"同族的形状：写侧的扩散最终会读成一种状态）。
package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRemoteLegMissingKeyIsFalse 缺键必须解成 false。
func TestRemoteLegMissingKeyIsFalse(t *testing.T) {
	var q QMTConfig
	if err := json.Unmarshal([]byte(`{}`), &q); err != nil {
		t.Fatalf("解码空对象: %v", err)
	}
	if q.RemoteLegOn() {
		t.Fatal("缺 remote_leg 键必须判本机腿（false）——否则现网每个存量账号凭空多一条远端腿读数")
	}
	// 只配了别的键、没碰 remote_leg 的账号快照同样是 false。
	var partial QMTConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"gateway_url":"http://127.0.0.1:8789"}`), &partial); err != nil {
		t.Fatalf("解码部分对象: %v", err)
	}
	if partial.RemoteLegOn() {
		t.Fatal("部分配置里缺 remote_leg 仍应为 false")
	}
	// 出厂默认（DefaultQMTConfig）也必须 false：这条是"新行为挂开关"的装配端读数。
	if DefaultQMTConfig().RemoteLegOn() {
		t.Fatal("DefaultQMTConfig 必须给本机腿——出厂开等于给所有账号造新读数")
	}
}

// TestRemoteLegExplicitTrueDecodes 显式 true 才生效。
func TestRemoteLegExplicitTrueDecodes(t *testing.T) {
	var on QMTConfig
	if err := json.Unmarshal([]byte(`{"remote_leg":true}`), &on); err != nil {
		t.Fatalf("解码 remote_leg=true: %v", err)
	}
	if !on.RemoteLegOn() {
		t.Fatal("显式 remote_leg=true 必须读出远端腿（这条键唯一的开法）")
	}
	var off QMTConfig
	if err := json.Unmarshal([]byte(`{"remote_leg":false}`), &off); err != nil {
		t.Fatalf("解码 remote_leg=false: %v", err)
	}
	if off.RemoteLegOn() {
		t.Fatal("显式 false 当然也是本机腿")
	}
}

// TestRemoteLegOmittedWhenFalse false 不占序列化字节（equals-legacy 的写侧腿）。
func TestRemoteLegOmittedWhenFalse(t *testing.T) {
	b, err := json.Marshal(QMTConfig{Enabled: true})
	if err != nil {
		t.Fatalf("编码: %v", err)
	}
	if strings.Contains(string(b), "remote_leg") {
		t.Fatalf("缺省（false）形态的序列化里出现了 remote_leg 键——配置回写会把新键铺给每个账号：\n%s", b)
	}
	b2, err := json.Marshal(QMTConfig{Enabled: true, RemoteLeg: true})
	if err != nil {
		t.Fatalf("编码远端腿: %v", err)
	}
	if !strings.Contains(string(b2), `"remote_leg":true`) {
		t.Fatalf("true 形态必须落盘（否则重启后就丢了这是远端腿）：\n%s", b2)
	}
}
