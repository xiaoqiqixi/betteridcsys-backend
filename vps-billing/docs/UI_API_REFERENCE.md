## Core 0.5.8 四层分类补充

公开与管理 taxonomy 包含 types、regions、nodes，父链为类型 → 地区 → 节点；套餐 DTO/商品写入必须有 node_id，使用同一 taxonomy_revision。管理 POST 包含完整 nodes；不能省略字段或按名字推断父级。完整限制、默认选中、schema12 与旧订单边界见 [CATALOG_TAXONOMY.md](CATALOG_TAXONOMY.md)。

# UI 独立开发 API 文档

核对日期：2026-10-21（Asia/Shanghai）。本文件只提供现有 HTTP API 行为、JSON 字段与业务约束，不提供项目源码、内部实现、真实业务数据、凭证或部署信息。前端独立使用 Vue 3 + TypeScript 开发；接口之外的能力不得编造。本文不是生产验收或部署授权。

## 0. 范围与记法

- 下面路径除健康检查外，均**相对 `/api/v1`**。这是业务API，不是页面路由。动态ID必须正确URL编码，游标当不透明字符串处理。
- `data`为成功信封中的业务内容；`?`表示字段可缺失，不等于显式null。时间为RFC3339 UTC字符串。所有示例均为合成，不含实际账户/订单/会话。
- 权限分“公共”“本人”“管理员”。管理员是全站管理身份，但没有绕过金额/支付/历史/状态一致性的权限。现有角色只有customer/admin。
- UI需覆盖客户首页、地区选择与套餐选购（无比较/全部地区）、订单/单笔详情、账单、服务与账户安全，以及后台账户/角色、商品、订单/账单/服务、审计、财务、站点、SMTP、扩展和版本信息。没有API的业务操作不得伪装可用。

## 1. 请求、鉴权与错误

### 同源请求与身份流程

1. 进入应用或登录页，先GET `/session`取得匿名/已登录Cookie对应的CSRF token。
2. POST携带该token；成功登录/注销后更新当前身份和token，敏感账户动作可能要求重新登录。
3. 不把认证Cookie/秘密传给第三方服务；不得用mock成功替代真实API结果。

- 请求路径使用 `/api/v1` 前缀，`credentials: "same-origin"`，发送 `Accept: application/json`、`Accept-Language: zh-CN|en`；有 body 才发 `Content-Type: application/json`。非 GET 必须带当前 `X-CSRF-Token`。15 秒浏览器超时；服务端一般有 12 秒请求 context 超时。**超时不代表未写入。**
- 成功包裹为 `{data: T}`，客户端应统一解包并校验；不要把信封当业务对象。正常读取/更新多为 200，新建订单/账户/商品/复制为 201，版本交接为 202。不要硬编码成功只能 200。
- 失败 `{error:{code:string,message:string,request_id?:string,details?:unknown}}`；客户端错误模型应保留code/message/requestId/status/details。`X-Request-ID` 是服务端生成的 32 位小写 hex；前端优先使用合法响应头 ID，再用 JSON 的 ID，不自行编造。网络断开可能完全没有可解析 JSON 或请求号；原生网络/Abort错误可能没有业务错误code。
- 错误代码用于安全翻译/解释；未知错误用通用提示，不显示 SQL、主机路径、密钥、原始 SMTP/支付诊断或任意对象 JSON。保留“查看请求编号”的可用性，但**事件 ID、action 和 HTTP 请求/状态不是同一概念**。
- 正式会话为同源 HttpOnly、Secure、SameSite=Strict Cookie（服务端管理，8 小时）；不把 Cookie/会话秘密写 localStorage，不发给外部 AI。每次认证及敏感操作后刷新 session 和 CSRF。演示模式的非 Secure Cookie 是本地演示专用，不可变成生产降级。
- 除专用渠道回调外，POST 都先验证 Cookie、`X-CSRF-Token` 和提供的 Origin；Host 也匹配配置来源。即使登录尚无用户，也必须先 GET `/session`。POST 的 CSRF 403 可能先于“未登录”检查。
- 公共端点无须已登录身份；“本人”端点要求会话且服务器限定所有权；“管理员”端点要求服务端当前启用的 `admin`。**隐藏导航不是授权。** 管理员调用客户端点仍只看自己；旧 `/audit` 是明确例外，见下表。
- `Cache-Control: no-store`；不为业务 API 加 Service Worker/跨账户缓存。用户 ID **或角色**变化时清空旧数据、关闭敏感弹窗，丢弃在途旧身份响应。
- 仅 GET/POST 是现有 API 方法。没有通用 PUT/PATCH/DELETE。JSON 限制各端点不同：通用 body 16 KiB；账户/商品/财务严格 body 64 KiB；SMTP/插件检查 8 KiB；取消/版本操作 4 KiB。财务类、账户及商品拒绝缺失/重复/未知/null/错误大小写字段。不要把所有端点假设为同一种 parser。

### 通用错误语义

