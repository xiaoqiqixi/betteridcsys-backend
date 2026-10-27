# 后台版本更新与主机确认执行

> **先核对选定正常发行。** 本页对应Core 0.5.10；只有正常Release、完整六资产与来源校验通过才可操作，公开验收不代替目标主机、备份及升级审查。旧v0.5.6–v0.5.9的新prepare/stage仍因来源分叉拒绝；旧目录必须有真实收据，不能伪造。见[历史影响](../../docs/HISTORY_REWRITE.md)、[版本合同](RELEASE-v0.5.10.md)和[公开验收](../../docs/CORE_PUBLIC_PREPARE_ACCEPTANCE.md)。

管理员版本页面查询固定官方 `xiaoqiqixi/betteridcsys` 正常 Release、说明和兼容条件，并可明确生成有审计的运维交接。网页继续使用 `manual_host_handoff`：不会获得 root、sudo、运行目录写权限或服务控制能力。Core 0.5.2 增加由获准主机管理员单独启动的安装/升级执行器；管理员审查私密规格和计划，确认精确 SHA-256 后才实际操作。网页可读取单独导出的脱敏状态，交接、暂存、应用和业务验收是不同事实。

本页按Core 0.5.10说明更新流程，发行状态与门禁结果须查对应正常Release、构建及公开验收；不能用历史绿灯替代本版，也不能用prepare替代目标环境安装。Core与SMTP 1.1.0分别构建和核验，本次不准备新的SMTP版本。支付运行时`aster.payment/1`自Core 0.5.3提供，默认关闭；实际渠道仍须独立实现、构建和验收，没有官方真实渠道或渠道交易查询、撤销、退款及特定ACK能力，见[支付插件指南](PAYMENT_PLUGINS.md)。全新安装默认CNY，升级保留原币种、独立报价及历史金额，不自动换汇。批准的SMTP版本不证明worker正在运行；兼容判断不代表数据库、恢复或运营验收。没有查询结果时显示未检查/不可用，不把缓存当最新发布。见[版本合同](RELEASE-v0.5.10.md)与[插件说明](PLUGINS.md)。

## 版本格式与引导边界

raw 拆分分发从 Core 0.5.0 引入。已发布 0.4.0 网页/工具只理解旧四资产归档；0.5.0 虽理解 raw，其固定策略不含 0.5.1 新工具；0.5.1 校验器也不会自行更新为后续执行器的完整策略。旧正式附件保持原样，不删减策略、伪造版本或手改交接绕过。

