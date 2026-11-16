# 用 Docker 跑 Lamp Guide 网关

Lamp Guide 的桌面版是 Win32 + WebView2 写的，只能跑在 Windows 上；**进容器的是网关**——那些 OpenAI / Anthropic 兼容的 `/v1` 接口，加上一套**跑在浏览器里的同一份管理界面**。容器里的进程叫 `lamp-guide-server`，由 `internal/zen/core` 直接抬起来，没有窗口、托盘和 WebView2，因此镜像很小，任何一台装了 Docker 的 Linux 机器（含 NAS、VPS、旁路由）都能跑。

- 想在 Windows 桌面上用带界面的版本：照旧用安装包，见 [README](../README.md)。
- 想把网关挂在局域网/公网给别的设备用：就是本文。

## 镜像里有什么

| 有 | 没有 |
| --- | --- |
| `lamp-guide-server`（静态链接，启动时纠属主再降权） | Win32 窗口 / 托盘 / WebView2 |
| `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 等转发 | 系统托盘菜单、窗口最大化/拖拽那几个按钮 |
| `/admin` 里的浏览器版管理界面（渠道、模型、聚合、签到、日志、设置） | 一键自更新、开机自启 |
| `/admin/api/*` 管理接口（JSON）、`/health` 健康检查 | agent 配置注入、sing-box 订阅代理 |

最后一行是有意为之：容器里没有「装置」的概念，注入编辑器配置、替换自身二进制、拉起本地 sing-box 侧车都没有意义，也容易踩坑，所以容器入口用 `DisableDesktop` 把它们整体裁掉了。**订阅（订阅轮询 / 代理模式）在容器里不会生效**，需要直连或用宿主机的网络代理。

## 支持哪些架构

镜像一次构建出三个架构，组成一个多架构清单，`docker pull` 会按宿主机自动挑：

| 平台 | 典型设备 |
| --- | --- |
| `linux/amd64` | x86_64 服务器、x86 型号的群晖/威联通、Intel 芯的 Windows/macOS 上的 Docker Desktop |
| `linux/arm64` | Apple Silicon 上的 Docker Desktop、树莓派 4/5 装 64 位系统、各类 arm64 小主机与 NAS |
| `linux/arm/v7` | 树莓派 2/3/4 装 32 位系统、老一些的 arm 开发板 |

```bash
# 一般不用管架构，docker 会自己选
docker pull ghcr.io/3322786756/lamp-guide-docker:latest

# 想确认清单里有哪些平台 / 强制拉某个架构
docker buildx imagetools inspect ghcr.io/3322786756/lamp-guide-docker:latest
docker pull --platform linux/arm64 ghcr.io/3322786756/lamp-guide-docker:latest
```

自己构建多架构（需要 buildx）：

```bash
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 \
  -t lamp-guide:latest --push .
```

只写 `docker build` 的话只出当前机器架构（amd64 机器上就是 `linux/amd64`）。二进制是 Go 静态交叉编译出来的，构建阶段不需要 QEMU 模拟 arm 跑编译器，运行也不需要模拟——真机直接跑。

## Web 管理界面

容器里没有 WebView2，但界面本身是普通网页（`web/` 那一套 HTML + JS），所以镜像会把它一起带上，用浏览器打开就行：

```
http://<宿主机>:8787/admin
```

- **在宿主机本机打开**（或用 `docker exec` / 反代到回环）：不用登录，`/admin` 直接跳到 `/admin/ui/`。
- **从别的设备打开**：先去「管理接口」那一节把 `ZEN_GATE_ALLOW_REMOTE_ADMIN` 设成 `1`，然后页面会先要求输入**主密钥**（就是 `/v1` 用的那个 `mainKey`）。登录后拿一个 12 小时有效的会话 cookie，`HttpOnly` + `SameSite=Strict`；换主密钥（`key/rotate` 或改 `ZEN_GATE_KEY`）会让已有会话立刻失效。

界面能做的事和桌面版一致：渠道登录/贴 Key、模型与聚合顺序、供应商、联网、签到、日志、用量、设置、导出/导入配置。少数只有桌面才做得成的动作会被换成等价路径：

| 桌面版动作 | 浏览器里的样子 |
| --- | --- |
| 导出配置 → 选保存位置 | 直接下载到浏览器的下载目录 |
| 导入配置 → 选文件 | 上传到数据目录的 `uploads/`，再由网关读它 |
| 「打开文件夹」「用默认浏览器打开链接」 | 打不开宿主机的资源管理器；链接改成新标签页打开 |
| 窗口最小化 / 最大化 / 拖拽 | 没有，那是浏览器的窗口 |

几个细节：

- 界面和 `/admin/api/*` 共用同一套权限：回环放行，远端要主密钥。界面内部走 `POST /admin/rpc`，语义等价于桌面版的消息桥，不会因为「在浏览器里」而多开一个口子。
- `ZEN_GATE_ALLOW_REMOTE_ADMIN=0` 时，远端打开 `/admin` 会拿到 403（`admin is loopback-only`），这是故意的。
- 反代（Nginx / Caddy）到公网时请把 HTTPS 一起配上，会话 cookie 不会自己变 Secure——明文 HTTP 暴露等于把主密钥明文送出去。
- 界面本身是静态资源，`curl` 也能验证：`curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/admin/ui/`（回环下应得 `200`）。

## 不用容器，直接跑网关

同一个入口也能在 Linux / macOS 上原生运行（单文件二进制，无外部依赖）：

```bash
CGO_ENABLED=0 go build -trimpath -o lamp-guide-server ./cmd/lamp-guide-server
ZEN_GATE_HOME=$HOME/.local/share/zen-gate ./lamp-guide-server
```

它不读任何桌面缓存，只认 `ZEN_GATE_HOME` 下的 `config.json`；日志直接打到 stdout，`Ctrl-C` / `SIGTERM` 退出。用 systemd 托管的话，`Restart=always` 加上一个指向数据目录的 `Environment=ZEN_GATE_HOME=...` 就够了。

## 快速开始

```bash
# 方式一：compose（推荐）
docker compose up -d
docker compose logs -f          # 启动那几行里有 API 地址和主密钥提示

# 方式二：手搓
docker build -t lamp-guide:latest .
docker run -d --name lamp-guide \
  -p 8787:8787 \
  -v lamp-guide-data:/data \
  lamp-guide:latest
```

`/data` 是网关的全部状态（`config.json`：渠道登录、供应商、主密钥、用量统计）。**卷不挂，重建即清零。**

首次启动的日志长这样（第一段是网关自己的日志，后面四行是这个入口打印的摘要）：

```
[2026-11-16 00:17:25.848] [INFO] {app} zen-gate 1.2.1 启动 (port 8787, proxy env)
[2026-11-16 00:17:25.851] [INFO] {app} dashboard ready at http://127.0.0.1:8787
lamp-guide-server 1.2.1 · go go1.26.11 · data /data
API      http://172.17.0.5:8787/v1  (OpenAI/Anthropic 兼容；其它设备填这个地址)
admin    http://127.0.0.1:8787/admin  (仅回环；ZEN_GATE_ALLOW_REMOTE_ADMIN=1 才接受主密钥的远端调用)
key      ofm-AbCd…  (ZEN_GATE_SHOW_KEY=1 可打印完整主密钥)
注意: 数据目录里没有任何渠道或供应商。...
```

容器里那行 `API` 打出来的是容器自己的网段地址，别照抄——对外要用宿主机的 IP 和发布出去的端口。

### 用 CI 构建好的镜像

这个仓库的 GitHub Actions 在每次 push 到 `main` 时会构建镜像并发布：

- 一定推送 GHCR：`ghcr.io/<owner>/<repo>:latest`，同时打一个 `:<短 sha>` 标签
- 仓库里配好 `DOCKERHUB_USERNAME` + `DOCKERHUB_TOKEN`（Secrets）和 `DOCKERHUB_REPO`（Variables）后，同一次运行会顺带推 Docker Hub

```bash
docker pull ghcr.io/3322786756/lamp-guide-docker:latest
docker run -d --name lamp-guide -p 8787:8787 -v lamp-guide-data:/data \
  -e ZEN_GATE_KEY=换成你自己的长随机串 \
  ghcr.io/3322786756/lamp-guide-docker:latest
```

现在这个包是公开的，`docker pull` 不需要登录。换成你自己的仓库时注意：新建的 GHCR 包默认私有，要先去仓库的 Packages 页面点一次 public，或者先 `docker login ghcr.io`。

### 在 Unraid 里安装

仓库里带了一份 Unraid 模板：[`unraid/lamp-guide.xml`](../unraid/lamp-guide.xml)。它用 bridge 网络、把 `/data` 指到 `/mnt/user/appdata/lamp-guide`，并把几个环境变量做成了表单字段。

两种装法：

```bash
# 1) 命令行下载到 Unraid 的用户模板目录，然后在 Docker 页「Add Container」里选它
mkdir -p /boot/config/plugins/dockerMan/templates-user
wget -O /boot/config/plugins/dockerMan/templates-user/lamp-guide.xml \
  https://raw.githubusercontent.com/3322786756/lamp-guide-docker/main/unraid/lamp-guide.xml
```

也可以直接在 Docker 页右下角的「Template」处粘贴上面的模板地址，效果相同。

模板里的几个字段：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| WebUI 端口 | `8787` | 宿主端口；容器内部固定监听 8787 |
| 数据目录 | `/mnt/user/appdata/lamp-guide` | 网关全部状态，务必在 appdata 里并被备份覆盖 |
| PUID / PGID | `99` / `100` | 数据文件的属主（Unraid 惯例 nobody:users）；容器启动时先按它改属主再降权运行 |
| 主密钥 | 空 | 留空则首次启动生成随机密钥，可从容器日志拿 |
| 允许远端管理 | `0` | 想从别的电脑用浏览器调 `/admin/api/*` 才改成 1（仍需主密钥） |

只想看网关活着没有、又不想开远端管理，可以让容器自己问自己（回环地址无需密钥）：

```bash
docker exec -it lamp-guide wget -qO- http://127.0.0.1:8787/admin/api/state
```

### 运行用户与数据目录权限

容器的入口脚本分两步走：

1. 以 root 起步，把 `ZEN_GATE_HOME`（默认 `/data`）的属主改成运行用户 —— 只在属主对不上时才改，不是每次启动都递归 chown；
2. 立刻 `su-exec` 降权，网关进程本身跑在运行用户名下，**服务进程不是 root**。

运行用户默认 `10001:10001`，可以用 `ZEN_GATE_UID` / `ZEN_GATE_GID`，或者 NAS 上常见的 `PUID` / `PGID` 指定：

```bash
docker run -d --name lamp-guide -p 8787:8787 \
  -v /mnt/user/appdata/lamp-guide:/data \
  -e PUID=99 -e PGID=100 \
  lamp-guide:latest
```

所以宿主机目录是谁建的都能起：`root` 建的、`nobody` 建的、别的容器的老目录都行，日志里会看到一行

```
docker-entrypoint: /data 当前属主 0:0，改成 99:100
```

如果你连「以 root 起步」都不接受，就在宿主机先把目录改好，再用 `--user` 指定同一个 uid，入口脚本会走非 root 分支、只做一次可写性检查：

```bash
sudo chown -R 10001:10001 /mnt/user/appdata/lamp-guide
docker run -d --user 10001:10001 -v /mnt/user/appdata/lamp-guide:/data ... lamp-guide:latest
```

日志里出现 `permission denied` 或「改属主失败」，说明数据目录所在的文件系统不让 root 改属主（少数网络挂载会这样），按上面的 `--user` 方案处理即可。

## 拿到主密钥

密钥就是所有 `/v1` 请求的凭据。三种拿法，任选：

```bash
# 1. 临时把密钥打进日志（看完记得改回 0；日志会留在宿主机上）
docker run --rm -e ZEN_GATE_SHOW_KEY=1 -v lamp-guide-data:/data ...   # 或在 compose 里改环境变量后 up -d

# 2. 直接读卷里的 config.json
docker compose exec lamp-guide cat /data/config.json | grep MainKey

# 3. 已经有密钥、只想换一个
curl -X POST -H "Authorization: Bearer $ZEN_GATE_KEY" \
  http://<host>:8787/admin/api/key/rotate
```

密钥长得像 `ofm-` 开头加 24 字节随机串，等同于账号密码：`config.json` 里还躺着各渠道的登录凭据，这个目录按密钥对待。

### 用自己的密钥

不想让容器随机生成、也不想从日志里去翻，就直接指定（自建部署、团队共用、接入既有客户端时更方便）：

```bash
docker run -d --name lamp-guide \
  -p 8787:8787 -v lamp-guide-data:/data --restart unless-stopped \
  -e ZEN_GATE_KEY=my-team-gateway-key-9f3a1c7b \
  lamp-guide:latest
```

- 设了 `ZEN_GATE_KEY` 就以它为准：启动时写进 `config.json`，**之后不带这个变量重启也沿用**，客户端的 Key 不会莫名其妙失效。
- 值里不能有空白或引号；短于 16 个字符照样接受，但会在 stderr 提醒一句——这个字符串就是 `/v1` 的唯一凭据。
- 想换就再启一次带新值的 `ZEN_GATE_KEY`，或者在运行中调 `POST /admin/api/key/rotate` 让网关自己生成一个新的。
- 介意密钥出现在 `docker inspect`/compose 文件里，就用 env 文件或密钥管理：

```bash
# env 文件（记得 chmod 600，并且别入库）
printf 'ZEN_GATE_KEY=%s\n' "$(openssl rand -hex 24)" > lamp-guide.env
docker run -d --name lamp-guide --env-file lamp-guide.env \
  -p 8787:8787 -v lamp-guide-data:/data lamp-guide:latest
```

## 把桌面应用里的配置搬进来

容器里没有界面，所以要复用已经在 Windows 上登录好的渠道，最省事的路径是「导出配置 → 挂进容器」：

```bash
# 在 Windows 上（桌面应用正在运行，KEY 是它的主密钥）
curl "http://127.0.0.1:8787/admin/api/export?secrets=1" \
     -H "Authorization: Bearer $KEY" -o config.json
# 注意必须带 ?secrets=1，否则导出的是掩码版，凭据会被当成「未配置」。
```

然后把 `config.json` 放到容器的 `/data/config.json`：

```bash
# compose：把 volumes 换成宿主机目录那一行（./data:/data），再
mkdir -p ./data && cp config.json ./data/config.json
docker compose up -d

# 或者具名卷
docker compose cp config.json lamp-guide:/data/config.json
docker compose restart lamp-guide
```

也可以反过来推：让容器先跑起来，再用 `POST /admin/api/import` 把配置推进去（需要 `ZEN_GATE_ALLOW_REMOTE_ADMIN=1` 加主密钥，见下节）。导入接口会校验并保留本机的绑定端口，掩码占位符会落成「未配置」而不是假凭据。

> 渠道登录态（尤其免费通道的 OAuth）会过期，过期后容器里没法点按钮重新登录：重新在桌面应用里登录一次，再导出一遍配置覆盖进卷里即可。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `ZEN_GATE_HOME` | `/data`（镜像内置） | 数据目录，配置/日志/用量都在这里 |
| `ZEN_GATE_PORT` | 配置里的端口，首次是 `8787` | 容器内监听端口；改这里记得同步改 `-p` |
| `ZEN_GATE_ALLOW_LAN` | `1` | 监听 `0.0.0.0`。设 `0` 只监听回环——容器里那样就没人连得上了 |
| `ZEN_GATE_ALLOW_REMOTE_ADMIN` | `0` | 允许非回环地址访问 `/admin/api`（凭主密钥），见下节 |
| `ZEN_GATE_SHOW_KEY` | `0` | 启动时把完整主密钥打进日志 |
| `ZEN_GATE_KEY` | 空 | 自定义主密钥；设了就写进 `config.json`，之后启动一直沿用它 |
| `TZ` | `Asia/Shanghai` | 时区，影响日志与用量统计的时间戳 |
| `PUID` / `PGID` | `10001` / `10001` | 容器内运行用户（NAS 惯例写法）；也可以写 `ZEN_GATE_UID` / `ZEN_GATE_GID` |

布尔量接受 `1/true/yes/on` 与 `0/false/no/off`；写别的值会在日志里提示并回落到默认值。

## 管理接口

`/admin/api/*` 原本是「只认回环」的：桌面应用从本机页面调它，这条约束同时挡住 DNS rebinding 和跨站请求。容器里每个请求都来自网桥地址（例如 `172.17.0.1`），回环判定永远不成立——所以默认情况下管理接口在容器里**不可用**，网关照常转发，只是改不了配置。

要用就走显式开关：

```yaml
environment:
  ZEN_GATE_ALLOW_REMOTE_ADMIN: "1"
```

打开后，远端调用方必须出示**主密钥**（`Authorization: Bearer <MainKey>` 或 `x-api-key`），并且 POST 的 `Origin`/`Referer`（如果带了）必须与请求地址同源——和原来的跨站规则一模一样，只是把「你在本机」换成了「你拿着主密钥」。回环调用方走的还是老逻辑，一点没变；没打开这个开关时，远端依旧一律 403。桌面版不会打开它。

```bash
KEY=$(docker compose exec -T lamp-guide grep -o '"mainKey": *"[^"]*"' /data/config.json | cut -d'"' -f4)
curl -H "Authorization: Bearer $KEY" http://<host>:8787/admin/api/state      # 看状态
curl -H "Authorization: Bearer $KEY" "http://<host>:8787/admin/api/usage.csv"  # 用量
curl -H "Authorization: Bearer $KEY" "http://<host>:8787/admin/api/export?secrets=1" -o backup.json
curl -H "Authorization: Bearer $KEY" -X POST --data-binary @backup.json \
     http://<host>:8787/admin/api/import
```

`/admin`（HTML）就是上面那套浏览器版管理界面。回环打开直接进；打开 `ZEN_GATE_ALLOW_REMOTE_ADMIN=1` 之后远端浏览器能看到登录页，用主密钥换会话。界面内部的每一次调用走 `/admin/rpc`，服务端在进程内按回环形状发给 `/admin/api/*`——所以「界面能用」不代表「管理面没关门」，权限还是那一套：回环放行，远端认主密钥。

> 安全提醒：`ZEN_GATE_ALLOW_REMOTE_ADMIN=1` 等于把管理面暴露给「知道主密钥的一切人」。别直接把 8787 挂到公网，套一层 HTTPS 反代（Nginx / Caddy / Traefik），并且定期 `key/rotate`。

## 给别的设备用

```bash
# 同一局域网里的手机 / 另一台电脑
curl http://<宿主机IP>:8787/v1/models -H "Authorization: Bearer ofm-..."
```

客户端里把 `Base URL` 填 `http://<宿主机IP>:8787/v1`，API Key 填主密钥即可；Anthropic 协议走 `/v1/messages`，OpenAI 协议走 `/v1/chat/completions` 和 `/v1/responses`。

只想本机用，就把 compose 的端口映射改成 `"127.0.0.1:8787:8787"`，此时远端连管理接口都没必要打开。

## 运维

```bash
docker compose logs -f                       # 日志走 stdout，由 Docker 收集
docker compose exec lamp-guide wget -qO- http://127.0.0.1:8787/health   # {"ok":true,"service":"zen-gate"}
docker compose build --pull && docker compose up -d                     # 升级镜像
docker run --rm -v lamp-guide-data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/lamp-guide-data.tgz -C /data .                        # 备份卷
```

镜像自带 `HEALTHCHECK`：容器状态变成 `healthy` 才代表端口通了、配置加载完了；`unhealthy` 先看日志里是不是渠道全部失效或端口被占。

## 常见问题

**启动日志说端口占用**：容器内 8787 被别的进程占了，或者宿主机端口没映射对。改 `ZEN_GATE_PORT` 的同时要改 `-p 新端口:新端口`（容器内端口就是配置里那个）。

**`/v1` 一律 401**：密钥不对。注意别把渠道的 API Key 当成网关主密钥——网关只认 `config.json` 里的 `mainKey`（以及 `agentKeys` 里的子密钥）。

**提示没有渠道或供应商**：卷是空的。按上面的「把桌面应用里的配置搬进来」走一遍。

**日志里有 `127.0.0.1:8095` 的绑定失败**：那是 Qoder 内置 hub 的可选端口，容器内就算占用了也不影响转发，可忽略。

**打开 `/admin` 是 403 / 一直停在登录页**：403 说明 `ZEN_GATE_ALLOW_REMOTE_ADMIN` 还是 `0`（远端只认回环）；停在登录页则是主密钥输错了，或者换了主密钥导致会话失效（刷新重登即可）。本机用 `docker exec lamp-guide wget -qO- http://127.0.0.1:8787/admin/ui/ | head -5` 能确认界面本身在不在。

**界面里点「导出」没反应**：浏览器拦了下载？换个浏览器或允许该站点下载；导出的文件是网关生成的一次性令牌（10 分钟内有效、只能下一次）。

**订阅/代理模式不生效**：见上文「镜像里有什么」——容器入口裁掉了 sing-box 侧车。要代理就在宿主机层面做（`HTTP_PROXY` 走宿主机的代理网关），或者别用容器。

**想让容器跟着系统代理出去**：`docker run -e HTTP_PROXY=... -e HTTPS_PROXY=...`，网关的上游请求会走标准库的 `ProxyFromEnvironment` 链路；具体是否生效取决于选中的渠道实现。
