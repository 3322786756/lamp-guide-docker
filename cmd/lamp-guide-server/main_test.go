package main

import (
	"strings"
	"testing"

	"zenui/internal/zen/store"
)

func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ZEN_GATE_HOME", home)
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestApplyKeyOverridesAndPersists(t *testing.T) {
	st := openTempStore(t)
	const want = "sk-my-own-lamp-guide-key-0123456789"

	if err := applyKey(st, want); err != nil {
		t.Fatal(err)
	}
	if got := st.Config().MainKey; got != want {
		t.Fatalf("内存里的主密钥 = %q, 期望 %q", got, want)
	}
	// 必须落盘：容器重启后客户端不能因为密钥变回随机值而全挂。
	reopened, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Config().MainKey; got != want {
		t.Fatalf("重新打开后的主密钥 = %q, 期望 %q", got, want)
	}
}

func TestApplyKeyEmptyKeepsExisting(t *testing.T) {
	st := openTempStore(t)
	before := st.Config().MainKey

	for _, raw := range []string{"", "   ", "\t\n"} {
		if err := applyKey(st, raw); err != nil {
			t.Fatalf("applyKey(%q): %v", raw, err)
		}
		if got := st.Config().MainKey; got != before {
			t.Fatalf("applyKey(%q) 改动了主密钥: %q -> %q", raw, before, got)
		}
	}
	if before == "" {
		t.Fatal("默认配置应当自带一个生成的主密钥")
	}
}

func TestApplyKeyRejectsWhitespaceAndQuotes(t *testing.T) {
	st := openTempStore(t)
	before := st.Config().MainKey

	for _, raw := range []string{"has space", "has\ttab", `has"quote`, "trailing newline\ntrailing"} {
		if err := applyKey(st, raw); err == nil {
			t.Fatalf("applyKey(%q) 应当报错", raw)
		}
		if got := st.Config().MainKey; got != before {
			t.Fatalf("被拒绝的 key 改动了配置: %q", got)
		}
	}
}

func TestApplyKeySameValueIsNoOp(t *testing.T) {
	st := openTempStore(t)
	key := st.Config().MainKey
	if err := applyKey(st, key); err != nil {
		t.Fatal(err)
	}
	if got := st.Config().MainKey; got != key {
		t.Fatalf("主密钥被改写: %q", got)
	}
	if err := applyKey(st, "  "+key+"  "); err != nil { // 前后空白会被裁掉
		t.Fatal(err)
	}
	if got := st.Config().MainKey; got != key {
		t.Fatalf("裁剪后应当等于原值，实际 %q", got)
	}
	if !strings.HasPrefix(key, "ofm-") {
		t.Fatalf("生成的主密钥不带 ofm- 前缀: %q", key)
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want int
	}{
		{"unset", "", 0},
		{"port", "9090", 9090},
		{"zero-means-unset", "0", 0},
		{"negative", "-1", 0},
		{"above-range", "70000", 0},
		{"junk", "http", 0},
		{"padded", " 8787 ", 8787},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZEN_GATE_PORT", tc.val)
			if got := envInt("ZEN_GATE_PORT"); got != tc.want {
				t.Fatalf("envInt(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		val  string
		def  bool
		want bool
	}{
		{"", true, true},
		{"", false, false},
		{"1", false, true},
		{"TRUE", false, true},
		{" yes ", false, true},
		{"on", false, true},
		{"0", true, false},
		{"false", true, false},
		{"Off", true, false},
		{"no", true, false},
		// 认不出的值回落到默认值，而不是猜一个方向。
		{"maybe", true, true},
		{"2", false, false},
		// 面板/compose/shell 常常把引号一起塞进环境变量里，别让它默默失效。
		{`"1"`, false, true},
		{`'1'`, false, true},
		{` "true" `, false, true},
		{`"off"`, true, false},
		{"Enabled", false, true},
		{"disabled", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("ZEN_GATE_ALLOW_LAN", tc.val)
			if got := envBool("ZEN_GATE_ALLOW_LAN", tc.def); got != tc.want {
				t.Fatalf("envBool(%q, %v) = %v, want %v", tc.val, tc.def, got, tc.want)
			}
		})
	}
}

func TestKeyPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "short"},
		{"ofm-abcdefghijklmnop", "ofm-abcd"},
	}
	for _, tc := range cases {
		if got := keyPrefix(tc.in); got != tc.want {
			t.Fatalf("keyPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
