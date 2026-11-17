package chprov

import (
	"net/url"
	"strings"
	"testing"
)

// reviewPKCE 是一个只存在于测试里的 PKCE 描述表：不联网、不需要设备标识。
func reviewPKCE(id string) Spec {
	return Spec{ID: id, Login: LoginSpec{
		Kind:         LoginPKCE,
		AuthURL:      "https://example.test/authorization",
		ExchangeURL:  "https://example.test/token",
		CallbackPath: "/auth/callback",
	}}
}

// TestPKCEHostedCallbackSkipsTheLoopbackListener 钉住容器那条路：
// 给了回调地址就不该再起本机监听，否则浏览器在别的机器上仍然跳不回来。
func TestPKCEHostedCallbackSkipsTheLoopbackListener(t *testing.T) {
	spec := reviewPKCE("review-pkce-hosted")
	base := "http://192.168.0.3:8787/channel/callback/review-pkce-hosted"
	sess, err := startPKCE(spec, base)
	if err != nil {
		t.Fatalf("开 PKCE 失败: %v", err)
	}
	defer Forget(spec.ID)
	if sess.RedirectURI != base {
		t.Fatalf("回调地址应是给进来的那个, 实际 %q", sess.RedirectURI)
	}
	if sess.LocalPort != 0 {
		t.Fatalf("改用网关地址后不该还占着回环端口, LocalPort=%d", sess.LocalPort)
	}
	// 授权页里必须带着这个地址：拼进去的是 URL 编码后的形态。
	if !strings.Contains(sess.OpenURL, url.QueryEscape(base)) {
		t.Fatalf("授权页没带上回调地址: %s", sess.OpenURL)
	}
}

// TestPKCEKeepsTheLoopbackListenerByDefault 钉住桌面版那条路不变。
func TestPKCEKeepsTheLoopbackListenerByDefault(t *testing.T) {
	spec := reviewPKCE("review-pkce-loopback")
	sess, err := startPKCE(spec, "")
	if err != nil {
		t.Fatalf("开 PKCE 失败: %v", err)
	}
	defer Forget(spec.ID)
	if sess.LocalPort == 0 {
		t.Fatal("没给回调地址时应当起本机回环监听")
	}
	if !strings.HasPrefix(sess.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("默认回调应指向本机回环, 实际 %q", sess.RedirectURI)
	}
	if !strings.HasSuffix(sess.RedirectURI, spec.Login.CallbackPath) {
		t.Fatalf("回调路径不对: %q", sess.RedirectURI)
	}
}

// TestAcceptCallbackWritesTheSlot 是回调写槽位的正向与反向两条。
func TestAcceptCallbackWritesTheSlot(t *testing.T) {
	spec := reviewPKCE("review-pkce-slot")
	sess, err := startPKCE(spec, "http://192.168.0.3:8787/channel/callback/review-pkce-slot")
	if err != nil {
		t.Fatalf("开 PKCE 失败: %v", err)
	}
	defer Forget(spec.ID)

	if err := AcceptCallback(spec.ID, "not-the-state", "code-1"); err == nil {
		t.Fatal("state 对不上时不该写进槽位")
	}
	if err := AcceptCallback(spec.ID, sess.State, "code-1"); err != nil {
		t.Fatalf("正常回调被拒: %v", err)
	}
	codeMu.Lock()
	got := codeTab[spec.ID+"|"+sess.State]
	codeMu.Unlock()
	if got != "code-1" {
		t.Fatalf("槽位里应是 code-1, 实际 %q", got)
	}
}

// TestChannelCallbackReportsAnUpstreamRejection 是「用户点了拒绝」那条：
// 状态码 400、文案带原因，且槽位里留一个前缀 "!" 让轮询翻译成错误。
func TestChannelCallbackReportsAnUpstreamRejection(t *testing.T) {
	spec := reviewPKCE("review-pkce-reject")
	sess, err := startPKCE(spec, "http://192.168.0.3:8787/channel/callback/review-pkce-reject")
	if err != nil {
		t.Fatalf("开 PKCE 失败: %v", err)
	}
	defer Forget(spec.ID)

	status, msg := ChannelCallback(spec.ID, url.Values{"state": {sess.State}, "error": {"access_denied"}})
	if status != 400 {
		t.Fatalf("被拒绝的回调应当 400, 实际 %d", status)
	}
	if !strings.Contains(msg, "access_denied") {
		t.Fatalf("文案里应带上游给的原因: %q", msg)
	}
	codeMu.Lock()
	got := codeTab[spec.ID+"|"+sess.State]
	codeMu.Unlock()
	if got != "!access_denied" {
		t.Fatalf("槽位应以 ! 前缀记下拒绝原因, 实际 %q", got)
	}
}

// TestChannelCallbackWithoutSession 是「回调打过来但没有登录在进行」。
func TestChannelCallbackWithoutSession(t *testing.T) {
	status, msg := ChannelCallback("review-pkce-nobody", url.Values{"state": {"x"}, "code": {"c"}})
	if status != 400 {
		t.Fatalf("没有会话时应当 400, 实际 %d", status)
	}
	if !strings.Contains(msg, "没有正在进行的登录") {
		t.Fatalf("文案要说清是没有进行中的登录: %q", msg)
	}
}

// TestChannelCallbackNeedsACode 钉住「只有 state 没有 code」这种半截回调。
func TestChannelCallbackNeedsACode(t *testing.T) {
	spec := reviewPKCE("review-pkce-nocode")
	sess, err := startPKCE(spec, "http://192.168.0.3:8787/channel/callback/review-pkce-nocode")
	if err != nil {
		t.Fatalf("开 PKCE 失败: %v", err)
	}
	defer Forget(spec.ID)

	status, msg := ChannelCallback(spec.ID, url.Values{"state": {sess.State}})
	if status != 400 || !strings.Contains(msg, "code") {
		t.Fatalf("缺 code 的回调应当 400 并说明, 实际 %d %q", status, msg)
	}
}
