# 文档中心

[项目首页](../README.md) · [财务系统](../vps-billing/README.md) · [云主控与被控](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/README.md)

先按要完成的任务选入口；安装命令只在安装手册维护，接口字段只在接口文档维护，避免同一操作出现多份不同写法。

## 第一次使用

| 任务 | 阅读顺序 | 完成后应得到什么 |
| --- | --- | --- |
| 安装财务 | [原生安装](../vps-billing/docs/INSTALL.md)或[宝塔部署](../vps-billing/docs/BAOTA.md) → [首次使用](../vps-billing/docs/GETTING_STARTED.md) | 正常 HTTPS 入口、自己的管理员账户、可核对的安装回执；不是 demo 账户 |
| 发布商品 | [首次使用](../vps-billing/docs/GETTING_STARTED.md) → [分类管理](../vps-billing/docs/CATALOG_TAXONOMY.md) → [报价规则](../vps-billing/docs/FINANCIAL_MODEL.md) | 类型/地区/节点及已审核的商品、独立币种价格与客户购买入口 |
| 准备主控与节点 | [主控首页](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/README.md) → [主控入门](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/GETTING_STARTED.md) → [安装器](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/deploy/install/README.md) | 独立管理站点和节点准备结果；不把软件安装当作 KVM 已验收 |
| 联接财务与主控 | [产品配置](../vps-billing/docs/PRODUCT_CONFIGURATION.md) → [资源插件](../vps-billing/docs/RESOURCE_PLUGINS.md) | 固定产品配置、受限主控身份与可核对的资源任务；不自动改变付款状态 |

## 财务运营

| 工作 | 手册 |
| --- | --- |
| 管理客户、角色、订单及账单 | [操作入门](../vps-billing/docs/GETTING_STARTED.md) · [管理员能力与订单状态](../vps-billing/docs/ADMIN_MANAGEMENT.md) |
| 维护商品与售价 | [类型 → 地区 → 节点 → 套餐](../vps-billing/docs/CATALOG_TAXONOMY.md) · [币种、价格和历史约束](../vps-billing/docs/FINANCIAL_MODEL.md) |
| 设置品牌、中英文与默认币种 | [站点设置](../vps-billing/docs/SITE_SETTINGS.md) |
| 安装扩展、接入支付 | [SMTP 与插件管理](../vps-billing/docs/PLUGINS.md) · [支付接入](../vps-billing/docs/PAYMENT_PLUGINS.md) · [资源插件](../vps-billing/docs/RESOURCE_PLUGINS.md) |
| 排查问题、查看业务记录 | [日志与审计](../vps-billing/docs/LOGGING.md) · [日常运维](../vps-billing/docs/OPERATIONS.md) |
| 更新、备份与恢复 | [后台版本更新](../vps-billing/docs/UPDATES.md) · [升级与回退](../vps-billing/docs/UPGRADE.md) · [备份恢复](../vps-billing/docs/BACKUP_RECOVERY.md) |

## 主控与 KVM 运维

| 工作 | 手册 |
| --- | --- |
| 管理台登录、成员权限、实例与任务 | [主控入门](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/GETTING_STARTED.md) · [Console API 与授权](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/CONSOLE_API.md) · [账户变更与会话撤销](IDENTITY_OPERATIONS.md) |
| 准备计算宿主、镜像、存储和节点准入 | [被控运维](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/NODE_README.md) · [主控运维](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/deploy/controller-OPERATIONS.md) · [安装器](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/deploy/install/README.md) |
| 配置 VPC、路由与恢复 | [后台网络管理](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/NETWORK_MANAGEMENT.md) · [overlay 运维](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/deploy/OVERLAY_OPERATIONS.md) |
| 管理 IP 库存与公网出口 | [公网池与分配回收](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/PUBLIC_IP_MANAGEMENT.md) · [公网 NAT](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/PUBLIC_NAT_EGRESS.md) |
| 配置 VM 公网与入站规则 | [直绑与浮动 IP](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/VM_PUBLIC_IP_MANAGEMENT.md) · [浮动 IPv4 入站](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/PUBLIC_INGRESS.md) |
| 定位任务和日志 | [日志与审计](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/LOGGING.md) · [恢复合同](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/OVERLAY_DESIGN.md) |

## 二次开发

- **财务**：[本地运行与目录](../vps-billing/README.md) → [API](../vps-billing/docs/CONTRACT.md) → [财务模型](../vps-billing/docs/FINANCIAL_MODEL.md) → [安全边界](../vps-billing/docs/SECURITY.md)。
- **前端**：[UI API](../vps-billing/docs/UI_API_REFERENCE.md)与[界面规范](../vps-billing/docs/UI_GUIDELINES.md)；真实会话、加载/空/错误态与移动端都是交付要求，不用演示数据兜底。
- **插件**：[运行时通信](../vps-billing/docs/PLUGIN_PROTOCOL.md)、[支付插件](../vps-billing/docs/PAYMENT_PLUGINS.md)、[资源插件](../vps-billing/docs/RESOURCE_PLUGINS.md)。SMTP、支付、资源各用独立身份与授权。
- **主控**：[架构](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/ARCHITECTURE.md) → [管理台 API](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/CONSOLE_API.md) / [网络 API](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/NETWORK_API.md) → [开发检查](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/README.md)。
- **构建与交付**：[Core 发布流程](../vps-billing/docs/CI_RELEASE_AUTOMATION.md) · [Cloud 独立发布通道](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/RELEASE_CHANNEL.md)。

## 版本与验收

| 要核对的事实 | 入口 |
| --- | --- |
| 某版本改了什么 | [财务变更记录](../vps-billing/docs/CHANGELOG.md) · [主控变更记录](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/CHANGELOG.md) |
| 财务正式资产与公开下载 | [Core v0.5.10 Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.10) · [公开 prepare 复验](CORE_PUBLIC_PREPARE_ACCEPTANCE.md) |
| 主控与被控预发布下载 | [Cloud RC Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/cloud-v0.1.0-rc.1) · [公开资产/验签/plan复验](CLOUD_PUBLIC_ACCEPTANCE.md) |
| 哪个提交通过了哪些测试 | [财务验证](../vps-billing/docs/VALIDATION.md) · [主控验证](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/VALIDATION.md) |
| 旧版本为什么不能重新准备 | [历史归属调整与升级限制](HISTORY_REWRITE.md) |
| 哪些还没做完 | [工作包计划](EXECUTION_PLAN.md) · [逐项台账](execution-tracker.json) · [KVM 功能对照](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/KVM_ENHANCED_PARITY.md) |

带日期的执行记录是当时的证据，不会随 `main` 更新自动变成当前结果。新用户无需从这些记录开始；排查特定提交时，再查 [10 月 25 日](EXECUTION-20261025.md)、[26 日](EXECUTION-20261026.md)、[27 日](EXECUTION-20261027.md)。

## 文档约定

- 操作指南写清前提、实际入口、步骤、成功结果和失败处理；没有实现的能力明确列出，不写成可点击的操作。
- 示例域名、保留 IP、操作 ID 和摘要必须替换；没有统一默认生产密码，不在文档中保存秘密。
- 版本号必须与源码和插件兼容声明一致；正式下载以完整资产与来源核验为准，不复制未经核实的一键脚本。
- 包内文档保留构建时快照。跨系统文档与最新状态在本仓库查看；已发布包不为更新措辞而原地替换。
