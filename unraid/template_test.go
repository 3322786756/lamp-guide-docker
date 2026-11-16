// Package unraid 只承载 Unraid Docker 模板的一致性测试。
//
// 模板本身是给 Unraid 的 Docker 管理器读的 XML，改错一个字（端口、卷、环境变量名）
// 在 NAS 上才会暴露，所以这里把「模板与镜像/文档必须一致」的部分钉成测试。
package unraid

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

type unraidConfig struct {
	Name        string `xml:"Name,attr"`
	Target      string `xml:"Target,attr"`
	Default     string `xml:"Default,attr"`
	Mode        string `xml:"Mode,attr"`
	Description string `xml:"Description,attr"`
	Type        string `xml:"Type,attr"`
	Display     string `xml:"Display,attr"`
	Required    string `xml:"Required,attr"`
	Mask        string `xml:"Mask,attr"`
	Value       string `xml:",chardata"`
}

type unraidTemplate struct {
	XMLName     xml.Name       `xml:"Container"`
	Version     string         `xml:"version,attr"`
	Name        string         `xml:"Name"`
	Repository  string         `xml:"Repository"`
	Registry    string         `xml:"Registry"`
	Network     string         `xml:"Network"`
	Shell       string         `xml:"Shell"`
	Privileged  string         `xml:"Privileged"`
	Support     string         `xml:"Support"`
	Project     string         `xml:"Project"`
	Overview    string         `xml:"Overview"`
	Category    string         `xml:"Category"`
	WebUI       string         `xml:"WebUI"`
	TemplateURL string         `xml:"TemplateURL"`
	Icon        string         `xml:"Icon"`
	ExtraParams string         `xml:"ExtraParams"`
	Configs     []unraidConfig `xml:"Config"`
}

func loadTemplate(t *testing.T) unraidTemplate {
	t.Helper()
	raw, err := os.ReadFile("lamp-guide.xml")
	if err != nil {
		t.Fatalf("读模板失败: %v", err)
	}
	var tpl unraidTemplate
	if err := xml.Unmarshal(raw, &tpl); err != nil {
		t.Fatalf("模板不是合法 XML: %v", err)
	}
	return tpl
}

// 模板必备字段：少任何一个，Unraid 的 Docker 页都会显示成空白或无法加载。
func TestTemplateHasRequiredElements(t *testing.T) {
	tpl := loadTemplate(t)
	if tpl.XMLName.Local != "Container" {
		t.Fatalf("根元素应为 Container，实际 %q", tpl.XMLName.Local)
	}
	if tpl.Version != "2" {
		t.Fatalf("Container 版本应为 2，实际 %q", tpl.Version)
	}
	required := map[string]string{
		"Name":        tpl.Name,
		"Repository":  tpl.Repository,
		"Registry":    tpl.Registry,
		"Network":     tpl.Network,
		"Shell":       tpl.Shell,
		"Support":     tpl.Support,
		"Project":     tpl.Project,
		"Overview":    tpl.Overview,
		"Category":    tpl.Category,
		"WebUI":       tpl.WebUI,
		"TemplateURL": tpl.TemplateURL,
		"Icon":        tpl.Icon,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Errorf("缺少 %s", name)
		}
	}
	if tpl.Network != "bridge" {
		t.Errorf("网络模式应为 bridge（容器内监听 0.0.0.0，靠端口映射对外），实际 %q", tpl.Network)
	}
	if tpl.Privileged != "false" {
		t.Errorf("不需要特权模式，应为 false，实际 %q", tpl.Privileged)
	}
	if !strings.HasSuffix(tpl.Icon, ".png") {
		t.Errorf("Unraid 图标要用 png，实际 %q", tpl.Icon)
	}
	if !strings.Contains(tpl.WebUI, "[PORT:8787]") || !strings.Contains(tpl.WebUI, "/admin") {
		t.Errorf("WebUI 应指向 /admin 并引用 8787 端口映射，实际 %q", tpl.WebUI)
	}
}

