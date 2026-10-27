# 插件扩展与独立 SMTP 安装

Core 自 `0.5.3` 提供 SMTP 与支付独立运行时插槽，`0.5.10` 新增资源插槽。插件是单独构建和运行的程序，通过受保护的 Unix socket 与 Core 通信；安装兼容插件无需重编 Core。Core 只读取元数据，主机管理员核验程序、兼容范围和哈希并明确批准后，才可启动专用非特权服务。没有网页上传、自动下载或通用命令执行入口。

官方 `email.smtp` 插件版本为 `1.1.0`，使用 `aster.smtp/1`，支持 Core `>=0.4.0,<0.6.0`。Core 没有内置 SMTP 传输回退。支付采用 `aster.payment/1`，具体渠道由开发者按[支付插件接入指南](PAYMENT_PLUGINS.md)实现；官方未提供真实收款渠道，默认关闭。资源使用 `aster.resource/1`，见[资源插件](RESOURCE_PLUGINS.md)；它不负责安装独立主控或计算被控。SMS 和对象存储尚无运行时插槽。

以下流程使用经核验的 Core `0.5.10` 与独立 SMTP `1.1.0`；两者分别核对正常 Release、完整资产和来源，不从版本号推定下载可用。SMTP 支持管理员配置与单封固定测试邮件；业务通知、邮箱验证、找回密码和发送队列尚未提供。

## 程序、审批与服务

```text
/opt/aster/current/bin/aster-api
/opt/aster/current/bin/aster-plugin-host
/opt/aster/plugins/email.smtp/1.1.0/{plugin,manifest.json,...}
/etc/aster/plugins.json
/run/aster-smtp/plugin.sock
```

Core 只被动发现版本化 manifest，不执行未知文件、不运行其 `--version`、不因文件出现启动服务。主机管理员安装并明确批准某个版本；策略固定 manifest SHA-256、客户端/插件 UID、socket 及获准 SMTP 主机/端口。Core 每次调用重新验证受保护路径和程序摘要，再通过同一 Unix 连接握手并发送；不必重编或重启 Core。

独立 `aster-smtp.service` 以 `aster-smtp` 身份运行，通过 `aster-plugin-host` 校验并执行已批准的文件描述符。API 身份为 `aster`；两者只共享 `aster-plugins` socket 访问组，插件不能加入 `aster` 组。发布目录和策略归 root，运行身份不可写；Core 没有 sudo 或服务管理权限。

这套边界隔离进程崩溃及 API 秘密，不是任意恶意代码沙箱。SMTP 插件有本次凭据、内容和出站能力；必须审查发行者、代码与包来源。策略主机/端口白名单是应用检查，不是操作系统出口防火墙，也不保证 DNS 指向固定地址。更严格的出口限制由主机管理员独立设计。

## 1. 安装独立包

先完成 [Core 安装](INSTALL.md)，核对版本与 [Release 来源](RELEASE.md)。从 SMTP Release 取得原始程序 `aster-smtp-1.1.0-linux-amd64`、资源 `aster-smtp-1.1.0-linux-amd64-resources.tar.gz`，以及 BUILD、PROVENANCE 和 SHA256SUMS；**不要**把 Core 包或 GitHub 自动源码归档当作插件包。核对独立包的来源提交、兼容区间、外层 SHA-256 和许可证。

Core 首次安装已创建 `aster-plugins` 组并授予 API socket 访问。先核对组与 API 账号，再创建独立无登录插件身份；升级自旧 Core 时按 [升级说明](UPGRADE.md)先准备共享组。已有 `aster-smtp` 账号时先核对其 UID、组关系和既有用途，不能覆盖或改用其他业务身份：

```sh
getent group aster-plugins
id aster
sudo useradd --system --no-create-home --home-dir /nonexistent \
  --shell /usr/sbin/nologin --gid aster-plugins aster-smtp
id aster
id aster-smtp
```

API unit 中的 `SupplementaryGroups=aster-plugins` 需要该组已经存在；首次启用这项 unit/环境配置需重启 API，使进程取得组访问权。之后只更新插件无需重启 API。检查插件不能读 `/etc/aster/api.env`、会话/数据库秘密和 SMTP 加密密钥；不能为连通 socket 给这些文件开放读取。

