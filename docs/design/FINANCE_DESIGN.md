# 星屿云财务系统 Core 0.6 设计

> 作者：架构设计（Claude）。实现：Codex。对标：魔方财务 v10（ZJMF-CBAP 10.4.6）的 MVP 范围，面向**全球客户**，不做实名认证 / ICP / 国内短信模板审核 / 增值税发票 / 支付宝微信；本轮也不做税务（VAT/GST）、TOTP 二次验证和账号锁定。版本号到功能成熟后才进入 1.0。

## 0. 唯一事实来源

- **API 契约**：`betteridcsys-frontend/contract/openapi.json`（由前端 `src/contract/*.ts` 的 Zod 定义导出，141 个操作）。后端必须逐字段实现；字段名、枚举、必填项、错误码均以它为准。后端仓库 vendor 一份到 `vps-billing/contract/openapi.json` 并记录 SHA-256，CI 用它做响应契约测试（对每个实现的端点，用真实 MySQL 跑一次并校验 `data` 结构）。
- 信封不变：成功 `{"data": …}`，失败 `{"error":{"code","message","request_id","details?"}}`，`X-Request-ID` 头。
- 写操作：`X-CSRF-Token` 必须；契约里 `x-idempotent: true` 的操作必须带 `Idempotency-Key`，同 key 同 body 重放返回首次结果，同 key 不同 body → `409 IDEMPOTENCY_KEY_REUSED`。
- 管理端点：契约里 `x-permission` 在**每个请求**服务端校验（接入现有未使用的 `internal/staffaccess`）。
- 不允许 `PUT`；更新用 `PATCH`，必须带 `revision`，不匹配 → `409 REVISION_CONFLICT`。
- 金额：整数最小单位 `amount_minor` + `decimal_places` 快照；禁止浮点。
- 兼容：前端 0.6 只使用新契约；旧 0.5 端点（`/plans`、`/admin/catalog`…）保留一个版本，标注 deprecated，不再扩展。

## 1. 账户与币种

- 每个客户有**账户币种**（注册时选；首张账单产生后锁定，`currency_locked=true`）。余额、续费、升级都用账户币种。
- 访客浏览可切换展示币种；下单时报价必须使用账户币种（未登录则在登录/注册后重新报价）。
- 商品价格按 **(币种 × 周期)** 独立定价，缺失组合即不出售；不做汇率换算。

## 2. 领域模型（新迁移从 015 开始）

| 表 | 关键字段 / 约束 |
| --- | --- |
| `products` | 由旧 `plans` 迁移；`kind`、`node_id`、`name_i18n JSON`、`description_i18n`、`specs JSON`（cpu_cores, memory_mb, disk_gb, bandwidth_mbps, traffic_gb NULL=不限, ipv4, ipv6）、`badges`、`stock NULL=不限`、`visibility(public/hidden/retired)`、`controller_id`、`remote_product_id`、`sort_order`、`revision` |
| `product_prices` | PK (product_id, currency, cycle)；`recurring_minor`、`setup_minor`；旧 `plan_prices` 迁为 `monthly` |
| `product_options` / `product_option_choices` / `option_prices` | 配置项（select/quantity/os_image），价格同样按 (币种×周期) |
| `os_images` | 从主控 `GET /v1/images` 同步；`enabled`、展示名；`product_os_images` 关联 |
| `coupons` / `coupon_redemptions` | percent(基点)/fixed(按币种)；适用商品+周期；`recurring`；`new_clients_only`；总次数/每人次数；起止时间 |
| `orders` / `order_items` | 保留现有 orders；新增 items（每行一个商品×数量，配置快照 JSON，hostname） |
| `invoices` / `invoice_lines` | 新增 `kind(order/renewal/upgrade/top_up/manual)`、`subtotal/credit_applied/total/amount_paid`、行项目（service_id、period 文本） |
| `payment_attempts` / `payment_settlements` | 沿用 BE-1 修复后的语义（expired/voided/duplicate） |
| `credit_ledger` | 追加写；`kind`、`amount_minor`(有符号)、`balance_after_minor`、`invoice_id`；余额 = 最后一行 `balance_after`；与账单更新同事务，行锁 `users` |
| `services` | 现有表补齐：`product_id`、`cycle`、`recurring_minor`、`price_override_minor`、`next_due_at`、`next_invoice_at`、`auto_renew`、`label`、`controller_id`、`resource_id`、`status`、`suspension_reason`、`cancellation_*`、`terminates_at`、`specs/os 快照`、`revision` |
| `service_tasks` | 每个对主控的动作一条：`action`、`status(queued/running/succeeded/failed/uncertain)`、`operation_id`、`message`；uncertain 只能人工/只读核对 |
| `automation_settings`（单例）/ `automation_runs` | 见 §5 |
| `tickets` / `ticket_messages` / `departments` | 状态 open/answered/customer_reply/on_hold/closed；内部备注 `internal` 只返回给员工 |
| `announcements` | i18n 标题正文、置顶、发布 |
| `affiliates` / `affiliate_referrals` / `affiliate_commissions` | 推广码；佣金 pending→available（hold_days 后）→ withdrawn（转入余额） |
| `staff_roles` / 用户 `role_id` | 权限列表 = 契约 `Permission` 枚举；内置 `owner` 角色拥有全部权限且至少保留一个活跃 owner |
| `api_tokens`、`ssh_keys`、`email_tokens`、`login_events`、`notification_prefs`、`email_templates`、`email_outbox` | 见 §6、§7 |

