package unraid

import (
	"os"
	"strings"
	"testing"
)

// 镜像与入口脚本是一体两面：容器要以 root 起步才能纠正卷属主，但服务进程必须降权。
// 这两件事任何一边被改动，都会让 NAS 上的「permission denied」重新出现。
func TestDockerfileWiresEntrypoint(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("读 Dockerfile 失败: %v", err)
	}
	df := string(raw)

	for _, want := range []string{
		"COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh",
		`ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]`,
		"ca-certificates tzdata su-exec",
		"USER root",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile 缺少 %q", want)
		}
	}
	// USER lamp 会让入口脚本以 10001 起步，chown 无权执行，等于回退到旧的报错。
	if strings.Contains(df, "USER lamp") {
		t.Error("Dockerfile 不应再 USER lamp：入口脚本需要 root 起步才能纠正数据目录属主")
	}
}

func TestEntrypointScriptIsSound(t *testing.T) {
	raw, err := os.ReadFile("../docker-entrypoint.sh")
	if err != nil {
		t.Fatalf("读 docker-entrypoint.sh 失败: %v", err)
	}
	if strings.Contains(string(raw), "\r") {
		t.Error("入口脚本必须是 LF 换行，CRLF 会让 alpine 的 /bin/sh 报错")
	}
	script := string(raw)
	if !strings.HasPrefix(script, "#!/bin/sh") {
		t.Error("入口脚本缺少 #!/bin/sh")
	}
	for _, want := range []string{
		"chown -R",
		`exec su-exec "$uid:$gid" /usr/local/bin/lamp-guide-server`,
		"exec /usr/local/bin/lamp-guide-server",
		`${PUID:-10001}`,
		`${PGID:-10001}`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("入口脚本缺少 %q", want)
		}
	}
}
