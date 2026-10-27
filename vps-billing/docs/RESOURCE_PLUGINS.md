# 独立资源插件：主控对接

Core `0.5.10` 首次正式提供资源协议 `aster.resource/1`；独立 worker **`resource.aster-cloud 0.1.1` 仍为候选**，不包含在 Core 六资产中，也没有因 Core 发行而自动发布。它不是支付渠道，也不是旧 `aster-billing-plugin` CLI 的改名。worker 由系统插件管理器校验、安装、选择、启用/禁用、回滚，经独立 systemd 服务加载；已有财务主机派发命令可显式选择这个 transport。网页不能安装、执行或配置任意插件路径，也不能从这里直接派发虚拟机。

**最低 Core 明确为 `0.5.10`，最高范围 `<0.6.0`。** Core、plugin-host 和 plugin-manager 必须来自同一经核验版本；旧正式 v0.5.9 没有此协议，旧开发包误声明的 `core_min=0.5.9` 不能作为兼容依据，新管理器和运行时均拒绝。资源包不是 Core 六资产之一，须独立核验与批准；此文不是 Release 成功或真机验收声明。

**旧开发包迁移：** 旧 main 本地曾生成 0.1.0（最低 Core 误写为 0.5.9）。当前候选独立提升为 **0.1.1**，使用新版本目录和新 manifest/程序摘要，不覆盖、重打或复用已批准的 0.1.0。先维护停用旧 worker，保留原包和审批记录；用 0.5.10 工具独立核验/安装 0.1.1，再按实际 revision 重新选择、批准和启用。失败不退回 HTTPS transport、不宣称旧包兼容；回滚旧 Core/插件必须核对其完整兼容组合，不能用新版工具强行接受错误最低版本。

## 不变的业务边界

- 保留 `aster.controller/1` 原 8 字段请求、原 6 字段结果、订单快照、operation ID 和 digest。资源 RPC 仅把 canonical JSON bytes 放入 base64 外封套；不把配置改成当前商品，也不生成新任务 ID。
- 仍由原 `performProvisioning` 在准备 transport 后原子 claim 已持久化的 job，再 send 一次并落库 complete；已 claim/unknown 的任务不重新 send。对账必须显式执行原 reconcile 命令，只有 read-only lookup。
- 不读写金额、币种、付款记录或历史快照，不生成本地已开通/已付款状态。`accepted` 只是主控受理，不是虚拟机已运行。
- 默认仍是原 HTTPS 路径。选择 resource 后没有 HTTP fallback；worker 缺失/禁用/不匹配会失败，不偷偷绕过插件。

## 安装和选择

目标为 Linux amd64、Ubuntu 22.04 / 24.04。下面是受信构建包的显式主机流程，不是独立插件已公开发行的下载承诺；Core 的两平台原生验证不能替代该 worker 的真实 systemd 安装、生产 token 权限及主控/KVM 验收。

先在受信构建环境构建可校验的包，无网络下载/执行包脚本；构建过程不启用服务：

```sh
GOTOOLCHAIN=go1.27.3 python3 vps-billing/deploy/scripts/build-resource-plugin.py \
  --output /tmp/aster-resource-aster-cloud-0.1.1-linux-amd64.tar.gz
# 单独保存上一步的 SHA256，传到服务器后重新计算比较。
```

包内含固定 inventory、manifest、静态 ELF、完整项目和 Go 许可证、内部 checksum。用本轮的 `plugin-manager.py` 执行 verify/install，需 root-owned、不可被其他用户写入的包和祖先路径。不要把本地普通用户所有的包直接当成主机批准文件。

```sh
python3 /opt/aster/current/deploy/scripts/plugin-manager.py verify \
  /root/approved/aster-resource-aster-cloud-0.1.1-linux-amd64.tar.gz --sha256 <核对后的摘要>
python3 /opt/aster/current/deploy/scripts/plugin-manager.py install \
  /root/approved/aster-resource-aster-cloud-0.1.1-linux-amd64.tar.gz --sha256 <同一摘要>
```

建立专用非登录 `aster-resource` 用户/组，与 API、SMTP、payment 用户隔离，不加入它们的组。根据 `status` 取得当前 revision；下面的 revision 必须按实际递增，不能直接照抄为生产值：

```sh
python3 /opt/aster/current/deploy/scripts/plugin-manager.py status
python3 /opt/aster/current/deploy/scripts/plugin-manager.py select --kind resource \
  --revision <当前revision> --id resource.aster-cloud --version 0.1.1 \
  --worker-uid "$(id -u aster-resource)" --client-uid 0 \
  --socket /run/aster-resource/plugin.sock --controller-id primary \
  --target controller.example.com:443
python3 /opt/aster/current/deploy/scripts/plugin-manager.py enable \
  --kind resource --revision <select返回的revision>
```

