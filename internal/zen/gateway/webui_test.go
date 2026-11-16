package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zenui/internal/zen/store"
)

// 容器里没有 WebView2，界面得靠浏览器跑。这一组测试钉住三件事：
// 回环调用照旧直接进界面；远端必须先用主密钥换会话；导出/导入这类只有桌面才做
// 得成的动作，在浏览器里有等价路径（下载令牌 / 上传后按服务端路径读）。

// remoteUIRequest 造一个"端口发布到公网"形状的请求：地址不是 127.0.0.1。
func remoteUIRequest(method, target, body string) *http.Request {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var r *http.Request
	if rdr != nil {
		r = httptest.NewRequest(method, target, rdr)
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Host = "192.168.1.9:8787"
	r.RemoteAddr = "172.17.0.1:41234"
	if body != "" {
		r.Header.Set("content-type", "application/json")
	}
	return r
}

func loopbackUIRequest(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("content-type", "application/json")
	}
	r.RemoteAddr = "127.0.0.1:50000"
	return r
}

// do 跑一次真实的路由（mux → safeRoute → route），而不是直接调某个 handler。
func do(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// loginCookie 走一遍登录，回会话 cookie。
func loginCookie(t *testing.T, s *Server, key string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	w := do(s, remoteUIRequest("POST", "http://192.168.1.9:8787/admin/login", string(body)))
	if w.Code != 200 {
		t.Fatalf("登录失败 status=%d body=%s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("登录成功却没有下发会话 cookie")
	return nil
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request {
	r.AddCookie(c)
	return r
}

func TestBrowserUILoopbackGoesStraightIn(t *testing.T) {
	s := newFenceServer(t)

	// 本机打开 /admin：不用登录，直接跳界面目录。
	w := do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787/admin", ""))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("location"); loc != "/admin/ui/" {
		t.Fatalf("location=%q", loc)
	}

	w = do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787/admin/ui/", ""))
	if w.Code != 200 {
		t.Fatalf("界面首页 status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("content-type"), "text/html") {
		t.Fatalf("content-type=%q", w.Header().Get("content-type"))
	}
	if !strings.Contains(w.Body.String(), "Lamp Guide") {
		t.Fatal("首页不是管理界面")
	}
	if !strings.Contains(w.Body.String(), "webbridge.js") {
		t.Fatal("首页没有挂浏览器桥，容器里页面会收不到回包")
	}

	// 静态资源：桥本身、以及页面真正用的那份 shell。
	for _, asset := range []string{"/admin/ui/js/webbridge.js", "/admin/ui/js/shell.js", "/admin/ui/styles/tokens.css"} {
		w = do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787"+asset, ""))
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s status=%d 长度=%d", asset, w.Code, w.Body.Len())
		}
	}
}

func TestBrowserUIWithoutTrailingSlashRedirects(t *testing.T) {
	s := newFenceServer(t)
	w := do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787/admin/ui", ""))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("location") != "/admin/ui/" {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("location"))
	}
}

func TestBrowserUIRemoteNeedsLogin(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)
	key := s.Store.Config().MainKey

	// 1) 没登录：给登录页，不是 403，也不是界面。
	w := do(s, remoteUIRequest("GET", "http://192.168.1.9:8787/admin", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "主密钥") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// 2) 密钥不对：拒绝。
	bad, _ := json.Marshal(map[string]string{"key": key[:6] + "-nope"})
	if w := do(s, remoteUIRequest("POST", "http://192.168.1.9:8787/admin/login", string(bad))); w.Code != 401 {
		t.Fatalf("错密钥 status=%d body=%s", w.Code, w.Body.String())
	}

	// 3) 没有会话时，界面资源与 RPC 都不给。
	if w := do(s, remoteUIRequest("GET", "http://192.168.1.9:8787/admin/ui/", "")); w.Code != 401 {
		t.Fatalf("未登录取资源 status=%d", w.Code)
	}
	if w := do(s, remoteUIRequest("POST", "http://192.168.1.9:8787/admin/rpc", `{"id":1,"method":"Gateway","payload":{"path":"state"}}`)); w.Code != 401 {
		t.Fatalf("未登录调 RPC status=%d", w.Code)
	}

	// 4) 正确密钥换到会话，之后一路通畅。
	cookie := loginCookie(t, s, key)
	if w := do(s, withCookie(remoteUIRequest("GET", "http://192.168.1.9:8787/admin", ""), cookie)); w.Code != http.StatusSeeOther {
		t.Fatalf("登录后 /admin status=%d", w.Code)
	}
	if w := do(s, withCookie(remoteUIRequest("GET", "http://192.168.1.9:8787/admin/ui/", ""), cookie)); w.Code != 200 {
		t.Fatalf("登录后取界面 status=%d", w.Code)
	}
	w = do(s, withCookie(remoteUIRequest("POST", "http://192.168.1.9:8787/admin/rpc", `{"id":7,"method":"Gateway","payload":{"path":"state"}}`), cookie))
	if w.Code != 200 {
		t.Fatalf("登录后 RPC status=%d body=%s", w.Code, w.Body.String())
	}
	var rep rpcReply
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.ID != 7 {
		t.Fatalf("reply=%+v", rep)
	}
	if rep.Data == nil {
		t.Fatal("state 应该带回数据")
	}

	// 5) 登出之后会话失效。
	if w := do(s, withCookie(remoteUIRequest("POST", "http://192.168.1.9:8787/admin/logout", ""), cookie)); w.Code != 200 {
		t.Fatalf("登出 status=%d", w.Code)
	}
}

func TestBrowserUIRefusedWithoutOptIn(t *testing.T) {
	s := newFenceServer(t)
	w := do(s, remoteUIRequest("GET", "http://192.168.1.9:8787/admin", ""))
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 拒绝的正文从一行 JSON 改成台头说明页：得告诉运维该设哪个环境变量。
	if ct := w.Header().Get("content-type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("远端应当拿到说明页，实际 content-type=%q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"ZEN_GATE_ALLOW_REMOTE_ADMIN", "远端管理没有开启"} {
		if !strings.Contains(body, want) {
			t.Fatalf("说明页里没提到 %q:\n%s", want, body)
		}
	}
	w = do(s, remoteUIRequest("POST", "http://192.168.1.9:8787/admin/login", `{"key":"x"}`))
	if w.Code != 403 {
		t.Fatalf("未开远端管理时登录口 status=%d", w.Code)
	}
}

func TestBrowserUISessionDiesWithKeyRotation(t *testing.T) {
	s := newFenceServer(t)
	s.SetAdminKeyRemote(true)
	cookie := loginCookie(t, s, s.Store.Config().MainKey)

	s.Store.Mutate(func(c *store.Config) { c.MainKey = "ofm-rotated-key-0123456789" })

	w := do(s, withCookie(remoteUIRequest("POST", "http://192.168.1.9:8787/admin/rpc", `{"id":1,"method":"Gateway","payload":{"path":"state"}}`), cookie))
	if w.Code != 401 {
		t.Fatalf("换主密钥后旧会话仍然可用 status=%d", w.Code)
	}
}

func TestBrowserUIRPCDispatch(t *testing.T) {
	s := newFenceServer(t)

	// 未知方法：如实回错，不假装成功。
	w := do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc", `{"id":3,"method":"Nope","payload":{}}`))
	var rep rpcReply
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OK || !strings.Contains(rep.Error, "未知的方法") || rep.ID != 3 {
		t.Fatalf("reply=%+v", rep)
	}

	// 窗口控制：浏览器里是空操作，但不能报错（页面标题栏那几个按钮）。
	w = do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc", `{"id":4,"method":"Window.Minimize","payload":{}}`))
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("窗口方法应当空操作成功：%+v", rep)
	}

	// 只有桌面才有的动作：明确说不支持，而不是静默成功。
	w = do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc", `{"id":5,"method":"OpenPath","payload":{"path":"C:\\\\"}}`))
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatalf("OpenPath 在容器里不该成功：%+v", rep)
	}
}

