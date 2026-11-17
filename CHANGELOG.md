# 更新记录

## 未发布

### Docker

- 新增无界面入口 `cmd/lamp-guide-server`：只启动网关，不带窗口、托盘与 WebView2，可在任意 Linux 宿主（含容器）运行。桌面版本不受影响。
- 新增多阶段 `Dockerfile`（静态链接、非 root、`/data` 数据卷、内置 `HEALTHCHECK` 打 `/health`）与 `docker-compose.yml`。
- 新增 `internal/zen/core` 的两个可选参数：`AllowLan` 让容器监听所有网卡（仅本次运行生效，不写回配置），`AllowRemoteAdmin` 允许远端的 `/admin/api` 以主密钥作为凭证；回环调用方的原有校验逻辑一字未改；这个选项在库里默认关闭，桌面版从不打开（无界面入口默认打开，见最后一条）。打开后远端浏览器可以打开 `/admin` 那张静态说明页（浏览器发不出凭据头），数据接口仍只认主密钥。
- 新增 `docs/DOCKER.md`：从桌面应用导出配置进容器、主密钥获取、环境变量、管理接口开放方式、反向代理与安全提示、常见问题。
- 镜像支持多架构：一次构建出 `linux/amd64`、`linux/arm64`、`linux/arm/v7`（树莓派 32 位），CI 用 buildx 推多架构清单；构建阶段靠 Go 交叉编译，不需要 QEMU 模拟 arm 跑编译器。
- CI 增加镜像发布作业：push 到 `main` 后自动构建并推送 GHCR（`ghcr.io/<owner>/<repo>`）；仓库里配好 `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` / `DOCKERHUB_REPO` 时同一次运行顺带推 Docker Hub。
- 无界面入口支持 `ZEN_GATE_KEY` 自定义主密钥：启动时写入 `config.json` 并沿用，省去从日志里翻随机密钥；运行中仍可用 `POST /admin/api/key/rotate` 更换。
- CI 增加 Linux 构建与容器冒烟作业（`go vet` 网关包、构建镜像、起容器验 `/health` 与卷内 `config.json`）。
- 新增 Unraid Docker 模板 `unraid/lamp-guide.xml`（bridge 网络、`/data` 指到 appdata、主密钥等字段表单化），并加 `unraid` 包测试锁住模板与镜像定义一致（端口 8787、卷 `/data`、环境变量名、图标与模板地址）。
- 新增容器入口脚本 `docker-entrypoint.sh`：以 root 起步只为把数据目录改成运行用户可写（修掉宿主机或 NAS 面板建的 root/nobody 目录导致的 `open store: open /data/config.json.tmp: permission denied`），随后 `su-exec` 降权，服务进程仍非 root；运行用户可用 `ZEN_GATE_UID`/`ZEN_GATE_GID` 或 NAS 惯例的 `PUID`/`PGID` 指定，拒绝以 root 起步时可用 `--user` 走非 root 分支。
- 新增浏览器版管理界面：容器里没有 WebView2，但界面本身是普通网页，`/admin` 现在直接提供同一套 UI（渠道、模型、聚合、签到、日志、设置）。`web/js/webbridge.js` 把桌面版的消息桥换成 `POST /admin/rpc`，`internal/zen/gateway/webui.go` 在服务端把每次调用按回环形状转给 `/admin/api/*`；权限沿用原规则——回环放行，远端开 `ZEN_GATE_ALLOW_REMOTE_ADMIN=1` 后用主密钥换 12 小时会话（`HttpOnly` + `SameSite=Lax`，换主密钥即刻失效）。导出改成一次性下载令牌，导入改成上传到 `<数据目录>/uploads/` 再读。桌面版走 WebView2 桥，行为不变。
- 远端打开 `/admin` 而 `ZEN_GATE_ALLOW_REMOTE_ADMIN` 仍是 `0` 时，不再回一句 `admin is loopback-only` 的 JSON，而是给一页说明（状态码仍是 403），写清楚要加的环境变量、Unraid 模板里在哪一项；Unraid 模板的「允许远端管理」默认值改成 `1`，NAS 上点 WebUI 就能直接看到登录页。
- 浏览器界面细节修正：没登录直接打开 `/admin/ui/`（或无斜杠的 `/admin/ui`）时，改为跳转到登录页（原来是一句 JSON 401）；js/css/图标等静态资源不再要求会话，登录页上的图标不再坏图；会话 cookie 从 `SameSite=Strict` 改成 `Lax`，从 Unraid 面板、书签这类跨站顶层导航过来不会每次都掉登录。
- 无界面入口（容器 / 直接跑 `lamp-guide-server`）不再默认「只给容器本机」：`ZEN_GATE_ALLOW_REMOTE_ADMIN` 默认改成开启，装好就能从局域网浏览器打开 `/admin` 输主密钥登录、用 `Authorization: Bearer <主密钥>` 调 `/admin/api/*`；回环调用方照旧免密钥。想锁回本机才设成 `0`（Dockerfile、compose、Unraid 模板与文档同步）。
- 排查更省事：`ZEN_GATE_ALLOW_REMOTE_ADMIN` 的值现在容忍引号和空格，`1/true/yes/on` 之外还会认 `enable(d)`、`0/false/no/off/disable(d)`；启动日志与 403 说明页都会写出这个进程实际读到的值，并说明「变量没进容器 / 值认不出 / 容器没重建」是哪种情况。
- 修掉容器里登录 PKCE 渠道（TRAE、Accio、LobsterAI 等）一直停在「正在等待上游确认…」：这类渠道的浏览器授权原本回调到 `127.0.0.1:<临时端口>`，而容器里的浏览器在另一台机器，它的回环地址回不到网关。现在浏览器不在本机时，回调地址改用浏览器正在用的那个地址 `http://<宿主机>:8787/channel/callback/<渠道 id>`，由网关自己接住（新增公开路径 `/channel/callback/<渠道 id>`，按每次登录新铸的 `state` 校验，对不上只回一页说明），换 token 时用的是同一个地址。Gemini 例外：Google 桌面客户端只登记了回环回调，仍需在跑网关的机器上开浏览器或走 ssh 隧道。