在非网页目录完成下载验证，插件管理程序以下命令由 root 执行。输入归档须为 root 所有、普通文件、无额外链接且不可组/其他用户写入；所有祖先目录也受 root 管理。可在确认未覆盖既有暂存文件后，将已核对的包放在 `/var/lib/aster-install/`（root:root 0755），文件 root:root 0644。不可直接指向普通下载用户可改写的文件。SHA 是从可信发布信息核对的完整 64 位摘要，不能从未经验证的未知包自行计算后当作来源证明；资源与程序均须放入上述受保护路径；下列 `RESOURCE_SHA256`、`BINARY_SHA256` 必须分别替换为经核实的摘要：

```sh
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  verify /var/lib/aster-install/aster-smtp-1.1.0-linux-amd64-resources.tar.gz --sha256 RESOURCE_SHA256 \
  --binary /var/lib/aster-install/aster-smtp-1.1.0-linux-amd64 --binary-sha256 BINARY_SHA256
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  install /var/lib/aster-install/aster-smtp-1.1.0-linux-amd64-resources.tar.gz --sha256 RESOURCE_SHA256 \
  --binary /var/lib/aster-install/aster-smtp-1.1.0-linux-amd64 --binary-sha256 BINARY_SHA256
```

`verify` 只读检查，`install` 只安装新版本，不批准、不启用、不执行程序、不管理服务。资源包只有五个普通文件，不含程序；安装器与原始程序组装后，版本目录只有六个普通文件：`plugin`、`manifest.json`、`LICENSE`、`README.md`、`THIRD_PARTY_NOTICES.json`、`SHA256SUMS`。安装器分别验证资源及原始程序的外层摘要、包内摘要、清单、静态 Linux amd64 ELF、版本/协议兼容、路径和大小；拒绝额外文件、重复项、软/硬链接、穿越、特殊归档扩展和已有版本覆盖。每版目录不可原地更新，旧版本保留用于回退。

## 2. 选择、批准与启动

在 `/etc/aster/api.env` 配置非秘密路径：

```ini
PLUGIN_DIRECTORY=/opt/aster/plugins
PLUGIN_POLICY_FILE=/etc/aster/plugins.json
```

准备原生 SMTP unit，并在首次修改 API unit/环境后重启 API。审核 service 中路径和目标主机的 systemd 支持，保持标准输出/错误关闭及权限限制：

```sh
sudo install -o root -g root -m 0644 \
  /opt/aster/current/deploy/systemd/aster-smtp.service /etc/systemd/system/aster-smtp.service
sudo systemd-analyze verify /etc/systemd/system/aster-api.service /etc/systemd/system/aster-smtp.service
sudo systemctl daemon-reload
# 仅首次加入共享组或修改 API 环境/unit 时执行。
sudo systemctl restart aster-api
```

先读取策略 revision；策略尚不存在时首次选择用 `0`。后续每次写入先读 `status` 并传当前 revision，冲突不会覆盖他人的修改。以下 `SMTP_HOST:SMTP_PORT` 须替换成获准的确切主机和端口；每个目标使用一次 `--target`，最多八个，不支持 URL、通配符或凭据：

```sh
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 status
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  select --id email.smtp --version 1.1.0 \
  --worker-uid "$(id -u aster-smtp)" --client-uid "$(id -u aster)" \
  --socket /run/aster-smtp/plugin.sock --target SMTP_HOST:SMTP_PORT --revision 0
```

`select` 总是写入关闭状态。核对输出中的新 revision、批准版本和目的地后，明确启用并启动服务；`CURRENT_REVISION` 必须替换为最新值：

```sh
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  enable --revision CURRENT_REVISION
sudo systemctl restart aster-smtp
sudo systemctl status aster-smtp --no-pager
```

包出现、安装成功、策略批准、服务运行、后台配置有效、真实 SMTP 接受是六个不同事实。管理员 API 的 `POST /api/v1/admin/plugins/check`（空 JSON `{}`、会话、来源与 CSRF）可主动检查握手，不传 SMTP 凭据、不发信。`GET /api/v1/admin/plugins` 是被动清单；`approved` 只代表本地审批和文件验证，不证明进程或上游可用。

## 3. 独立更新与回退