// 每个 Config 的必填属性都要在，否则 Unraid 表单项渲染异常。
func TestTemplateConfigsAreWellFormed(t *testing.T) {
	tpl := loadTemplate(t)
	if len(tpl.Configs) < 5 {
		t.Fatalf("Config 太少（%d 个），模板不完整", len(tpl.Configs))
	}
	seen := map[string]bool{}
	for i, c := range tpl.Configs {
		if c.Name == "" || c.Target == "" || c.Type == "" || c.Display == "" || c.Required == "" || c.Mask == "" {
			t.Errorf("第 %d 个 Config 属性不全: %+v", i+1, c)
		}
		// 密码类字段（Mask="true"，例如主密钥）本来就没有默认值，留空是正确行为。
		if c.Type != "Label" && c.Mask != "true" && c.Default == "" {
			t.Errorf("Config %q 缺 Default", c.Name)
		}
		if c.Type != "Label" && strings.TrimSpace(c.Description) == "" {
			t.Errorf("Config %q 缺 Description", c.Name)
		}
		key := c.Type + "/" + c.Target
		if seen[key] {
			t.Errorf("Config 重复: %s", key)
		}
		seen[key] = true
	}
}

// 端口与数据卷必须跟镜像一致：容器内 8787 监听、/data 是状态目录。
func TestTemplatePortAndVolume(t *testing.T) {
	tpl := loadTemplate(t)
	var ports, paths []unraidConfig
	for _, c := range tpl.Configs {
		switch c.Type {
		case "Port":
			ports = append(ports, c)
		case "Path":
			paths = append(paths, c)
		}
	}
	if len(ports) != 1 {
		t.Fatalf("应只有一个端口映射，实际 %d 个", len(ports))
	}
	if ports[0].Target != "8787" || ports[0].Mode != "tcp" {
		t.Fatalf("端口映射应为 8787/tcp，实际 target=%q mode=%q", ports[0].Target, ports[0].Mode)
	}
	if strings.TrimSpace(ports[0].Value) == "" {
		t.Fatalf("端口映射缺少宿主端口默认值")
	}
	if len(paths) != 1 {
		t.Fatalf("应只有一个卷，实际 %d 个", len(paths))
	}
	if paths[0].Target != "/data" || paths[0].Mode != "rw" {
		t.Fatalf("卷应为 /data:rw，实际 target=%q mode=%q", paths[0].Target, paths[0].Mode)
	}
	if !strings.HasPrefix(strings.TrimSpace(paths[0].Value), "/mnt/user/") {
		t.Errorf("Unraid 上数据目录应落在 /mnt/user/ 下，实际 %q", strings.TrimSpace(paths[0].Value))
	}
}

// 模板里出现的环境变量必须真被入口程序读取，避免写了名字却没人认。
func TestTemplateEnvVarsAreReadByEntrypoint(t *testing.T) {
	tpl := loadTemplate(t)
	known := map[string]bool{
		"ZEN_GATE_KEY":                true,
		"ZEN_GATE_HOME":               true,
		"ZEN_GATE_PORT":               true,
		"ZEN_GATE_ALLOW_LAN":          true,
		"ZEN_GATE_ALLOW_REMOTE_ADMIN": true,
		"ZEN_GATE_SHOW_KEY":           true,
		"ZEN_GATE_UID":                true,
		"ZEN_GATE_GID":                true,
		"PUID":                        true,
		"PGID":                        true,
		"TZ":                          true,
	}
	var vars []string
	for _, c := range tpl.Configs {
		if c.Type != "Variable" {
			continue
		}
		if !known[c.Target] {
			t.Errorf("模板里的环境变量 %q 不在已知清单里", c.Target)
		}
		vars = append(vars, c.Target)
	}
	if len(vars) == 0 {
		t.Fatal("模板没有任何环境变量")
	}
	for _, must := range []string{"ZEN_GATE_KEY"} {
		found := false
		for _, v := range vars {
			if v == must {
				found = true
			}
		}
		if !found {
			t.Errorf("模板必须暴露 %s，否则用户只能从日志里翻密钥", must)
		}
	}
}