| HTTP / code | UI 应做什么 |
| --- | --- |
| 401 `AUTH_REQUIRED` / `INVALID_CREDENTIALS` | 登录／正确显示登录失败，不泄露账号存在性 |
| 403 `FORBIDDEN` / `CSRF_INVALID` | 禁止成功态；清掉越权数据，按会话恢复流程处理 |
| 404 `NOT_FOUND` | 不存在或无所有权；不猜测其他账号资料 |
| 400 `INVALID_BODY`、各 `INVALID_*`；415 `JSON_REQUIRED` | 保留输入、定位字段；不放宽服务端约束 |
| 409 各版本／业务状态冲突 | 重新读取并明确让用户确认；不静默借新 revision 覆盖旧草稿 |
| 429 `RATE_LIMITED` | 遵守 Retry-After，禁连点；不是自动循环重发 |
| 421 `INVALID_HOST` / 405 `METHOD_NOT_ALLOWED` | 配置或调用问题，不绕过来源和方法 |
| 500 `INTERNAL_ERROR` / 503 依赖不可用 | 安全错误文案与合法请求号；写操作结果可能不确定 |
| 非 JSON/缺 data：`INVALID_RESPONSE`；其他 `REQUEST_FAILED` | 请求层已有校验，不能当空数据或操作成功 |

## 2. 全部现有路由清单（56 个 method/path 合同）

`—` 表示无请求 body；GET 的输入列是 query。下表出参是 `data` 中的内容。标记“UI使用”表示接口属于本次UI所需范围，**不等于本次做了在线业务测试**。

### 2.1 公共与身份（6）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/session` | 公共 / UI使用 | — | `Session`：未登录 `user:null`；取得/沿用 Cookie 对应 CSRF |
| POST `/auth/login` | 匿名 Cookie + CSRF / UI使用 | `{email,password}` | `Session`，轮换 Cookie/CSRF；前端之后重读 session；邮箱小写/trim，密码不 trim；失败 401，15 分钟 IP/邮箱限流 20 次 |
| POST `/auth/logout` | Cookie + CSRF / UI使用 | `{}` | `{user:null,csrf_token}`，撤原会话并换匿名 Cookie |
| GET `/site-settings` | 公共 / UI使用 | — | `PublicSiteSettings`，不含私有运营字段 |
| GET `/currencies` | 公共 / UI使用 | — | `CurrencyRegistry`，包括停用历史币种，无 usage 计数 |
| GET `/plans` | 公共 / UI使用 | `currency=CODE` | `Plan[]`，缺省用配置默认币种；只公开商品总开关上架项，选币缺价/停价仍可能返回 `available:false` |

### 2.2 客户与客户自身范围（11）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/overview` | 本人 / UI使用 | `currency=CODE` | `Overview`；金额/主要计数按选币，最近订单最多 5 条、跨币种各保留原币 |
| GET `/services` | 本人 / UI使用 | — | `Service[]`，不含 customer_id；只读，无资源操作 |
| GET `/orders` | 本人 / UI使用 | — | `Order[]`，既有 MySQL 清单最多 1000 条 |
| POST `/orders` | 本人 + CSRF / UI使用 | `CreateOrderInput` + `Idempotency-Key` header | 首次 201 `Order`；同键相同请求已完成重放 200，`Idempotency-Replayed:true`；见 §4 |
| GET `/orders/{id}` | 本人 / UI使用 | — | `OrderDetails`，没有 customer 字段；不依赖清单 1000 条上限 |
| POST `/orders/{id}/cancel` | 本人 + CSRF / UI使用 | `{reason}` | `{order:Order,invoice:Invoice}`：cancelled/void 同事务；重复成功不重复审计，见 §4 |
| GET `/invoices` | 本人 / UI使用 | — | `Invoice[]`，既有 MySQL 清单最多 1000 条 |
| GET `/invoices/{id}` | 本人 / UI使用 | — | `Invoice`，直接读取；开弹窗/确认状态时重读，不能只信旧列表 |
| POST `/invoices/{id}/checkout` | 本人 + CSRF / UI使用 | UI发 `{}` | `CheckoutSession`；默认支付关闭返回 503；先持久支付尝试再调用渠道；不自动标已付 |
| GET `/audit` | 登录 / UI使用 | — | `Audit[]`：customer 仅本人；**旧兼容路径按 admin 角色可返回全站**。既有清单 1000 条上限，非分页日志查询 |
| GET `/audit-events` | 本人 / UI使用 | `AuditQuery`（不允许 owner_id） | `AuditEventsPage`，真正游标分页，管理员调用这个客户路径也限本人 |

### 2.3 全站管理读取与安全取消（9）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/overview` | 管理员 / UI使用 | `currency=CODE` | `AdminOverview`；客户数全局，其余计数/金额按币种；不能把不同币种直接求和 |
| GET `/admin/customers` | 管理员 / UI使用 | — | `User[]`，只含 customer 角色，用于现有归属展示；无 disabled/revision，不能替代账户管理 |
| GET `/admin/services` | 管理员 / UI使用 | — | `AdminService[]`，额外准确 customer_id；只读，无开通/暂停/重装/终止 |
| GET `/admin/orders` | 管理员 / UI使用 | — | `Order[]`，全站既有清单最多 1000 条，不含每条完整客户详情 |
| GET `/admin/orders/{id}` | 管理员 / UI使用 | — | `OrderDetails`，额外公开 customer，其他脱敏规则同客户详情 |
| POST `/admin/orders/{id}/cancel` | 管理员 + CSRF / UI使用 | `{reason}` | `{order,invoice}`；全站安全取消，但不能跳过支付/状态/历史一致性 |
| GET `/admin/invoices` | 管理员 / UI使用 | — | `Invoice[]`，全站既有清单最多 1000 条 |
| GET `/admin/invoices/{id}` | 管理员 / UI使用 | — | `Invoice`，直接按 ID 读取；**没有管理员代付 checkout** |
| GET `/admin/audit-events` | 管理员 / UI使用 | `AuditQuery`（可 owner_id） | `AuditEventsPage`；事件增加 owner_id，表示事件所属账户而非操作者 |

