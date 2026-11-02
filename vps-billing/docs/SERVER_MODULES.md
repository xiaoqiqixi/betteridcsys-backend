# 服务器模块作者与运维指南

Core 0.6 使用 `aster.server/1` 把商品、付款和服务任务连接到独立的开通平台。财务核心只依赖通用协议；星屿云的 `aster.controller/1`、`aster.service/1` SDK 和旧资源槽均位于 [`plugins/server.aster-cloud`](../backend/plugins/server.aster-cloud/README.md)。Core 不导入这些包。

一个接口（connection）就是一个批准的插件插槽，对应一台主控。每个接口有独立进程、UID、socket、凭据和持久化目录。商品直接绑定一个接口：

```json
{"module_id":"server.aster-cloud","connection_id":"east","config":{"preset_id":"standard"}}
```

财务没有接口组、放置策略或跨接口调度。主控决定节点、容量和放置。商品 `module: null` 表示人工开通；付款后员工填写主机名和地址，再确认开通。商品目录的地区、节点属于销售目录，不是计算节点选择器。

## 实现模块

从 [`server-template`](../backend/plugins/server-template/README.md) 开始，实现 [`serverv1.Processor`](../backend/pkg/pluginapi/serverv1/contract.go) 的 `Call(context.Context, Request) (Response, error)`。Linux 入口调用 `pluginserver.RunServer`；不要自行实现未认证的 socket 或任意命令执行接口。模板的内存状态仅供学习，真实模块必须提供持久化收据。

| 方法 | 作用 |
| --- | --- |
| `describe` | 模块 ID、名称、版本、能力、商品/下单 schema、自定义动作 |
| `test_connection` | 验证本接口的受保护连接 |
| `validate_product` | 校验、规范化商品配置，返回规范 JSON 的 SHA-256 digest |
| `create`, `suspend`, `unsuspend`, `terminate`, `change_package` | 服务生命周期 |
| `renew` | 可选续费钩子；付款事务入队，输入含 invoice_id、period_start、period_end |
| `power`, `reinstall`, `reset_password`, `rescue_enter`, `rescue_exit` | 按实际能力提供管理操作 |
| `snapshot_create`, `snapshot_restore`, `snapshot_delete`, `action` | 快照及带 `action_id` 的自定义动作 |
| `status`, `operation_status`, `console`, `metrics`, `snapshots`, `images` | 只读观察；`console` 返回短期一次性 URL |

名称使用 `{"zh-CN":"示例","en":"Example"}`。`describe.capabilities` 表示模块支持的方法，`status.capabilities` 和 `status.custom_actions` 表示当前资源实际可用的操作。后台和客户端据此呈现控件；前端隐藏按钮不能替代后端的所有权、权限和状态校验。

每次变更带 `operation_id`、`digest`、`service_id`、`owner_ref`、配置和输入。调用 `Request.Seal()` 生成规范摘要，模块用 `Verify()` 验证。`operation_id` 相同且摘要相同必须返回同一操作收据；同 ID 不同摘要必须拒绝。返回 `done` 表示已完成，`accepted` 表示等待核对，`failed` 只用于明确失败。响应必须回显协议、操作 ID、原摘要；`create` 成功必须包含资源 ID。

必须在发送前持久化操作领取状态；断线、超时、无效响应、崩溃或提交确认丢失均按不确定处理。Core 只调用 `operation_status`，**绝不自动重发变更**。插件重启后也只能查询已领取操作。查询接口不能顺带创建资源或继续发送初始化命令；多阶段初始化由插件内部持久化状态机推进，每阶段只领取一次。明确失败且已有资源时返回其 ID；管理员显式重试必须复用该资源。

## Schema 与秘密

Schema 根为 `type: object`、`additionalProperties: false`，最多 64 个属性。支持 `string`、`integer`、`number`、`boolean`、标量 `enum` 和 `array<enum>`，以及 required、default、数值/长度范围。字段 `x-i18n` 支持中英文 title/description：