首次使用0.5.4时要求核对并引导目标资源中的完整工具，这一边界继续适用于0.5.10。按[安装手册](INSTALL.md#1-核对来源与归档)核对选定正常Release的六资产、源码/构建来源，审查并引导`core-manager.py`、`core-operation.py`、`update-manager.py`、`plugin-manager.py`到本次root保护目录。prepare独立核验官方来源并组装新目录，不要求制造旧网页不支持的交接。已有旧实例只有满足真实回执、unit、配置、权限与schema检查才可接纳；不承诺任意旧分叉目录已实测升级。具体范围见[升级流程](UPGRADE.md)与[验证记录](VALIDATION.md)。

## 管理员网页操作

1. 明确点击“检查官方更新”，核对组件、真实当前版本、候选、提交、Release 和纯文本说明。只接受正常稳定 Core `vX.Y.Z` / SMTP `smtp-vX.Y.Z`，不提供 URL、命令或安装目录输入。
2. 选择候选并明确确认由主机运维继续，生成有审计的交接。服务端重新检查官方信息与本地状态；冲突、来源变化或审计失败不能显示成功。
3. 下载交接，通过受控途径交给获准主机管理员。交接有效期 30 分钟；`awaiting_host_operator` 表示尚未开始主机安装，失效或现场改变需重新检查。
4. 若配置了实际主机回执，查看独立状态区域；它不以网页交接推导安装成功，也不提供 root 执行按钮。

交接 JSON 未签名，包含内部请求/操作者标识，不是公开附件或任意安装许可。主机操作授权来自获准管理员对实际实例和计划的确认。网络中断、终端返回不确定或页面暂不可用时先核对审计/主机记录，不能自动重发或再次安装。

## 独立核验与暂存

`update-manager.py` 仍只提供 `stage/status`，没有 apply。它独立核对固定官方仓库、稳定标签/提交、资产摘要、BUILD/PROVENANCE、成功构建及包结构，然后保存 root 私密资产和 journal；不执行新程序、备份、迁移、切链接或管理 systemd。raw 交接为 schema 2、BUILD 为 schema 3；旧 schema 1 只对应旧归档。

```sh
sudo install -d -o root -g root -m 0700 /var/lib/aster-updates
sudo python3 /var/lib/aster-bootstrap/0.5.10/update-manager.py stage \
  --handoff /root/aster-update-REQUEST_ID.json \
  --state-directory /var/lib/aster-updates \
  --current-core-version 0.5.7 \
  --current-plugin-version 1.1.0
sudo python3 /var/lib/aster-bootstrap/0.5.10/update-manager.py status \
  --state-directory /var/lib/aster-updates --request-id REQUEST_ID
```

这是从运行中的 Core 0.5.8、已选 SMTP 1.1.0 升级到 Core 0.5.10 的模板；执行时须已有目标版本的真实有效交接，所有路径、版本、ID 须匹配现场，不能预造交接。交接文件 root 所有、0600、无链接，状态目录 0700、所有祖先受保护；首次建立前检查没有复用其他应用目录。已有请求不可覆盖，不删除 journal 后重试。`status` 重核已验证资产，输出始终说明尚未应用。

Core stage 缺备份时为 `backup_required`。携带既有文件证据时必须同时提供 `--backup`、`--backup-sha256`、`--backup-metadata`，对应已核验当前版本备份的 SQL gzip/实际摘要/自动 `BACKUP.json`。文件检查不证明 SQL 内容或隔离恢复。已配置插件且尚未独立核对时保留 `compatibility_review_required`；不能省略实际插件版本以绕过。`awaiting_host_operator` 仍只是等待主机处理。

stage 的请求目录含额外 journal，不能直接作为 `prepare --assets-directory` 的精确六资产输入。prepare 可独立下载，或使用另一个受保护的精确六资产目录；不修改既有暂存检查点。

## 主机管理员确认安装或升级

`core-manager.py prepare` 只负责来源核验/目录准备；`core-operation.py` 负责确认后的实际操作。新安装见 [INSTALL.md](INSTALL.md)，已有实例见 [UPGRADE.md](UPGRADE.md)。先准备系统依赖、真实 TLS、私密输入和恢复计划，创建规范规格，再执行：

```sh
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py plan \
  --spec /root/aster-upgrade/upgrade.json
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py apply \
  --spec /root/aster-upgrade/upgrade.json \
  --confirm-plan-sha256 REVIEWED_PLAN_SHA256
```

管理员逐项审查本次计划，再手工使用实际摘要确认；不把 plan 输出自动传给 apply。精确实例、候选/当前提交、工具、配置/秘密引用、插件与 TLS 摘要受约束。全新安装拒绝已有对象；升级核对当前版本/schema/插件，启用本站维护门禁、停止 API、验证新备份后才迁移与切换。系统依赖、公共证书、其他站点或无关服务不由本次操作接管。

```sh
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py status \
  --state-directory /var/lib/aster-operations --operation-id ACTUAL_OPERATION_ID
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py resume \
  --spec /root/aster-upgrade/upgrade.json \
  --confirm-plan-sha256 ORIGINAL_REVIEWED_PLAN_SHA256
```

主机管理员为每次确认的操作运行一次执行器，不安装持续监听网页请求的 root 服务。锁和 root 私密 journal 记录真实检查点；resume 必须使用原规格/ID/摘要并重新核验输入，不盲目重放不确定动作。公开状态 `needs_manual_recovery`（私密 journal 为 `recovery_required`）要求保留现场；程序回退须证明实际旧 schema 兼容并验证旧服务，维护状态保留供管理员判断。自 Core 0.5.3 引入支付数据库结构后，不能直接切回 0.5.2 程序，0.5.4 和目标 0.5.10 均保留这一回退边界。数据库备份不自动导入，迁移不自动降级，详见 [失败处理](UPGRADE.md#失败后的判断)。

## 网页只读主机状态

执行器规格可设置 `status_file`，例如 `/var/lib/aster-status/STATUS.json`；API 通过受控配置的 `HOST_OPERATION_STATUS_FILE` 读取相同路径。新安装可在规格中选择该导出，先建立 root 所有、API 可读的保护父目录；路径须在实例/配置/私密状态根之外。升级规格须匹配既有环境中的同一路径，先独立审查配置变更及服务重启，不能通过升级悄悄新增。路径不从 HTTP 请求接受。导出为单独 root 写入的脱敏 schema 1 回执，包含操作 ID、动作、目标/旧版本与提交、阶段、维护/迁移/服务/程序回退、HTTPS 与业务验收标志。它不含密码、DSN、完整环境、SQL、备份内容或私密 journal。

当前执行器每次原子导出均创建 root 所有、0644 的独立普通文件，不保留手工设置的 0640 或组归属。导出父目录须预先存在；父目录及全部祖先须 root 所有、不可组/其他用户写入、无 setuid/setgid/sticky 特殊位或链接，并允许 other 遍历（建议使用 0755）；仅运行组可遍历的 0750 目录不能通过当前执行器的计划检查。文件须单一硬链接、1–65536 字节。API 只有读权限，不得开放私密状态目录或把秘密放入公开回执。

管理员接口 `GET /api/v1/admin/updates/operation` 为只读、禁缓存；`unconfigured` 表示未配置路径，`absent` 表示尚无回执，`unavailable` 表示读取/信任/结构检查失败，`available` 才带实际回执。坏回执不会显示成功，不泄露私密失败详情。回执是本机上次观察，页面刷新不执行操作；终端失败、旧缓存或交接状态不能被转换为“已安装”。执行器的 `succeeded` 仍保留业务验收要求，需实际管理员登录和业务检查。

## 官方查询凭据与信任范围

确需私有仓库读取时，API 使用独立 `UPDATE_RELEASE_TOKEN_FILE`，建议 aster 所有、0400、祖先受保护；主机工具使用独立 root 0600 的 `--github-token-file`。只授予固定仓库 Contents/Actions 所需读取，不从网页输入、不自动创建长期高权限凭据。凭据/限流/网络失败显示不可用，不改成“已是最新”。

Bearer 值按不透明字符串处理，仅检查安全 HTTP 字符及 1–4096 字节上限；文件可有一个末尾 LF/CRLF，最多 4098 字节，不接受空白、嵌入换行或控制字符，不解析 JWT。依据 [RFC 6750 §2.1](https://datatracker.ietf.org/doc/html/rfc6750#section-2.1)和 [GitHub 安装令牌格式变更说明](https://github.blog/changelog/2026-04-24-notice-about-upcoming-new-format-for-github-app-installation-tokens/)，不假定旧短固定格式。

官方 TLS、GitHub asset 摘要与未签名 PROVENANCE建立来源/一致性约束，不是独立密码学发行签名，不能防御官方发行权限失陷。交接 32KiB、资源压缩 80MiB/展开 160MiB、单文件/原始程序 64MiB、备份压缩 256MiB/展开 2GiB、metadata 16KiB 等边界不通过临时参数绕过。实际云端证据只认 [VALIDATION.md](VALIDATION.md)的对应提交/run；不据文档或合成离线测试宣称用户主机、公共 CA、灾备或跨版本恢复已验收。
