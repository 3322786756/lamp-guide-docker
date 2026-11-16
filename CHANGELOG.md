# 更新记录

## 未发布

### Docker

- 新增无界面入口 `cmd/lamp-guide-server`：只启动网关，不带窗口、托盘与 WebView2，可在任意 Linux 宿主（含容器）运行。桌面版本不受影响。
- 新增多阶段 `Dockerfile`（静态链接、非 root、`/data` 数据卷、内置 `HEALTHCHECK` 打 `/health`）与 `docker-compose.yml`。
- 新增 `internal/zen/core` 的两个可选参数：`AllowLan` 让容器监听所有网卡（仅本次运行生效，不写回配置），`AllowRemoteAdmin` 允许远端的 `/admin/api` 以主密钥作为凭证；回环调用方的原有校验逻辑一字未改，远端仍默认拒绝。打开后远端浏览器可以打开 `/admin` 那张静态说明页（浏览器发不出凭据头），数据接口仍只认主密钥。
- 新增 `docs/DOCKER.md`：从桌面应用导出配置进容器、主密钥获取、环境变量、管理接口开放方式、反向代理与安全提示、常见问题。
- 镜像支持多架构：一次构建出 `linux/amd64`、`linux/arm64`、`linux/arm/v7`（树莓派 32 位），CI 用 buildx 推多架构清单；构建阶段靠 Go 交叉编译，不需要 QEMU 模拟 arm 跑编译器。
- CI 增加镜像发布作业：push 到 `main` 后自动构建并推送 GHCR（`ghcr.io/<owner>/<repo>`）；仓库里配好 `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` / `DOCKERHUB_REPO` 时同一次运行顺带推 Docker Hub。
- 无界面入口支持 `ZEN_GATE_KEY` 自定义主密钥：启动时写入 `config.json` 并沿用，省去从日志里翻随机密钥；运行中仍可用 `POST /admin/api/key/rotate` 更换。
- CI 增加 Linux 构建与容器冒烟作业（`go vet` 网关包、构建镜像、起容器验 `/health` 与卷内 `config.json`）。
- 新增 Unraid Docker 模板 `unraid/lamp-guide.xml`（bridge 网络、`/data` 指到 appdata、主密钥等字段表单化），并加 `unraid` 包测试锁住模板与镜像定义一致（端口 8787、卷 `/data`、环境变量名、图标与模板地址）。
- 新增容器入口脚本 `docker-entrypoint.sh`：以 root 起步只为把数据目录改成运行用户可写（修掉宿主机或 NAS 面板建的 root/nobody 目录导致的 `open store: open /data/config.json.tmp: permission denied`），随后 `su-exec` 降权，服务进程仍非 root；运行用户可用 `ZEN_GATE_UID`/`ZEN_GATE_GID` 或 NAS 惯例的 `PUID`/`PGID` 指定，拒绝以 root 起步时可用 `--user` 走非 root 分支。

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