### 2.4 账户、角色、会话（6）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/customer-accounts` | 管理员 / UI使用 | `q?,role?,status?,limit?,cursor?` | `CustomerAccountsPage`；q 名称/邮箱匹配，role customer/admin，status active/disabled，默认50/最多100，ID降序；游标绑定操作者身份/版本/筛选/大小 |
| POST `/admin/customer-accounts` | 管理员 + CSRF / UI使用 | `{email,name,role,password,reason}` | 201 `CustomerAccount`，邮箱唯一，创建后不可改；无客户端自定 ID |
| GET `/admin/customer-accounts/{id}` | 管理员 / UI使用 | 不允许 query | `CustomerAccount`，编辑前取当前 revision |
| POST `/admin/customer-accounts/{id}` | 管理员 + CSRF / UI使用 | `{name,role,disabled,revision,reason}` | `CustomerAccount`，全字段更新；不是 PATCH，不能传 email |
| POST `/admin/customer-accounts/{id}/password` | 管理员 + CSRF / UI使用 | `{password,revision,reason}` | `CustomerAccount`，重置密码并撤全部会话；自己重置后重新登录 |
| POST `/admin/customer-accounts/{id}/revoke-sessions` | 管理员 + CSRF / UI使用 | `{revision,reason}` | `CustomerAccount`，撤全部会话；自己操作后重新登录 |

账户 ID 为 32 位小写 hex。名称 trim 后 1–120 UTF-8 字节；reason trim 后 1–500 字节纯文本；邮箱普通 ASCII mailbox ≤254字节，小写/trim；密码 **16–72 UTF-8 字节，不 trim**、不能全空白、不能出现在 reason 中。只有 customer/admin，不做自定义角色矩阵。禁止自己降权/停用，保证至少一位启用管理员；敏感更改递增 revision、撤旧会话，仅改名称不强制退出。冲突 409 `ACCOUNT_EXISTS/ACCOUNT_CONFLICT/SELF_ACCOUNT_CHANGE/LAST_ACTIVE_ADMIN`；400 `INVALID_ACCOUNT/INVALID_ACCOUNT_QUERY`；账户存储能力不可用 503 `ACCOUNT_MANAGEMENT_UNAVAILABLE`。查询不要发送未知/重复参数，修改接口禁止 query。

### 2.5 真正商品目录（4）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/catalog` | 管理员 / UI使用 | 不允许 query | `Catalog`，含草稿/下架与各独立币种报价 |
| POST `/admin/catalog` | 管理员 + CSRF / UI使用 | `CatalogInput` | 201 `CatalogResult`，新 ID 服务端生成；可草稿或验证后上架 |
| POST `/admin/catalog/{id}` | 管理员 + CSRF / UI使用 | `CatalogInput` | `CatalogResult`；全量名称、规格、状态、prices；上下架也是此操作，不是额外 publish API |
| POST `/admin/catalog/{id}/duplicate` | 管理员 + CSRF / UI使用 | `{revision,currency_revision,reason}` | 201 `CatalogResult`，复制为新 ID且**强制草稿** |

目录 ID 长 1–64，ASCII 字母/数字/`-_.`，不能是 `.` / `..`。name/region trim后必填≤120字节，description≤500字节允许换行；reason必填≤500字节。正整数规格上限：cpu4096、memory_gb1048576、disk_gb16777216、transfer_tb1048576；prices 必须为数组（草稿可空），最多32币种、不可重复，每个 amount_minor 为有效正安全整数。上架必须至少一种**启用币种**有有效且 available 的报价。

目录 revision 与财务 plan-prices 是同一全局价格版本。`currency_revision` 必须来自**实际用于此草稿金额解析的币种元数据**；不能提交时借最新版本给旧精度背书。保存可能锁定币种精度并推进 currency_revision；以响应为准重新读取。旧版本 409 `CATALOG_CONFLICT`／`CURRENCY_CONFLICT`，非法400 `INVALID_CATALOG`。没有删除 API，不删除历史记录；不生成汇率。

### 2.6 财务与站点设置（6）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/currencies` | 管理员 / UI使用 | — | `CurrencyRegistry`，每币种有只读 usage 与 precision_locked |
| POST `/admin/currencies` | 管理员 + CSRF / UI使用 | `CurrencyUpdate` | 新 `CurrencyRegistry`；全量登记，代码永久保留，不能删除已有币种 |
| GET `/admin/plan-prices` | 管理员 / UI使用 | `currency=CODE` | `PlanPriceList`；缺价 amount="" / amount_minor=0 / available=false，不是免费 |
| POST `/admin/plan-prices` | 管理员 + CSRF / UI使用 | `PlanPricesUpdate` | 新 `PlanPriceList`；单币种1–500行局部upsert，不删其他报价；plan_available是跨币种商品总开关 |
| GET `/admin/site-settings` | 管理员 / UI使用 | — | `SiteSettings`，完整设置与 revision |
| POST `/admin/site-settings` | 管理员 + CSRF / UI使用 | 完整 `SiteSettings` | 新 `SiteSettings`；设置/审计同事务，版本冲突409 `SETTINGS_CONFLICT` |

