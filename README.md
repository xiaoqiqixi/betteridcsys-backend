# 星屿云 · 财务系统后端

面向云服务业务的商品、客户和账务管理系统。提供商品配置、独立币种报价、订单与账单、账户权限、审计日志和插件接口，配合[财务前端](https://github.com/xiaoqiqixi/betteridcsys-frontend)构建客户门户与管理后台。

采用 Go + MySQL，原生 Linux 部署。默认中文、人民币，支持英文和美元；销售与账务由财务系统管理，计算资源通过独立插件连接云主控。

[使用文档](docs/README.md) · [API 文档](vps-billing/docs/CONTRACT.md) · [发行版本](https://github.com/xiaoqiqixi/betteridcsys-backend/releases) · [问题反馈](https://github.com/xiaoqiqixi/betteridcsys-backend/issues)

## 主要功能

| 模块 | 功能 |
| --- | --- |
| 商品管理 | 类型/地区/节点分类，新增与复制商品、规格编辑、独立定价、上下架 |
| 客户与权限 | 客户和管理员账户、角色管理、账户状态与会话控制 |
| 订单与账单 | 购买确认、单笔详情、状态管理、付款入口、历史商品与价格快照 |
| 站点运营 | 品牌与语言设置、默认币种、邮件配置、业务日志与审计 |
| 扩展接口 | SMTP、支付、资源插件，独立运行时身份与授权 |
| 运维工具 | 版本检查、主机升级工具、备份恢复、健康检查与 systemd 服务 |

不同币种独立定价，不自动换汇或重算历史订单。支付渠道需单独安装和配置；付款成功不等于虚拟机已开通。

## 运行环境

| 项目 | 要求 |
| --- | --- |
| 操作系统 | Ubuntu 22.04 / 24.04，amd64，systemd |
| 数据库 | Oracle MySQL 8.4 LTS |
| Web 入口 | Nginx，同源 HTTPS，可信 TLS 证书 |
| 部署方式 | 原生服务，无 Docker、Redis 依赖 |

运行发行二进制不需要 Go 或 Node.js。计算节点应独立部署，不与财务数据库混用。

## 一键安装

安装入口将随首个发行版本提供；当前没有已验证的新仓库一键命令，请勿用开发脚本代替生产安装。

安装向导按以下顺序配置：

1. 使用发行版提供的安装入口，自动下载并核验配套的后端与前端文件。
2. 按[原生安装手册](vps-billing/docs/INSTALL.md)配置专用数据库、域名、TLS 和匹配的前端文件。使用宝塔时阅读[宝塔部署](vps-billing/docs/BAOTA.md)。
3. 创建自己的管理员账户，按[首次使用](vps-billing/docs/GETTING_STARTED.md)设置站点、分类和商品。
4. 上线前完成备份与恢复演练，核对邮件、支付和资源插件的独立配置。

用户不需要安装编译工具。系统没有通用默认管理员密码。

## 运维与扩展

- [版本更新](vps-billing/docs/UPDATES.md) · [升级与回退](vps-billing/docs/UPGRADE.md) · [备份恢复](vps-billing/docs/BACKUP_RECOVERY.md)
- [日志与审计](vps-billing/docs/LOGGING.md) · [日常运维](vps-billing/docs/OPERATIONS.md)
- [插件管理](vps-billing/docs/PLUGINS.md) · [支付插件](vps-billing/docs/PAYMENT_PLUGINS.md) · [资源插件](vps-billing/docs/RESOURCE_PLUGINS.md)
- [云主控与 KVM 被控](https://github.com/xiaoqiqixi/betteridcsys-cloud) · [商品资源配置](vps-billing/docs/PRODUCT_CONFIGURATION.md)

## 开发与集成

[API 文档](vps-billing/docs/CONTRACT.md) · [插件开发](vps-billing/docs/PLUGIN_PROTOCOL.md) · [开发文档](docs/development/README.md)

## 许可证

[Apache License 2.0](LICENSE)。
