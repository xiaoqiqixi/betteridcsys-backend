# Aster Billing API（星屿云）

Go HTTP API、MySQL 8.4/InnoDB 存储和显式 demo 内存模式。当前正式 Core 为 0.5.2，主干 0.5.3 尚未发行；支持可配置运营身份、简体中文/English、登记币种独立报价与订单/账单。全新安装仅 CNY，升级保留旧 USD。支付渠道由开发者按 [支付适配器合同](../docs/PAYMENT_PLUGINS.md)集成；官方默认关闭支付，SMTP 为单独安装的运行时插件。完整 API 约定见 [CONTRACT.md](../docs/CONTRACT.md)，实际验证见 [VALIDATION.md](../docs/VALIDATION.md)。

## 本机演示

使用受支持、已更新的 Go 工具链；模块最低声明为 1.26.0（现有 `x/crypto v0.56.0` 安全更新的上游最低要求），当前 CI 固定 1.27.3，实际发布版本记录在 BUILD.json。工具链仅用于构建，不要求 Ubuntu 22 的系统预装同版本 Go；原生静态发行程序运行不需要 Go 或 Node.js。前端开发版本见 package.json/锁文件。

```sh
cd backend
go mod download
go test -race ./...
go vet ./...
go build -buildvcs=false -o ./bin/aster-api ./cmd/server
APP_MODE=demo APP_ORIGIN=http://localhost:5173 ./bin/aster-api
```

默认监听 127.0.0.1:8080。前端同源代理 /api，禁止开放 demo 或 Vite 到公网。只在显式 APP_MODE=demo 可用的公开测试账号：

| 用途                   | 邮箱              | 密码              |
| ---------------------- | ----------------- | ----------------- |
| 客户                   | demo@aster.local  | DemoCustomer!2026 |
| 管理员                 | admin@aster.local | DemoAdmin!2026    |
| 另一客户（所有权测试） | other@aster.local | DemoOther!2026    |

demo 无初始服务或假已付账单；创建订单只生成待付款记录，重启丢失内存数据。HTTP demo cookie 保持 HttpOnly/SameSite=Strict，但没有 Secure。默认生产模式不创建这些账号，不能通过切到 demo 绕过配置错误。

## 配置

| 环境变量                                     | 作用                                                                                               |
| -------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| APP_MODE                                     | production（默认）或显式 demo                                                                      |
| LISTEN_ADDR                                  | 默认 127.0.0.1:8080；原生 systemd 模板使用 127.0.0.1:18080                                         |
| APP_ORIGIN                                   | 精确 HTTPS 来源，无路径/尾斜杠，如 https://billing.example.com                                     |
| SESSION_SECRET / SESSION_SECRET_FILE         | 独立随机秘密，至少 32 字节；优先受限文件                                                           |
| MYSQL_DSN / MYSQL_DSN_FILE                   | Go MySQL DSN；生产优先验证证书的 tls=true                                                          |
| MYSQL_ALLOW_INSECURE                         | 默认 false；原生同机 Unix socket 须显式 true，并核验 socket 权限、账号与目标；不得据此启用明文 TCP |
| TRUSTED_PROXY_CIDRS                          | 直接可信代理精确 CIDR；空值不信任转发地址                                                          |
| BOOTSTRAP_EMAIL / NAME / ROLE                | CLI 创建用户；role 默认 customer，管理员需显式 admin                                               |
| BOOTSTRAP_PASSWORD / BOOTSTRAP_PASSWORD_FILE | 唯一 16–72 字节强口令，不用 demo/示例值                                                            |
| PAYMENT_PROVIDER / EMAIL_PROVIDER            | 保留 payment.disabled / email.disabled；后者不是受管理 SMTP 总开关                                 |
| SMTP_ENCRYPTION_KEY_FILE                     | 可选32字节原始私密文件；未设置不能发送，无效则拒绝启动；不支持内联秘密值                           |
| PLUGIN_DIRECTORY / PLUGIN_POLICY_FILE        | 受保护版本目录与 root 明确审批策略；被动发现不执行程序                                             |
| UPDATE_RELEASE_TOKEN_FILE                    | 可选受保护只读 GitHub 凭据文件，不从网页输入；未知来源不可用时不绕过                               |
| INTEGRATIONS_CONFIG_FILE                     | 可选受限本地 JSON；当前禁用适配器无需配置                                                          |

FILE 形式优先。生产拒绝弱/示例会话秘密、无 DSN、非 HTTPS 来源、无效代理 CIDR/未知模式。强制 TLS 时拒绝 skip-verify、preferred 和明文回退。不要在源码、命令行、聊天或日志中存密码/DSN。