币种代码3位大写ASCII；最多32项，name1–80字节，prefix/suffix各0–24字节，decimal_places0–6，sort_order0–10000，默认币种必须启用。`precision_locked/usage`只读，**不能回传**到 CurrencyUpdate。已有报价或历史记录永久锁精度；有未付订单/账单或未结束服务义务不能停用。

`PlanPricesUpdate.amount` 是**正十进制字符串**，不是 JSON 数字，不接受符号、指数、前导零、零价、超精度，不舍入；用精确十进制字符串/BigInt解析，不用 `parseFloat()*100`。目录接口 amount_minor 则是经过精确解析后的整数，别混淆两个协议。

财务错误：400 `INVALID_CURRENCY_CONFIG/INVALID_PLAN_PRICES/UNSUPPORTED_CURRENCY`；409 `CURRENCY_CONFLICT/PRICE_CONFLICT/CURRENCY_PRECISION_LOCKED/CURRENCY_DISABLED/CURRENCY_IN_USE`，最后一项 `details:{currency,usage}` 可显示经校验的义务计数。

站点可设置预设 logo star/cloud/server、theme blue/teal/violet，支持邮箱/HTTPS链接、展示HTTPS来源、纯文本署名与账单抬头/地址、默认语言/币种。必填site_name/invoice_issuer。文本按 UTF-8 字节限制：site_name120、company_name240、support_email254、support_url500、display_url300、email_signature1000、invoice_issuer240、invoice_address1000；后两类长文本与署名适当允许换行。`display_url` 仅HTTPS来源，链接禁userinfo/hash等，客户端做同等URL/字段校验，服务端仍独立校验。**没有上传图片、任意HTML/CSS/脚本、抓取URL或修改DNS/TLS功能。**

### 2.7 SMTP、扩展能力、更新（8个UI合同）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/email-settings` | 管理员 / UI使用 | — | `EmailSettings`，只有非秘密配置、凭据存在标志、插件和目的地状态 |
| POST `/admin/email-settings` | 管理员 + CSRF / UI使用 | `EmailSettingsUpdate` | 新 `EmailSettings`，保存不连接、不发信；密钥/配置/审计保护保留 |
| POST `/admin/email-settings/test` | 管理员 + CSRF / UI使用 | `{revision,to}` | `{status:"accepted"}`，按**已保存启用配置**发固定单封测试；不是收件箱投递证明 |
| GET `/admin/integrations` | 管理员 / UI使用 | — | `{items:IntegrationSummary[]}`，只读能力/配置字段说明，**没有配置秘密值或 secret_ref 值** |
| GET `/admin/updates` | 管理员 / UI使用 | — | `UpdateStatus`，仅本地快照，不主动联网；未查/失败不能显示“无更新” |
| POST `/admin/updates/check` | 管理员 + CSRF / UI使用 | 严格 `{}` | `UpdateStatus`，明确查固定官方正常Release，不可传任意仓库/URL/token |
| POST `/admin/updates/handoff` | 管理员 + CSRF / UI使用 | `UpdateHandoffInput` | 202 `UpdateHandoff`，只生成**未签名的主机操作交接**；不安装、不执行、不等于更新成功 |
| GET `/admin/updates/operation` | 管理员 / UI使用 | — | `NativeOperationView`，受保护本机回执只读投影，不启动进程，不表示持续健康或一定执行了当前网页交接 |

SMTP用户名/密码**只写**，不读回、不回显、不进预览数据/日志/审计；空值保留。显式清除须 clear_username 和 clear_password 同时为true且关闭；已有保存凭据时，改服务器host/port/tls_mode须完整替换两项凭据或同时清除并关闭；尚无保存凭据的停用草稿可修改这些目的地字段而不强制填写凭据，启用仍须完整有效配置。只支持starttls/implicit_tls。配置可先保存，测试另需root审批、精确目的地、版本和握手验证；`approved`不是连接或投递证明。测试错误409 `SMTP_DISABLED/SETTINGS_CONFLICT`、503 `SMTP_KEY_UNAVAILABLE`、429 `RATE_LIMITED`（30秒）、502 `SMTP_UNAVAILABLE/SMTP_DELIVERY_UNCERTAIN`。不确定发送**不自动重发**。没有通知、验证邮箱、密码找回API。

更新模式固定`manual_host_handoff`；候选须严格校验，Release说明纯文本；来源固定为服务端官方发布渠道，component core/smtp。交接有效30分钟，核对候选fingerprint与本机版本/策略并先持久审计。错误400 `UPDATE_ACK_REQUIRED`、409 `UPDATE_BUSY/UPDATE_STALE/UPDATE_INCOMPATIBLE`、503 `UPDATE_UNAVAILABLE`。不能把此按钮做成“网页一键安装成功”。

### 2.8 非本次UI必需，但确实存在的合同（6）

