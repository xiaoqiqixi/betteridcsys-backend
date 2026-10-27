# 星屿云 · 财务系统后端

Go / MySQL 财务 Core：商品分类和独立报价、客户/管理员账户、订单/账单、历史快照、会话安全、审计与插件运行时。不内置真实收款渠道，不把付款成功等同于虚拟机已开通。

[财务前端](https://github.com/xiaoqiqixi/betteridcsys-frontend) · [财务后端](https://github.com/xiaoqiqixi/betteridcsys-backend) · [主控与被控](https://github.com/xiaoqiqixi/betteridcsys-cloud)

## 目录与兼容

| 目录 | 内容 |
| --- | --- |
| `vps-billing/backend/` | 独立 Go 模块、API、schema 1–13、插件协议与测试 |
| `vps-billing/deploy/` | systemd/Nginx、原生安装、受控升级、备份与 CI 工具 |
| `vps-billing/docs/` | [API](vps-billing/docs/CONTRACT.md)、[安装](vps-billing/docs/INSTALL.md)、[运维](vps-billing/docs/UPGRADE.md)与能力边界 |
| `docs/` | 跨系统合同、开发清单和历史验收记录，不是新仓库已验收声明 |

Core **0.5.10** / schema13 / 23表；Ubuntu 22.04 / 24.04 amd64、Oracle MySQL 8.4 LTS、原生 Linux，不使用 Docker；暂不支持 Ubuntu26。默认中文 / CNY，保留独立币种报价，不自动换汇，不重算历史金额。

## 构建与测试

开发环境使用 Go 1.27.3。运行已编译服务不需要 Go 或 Node。

```sh
cd vps-billing/backend
go mod download
go test -race -count=1 ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o ../../bin/aster-api ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o ../../bin/aster-plugin-host ./cmd/plugin-host
```

未提供 `ASTER_MYSQL_TEST_DSN` 时数据库测试会明确跳过，不算完整验收。真实测试须使用名称含 `_test` 的隔离库；不要把生产凭据交给测试。原生 CI 在 Ubuntu22/24 自建、清理 socket-only MySQL，检查 Go 测试没有跳过。

前端来自独立仓库；此处不复制 Vue 应用或混入未提交前端。站点部署需要与已验证前端发行产物配对，并由 Nginx 提供同源 HTTPS。

## 安装与更新通道

保留的安装/发行脚本仍含原项目的固定来源合同；迁移前不能把它们当作本仓库已有正式下载通道。不会简单替换仓库字符串或跳过 SHA/签名校验来“兼容”。新仓库的首版发行、前端产物组合、安装/更新来源及旧实例受控迁移均在 [迁移清单](MIGRATION.md)中单独验收。

## 拆分与交付状态

本仓库从已提交、通过原项目 CI 的 main `fa4d2139ff5ecfc2802c0825e839134ca488d138` 拆出，不包含原工作区未完成的 schema14/员工权限改动。保留 Apache-2.0 许可证；初始来源见 [SOURCE_ORIGIN.json](SOURCE_ORIGIN.json)。它是来源清单，不是发行签名或本仓库的验收证明。

只使用 `main` 分支，不导入旧 Git 历史、贡献者署名或旧标签。原仓库 CI 结果不自动继承；本仓库构建与验收以自己的提交和运行记录为准。

三个新仓库均完成构建、首版 Release、匿名产物下载/校验和安装更新来源迁移后，才删除原仓库及其 Releases。未完成迁移门禁时不得删除；具体门禁见 [仓库迁移清单](MIGRATION.md)。