1. 保存现有 Core 程序哈希、插件版本/包摘要、策略 revision 与配置记录，处理所有在途测试；不要通过自动重发确认结果。
2. 按第 1 节核验并安装另一个兼容版本到新目录。安装本身不切换服务，不能覆盖或修改旧程序。
3. 用最新 revision `select --version NEW_VERSION`，再次明确列出 UID/socket/获准目标。该步骤关闭插件；核对后执行 `enable` 和 `systemctl restart aster-smtp`。
4. 主动检查握手、后台状态和 Core 的健康/业务。核对 Core 程序哈希/PID 没有因插件更新改变；SMTP 测试只能面向已获准目标。
5. 如果新版本失败，读取最新 revision，用 `rollback` 选择已安装的较旧兼容版本，再重启 SMTP 服务。回退保留当前启用状态、UID、socket 与目标；如果当前已关闭，仍需明确启用。

```sh
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  rollback --version OLD_PLUGIN_VERSION --revision CURRENT_REVISION
sudo systemctl restart aster-smtp
```

旧 SMTP 1.0.0 仅支持 Core `<0.5.0`，不能用于 Core 0.5.x。SMTP 1.1.0 兼容 Core 0.4.x 与 0.5.x，可作为升级桥接版本；但已发布的 Core 0.4.0 管理工具不支持新拆分输入，桥接前须独立核验并使用新版管理工具，不能假定旧工具自动得到新能力。正式文件不原地覆盖；Git 标签和源码历史保留，旧 Release 的下载可用性以实际保留清单为准。

Core 和 SMTP 版本不一一对应；独立补丁版可以在 manifest 声明的范围内替换。超出 `core_max_exclusive` 必须停止调用，先审查新 Core/协议/插件兼容关系。不得放宽校验、改 manifest 后自称原包或用 symlink 选择插件版本。

当前只承诺协议与声明区间的检查，没有所有未来版本的兼容保证，也没有 LTS/SLA。删除旧版本前先确认没有选中、没有在途调用且恢复策略不再需要它；管理工具不会自动清理历史版本。

## 4. 停用与故障处理

停用先原子关闭策略，再停止服务；使用最新 revision：

```sh
sudo python3 /opt/aster/current/deploy/scripts/plugin-manager.py \
  --root /opt/aster/plugins --policy /etc/aster/plugins.json --core-version 0.5.10 \
  disable --revision CURRENT_REVISION
sudo systemctl stop aster-smtp
```

也可在后台关闭 SMTP 业务设置。已开始的请求可能持有旧审批/凭据；关策略或停止进程不能撤回已接受邮件。若发送后断线、崩溃、超时或返回非法响应，核心按可能已发送处理，不自动重试；先核对接受记录。停用不删除数据库凭据、加密密钥、旧版本或策略记录。

systemd 对插件崩溃间隔五秒恢复，60 秒内最多三次启动，达到频率限制则保持失败。模板限制内存 192 MiB、CPU 配额 50%、任务数 64、文件描述符 64；这些是运行边界，不是容量保证。检查 `systemctl status/show` 的 PID、退出状态、重启计数以及程序/策略/目录权限；插件 stdout/stderr 不进 journal，不能通过打开完整请求日志排障。Core 连续三次连接/协议失败短暂熔断五秒，不重发原邮件；插件缺失、坏包或失败不会阻止 Core 启动、账号、订单和账单服务。策略损坏时工具会拒绝写入，应保留现场并停止插件服务，不能手改摘要掩盖问题。

## 官方 SMTP 配置与测试