| 方法与路径 | 权限 / UI范围 | 输入 | 返回 / 注意 |
| --- | --- | --- | --- |
| GET `/admin/plugins` | 管理员 / **非本次UI必需** | — | `{items:Discovered[],smtp:PluginState,payment:PluginState}`，被动本地清单/审批，不执行或连接；无需为视觉重做新增页面 |
| POST `/admin/plugins/check` | 管理员 + CSRF / **非本次UI必需** | 严格 `{}` | `PluginIdentity`，SMTP兼容握手，不给SMTP凭据、不发信；失败503 `PLUGIN_UNAVAILABLE` |
| POST `/admin/plugins/payment/check` | 管理员 + CSRF / **非本次UI必需** | 严格 `{}` | `PluginIdentity`，支付身份/协议握手，不调用渠道；失败503 `PLUGIN_UNAVAILABLE` |
| POST `/payment-callbacks/{providerID}` | 渠道验签 / **不是浏览器接口** | 原始渠道body/header，不是UI JSON模型 | `{received:true}`，核心验签后原子结算；唯一浏览器CSRF例外，但仍严格Host/方法/大小/渠道协议；**前端不得调用模拟已付款** |
| GET `/healthz`（无API前缀） | 公开 / **非本次UI必需** | — | `{status:"ok"}`，只有存活 |
| GET `/readyz`（无API前缀） | 公开 / **非本次UI必需** | — | `{status:"ready"}` 或503 `NOT_READY`，数据库及迁移就绪，不是完整业务验收 |

## 3. DTO字段表（JSON合同，不是源码）

字段记法：`文本`/`整数`/`布尔`/`时间`/`数组`；`Minor`是整数最小单位，绝对值≤9007199254740991，可购买报价/下单quote/应付额为正。公共 `/plans` 中已上架但当前币种缺价的套餐可合法返回 `price_minor:0`、`available:false`，只表示没有可购买报价，不是免费；应显示不可购买，不因这一项的0值判整份目录无效。`revision`是正安全整数。`?`可缺失，`null`是明确无关联。字段名精确区分大小写。新记录币种格式完整；历史缺少的抬头/套餐快照不得补成今日值。

### 3.1 业务与财务对象

| 对象 | 完整字段与类型 |
| --- | --- |
| User | id/email/name：文本；role：customer或admin |
| Session | user：User或null；csrf_token：文本；demo_mode：布尔 |
| CurrencyFormat | code/name/prefix/suffix：文本；decimal_places：整数0–6 |
| CurrencyUsage | plan_prices/orders/invoices/services/pending_orders/unpaid_invoices/ongoing_services：非负整数计数 |
| CurrencyDefinition | CurrencyFormat全部字段；enabled：布尔；sort_order：整数；precision_locked：布尔；usage?：CurrencyUsage（仅管理员） |
| CurrencyRegistry | revision：整数；default_currency：文本；currencies：CurrencyDefinition数组 |
| CurrencyUpdate | revision/default_currency；currencies数组中只交code/name/prefix/suffix/decimal_places/enabled/sort_order，不交只读precision_locked/usage |
| Plan | id/name/description/region/currency：文本；cpu/memory_gb/disk_gb/transfer_tb：整数；price_minor：Minor；price_revision：整数；currency_format：CurrencyFormat；available：布尔 |
| PlanSnapshot | plan_id/name/region：文本；cpu/memory_gb/disk_gb/transfer_tb：整数；billing_cycle：固定monthly |
| InvoiceIdentity | name/address/support_email：文本 |
| Order | id/number/plan_name/status/currency/hostname：文本；total_minor：Minor；currency_format：CurrencyFormat；created_at：时间；service_id?：文本；issuer?：InvoiceIdentity；plan_snapshot?：PlanSnapshot |
| Invoice | id/number/order_id/status/currency：文本；amount_minor：Minor；currency_format：CurrencyFormat；issued_at/due_at：时间；paid_at?：时间；issuer?：InvoiceIdentity。**没有承诺真实line_items、税费、退款字段** |
| Service | id/name/plan_name/region/ipv4/status/currency：文本；next_due_at：时间；price_minor：Minor；currency_format：CurrencyFormat；cpu/memory_gb/disk_gb：整数 |
| AdminService | Service全部字段+customer_id：文本；客户Service不带该归属字段 |
| Audit | id/action/target/actor_email/detail：文本；created_at：时间 |
| AdminAuditEvent | Audit全部字段+owner_id：文本，事件所属账户，不是actor ID |
| AuditEventsPage | items：Audit数组或管理员AdminAuditEvent数组；has_more：布尔；next_cursor?：文本；limit：整数 |
| AuditQuery | limit?：整数；cursor?/action?/actor_email?/from?/until?：文本（时间字段按RFC3339）；owner_id?仅管理员专用接口 |
| Overview | active_services/pending_invoices：整数；unpaid_total_minor/monthly_total_minor：Minor；currency：文本；currency_format：CurrencyFormat；recent_orders：Order数组；count_scope固定currency；recent_orders_scope固定all |
| AdminOverview | customers/total_services/unpaid_invoices：整数；unpaid_total_minor/revenue_minor：Minor；currency：文本；currency_format：CurrencyFormat；count_scope固定currency；customers_scope固定all |
| CreateOrderInput | plan_id/hostname/currency：文本；quote对象内price_minor：Minor、decimal_places：整数、price_revision：整数 |
| CheckoutSession | provider_id/session_id/checkout_url：文本；expires_at：时间 |
| OrderPaymentAttempt | id/provider_id/currency/state：文本；amount_minor：Minor；currency_decimal_places：整数0–6；created_at/updated_at：时间。无商户、幂等键、交易号或checkout URL |
| OrderDetails | order：Order；customer?：User（仅管理员专用接口）；invoice：Invoice或null；service：Service或null；payment_attempts：OrderPaymentAttempt数组；audit：Audit数组；audit_has_more/payment_attempts_has_more：布尔 |
| CustomerAccount | User全部字段+disabled：布尔、revision：整数；不返回密码/散列 |
| CustomerAccountsPage | items：CustomerAccount数组；has_more：布尔；next_cursor?：文本；limit：整数 |
| CatalogPrice | currency：文本；amount_minor：Minor；available：布尔 |
| CatalogItem | id/name/description/region：文本；cpu/memory_gb/disk_gb/transfer_tb：整数；available：布尔；prices：CatalogPrice数组 |
| Catalog | revision/currency_revision：整数；items：CatalogItem数组 |
| CatalogInput | CatalogItem中除id外的所有字段；额外revision/currency_revision：整数、reason：文本，全部必需 |
| CatalogResult | revision/currency_revision：整数；plan：CatalogItem |
| PlanPriceList | revision：整数；currency：文本；currency_format：CurrencyFormat；plans数组每项plan_id/plan_name/amount：文本、plan_available/available：布尔、amount_minor：Minor |
| PlanPricesUpdate | revision：整数；currency：文本；prices数组每项plan_id/amount：文本、available/plan_available：布尔，全部必需 |

