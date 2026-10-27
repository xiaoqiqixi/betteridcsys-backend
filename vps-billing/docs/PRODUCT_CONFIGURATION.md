# 商品产品配置与主控边界

本功能首次随 Core v0.5.9 发行，Core **0.5.10** 保留 schema 13，不新增迁移。当前版本的发行与精确测试见[变更记录](CHANGELOG.md)和[验证记录](VALIDATION.md)。旧 v0.5.6–v0.5.9 在历史归属调整后的新 prepare/stage 仍受阻；现有目录与升级条件见[升级说明](UPGRADE.md)。正式发行与目标主机验收是不同结论，不以本文件或局部测试代替。

## 参考了什么

实际阅读了 IDCsmart 官方的[商品管理（含产品转移）](https://docs.idcsmart.com/docs/财务系统使用文档/商品设置/商品管理（含产品转移）)和[魔方云对接魔方财务](https://docs.idcsmart.com/docs/魔方云/11.%20关于三方对接/魔方云对接魔方财务)，并核对其原始操作截图：

- 销售商品、价格、产品配置和自动开通分别管理；商品关联独立控制接口，不把接口账号塞进普通客户选项。
- 模块配置将稳定机器键和显示名分开。数据中心、计算节点、镜像和存储使用主控自己的资源 ID，不能从销售分类名称猜测。
- 参考系统在新增商品时抓取配置项，后续手动维护；这不是持续同步保证。
- 开通策略与产品参数分离。本项目采用自己的权限及付款守卫，不复制参考系统的账号权限或策略默认值。
- 文档中的产品转移是独立插件的所有权变更，不是服务器资源迁移。

借鉴的是分层思路，不是对接第三方 API。本系统实现自己的产品配置、资源模型和任务合同，**不声称兼容魔方云 HTTP API**，不要求提供第三方地址或凭据。主控通信采用受保护 HTTPS，或显式批准的独立资源 worker；默认没有自动开通 worker，真实 KVM 验收后置。配置或协议测试不能代替实际资源创建结果，详见[资源插件](RESOURCE_PLUGINS.md)。

## 本项目的边界

1. **分类不是资源。** `类型 → 地区 → 节点 → 套餐` 是销售目录；其中 `node_id` 是目录节点，不是计算节点。主控区域、计算节点、镜像、存储等标识须独立配置、显式关联。
2. **产品配置不是价格。** 使用独立配置与版本，不改变已有商品金额、币种精度、独立定价、支付幂等或历史账单逻辑。展示配置不得在客户端自行推导真实售价。
3. **新单冻结，旧单不补造。** 新订单以服务端确认的配置形成独立不可变产品配置快照。不能修改既有财务 `PlanSnapshot`，不能用当前配置回填旧订单，不能因新配置而拒绝原有合法幂等重放。
4. **先付款，明确执行。** 首阶段只允许已付款订单由管理员明确发起手动开通任务。不消费既有 `order.created`；该事件及取消守卫的语义不变。付款不是资源已就绪，本地状态变化也不是远端命令成功。不提供无需付款开通或关闭 TLS 校验的旁路。
5. **外部结果必须可区分。** 明确成功、明确失败和结果不明不可混为一谈。超时或断线不能伪造失败/成功，也不能静默重放开通写操作；应保留待核对状态及审计线索。
6. **敏感配置不进入购买快照。** 主控口令、令牌和连接凭据不得进入客户 DTO、订单快照、页面回显或原始错误日志。配置审计记录操作者、目标及修订，不记录凭据内容。

主控通信使用本系统可插拔的版本化协议；默认没有后台 worker、定时调度或真实开通。配置界面与 Web API 不读取端点凭据，也不发送网络开通请求。重装、暂停、删除、升级、库存调度和自动失败重试不因这次产品配置开发而默认启用。

## 显式主机命令

管理员只能为**订单和关联账单均已付款**的订单准备手动任务。准备任务不会发送请求；只有 root 在主机上显式调用以下一次性命令，才会尝试发送已有任务。参数是 32 位小写十六进制的**任务 ID**，不是订单 ID：

```sh
sudo env \
  PROVISIONING_MYSQL_DSN_FILE=/etc/aster/provisioning/mysql.dsn \
  PROVISIONING_POLICY_FILE=/etc/aster/provisioning/policy.json \
  /opt/aster/current/bin/aster-api -dispatch-provisioning "$JOB_ID"
```

- `PROVISIONING_MYSQL_DSN_FILE` 是专用数据库连接文件；`PROVISIONING_POLICY_FILE` 是固定控制端配置文件。二者均为规范绝对路径、root 所有的私密普通文件，不允许符号链接、多硬链、不安全可写祖先或组/其他用户读取。口令不写在命令参数、Web 环境或商品配置中。
- 策略将控制端 ID 绑定到固定 HTTPS 端点、明确允许的 CIDR 和私密 token 文件；可指定受保护 CA 文件。解析出的地址必须全部在允许范围，并固定使用经验证的地址；保留证书链、主机名校验，不跟随跳转、不使用环境代理、不降级 TLS。
- 策略与连接准备失败时不领取任务；进入发送后，同一任务只允许一次领取和一次网络写入尝试。没有自动重拨或自动重发。
- 超时、连接中断、无效/不匹配响应，或者发送后无法确认数据库完成写入，均不能当成“未开通”。按 `uncertain` 或待核对状态处理；**不得再次执行开通写操作或新建替代任务来绕过一次性限制**。

核对结果时仅向远端发送只读 GET，不重复开通 POST；本地仍会更新任务状态并记录审计：

```sh
sudo env \
  PROVISIONING_MYSQL_DSN_FILE=/etc/aster/provisioning/mysql.dsn \
  PROVISIONING_POLICY_FILE=/etc/aster/provisioning/policy.json \
  /opt/aster/current/bin/aster-api -reconcile-provisioning "$JOB_ID"
```

执行前应核对匹配版本、schema、最小数据库权限和本机文件保护；以上示例不安装或启动服务，也不证明实际资源已创建。

## `aster.controller/1` 结果合同

该协议是本系统自己的 bridge，不是任何第三方原生 API。稳定公开 Go 合同由 `backend/controllerapi` 导出（请求、产品、资源、结果及协议标识），不提供财务数据库或付款操作。独立主控/KVM 另行开发，本财务版本不附带其实现。请求来自不可变订单产品配置，包含操作 ID、订单 ID、非秘密资源参数及配置版本/摘要，不包含金额、账单信息或账户凭据。

控制端结果必须是有界、完整的 JSON，并严格绑定协议版本、原操作 ID、订单 ID 和**原请求字节的 SHA-256**。`accepted` 仅表示接受任务；`succeeded` 必须带合法资源标识；`rejected` 不得带资源标识。任何状态都不会自动制造本地服务或修改支付记录。

HTTP **409 只表示可以返回已存在 operation 的结果**：必须收到完整且满足上述绑定的结果对象才可接受，不能仅凭状态码认定成功或失败；同一操作的既有 `accepted`、`succeeded` 或 `rejected` 按其明确结果处理。网络错误、空响应、摘要不符、非法状态或其他无法核实的响应保留结果不明，不能触发重发。

### 管理端接口

以下路径均位于 `/api/v1` 下，须有效管理员会话；写入还须符合现有同源及 CSRF 守卫。它们不调用主控网络：

| 方法和路径 | 行为 |
| --- | --- |
| `GET /admin/catalog/{plan_id}/product-configuration` | 读取配置；未配置商品返回 disabled、revision 1 |
| `POST /admin/catalog/{plan_id}/product-configuration` | 完整提交 `{revision,spec,reason}`；旧 revision 返回 409 |
| `POST /admin/orders/{order_id}/provisioning-jobs` | `{reason}` 为已付款订单准备唯一任务，首次 201，重放 200 |
| `GET /admin/provisioning-jobs/{job_id}` | 读取任务状态，不返回凭据、请求体或领取令牌 |

配置响应完整字段为 `plan_id, revision, price_revision, spec, configuration_sha256`。保存仅提升产品配置修订及报价确认修订，不修改报价金额。复制销售商品不隐式复制开通配置，新商品默认不关联主控。

`spec` 必须完整包含 `mode,controller_id,remote_product_id,resources`，不接受任意 JSON、重复/未知/大小写别名字段。`disabled` 时两个 ID 均为空、resources 为 null；`preset` 使用两个 ID、resources 为 null；`custom` 只使用 controller_id、remote_product_id 为空，resources 必须完整包含以下 17 个字段：

| 字段 | 含义与单位 |
| --- | --- |
| `datacenter_id`, `compute_node_id`, `node_group_id` | 主控数据中心、计算节点、节点组标识；前两者至少一项，节点组可空 |
| `image_id` | 系统镜像标识，必填 |
| `cpu_cores`, `memory_mib` | CPU 核数、内存 MiB；内存以 128 MiB 为粒度 |
| `system_disk_gib`, `system_storage_id` | 系统盘 GiB、存储标识（可空） |
| `data_disk_gib`, `data_storage_id` | 数据盘 GiB、存储标识；无数据盘时存储标识须空 |
| `network_type` | `normal` 或 `vpc` |
| `bandwidth_mbps`, `inbound_bandwidth_mbps` | 出/入带宽 Mbps |
| `transfer_gib`, `traffic_direction` | 流量 GiB，方向 `in/out/all` |
| `ipv4_count`, `ipv6_count` | IP 数量，总数至少一个 |

带宽与流量 0 在**本协议**中代表主控默认值，不能据此推断任何第三方 API 的 0 值语义。所有标识为受限非秘密机器键，不是 URL、命令或显示名称。

### 主机策略与主控请求

策略文件由主机管理员维护，不提供浏览器编辑入口。下面地址仅为格式示例，不会自动启用；实际启用前必须实现并验收自己的接收端：

```json
{
  "schema": 1,
  "controllers": [{
    "id": "primary",
    "enabled": false,
    "endpoint": "https://controller.example.invalid/aster/v1/provision",
    "allowed_cidrs": ["192.0.2.0/24"],
    "token_file": "/etc/aster/provisioning/primary.token",
    "ca_file": ""
  }]
}
```

控制端收到 POST 至该固定 endpoint；核对结果为 GET 至 `endpoint/{operation_id}`。令牌使用 Bearer，操作 ID 使用 `Idempotency-Key`，请求摘要使用 `X-Aster-Request-SHA256`；接收端仍须自己持久化去重，不能把 HTTP header 当成去重实现。请求字段严格为 `protocol, operation_id, order_id, owner_ref, hostname, configuration_revision, configuration_sha256, product`；摘要基于发送的规范 JSON 字节，不基于重新排序的远端 JSON。结果字段严格为 `protocol, operation_id, order_id, request_sha256, status, resource_id`，最多 16 KiB。

主机专用数据库账号至少须读取 schema、订单/账单/币种及不可变配置/任务，更新任务并追加审计；不得给它修改订单付款状态、金额或币种设置的权限。Web 运行账号不得读取上述 root 私密文件或调用特权主机命令。本轮不安装 worker 或启用调度器。

边缘日志仅记录固定路由模板：商品产品配置、订单准备任务和查询任务分别归类为 `/api/v1/admin/catalog/:id/product-configuration`、`/api/v1/admin/orders/:id/provisioning-jobs`、`/api/v1/admin/provisioning-jobs/:id`。不把实际商品/订单/任务 ID、URL、查询参数或凭据写入路由字段。

## 迁移和验收

开发迁移 `013_product_configurations.sql` 追加三个表：`product_configurations`、`order_product_configurations`、`provisioning_jobs`，目标为 schema 13 / 23 表；已有 001–012 保持原文。迁移不补造旧订单配置，也不创建开通任务。备份与预检仍校验完整迁移前缀和精确物理表/列清单，不因新表放宽未知对象检查，并保留旧 schema 6–12 的备份前缀识别。

开发期 schema 变更后必须用匹配新旧源码分别验证预检、迁移重复执行、升级前备份和隔离恢复。旧程序不得直接连回新 schema；回滚须停写并恢复匹配的升级前备份与旧程序，不能删除迁移记录伪降级。开发测试通过不代表正式发布、真实主控对接或原生部署验收已完成。