```json
{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "plan":{"type":"string","enum":["small","large"],"x-i18n":{"zh-CN":{"title":"套餐"},"en":{"title":"Plan"}}},
    "backup":{"type":"boolean","default":false}
  },
  "required":["plan"]
}
```

不支持嵌套对象、远程 `$ref`、任意 HTML 或脚本。前端使用通用 `SchemaForm`；后端仍必须独立规范化并验证。商品配置和下单字段会进入购买快照，必须只包含非秘密参数。连接秘密放在 systemd credentials 中。下单时不能把模块密码保存到待付款订单；使用模块生成的凭据、SSH 公钥或开通后的直接动作。星屿云原生结账密码只保留单向 hash，不保留明文。

自定义动作包含 `id`、多语言 `name`、`danger`、多语言 `confirm` 和 `input_schema`；密码字段声明 `writeOnly: true`，核心从持久化输入中剥离，仅在本次 `Request.Secrets` 传递。危险动作需二次确认。不得把密码、令牌、请求体或提供方原始错误写入数据库、收据、日志或审计。一次性 URL 不写入持久化任务。

快照是否必须停机由模块 `snapshots` 返回的 `requires_stopped` 决定。核心在直接操作和任务派发前读取并校验此限制；支持在线快照的平台可返回 `false`，星屿云模块仍返回 `true`。

## 打包、安装与批准

构建本地 Linux amd64 归档，不安装、不启用，也不创建发布：

```sh
python3 -B vps-billing/deploy/scripts/build-server-module.py \
  --plugin aster-cloud --output artifacts/server-aster-cloud.tar.gz
# --plugin template 构建作者模板
```

归档含程序、manifest、README、LICENSE、第三方许可清单和 SHA256SUMS。manifest 使用 `server.<name>`、`aster.server/1`、`server.call`、独立版本与 Core `>=0.6.0,<0.7.0` 兼容区间。构建器用安装器解析、ELF 检查和摘要核验验证输出；不覆盖已有归档。新增依赖须补齐许可清单。

主机管理员审核程序、出站目标和归档摘要后，使用 `plugin-manager.py verify/install/select/enable`。每个接口使用独立的 `aster-srv-<connection>` 非登录账户和 canonical socket `/run/aster-server/<connection>/plugin.sock`。示例中 UID、目标和 revision 必须换成主机实际值：

```sh
python3 /opt/aster/current/scripts/plugin-manager.py install server-aster-cloud.tar.gz --sha256 "$ARCHIVE_SHA256"
python3 /opt/aster/current/scripts/plugin-manager.py select \
  --kind server --slot east --id server.aster-cloud --version 1.0.0 \
  --worker-uid "$WORKER_UID" --client-uid "$API_UID" \
  --target controller.example:443 --revision "$POLICY_REVISION"
# status 显示新 revision；enable 必须使用这个新值。
python3 /opt/aster/current/scripts/plugin-manager.py enable --kind server --slot east --revision "$NEW_REVISION"
systemctl enable --now aster-server@east.service
```

使用仓库提供的 [`aster-server@.service`](../deploy/systemd/aster-server@.service)。先建立账户、`aster-plugins` 通信组和 root 私有的 `/etc/aster/server/east/`。API 账户通过通信组访问 socket，worker 不加入 API 的私有组。`LoadCredential=server:/etc/aster/server/east/` 把文件映射为 `server_<filename>`；`ReadServerCredential` 只接受这些固定名称。星屿云所需 `config.json`、`token`、可选 `ca.pem` 见插件 README。

插槽策略 `servers[]` 保存 `slot/id/version/manifest_sha256/worker_uid/client_uid/socket/allowed_targets/enabled`，不保存密码。每次调用重新核验审批与程序身份，双方校验 Unix peer UID。仅服务器槽允许受保护 root CLI 作为额外调用方；SMTP、支付槽仍拒绝 root。各 worker UID、socket 独立，API 无安装、审批或 systemd 控制权。