### 3.2 运营与扩展对象

| 对象 | 完整字段与类型 |
| --- | --- |
| PublicSiteSettings | default_locale：zh-CN或en；default_currency/site_name/company_name/support_email/support_url/display_url：文本；logo：star/cloud/server；theme：blue/teal/violet |
| SiteSettings | PublicSiteSettings全部字段+revision：整数、email_signature/invoice_issuer/invoice_address：文本 |
| EmailSettings | revision/port：整数；enabled/username_configured/password_configured/key_configured/plugin_available/destination_allowed：布尔；host/from_address/from_name/plugin_version：文本；tls_mode：starttls或implicit_tls；plugin_status：not_installed/disabled/unavailable/approved；updated_at：时间 |
| EmailSettingsUpdate | revision：整数；enabled：布尔；host/from_address/from_name：文本；port：整数；tls_mode：starttls或implicit_tls；username?/password?：只写文本；clear_username?/clear_password?：布尔；前7字段必需 |
| IntegrationSummary | id/name/description：文本；kind：payment或email；enabled/configured：布尔；status：disabled/ready/unavailable；capabilities/supported_currencies/limitations：文本数组；currency_decimal_places：币种→整数0–6的对象；configuration_mode：environment或admin_api；configuration_fields数组每项key/label：文本、type：string/secret_ref/url、required/secret：布尔。**字段说明不是配置值** |
| Discovered | id/version/protocol：文本；status：invalid或discovered |
| PluginState | status：not_installed/disabled/unavailable/approved；id/version/protocol/smtp_protocol/smtp_core_min/smtp_core_max_exclusive：文本；policy_revision：整数 |
| PluginIdentity | protocol/id/version/sha256/nonce：文本；capabilities：文本数组，握手身份不是任意主机命令结果 |

### 3.3 更新与回执对象

| 对象 | 完整字段与类型 |
| --- | --- |
| UpdateAsset | name/sha256：文本；id/size：正安全整数 |
| UpdateCandidate | distribution_format：archive-v1或raw-binaries-v1；component：core或smtp；tag/version/release_url/notes/source_sha/catalog_sha256：文本；published_at：时间；release_id/run_id/run_attempt：正整数；compatible/update_available：布尔；preconditions：文本数组；assets：UpdateAsset数组 |
| UpdateStatus | current：当前版本对象；source：来源状态对象；checked_at：文本（未查可空）；items：UpdateCandidate数组；mode固定manual_host_handoff |
| UpdateStatus.current | core_version/smtp_version：文本；smtp_status：not_installed/disabled/unavailable/approved；smtp_protocol?/smtp_core_min?/smtp_core_max_exclusive?：文本；plugin_policy_revision：整数 |
| UpdateStatus.source | repository：固定官方仓库标识（不允许用户提交任意仓库）；status：not_checked/available/unavailable |
| UpdateHandoffInput | component：core或smtp；tag/source_sha/catalog_sha256：文本；release_id：整数；acknowledge_manual_handoff：必须true |
| UpdateHandoff | schema固定2；status固定awaiting_host_operator；intent：下列交接对象。客户端可保留旧schema1/旧资产格式兼容 |
| UpdateHandoff.intent | schema固定2；distribution_format：archive-v1或raw-binaries-v1；request_id/actor_id：文本；created_at/expires_at：时间；repository：固定官方标识；repository_id：整数；component：core或smtp；tag/version/source_sha/current_core_version/current_plugin_version：文本；release_id/run_id/run_attempt/plugin_policy_revision：整数；assets：UpdateAsset数组 |
| NativeOperationView | state：unconfigured/absent/unavailable时不带operation；state为available时带下列NativeOperation |
| NativeOperation | schema固定1；operation_id：32位小写hex；action：install/upgrade/recover；status/phase：见下；target_core_version/target_source_sha/previous_core_version/previous_source_sha：文本；maintenance/active_release_changed/https_ready/business_validation_required：布尔；database_migration：{started：布尔,completed：布尔}；service_action/program_rollback/error_code：见下；started_at/updated_at：UTC时间 |
| NativeOperation.status | checking/prepared/executing/succeeded/failed/needs_manual_recovery/rolled_back |
| NativeOperation.phase | confirmed/install_preflight/provision/configure/old_schema_preflight/maintenance/backup/migrate/first_admin/activate/readiness/complete |
| NativeOperation.service_action | none/stopped/started/previous_started |
| NativeOperation.program_rollback | none/verified_old_schema/blocked_schema_or_readiness |
| NativeOperation.error_code | none/operation_failed/interrupted_requires_recovery |

