# 财务独立发行工具

后端来源固定为 `xiaoqiqixi/betteridcsys-backend`（ID `1435344087`）；前端固定为 `xiaoqiqixi/betteridcsys-frontend`（ID `1435344023`）。安装、暂存和准备路径不访问旧仓库。历史格式解析器和 `historical-artifacts.json` 仅保留原始档案含义，不能把旧仓库附件声明为新仓库发行。

## 配对与附件

`FRONTEND_PAIR.json` 固定前端提交、版本、兼容文件摘要、归档摘要、FRONTEND 清单摘要及 CI artifact ID / ZIP 摘要 / run ID / attempt。更新配对必须一次性审核这些值；不能改成分支、latest 或候选摘要白名单。当前 pin 是已有的 `aa86bf06…` 候选，不代表正在重写的前端已验收。CI artifact 会过期，过期即拒绝；届时应审核新的成功产物并提交新的完整 pin。

前端下载检查 `.github/workflows/ci.yml` 的成功 `frontend` job、完整仓库 ID、提交和重跑次数，校验 ZIP 及两个成员摘要，再运行 `contracts/frontend-bundle/frontend_bundle.py` 的固定摘要检查器。不读取前端 `src/`、路由或组件布局。

`paired-bundles-v1` 有七个附件：

- `aster-finance-backend-<CORE>-linux-amd64.tar.gz`、`BACKEND.json`
- `aster-finance-frontend-<FRONTEND>.tar.gz`、`FRONTEND.json`
- `BUILD.json`、`PROVENANCE.json`、`SHA256SUMS`

前后端版本可以不同，但 `compatibility.json` 的 Core 版本必须与配对一致。后端包带有后续准备所需的完整检查工具。发行工作流为七个附件产生 GitHub artifact attestations。主机使用系统安装的 `/usr/bin/gh attestation verify` 验证 Sigstore 签名、证书和透明日志时间戳，并检查证书中的仓库 ID、owner ID、工作流、源码、标签和 run/attempt；同时通过 GitHub API 检查精确 `release` job 成功。网页只读取有界元数据，明确把签名检查留给主机准备步骤，不下载或执行归档。

## 发布条件与 dry-run

`.github/workflows/release.yml` 仅接受 `v*` 标签和 `workflow_dispatch`。标签必须是源码 Core 版本对应的稳定版本；手动运行始终是 dry-run，没有发布开关。PR 使用独立只读 `release-tooling.yml`，不签名、不创建 Release。后台包构建两次，逐文件比较，再配对前端。

标签发布要求该源码已保留在 main，并且 `.github/workflows/ci.yml` 的同一成功运行包含以下成功 job。原生 job 尚未接入，所以当前正式发布会被阻止：

| 必须成功的 job 名称 | 当前接入状态 |
| --- | --- |
| `Go and real MySQL (ubuntu-22.04)` | 已有 |
| `Go and real MySQL (ubuntu-24.04)` | 已有 |
| `vulnerability` | 已有 |
| `Real MySQL financial browser acceptance` | 已有 |
| `Native financial acceptance (ubuntu-22.04)` | 待 CI 负责人添加 |
| `Native financial acceptance (ubuntu-24.04)` | 待 CI 负责人添加 |

原生矩阵应依次准备隔离 runner、使用 `scripts/frontend_pair.py pin/verify` 获取并验证独立前端 checkout、提供受保护的既有 `0.5.2` 基线（`ASTER_INSTALLED_LEGACY_BASELINE`）、在 `mysql-native.sh` 的真实 MySQL fixture 内调用 `accept-native.sh --allow-isolated-runner`。后者继续调用 Core、payment、plugin 验收。遗留基线需来自已备份并验证的现有安装；不会从旧仓库下载，也不能以复制附件或本地候选冒充门禁 6 的实际升级证明。

另应添加 `Synthetic financial performance (ubuntu-24.04)`，使用同一受控 MySQL fixture 调用 `performance-native.sh --allow-isolated-runner --repo-root ... --output ...`；保留性能报告和 cleanup receipt。性能结果独立记录，不替代原生或生产容量验收。

## 主机升级

从经审核的新仓库源码提供受保护的工具目录，保留 `scripts/`、`contracts/frontend-bundle/` 和 `vps-billing/deploy/scripts/` 的相对结构，或使用已完成签名验证的独立后端包内工具。不要先执行下载包中的验证程序来证明该包可信。主机需要受信任的系统 `/usr/bin/gh`，不存在关闭验签参数。

```sh
sudo python3 -I /protected/backend/vps-billing/deploy/scripts/core-manager.py prepare \
  --version <NEW_VERSION> --release-directory /opt/aster/releases/<NEW_VERSION>
```

`prepare` 验证新来源和签名、在内存中检查两份归档，并原子创建完整版本目录及本地准备收据。前端 `dist/` 映射为 `web/`；`core-operation.py plan/apply` 的现有备份、配置、数据库预检、确认摘要及切换流程仍负责激活，最终地址保持 `/opt/aster/current/web`。

既有 `raw-binaries-v1` 安装继续通过全部原有清单、权限和迁移校验。新目标需要签名的 paired 收据；不会通过本地构建绕过正式准备。已装 `0.5.10` 的主机需要更高的首次新仓库发行版本，不能用同版本号绕过升级顺序。

## 验证范围

`bash scripts/check-release-tooling.sh` 在 Linux 普通用户下运行配对、真实 Git/Go 打包、攻击输入、主机准备与 executor、旧格式安装回归，以及 `go test -race -count=1 ./internal/updates/...`。它不启动宿主服务或生产数据库。`check.sh` 保留完整旧回归入口，前端部分改为验证独立 checkout 和配对 CI bundle。各 native wrapper 的 `--check` 运行其非特权测试；真正的 systemd/用户/数据库变更仍限制为显式选择的 GitHub-hosted Ubuntu fixture。

本机 macOS 的全量旧测试不能代替 Linux 结果：`renameat2`、Linux xattr、pidfd、`/proc` 和 Linux Go runtimeplugins 无法在该平台运行。此外，旧跨组件测试仍引用已移走的 `aster-cloud/**`、旧前端浏览器/路由源码、缺失的 `performance.yml` / `core-public-prepare.yml`；这些失败保留并单独报告，未删除或跳过断言。新增发行检查的通过不代表这些旧跨仓库测试或门禁 2/3/5/6 全部完成。