func TestBrowserUIFileRoundTrip(t *testing.T) {
	s := newFenceServer(t)

	// 1) 导出：服务端把字节存成一次性令牌。
	w := do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc",
		`{"id":1,"method":"Gateway","payload":{"path":"state","saveTo":"state.json"}}`))
	var rep rpcReply
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("导出失败：%+v", rep)
	}
	data, _ := rep.Data.(map[string]any)
	token, _ := data["download"].(string)
	if token == "" {
		t.Fatalf("导出没有下载令牌：%+v", data)
	}
	if data["path"] != "state.json" {
		t.Fatalf("导出文件名=%v", data["path"])
	}

	// 2) 下载：一次有效。
	w = do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787/admin/download/"+token, ""))
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("下载 status=%d 长度=%d", w.Code, w.Body.Len())
	}
	if !strings.Contains(w.Header().Get("content-disposition"), "attachment") {
		t.Fatalf("content-disposition=%q", w.Header().Get("content-disposition"))
	}
	if w := do(s, loopbackUIRequest("GET", "http://127.0.0.1:8787/admin/download/"+token, "")); w.Code != 404 {
		t.Fatalf("令牌应当只能用一次 status=%d", w.Code)
	}

	// 3) 上传：浏览器选的文件落到数据目录的 uploads/ 下。
	cfg := `{"mainKey":"ofm-browser-import-key-0123456789","port":8788}`
	w = do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/upload?name=cfg.json", cfg))
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("上传失败：%+v", rep)
	}
	upData, _ := rep.Data.(map[string]any)
	path, _ := upData["path"].(string)
	if !strings.Contains(path, "uploads") {
		t.Fatalf("上传路径=%q", path)
	}

	// 4) 导入：照旧只认服务端路径，所以页面把上传路径原样传回来。
	payload, _ := json.Marshal(map[string]any{"path": "import", "loadFrom": path})
	w = do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc",
		`{"id":2,"method":"Gateway","payload":`+string(payload)+`}`))
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("导入失败：%+v", rep)
	}
	if got := s.Store.Config().MainKey; got != "ofm-browser-import-key-0123456789" {
		t.Fatalf("导入没有生效，mainKey=%q", got)
	}
}

func TestBrowserUIUploadRejectsForeignPaths(t *testing.T) {
	s := newFenceServer(t)

	// 名字里的目录分隔符不能变成路径穿越。
	w := do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/upload?name=..%2F..%2Fevil.json", `{}`))
	var rep rpcReply
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	data, _ := rep.Data.(map[string]any)
	path, _ := data["path"].(string)
	dir := path
	if i := strings.LastIndexAny(path, "/\\"); i > 0 {
		dir = path[:i]
	}
	if !strings.HasSuffix(dir, "uploads") {
		t.Fatalf("上传落到 uploads/ 之外：%q", path)
	}

	// loadFrom 只认 uploads/ 里自己写的文件，别处一律拒绝。
	payload, _ := json.Marshal(map[string]any{"path": "import", "loadFrom": "/etc/passwd"})
	w = do(s, loopbackUIRequest("POST", "http://127.0.0.1:8787/admin/rpc",
		`{"id":1,"method":"Gateway","payload":`+string(payload)+`}`))
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OK || !strings.Contains(rep.Error, "上传") {
		t.Fatalf("任意路径不该被读：%+v", rep)
	}
}
