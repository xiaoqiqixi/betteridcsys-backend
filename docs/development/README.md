# 财务开发与发布指南

[使用文档](../README.md)

## 代码与测试

- `vps-billing/backend/`：Go API、插件运行时、迁移和数据库测试。
- `vps-billing/deploy/`：原生安装、升级、备份和 CI 工具。
- [API 合同](../../vps-billing/docs/CONTRACT.md)与[财务模型](../../vps-billing/docs/FINANCIAL_MODEL.md)定义业务边界。
- [验证记录](../../vps-billing/docs/VALIDATION.md)和[发布工具](../../vps-billing/docs/CI_RELEASE_AUTOMATION.md)记录精确测试范围。

实库测试只使用名称含 `_test` 的隔离库。Ubuntu22/24 CI 创建 socket-only MySQL 并要求 Go 测试零跳过；没有 DSN 的单元检查不能代替实库回归。浏览器、安装、升级与恢复需各自的独立证据。

### 独立前后端配对验收

[FRONTEND_PAIR.json](../../FRONTEND_PAIR.json)固定前端仓库 ID、完整提交、版本和兼容文档摘要。CI 先核验 GitHub 仓库身份与两份独立 Git 源码，再构建前端；不从可变 main、旧前端或 demo 数据回退。前端仍是独立仓库，临时检出到 `vps-billing/frontend/` 只用于既有浏览器入口，不导入后端 Git 历史。

生成的 TypeScript 编译缓存不得作为受版本管理的源码。构建前后都核对 Git 源码未变；可忽略正常生成的 `dist/`、依赖目录和编译缓存，但不能忽略已跟踪文件的差异。

`Real MySQL financial browser acceptance` 连接真实 Go API、独立 MySQL 和严格 HTTPS；只在专用 Firefox 配置中信任临时测试 CA，不改宿主证书信任。检查客户/管理员、中英文、手机/桌面、错误文案与历史账务数据不变。现有浏览器只读业务检查不替代 Go 事务测试、实际支付渠道或原生升级验收。

保留的 `frontend-source-pair.json` 分别记录后端和前端 SHA，静态候选包及清单记录本次实际构建内容；它们不是 Release 或独立签名。发布时仍需完成固定来源验签、前后端资产配对、安装/升级/备份/恢复和回滚门禁。修改配对文件必须重新完成相关 CI，不能继承其他源码的通过结果。

### 独立后端候选制品

[backend_bundle.py](../../scripts/backend_bundle.py) 从当前已提交的后端 Git 快照构建 Linux amd64 API 与插件宿主，保留迁移、部署资源、文档、Go 模块与原始许可证。它不检出、构建或夹带前端；`BACKEND.json` 单独记录后端 SHA 和 `FRONTEND_PAIR.json` 的前端身份。两个仓库不是同一来源提交。

公开 CLI 提供 `build --source-sha SHA --output NEW_DIRECTORY` 和 `inspect --manifest FILE --archive FILE --expected-source-sha SHA --expected-frontend-sha SHA`。构建要求非 root、干净源码与全长提交；只读检查验证有界单一 gzip、规范 tar、固定文件布局、Linux amd64 二进制、完整清单及精确配对。拒绝重复字段、链接、路径穿越、夹带前端、缺少迁移或改写已有输出。测试使用真实 Git、Go 与制品字节，不以模拟 ELF 代替构建。

Ubuntu22/24 CI 在实库回归后各构建实际后端两次并比较文件，制品名绑定运行 ID 与 attempt。`backend-tar-v1` 是新的独立后端内容格式，**不是旧 `raw-binaries-v1` 安装包**；内容摘要也不是来源签名。旧安装器不接受它，不能手工复制成旧 `BUILD.json` 或伪造安装收据。三仓库发行认证、前端发行资产绑定、新安装/升级消费路径及旧实例迁移完成前，只保留候选，不作为用户下载入口。

### 用实际候选程序完成配对浏览器验收

[pair_runtime.py](../../scripts/pair_runtime.py) 的 `stage` 检查两份候选包后创建新的私有运行目录，`verify` 检查其来源、内容清单、布局、权限和文件是否变化。API 使用后端包中的原始二进制，网页使用前端包中的原始静态文件；许可证分别保留。`PAIR.json` 明确为未认证、未安装候选，不生成旧格式的 `BUILD.json` 或安装收据。

[前端内容协议](../../contracts/frontend-bundle/SOURCE.json)仅保留已审查的静态包解析工具和许可证，固定到前端 `aa86bf0` 的原始字节，不包含应用源码。协议工具的来源与被测前端应用的来源分别处理，后者仍由 `FRONTEND_PAIR.json` 约束。

CI 通过 `--candidate-root` 明确选择候选包模式，不允许失败后退回源码处理器。已有 Go overlay 只创建专用 MySQL 数据、保存历史行快照并代理回环 HTTPS；真正的 API 请求交给包内 `aster-api` 进程。启动前核对二进制摘要和实际 `-version`，只注入专用 Unix 数据库和私密配置；退出后确认进程正常关闭、历史业务行不变及隔离库已删除。证书仍只进入临时 Firefox 配置。报告中两项 `packaged_*` 标志必须来自本轮实际生命周期，而不是“文件存在”。

这项验收覆盖已打包程序与网页的真实 MySQL/浏览器组合，不等于 systemd/Nginx 安装、来源签名、旧实例升级、备份恢复或回滚验收；这些门禁仍须独立完成。

## 设计与实施

- [内置升级方案](BUILTIN_UPGRADES.md)：财务整站、云主控及节点的受控升级。
- [功能实施计划](../EXECUTION_PLAN.md)与[逐项台账](../execution-tracker.json)：未完成项和验收标准。
- [身份操作合同](../IDENTITY_OPERATIONS.md)：权限变更、审计与会话失效。

设计规格和工程记录不代表功能已发布。用户手册只描述相应版本可操作的功能。

## 来源与发布记录

[仓库迁移](REPOSITORY_MIGRATION.md) · [初始文件来源](../../SOURCE_ORIGIN.json) · [历史归属变更](../HISTORY_REWRITE.md)

历史记录按其提交和运行 ID 阅读，不自动继承为当前版本证据：
[Core 公开验收](../CORE_PUBLIC_PREPARE_ACCEPTANCE.md)、[Cloud 公开验收](../CLOUD_PUBLIC_ACCEPTANCE.md)、[10 月 27 日执行记录](../EXECUTION-20261027.md)。

## 本地构建与测试

后端源码位于 `vps-billing/backend/`，使用 Go 1.27.3；部署工具和手册分别位于 `vps-billing/deploy/`、`vps-billing/docs/`。

```sh
cd vps-billing/backend
go mod download
go test -race -count=1 ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o ../../bin/aster-api ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o ../../bin/aster-plugin-host ./cmd/plugin-host
```

测试应在隔离 Linux 环境运行。真实数据库测试需要 `ASTER_MYSQL_TEST_DSN`；未配置时跳过的测试不计为数据库验证。不要向测试提供生产数据库凭据。
