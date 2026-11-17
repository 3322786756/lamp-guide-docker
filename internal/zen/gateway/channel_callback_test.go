package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"zenui/internal/zen/chprov"
)

// TestChannelCallbackURI 钉住「谁来接 PKCE 回调」这条判断。
//
// 回环访问 → 空串（桌面版照旧起本机监听）；远端访问 → 浏览器现在用的地址；
// Google 例外（它的桌面客户端只登记了 127.0.0.1 回环回调）。
func TestChannelCallbackURI(t *testing.T) {
	trae, ok := chprov.SpecOf("trae")
	if !ok {
		t.Fatal("描述表里没有 trae")
	}
	gemini, ok := chprov.SpecOf("gemini")
	if !ok {
		t.Fatal("描述表里没有 gemini")
	}

	remote := httptest.NewRequest("POST", "http://192.168.0.3:8787/admin/api/channel/login", nil)
	remote.RemoteAddr = "192.168.0.14:51234"
	if got := channelCallbackURI(remote, trae); got != "http://192.168.0.3:8787/channel/callback/trae" {
		t.Fatalf("远端访问时回调应指到网关自己, 实际 %q", got)
	}
	if got := channelCallbackURI(remote, gemini); got != "" {
		t.Fatalf("Google 仍要走本机回环, 实际 %q", got)
	}

	local := httptest.NewRequest("POST", "http://127.0.0.1:8787/admin/api/channel/login", nil)
	local.RemoteAddr = "127.0.0.1:51234"
	if got := channelCallbackURI(local, trae); got != "" {
		t.Fatalf("回环访问不该改回调地址, 实际 %q", got)
	}
}

// TestChannelCallbackRouteIsPublicButStateGated：回调路径不过 adminFence
// （浏览器顶层导航带不了头），但没有会话 / state 对不上就什么都不会发生。
func TestChannelCallbackRouteIsPublicButStateGated(t *testing.T) {
	s := newFenceServer(t)

	req := httptest.NewRequest("GET", "http://192.168.0.3:8787/channel/callback/trae?state=whatever&code=the-code", nil)
	req.RemoteAddr = "192.168.0.14:51234"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("没有进行中的登录时应当 400, 实际 %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("回调应该给人看一页 HTML, content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "没有正在进行的登录") {
		t.Fatalf("文案要说清原因: %s", rec.Body.String())
	}
}

// TestChannelCallbackUnknownChannel：路径里的 id 也要校验，不能凭空造会话。
func TestChannelCallbackUnknownChannel(t *testing.T) {
	s := newFenceServer(t)
	req := httptest.NewRequest("GET", "http://192.168.0.3:8787/channel/callback/not-a-channel?state=x&code=y", nil)
	req.RemoteAddr = "192.168.0.14:51234"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("未知渠道应当 404, 实际 %d", rec.Code)
	}
}