固定官方来源标识与URL由API返回且必须严格校验，不能换成任意请求URL；这里不提供仓库访问入口。Release说明只显示纯文本。回执不可用时没有主机路径、任意stdout或原始诊断。状态应一致：迁移完成不能尚未开始；HTTPS就绪仅成功complete且非maintenance；成功回执须已迁移、切换、服务启动和HTTPS就绪；回滚只表示已验证旧schema并重新启用原程序，不是任意数据库降级。客户端不能把异常矛盾状态渲染成成功。

### 3.4 合成请求/响应示例（纯JSON，非实现源码）

示例ID、名称、金额、日期全部为虚构，不作为默认真实商品或账户。`<...>`只是说明用占位符，不能用于生产认证。

**GET `/session`，未登录成功：**

```json
{"data":{"user":null,"csrf_token":"<synthetic-csrf-token>","demo_mode":false}}
```

**POST `/orders`，另带CSRF与`Idempotency-Key: synthetic-order-key-0001`：**

```json
{"plan_id":"plan-demo","hostname":"demo-host","currency":"CNY","quote":{"price_minor":3900,"decimal_places":2,"price_revision":7}}
```

**下单成功201（完整合成快照）：**

```json
{"data":{"id":"11111111111111111111111111111111","number":"ORD-DEMO-001","plan_name":"演示套餐","hostname":"demo-host","status":"pending_payment","total_minor":3900,"currency":"CNY","currency_format":{"code":"CNY","name":"人民币","prefix":"¥","suffix":"","decimal_places":2},"created_at":"2026-10-21T00:00:00Z","plan_snapshot":{"plan_id":"plan-demo","name":"演示套餐","cpu":1,"memory_gb":2,"disk_gb":40,"transfer_tb":1,"region":"演示区域","billing_cycle":"monthly"}}}
```

**POST `/admin/catalog`，新增草稿（prices可空；上架需有效启用币种报价）：**

```json
{"revision":7,"currency_revision":3,"reason":"合成演示：新增草稿","name":"演示套餐","description":"仅用于界面设计","cpu":2,"memory_gb":4,"disk_gb":80,"transfer_tb":2,"region":"演示区域","available":false,"prices":[{"currency":"CNY","amount_minor":7900,"available":true}]}
```

**分页结果（无数据也有正确结构）：**

```json
{"data":{"items":[],"has_more":false,"limit":50}}
```

**409业务冲突（合成请求号）：**

```json
{"error":{"code":"PRICE_CHANGED","message":"报价已改变，请重新确认当前价格后下单","request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
```

**POST `/admin/orders/{id}/cancel`：**

```json
{"reason":"合成演示：客户不再需要此订单"}
```

**支付未配置，503不是成功：**

```json
{"error":{"code":"PAYMENT_NOT_CONFIGURED","message":"支付渠道当前不可用，已有付款请核对原渠道","request_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
```

## 4. UI 不得改变的业务规则

### 金额、精度、偏好与历史

- 默认中文/`zh-CN`、CNY；支持英文`en`，CNY/USD独立报价，并保留现有可配置币种注册表（0–6位小数）。语言与币种独立；不把切换英文等同USD，不自动翻译管理员输入数据。
- 金额最大安全整数 `9007199254740991`；只在**记录自身币种与快照精度**下格式化。用记录快照和BigInt精确格式化，避免大金额精度损失；缺失/非法格式显示“—”，不猜测币种。不用选择器当前币种给旧账单改标签，不跨币种相加，不 `/100` 写死两位。
- 客户选币只允许启用币种；管理员报表可查登记的停用历史币种，但这是报表偏好，不写成客户购买币种。不用当前启用表过滤历史金额显示。
- 商品/独立价格/公司/账单抬头改动仅影响之后记录；历史金额、currency_format、issuer、plan_snapshot和创建/开具/到期时间不能重算、重写、补造。

### 下单幂等与报价

- 新UI必须显式提交currency和完整quote（从当前Plan的报价字段读取并校验）。服务端旧兼容允许currency/quote省略，**不是重做UI移除确认条件的理由**。
- plan_id非空≤64字节；hostname由后端trim/lowercase，1–63位ASCII小写字母/数字/中横线，首尾字母或数字；不支持点分FQDN。幂等header为8–128位`[A-Za-z0-9_.:-]`。
- 同用户、同键、同套餐/主机名/币种返回原订单；不同请求同键409 `IDEMPOTENCY_CONFLICT`。不确定结果保留原键与完整原请求，先重读会话核对原账户，再核对订单；**不能生成新键盲重下单**。已完成重放优先返回原历史，不受今日quote变动影响。
- 首次quote在事务内检查price_minor/decimal_places/price_revision；过期409 `PRICE_CHANGED`且不创建订单。须重新载入、展示新价、由用户重新确认；不能自动接受涨价。其他常见400 `INVALID_QUOTE/VALIDATION_ERROR/INVALID_CURRENCY/IDEMPOTENCY_KEY_REQUIRED`、409 `PLAN_UNAVAILABLE/CURRENCY_DISABLED`。
- 创建订单只原子生成pending_payment订单、unpaid账单及审计/outbox；**不付款、不开VPS、不消费资源开通任务**。

### 订单状态不是自由编辑字段

