# Cairn

[English](README.md) | 简体中文

开源的 Linux 服务器管理面板。

> *Cairn*（石标）：登山路上由行人一块块垒起的石堆，每一座都独立矗立，连起来就标出了一条路。
> 每台服务器上的 Cairn 也是这样：各自是一个独立、完整的面板。把多个面板连起来统一管理，是计划中的功能。

**状态：尚未发布。** 前两个里程碑（P0 平台底座、P1 建站核心）的功能已经实现，界面已经重构。目前还没有发布任何版本，正在准备全面的真机验收。在那之前，请自己构建安装包（见[安装](#安装)），只在测试服务器上使用。

## 已经可用

- **发行版**：Debian 12、13，Ubuntu 22.04、24.04、26.04，Rocky Linux、AlmaLinux、RHEL 9、10；amd64 和 arm64。这些发行版都在容器和虚拟机里测试过（RHEL 用 Rocky Linux 和 AlmaLinux 代表）；arm64 的安装包已经构建，但还没有在机器上运行过。在 Enterprise Linux 上 SELinux 保持 enforcing。
- **网站**：静态网站；PHP 网站，多个 PHP 版本并存，每个网站一个 PHP-FPM 池和两个系统账号；反向代理；Node.js、Python、Go、Java、.NET 应用，作为 systemd 服务运行；以及基于 nginx stream 模块的 TCP、UDP 四层代理。
- **TLS 证书**：通过 ACME 签发（Let's Encrypt、ZeroSSL、Google Trust Services 或自定义 CA；HTTP-01，或经 Cloudflare、DNSPod、阿里云、华为云、Route 53、GoDaddy、火山引擎、西部数码、RFC 2136 做 DNS-01），也可以上传；网站和面板自己都能用，自动续期。
- **软件**：nginx、PHP 及其扩展、MariaDB、MySQL、PostgreSQL、Redis、Valkey、Node.js、Python、Go、Java（Temurin）、.NET，以及解压 7z 和 RAR 用的 unar。软件包先用发行版自己的软件源，没有时用上游官方的软件源（发行版没有的 PHP 版本用 sury、ondrej PPA 或 Remi）。编程语言运行时用官方构建，按各项目官方发布元数据里的校验值核对。安装前面板会检查各下载源有没有这个确切版本、速度如何（运行时可以自己选下载源），然后锁定确切版本安装。服务可以在面板里启动、停止、重载，配置文件可以在面板里编辑。
- **数据库**：MariaDB 或 MySQL、PostgreSQL、Redis 或 Valkey：库、用户和授权、远程访问、转储和恢复（恢复以库自己的用户身份执行），以及 Redis 的键浏览器。Enterprise Linux 上目前只支持 MariaDB 和 Redis 或 Valkey。
- **文件和终端**：文件管理器，支持断点续传、在线编辑、压缩解压、回收站和从网址下载；Web 终端，可以保存常用命令。
- **计划任务、日志、备份**：基于 systemd timer 的计划任务（Shell 脚本、访问网址、备份）；系统日志、网站日志和 SSH 登录记录；网站、数据库和目录备份到本地或 S3 兼容存储，可选 age 加密，按日、周、月规则保留。
- **防火墙和 SSH**：在 nftables、firewalld 或 ufw 上管理端口规则、信任和封禁的地址、禁 ping 和端口转发。每次修改如果没有通过新连接确认，60 秒后自动回滚。SSH 设置和公钥；修改 SSH 端口时，旧端口一直保留，直到新端口被外部连上为止。
- **主机**：主机名、时区、swap、系统更新、自动安全更新，以及系统软件源的镜像。
- **账号和 API**：两步验证（TOTP，带恢复码），三种角色（管理员、运维、审计），API 令牌和 OpenAPI 描述，审计日志，以及邮件和 Webhook 通知。
- **面板自身**：签名的在线更新，可以回滚；项目的官方消息；拉取这些数据可以关闭。English 和简体中文；浅色、深色和跟随系统三种配色；可以用键盘操作，也可以在手机上使用。

## 计划中

已有设计，尚未实现：

- Docker、应用商店、WordPress 管理；上游已停止维护的版本放在容器里运行。
- AI：所有模型都只通过 API 接入（云厂商的服务，或你在本机、其他机器上部署的推理引擎），提供 AI 网关、运维助手和 MCP 服务端。Cairn 不会自带模型服务。
- 监控告警、更多通知渠道、安全中心、WAF、日志分析、邮局、FTP 和 SFTP 账号、DNS 解析记录管理、迁移工具。
- 节点管理：一个面板管理其他面板，每台服务器仍然可以从自己的地址管理。
- 更多发行版（安装脚本已经接受 Enterprise Linux 8 和 CentOS Stream 9、10，但没有测试过；openEuler 等以后再做）、loong64、更多语言。
- WebAuthn 登录。

## 安装

目前还没有发布任何版本，所以没有可以下载的安装包。想现在试用，就在一台装有 Docker 的机器上构建安装包（每个构建步骤都在容器里执行）：

```sh
make build package                # amd64；arm64 加 ARCH=arm64
```

然后把适合目标系统的安装包从 `dist/` 复制到测试服务器上安装：

```sh
sudo apt-get install ./cairn_*_amd64.deb      # Debian、Ubuntu
sudo dnf install ./cairn-*.x86_64.rpm         # Rocky Linux、AlmaLinux、RHEL
sudo cairn setup-link                         # 打印一次性的初始化链接，用来创建第一个管理员
```

面板以 HTTPS 监听安装时随机选定的端口。手动安装软件包不会动防火墙，可以在面板的"防火墙"页面开启。

正式发布时会附带安装脚本 `install.sh`。它校验发布的签名校验清单，用 apt-get 或 dnf 安装软件包，不添加任何软件源（`--with-repo` 则改为添加 Cairn 的签名软件源）；询问面板的默认语言（`--lang en|zh-CN`）；列出哪些端口保持放行、哪些正在监听的端口会被关闭，然后启用防火墙（`--no-firewall` 跳过）。在终端里运行时会先询问，并且要求你另开一个 SSH 连接确认还能登录，否则重新关闭防火墙。以后的版本通过面板的在线更新安装。

## 安全

- Web 进程 `cairn-web` 以非特权用户 `cairn` 运行。需要 root 的操作都经过 `cairn-helper`：它只在本机的套接字上监听，只接受一组固定的动作，参数都经过校验。
- 外部程序都以参数数组的方式启动。用到 shell 的只有助手内部一段固定的包装脚本，以及你自己写的内容：应用的启动命令和计划任务的脚本。
- 浏览器的会话要同时具备 Cookie 和一个只有面板自己的页面才持有的密钥。终端和下载使用一次性票据；服务器上的网站收不到面板的 Cookie。
- 对服务器和账号的修改都记入审计日志。
- 防火墙的修改没有确认就会回滚；修改 SSH 端口时旧端口一直保留，直到新端口被外部连上，所以操作失误不会把你锁在外面。
- 零遥测：不上报任何数据，不强制更新，不需要绑定账号。面板只从项目拉取签名的公开数据（官方消息、版本索引），这一项可以关闭。

### `root_access = "never"` 管什么

服务器上的 root 可以在 `/etc/cairn/config.toml` 里设置它；文件里已经有 `[security]` 一节时，改那一节里的 `root_access`，不要再加一节。面板改不了这个设置。改完后重启 `cairn-helper`。

```toml
[security]
root_access = "never"
```

它会关掉文件管理器的全盘访问、root 终端、以 root 身份运行的计划任务和对 root 公钥的修改，并让服务的配置文件在面板里只能看、不能改。

它**不能**保证在每台服务器上都挡住 root。面板的管理员，或者控制了 Web 进程的攻击者，仍然可以以普通登录用户的身份开终端、添加 SSH 公钥、建计划任务；而能用 `sudo` 的用户就能成为 root。很多云镜像会建一个能免密 `sudo` 的默认用户。在这样的服务器上，如果要依靠这个设置，请先收回登录用户的免密 `sudo`。

## 卸载

卸载软件包不会碰网站：网站的文件、账号、单元（`cairn-app-*`）和 server 配置，数据库，从面板安装的软件，网站要用的文件（`/etc/nginx/cairn`、`/var/lib/cairn-acme`、`/opt/cairn/runtimes`），以及 firewalld 或 ufw 里的规则都会留下，网站照常运行。证书不再自动续期。系统账号 `cairn` 和 `cairn-unpack` 也会留下；面板为安装软件添加的软件源同样留下：`/etc/apt/sources.list.d`、`/etc/apt/keyrings`、`/etc/apt/preferences.d` 里的 `cairn-*` 文件，或者 `/etc/yum.repos.d` 里的 `cairn-*.repo` 及其在 `/etc/pki/rpm-gpg` 里的公钥 `RPM-GPG-KEY-cairn-*`。

| 内容 | `apt remove cairn` | `apt purge cairn` | `dnf remove cairn` |
|---|---|---|---|
| 程序和面板自己的服务单元 | 删除 | 删除 | 删除 |
| 配置（`/etc/cairn`） | 保留 | 删除，但 `/etc/cairn/apps` 里应用网站的环境变量文件保留 | 保留 |
| 面板数据（`/var/lib/cairn`：数据库、密钥、上传文件） | 保留 | 删除 | 保留 |
| 本地备份和数据库转储（`/var/lib/cairn/backups`） | 保留 | **保留**（设置 `CAIRN_PURGE_BACKUPS=1` 时删除） | 保留 |
| 助手进程的状态（`/var/lib/cairn-helper`：备份计划及其存储桶设置、证书、更新包） | 保留 | 删除 | 保留 |
| 计划任务（`/etc/cairn-cron`、`cairn-cron-*` 单元） | 保留，已停止 | 删除 | 保留，已停止 |
| Cairn 的防火墙（nftables 表 `cairn` 和 `cairn_forward`、`/etc/cairn-firewall`） | 表在重启前仍然生效，文件保留 | 删除 | 同 `apt remove` |

- `apt remove` 或 `dnf remove` 之后重新安装，Cairn 沿用保留的数据，并重新启用计划任务和防火墙。
- `apt purge` 之后重新安装是一个新面板；保留下来的备份在"文件"页的备份目录里，数据库转储在"数据库"页里。备份计划和存储桶设置已经删除，存储桶里的副本不受影响。加密的备份要用你建计划时自己保存的私钥解密：Cairn 从不保存私钥。
- 连备份一起清除：`sudo CAIRN_PURGE_BACKUPS=1 apt purge cairn`（变量要写在 `sudo` 后面，`sudo` 会丢掉其它环境变量）。已经清除过的：`sudo rm -rf /var/lib/cairn`。
- RPM 没有清除这一步。要像 `apt purge` 那样删掉 `dnf remove` 留下的内容：

```sh
sudo rm -rf /etc/cairn-cron /etc/systemd/system/cairn-cron-*.service /etc/systemd/system/cairn-cron-*.timer
sudo systemctl daemon-reload
sudo nft delete table inet cairn; sudo nft delete table inet cairn_forward
sudo rm -rf /var/lib/cairn-helper /var/lib/cairn-unpack /etc/cairn-firewall /etc/sysctl.d/60-cairn-forward.conf
# Keeps app sites' environment files (/etc/cairn/apps) and the backups;
# add "sudo rm -rf /var/lib/cairn" to delete the backups too.
sudo find /etc/cairn -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} +
sudo find /var/lib/cairn -mindepth 1 -maxdepth 1 ! -name backups -exec rm -rf {} +
```

## 许可证

[GNU Affero General Public License v3.0](LICENSE)（`AGPL-3.0-only`）。

本项目自己的源文件以一行 SPDX 标注开头，按各语言的注释写法书写：

```
// SPDX-License-Identifier: AGPL-3.0-only
```

第三方代码保留各自的许可头。`web/src/components/ui` 里的界面组件基于 shadcn-vue，标注为 `SPDX-License-Identifier: MIT`，见[它们的许可证](web/src/components/ui/LICENSE)。

## 技术栈

Go（三个静态链接的程序：`cairn-web`、`cairn-helper` 和命令行 `cairn`）· SQLite · Vue 3 + TypeScript + Tailwind CSS + Reka UI (shadcn-vue) · systemd · deb 和 rpm 安装包
