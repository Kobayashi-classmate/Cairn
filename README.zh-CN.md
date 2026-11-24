# Cairn

[English](README.md) | 简体中文

开源的 Linux 服务器管理面板。

> *Cairn*（石标）：登山路上由行人一块块垒起的石堆，每一座都独立矗立，连起来就标出了一条路。
> 每台服务器上的 Cairn 也是这样：各自是一个独立、完整的面板，又可以连成一张网统一管理。

**状态：设计阶段**（草案 v0.2），目前还没有可以运行的代码。

## 特点（设计目标）

- **多发行版**：一级支持 Debian 12/13、Ubuntu 22.04/24.04/26.04、Rocky/Alma/RHEL 9/10；二级支持 EL 8、openEuler 以及多个国产发行版；架构支持 amd64、arm64、loong64。
- **功能完整**：网站（PHP/Java/Node/Go/Python/.NET/反代/四层代理）、SSL、DNS、数据库、文件、终端、计划任务、Docker、应用商店、备份、监控告警、安全中心、WAF、邮局、FTP、多用户、节点管理等 30 个模块，可以按需开关。
- **默认安全**：Web 进程以非特权用户运行，root 操作只能通过一个动作固定的本地助手进程完成；不拼接 shell 命令；全程审计。
- **开箱即用**：不编译任何软件，常用建站环境大约一分钟装好。下载源可以自动测速选择，也可以手动指定；无论哪种方式，都严格安装用户所选的版本。
- **AI 基础设施**：所有模型都通过 API 接入，包括云厂商的服务，以及用户在本机或其他机器上自己部署的推理引擎。提供 AI 网关、运维助手、MCP 服务端。面板本身不自带模型服务。
- **多面板协作**：每台服务器的面板都能独立使用；节点管理只是多一个统一管理的入口，主面板停机时，各节点照常从自己的地址管理。
- **多语言**：首批支持 English 和简体中文，后续扩展。安装时选择默认语言，面板里可以随时切换。
- **零遥测**：不上报任何数据，不强制更新，不绑定账号。

## 技术栈

Go（静态编译，单二进制）· SQLite · Vue 3 + TypeScript + Tailwind CSS + Reka UI (shadcn-vue) · systemd · 产出 deb/rpm 两种安装包

## 卸载

卸载软件包不会碰网站：网站的文件、账号、单元（`cairn-app-*`）和 server 配置，数据库，从面板安装的软件，网站要用的文件（`/etc/nginx/cairn`、`/var/lib/cairn-acme`、`/opt/cairn/runtimes`），以及 firewalld 或 ufw 里的规则都会留下，网站照常运行。证书不再自动续期。系统账号 `cairn` 和 `cairn-unpack` 也会留下。

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

源文件头部统一标注：

```
// SPDX-License-Identifier: AGPL-3.0-only
```
