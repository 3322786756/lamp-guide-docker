// webui.go —— 浏览器里的管理界面：静态资源、登录会话、以及浏览器版宿主通道。
//
// 桌面版界面走的是 WebView2 消息桥（web/js/bridge.js ↔ internal/shell）：页面不
// 碰 HTTP，宿主在进程内把每条消息喂给网关的管理路由。容器里没有这个宿主，所以
// 这里补上最小的一层，让同一份界面在浏览器里跑起来：
//
//	GET  /admin                未登录给登录页；已登录 302 到 /admin/ui/
//	POST /admin/login          主密钥换一个会话 cookie
//	POST /admin/logout         清掉会话
//	GET  /admin/ui/…           界面静态资源（web/ 里同一份文件）
//	POST /admin/rpc            bridge.js 的 postMessage 等价物
//	POST /admin/upload         浏览器选的文件落地，import 需要的是服务端路径
//	GET  /admin/download/<令牌> 导出的字节还给浏览器下载
//
// 鉴权与 /admin/api 同一套判断：回环调用照旧放行（本机 curl、桌面版的进程内调
// 用），远端要么带主密钥 Bearer，要么带着登录换来的会话 cookie；两边都没有时给
// 登录页，而不是一句看不懂的 403。
package gateway

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"zenui/web"
)

const (
	// sessionCookie carries the browser dashboard's login state.
	sessionCookie = "lamp_admin"
	// sessionTTL bounds how long one login lasts. The cookie also dies with the
	// process (new signing secret) and with a main-key rotation (the token is
	// derived from the key), which is what an operator expects.
	sessionTTL = 12 * time.Hour
	// uploadLimit caps a config import posted from the browser.
	uploadLimit = 8 << 20
	// exportTTL is how long an export waits for the browser to fetch it.
	exportTTL = 10 * time.Minute
	// browserUIDir is where the界面 assets are mounted.
	browserUIDir = "/admin/ui"
)

// exportFile is one export waiting to be downloaded by the browser.
type exportFile struct {
	name string
	data []byte
	at   time.Time
}

// rpcRequest mirrors web/js/bridge.js: one request per id, one reply per id.
type rpcRequest struct {
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// rpcReply is the same envelope internal/shell returns, so the page cannot tell
// which host it is talking to.
type rpcReply struct {
	ID    int    `json:"id"`
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// newSessionSecret mints the per-process cookie signing key.
func newSessionSecret() []byte {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// 没有随机源也不能退化成"谁都能登录"：用启动时间兜底，效果仍是重启即
		// 失效，和正常路径一样。
		sum := sha256.Sum256([]byte(time.Now().String()))
		return sum[:]
	}
	return buf
}

// sessionToken is the cookie value: an HMAC over the main key, so rotating the
// key (or the process restarting) ends every outstanding session.
func (s *Server) sessionToken() string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte("lamp-admin\x00"))
	mac.Write([]byte(s.Store.Config().MainKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// sessionOK reports whether the request carries this deployment's session.
func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.sessionToken())) == 1
}

// webAuthed is the browser dashboard's gate: loopback keeps the original free
// pass, a remote caller needs either the login cookie or the main key itself.
func (s *Server) webAuthed(r *http.Request) bool {
	if remoteIsLoopback(r) {
		return true
	}
	if !s.adminKeyRemote {
		return false
	}
	if s.sessionOK(r) {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(bearerOf(r)), []byte(s.Store.Config().MainKey)) == 1
}

// webFence answers with the right refusal and returns false when the caller may
// not proceed. A remote caller that has not logged in yet gets a plain 401 the
// page turns into "回登录页", not the loopback wording meant for /admin/api.
func (s *Server) webFence(w http.ResponseWriter, r *http.Request) bool {
	if s.webAuthed(r) {
		return true
	}
	if remoteIsLoopback(r) {
		return adminAllowed(w, r, r.Method != http.MethodGet)
	}
	if !s.adminKeyRemote {
		writeJSON(w, 403, map[string]any{"error": "admin is loopback-only"})
		return false
	}
	writeJSON(w, 401, map[string]any{"error": "需要先登录：打开 /admin 输入主密钥"})
	return false
}