完成核准的 Core 安装并核对管理员 SMTP 设置 API 可正常读取，再按安装手册准备 [独立加密密钥](INSTALL.md#可选smtp-加密密钥)，由管理员在扩展中心保存 SMTP 设置。配置可在安装插件前保存，保存不会连接服务器。要发送，必须同时满足：兼容插件已安装/批准/运行、主机端口在策略中、密钥与加密凭据匹配、有效配置、后台启用、管理员会话/来源/CSRF/revision 校验。

| 字段                                | 约定                                                        |
| ----------------------------------- | ----------------------------------------------------------- |
| `revision`                          | 后台设置版本，冲突 409；与主机策略 revision 分开            |
| `enabled`                           | 默认 false；保存启用不等于插件/目的地已批准                 |
| `host` / `port`                     | DNS/IP 主机、1–65535；不填 URL，须与策略的确切主机/端口一致 |
| `tls_mode`                          | `starttls` 或 `implicit_tls`；都强制验证 TLS                |
| `from_address` / `from_name`        | 单个 ASCII 裸邮箱与纯文本名，禁止头部注入                   |
| `username` / `password`             | 只写；留空/省略表示保留，不回显                             |
| `clear_username` / `clear_password` | 同时清除且关闭，不能同时提交新值                            |

更换主机、端口或 TLS 模式必须完整替换两项凭据，或同时清除并关闭。不可解密的旧密文只能完整替换/清除；新随机密钥不能恢复旧密文。数据库使用 AES-256-GCM 保存凭据，密钥只由 Core 读取；插件仅接收本次解密配置，不接收密钥、数据库 DSN 或会话秘密。恢复与轮换见 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md#smtp-密钥备份恢复与轮换)。

管理读取返回 `plugin_available/plugin_status/plugin_version/destination_allowed`。本地 `approved` 状态不是连通证明；`configured` 表示配置与解密有效，清单 `ready` 仍不证明邮箱投递。未批准插件或目的地时前端禁用测试并显示原因，Core 也独立拒绝调用。

传输最低 TLS 1.2，验证系统信任链及主机身份；不允许跳过验证、明文认证或 STARTTLS 降级。验证 TLS 后才使用 PLAIN/LOGIN；不支持匿名中继/OAuth。测试仅一个收件人、固定主题与纯文本正文，没有 HTML/附件/批量/队列。每个 Core 进程最多一个在途测试，启动间隔至少 30 秒；RPC 握手上限两秒、发送调用上限十二秒，不包含本地文件校验耗时，最终审计最多两秒。

Core 不自动重试 RPC。SMTP 传输仅对 MAIL 前已明确的暂时失败最多重连一次；MAIL 开始后不自动重发。`accepted` 表示 SMTP 服务器接受，非收件箱证明；`SMTP_DELIVERY_UNCERTAIN`、断网或响应丢失时先核对记录，禁止盲目重发。审计仅记录请求与结果分类，不记录凭据、收件地址、正文或上游原始回复。真实服务商、SPF/DKIM/DMARC 与投递不由回环 TLS 测试证明。

## 支付渠道扩展

支付插件使用独立 manifest、商户审批、UID、service 和 socket，不能把 SMTP 的 `smtp.send` 用于支付。开发者负责渠道接口和验签；Core 根据保存的订单、账单、金额、币种精度及所有权核对已验签证据，并在数据库事务中处理重放、渠道交易唯一性和账务状态。浏览器回跳或结账链接不代表付款完成。

客户结账和服务商通知已有专用接口。支付尝试先于渠道调用持久化，结果不确定时不会自动创建第二次付款；取消也会排除已有支付尝试。安装工具对支付启停及回退须使用 `--kind payment`，停用或回退不会删除在途记录。完整协议、合成示例、原始通知与通用应答合同见[支付接入指南](PAYMENT_PLUGINS.md)。当前未提供渠道查询、撤销、退款、对账或资源开通，特定明文／XML／签名应答还需扩展协议。

`PAYMENT_PROVIDER=payment.disabled`、`EMAIL_PROVIDER=email.disabled` 和 `INTEGRATIONS_CONFIG_FILE` 是保留的旧编译期配置边界，不安装或启用运行时插件。当前服务器只允许两个禁用 ID 的空配置；不要改为真实渠道 ID 或 `email.smtp`。运行时选择以受保护的 `plugins.json` 审批为准，支付状态通过 `GET /api/v1/admin/plugins` 查看；这些审批不会授予插件数据库或服务管理权限。

## 资源插件

本轮 main 另提供受保护、独立进程的 `resource.aster-cloud` worker，支持通过本系统插件管理器安装/审批/回滚以及原持久化资源 job 的 host-only 派发；不属于支付渠道，也不是 Web 任意执行入口。它只读取专用 systemd credentials，不读取财务数据库。见 [RESOURCE_PLUGINS.md](RESOURCE_PLUGINS.md) 的构建、安装、配置、对账和兼容性说明。旧公开 Release 尚不含该协议，真实 KVM/生产 service 验收与离线包校验分开记录。
