package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zenui/internal/zen/lane"
	"zenui/internal/zen/store"
)

// The dashboard fence has two halves. The loopback half is the original one and
// must keep behaving exactly as it always did (admin_fence_test.go pins that).
// The remote half only exists for a host with no loopback transport — the
// container entry point — and is off unless the host turns it on, in which case
// the main key stands in for "this caller is already on the machine".

func newFenceServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("ZEN_GATE_HOME", t.TempDir())
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	return New(lane.NewLane(), st)
}

func TestRemoteAdminRefusedByDefault(t *testing.T) {
	s := newFenceServer(t)
	key := s.Store.Config().MainKey

	r := httptest.NewRequest("GET", "http://192.168.1.9:8787/admin/api/state", nil)
	r.Host = "192.168.1.9:8787"
	r.RemoteAddr = "172.17.0.1:41234"
	r.Header.Set("Authorization", "Bearer "+key)

	w := httptest.NewRecorder()
	if s.adminFence(w, r, false) {
		t.Fatal("a remote caller was accepted while the opt-in was off")
	}
	if w.Code != 403 || !strings.Contains(w.Body.String(), "loopback-only") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRemoteAdminRequiresMainKey(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)
	mainKey := s.Store.Config().MainKey

	cases := []struct{ name, key string }{
		{"no-key", ""},
		{"wrong-key", "ofm-not-the-main-key"},
		{"key-prefix", mainKey[:8]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://192.168.1.9:8787/admin/api/state", nil)
			r.Host = "192.168.1.9:8787"
			r.RemoteAddr = "172.17.0.1:41234"
			if tc.key != "" {
				r.Header.Set("Authorization", "Bearer "+tc.key)
			}
			w := httptest.NewRecorder()
			if s.adminFence(w, r, false) {
				t.Fatalf("accepted key %q", tc.key)
			}
			if w.Code != 403 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}

func TestRemoteAdminAcceptsMainKey(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)

	for _, header := range []string{"Authorization", "X-Api-Key"} {
		t.Run(header, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://192.168.1.9:8787/admin/api/state", nil)
			r.Host = "192.168.1.9:8787"
			r.RemoteAddr = "172.17.0.1:41234"
			if header == "Authorization" {
				r.Header.Set(header, "Bearer "+s.Store.Config().MainKey)
			} else {
				r.Header.Set(header, s.Store.Config().MainKey)
			}
			w := httptest.NewRecorder()
			if !s.adminFence(w, r, false) {
				t.Fatalf("main key rejected: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// A keyed remote caller is authenticated, not trusted: the cross-site rule on
// mutations is unchanged, so a page on another origin still cannot drive it.
func TestRemoteAdminKeepsSameOriginRule(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)
	key := s.Store.Config().MainKey

	build := func(origin string) *http.Request {
		r := httptest.NewRequest("POST", "http://192.168.1.9:8787/admin/api/settings", strings.NewReader("{}"))
		r.Host = "192.168.1.9:8787"
		r.RemoteAddr = "172.17.0.1:41234"
		r.Header.Set("Authorization", "Bearer "+key)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	for _, origin := range []string{"http://evil.invalid", "http://192.168.1.9:9999", "https://192.168.1.9:8787"} {
		w := httptest.NewRecorder()
		if s.adminFence(w, build(origin), true) {
			t.Fatalf("cross-site mutation accepted with origin %q", origin)
		}
		if w.Code != 403 || !strings.Contains(w.Body.String(), "cross-site") {
			t.Fatalf("origin %q: status=%d body=%s", origin, w.Code, w.Body.String())
		}
	}

	for _, origin := range []string{"", "http://192.168.1.9:8787"} {
		w := httptest.NewRecorder()
		if !s.adminFence(w, build(origin), true) {
			t.Fatalf("same-origin mutation rejected with origin %q: status=%d body=%s", origin, w.Code, w.Body.String())
		}
	}
}

// The opt-in must not loosen the loopback path: a loopback peer addressing a
// non-loopback Host is still the DNS-rebinding case and is still refused.
func TestLoopbackPathUnaffectedByRemoteOptIn(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)

	r := httptest.NewRequest("GET", "http://review.invalid/admin/api/state", nil)
	r.Host = "review.invalid"
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("Authorization", "Bearer "+s.Store.Config().MainKey)

	w := httptest.NewRecorder()
	if s.adminFence(w, r, false) {
		t.Fatal("loopback path accepted a non-loopback Host")
	}

	// ...and a loopback caller still gets in without any key, as always.
	ok := httptest.NewRequest("GET", "http://127.0.0.1:8787/admin/api/state", nil)
	ok.Host = "127.0.0.1:8787"
	ok.RemoteAddr = "127.0.0.1:54321"
	if w := httptest.NewRecorder(); !s.adminFence(w, ok, false) {
		t.Fatalf("loopback caller rejected: status=%d body=%s", w.Code, w.Body.String())
	}
}

// 容器里没有回环链路：运维打开端口后，浏览器打开 /admin 得不到 Bearer 头，
// 所以这页静态说明必须能看；而它下面的数据接口仍然只认主密钥。
func TestRemoteDashboardPageFollowsOptIn(t *testing.T) {
	s := newFenceServer(t)

	remote := func() *http.Request {
		r := httptest.NewRequest("GET", "http://192.168.0.10:8787/admin", nil)
		r.Host = "192.168.0.10:8787"
		r.RemoteAddr = "172.17.0.1:41234"
		return r
	}

	w := httptest.NewRecorder()
	if s.dashboardFence(w, remote()) {
		t.Fatalf("默认配置下远端不该拿到面板页: status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Code != 403 {
		t.Fatalf("期望 403，实际 %d", w.Code)
	}

	s.SetAdminKeyRemote(true)
	if w := httptest.NewRecorder(); !s.dashboardFence(w, remote()) {
		t.Fatalf("打开远端管理后，远端仍被挡在说明页之外: status=%d body=%s", w.Code, w.Body.String())
	}

	// 说明页放行不等于数据接口放行：同一个远端请求打 /admin/api 仍然要主密钥。
	w = httptest.NewRecorder()
	if s.adminFence(w, remote(), false) {
		t.Fatal("说明页放行后，数据接口也不该放行")
	}
	if w.Code != 403 || !strings.Contains(w.Body.String(), "requires the main API key") {
		t.Fatalf("期望 403 且提示需要主密钥，实际 status=%d body=%s", w.Code, w.Body.String())
	}
}
