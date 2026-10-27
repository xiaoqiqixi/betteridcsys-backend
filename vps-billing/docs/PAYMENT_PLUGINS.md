# 支付运行时插件接入

支付渠道由运营者选择，由开发者依据渠道官方协议编写独立程序。Core 0.5.3 提供 `aster.payment/1` 运行时插槽：Core 编译后可读取插件目录中的元数据，经主机管理员批准后通过 Unix socket 调用独立进程，替换兼容插件无需重新编译 Core。

本指南适用于 Core 0.5.3 及声明兼容的支付插件。Core 0.5.2 及更早程序不支持此运行时协议，须先按升级指南更新 Core，不能直接套用本文操作。仓库没有可用于真实收款的官方支付渠道；合成示例不会连接平台、扣款或退款。

## 责任边界

Core 掌握客户身份、订单和账单、整数金额、币种精度以及财务状态。插件仅创建渠道结账会话、按所选渠道协议验证通知并返回交易证据；没有数据库接口、改账接口或自动开通权限。浏览器回跳、客户端成功字段及打开结账页面均不能证明付款。

渠道开发者负责商户接口、秘密管理、签名算法与原始字节规则、商户身份验证、时间戳及重放窗口、密钥轮换和渠道幂等行为。Core 在此基础上再次比对已保存的业务记录，持久化处理重复与冲突通知。Core 信任经过批准的验签程序；进程隔离不能证明任意插件的验签实现正确。

当前协议提供创建会话和验证已结算通知，不提供查询、撤销、退款、订阅、对账或资源开通。不能把渠道超时解释为付款失败，也不能通过重新生成支付键来避开原尝试。

## 独立程序与元数据

公共 Go 类型位于 `backend/pkg/pluginapi/paymentv1`，协议封装位于 `backend/pkg/pluginapi`，可复用的 Unix socket 服务端位于 `backend/pkg/pluginserver`。插件不导入 `internal/billing`，也不需要与 Core 链接构建。其他语言可按相同 JSON 合同实现。

```text
/opt/aster/plugins/payment.<name>/<version>/
  plugin
  manifest.json
```

manifest 使用严格 JSON，拒绝未知字段、重复键、null 和超限内容。如下为结构示意，哈希须由实际文件计算，示例不可直接安装：

```json
{
  "schema": 1,
  "id": "payment.example",
  "version": "1.0.0",
  "protocol": "aster.payment/1",
  "core_min": "0.5.3",
  "core_max_exclusive": "0.6.0",
  "capabilities": ["payment.checkout", "payment.verify_callback"],
  "executable": "plugin",
  "sha256": "实际程序的64位小写SHA256",
  "payment": {
    "currencies": [{"code": "CNY", "precision": 2}],
    "checkout_hosts": ["checkout.example.com"]
  }
}
```

声明最多 32 个币种和 8 个结账主机，能力数组的两个条目及其顺序须与示例一致。每个支持币种必须显式声明精度，0–6 位；插件须与账单历史快照完全一致，不换汇、不舍入。结账链接只允许 HTTPS 和清单中的精确 DNS 主机，拒绝凭据、端口和 fragment。币种登记与渠道支持分别校验。

Core 只读取受保护的目录和文件，不因发现文件而执行、下载或启动插件。审批固定 ID、版本、manifest 哈希、程序哈希、商户身份、worker/client UID 和 socket。双方核验 Unix peer UID，握手核对协议、身份、版本、程序哈希和随机 nonce；当前调用还固定读取审批时的 manifest 哈希和商户身份，避免审批切换造成串用。

## 安装、审批与进程生命周期

先审查开发者的源码、依赖、来源记录、校验文件、渠道合同和测试结果，再使用当前版本的 `deploy/scripts/plugin-manager.py`。该工具可核验并安装资源归档与独立 Linux amd64 程序；命令合同与资源结构以工具 `--help` 和[插件指南](PLUGINS.md)为准。不要从后台上传任意程序或只修改 `PAYMENT_PROVIDER` 期待自动安装。

