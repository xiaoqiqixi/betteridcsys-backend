# Resource runtime 验证记录（2026-10-23）

此记录只覆盖独立资源插件和派发 transport，不是整个系统生产验收或真机 KVM 验收。未发布新 Release，未改现有标签。

## 实际执行

- macOS / Go 1.27.3：`go test -race -count=1` 的 pluginapi、resourcev1、controllerapi、internal/controllers、resourcebridge 全通过；包含真实回环验证 TLS、CIDR拒绝、原请求/结果完整绑定。`go vet` 通过；Linux amd64 静态 worker/core/host 及测试二进制交叉编译通过。
- 用户授权 Ubuntu22，Linux 5.15，独立 nobody 用户，不使用数据库：完整 runtimeplugins **112 PASS 事件，0 FAIL、0 SKIP**（包含原 SMTP/payment 及新增 resource）；host provisioning **9 PASS 事件，0 FAIL、0 SKIP**；resourcebridge **3 PASS 事件，0 FAIL、0 SKIP**。
- Ubuntu22 Python3.10：原与新增 `test-plugin-manager.py` **51 tests OK**，包含资源安装/选择/启用/禁用/回滚/disabled-remove，原 SMTP/payment 保留，root-only 资源客户端及 UID/socket 冲突拒绝。
- 真实编译的资源包经过 Linux **生产 plugin-manager verify**，`verified:true`、`service_action:none`。包 binary SHA256 `a9b07ce58f39c48c3921ece31896d0677907cea71ff73a0a641f75b4fe12efdf`；验证包 SHA256 `d4a6c8d4275510907b77ae110be37e12a824f4073aa53969e8ed1d2796355d5d`。构建 helper 输出完整许可证、声明、静态 ELF 和 checksum；这些不是 GitHub 发布产物。
- 验证结束已删除本轮 `/var/tmp/aster-resource-gate-20261023` 和 `/run/aster-resource-gate-20261023`，关闭专用 SSH master；原 API、edge、MySQL 三服务起止均 active。没有安装软件、建长期账号、改 CA/网络或修改生产 DB。

Linux 测试由 Go1.27.3 交叉编译二进制执行，**非 race**；不能把这段 Ubuntu 记录说成 Linux race 通过。Linux race 与完整 MySQL/原财务验收仍由最终 main 的正式 CI 独立判断。

## 保留的失败及修正

首次 macOS manager 运行因系统 `/var` symlink、后因缺少 Linux `renameat2(RENAME_NOREPLACE)` 无法做安装测试；没有为此降低文件/原子安装检查。Ubuntu 首次 manager 运行的两项版本对照失败是上传仅有脚本、缺少相对路径下 `protocol.go` 和 SMTP VERSION；补齐并 hash 校验最小 source layout 后完整 51 项通过。没有删除/skip 测试、重试业务请求或放宽协议。

非敏感原始证据留在忽略目录 `artifacts/resource-runtime-20261023/`：`runtime.log`、`server.log`、`bridge.log`、`manager.log`（首次失败）、`manager-final.log`、`package-verify.json`、`cleanup.log`。只有 nonce/合成请求及固定错误分类，不保留登录凭据、主控 token 或用户数据。

## 未验证/未提供

- 未安装或启动新的真实 systemd resource service，未使用生产主控 token，未在真实财务库派发订单。真实 service sandbox、credentials mount 和实际跨进程 root↔worker 主机验收仍需专门部署证据；peer UID / socket / 文件边界已由 Linux测试覆盖。
- 不自动安装 resource service，不提供 Web 上传或执行插件，不自动扫描/重发 queued/uncertain job。派发/对账仍由已有显式 root host command 调用，故保留人工控制和原数据库 claim 边界。
- 未改变旧 `aster-billing-plugin` CLI 的身份，未把它假称为新 manager runtime；两者用途不同。

## 可复现 CI 包门禁

`deploy/ci/check.sh` 在完整 Go race/MySQL、vet/core 静态构建之后，前端检查之前调用：

```sh
bash vps-billing/deploy/ci/check-resource-package.sh /absolute/path/to/repository
```

仅接受原生 Linux amd64 与原生 Go 工具链，依赖 system Python3；非 root runner 需要无交互 sudo。其他平台或缺少前提直接失败，**不把跳过当通过**。编译由调用者完成，不提权编译；独立 root 半段在固定 `/run/aster-resource-package-gate.<8随机字符>` 创建 root0700 目录，复制并核验包与 manager 的 SHA256，只调用生产 `plugin-manager verify`。不安装、不修改 policy、不启动或启用 service。验证无数据库/主控 token 输入；manager 子进程使用隔离 Python 和最小环境。清理核对固定前缀、owner/mode、inode 和限定文件名；遇到被替换目录或不明文件拒绝递归删除。

新增边界单测覆盖 hash 错误、symlink/相对路径/FIFO/超限输入、保护目录前缀/UID/mode/inode、限定 cleanup、隔离环境与 verify-only；`test_check.py` 覆盖资源包失败穿透 tee、停止后续前端检查以及前提文件缺失。Ubuntu22 以 nobody 执行本轮 `test_check.py` 与 `test_resource_package.py`，共 **23 tests OK**。另在 root 隔离目录执行完整 helper 编排 fixture：复用之前真实 Go1.27.3 构建的 ELF（binary SHA256 不变），生产 manager verify 成功，故意错误 hash 失败，成功/失败两路的 `/run` 和 build 临时目录均已清理。

**该 Ubuntu22 未安装 Go，编译器调用由 fixture 模拟，并非原生 Go 构建通过。** fixture 输出包含生产 helper 的成功文案，但不能作为原生构建证据；实际原生静态构建由最终 main CI 执行。证据：`ci-helper-linux-tests.log`、`ci-helper-linux-fixture.log`、`ci-helper-cleanup.log`。此次 helper 验收不安装任何软件/插件或服务，原财务三服务维持 active。
