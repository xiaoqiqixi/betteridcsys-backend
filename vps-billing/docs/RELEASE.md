# 版本、构建与发布

Core 与运行时插件分别采用语义化版本、构建包和 Release。本页准备目标 Core `0.5.6` 的交付，发行状态与门禁结果须查对应正常 Release 及构建；本轮开发对照基线为正式 `0.5.4`，正式资产以 Release 实际清单为准。官方 SMTP 保持独立 `1.1.0`，本次不准备新的 SMTP 版本；`aster.smtp/1` 协议保持不变，兼容 Core `>=0.4.0,<0.6.0`。支付运行时协议保持 `aster.payment/1`，最低要求 Core `0.5.3`；0.5.4 沿用的支付数据库结构及回退边界继续适用于 0.5.6，见 [UPGRADE.md](UPGRADE.md)。发布前必须通过与源码对应的检查；实际执行结论见 [VALIDATION.md](VALIDATION.md) 和各 Release 的来源文件，不从历史绿灯推导新版本通过。

本项目在 `main` 维护。正常发布采用公开、非草稿、非预发布的 Release；这是版本交付状态，不宣称支付、主控/被控、商业运营或安全认证已完成。发布流程不部署服务器、不操作真实付款、不发送外部邮件。root `LICENSE` 和第三方版权材料保留；根 README 可随产品变化更新。

## 0.5.6 补丁范围

本候选保留客户首页、套餐比较、移动端导航与统一抽屉/弹窗；补齐客户及管理员角色、停用、密码重置与会话撤销，真实单笔订单详情，以及商品创建/复制/编辑、草稿/上架和多币种报价。生产日志使用脱敏 JSON/journald，业务审计支持权限隔离的游标分页。账户写入要求原因、版本与实际管理员复核，禁止危险自降权/自停用并保护最后一位启用管理员；订单不开放任意状态改写。

迁移 009 为审计索引，010 为账户状态/版本，原 001–008 不重写。全局商品/价格版本使旧报价失效，已成交金额、历史快照、支付幂等及安全约束保持；没有真实资源开通、Redis 或新支付渠道。Go 工具链为 1.27.3，运行原生 Linux amd64；Ubuntu 22/24 与其他发行版的验证范围分别报告，不把一个平台通过冒充全面支持。
视觉验收须实际打开本候选运行生成的桌面和手机截图，并覆盖两种语言、空态、长金额、导航及焦点；截图文件存在或自动化检查通过均不能替代人工看图。本节是候选范围，是否完成及仍有何限制以实际验证记录为准。

## 计划交付物与独立版本

下表和后文命令是发布准备模板；`v0.5.6` 标签及资产只有在发布完成并核验后才可用于官方安装或升级。

| 项目 | 标签          | 原始 Linux amd64 程序                                                | 单独资源包                                      |
| ---- | ------------- | -------------------------------------------------------------------- | ----------------------------------------------- |
| Core | `v0.5.6`      | `aster-api-0.5.6-linux-amd64`、`aster-plugin-host-0.5.6-linux-amd64` | `aster-core-0.5.6-linux-amd64-resources.tar.gz` |
| SMTP | `smtp-v1.1.0` | `aster-smtp-1.1.0-linux-amd64`                                       | `aster-smtp-1.1.0-linux-amd64-resources.tar.gz` |

每个 Release 另含 `BUILD.json`、`PROVENANCE.json`、`SHA256SUMS`：Core 共 6 个手工发布资产，SMTP 共 5 个。程序为未压缩的静态 ELF 可执行文件；下载后须按固定目标名安装和赋予执行权限。资源包不含 ELF，不能把它当作已经带程序的旧运行包。

Core 目标资源根目录为 `aster-core-0.5.6-linux-amd64-resources/`，包含 `web/`、`migrations/*.sql`、`docs/`、原生 `deploy/` 工具与样例、依赖清单及许可证。资源内 `core-manager.py prepare` 核验同版本资产并自动组装 `bin/aster-api`、`bin/aster-plugin-host`；管理员再审查私密规格和计划摘要，明确确认 `core-operation.py apply` 完成专用实例的初始化、迁移、配置和激活。MySQL 系统服务、Nginx 程序、TLS 证书和宿主环境须先准备；网页没有执行权限。单个 API 程序不能替代网页、配置、数据库或插件宿主。