审批示例中的 UID、socket、商户 ID、revision 和版本均须换成实际值：

```sh
python3 /opt/aster/current/deploy/scripts/plugin-manager.py select \
  --kind payment --revision 0 --id payment.example --version 1.0.0 \
  --worker-uid 1102 --client-uid 1100 \
  --socket /run/aster-payment/plugin.sock --merchant-id merchant-example \
  --target gateway.example.com:443
```

`revision` 必须与当前策略一致，首次选择使用 revision 0，保存后变为 1；选择时默认关闭，再以最新 revision 明确 enable；工具拒绝陈旧写入。`disable`、`enable` 和 `rollback` 对支付槽须明确指定 `--kind payment`，回退仍需通过兼容和哈希校验。停用不删除数据库中的在途支付记录，也不表示渠道交易已取消。

使用 `deploy/systemd/aster-payment.service` 作为经审查的主机模板。worker 使用与 API、SMTP 都不同的非登录用户，不能加入可读 API 秘密的组；socket 共享组只用于通信。模板限制可写路径、设备、内核权限、进程数、内存和 CPU，抑制插件原始输出，并由 systemd 管理退出、重启与停止。宝塔自定义的数据库、备份及秘密路径必须另行加入隔离规则。

支付秘密由插件自己的受保护配置管理。推荐以 systemd `LoadCredential=` 交付给专用服务；宿主只传递受限的 `CREDENTIALS_DIRECTORY`，不继承 API 环境、DSN、会话秘密或 SMTP 凭据。模板未预配任何真实商户秘密或网络出口授权；开发者必须说明所需域名、证书和出口策略。

审批后由管理员启动服务。Core 不执行服务管理；完成批准项读取与文件校验后，单次 RPC 最多 12 秒，其中握手另受最多 2 秒约束，RPC 不自动重试。业务选择、握手检查与 RPC 共用最多 8 个调用槽，worker 同时最多处理 8 个操作。批准文件的磁盘读取不由 RPC 超时中断，不能将 12 秒理解为整个 HTTP 请求的硬上限。

连续协议或连接故障触发短暂熔断。调用方主动取消不等于插件故障；熔断状态归属于当次批准的完整支付选择，旧选择下的在途成功或失败不会影响新选择。worker 崩溃、卡住、身份不符或版本不兼容时，支付请求按不可用处理，Core 其余功能继续运行。超时和取消都不会解除已持久化的支付尝试。

管理员可读取 `GET /api/v1/admin/plugins` 查看发现项和支付槽状态，或带会话与 CSRF 调用 `POST /api/v1/admin/plugins/payment/check` 进行不携带渠道秘密的握手检查。检查成功只说明进程身份和协议兼容，不代表渠道可收款。

## 结账与支付尝试

客户调用 `POST /api/v1/invoices/{id}/checkout`，需要有效会话、CSRF 和正确 Host。Core 从持久化记录取得客户、订单、账单、金额和币种；请求 body 中的金额、账户或成功字段不会成为付款依据。管理员专用账单路径不允许结账。

Core 在调用插件之前，在数据库事务中锁定订单和账单，重新验证所有权、待付状态、金额与快照，并保存唯一支付尝试。插件取得核心生成的 `AttemptID`，`IdempotencyKey` 与它相同，还取得账单/订单/账户 ID、金额、币种、精度及核心生成的回调 URL。支付尝试持久化固定 provider 与商户身份；每次 RPC 另外固定当次批准的 manifest 哈希，防止读取批准项后发生切换。批准相同 provider 和商户的新兼容版本，表示允许该版本继续验证此前的在途支付，不要求旧程序永久运行。当前支付槽一次只选择一个 provider 与商户；切换前须核对旧在途交易并安排可验证旧通知的兼容版本，不能把更换选择当作撤销旧交易。