## Lamp Guide 1.2.1 — 2026-11-06

- 签到中心增加「一键签到」；执行中禁用按钮，展示逐账号结果，关闭自动执行后也可手动补签。
- 新增 AStudio、AutoClaw 国内/国际版、超级麦吉/MagiCrew 国际版，共 5 个渠道条目，覆盖插件导入或手工凭据、每日领取、余额、完整动态目录及聚合转发；尚无这些渠道的原生浏览器/短信登录向导，真实账号端到端验证待完成。
- 新渠道按账号隔离目录与推理凭据，支持到期续期或签发推理 Key，遵守聚合禁用与退出状态；AStudio 累计输出转换为增量，MagiCrew 保留文字积分倍率。
- 增加统一签到调度、北京时间执行时段、逐账号结果与本地去重、渠道开关、手动补签和失败退避。
- 接入 CodeBuddy 中国版、CodeArts、LobsterAI、TRAE、Loomy 及 MiniMax 国内/国际版的每日领取流程。严格检查上游业务结果，未知金额不编造。
- MiniMax 领取前支持到期 OAuth 凭据刷新，并保持令牌轮换前后的签到账号身份；领取使用当前网关代理并禁止凭据重定向。
- 移除把 WorkBuddy 国际版后台活跃对话报告为签到成功的调度；ZCode 验证码与未核实的 Qoder 中国版活动显示手动处理。
- 新增调度、重启去重、多账号、代理与重定向、业务响应、签名、刷新及管理 API 回归测试；整理新增渠道候选及接入限制。

## Lamp Guide 1.2.0 — 2026-11-05

这是 zen-gate 桌面重构版本的独立仓库首发，版本号独立于原版 zen-gate。

### 桌面与安装

- 产品名改为 Lamp Guide，加入手写 SVG 灯笼图标、Windows 应用图标和安装向导品牌资源。
- 日夜主题切换带过渡动画，保存主题偏好，响应减少动画设置。
- 提供 Windows x64 中文安装向导、快捷方式、卸载入口及 WebView2 离线运行环境。
- 沿用原账号数据目录、协议标识和客户端配置；开机启动指向已安装程序。

### 模型与聚合

- 聚合页加入模型搜索、启用优先排序、底部分页及更宽松的布局。
- 渠道参与轮换按钮可直接开关，支持自定义渠道。
- 展开模型后可批量启用或停用其全部渠道候选。
- 获取上游完整模型目录，包括积分模型；在渠道候选中显示上游返回的积分倍率。
- 改进模型别名归并，保留版本和变体边界。
- 修正已停用模型仍出现在聚合 API / ZCode 同步名单中的问题。

### 渠道与运行

- OpenCode 增加独立管理页面，集中配置代理及刷新额度。
- 修正 WorkBuddy 账号同步、身份及目录处理，改善 Qoder 登录契约与渠道额度缓存。
- 增加模型禁用、聚合成员关系、目录刷新、请求重试与配置持久化等回归验证。
- 更新检查改用 Lamp Guide 仓库，保留自定义更新地址；继续通过安装包升级。

完整项目来源及组件声明见 [THIRD_PARTY.md](THIRD_PARTY.md)。