SMTP 资源根目录为 `aster-smtp-1.1.0-linux-amd64-resources/`，只含 `manifest.json`、`LICENSE`、`README.md`、`THIRD_PARTY_NOTICES.json`、`SHA256SUMS`。管理工具同时核验资源与原始程序，再组装为不可覆盖的六文件版本目录；安装不批准、不启动。两种 Release 分别记录来源，不能用 Core 校验和证明 SMTP 程序。

新程序 Release **不手动上传源码 tar/zip**。源码从 Git 对应标签取得；GitHub 自带的源码下载由平台生成。旧四资产归档的受限读取兼容仍保留；正式文件不覆盖、重命名或重新构建。旧 Release 与草稿只按单独明确授权清理，保留当前 Core、SMTP 及全部 Git 标签和源码历史，下载可用性以实际 Release 清单为准。SMTP 发布不替代 Core 的 latest 标记。

新 `BUILD.json` 为 schema 3，分发类型 `raw-binaries-v1`，记录逻辑程序路径对应的原始文件名/SHA-256、资源包名，以及种类、版本、源码、时间、目标、工具链和关键输入。资源哈希由外层来源记录和校验清单绑定，BUILD 不自包含资源哈希。`PROVENANCE.json` 绑定仓库、标签、源码、Actions run/attempt、受信发布控制文件及各资产的大小/哈希。`SHA256SUMS` 校验同 Release 除自身外的原始程序、资源包、BUILD 和来源文件。校验和及未签名的来源记录证明一致性，不能替代下载来源认证、签名、完整 SBOM 或法律审查。

程序包不含真实配置、密钥、客户数据、数据库转储、测试日志或缓存。测试证据保持脱敏，不能为方便排障把完整环境变量或服务商回复附进 Release。

## 后台版本查询与安装交接

后台更新只接受固定官方仓库的正常 Core/SMTP Release，显示来源、版本/说明与兼容条件；草稿、预发布、不兼容或资料不完整的资产不能被当作可自动安装候选。管理员明确交接后状态为“待运维处理”，不更改运行服务。`update-manager.py stage` 只暂存，`core-manager.py prepare` 只准备目录；独立的 `core-operation.py` 在主机管理员审查规格并确认计划摘要后，才实际初始化、迁移和激活。后台只读受保护执行回执，数据库恢复/schema/维护窗口仍须核对，见 [UPDATES.md](UPDATES.md) 和 [UPGRADE.md](UPGRADE.md)。

归档摘要和PROVENANCE目前没有独立发行签名。官方下载与API的TLS来源校验不能描述为签名验真或防止仓库管理员恶意发布；发布和更新流程各自验证一致性，不能只复用网页给出的校验值。

## 从精确提交复现

需要 Git、GNU tar、gzip、sha256sum、Python 3，以及 BUILD 中相同的 Go/Node.js/npm。Core 构建使用前端锁文件；SMTP 只构建独立 Go 程序。依赖由 `go.sum` / `package-lock.json` 固定，下载渠道与缓存仍须可信。以下命令在仓库根目录运行，SHA 与版本必须来自同一已审查提交，输出目录尚不存在且位于仓库之外。准备 0.5.6 时须先完成对应版本声明并提交候选，再替换 `COMMIT_SHA`，不能用这些示例给未提交工作区或旧 0.5.4 程序改名：

```sh
vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind core --version 0.5.6 --output /workspace/core-a
vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind core --version 0.5.6 --output /workspace/core-b
diff -r /workspace/core-a /workspace/core-b

vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind smtp --version 1.1.0 --output /workspace/smtp-a
vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind smtp --version 1.1.0 --output /workspace/smtp-b
diff -r /workspace/smtp-a /workspace/smtp-b
```

