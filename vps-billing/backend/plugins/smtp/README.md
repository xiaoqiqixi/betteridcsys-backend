# 官方独立 SMTP 插件 1.1.0

此 Linux amd64 程序单独编译、安装和发布，标签为 `smtp-v1.1.0`，协议为 `aster.smtp/1`，支持 Core `>=0.4.0,<0.6.0`。核心通过受限 Unix socket RPC 调用，经独立用户和握手验证后才传入本次凭据。安装或替换插件不需要重新编译核心，文件出现不会自动执行。

发布同时提供原始可执行文件 `aster-smtp-1.1.0-linux-amd64`、资源包 `aster-smtp-1.1.0-linux-amd64-resources.tar.gz`，以及 BUILD、PROVENANCE、SHA256SUMS。资源目录只含 `manifest.json`、`LICENSE`、`README.md`、`THIRD_PARTY_NOTICES.json`、`SHA256SUMS`，不含 ELF。先核对两类资产独立摘要，再用新版 `plugin-manager.py verify/install` 的 `--binary` 和 `--binary-sha256` 参数组装；安装本身不启用程序。

主机管理员必须批准固定版本、清单哈希、不同非特权 UID、socket 和精确 SMTP 主机/端口，并单独管理 `aster-smtp.service`。升级采用不可覆盖目录和原子策略切换。旧公开 1.0.0 资产不改写，其 Core 上界仍为 `<0.5.0`；1.1.0 保持协议不变并声明 `<0.6.0`，旧 Core 管理工具不自动获得拆分安装能力。详细安装、核验及回退见 [PLUGINS.md](https://github.com/xiaoqiqixi/betteridcsys/blob/main/vps-billing/docs/PLUGINS.md)、[PLUGIN_PROTOCOL.md](https://github.com/xiaoqiqixi/betteridcsys/blob/main/vps-billing/docs/PLUGIN_PROTOCOL.md)。

本程序只负责 SMTP 传输。核心负责管理员授权、CSRF、已保存设置、凭据加密、版本冲突、发送限速和审计；插件不读取核心配置、加密密钥或账务数据库，也不记录凭据、邮件内容或 SMTP 原始回复。当前核心只接入管理员主动测试邮件，未接入通知、邮箱验证或密码找回。

开发目录中的 `Send(ctx, Config, Message)` 与 `NewClient().Send(ctx, Config, Message)` 使用同一实现。`ValidateConfig`、`ValidateMessage` 和 `ValidateRecipient` 只做本地验证，不连接网络。配置包含 `Host`、`Port`、`TLSMode`、`Username`、`Password`、`FromAddress`、`FromName`，凭据由调用方仅在内存中传入。

## 传输和输入边界

- 仅支持 `starttls` 和 `implicit_tls`。最低 TLS 1.2，使用操作系统信任根，必须验证证书链与配置主机的 DNS/IP 身份。STARTTLS 缺失或证书无效时停止，认证不会降级到明文。
- 管理员可配置同机或私网中继；它同样需要有效证书。主机只能是 DNS 名或不带方括号/区域标识的 IP 地址，端口为 1–65535。网络目的地的管理权限和主机出口策略由核心与运维负责。
- 认证要求用户名与密码同时存在；在验证 TLS 后优先使用服务器声明的 AUTH PLAIN，否则使用 AUTH LOGIN。不支持匿名中继、OAuth 或未声明的认证机制。
- 仅支持一个 ASCII 裸邮箱收件人及纯文本 UTF-8 邮件。拒绝展示名式收件人、多收件人、头部控制字符、地址注入及无效编码；主题最多 200 字节，正文最多 64 KiB，发件展示名最多 128 字节。正文规范化为 CRLF，并使用按行限制长度的 MIME base64 编码。
- 每次调用总上限 30 秒，DNS/连接最多 5 秒，每个协议阶段最多 10 秒；调用方更短的 context 截止时间优先。取消会关闭本次连接。每次连接最多读取 512 KiB 对端字节（包括 TLS 记录），避免无界 SMTP 回复占用内存。

核心 RPC 调用总期限为 12 秒、握手最多 2 秒，不能重新连接并自动重发 RPC。插件进程退出、身份/哈希不符或 socket 不可用时，核心返回安全分类错误并继续处理其他请求。运行进程隔离和 systemd 资源限制不构成任意不可信代码沙箱；本插件必然能看见交给它的 SMTP 凭据。只安装经审核和批准的程序。

## 结果与重试

只有明确发生在 MAIL 命令之前的暂时失败可能自动重试一次，间隔 200 毫秒；总计最多两次连接尝试。MAIL 开始后不自动重试。DATA 写入、终止符刷新或最终回复存在不确定性时，返回 `SMTP_DELIVERY_UNCERTAIN`，`DeliveryUncertain=true`，禁止调用方自动重发。

最终 DATA `250` 只表示 SMTP 服务器接受邮件，不保证收件箱投递。此后连接关闭错误不能改成发送失败；核心也必须保留已经明确接受的结果。没有队列、投递回执、SMTP 服务端幂等保证、DKIM 签名、HTML 或附件功能。

错误只暴露固定 `Code`、`DeliveryUncertain`、`RetryableBeforeDelivery` 分类；没有保留或展开原始网络、TLS、认证或 SMTP 错误。调用方不应将错误分类视为实际投递证明。

## 验证范围

`smtp_test.go` 使用临时本地 SMTP/TLS 服务和仅测试代码可设置的信任根，覆盖两种 TLS 模式、PLAIN/LOGIN、证书与主机名拒绝、超时/取消、有限重试、DATA 不确定性、回复字节上限、头部输入及并发会话。

`internal/billing/email_smtp_e2e_test.go` 在隔离子进程中通过标准系统证书入口装载临时 CA，验证真实管理员 HTTP → 加密保存配置 → 测试中显式注入的 SMTP 传输 → 本地 TLS 接受 → 审计。生产接口没有跳过证书验证或注入测试信任根的开关。所有测试只连接回环地址，不发送外部邮件；真实服务商的连通性、SPF/DKIM/DMARC 和收件箱投递仍需单独授权和验证。

独立插件和核心的实际不同 UID/systemd 替换、崩溃与回退验收由 `deploy/ci/plugin-native.sh` 执行，并保存对应 run 的 `plugin-native.json`；是否通过以该提交的实际证据为准。原始第三方许可证文字在包内 `THIRD_PARTY_NOTICES.json`，它不是完整 SBOM 或法律合规结论。