worker 的 `/var/lib/aster-server/<connection>/` 为 0700 私有持久化状态。升级或回退必须保留收据。停止旧 worker，安装独立版本，按当前 revision 切换批准版本，再启动并核对在途操作；不要原地覆盖程序或删除未知操作的收据。网络目标白名单不是操作系统出站隔离，部署时仍需设置出口策略。

## 财务 API 与运营

完整字段以[前端契约](../contract/openapi.json)为准：

- `GET /admin/integrations/modules`、`GET /admin/integrations/connections` 查看模块和接口。
- `PATCH /admin/integrations/connections/{id}` 使用 revision 更新名称/启用状态；宿主批准仍是调用前提。
- `POST .../{id}/test` 测试连接，`POST .../{id}/sync-images` 同步模板。镜像按接口隔离，新镜像默认禁用，员工显式启用并绑定商品。
- 商品 `module` 只绑定一个接口，服务保留购买时的配置与 digest；改商品不会改写既有服务。
- `Product.order_schema` 与 `CartItemInput.module_fields` 传递额外非秘密下单字段。
- `POST /services/{id}/actions/{action_id}` 使用 `{input}` 和幂等键，返回持久化任务。
- 人工服务通过管理员服务 PATCH 的 `manual_info` 保存主机名、IPv4/IPv6，再执行 `provision`；模块服务拒绝人工覆盖远端信息。

`auto_provision` 控制新开通，已领取任务仍进行只读核对。暂停、恢复、终止、套餐变更只有模块确认后才更新服务结果。禁用接口阻止新商品绑定和新开通；已有操作仍需核对，不能把禁用当作取消远端操作。原 root CLI 保留，使用通用槽：

```sh
sudo env PROVISIONING_TRANSPORT=server \
  PROVISIONING_MYSQL_DSN_FILE=/etc/aster/provisioning/mysql.dsn \
  PROVISIONING_PLUGIN_ROOT=/opt/aster/plugins \
  PROVISIONING_PLUGIN_POLICY_FILE=/etc/aster/plugins.json \
  /opt/aster/current/bin/aster-api -dispatch-provisioning "$TASK_ID"
# 相同环境下 -reconcile-provisioning "$TASK_ID" 只核对。
```

不再使用 `SERVICE_CONTROLLER_POLICY_FILE`、旧 `PROVISIONING_POLICY_FILE` 或资源槽回退。0.5.10 已配置主控的迁移步骤见[插件迁移说明](../backend/plugins/server.aster-cloud/README.md#migrate-a-configured-controller)。先迁移数据库 schema，停写、核清旧任务，再运行 host-only 迁移工具；每次明确选择一个主控，生成一个接口。工具不安装、审批或启动 worker。未知旧操作必须先核清，不能用迁移绕过重放保护。

## 验证

```sh
cd vps-billing/backend
go test -race ./pkg/pluginapi/serverv1/... ./plugins/server-template ./plugins/server.aster-cloud/...
```

作者用 `conformance.Test` 提供隔离平台 fixture；可为方法、自定义动作和秘密传入 fixture 数据。运行器检查描述、规范化、create、摘要冲突、重放、异步查询、生命周期、自定义动作及取消。还必须用平台故障夹具验证发送后断线、重启恢复和秘密不落盘。

Linux `Server modules` CI 检查认证 socket、插槽安装/隔离、Core 无云依赖，并从云仓库最新 `main` 构建真实主控，配合云仓库假节点 runner，完成真实 MySQL 的订单→付款→模块→active 服务→停止/启动，断言只分配一次资源。产物记录确切云提交和非秘密结果。`Commerce core` CI 检查 fake module 生命周期、未知结果不重发、数据库/审计中无密码及迁移。真实 libvirt/KVM 验收由独立 `Order to VM` CI 执行；假节点烟测不代替它。