资源 slot **只允许 root host command 客户端**；worker 必须非 root；与其他 slot 的 worker/客户端 UID 及 socket 不得冲突。安装/选择/启用不启动程序。单独审核、部署 `deploy/systemd/aster-resource.service` 后才 `systemctl enable --now aster-resource`。该 unit 不是现有 core 安装器自动开通的服务，不要声称财务升级已经把资源服务启动了。

## 固定凭据，不经浏览器/RPC

主机 root 创建 `/etc/aster/resource/` 为 0700，以下文件为 root:root 0600。systemd `LoadCredential` 为独立 worker 提供只读副本，worker 只接受固定 credential 名称和保护好的目录；不继承 API 环境、DB 口令或付款凭据。

`policy.json`：

```json
{
  "schema": 1,
  "controller_id": "primary",
  "endpoint": "https://controller.example.com/aster/v1/provision",
  "allowed_cidrs": ["192.0.2.0/24"],
  "custom_ca": false
}
```

地址和 CIDR 必须按实际受信主控填写（上面是文档保留网段）。`controller.token` 是该主控专用受限 token，**不要写进命令行、商品参数、RPC 或日志**。默认使用系统可信 CA。若主控使用私有 CA，设置 `custom_ca:true` 并增加 root-owned systemd drop-in：

```ini
[Service]
LoadCredential=resource-ca:/etc/aster/resource/ca.pem
```

worker 启动时校验固定 `controller_id`、launcher 批准的精确目标 host/port、HTTPS URL、CIDR、凭据和 CA，再解析并 pin 一个允许的 IP。保留原传输的主机名和证书验证、禁用代理/重定向/自动重拨。DNS/凭据/目标修改需要重启服务，不在单笔请求中替换。policy 与 slot 的 controller ID 必须一致。

RPC 握手返回并验证 approved manifest identity/hash/capabilities/nonce；同一个 Unix 物理连接只允许一次 send **或** lookup。另一个连接、重放、混用 send/lookup 均拒绝。网络错误/缺失字段/错误 digest/超时/worker panic 一律 unknown；错误只输出固定分类，不转发远端错误正文或秘密。

独立进程隔离不等于恶意插件沙箱：被 root 批准的资源 worker 必然能读取其专用主控 token 并获得相应主控权限。仅安装经审核并 pin 摘要的二进制，主控 token 权限应限制到目标财务租户；部署时继续使用出口防火墙限制主控目的地。

## 由原已持久化 job 派发

下面是主机 root 操作，不是 API 环境。保持原隔离的、root 0600 `PROVISIONING_MYSQL_DSN_FILE`。resource 模式不得同时设置 `PROVISIONING_POLICY_FILE`（原 HTTPS policy）；配置含糊会拒绝：

```sh
export PROVISIONING_TRANSPORT=resource
export PROVISIONING_PLUGIN_ROOT=/opt/aster/plugins
export PROVISIONING_PLUGIN_POLICY_FILE=/etc/aster/plugins.json
export PROVISIONING_MYSQL_DSN_FILE=/etc/aster/host-provisioning/mysql.dsn
unset PROVISIONING_POLICY_FILE
/opt/aster/current/bin/aster-api -dispatch-provisioning <已有job-id>
# unknown 不重新派发。确认主控情况后使用只读 lookup 对账：
/opt/aster/current/bin/aster-api -reconcile-provisioning <同一个job-id>
```

恢复原 HTTPS 是运维显式切换：停止新派发，先处置 sending/uncertain job，再清理 plugin root/policy 环境并指定原 `PROVISIONING_POLICY_FILE`。不要用换 transport 的办法重发不确定任务。plugin-manager rollback 只能选择已安装的旧版本，不覆盖旧包，服务重启仍由运维执行。

若回退到**不认识 resource 的旧 core**，仅 disable 还不够：旧 parser 会拒绝整个含 resource 的 policy。先停止资源服务及新派发，处理 uncertain job，使用本轮 manager `disable --kind resource --revision <当前值>`，再 `remove --kind resource --revision <新值>`，保留 SMTP/payment 条目及已安装不可变资源包后才回退 core。`remove` 只接受 disabled 的 resource slot，不删除软件、不代替停服务、不支持删除 SMTP/payment。

## 验证范围

新增自动测试覆盖 canonical 原请求/原结果、保留 null resources、身份/目标 pin、root-only 客户端政策、同物理连接握手和 4 种重放组合完整 HTTP 409、processor 只调用一次、禁用/选择轮换、unknown/panic 不重发且秘密不出错、独立资源 slot 生命周期、原 HTTPS 回归及原主机 claim/complete 回归。Linux socket/peer UID、systemd credential 行为必须在 Linux 执行，macOS 交叉编译不计为通过。

真实主控+KVM 开通/网络效果、生产 token 权限与真实 systemd service 安装验收仍需对应环境证据。本轮测试不付款、不接触真实客户、不用演示成功冒充开通。