// serveBrowserUI serves the page itself: the login form until the caller is
// authenticated, then a redirect to the assets root (relative URLs in
// index.html need a directory-style path).
//
// The page-level gate is dashboardFence — the same one the old static page used,
// so a host that never opted into remote admin still answers exactly as before.
func (s *Server) serveBrowserUI(w http.ResponseWriter, r *http.Request) {
	// 远端 + 宿主没开 opt-in：给一页门说明，而不是一句 「admin is
	// loopback-only」——那句话看着像功能坏了，实际只是环境变量没设。
	if !remoteIsLoopback(r) && !s.adminKeyRemote {
		s.serveRemoteAdminOff(w, r)
		return
	}
	if !s.dashboardFence(w, r) {
		return
	}
	if s.webAuthed(r) {
		http.Redirect(w, r, browserUIDir+"/", http.StatusSeeOther)
		return
	}

	page, err := fs.ReadFile(web.FS(), "login.html")
	if err != nil {
		page = dashboardHTML
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-content-type-options", "nosniff")
	_, _ = w.Write(page)
}

// remoteAdminOffPage answers the one question a remote operator actually has
// when the panel refuses them: what do I set, and where?
const remoteAdminOffPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Lamp Guide · 远端管理未开启</title>
<style>
  :root { color-scheme: dark }
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #0f1115; color: #e6e8ee;
         font: 15px/1.75 -apple-system, "Segoe UI", "Noto Sans SC", "Microsoft YaHei", sans-serif }
  main { width: min(660px, calc(100vw - 40px)); padding: 30px 30px 26px;
         background: #171a21; border: 1px solid #262b36; border-radius: 14px }
  h1 { margin: 0 0 6px; font-size: 19px; letter-spacing: .2px }
  .tag { display: inline-block; margin-bottom: 18px; padding: 2px 8px; border-radius: 999px;
         background: #2a2113; border: 1px solid #574623; color: #e0b872; font-size: 12px }
  p { margin: 10px 0 }
  code { padding: 1px 6px; border-radius: 5px; background: #0b0d11; border: 1px solid #262b36; font-size: 13px }
  pre { margin: 12px 0; padding: 12px 14px; overflow: auto; border-radius: 9px;
        background: #0b0d11; border: 1px solid #262b36; font-size: 12.5px; line-height: 1.6 }
  .dim { color: #8b93a7; font-size: 13.5px }
  .sep { height: 1px; margin: 20px 0; background: #262b36 }
</style>
</head>
<body>
<main>
  <span class="tag">403 · 仅限本机</span>
  <h1>远端管理没有开启</h1>
  <p>管理界面默认只给容器本机（回环地址）用。你现在是从别的设备访问，所以被挡在这里。</p>
  <p><b>要从浏览器管理</b>：给容器加环境变量 <code>ZEN_GATE_ALLOW_REMOTE_ADMIN=1</code> 再重启，然后刷新这个地址，输入主密钥登录。</p>
  <pre>docker run -d --name lamp-guide -p 8787:8787 \
  -v lamp-guide-data:/data \
  -e ZEN_GATE_ALLOW_REMOTE_ADMIN=1 \
  ghcr.io/3322786756/lamp-guide-docker:latest</pre>
  <p class="dim">Unraid：把模板里的「允许远端管理」改成 1，点应用（容器会重建）；compose：<code>ZEN_GATE_ALLOW_REMOTE_ADMIN: "1"</code>。</p>
  <div class="sep"></div>
  <p><b>先确认变量真的进了容器</b>（<code>docker restart</code> 不会带上新的 <code>-e</code>，必须重建容器）：</p>
  <pre>docker exec lamp-guide env | grep ZEN_GATE</pre>
  <p>这个进程读到的 <code>ZEN_GATE_ALLOW_REMOTE_ADMIN</code> 是 <b class="val">{{value}}</b>。</p>
  <p class="dim hint">{{hint}}</p>
  <div class="sep"></div>
  <p class="dim">不想对外开放也行：在 NAS 上 <code>ssh -L 8787:127.0.0.1:8787 root@nas</code>，再在本机打开 <code>http://127.0.0.1:8787/admin</code>，这样不需要开远端管理。</p>
  <p class="dim">这个端口上的 <code>/v1</code>（模型接口）和 <code>/health</code> 不受影响，照旧可用。</p>
</main>
</body>
</html>
`

// serveRemoteAdminOff renders that page. Status stays 403 so scripts and the
// CI smoke checks keep seeing a refusal, only the body became readable.
func (s *Server) serveRemoteAdminOff(w http.ResponseWriter, r *http.Request) {
	value, hint := remoteAdminOffState()
	page := strings.ReplaceAll(remoteAdminOffPage, "{{value}}", html.EscapeString(value))
	page = strings.ReplaceAll(page, "{{hint}}", html.EscapeString(hint))
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-content-type-options", "nosniff")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, page)
}

// remoteAdminOffState 说清「为什么远端被挡住」，用的是这个进程真正读到的环境变
// 量 —— 「我明明加了 ZEN_GATE_ALLOW_REMOTE_ADMIN=1」这类问题就看这两行：变量没
// 进容器，还是值写得认不出来，还是容器根本没重建。
func remoteAdminOffState() (value, hint string) {
	raw, set := os.LookupEnv("ZEN_GATE_ALLOW_REMOTE_ADMIN")
	if !set {
		return "（没有设置）",
			"无界面入口默认就是允许远端管理的，现在这个进程却没开——说明它跑的不是 lamp-guide-server（桌面版或别的嵌入方默认只认本机），或者代码是旧的。用新镜像重建容器。"
	}
	if strings.TrimSpace(raw) == "" {
		return "（空值）",
			"变量存在但是空的，等同于关闭。删掉这个变量（默认就是开的）或设成 1，再重建容器。"
	}
	switch strings.ToLower(strings.Trim(strings.TrimSpace(raw), `"'`)) {
	case "1", "true", "yes", "y", "on", "enable", "enabled":
		return raw,
			"值看着是对的，说明容器还是加变量之前的那个进程：重建容器后才会生效。"
	case "0", "false", "no", "n", "off", "disable", "disabled":
		return raw,
			"远端管理被这个变量显式关掉了（0/false/off 都算关闭），所以只剩本机可用。删掉它（默认开着）或改成 1，再重建容器。"
	default:
		return raw,
			"这个值认不出来，按关闭处理。只认 1 / true / yes / on（大小写随意，别带引号或空格）；删掉变量则默认开启。"
	}
}

// serveAssets serves web/ under /admin/ui/, with no directory listings: the
// page asks for exact files.
func (s *Server) serveAssets(w http.ResponseWriter, r *http.Request) {
	// 界面里的资源全是相对路径，缺了结尾的斜杠会解析到 /admin/ 下去。补斜杠要
	// 放在鉴权之前，否则重定向本身先被拒成一句 JSON 401。
	if strings.HasSuffix(r.URL.Path, browserUIDir) {
		http.Redirect(w, r, browserUIDir+"/", http.StatusMovedPermanently)
		return
	}
	if !remoteIsLoopback(r) && !s.adminKeyRemote {
		s.serveRemoteAdminOff(w, r)
		return
	}
	// 界面代码本身不是秘密（就在公开仓库里），所以 js/css/图片这些静态资源不再
	// 要求会话——登录页要用同一份资源，而它必然发生在登录之前。
	// 页面本身（index.html 这类）仍然要会话，没有就送去登录页：浏览器不该停在
	// 一句 {"error":"需要先登录：打开 /admin 输入主密钥"} 上。
	name := strings.TrimPrefix(r.URL.Path, browserUIDir)
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if path.Ext(name) == ".html" && !s.webAuthed(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data, err := fs.ReadFile(web.FS(), name)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("content-type", ctype)
	w.Header().Set("x-content-type-options", "nosniff")
	if path.Ext(name) == ".html" {
		w.Header().Set("cache-control", "no-store")
	} else {
		w.Header().Set("cache-control", "private, max-age=300")
	}
	_, _ = w.Write(data)
}

// serveLogin trades the main key for a session cookie. The key travels in the
// body (form field key / JSON key) or as a Bearer header, and it is compared in
// constant time against the configured main key only.
func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request) {
	if !remoteIsLoopback(r) && !s.adminKeyRemote {
		writeJSON(w, 403, map[string]any{"error": "admin is loopback-only"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	key := bearerOf(r)
	if err := decodeBody(r, &body); err == nil && strings.TrimSpace(body.Key) != "" {
		key = strings.TrimSpace(body.Key)
	}
	if key == "" {
		writeJSON(w, 400, map[string]any{"error": "缺少主密钥"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.Store.Config().MainKey)) != 1 {
		// 不透露"差一点对"，只说不对。
		writeJSON(w, 401, map[string]any{"error": "主密钥不对"})
		return
	}
	// SameSite 用 Lax 而不是 Strict：从 Unraid 面板、书签栏、聊天窗口这类「别
	// 的站点」点过来的顶层导航，Strict 的 cookie 根本不会带上，于是每点一次
	// WebUI 按钮都像掉线一样回到登录页。Lax 仍然不会跟着跨站子请求/表单发出去。
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessionToken(),
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// serveLogout drops the session cookie.
func (s *Server) serveLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// serveRPC is the browser's stand-in for internal/shell's message pump.
func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	if !s.webFence(w, r) {
		return
	}
	var req rpcRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, 400, rpcReply{OK: false, Error: "请求格式不合法：" + err.Error()})
		return
	}

	method := strings.ToLower(strings.TrimSpace(req.Method))
	switch {
	case method == "gateway":
		data, err := s.rpcGateway(req.Payload)
		replyRPC(w, req.ID, data, err)

	case strings.HasPrefix(method, "window."):
		// 容器里没有窗口：标题栏那几个按钮变成空操作，页面照旧工作。
		replyRPC(w, req.ID, map[string]any{"handle": "browser"}, nil)

	case method == "openurl", method == "openpath", method == "picksavepath", method == "pickopenpath":
		// 这三个由 web/js/webbridge.js 在浏览器侧处理（新标签页 / 选文件上传 /
		// 直接下载），走到这里说明客户端没装桥，如实回一句。
		replyRPC(w, req.ID, nil, errors.New("这个动作只在桌面版里可用"))

	default:
		replyRPC(w, req.ID, nil, fmt.Errorf("未知的方法：%s", req.Method))
	}
}

// replyRPC writes one reply envelope; error text is what the page shows.
func replyRPC(w http.ResponseWriter, id int, data any, err error) {
	rep := rpcReply{ID: id, OK: err == nil, Data: data}
	if err != nil {
		rep.Error = err.Error()
	}
	writeJSON(w, 200, rep)
}

// rpcGateway is the page's only door into the gateway, same contract as
// internal/host.handleGateway: path → /admin/api/<path>, body → POST.
//
// The two file-shaped arguments differ from the desktop: SaveTo cannot write to
// a path the operator picked (a browser has no such path), so the bytes are
// parked under a one-shot token and the page downloads them; LoadFrom points at
// a file the browser uploaded moments ago.
func (s *Server) rpcGateway(raw json.RawMessage) (any, error) {
	var args struct {
		Path     string          `json:"path"`
		Body     json.RawMessage `json:"body"`
		SaveTo   string          `json:"saveTo"`
		LoadFrom string          `json:"loadFrom"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("缺少 path")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("参数不是合法 JSON：" + err.Error())
	}
	if strings.TrimSpace(args.Path) == "" {
		return nil, errors.New("缺少 path")
	}

	var body any
	if len(args.Body) > 0 && string(args.Body) != "null" {
		if err := json.Unmarshal(args.Body, &body); err != nil {
			return nil, errors.New("body 不是合法 JSON：" + err.Error())
		}
	}
	if args.LoadFrom != "" {
		fileRaw, err := readUpload(args.LoadFrom)
		if err != nil {
			return nil, fmt.Errorf("读取上传文件失败：%w", err)
		}
		if err := json.Unmarshal(fileRaw, &body); err != nil {
			return nil, errors.New("文件不是合法 JSON：" + err.Error())
		}
	}

	status, out, err := s.adminInProcess(args.Path, body)
	if err != nil {
		return nil, fmt.Errorf("网关调用失败：%w", err)
	}
	if status >= 400 {
		// 404/500 这类回答不带 ok 字段，只看 {"ok":} 会把"路由不存在"当成成功。
		return nil, errors.New(adminErrorText(out, status))
	}

	if args.SaveTo != "" {
		name := filepath.Base(strings.TrimSpace(args.SaveTo))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = "lamp-guide-export.bin"
		}
		token := s.parkExport(name, out)
		return map[string]any{"path": name, "bytes": len(out), "download": token}, nil
	}

	var decoded any
	if err := json.Unmarshal(out, &decoded); err != nil {
		// 非 JSON 响应（导出、CSV）原样回文本。
		return string(out), nil
	}
	if m, isMap := decoded.(map[string]any); isMap {
		if v, has := m["ok"]; has {
			if b, _ := v.(bool); !b {
				return nil, errors.New(adminErrorText(out, status))
			}
		}
	}
	return decoded, nil
}

// adminInProcess runs an admin route inside this process, loopback-shaped, the
// way internal/zenbridge does for the desktop window. No key is involved: the
// caller already passed the browser fence.
func (s *Server) adminInProcess(route string, body any) (int, []byte, error) {
	var rdr io.Reader
	method := http.MethodGet
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, "http://127.0.0.1/admin/api/"+strings.TrimPrefix(route, "/"), rdr)
	// These are exactly the facts adminAllowed checks: a loopback Host and a
	// loopback RemoteAddr.
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:0"
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	// A mutating request must carry a same-origin Origin or none at all.
	req.Header.Set("origin", "http://127.0.0.1")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// adminErrorText pulls the most useful sentence out of an error body.
func adminErrorText(body []byte, status int) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		if s, isStr := m["error"].(string); isStr && s != "" {
			return s
		}
	}
	if text := strings.TrimSpace(string(body)); text != "" && len(text) < 400 {
		return text
	}
	return fmt.Sprintf("网关返回 %d", status)
}

// parkExport keeps an export until the browser fetches it.
func (s *Server) parkExport(name string, data []byte) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", name, time.Now().UnixNano())))
		buf = sum[:16]
	}
	token := hex.EncodeToString(buf)

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	now := time.Now()
	for k, f := range s.exports {
		if now.Sub(f.at) > exportTTL {
			delete(s.exports, k)
		}
	}
	s.exports[token] = exportFile{name: name, data: data, at: now}
	return token
}

// serveDownload hands one parked export to the browser exactly once.
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, token string) {
	if !s.webFence(w, r) {
		return
	}
	s.fileMu.Lock()
	f, found := s.exports[token]
	delete(s.exports, token)
	s.fileMu.Unlock()
	if !found {
		writeJSON(w, 404, map[string]any{"error": "导出已过期，请重新导出"})
		return
	}

	ctype := mime.TypeByExtension(path.Ext(f.name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("content-type", ctype)
	w.Header().Set("content-disposition",
		fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", f.name, urlPathEscape(f.name)))
	w.Header().Set("cache-control", "no-store")
	_, _ = w.Write(f.data)
}

// serveUpload lands a file the browser picked, so the import route — which only
// understands a server-side path — has something to read.
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	if !s.webFence(w, r) {
		return
	}
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("name")))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "config.json"
	}
	name = uploadSafeName(name)

	data, err := io.ReadAll(io.LimitReader(r.Body, uploadLimit+1))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "读取上传内容失败：" + err.Error()})
		return
	}
	if len(data) > uploadLimit {
		writeJSON(w, 413, map[string]any{"error": fmt.Sprintf("文件超过 %d MB", uploadLimit>>20)})
		return
	}

	dir := filepath.Join(s.Store.Home, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, 500, map[string]any{"error": "无法创建上传目录：" + err.Error()})
		return
	}
	full := filepath.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	if err := writeFilePrivate(full, data); err != nil {
		writeJSON(w, 500, map[string]any{"error": "保存上传文件失败：" + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"path": full, "bytes": len(data)}})
}

// readUpload reads back a file this process wrote, and refuses anything else so
// a crafted loadFrom cannot become an arbitrary-file read.
func readUpload(p string) ([]byte, error) {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("路径必须是绝对路径")
	}
	if filepath.Base(filepath.Dir(clean)) != "uploads" {
		return nil, errors.New("只接受 /admin/upload 上传的文件")
	}
	return os.ReadFile(clean)
}

// uploadSafeName keeps letters, digits, dot, dash and underscore from the
// browser-supplied name — a filename is a label here, never a path.
func uploadSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			// 中文名也留住：非 ASCII 原样写，只有分隔符和 shell 元字符被换掉。
			if r > 127 {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "config.json"
	}
	return out
}

// writeFilePrivate writes a file only this process's user can read: an uploaded
// config can carry credentials.
func writeFilePrivate(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// urlPathEscape escapes a filename for the RFC 5987 filename* parameter.
func urlPathEscape(name string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}
