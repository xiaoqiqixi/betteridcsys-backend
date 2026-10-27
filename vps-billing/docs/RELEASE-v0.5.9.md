# Core v0.5.9 发行记录与边界

Core **0.5.9** 已正式发布，正式对照基线为 [v0.5.8](RELEASE-v0.5.8.md)（schema 12）。[v0.5.9 Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.9) 固定源码 `4252fed37dfb4d4e7f621fbd0495a2cd2171c8b7`：[main CI 39719713456](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39719713456)、[Runtime build 39721645970](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39721645970) 和[独立发布器 39723864667](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39723864667) 均成功；六资产匿名下载和大小/SHA256/BUILD/PROVENANCE/完整13份迁移核验通过。旧标签、旧正文和资产不覆盖；这不表示主控/KVM或目标客户环境已经验收。原始失败、本地测试及未验证范围见 [VALIDATION.md](VALIDATION.md)。

## 本版财务功能

- 后台独立产品配置面板支持不关联主控、固定主控预设、固定自定义资源三种方式；配置与展示规格、价格、凭据分开。
- 新订单在事务内冻结私密产品配置快照，不回填旧订单，不覆盖原财务 `PlanSnapshot`。已有合法幂等重放仍返回原记录。
- 配置更新采用并发 revision，提升配置与报价确认修订，但不改金额、币种或独立报价；旧确认须重新读取并核对。
- 只有订单及账单均已付款，管理员才能准备唯一的手动任务。默认无 worker、无调度器；创建订单、保存配置或准备任务都不会发送开通请求。
- root 可使用受保护主机策略与专用数据库文件显式一次性下发，或查询原操作结果。Web 不读取端点凭据、不执行特权命令、不进行主控网络写入。发送结果不明保留 `uncertain`，不重发；HTTP 409 仅接受完整绑定 operation、order 和原请求摘要的既有结果。
- 使用本系统自有 `aster.controller/1`，公开稳定 Go 类型合同位于 `backend/controllerapi`，不包含财务数据库或付款操作。借鉴 IDCsmart 的分层思路，**不是对接其 API**。独立主控/KVM 另行开发，本财务发行不附带真实接收端、虚拟机或自动资源管理。

默认中文/CNY，保留独立多币种定价、精度、支付幂等、认证/CSRF、角色与所有权约束。没有真实收款、新支付渠道或业务邮件能力。付款、任务接受和 VM 已运行是不同事实，不能用界面成功提示代替后端及主控证据。字段、权限、主机命令及安全失败规则见 [PRODUCT_CONFIGURATION.md](PRODUCT_CONFIGURATION.md)。

## 数据库与回滚

新增 `013_product_configurations.sql` 的三个表：`product_configurations`、`order_product_configurations`、`provisioning_jobs`。本版要求 **schema 13 / 23 表**；001–012 原文与校验和不变。迁移不回填历史配置、不创建任务，不重写价格、订单、账单、支付、服务或财务快照。

从正式 0.5.8 升级应用 013；更早来源依次应用缺失的原迁移。先维护停写、停止旧 API、验证备份和隔离恢复，再迁移并同时切换匹配程序与网页。**0.5.8/schema12 程序不能直接运行 schema13 库**；不能删迁移记录、伪降 schema 或只换旧二进制。回滚须恢复已验证的升级前备份与匹配旧程序；执行器不自动降级或导入数据库。见 [UPGRADE.md](UPGRADE.md) 与 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md)。

## 独立版本和六资产

Core 为 `v0.5.9`；官方 SMTP 保留独立 **1.1.0**，`aster.smtp/1`、Core `>=0.4.0,<0.6.0`，不重发 SMTP。支付协议仍为 `aster.payment/1`，默认关闭。

正常 Core Release 必须恰好包含：

1. `aster-api-0.5.9-linux-amd64`
2. `aster-plugin-host-0.5.9-linux-amd64`
3. `aster-core-0.5.9-linux-amd64-resources.tar.gz`
4. `BUILD.json`
5. `PROVENANCE.json`
6. `SHA256SUMS`

资源唯一根目录为 `aster-core-0.5.9-linux-amd64-resources/`，含编译网页、完整迁移与配套工具，不含秘密、数据库、测试输出或额外 ELF。两个程序与资源须同版本；BUILD schema 3、`raw-binaries-v1`、源码、run/attempt、大小与 SHA-256 必须一致。未签名来源记录不是独立发行签名；固定官方来源仍须核验。

## 发布顺序与验收状态

1. 精确最终源码提交的完整 main CI 成功。
2. 新建未占用的 `v0.5.9` 标签指向该提交，不移动旧标签。
3. 标签触发正式 Runtime build，重新执行适用的真实 MySQL、原生 systemd/Nginx/HTTPS、升级/备份恢复/回滚、浏览器、依赖扫描与双构建门禁。
4. 默认分支独立发布器核验构建、来源和资产后公开正常 Release；不手工创建同名 Release 抢占，不盲目重试不确定写入。
5. 匿名下载六资产并校验逐字节大小、SHA-256、BUILD/PROVENANCE、精确源码与完整迁移；标签推送成功本身不是完成标准。

动态 Release 正文引用本次实际源码和 run/attempt，不预填成功。提交前分组实库、局部 race、portable 或人工截图不代替最终完整 CI；本轮 Go 依赖扫描、schema13 原生升级/恢复及完整合成浏览器矩阵已由上述正式运行通过。Ubuntu22/24 分别记录，不宣称所有 Linux 已支持。合成测试不代表真实主控、支付渠道、公共证书续期、灾备或生产容量验收。

原生 Linux amd64 部署，不用 Docker；运行无需 Go/Node.js。安装、宝塔和插件说明见 [INSTALL.md](INSTALL.md)、[BAOTA.md](BAOTA.md)、[PLUGINS.md](PLUGINS.md)；完整发布保护见 [CI_RELEASE_AUTOMATION.md](CI_RELEASE_AUTOMATION.md)。