旧数据迁移：`plans`→`products`(monthly)；已有 `orders/invoices` 保持原样并补默认值（kind=order，单行 line）；已付订单若无 services 记录，迁移时**不**自动补造（需管理员在后台“同步服务”）。

## 3. 状态机

- **订单**：`pending_payment → paid → active`（全部服务开通成功）；`pending_payment → cancelled`（客户/管理员/超时）。`fraud` 仅管理员。
- **账单**：`unpaid → paid`；`unpaid → cancelled`；`paid → refunded/partially_refunded`。部分付款允许（余额抵扣 + 网关），`balance_due` = total − amount_paid − credit_applied。
- **服务**：`pending`（已下单未付）→ `provisioning`（已付，派发中）→ `active`；`active ⇄ suspended`（逾期/流量/管理员）；`active|suspended → terminated`；`provisioning → failed`（明确拒绝）；`cancelled`（未付订单取消）。终止是不可逆的主控 delete；服务记录永久保留。
- **服务任务**：与主控 operation 一一对应，状态单调；`uncertain` 不自动重试，只允许只读 lookup 或人工处置。

## 4. 计费规则

- 报价 `POST /cart/quote` 纯函数、无副作用；返回 `quote_digest = sha256(规范化报价 JSON)` 和 10 分钟有效期。`POST /orders` 重新计算，digest 不同 → `409 QUOTE_CHANGED`。
- 计算顺序：商品周期价 + 配置项 → ×数量 → 首期 setup 费 → 优惠码（只作用于其适用行；percent 用基点，**每行向下取整到最小单位**）→ 总计。不计税。
- 余额抵扣：`apply_credit=true` 时下单同事务从余额扣 `min(余额, total)`；剩余走网关。0 元直接 paid。
- **续费**：周期到期前 `invoice_days_before_due` 天生成续费账单（每服务每周期唯一：`UNIQUE(service_id, period_start)`）；`auto_apply_credit` 开启时自动用余额支付。付款后 `next_due_at += 周期`（按日历月加，月末对齐：1/31 + 1 月 = 2/28/29）。
- **升降级**（按剩余时间比例）：`days_remaining = ceil((next_due_at − now)/1 天)`；`period_days = next_due_at − (next_due_at − 周期)`；差价 = `floor((新周期价 − 旧周期价) × days_remaining / period_days)`。正数开升级账单，付款后派发 resize；负数直接计入余额（kind=refund）并派发。10 分钟内同一服务只允许一次变更。
- **取消**：`immediate`（立即终止，不退款）或 `end_of_period`（到期终止，期间不再出续费账单）；可撤回直到执行。
- 充值：最小/最大由设置控制，生成 `top_up` 账单，付款后写 `credit_ledger`。

## 5. 自动化调度（进程内，替代手工 cron）