// 镜像相关事实（仓库地址、内部端口、数据目录）必须与仓库里的文件对得上。
func TestTemplateMatchesImageDefinition(t *testing.T) {
	tpl := loadTemplate(t)
	if !strings.HasPrefix(tpl.Repository, "ghcr.io/") {
		t.Errorf("Repository 应指向 GHCR 镜像，实际 %q", tpl.Repository)
	}

	dockerfile, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("读 Dockerfile 失败: %v", err)
	}
	df := string(dockerfile)
	if !strings.Contains(df, "EXPOSE 8787") {
		t.Errorf("Dockerfile 不再 EXPOSE 8787，模板端口需要同步")
	}
	if !strings.Contains(df, "ENV ZEN_GATE_HOME=/data") {
		t.Errorf("Dockerfile 的 ZEN_GATE_HOME 不再是 /data，模板卷需要同步")
	}
	if !strings.Contains(df, "./cmd/lamp-guide-server") {
		t.Errorf("Dockerfile 构建目标变了，模板 Repository 需要复核")
	}

	if _, err := os.Stat("../packaging/assets/lamp-guide.png"); err != nil {
		t.Errorf("Icon 指向的 png 不在仓库里: %v", err)
	}
	if !strings.Contains(tpl.Icon, "packaging/assets/lamp-guide.png") {
		t.Errorf("Icon 应指向 packaging/assets/lamp-guide.png，实际 %q", tpl.Icon)
	}
	if !strings.Contains(tpl.TemplateURL, "/unraid/lamp-guide.xml") {
		t.Errorf("TemplateURL 应指向本模板自己，实际 %q", tpl.TemplateURL)
	}
}

// 数据目录的属主在 NAS 上经常对不上（面板用 root/nobody 建目录），镜像靠入口脚本
// 先 chown 再降权解决。模板里的 PUID/PGID 因此必须存在且有合理默认值。
func TestTemplateRunsAsConfigurableUser(t *testing.T) {
	tpl := loadTemplate(t)
	want := map[string]string{"PUID": "99", "PGID": "100"}
	got := map[string]string{}
	for _, c := range tpl.Configs {
		if c.Type != "Variable" {
			continue
		}
		if _, ok := want[c.Target]; ok {
			got[c.Target] = c.Default
		}
	}
	for name, def := range want {
		if got[name] != def {
			t.Errorf("模板的 %s 默认值应为 %q（Unraid 惯例），实际 %q", name, def, got[name])
		}
	}
}

// 从 NAS 上点 WebUI 按钮是远端访问，opt-in 关着只会看到 403 说明页。既然这个模板
// 就是给 NAS 面板点开用的，默认值必须把它打开；关闭由用户在面板里改回 0。
func TestTemplateEnablesRemoteAdminByDefault(t *testing.T) {
	tpl := loadTemplate(t)
	for _, c := range tpl.Configs {
		if c.Target != "ZEN_GATE_ALLOW_REMOTE_ADMIN" {
			continue
		}
		if c.Default != "1" || strings.TrimSpace(c.Value) != "1" {
			t.Fatalf("ZEN_GATE_ALLOW_REMOTE_ADMIN 默认应为 1（否则 NAS 上点 WebUI 只有 403）：Default=%q Value=%q", c.Default, c.Value)
		}
		if !strings.Contains(c.Description, "0") {
			t.Errorf("说明里要写清楚设成 0 会怎样，实际 %q", c.Description)
		}
		return
	}
	t.Fatal("模板里没有 ZEN_GATE_ALLOW_REMOTE_ADMIN")
}
