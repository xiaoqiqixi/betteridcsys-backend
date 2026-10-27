# Core v0.5.7 发布说明

状态：**控制台实现的 main 完整 CI 已通过；最终标签构建、正常 Release 与六资产校验分别按实际证据确认**。前一正式版本为 Core `0.5.6`，本轮目标为 `v0.5.7`。本地检查不表示新标签、正常 Release、公共资产核验或生产安装已经成功；正式结论必须绑定本轮精确提交与实际运行。历史 [0.5.6 发布资料](RELEASE.md)、旧标签与既有正式附件不重写或覆盖。

## 范围与不变边界

- 将候选客户/管理员控制台正式接入；云服务器购买、订单、账单、账户资料、管理设置和角色导航继续使用真实 API 与明确的认证状态。
- 顶部“语言与币种”仅承载语言、购买/报表币种；侧栏/手机导航底部“账户”承载账户资料、浅色/深色外观和退出，删除重复偏好入口。
- 浅色/深色及蓝/青/紫品牌统一使用语义 token，修复状态文字、主操作、表头、表单与弹层的颜色角色；声明值测量与实际浏览器检查分别记录。
- 不新增数据库迁移，继续 schema 10；已有 001–010 迁移及校验和、CNY 默认、独立报价、0–6 位精度、历史快照、支付幂等、认证/CSRF、审计和管理员保护边界不变。
- 不增加真实 VPS 开通、支付渠道、退款、邮件业务通知或新的 SMTP 功能；界面可用不等于业务已支付、服务已开通或邮件已送达。

## 独立版本与资产

| 组件 | 目标/现有标签 | 版本与兼容 |
| --- | --- | --- |
| Core | 准备 `v0.5.7` | API、插件宿主和资源包必须同为 `0.5.7` |
| SMTP | 保留 `smtp-v1.1.0` | `1.1.0`、`aster.smtp/1`、Core `>=0.4.0,<0.6.0`；不重发 SMTP |

Core 六资产仍为：

1. `aster-api-0.5.7-linux-amd64`
2. `aster-plugin-host-0.5.7-linux-amd64`
3. `aster-core-0.5.7-linux-amd64-resources.tar.gz`
4. `BUILD.json`
5. `PROVENANCE.json`
6. `SHA256SUMS`

资源归档唯一根目录为 `aster-core-0.5.7-linux-amd64-resources/`，不含 ELF、秘密、数据库转储或测试输出；两个静态 ELF 单独核验。BUILD schema 3 / `raw-binaries-v1`、来源摘要、大小、许可证与固定官方仓库检查保持严格。SMTP 1.0.0 的旧兼容上界 `<0.5.0` 不得为本次改写；需要运行 SMTP 的实例须独立核对已批准的兼容 `1.1.0`，或明确停用不兼容旧插件。

## 构建与安装示例

只有已审查并提交的源码与对应版本声明可用于正式构建。以下为说明，不表示已经执行；`COMMIT_SHA` 必须替换为本轮最终真实提交：

```sh
vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind core --version 0.5.7 --output /workspace/core-a
vps-billing/deploy/scripts/package-release.sh \
  --commit COMMIT_SHA --kind core --version 0.5.7 --output /workspace/core-b
diff -r /workspace/core-a /workspace/core-b
```

脚本只从精确 Git archive 构建，不将未提交工作区作为发布源码，也不能把 0.5.6 程序重命名为 0.5.7。待正常 Release 和公共资产全部核验后，再按 [安装](INSTALL.md)、[升级](UPGRADE.md)、[宝塔](BAOTA.md)、[插件](PLUGINS.md) 和 [更新交接](UPDATES.md) 准备同版本受保护工具。`prepare` 不切换服务，`stage` 不执行升级；实际主机变更仍需人工审查规格并确认精确计划摘要。

## 本轮本地验收记录与正式门禁

控制台实现提交 `845ddc7b20d1c9b6de7fd34890c440cffa858723` 的 [main 完整 CI 39590301919](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39590301919) 已通过；本发行文档校正不改运行源码或依赖。最终标签 Runtime build 仍对精确发行提交重新验收、双构建，再由独立发布器交付，不能把前一阶段 CI 当作 Release 成功。

- 正式前端实际完成 837 条 Vitest、19 条 Node 数据层测试，typecheck、format:check、生产 build 通过。
- 独立 `npm ci` 后复核的生产产物 SHA-256 一致；本轮 `npm audit` 全依赖报告 0 项漏洞。
- root 通过 CUA 实际打开正式 dist 的浅色合成只读 fixture：桌面商品 89、币种 142、手机 catalog 67，共 298 项文字测量、0 失败；3 项 disabled/group-opacity 明确跳过，不计通过。保存 3 张实际截图并目视桌面/手机，浅色偏好重载后保留。这不是 MySQL-backed E2E，也不冒充完整双语言、双外观/三品牌和浏览器矩阵验收。
- 后端/MySQL、安全与历史升级、原生 Linux amd64/systemd/Nginx HTTPS、严格 supervisor、备份恢复及插件隔离的正式结论，以本轮提交实际 Actions 证据为准；本地 Darwin 的有限入口不替代这些门禁。
- 精确提交双构建、完整 CI、正常 Release 与匿名下载六资产核验，分别绑定实际 run/attempt、源码与来源文件；不能从本地绿灯或历史 0.5.6 发行推导本次已经通过。

发布须以获准的精确源码为单位，通过完整门禁与独立发布器后交付；不得覆盖既有标签或资产。失败和不确定结果保持原样并调查，不借历史绿灯宣称本版通过。实际结论填入 [VALIDATION.md](VALIDATION.md) 顶部本轮小节。