- `aster-api` 内一个调度器 goroutine，每分钟 tick；每个 job 用 MySQL `GET_LOCK('aster:job:<name>', 0)` 保证多实例单执行；每次运行写 `automation_runs`（processed/failed/message）。单个对象失败不影响其他对象。
- 任务：`generate_renewals`、`send_reminders`（到期前/逾期多级）、`suspend_overdue`（逾期 ≥ suspend_after_days → 主控 suspend）、`terminate_overdue`（≥ terminate_after_days，必须 > suspend）、`cancel_unpaid_orders`、`apply_credit`、`dispatch_provisioning`（`auto_provision` 开启时把已付服务派发到主控；保留 root CLI 作为手动通道）、`release_affiliate`、`purge_sessions`、`close_tickets`（answered 且 X 小时无回复）。
- 付款后自动解除欠费暂停（只解除 reason=overdue 的暂停）。
- 设置校验：`suspend_after_days < terminate_after_days`；提醒天数递增。

## 6. 认证与安全

- 注册（站点可关闭）、邮箱验证（可设为下单前必须）、找回密码（令牌 30 分钟、单次、哈希存储，响应永远 200）。
- 会话：列表、单个撤销；改密码撤销其他会话。登录事件表（成功/失败）。
- **不做账号锁定**：只保留按 IP 前缀（IPv4 /32、IPv6 /64）的请求频率限制，防暴力破解且不会把任何账号锁死；BE-1 中按邮箱的退避去掉。注册、找回密码同样只按 IP 限频。
- **个人 API Token**：`ast_` 前缀 + 32 字节随机；只存 SHA-256；scope read/write；IP 白名单；过期；`Authorization: Bearer` 访问客户端点（不需要 CSRF，不能访问 `/admin/*` 与账户安全端点）。
- SSH 公钥：解析校验算法（ed25519/ecdsa-p256/rsa≥3072），计算 SHA256 指纹；开通/重装时按 id 传给主控。
- 客户审计视图不暴露员工身份（BE-1 已修）。

## 7. 通知

- `email_outbox` 由调度器消费（每分钟，指数退避，最多 5 次，失败留痕）；模板 = 契约 `EmailTemplate.key`，按客户语言渲染，Go `text/template`，变量白名单。
- 尊重客户通知偏好（安全类邮件——验证、重置——不可关闭）。

## 8. 与云主控的集成

- 开通沿用 `aster.controller/1`（不改协议）。
- 新增 **`aster.service/1`**（云侧由 CL-3 实现，见 `CLOUD_V2.md §3`）：财务通过同一安全 transport（固定 DNS/CIDR、TLS、无代理、无重定向、单次拨号）调用服务动作、状态、控制台票据、监控、快照、镜像目录。客户端点 `/services/{id}/power|reinstall|reset-password|rescue|console|snapshots|metrics|upgrade` 全部经由它。
- `capabilities` 字段来自主控 `GET /v1/services/{resource_id}`；主控不支持的能力返回 `false`，对应端点返回 `501 FEATURE_UNAVAILABLE`。
- 密码类参数只在请求体里经 TLS 传给主控，财务不落库、不进日志、不进审计 reason。

## 9. 分工（并行 Codex 任务）

| 任务 | 范围 | 迁移号段 |
| --- | --- | --- |
| **BE-3 Commerce** | §1–§5、§8：商品/价格/配置项/镜像、报价/订单/优惠码、账单/余额/充值/支付、服务生命周期、续费/升降级/取消、调度器、自动派发与服务动作、对应管理端点、客户 overview | 015 起 |
| **BE-4 Accounts & Support** | §6、§7、工单/部门、公告、推广、员工角色/RBAC、邮件模板与 outbox、站点/安全设置 | 015 起（后合并者重新编号） |

两个任务都基于 `fix/core-correctness`（BE-1）分支；路由注册、`models.go` 等公共文件冲突由后合并者解决。

## 10. 验收

- 每个契约端点：真实 MySQL 下的 handler 测试 + 契约结构校验；权限端点有越权负例。
- 钱：报价/订单/续费/升降级/退款/余额的表驱动测试，包含多币种、0/2/3 位小数、优惠码边界、月末对齐、并发下单同一库存（不超卖）。
- 调度器：多实例并发只执行一次；单对象失败不阻塞；重复 tick 幂等。
- 安全：API token 权限边界、重置令牌单次、IP 限频不影响其他来源。
