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

`Real MySQL financial browser acceptance` 连接真实 Go API、独立 MySQL 和严格 HTTPS；只在专用 Firefox 配置中信任临时测试 CA，不改宿主证书信任。检查客户/管理员、中英文、手机/桌面、错误文案与历史账务数据不变。现有浏览器只读业务检查不替代 Go 事务测试、实际支付渠道或原生升级验收。

保留的 `frontend-source-pair.json` 分别记录后端和前端 SHA，静态候选包及清单记录本次实际构建内容；它们不是 Release 或独立签名。发布时仍需完成固定来源验签、前后端资产配对、安装/升级/备份/恢复和回滚门禁。修改配对文件必须重新完成相关 CI，不能继承其他源码的通过结果。

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