生产 cookie 是 __Host-aster_session，Secure、HttpOnly、SameSite=Strict、Path=/。API 端口不公开，可信反向代理覆盖 X-Real-IP，客户端原始转发头不可信。仅从配置的直接 peer CIDR 接受该头，IPv4/IPv6 地址归一化用于限速；不信任 X-Forwarded-For。不能使用 0.0.0.0/0 或任意私网作为代理信任。

## 数据库、迁移与身份

数据库更新保留历史金额和快照，并新增持久化支付尝试与结算去重记录。工具通过独占迁移锁、校验和与结构预检验证版本；MySQL DDL 无法整体回滚，升级前须停写并验证备份与隔离恢复，不能删除记录手动降级。具体步骤见 [安装](../docs/INSTALL.md)和 [升级](../docs/UPGRADE.md)。

以独立 DDL 身份执行 `aster-api -migrate`，API 用只有本库 SELECT/INSERT/UPDATE/DELETE 的 DML 身份。CLI 仍需完整生产配置。以 BOOTSTRAP_* 配置执行 `aster-api -create-user` 创建测试用户；没有公开注册、密码找回、自助改密、MFA、角色修改或完整账号管理 API。详细配置和权限步骤见 [INSTALL.md](../docs/INSTALL.md)。

全新只有 CNY 注册与示例报价，默认不可售；升级保留既有 USD 价格/历史/合法默认项。下单只读取 plan_prices 的登记币种独立价格，不使用旧 plans.price_minor/currency。币种、套餐和报价均启用才能购买；后台提供现有套餐价格批量编辑，不提供套餐规格创建/修改。代码/精度/停用义务及快照规则见 [财务模型](../docs/FINANCIAL_MODEL.md)，勿用直接 SQL 绕过应用约束。

## 会话与下单

1. GET /api/v1/session 获取 cookie 和 CSRF。
2. POST /api/v1/auth/login 携带 JSON email/password 与 X-CSRF-Token；成功后使用新 token。
3. GET /currencies 读取币种元数据，仅选择 enabled 项；GET /plans?currency=CODE 读取独立报价和 price_revision，省略币种使用已配置默认。
4. POST /orders 携带 plan_id、hostname、显式 currency、`quote:{price_minor,decimal_places,price_revision}` 和唯一 Idempotency-Key（8–128 个 ASCII 字母/数字/_.:-）。quote 必须来自已展示报价，变化返回 409 PRICE_CHANGED 且不创建记录。旧客户端可省略 quote，按执行时价格执行；新客户端始终提供。
5. 浏览器变更请求均需 CSRF；支付渠道专用通知路径另行验签。存在 Origin 时必须精确匹配 APP_ORIGIN。未知 JSON 字段（含顶层价格/owner/role）拒绝，quote 仅作为服务端价格的确认条件，不允许客户端定价。
6. 注销撤销原会话并轮换匿名 token；会话 8 小时到期，数据库保存 token 哈希。

请求 locale 使用受支持的语言，界面独立选择 zh-CN/en，默认中文。locale 不决定货币；报价/统计按显式 currency 查询。客户数全局，服务/账单计数和金额按币种。列表历史可以混合登记币种，每条记录自己的币种，不跨币种求和。

同用户同幂等键同请求返回原订单；改变 currency/plan/hostname 冲突。MySQL 使用唯一请求键和事务；死锁/锁超时最多有限次重试。网络结果不明时复用原键及原请求。订单、账单、幂等记录、审计和 outbox 原子提交；金额、币种和运营身份快照之后不可被设置/价格更新改写。已完成重放优先返回原历史，quote 不改变旧幂等指纹。新订单另存 plan_snapshot，旧缺失规格/公司身份不从当前配置补造。

金额为最小货币单位整数，按每条记录的 currency_format.decimal_places（0–6）解释；单项和汇总不得超过安全整数上限 9007199254740991；客户端格式化不能通过浮点除法丢失分值。没有汇率、充值钱包或换汇。

## 财务管理接口

公开 GET /currencies，管理员 GET/POST /admin/currencies 和 /admin/plan-prices；写入需要 CSRF、严格字段、revision 和事务审计。登记最多 32 个三个大写字母代码，precision 0–6，使用后永久锁定；不能删除代码，有 pending_payment 订单、unpaid 账单或非 terminated 服务不能停用。价格批量输入精确正十进制字符串，不自动舍入或换汇，套餐总开关跨币种。完整 DTO、错误与上限见 [CONTRACT.md](../docs/CONTRACT.md)。

## 站点设置与支付边界