- 已实现的动作：下单→pending_payment/unpaid；安全取消→cancelled/void；通过已批准支付worker验签且核心结算成功→paid/paid。
- 没有通用 `PATCH status`、手动标已付款、任意作废账单、退款、冲正、续费、钱包、余额换汇、完整会计账本或资源生命周期API。管理员有所有**已实现**全站业务权限，但仍受这些一致性约束，不能用假的可点击按钮冒充功能。
- 取消body只有trim后1–500字节纯文本reason；已付款、关联服务、配对不一致、不安全旧历史、创建中/有效/不确定/结算支付尝试均拒绝409 `CANCELLATION_STATE_CONFLICT`，旧兼容层无法排除自定义支付处理时409 `CANCELLATION_UNAVAILABLE`。不能靠停用插件解除保护。
- 取消响应不确定时先查询，必要时按同一订单/原因重做安全取消；不能先把本地状态画成成功。客户端必须校验响应中的原金额、快照和配对，服务端继续权威决定。

### checkout 与支付结果

- 只在最新本人账单unpaid且可付时显示真正checkout；管理员详情只读，不代付。
- 客户端必须验证provider/session非空、**HTTPS**、无userinfo、未来expires_at；服务端另有目标host允许清单。只在当前账单/当前弹窗generation仍匹配时展示链接；切换/刷新/关闭后不能泄露旧链接。
- 支付默认关闭，没有内置真实渠道；503 `PAYMENT_NOT_CONFIGURED`要诚实显示不可用，不摆假“支付成功”。409 `INVOICE_NOT_PAYABLE/PAYMENT_CURRENCY_UNSUPPORTED/PAYMENT_PENDING`、502 `PAYMENT_PROVIDER_ERROR`、503 `PAYMENT_STORAGE_UNAVAILABLE`要保留核对渠道与刷新账单指引，禁自动重复付款。
- 回跳、二维码扫码、浏览器字段或点击按钮都**不能改账**；已付只能来自服务端已验签持久结算。事件和渠道交易各唯一去重，UI不触碰此逻辑。

### 真实范围、分页与日志

- 新账户管理及审计是真游标分页，默认50/最多100；筛选变更从第一页重新查询，不能复用绑定旧条件的游标。旧订单/账单/服务/客户等MySQL清单最多1000条，不能用加载数组冒充全库搜索、导出、总量。
- 审计action精确匹配`^[a-z][a-z0-9_.-]{0,63}$`；actor_email精确字符串≤254字节，可为系统actor如`payment:provider`，不是必须邮箱。from含、until不含；RFC3339归一UTC，最多微秒、from<until。owner_id仅管理员专用路径32位小写hex；不是actor身份。
- 审计query≤4096字节；未知/重复/空参数或篡改/跨范围/改筛选复用cursor均400 `INVALID_AUDIT_QUERY`。排序created_at DESC,id DESC；第一页上界不是跨请求数据库快照。当前页失败可用当前cursor重查，不能回退伪造第一页成功。
- 订单详情支付尝试/本单审计各最多100并有has_more；不能将“详情未展示更多”写成没有其他记录。invoice/service显式null表示没有可确认关联，不能按名称/金额猜服务；MySQL订单详情同一只读repeatable-read快照。
- 日志页面查的是业务审计，不是host journal、HTTP请求日志或不可篡改总账；同库审计仍有自身留存与篡改边界。不向Gemini上传主机日志或真实业务数据。

## 5. 独立UI交付要求与边界

- 交付可运行的Vue 3 + TypeScript前端及必要样式，不只交建议或一张图；路由、页面和可复用视觉组件可独立设计，随后由项目负责人集成真实业务接口。
- 优先客户首页、地区选择与套餐选购（无比较/全部地区）和移动端，再统一订单/账单/后台。必须有完整导航抽屉、弹窗、语言/币种入口、商品新增/复制/编辑规格/定价/上下架流程及管理员权限可见性。
- 实现真实加载/空数据/错误/冲突/不确定状态，不用mock成功取代真实请求。假数据仅明确标注的预览或测试；不得接入实际成功分支。
- 不改变Go+MySQL和原生Linux兼容边界；不引入Docker/Redis/控制器/真实资源开通/新支付渠道；不部署、不发布、不改版本。
- 不删安全校验以换取页面简短，不把长金额/长标题/错误信息藏掉骗截图；不得上传真实用户数据、会话、凭证、日志或主机信息。
- 新产出须检查类型、构建、测试并在真实浏览器截图。验证中英文、语言/币种独立、历史/长金额、小屏、键盘焦点、抽屉/弹窗关闭、浏览器返回、账户/角色切换、写操作冲突与不确定态。模拟API回归必须标明，不能冒充真实MySQL/支付验收。

## 6. 接口覆盖与未知项

已核对现有HTTP合同：本文§2.1–2.7共50个UI所需method/path，§2.8另6个非本次UI必需合同，总计56。动态ID归一为`{id}`，query变化不另计；不存在的publish/refund/status PATCH没有计入。此核对只证明文档覆盖，不代表本次执行了业务、数据库或主机测试。

未提供也不得自行假定的能力：全库订单/账单/服务分页搜索与导出、任意状态编辑、真实VPS开通、人工收款/退款/冲正/续费、任意插件上传或审批、完整会计账本、生产监控/主机日志浏览。真实邮件/支付/生产容量、原生安装/备份回滚属于单独验收，不在独立UI设计证明之内。