脚本从 Git archive 建立快照，不打包未提交产品改动；执行的配方和辅助程序必须等于所选提交。Go 使用静态 Linux amd64 构建、`-trimpath` 和固定参数；归档排序、时间和 owner 固定，gzip 不写入随机时间。两次一致仅证明指定环境下的复现范围，不是跨工具链或跨平台的普遍保证。

版本必须与源码中 Core/SMTP 的版本声明一致。不能给旧程序随意改包名制造新版本。构建后检查 API 的 `-version`、SMTP 的 `--version`、ELF 目标、完整文件清单与许可证。插件宿主没有版本 CLI 参数，按 BUILD 中的构建信息及 SHA-256 核对身份；新的插件 manifest 还要检查协议、核心兼容区间和程序 SHA-256。

## 发布门禁

1. Go race/vet、前端单元与组件/类型/格式/生产构建、独立 HTTP/config 及发布负向测试通过。真实 MySQL 测试不得跳过。
2. MySQL 的历史升级、新支付结构、重复/已知中断恢复、checksum、并发幂等、权限、币种与历史快照回归通过；API 运行身份不能 DDL 或 GRANT。新增财务回归覆盖全新 CNY-only、旧 USD/默认项保留、0–6 位精确报价、精度锁、义务停用阻止、历史币种/套餐快照和审计回滚。
3. 真实执行器初始化、systemd/Nginx HTTPS、API 重启、正式 0.5.2 到新源码候选升级、进程中断恢复、启动失败后的兼容回退、未知 schema 保持维护，以及备份/空库恢复均有脱敏结果；浏览器保留沙箱并覆盖桌面/手机交互。发布前候选不得伪造官方来源。
4. 插件专项验证：未知文件不会执行，审批/哈希/路径/UID/协议拒绝边界正确；真实独立 SMTP 安装、版本切换、回退和崩溃恢复期间，既有 Core 程序哈希与存活进程不变，核心业务继续响应。测试只用回环合成 SMTP/TLS 与本地 HMAC 支付程序，不连接真实服务商；支付验证还包括取消并发、回调重放、交易唯一性及含支付记录的恢复。
5. Core、SMTP 各自双构建一致；原始 ELF 与资源分别校验，资源不含 ELF，组装后完整清单、权限、来源、版本和校验和通过。发布资产来自已验证提交，不能以工作区文件或历史候选冒充。
6. npm/Go 扫描及系统依赖检查分别报告范围。扫描网络或工具失败即为未完成；模块级告警不能被“可达函数 0”掩盖。

真正的公共 CA/域名/续期、数据库进程与整机重启、跨 schema 降级、独立实例/异地恢复、生产容量、真实服务商邮件和支付不在上述自动化结论内。单次 CI 通过也不替代这些项目。本文是放行要求；完成情况必须查实际运行记录。

## 发布流程与失败处理

受信默认分支控制程序验证标签、源码和成功构建 run，下载对应 artifact 作为数据核验，先创建暂存草稿、上传并复核全部资产，再变为正常公开 Release。只有最终 API 确认 `draft=false`、`prerelease=false`、标签/源码/附件一致后才能报告发布成功。具体信任边界见 [CI_RELEASE_AUTOMATION.md](CI_RELEASE_AUTOMATION.md)。

发布失败时保留实际状态、已上传资产清单与失败原因，检查是否已经执行后再处理。不要因标题可见就声称文件完整，也不覆盖已有同名 Release 或移动标签。缺少连接、权限或 API 支持时，交付完整本地程序包并明确记录发布阻塞，不制造长期凭据或绕过拒绝。

安装见 [INSTALL.md](INSTALL.md)，宝塔见 [BAOTA.md](BAOTA.md)，Core 升级见 [UPGRADE.md](UPGRADE.md)，插件单独替换见 [PLUGINS.md](PLUGINS.md)，恢复见 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md)。正式版本仍存在业务边界：真实渠道接入与商户验收、支付查询/撤销/对账/退款、特定渠道应答、VPS 控制器、财务账本、邮箱验证/找回、MFA、完整分页、监控值守及灾备能力尚需完成。