GET /site-settings 公开展示子集；管理员 GET/POST /admin/site-settings 读写完整设置。写入校验角色、CSRF、revision，设置与审计同事务；冲突返回 409。只接受纯文本、预设 logo/theme 和受限 HTTPS 链接，不接受任意脚本/HTML/CSS、远程抓取、文件上传或服务商秘密。展示地址不控制 DNS、证书或 APP_ORIGIN。

管理员 GET /admin/invoices/{id} 提供直接只读详情；客户 GET /invoices/{id} 仍按所有权查询，避免用最多 1,000 条列表筛选漏掉旧账单。

POST /invoices/{id}/checkout 验证所有权和持久化账单，先保存支付尝试再调用获准的独立运行时插件。主干通过 `aster.payment/1` 接入，替换兼容插件无需重编 Core；具体真实渠道由开发者维护。专用 POST /payment-callbacks/{providerID} 验签后，由 Core 事务比对业务绑定、去重并原子记录付款。浏览器回跳不记账，超时保持未确认，在途尝试阻止取消。没有插件时返回 503 PAYMENT_NOT_CONFIGURED。自动开通、退款与查询对账尚未提供，完整合同及合成程序见 [PAYMENT_PLUGINS.md](../docs/PAYMENT_PLUGINS.md)。

## 官方 SMTP

`plugins/smtp` 是独立版本 1.1.0 的运行时程序，协议 aster.smtp/1、兼容 Core >=0.4.0,<0.6.0；核心没有内置 SMTP 传输回退。它经 root 审批及不同 UID 的 Unix socket 握手后只处理本次 TLS 传输；核心通过 `GET/POST /admin/email-settings` 管理 revision、权限、审计和 AES-256-GCM 凭据，`POST /admin/email-settings/test` 仅发送固定测试内容。读取仅返回凭据存在标志，用户名/密码不回显。SMTP 密钥与数据库分开保护，具体权限、一次性生成、备份及手动轮换见 [安装手册](../docs/INSTALL.md#可选smtp-加密密钥)和 [备份恢复](../docs/BACKUP_RECOVERY.md#smtp-密钥备份恢复与轮换)。

SMTP 默认关闭，保留 `EMAIL_PROVIDER=email.disabled`，不要改为 `email.smtp`。设置保存后生效，密钥文件变更须重启。只有独立程序已安装/批准/运行且精确目的地获准，配合已保存启用配置与当前 revision 才能测试；每进程 30 秒限速且仅一个在途请求。`accepted` 只证明 SMTP 接受；不确定结果先核对后再决定是否重试。未接入通知、邮箱验证或密码找回，真实服务商投递不能由本地 TLS 测试证明。完整字段和错误见 [API 约定](../docs/CONTRACT.md)。

## 测试

```sh
go test -race -v ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false ./cmd/server
```

真实 MySQL 测试只在显式配置 ASTER_MYSQL_TEST_DSN 时执行；专用库名必须含 _test，绝不能连接生产。使用受限进程环境注入测试 DSN，避免命令行暴露秘密。测试有 DDL/DML，需要一次性库；检查测试代码的清理范围，保留未知数据库不动。

```sh
# ASTER_MYSQL_TEST_DSN 已由受限测试环境提供。
go test -race -v ./internal/billing -run TestMySQL
```

测试结果的“跳过”不等于通过。HTTP/config 独立回归在 ../tests，完整发布/恢复门槛见 [RELEASE.md](../docs/RELEASE.md)。列表仍上限 1,000 且无分页/完整导出；审计非不可篡改账本；登录限速为进程内；没有后台会话清理、异地备份/PITR 或部署监控。上述边界不会因本轮测试通过而自动消失。

## 安装前只读检查

在准备好的完整版本目录中，使用与实际服务对应的受保护配置执行 `aster-api -preflight`（或同义的 `-schema-status`）。它输出脱敏 JSON：本地配置结果及 MySQL 8.4、所选数据库、连接类型、已应用迁移/checksum、待迁移项和管理员计数。状态区分 `empty`、`pending_migrations`、`current`、`incompatible`，不会调用迁移、建用户或绑定服务监听。空表检查不代表账号授权、隐藏对象或服务就绪的完整证明；操作前仍须核对目标和维护窗口。

迁移、创建首个管理员及服务启动保持独立明确步骤；本地配置检查在数据库变更前执行。Unix socket 的无 TLS 例外须显式开启，不能用 `MYSQL_ALLOW_INSECURE=true` 连接明文 TCP。完整步骤见 [INSTALL.md](../docs/INSTALL.md)，主机准备与读取事实见资源内 `deploy/scripts/core-manager.py`。