成功返回仍采用 `provider_id`、`session_id`、`checkout_url`、`expires_at`。同账单重复点击复用仍有效的已保存会话，不再次调用渠道。创建中、超时、响应丢失、会话失效或结果不确定时返回冲突/未确认状态，保留原尝试，不自动重新付款。会话过期不能证明渠道没有收款；当前没有解除在途记录或渠道查询接口，需要开发者在渠道接入中设计后续核对能力，勿直接改数据库绕过。

取消订单与创建支付尝试使用一致的事务锁顺序。一旦存在支付尝试，即使插件停用也不能直接取消；没有支付尝试且满足原有条件的未付订单可正常取消并作废账单。

## 回调与原子结算

服务商通知地址为 `POST /api/v1/payment-callbacks/{providerID}`。仅此专用路径免浏览器 CSRF，不免 Host、方法、请求大小或验签检查。原始 body 最多 64 KiB，保留签名所需请求方法、路径、query 和受限 headers；不传浏览器 Cookie 与 CSRF token。头采用 `[{"name":"X-Signature","values":["..."]}]` 形式，避免动态 JSON 键破坏严格协议。

worker 必须先验证签名、商户和时间窗口，再返回 `VerifiedEvent`。结果包含尝试、provider、事件、渠道交易、商户、账户、订单、账单 ID，以及金额、币种、精度、结算时间和 `settled` 状态。不能从未验签 JSON 直接构造成功证据。

Core 从保存的尝试取得独立预期值，在同一数据库事务中重验订单/账单和所有绑定，并原子保存交易、`paid` 账单/订单、审计与 `payment.settled` outbox 事件。`(provider_id,event_id)` 与 `(provider_id,transaction_id)` 分别唯一；完全相同通知幂等返回成功，同键改变交易或业务绑定返回冲突。已付订单不会再次入账，也不会凭此启动 VPS。

成功（含合法重放）返回 HTTP 200：`{"data":{"received":true}}`。格式或验签失败返回 400，不存在的尝试返回 404，身份/账务冲突返回 409，插件或存储不可用返回 503。只有数据库提交成功才确认收到；服务商须能按此应答合同重试。需要特定明文、XML 或签名应答的渠道，当前通用路由尚不支持，须先扩展并审查协议，不能假称已兼容。

普通日志不记录回调原文、签名、渠道错误或秘密。数据库唯一约束提供持久化去重；插件负责渠道通知窗口，两者互不替代。升级、备份和恢复必须包括支付尝试与结算记录；恢复较旧备份后应先核对渠道侧后续交易，避免恢复结果被误当成没有付款。

## 合成插件与契约测试

`backend/plugins/paymentfixture` 提供只用于本地测试的程序与 HMAC 合同，返回保留的 `.test` 域名，不访问任何平台。示例密钥仅证明合成消息由测试端签发，不能证明真实收款。它用于学习独立进程、原始 body 签名、金额比对、重放、故障和状态转换，不作为收款渠道发布。独立命令接收宿主的 `--socket`、`--client-uid`、`--targets`、`--merchant-id`，只从 systemd credential `payment-fixture-key` 读取 32–4096 字节的本地测试密钥。合成通知在原始 JSON body 上计算 HMAC-SHA256，将小写 hex 放入单值 `X-Fixture-Signature`；通知时间须在最近 5 分钟内，未来偏差最多 30 秒。参见同目录 Go 测试构造完整事件，勿复用示例密钥于真实系统。

```sh
cd vps-billing/backend
go test -race ./pkg/pluginapi/... ./pkg/pluginserver/... \
  ./internal/runtimeplugins/... ./plugins/paymentfixture/... ./internal/billing
```

完整数据库测试需要隔离 MySQL 测试实例；未配置测试 DSN 时的跳过不能视为迁移、并发或恢复通过。CI 使用仓库固定的原生 MySQL 环境。第三方渠道仍须提供官方协议来源、签名正反例、商户/币种矩阵、超时和重复通知策略，以及实际测试商户验收；这些不是合成测试所能替代的。
