// Command lamp-guide-server runs the zen gateway on its own: no Win32 window,
// no WebView2, no tray. It is the entry point the container image uses, and it
// also runs directly on any Linux/macOS host.
//
// Everything it touches is gateway code (internal/zen/core); the desktop layers
// (internal/shell, internal/native, internal/host) are Windows-only and stay out
// of this binary, which is why the image needs no GUI stack at all.
//
// Configuration comes from the environment, because there is no window to click:
//
//	ZEN_GATE_HOME             data directory (config.json, logs, tags, stats)
//	ZEN_GATE_PORT             listen port (default: the port in config.json, 8787)
//	ZEN_GATE_ALLOW_LAN        bind every interface (default on; 0/false/no/off
//	                          keeps it on loopback)
//	ZEN_GATE_ALLOW_REMOTE_ADMIN  accept the dashboard and /admin/api from a
//	                          non-loopback address when the main key is presented
//	                          (default off)
//	ZEN_GATE_SHOW_KEY         print the main API key at boot (default off)
//	ZEN_GATE_KEY              use this main API key instead of the one in
//	                          config.json (persisted on boot; empty keeps it)
//
// Channels, providers and keys are whatever the data directory already holds:
// point ZEN_GATE_HOME at a config.json produced by the desktop app (导出配置 with
// secrets) or write it through /admin/api. Mount that directory — it is the whole
// state of the gateway.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"zenui/internal/zen/core"
	"zenui/internal/zen/gateway"
	"zenui/internal/zen/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "lamp-guide-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := core.Options{
		Home:             strings.TrimSpace(os.Getenv("ZEN_GATE_HOME")),
		Port:             envInt("ZEN_GATE_PORT"),
		NoTray:           true, // logs go to stdout; the container collector owns them
		DisableDesktop:   true, // no agent-config injection, self-update or sing-box sidecar
		AllowLan:         envBool("ZEN_GATE_ALLOW_LAN", true),
		AllowRemoteAdmin: envBool("ZEN_GATE_ALLOW_REMOTE_ADMIN", false),
		// No desktop surface: user-facing toasts become log lines, and there is
		// no browser to hand an OAuth URL to (the admin API returns the URL
		// instead, and prints it here).
		OnNotify: func(title, body string) {
			fmt.Printf("[notify] %s: %s\n", title, body)
		},
		OpenExternal: func(url string) {
			fmt.Printf("[open] %s\n", url)
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := core.Start(ctx, opts)
	if err != nil {
		return err
	}
	// core.Start owns every background loop, so there is nothing to poll: wait
	// for the signal, then tear the gateway down exactly once. Stop flushes the
	// stats/config snapshot; the short pause lets a closing response land before
	// the process leaves.
	defer func() {
		c.Stop()
		time.Sleep(200 * time.Millisecond)
	}()

	// 自定义主密钥放在 Start 之后：那时 store 已经建好（首次启动会写好默认配置），
	// 再把它改掉并落盘，重启、重装、换机器都能沿用同一个密钥。
	if err := applyKey(c.Store, os.Getenv("ZEN_GATE_KEY")); err != nil {
		return err
	}

	report(c, opts)
	<-ctx.Done()
	fmt.Println("收到退出信号，正在关闭…")
	return nil
}

// report prints the three facts an operator needs from a headless boot: where
// the API is, where the admin surface is, and — on the first boot only — the key
// that gates both. The key is a credential: it is printed when asked for, and
// otherwise only its prefix.
func report(c *core.Core, opts core.Options) {
	cfg := c.Store.Config()
	port := cfg.Port
	fmt.Printf("lamp-guide-server %s · go %s · data %s\n", gateway.Version, runtime.Version(), c.Store.Home)
	// 局域网地址才是别的设备真正要填的地址；只监听回环时就没有这一行。
	if lan := c.Gateway.LANBaseURL(); lan != "" {
		fmt.Printf("API      %s  (OpenAI/Anthropic 兼容；其它设备填这个地址)\n", lan)
	} else {
		fmt.Printf("API      %s  (仅本机；ZEN_GATE_ALLOW_LAN=1 才对局域网开放)\n", c.Gateway.BaseURL())
	}
	if opts.AllowRemoteAdmin {
		fmt.Printf("admin    http://<本机地址>:%d/admin/api/*  (需主密钥)\n", port)
	} else {
		fmt.Printf("admin    http://127.0.0.1:%d/admin  (仅回环；ZEN_GATE_ALLOW_REMOTE_ADMIN=1 才接受主密钥的远端调用)\n", port)
	}
	if envBool("ZEN_GATE_SHOW_KEY", false) {
		fmt.Printf("key      %s\n", cfg.MainKey)
	} else {
		fmt.Printf("key      %s…  (ZEN_GATE_SHOW_KEY=1 可打印完整主密钥)\n", keyPrefix(cfg.MainKey))
	}
	if len(cfg.ChannelAccounts) == 0 && len(cfg.Providers) == 0 {
		fmt.Println("注意: 数据目录里没有任何渠道或供应商。请把桌面应用「导出配置」(含密钥) 的 config.json 放进 " +
			"ZEN_GATE_HOME，或通过 /admin/api 写入，网关才有模型可服务。")
	}
}

// applyKey installs ZEN_GATE_KEY as the main API key. The gateway gates both
// /v1 and (when the remote opt-in is on) /admin/api on this one string, so the
// value is written to config.json rather than kept as a one-shot override:
// restarting the container must not silently change the credential out from
// under every client. An empty value means "keep whatever config.json holds",
// which on a fresh volume is the key store.Open generated.
func applyKey(st *store.Store, raw string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return nil
	}
	if strings.ContainsAny(key, " \t\r\n\"'") {
		return fmt.Errorf("ZEN_GATE_KEY 不能包含空白或引号: %q", key)
	}
	if len(key) < 16 {
		fmt.Fprintf(os.Stderr, "注意: ZEN_GATE_KEY 只有 %d 个字符，容易被猜；建议用 32 位以上的随机串\n", len(key))
	}
	if st.Config().MainKey == key {
		return nil
	}
	st.Mutate(func(cfg *store.Config) { cfg.MainKey = key })
	if err := st.Save(); err != nil {
		return fmt.Errorf("把 ZEN_GATE_KEY 写入 config.json 失败: %w", err)
	}
	fmt.Println("主密钥已按 ZEN_GATE_KEY 更新，并写入 config.json（后续启动沿用）")
	return nil
}

func keyPrefix(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8]
}

// envInt reads a positive integer override; 0 (or junk) means "no override".
func envInt(name string) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 65535 {
		fmt.Fprintf(os.Stderr, "忽略无效的 %s=%q\n", name, v)
		return 0
	}
	return n
}

// envBool reads a toggle, with an explicit default for the unset case.
func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		fmt.Fprintf(os.Stderr, "忽略无法识别的 %s=%q\n", name, v)
		return def
	}
}
