# 财务验证记录

## 2026-10-27：正式Core0.5.10与发布后公开验收

以下分别记录来源与实际范围，不以任一项替代其他项：

- 发行源码`dab0167d58cd99a19fc4a0eafff23bbe7ff95cf0`的[财务40435816730](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40435816730)五门全部成功：Ubuntu22/24完整Go/MySQL/race、前端、systemd/TLS、插件崩溃、安装升级/备份恢复/回滚及浏览器均实跑。
- [Runtime build40439181617](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40439181617)与[独立发布器40441128635](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40441128635)成功，正常非草稿Release `424987299`，六资产绑定精确发行源码。
- [公开prepare40441393371](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40441393371)在Ubuntu22/24 **2/2成功**：实际无凭据下载、完整来源、生产prepare、202发行文件/13迁移与原始收据复核及清理通过；程序执行/安装false、服务/数据库动作none。
- 后续main `b132123`的[财务40439673220](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40439673220)5/5、[Cloud40439673255](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40439673255)11/11均成功。它不是发行资产的source SHA，更晚改动仍须自己的完整门禁。

完整资产/ZIP摘要和边界见[公开验收](../../docs/CORE_PUBLIC_PREPARE_ACCEPTANCE.md)及[27日执行记录](../../docs/EXECUTION-20261027.md)。
源码候选native链不冒充正式新资产整机安装，公开prepare也不替代用户主机、真实已准备旧分叉目录升级、宝塔环境、公共证书续期、真实支付、KVM/物理网络或全站最终视觉验收。
资源包内文档是构建前源码快照，状态文字以对应Release及发布后复验记录澄清，不重发旧标签/资产。

---

以下各日期段落保留当时状态与失败，不将历史待验改写为当时已通过。

## 2026-10-25：Core 0.5.10 / resource 0.1.1 候选（当时未发布）

以下记录本轮提交前的候选准备检查；主线提交/推送不等于发行，本阶段没有创建 tag/Release。旧记录、旧标签与历史资产不覆盖。远端只读核对时 `v0.5.10` ref 返回 404，8 个 Release 未找到同名项；未来发布前仍须重新核对。当前版本号不是完整验收声明。

Core API/plugin-host、前端 package/lock、插件管理器默认版本为 **0.5.10**；独立资源 worker/builder/gate 为 **0.1.1**，最低 Core **0.5.10**。旧本地 0.1.0 的错误最低版本不能复用，新候选使用新目录/摘要；旧目录不覆盖。SMTP 保持 **1.1.0**。本轮未改财务金额、支付、历史快照或 SQL；001–013 与旧正式 v0.5.9 的 `git diff` 为空，仍为 schema 13。生产来源祖先与本地收据条件未变；0.5.10 的受信文件清单为新增独立真实 MySQL 浏览器门禁按版本扩展，旧版本仍使用其对应清单。

| 本轮实际检查 | 结果与边界 |
| --- | --- |
| 发布控制器 / runtime release 离线回归 | **93 PASS**；包括新版本六资产/来源/SMTP/迁移和发行正文一致性，GitHub 全为 fixture，没有发布动作 |
| 主机 update-manager 离线回归 | **46 PASS**；含 0.5.10 stage 成功及分叉来源拒绝；HTTP 仅回环，不触达业务环境 |
| release-manifest | **11 PASS**；Core 0.5.10 / SMTP 1.1.0 精确版本检查 |
| 资源包 gate 单测 | **12 PASS**；包内 0.1.1/minCore 0.5.10 与 Go 常量一致，旧 0.1.0 gate 结果拒绝 |
| plugin-manager 便携版本/兼容聚焦 | **3 PASS**；当前版本、SMTP 范围、旧 Core/错误资源最低版本拒绝 |
| doctor / 负向 prepare 聚焦 | **2 PASS**；22/24 目标与验收状态分开，26/其他 unsupported；分叉 source、错误 merge-base、分叉 policy 拒绝且不发布目录 |
| Go pluginapi + resourcebridge | 两包 **race 与 vet PASS**，实际回环 TLS 测试通过；不是 Linux Unix RPC/真机/真实主控全量验收 |
| 资源包离线构建 | 生成 Linux amd64 **0.1.1** 静态包，生产 verify 函数在显式普通用户 trust fixture 下核验通过；无安装、审批或服务动作，不是 root 主机验收/正式下载资产 |
| 全后端受跟踪包检查 | 枚举 **20** 个受跟踪包，排除 ignored artifacts；Darwin race 整体未通过：3 包依赖 Linux-only runtimeplugins 导致 setup failed，另6包被标签排除；其余11包 **181 PASS事件 / 0断言失败 / 0 SKIP事件**。真实MySQL测试未运行，不能称全后端通过 |
| Linux 静态构建 / vet | 全20包 Linux/amd64、CGO=0 build 与 cross-vet 通过；API/plugin-host 静态ELF构建通过；runtimeplugins Linux测试二进制编译通过，未执行Linux RPC |
| Darwin portable vet | 11个无Linux依赖包 vet 通过；不以筛选后通过替代前一行的全范围失败事实 |
| Python 语法 / diff | 修改的 Python `py_compile`、`git diff --check` 通过；未改迁移、生产 ancestry 或执行器 |

上述 Python PASS 是本轮版本候选阶段的实际分组结果；后续增加独立实库浏览器受信入口及按版本分段清单后，受影响的来源/发布测试须再跑，不能直接复用这些数字当最终精确提交绿灯。

**明确未通过/未执行完成的部分：**本机 Darwin 运行完整 plugin-manager 共 **52 项，30 PASS / 22 ERROR**，22 项均因缺 Linux `renameat2` 原子不覆盖安装接口阻断，不能写成全量通过。新增成功 prepare 用例已通过来源/资源检查后，在同一 Linux 接口处阻断。新增 0.5.9→0.5.10 升级与缺 receipt 两用例，在既有测试 setUp 因 Darwin 缺 `os.removexattr` 阻断；不删除测试、不替换内核原子语义来假绿，须由 Linux 门禁跑完。

新增升级 fixture 使用真实 `prepare` 产生本地 PREPARED 收据（不是手写/伪造 receipt），0.5.9 与 0.5.10 保持相同 13 份合成迁移；GitHub、ELF、数据库和服务都明确为离线 fixture。它将验证来源连续性和受保护目录合同，**不能证明旧正式六资产、新公开 prepare、真实 MySQL/systemd 或客户主机升级已验收**。

原始支付 `TestPaymentServerRequiresOneOperationOnHandshakenConnection` 未改未删：checkout/verify 的四种重放、独立固定 Unix 连接、拒绝完整 HTTP409 和处理器仅调用一次的约束均保留。本机只完成 Linux 测试二进制编译，不能宣称该 Linux 测试已运行。

Linux 现有 `deploy/ci/check.sh` 已自动执行这几套测试；可单独复验：

```sh
cd vps-billing
python3 deploy/scripts/test-core-manager.py \
  CorePreparationTests.test_0510_new_main_source_prepares_without_history_exception \
  CorePreparationTests.test_0510_prepare_rejects_diverged_source_or_policy_before_publication
python3 deploy/scripts/test-core-operation.py \
  ExecutorTests.test_real_prepared_059_upgrades_to_0510_without_refetching_old_history \
  ExecutorTests.test_059_without_prepared_receipt_cannot_upgrade_to_0510
```

财务部署目标仅 Ubuntu 22.04/24.04 amd64；24.04 是既有完整 native CI 基线，**22.04 完整原生安装、HTTPS、升级、备份恢复和回滚仍未验收**。26.04 与其他发行版暂不支持；doctor 的支持报告没有修改 install/upgrade 宿主执行条件。没有只加 22.04 matrix 假称完成。最终精确提交完整 CI、真实 MySQL、原生全链、浏览器/视觉矩阵、依赖扫描、正式 Runtime build、Release 和匿名六资产/prepare 核验均须分别补齐，历史 0.5.2 public prepare 基线不能替代新版本的公开闭环。真实 KVM 验收仍后置。见 [0.5.10 候选清单](RELEASE-v0.5.10.md)。

---

## 2026-10-22：Core v0.5.9 正式发行完成

正常公开 [v0.5.9 Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.9)，Release ID `420315605`，源码固定 `4252fed37dfb4d4e7f621fbd0495a2cd2171c8b7`，不是后续主控开发提交。默认中文/CNY，Core 0.5.9 / SMTP 1.1.0；旧标签和资产未覆盖，财务发行不附带主控/KVM。

| 最终门禁 | 实际结果 |
| --- | --- |
| 精确 main 完整 CI | [39719713456](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39719713456) 全部成功；不是借用首轮局部绿灯 |
| 标签正式 Runtime build | [39721645970 / attempt 1](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39721645970) 全部成功：真实 MySQL 全范围 Go race **331顶层 / 1465含子项 PASS、0 FAIL / SKIP、无 race warning**；vet、静态构建通过 |
| 前端与浏览器 | 正式构建执行 **962 Vitest + 35 Node**、typecheck/format/build；完整 sandboxed Chrome 合成浏览器验收通过，30张实际运行截图、cleanup=true。此套使用loopback demo，不能冒充前述真实MySQL/CUA检查或真实支付渠道 |
| 原生部署与依赖 | Ubuntu24隔离 systemd / Nginx / HTTPS、业务、schema13升级、备份恢复和回滚门禁成功，npm与Go扫描成功；不是所有Linux或客户主机验收 |
| 可复现包与来源 | candidate/rebuild两次完整打包及provenance封装后的两次 `diff -r` 均成功；来源、run、attempt和受信政策绑定精确源码 |
| 独立发布器 | [39723864667](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39723864667) 自动成功，正常公开Release，恰好6个资产；不是标签推送成功即算发行 |
| 匿名公共核验 | 2026-10-22 04:44:30 UTC，无token下载全部6资产；逐一大小/SHA256与GitHub元数据、SHA256SUMS、BUILD和PROVENANCE一致，schema13完整13份SQL与选定Git blob一致；只读21次API、不解压、不执行、不安装 |

匿名核验回执 `artifacts/release-v0.5.9/public-final/VERIFICATION.json` SHA256：`a550819b90d0e6c967a02fbb3a277fe540010a8156aa411c9073aa51905a76a8`。Runtime、发布器日志及原生/浏览器artifact归档在ignored `artifacts/release-v0.5.9-checks/`。公开校验清单见 [SHA256SUMS](https://github.com/xiaoqiqixi/betteridcsys/releases/download/v0.5.9/SHA256SUMS)。

**仍未验收：**真实支付渠道/客户收款、真实业务邮件、物理手机全矩阵、公共证书续期、灾备与生产容量、完整主控/KVM及真实VM/网络/恢复。现有Ubuntu22财务测试站仍为v0.5.8，本轮没有自动升级或把虚拟化安装到该小内存主机。schema13回滚必须恢复匹配备份，不能只换回schema12二进制。

独立主控开发提交 `273549ca6908dab4c2eb5592c8f325ee3fbd20df` 已推main，开发CI及同提交财务完整CI成功；具体范围和真实KVM延期记录见 [主控验证](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/VALIDATION.md)。本节以下历史记录保持原样，失败和当时未验状态不被事后抹掉。

---

## 0.5.9 首轮 CI 浏览器探针路径合同失败（历史；后续已完整重验）

精确 main `233314895e730807d2c618f6079644680ef07a97` 的 [CI 39717442335](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39717442335) 整体失败：真实 MySQL 必需回归、原生 systemd/TLS/业务/备份恢复与回滚、npm 和 Go 依赖扫描均已通过，但新产品配置浏览器探针在最后的写入路径比较失败。四次保存、布局、定制下拉和关闭断言已先通过；探针拿 adapter-relative `/admin/...` 比较公共 dispatcher 实际记录的 URL.pathname `/api/v1/admin/...`，不是把请求失败当成功。

最小修复把精确四次、POST 方法及相同配置 endpoint 约束统一为被记录的完整 `/api/v1` 路径，并增加无浏览器路径合同回归，拒绝少/多写、其他商品、后缀、双前缀、相对路径和 GET。保留原来的四视口、全部断言及时间限制，不加重试、不删探针、不改应用 UI 或生产 API。完整原日志及失败 artifact 保留在 ignored `artifacts/release-v0.5.9-checks/ci-39717442335/`；必须对修复后精确提交重新跑完整 main CI，再考虑新标签与 Runtime build。该次独立 performance [39717442371](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39717442371) 成功，不代替失败的完整 CI。

---

## 2026-10-22：Core 0.5.9 正式发行门禁启动（待 CI）

用户已授权把本轮财务改动更新到 GitHub main 并正常发布新版本。Core 元数据、插件管理器默认、前端与安装示例统一为 0.5.9；SMTP 保留独立 1.1.0，不覆盖旧标签。新增公开 `backend/controllerapi` 是既有自有协议的类型别名，不暴露账务数据库或付款接口；契约测试实际通过。

提交前最终前端 962 Vitest + 34 Node、typecheck/format/build 均通过，发布管道 67、runtime 元数据 25、manifest 11 和插件当前版本契约 2 项通过。另在 Darwin/Python3.9 环境尝试全部 CI Python 322 项，100 项失败/错误（49 failures、51 errors）：缺 GNU timeout/Go PATH、Linux /proc/renameat2 与新 Python 安全解包 runtime 等，日志保留；该次不能作为通过，也不为通过而降级 Linux 守卫。完整 Linux 真实 MySQL、原生升级/恢复、浏览器及安全扫描由精确提交的正式 CI 重新执行。

此时尚未创建 v0.5.9 标签或 Release，以下为本地开发结果，不预填发行成功。独立主控/KVM 在相邻 `aster-cloud/` 开发，不混入财务六资产，不声称其功能已经完成。

---

## 2026-10-22：产品配置与手动任务验收记录（v0.5.9 发布准备，待最终 CI）

用户已授权准备财务 **v0.5.9** 发行；以下保留提交前的实际执行范围。最终同提交完整 CI、标签 Runtime build、独立发布器及公共六资产核验均待实际运行，不能从版本号或本地结果推定成功。主控/KVM 独立开发，不混入财务发行。

正式对照基线为 main `0a395970657fb89132efb666f2682b83d81cc57c` 的 **v0.5.8 / schema 12**。本轮按用户要求学习 IDCsmart 的商品、资源参数与执行策略分层思路，实现本系统自己的 `aster.controller/1` 合同，**不是对接其 API**。开发工作树追加 013，目标 **schema 13 / 23 表**，新增 `product_configurations`、`order_product_configurations`、`provisioning_jobs`；已有 001–012、金额、支付和历史快照边界保留。默认不启用 worker 或真实开通；没有创建 VM、真实付款或新增支付渠道。接口及限制见 [PRODUCT_CONFIGURATION.md](PRODUCT_CONFIGURATION.md)。

### 已实际执行的范围

| 检查 | 结果及精确边界 |
| --- | --- |
| 最终前端检查 | **962 条 Vitest + 34 条 Node 数据层测试通过，0 skip**；typecheck、format:check、生产 build 通过。当前 lockfile 的 npm **全依赖扫描 0 漏洞**，不据此推断 Go 或系统依赖风险 |
| Ubuntu22 / MySQL8.4.13 实库过程 | 原单进程完整 billing 在固定 **256 MiB** 内存预算下 OOM 失败，失败日志保留；没有提高限制或把该次算作通过。随后冷启动分组执行 **53 组、210 个顶层测试、952 个 PASS 事件、0 skip**，该基线在最终 canonical 商品 ID 修复前完成。最终配置、迁移、任务、catalog-order 四组增量 **147 个 PASS 事件、0 skip** 通过；两批不是同一最终源码的完整重跑，不能合并宣称最终同 SHA 全套通过 |
| Go 构建、检查与其他包 | Go **1.26.4** 的 Linux 交叉编译通过；controllers 本机 race 通过。Linux 的 cmd/server、controllers、integrations、updates、pluginapi/paymentfixture、SMTP 完整相应范围及 runtimeplugins 全套通过，Go 全 scope vet 通过。**不是最终源码全 scope race 验收** |
| 运行时插件容量测试诊断 | 原 `TestPaymentApprovalSharesRPCCapacityAndRecovers` 同一 binary 重复 25 轮，**24 FAIL / 1 PASS**，确认 Manager RPC 槽与服务端连接槽独立、服务端异步处理 close 的测试耦合。最小修仅将该测试限定为 Manager 槽清空、空审批路径上的 `PaymentCheck == ErrDisabled`，不改生产容量或协议、不加 sleep/重试。修后 **25/25 通过**，完整 runtimeplugins 通过；实际有效握手仍由 RuntimeContract 覆盖，原 OneOperationOnHandshakenConnection 测试未删除或放宽，完整 HTTP 409 与四种重放组合约束保留 |
| Linux 部署工具 portable 测试 | **180 项（44 + 21 + 24 + 91）通过**。范围为脚本、解析、渲染和隔离夹具合同，**不是本轮 schema13 原生安装、升级或备份恢复验收** |
| CUA 真实浏览器与实库 | 经实际 HTTPS API 连接隔离 MySQL，以管理员身份保存自定义产品配置 **r2 / r3** 并重新读取确认；不是 demo 数据或拦截响应。实际检查 **320 / 375 / 1440 px** 无横向溢出；自定义下拉、Escape、浏览器 Back、保存后目录刷新均检查，关闭后准确恢复 `standard` 商品触发器焦点。不是完整浏览器矩阵、物理手机矩阵或 WCAG 认证 |
| SQL 交叉核验 | 两条产品配置变更审计存在，独立价格 before/after **逐字节相同**；隔离实例订单、账单、支付尝试/结算、服务和开通任务均为 **0**。保存配置未下发任务、未制造资源或付款 |

### 未验证与交付状态

- **尚未完成**最终同提交的完整 CI、全 scope race 和全部浏览器矩阵。
- **尚未验证** schema13 原生升级、备份恢复及回滚；本轮 portable 或迁移测试不能替代这些门禁。
- **尚未完成**本轮 Go 依赖安全扫描；npm 0 漏洞不能代替 Go 扫描。
- 没有实现或验收真实主控接收端及 VM 生命周期，不能称已真实开通或兼容第三方主控 API。
- 正式 v0.5.8、公开 443、正式 API 18080 及其 MySQL 实例未更新。本轮**未推送、未发布**，不覆盖旧标签或资产，也不把开发工作树重新包装成正式 v0.5.8。
- 清理回执已确认：隔离 UI 数据库、专用 SQL 用户、OS UID、8443 / 18081 测试服务及其对应目录已删除，测试 MySQL r2 已停止；原 case 权限恢复 700，`mysql.cnf`、`test-dsn` 及临时浏览器密码文件删除。**保留私有、停机的测试 data 目录及日志**，不称全部测试数据目录已删除。正式 API / Nginx / MySQL 均 active。本轮没有新增 CA 或修改钥匙串，浏览器复用并正常校验现有 IP 证书。

本轮原始失败、分组结果、最终增量、前端日志、SQL 对照及实际截图在 ignored `artifacts/controller-20261022/`。以下历史记录原样保留，不用本轮局部结果改写旧失败或借用历史正式发行门禁。

---

## 0.5.8 第二轮 CI 旧备份 fixture 计数失败（待完整重验）

提交前补验：Ubuntu22 自带 Python3.10 下，完整备份 shell 及五组主机工具编排单测 **217项（11+48+45+23+90）全部通过、0 skip**，使用隔离 fakebin/回环夹具，不是新一次真实 systemd 安装。尝试完整 CI 编排321项时，22项被CI专用MySQL包安全解包器的Python>=3.12守卫拒绝，另3项因未配置已有Go工具PATH报错；原日志保留，不放宽守卫或把该次321项算通过，补齐独立测试runtime后及最终CI仍须完整验证。生产应用/主机工具范围与CI包下载测试runtime不能混称。

2026-10-21，精确 main `fe6c65707a3593da4c2cb76334c352cff5935a09` 的 [CI 39622586940](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39622586940) 已完成 Go race 全 scope 1317 PASS、0 SKIP、无 race warning（billing 488.457s），vet/静态构建及前端 914 Vitest + 27 Node、typecheck/format/build 均通过。随后 Python 首组 321 项中一项失败：`test_suite_runs_real_scans_without_ripgrep_installed` 的备份测试已新增 prefix11，却仍断言总数14及只验证prefix6–10；实际为8并发当前备份+1只读当前备份+6个旧前缀，共15。只修测试 fixture 计数并加强 prefix6–11、当前12的完整checksum/独立物理inventory对照；当前12仍必须恰好9份，旧11必须19表且无节点表/列，不改生产备份或安全扫描。后续 native/browser 等门禁仍跳过，整轮不是成功。原失败日志保留在 ignored `artifacts/release-v0.5.8-checks/ci-39622586940-fe6c657/`；修复须新提交重新完整执行，不能用已过的局部门禁代替。

---

## 0.5.8 首轮 CI 整包时限失败（待修复后完整重验）

2026-10-21，精确 main `ebdbc6cea58ec5e8387cc8efd813a800b73d6519` 的 [CI 39619608793](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39619608793) 失败：Go `internal/billing` 整包触发默认 600 秒总闹钟（600.041s），已记录 1267 个 PASS 事件、0 SKIP、无 race warning；当时分类子测试仅运行 0 秒，栈在 fixture 迁移，不能称断言失败或已证明并发死锁。后续 native、浏览器等门禁未执行，不计通过；失败日志保留在本地 ignored `artifacts/release-v0.5.8-checks/ci-39619608793-ebdbc6c/`。

独立 Ubuntu22 全量实库 Go race 已实际完成（billing 包 938.641s、全 scope 1317 PASS）；因此只给 CI 整包设置固定有界 `-timeout=20m`，保留 `-race -count=1 -v ./...`、pipefail、所有断言及禁止 SKIP 扫描，不修改生产或单操作时限、不添加重试。新增 fake-tools 合同测试验证固定命令、继承超时不能改写该显式参数、Go 非零退出穿透 tee；它不是新增一次真实 MySQL 验收。同 SHA 的 [performance 39619609072](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39619609072) 独立成功，不能替代失败的完整 CI。修复后必须对新的精确提交重新跑完整门禁，再考虑标签和发布；旧失败结论不改写。

---

## Core 0.5.8 四层分类与默认报价（提交前检查）

2026-10-21，接续 `e253440b8296c51f0413dcfa0b2a0d52eb4d7775`。用户明确改为 **类型 → 地区 → 节点 → 套餐**，覆盖此前不要节点的选择；新增节点只作购买分类，不加入真实开通、线路或库存。单选仍有完整框及勾选；初载默认当前节点列表首个套餐，摘要/手机底价同步有效报价，不报价不跳项或换汇，仍需显式确认。源码只更新 main；用户随后明确授权新 Release，目标 Core 0.5.8，SMTP 仍为 1.1.0，不覆盖旧标签。

提交前实际检查：

- 前端全量 **914 Vitest + 27 Node** 通过，0 skip；typecheck、format:check、生产 build 通过。
- 真实 Ubuntu22 隔离 MySQL8.4.13 从11追加迁移12并重复迁移；迁移前备份校验通过；**18张既有业务表及plans原字段的哈希全部不变**（schema_migrations新增12不计入该比较），DML-only API只读preflight为current/schema12。001–011迁移保持原SHA，PlanSnapshot/newRecords/payment生产实现未替换。
- CUA已实际读取用户指定Akile桌面及375px页面，确认默认第一个套餐及四级摘要；未在Akile创建订单、付款或接受条款，不复制其源码或把本项目源码/旧UI发给第三方。
- 本轮专用localhost CA已获系统信任并严格校验；实际CUA已接入生产API/真实MySQL，验证375px单地区/单节点仍有1px选中框、默认¥39/切节点¥69/切类型¥159同步，后台通过真实表单新增节点、改名及商品归属保存，原报价69.00保持；不是demo拦截数据。系统认证此前阻塞的历史记录保留，但本轮已解除。
- Ubuntu22 真实 MySQL 全套 Go race **1317 PASS、0 fail、0 skip、无 race warning**，vet 通过；该套后端源码与最终058仅 CoreVersion 字面量不同，不能据此省略最终提交的CI。新012迁移及所有新增节点路径均实际执行。
- 用户明确批准后，CUA创建且只创建一笔隔离合成待付款订单；刷新仍能读取订单与账单，SQL交叉确认6900 CNY、1条幂等记录/创建审计、0支付尝试、0服务，所有独立报价行与迁移前哈希相同。最终适配器真实HTTPS只读契约 **42项通过，0业务写入**。
- 用户截图的首页失败源于测试SSH网络超时，代理返回HTML502，不是财务数据被改。恢复转发后首页正常。新增非JSON5xx安全提示并保留POST不确定/不重试语义；实测短暂停转发→准确不可用提示→恢复后读取真实待付69元，200坏JSON仍严格拒绝。一次API检查与受控断流重叠的502失败日志保留，恢复后从头完成42项；没有重放业务写入。
- 最终前端构建实际检查1440px、375px及此前320px边界：默认报价同步、单项选中框、无横向溢出；中英文切换不改CNY，自定义下拉键盘操作、Escape焦点恢复、浏览器返回关闭确认抽屉。截图为真实MySQL，不以CI合成fixture截图冒充。不是物理手机全矩阵或完整WCAG认证。
- 058版本元数据11项、独立发布管道66项通过；插件管理器本机29通过/19环境错误（Darwin缺Linux renameat2），不降低原子不覆盖保护；Linux完整套件须由最终CI补验。

本节为提交前快照；最终main CI、标签Runtime build、独立发布器与公共六资产核验须分别绑定精确最终提交，不借用上一轮绿灯或宣称未测物理手机/全Linux发行版。测试证书、凭据和自有服务在本轮实测结束后必须精确清理，后续实际结果及清理回执独立留证。动态Release正文给出精确构建run/attempt，API与升级限制见 [CATALOG_TAXONOMY.md](CATALOG_TAXONOMY.md)，ignored本地证据在 `artifacts/mysql-browser-20261021/`。

---

## main 商品三级分类本轮验证（未发行）

2026-10-21，基线 `c4db745fcc28f00c896ef8c626e8d8ccf03f867c`。本轮实现类型 → 地区 → 套餐及后台注册表；只更新 main，不创建/覆盖标签或 Release，已发行 v0.5.7 的产物与正文不改。

截至本次源码提交前已实际完成：

| 检查 | 结果与边界 |
| --- | --- |
| 前端完整单测 | 894 条 Vitest + 23 条 Node 数据层通过，0 skip；typecheck、format:check、生产 build 通过。新增客户22/后台35及数据层4项（含进行中保存不能被旧放弃提示关闭），既有断言保留 |
| npm 依赖扫描 | 当前 lockfile 全依赖 `npm audit` 返回 0 漏洞；不据此宣称 Go 或系统依赖全部无风险 |
| Ubuntu22 实库 | 独立非 root MySQL8.4.13、生产 API、DML-only 运行账号，首次/重复迁移及只读 schema11 preflight 通过；实际32项 HTTPS API检查通过，含权限、跨父级拒绝、上架4个合成测试商品、分类改名、在售分类停用409、旧报价409、原合法幂等200、历史订单详情完整JSON不变。没有真实销售、付款或资源开通 |
| 源码边界 | 本机与远端211项后端/部署输入 SHA 一致；001–010十份迁移逐字与基线一致；五份生产 payment/checkout Go文件未改；PlanSnapshot模型未改 |
| 过程失败 | 首次schema11 preflight发现MySQL输出CHECK操作数括号不匹配，修复为仅接受011两条精确表达式，并增加正常/篡改回归；首次一次性HTTP脚本期待错误码INVALID_CATALOG，而实际正确返回400 INVALID_TAXONOMY，修正脚本而非放宽拒绝。保留初始日志 |

提交时完整Go race/vet仍运行；手机/桌面真实浏览器验证等待用户完成本轮临时证书认证，完成后必须移除信任。当前不把上面局部记录称为正式完整验收。后续完整main CI、实际截图与清理回执单独绑定最终提交与实际执行结果，不沿用旧Release的绿色状态。本轮源码入口及升级回滚约束见 [CATALOG_TAXONOMY.md](CATALOG_TAXONOMY.md)。本地证据在 ignored `artifacts/taxonomy-implementation-20261021/`；不提交凭据、CA私钥、数据库或原始用户环境信息。

---

## Core v0.5.7 本轮本地检查与发行门禁

2026-10-21：目标 Core `0.5.7`，前一正式版本为 `0.5.6`；官方 SMTP 继续独立 `1.1.0`，兼容 `>=0.4.0,<0.6.0`。正式前端已接入新控制台。以下为本轮实际本地范围，不借历史正式门禁宣称新版本已经发行。

| 实际本地检查 | 结果与边界 |
| --- | --- |
| 正式前端回归 | 837 条 Vitest、19 条 Node 数据层测试通过；typecheck、format:check、生产 build 通过 |
| 独立依赖/产物核对 | 独立 `npm ci` 后构建产物 SHA-256 与所核对构建一致；`npm audit` 全依赖报告 0 项漏洞 |
| 实际浏览器抽查 | root 通过 CUA 实际打开正式 dist 的浅色合成只读 fixture，完成桌面商品 89、币种 142、手机 catalog 67，共 298 项文字测量、0 项失败；另有 3 项 disabled/group-opacity 明确跳过，不计为通过。保存 3 张实际截图，已目视桌面/手机，浅色偏好重载后保留；不是 MySQL-backed E2E、完整浏览器矩阵或 Linux 严格 supervisor 验收 |
| 版本与发布正文 | Core 与插件管理器默认均为 0.5.7，SMTP 1.1.0 及原兼容范围不变；057 发布正文使用本次源码提交与实际 Actions run 链接，056 历史正文保留 |

首次独立分支 CI [39585438889](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39585438889) 对 `17f25ef0532218103d45c16e5db577abc4562b2f` 完成真实 MySQL 全回归、原生 systemd/Nginx HTTPS、备份恢复/回滚以及 Go/npm 安全门禁；完整浏览器仍失败（public/auth 阶段等待站点响应超时），所以该运行整体不是通过。已修正 hash 同文档导航与全新 shell 等待的测试契约，并保留有界诊断，不加重试或放宽超时。另经实际 320px 浏览器发现并修复极长独立报价字段挤压（输入宽度 5→214px、原金额及 12px 字号不变），附加 5 条回归；最终完整 CI 仍须绑定修复后源码重新执行。

抽屉 history 标记现按每次打开生成独立 token，避免整页刷新后重用 `useId` 标记。新增 5 项生命周期回归；刷新后重开抽屉，root 用实际 CUA DOM 确认首次关闭后 dialog 数量从 1 变为 0。非文字复选框的内部 `on` 值不当作可见文字测量；长报价浅色弹窗 45 项实际文字测量为 0 失败。

安全扫描的含义须区分：首轮 npm 全依赖 0 项漏洞；Go 为调用与已导入包风险 0，另有 1 条未进入应用依赖调用路径的 module advisory `GO-2026-5932`（`x/crypto/openpgp`），不把门禁成功写成所有依赖绝无风险。

### 控制台实现的完整主干验收

UI 实现提交 `845ddc7b20d1c9b6de7fd34890c440cffa858723` 已在 [main 完整 CI 39590301919](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39590301919) 通过全部三个 job：完整真实 MySQL 回归、原生 systemd/Nginx HTTPS 与安装/升级/备份恢复/回滚、沙箱浏览器、Go/npm 安全扫描、workflow 检查，以及主干专用的正式组件元数据读取。修复前失败运行仍保留，不用本次成功改写历史失败。前一独立分支提交 `0eeb4d7` 的 [CI 39589193071](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39589193071) 也完整通过其适用门禁；其 main-only job 按设计跳过，不能混为全套 main 结论。

同一实现提交的 [隔离 HTTP 性能基线 39590302094](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39590302094) 已完成并成功；它是固定合成 MySQL 数据与回环只读 HTTP 配置，不是生产容量或所有发行版验收，也不据此声称本次 UI 改动提升后端吞吐。

最终文字测量已排除原生 checkbox 的内部 `on` 值，当前为 89 + 142 + 67 = 298 项计算样式文字测量（非完整 WCAG 认证），不是最初包含非文字值的 300 项计数。此后的发行文档校正不修改运行源码、依赖或财务规则；新标签的 Runtime build 仍须对精确最终提交重新执行完整门禁、双构建及独立发布。正常 Release 与六资产公共下载校验不能由上述 CI 预先代替。

正式 CI、原生后端/MySQL、沙箱浏览器、原生安装升级、精确提交双构建、正常 Release 与公共六资产核验结果，以本轮最终提交对应的 Actions run/attempt、来源文件和实际下载记录为准；上述本地检查不能代替这些门禁，也不构成全 Linux 或生产就绪声明。详见 [v0.5.7 发布说明](RELEASE-v0.5.7.md)。下方原有记录及用户未提交内容原样保留，不更改历史标签或正式资产。

# 2026-10-20：手机页头偏好优化（未发行的视觉候选）

上一阶段 Core [v0.5.6](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.6) 已正常发布，源码为 `7abfc4612825aaa8f2c994326376cbedca1bb80f`；正式构建 `39438043611`、独立发布器 `39441105399` 成功，六资产匿名下载核验有独立记录。下面保留原提交前快照，不将它的“待发布”表述或历史结果冒充当前状态。

用户随后圈出375px审计页顶部语言/币种两个大选择框。本轮仅修共用页头：≤1024px把两项偏好合成一个显示当前语言及币种代码的入口，打开完整标签的原生对话框；桌面保持直接选择。手机只保留当前页名，721–1024px有足够空间时保留完整面包屑。320px先移除装饰图标、窄桌面仅在视觉上隐藏冗余标签，不截完整页名、不缩字号。实测紧凑页头64px，原375px为117px，正文上移53px；1025px长标题页及1440px页头76px。

| 实际执行 | 最终结果与边界 |
| --- | --- |
| 前端完整检查 | 固定 Node24.19.0：20文件/559 PASS；typecheck、format、生产build均退出0。无依赖或锁文件改动 |
| 原维护的浏览器边界 | 实际 Mac Chrome155.0.8059.85、沙箱开启、生产build回环preview：responsive50/checkout28/overlay15/admin6/audit8/management11全过；直接复用原断言，不改变50矩阵或原30张正式截图合同 |
| 新视觉/交互 | 17场景覆盖320/375/720/721/768/1024/1025/1440、中英、三主题、长页名/币种名、短屏、Tab/Shift+Tab、Escape/完成/遮罩关闭、返回、抽屉互斥与断点焦点；全部通过。实际打开最终生产截图复核手机审计、偏好面板、320长标题/短屏滚底、平板面包屑、窄桌面长标题等子集，不宣称看过所有图或用户已认可审美 |
| 偏好与历史金额 | 2个独立合成浏览器流程经真实入口及重载：客户USD/语言偏好持久化，停用JPY不可选；管理员历史JPY仅作报表且不覆盖客户CNY存储，重载按原规则恢复。原CNY39.00/KWD9007199254740.991历史金额不变；不代表服务端授权、真实结算或多客户MySQL浏览器 |
| 浏览器helper竞态 | 连续1024→1025→721→1440→320→1440，改视口后立即通过真实入口选择，6/6；等待当前断点与Vue渲染对齐，不添加点击/选择重试、force或DOM改值 |
| 源码与构建 | 65个前端规范文件及4个生产构建文件在最终浏览器前后SHA一致。App原35个顶层函数（34声明及1 arrow）与7ab逐字相同；144个后端规范输入保持不变。只增偏好组件及对应模板/布局/测试，没有改金额、价格、幂等、付款或权限处理器 |

过程失败原样保留：首个原维护探针在dev服务因Vite HMR WebSocket被严格网络门禁拒绝，改用生产preview而非放宽门禁；原50最初两条要求平板完整面包屑失败，恢复721–1024px的上下文后真绿；跨断点旧helper读取了尚未卸载的触发器而超时，修为等待断点与渲染对齐。实际看图还修了721px两层页头、320px长标题挤成三行、1025px长标题把选择器推到第二行的问题，不以提高高度门槛掩盖。

本轮所有浏览器接口均为本地拦截的明确合成数据，0外联、0业务写入、0pageerror；不是完整live API E2E、Linux严格supervisor或新版本正式验收。没有重跑Go/MySQL、原生主机生命周期或依赖网络扫描，也没有借旧v0.5.6的正式门禁宣称新UI已发行。当前未推送、未改标签、未发布新版本；先交实际前后图供用户确认，不覆盖已发v0.5.6。

证据入口：`artifacts/handoff-20261020/toolbar-polish-20261020/`，包括 `before/`、`final-production/`、`preference-flows-final/`、`helper-regression/`、`existing-probes-production-final/`、`FINAL-BROWSER-VERIFICATION.json`、最终四项前端检查日志及源/构建清单。初始含生成的TypeScript buildinfo的66项清单另存；最终65项只排除该生成缓存，不改真实输入。临时preview、dev与自有浏览器进程结束后精确清理，不影响用户窗口。

---

# Core 0.5.6 管理补全与发布验收（原提交前快照）

2026-10-20，用户要求补齐客户/角色、单笔订单详情、商品新增上架，并授权正常推送与发布。目标采用未占用的 **v0.5.6**；旧失败标签 v0.5.5 不改。起点 main `a203f478df950b3884da65785cbbeeb60f8ca3c6`，正式对照为 v0.5.4 `6a21841d7458c95b29fdae1830f99d59c9652ddc`，没有带入缺失的旧提交 `86e57114...`。

本轮增加账户状态及版本迁移010，保留009审计索引及此前77文件成果。下述历史阶段测试数量不覆盖本轮新源码；精确源码、重新测试、截图及 GitHub 正式构建、正常 Release 与公共附件校验须独立记录。“准备0.5.6”不是已经发布。

## 管理补全最终本地记录（2026-10-20）

已实现真实客户分页、创建、角色/启停、密码重置、撤会话；直接按 ID 的客户/管理员订单详情；真实商品新增、复制草稿、编辑规格、独立报价、上下架及对应中英文响应式页面。管理员不受本人资源过滤，但不提供任意改已付状态、代付款或伪造资源开通。最后一个管理员、自降权、在途旧密码登录、会话版本、CSRF、事务审计及历史快照均受测试约束，详情见 [ADMIN_MANAGEMENT.md](ADMIN_MANAGEMENT.md)。

| 项目 | 本轮最终结果与边界 |
| --- | --- |
| 完整后端 | Ubuntu 22.04.5 amd64、Go 1.27.3、官方包核验的私有 MySQL 8.4.13：`go test -mod=readonly -p=1 -race -count=1 -v -timeout=20m ./...` 及完整 vet 退出 0。286 顶层、含子例 1132 PASS，名称含 MySQL 的顶层 75，0 FAIL / SKIP / race warning；billing 749.243 秒。额外 1GB 主机夹具的 20 分钟上限预先设定，不改变正式 CI 默认时限、断言或协议。144 个规范后端输入在当前本机、远端及测试时 SHA-256 逐项一致 |
| 正式 v0.5.4 数据库补项 | 生产验证器核验官方正常 Release、成功构建及六资产后，实际官方 0.5.4 API 创建 schema8 并导入合成历史，当前源码构建的 0.5.6 只读检查 pending[9,10] 后实际迁至10。旧六用户字段及其余业务全量 hash、行数、快照保持；新字段 disabled=0/revision=1；重复迁移不变。旧官方 API 对新 schema 明确拒绝且不写数据。独立测试库已确认删除，外层私有 MySQL 已停止；不是 0.5.4 的完整主机安装/回滚验收 |
| 金额精度竞态先红后绿 | 实际 HTTP 证明旧候选在未定价币种精度变更后错误接受旧金额（201）；修正后相同陈旧输入完整返回 409 CATALOG_CONFLICT。目录写入同时绑定价格 revision 与实际解析币种的 currency_revision，含混合快照、创建/编辑/复制、零副作用及新精度精确金额的 Memory/MySQL 回归。未改旧币种配置协议、汇率或历史金额 |
| 前端完整检查 | 固定 Node 24.19.0：19 文件 / 543 PASS，typecheck、format、build 均 0。真实网络 npm audit 含 dev 依赖总漏洞 0，锁文件不变。60 个前端源码与 4 个生产构建文件在最终浏览器前后 SHA 不变 |
| 最终完整浏览器 | Mac 沙箱 Chrome → Ubuntu22 回环新 API demo：原 50 responsive / 28 checkout / 15 overlay / 6 admin / 8 audit 与新增 11 合成管理边界全过；原正式 PNG 恰30张。唯一最终完整执行退出0，单调202.522秒，小于原240秒门禁。不是 Linux 严格 supervisor；demo 不是多客户 MySQL 浏览器 |
| 实际管理业务浏览器 | 真实 demo 完成账户5次成功写入（创建、升管理员、重置密码、撤会话、停用）；商品4次（创建草稿、三币独立精确报价上架、复制、下架）；客户选购创建 CNY1234 minor 待付款订单。草稿、复制品及下架商品不在公共目录；已有成交快照保持。另留4张实际管理图，不替换原30图合同 |
| 视觉与交互 | 实际查看新客户/商品桌面、375px 客户订单、手机账户弹窗首尾、商品报价编辑及320px英文极长金额等子集；修复价格窄列、分页占错列、保存后未刷新、迟到响应污染、旧草稿借新版号及 Tab 离开弹窗，保留先红后绿证据。短屏冲突提示可能需向上滚读完整错误，关闭及取消仍可达；不宣称所有状态同屏、全图人工验收或用户已认可审美 |
| Ubuntu22 工具与边缘 | native-tool 42、backup Python 18、core-operation 90、release-pipeline 64 均通过；backup shell mock 通过，不冒充真实备份恢复。实际官方 Nginx 1.30.6 两 profile 共14检查、23条 syslog 通过，含新管理路由固定标签与隐私；私有回环自签 CA，不是公共 TLS 或系统 Nginx 安装 |

中间失败保留：首轮完整 Go 为旧 v1 支付历史测试调用新增账户列的 fixture 错误，仅把该 fixture 改回真实旧五列 INSERT；不让生产 AddUser 兼容未知旧 schema。第二轮在发现精度竞态后精确中止自有进程（143，清理成功），不算通过；最终第三轮完整重跑才是上表结果。浏览器、网络隔离与元数据缓存的历史失败亦保留，不以重试掩盖产品问题。

证据入口（本地非发行附件）：`artifacts/handoff-20261020/admin-completion/full-go-3/results/`（含官方旧版实际升级）、`admin-completion/official-054/`、`admin-completion/precision-race-{red,green}.json`、`admin-completion/tools-linux/` 及 `management-workflows/frontend/`。源码、构建、截图分别冻结；原 App 34 个既有顶层函数与本阶段实际 before 字节一致，不声称整个 App script 或新管理逻辑未改。

**本记录为提交前的本地验收快照，不是发布成功声明。** GitHub 对最终提交的完整 MySQL/打包工具、原生 systemd/Nginx/TLS、真实备份恢复/回滚、Linux 严格浏览器、最新 Go/npm 安全门禁仍须实际成功；之后新版本双构建、独立发布器、正常 Release 和公共六资产下载校验才完成发行。正式结果应以对应提交的 Actions 和 Release 为准，不以标签推送代替。公共 CA 续期、主机重启持久化、其他发行版/架构、真实渠道及生产容量未因此获验收；没有宣称所有 Linux 或全部生产场景就绪。Ubuntu22 已运行真实新程序与全量 MySQL 回归，不限定只支持 Ubuntu24。

---

# 历史阶段验证记录

## 2026-10-20：生产日志与审计候选（本轮）

延续上述未提交分支，不覆盖已有客户首页、套餐比较、覆盖层或管理员服务修改，没有推送、改标签或发布。此次新增生产 Go 代码及 migration 009，不能再沿用早期“只改测试/UI、生产协议文件无修改”的宽泛表述；支付 Unix RPC 生产协议仍保持不变。

已补：API JSON 完成日志、服务端请求 ID、固定路由/方法及类型化错误脱敏、panic 完整响应/已提交中断边界、SMTP 最终审计写入失败诊断；SIGTERM 等待排空后再释放数据库；Nginx 白名单 JSON syslog 与专用实例原始 engine 日志取舍；备份失败安全 NDJSON；真实审计游标分页/精确筛选/权限隔离/三类严格恢复索引。旧 `/audit` 兼容语义及其他清单上限不改，未增删除审计、主机日志 Web 读取或任意财务改状态接口。运维方法与保留限制见 [LOGGING.md](LOGGING.md)。

审计 UI 实际以卡片替代手机横向宽表，默认折叠筛选，UTC 时间、完整事件 ID、所属客户与操作者分别表达，原生 details 展开长详情；真实失败显示重试和响应请求 ID。未知历史 action 的 Object.prototype 命中及客户标题错误均由实际检查发现，分别补回归并最小修复，不删历史数据。root 实际打开前后桌面、375px 中英/错误、客户本人结果图；长详情/键盘/320px 图由 UI 子任务实际复核，单元测试不充当视觉完成。

| 本轮项目 | 实际结果与边界 |
| --- | --- |
| 旧实现日志先红 | 在上一阶段 120 个规范后端输入上，实际 Linux `-race` 两项失败：没有请求完成及失败就绪结构化事件；非编译失败 |
| 中间候选全后端 | Ubuntu 22.04.5 / Go 1.27.2 / MySQL 8.4.13：255 顶层、含子例 1028 PASS，52 个 MySQL 顶层，0 FAIL / SKIP / race warning，vet 0；绑定 iteration2 的 129 源输入，不包括后续 UTC 边界和依赖升级，不是最终工具链验收 |
| 最终候选全后端 | Ubuntu 22.04.5 / Go 1.27.3 / MySQL 8.4.13：256 顶层、含子例 1031 PASS，52 个 MySQL 顶层，0 FAIL / SKIP / race warning，完整 `-race -count=1` 与 vet 均退出 0；本地、远端、测试输入的 130 个规范文件逐 SHA-256 相同。billing 包 596.470 秒，额外低内存兼容夹具预先使用 20 分钟 package 上限，无重试、断言或生产协议放宽，未改正式 CI 默认时限 |
| 前端完整检查 | 固定 Node 24.19.0；最终 17 文件/494 PASS，typecheck/format/build 0，含 dev 的真实 npm audit 0 漏洞；新增 audit 浏览器合成边界 8/8 |
| 本轮 fresh API 浏览器 | 新 Go 1.27.3 静态程序的 Ubuntu 22 回环 demo + Mac 沙箱 Chrome：原 50 responsive、28 checkout、15 overlay、6 admin、新 8 audit 均通过，原命名 PNG 恰 30 张；单调总耗时 147.432 秒。真实新接口验证管理员取消的操作者与 owner 不同、客户本人投影及 admin 403；不是 Linux 严格 supervisor 验收或多客户 MySQL 浏览器 |
| 真实 Nginx | Ubuntu 22 上官方 stable 1.30.6：generated/template 两 profile 共 12 检查通过，15 条实际 Unix syslog；HTML/deep link/assets/maintenance 安全头、未知 SNI 拒绝、真实 502、query/path/body/headers/伪造 ID 脱敏通过。私有自签 CA、非 root 回环实例，不是公共证书或主机安装验收 |
| 安全头先红 | 独立副本单行恢复旧 generated location 的 `add_header Cache-Control`，真实 Nginx 触发 `Static/maintenance security headers lost`；这是精确算法缺陷复现，不冒称整个旧源码快照实跑 |
| UTC 边界先红 | 原归一顺序实际接受两条 UTC 越出 MySQL 年范围的合法 offset 时间；新增测试两例均按预期失败。已先 UTC 再检查年份，最终工具链绿测单独记录 |
| 新安全工具链/依赖 | 实际 Go 1.27.3：`go mod verify`、UTC 边界绿测、CGO=0/trimpath 静态 API build、固定 govulncheck 1.9.0 均退出 0。漏洞库时间 `2026-10-20 00:20:56 UTC`：可达符号 0、导入包 0，required module 从旧 19 条降为 1 条 OpenPGP N/A，不能简称模块零漏洞 |
| 真实 journal 子集 | Ubuntu 22/systemd 249 以 UID 998 启动唯一 transient demo unit，`ProtectSystem=strict`/`PrivateTmp`/`LimitCORE=0` 实际生效；API 新 ID 与 journal 完成事件匹配，伪造 caller ID、合成 query/header/Cookie canary 未入 journal，成功 health 探测无刷屏。380 条完成事件及启停 JSON 保留；浏览器结束后精确停止自有 unit，真实 `server.draining`→`server.stopped` 且 PID 已退出，无 drain_failed。仅回环内存 demo，不是原生安装、MySQL 生产服务或重启持久化验收 |
| 部署日志工具 | Ubuntu 22：core-operation 90/90、backup unit 16/16、mock backup 10 组通过，含 8 并发、schema9/prefix6–8、安全失败 42–44、三个不同固定校验 reason 先红后绿；这些 mock 不是实际备份/恢复 |
| 工作流静态检查 | 本机 Mac/Go 1.26.4 独立缓存实跑项目固定 actionlint 1.7.7，四份最终 workflow 退出 0；无外部 shellcheck，非正式 GitHub runner 或新 Go 生产程序验收 |

Nginx 首轮真实发现 syslog `tag=aster-edge` 语法无效（仅字母数字下划线），改为 `aster_edge` 并补静态负向回归。第二次测试归档的 mtime=0 与同字节长度替换导致旧 Python timestamp pyc 被复用；源码/缓存差异通过实际 marshal 证实，原失败保留，后续使用全新独立缓存前缀，不篡改产品导入机制。第三轮 generated 已通过，template 因非 root 裸解包没有 `/var/cache/nginx` 失败；仅补夹具私有 temp_path，生产模板与安全断言不放宽，最终两套通过。`-e` 从 Nginx 1.19.5 起使用，1.19.4 的 SNI 最低支持边界不降低。

Nginx 包从官方 HTTPS 获取三指纹 key bundle，独立比对官方文档；当前 InRelease 签名由 `8540A6F18833A80E9C1653A42FD21310B49F6B46` 验证，再校验 Release→Packages→DEB 的大小/SHA-256。旧单 key 首次验证失败原样保留；没有加入主机 apt 源、安装维护脚本或改变 Nginx 服务。MySQL 8.4.13 仍为此前明确的官方 HTTPS 元数据/四包 SHA 验证，不混称成 Nginx 的签名链验收。

安全更新：官方 Go 1.27.3（2026-10-19）包含 x509 安全修复，已独立核对官方 Linux amd64 归档大小与 SHA-256、仅提取到 CASE；当前 workflow/pin/性能守卫同步 1.27.3，不改 Actions SHA 或历史工具链事实。现有 `x/crypto` 从 0.43.0 最小升级至 0.56.0，覆盖此前 18 条有修复版本的模块告警；其上游要求 Go 1.26，故模块最低声明同步 1.26.0，无新直接依赖。OpenPGP 不维护告警没有可用修复，本程序不导入该包；最终漏洞库扫描范围见上表，不能简称 Go 零漏洞。静态原生程序不依赖目标主机 Go，不因此排除 Ubuntu 22。

另以已校验的旧/新模块缓存逐字节比对，bcrypt/blowfish 五个生产 Go 文件完全相同；升级没有改密码算法或应用 cost。正式 v0.5.4 提交 `6a21841d...` 的 001–008 迁移与当前逐 SHA-256 完全一致，009 真实 MySQL 8→9 升级/索引中断恢复与历史数据保持由新增测试覆盖；不把源码等价及数据库夹具冒充官方 v0.5.4 二进制的完整主机升级/回滚。

完整工具 discover 以短私有 TMPDIR、全新可写 PYTHONPYCACHEPREFIX 及 77 个精确公开输入执行 308 个顶层测试，退出 1：记录 285 个顶层通过、22 个 failure、3 个 error（payment 子例与父断言重叠，不能直接相减），0 SKIP。21 条为正式 DEB 安全提取器要求 Python >=3.12、测试主机为 3.10 的明确拒绝；其余为避免与 1GB 全量 race 竞争而未提供 Go 的三次构建及 payment 父断言连带失败。资源释放后以全新 141 项精确公开输入（130 个规范后端文件、最新 README 及直接 helper，0 个 AppleDouble）实际补跑原两个顶层测试：2.497 秒、退出 0、0 FAIL/ERROR/SKIP，覆盖 payment 0.1.0/0.1.1 及 SMTP 1.1.1 三次真实 Go 1.27.3 构建，保留 payment 父 hash 断言；取得三份 ELF64/x86-64、resources 与 manifest，输入前后相同。不把这次补项改写成原完整 discover 已全绿。首次精确补项 launch 因漏一个模块顶层导入的公开 SMTP helper，在 unittest 和 Go build 前停止，0 测试/0 构建；原证据保留，新输入先通过 import 预检再第一次实际执行测试，不是给失败测试添加重试。此前遗漏公开依赖、禁止私有缓存写入的性能损失、过长 Unix socket TMPDIR、测试 tool-role 排除了 `sha256sum` 数字及旧 staging 提取锚点的问题均保留原日志，修正夹具/测试后对应路径通过，不放宽生产守卫。

**完整生产验收仍未完成**：整套原生安装/实际生产 unit、公共 TLS/续期、升级/真实备份恢复/回滚、Linux 严格浏览器、Python >=3.12 正式打包工具门禁、系统依赖审计、正式 GitHub 双构建/Release/下载资产验证仍待完成。不能以此阶段候选称为生产就绪；没有自动继续发布或监测。

证据入口为 `artifacts/handoff-20261020/production-logging/`：`backend/iteration2/independent-summary.json`、`backend/iteration3/`、`frontend/`、`deploy/linux22-evidence/edge-observability-final.json`、`root-review/`。合成数据、失败与中间源码分别保留；报告计数包含子测试，不能当作相同数量的互不重复测试。

## 2026-10-20：管理员全站服务与权限补缺

本阶段接在下方支付测试及覆盖层视觉冻结之后，仍基于 `a203f478` 的未提交工作树，没有推送、改标签或发布。按用户要求核对全站管理能力后，确认既有管理员订单、账单及审计读取不按本人筛选，但服务没有管理员接口或导航。本次只补这个真实缺口：`GET /admin/services` 复用现有全站 Store 查询，以管理员专用 DTO 增加 `customer_id`；客户 `/services` 继续按本人过滤，不改变通用 `Service.UserID` 的隐藏语义。不改 Store、数据库迁移、财务记录或支付/取消状态机。

前端新增“服务管理”、独立管理员服务缓存、准确客户 ID 与可匹配的名称/邮箱；客户未载入时保留原 ID，不猜归属。明确 1000 条既有上限和已载入搜索范围，首次失败不显示确定空态，刷新失败保留旧记录并标注可能过期。同 ID 的角色切换也同步清除管理员缓存、递增请求 generation，迟到管理员响应不能恢复旧数据。管理员取消失败的文案改为核对支付处理，不再让管理员“联系运营方”；客户文案及取消结果验证保持原样。

| 项目 | 本阶段实际结果 | 范围与证据 |
| --- | --- | --- |
| 真实 MySQL 后端 | Ubuntu 22.04.5 amd64 / Go 1.27.2 / MySQL 8.4.13；`-race ./...`、vet 均退出 0；231 顶层、含子测试 943 PASS，0 FAIL / SKIP / race warning | 含 46 个 `TestMySQL` 顶层；不是 943 个互不重复的测试 |
| 新服务权限 | 3 个新增测试均通过；其中真实 MySQL + HTTP 验证两客户、管理员、历史格式和极值金额 | 匿名 401、客户 403；拒绝前不调用服务读取；客户路径无 owner 泄露；同一管理员 cookie 在测试库持久降权后下一请求 403，不等于已实现会话撤销管理 |
| 前端完整检查 | 固定 Node 24.19.0；typecheck、format、生产 build 退出 0；16 文件 465 PASS，0 SKIP | 含新增角色切换/迟到读、归属、失败态及管理员取消文案回归 |
| 完整浏览器 | 新鲜 Ubuntu 22 回环内存 demo + Mac 沙箱 Chrome 155；既有 50 responsive、28 checkout、15 overlay 及新增 6 admin case 通过，正式截图恰 30 张 | 单调总计时 136.11 秒；小于既有 240 秒，但不是 Linux 严格 supervisor 验收 |
| Linux 工具合同 | Ubuntu 22 Python 3.10 下 `test_browser_demo.py` 43/43、退出 0 | fake Go/Node/browser 的 unit/lifecycle 测试，不启动真实浏览器或 MySQL；生产 `browser-demo.py` 未改 |
| 当前前端依赖审计 | 含 dev 的 `npm audit --audit-level=high --json` 退出 0、报告总漏洞 0 | 没有新依赖或锁文件修改；不替代 Go、OS 或整机审计 |

本轮 MySQL 官方 HTTPS 元数据实际返回 **8.4.13**，不是沿用上一阶段的 8.4.11。四个 Ubuntu 22 官方 DEB 独立核对大小/SHA-256 后提取到新的 CASE 工具目录；不是本地 InRelease 签名验证，也没有安装/修改主机 MySQL 服务。专用 UID 998、私有 Unix socket、TCP 关闭；测试库与失败/清理日志保留。120 个规范 Go/SQL/mod/sum 输入在本地、远端及测试时采集清单中逐项相同；旧归档的 AppleDouble 元数据排除于规范比较但保留，不改生产迁移守卫。新夹具只把原脚本工具目录改到新路径，拒绝覆盖旧工具。

浏览器跨客户填充场景使用实际 Vue 构建及严格本地合成接口；所有写请求被拦截，仅一个明确的合成取消 POST 本地返回不确定响应，未落到真实 demo/渠道。确定性迟到响应测试程序派发原刷新事件，不冒称是模态框后的物理点击。真实新 demo 的服务清单为空；两客户的真实后端归属证明来自上表 MySQL+HTTP 测试，不能混称浏览器使用了多客户 MySQL 数据。

实际看图发现并修了两处：未知客户少一行邮箱使桌面同行金额不齐，使用仅管理员卡片的三条局部规则对齐；初轮几何断言虽 6/6，64 字符 ID 仍把“客户归属”标签挤成竖排，固定标签列后新增单行断言先红后绿。修后实际检查桌面、375px 中英文/抽屉及 320×480 长 ID、长邮箱、极值历史金额。没有隐藏 ID、裁切金额或伪造邮箱。原全局 `style.css` 保持上一视觉阶段的字节；当前 `App.vue` script 为新增能力刻意修改，**不能再沿用上一阶段“九个 script 均与 main 相同、后端生产文件无修改”的结论**；其余八个 Vue script 仍相同。

证据入口：本机 `artifacts/handoff-20261020/admin-management/` 的 `backend/independent-summary.json`、`root-review/`、前端检查日志、`e2e-final.log`、`e2e-timing.json` 及前后截图。独立只读审查未发现本轮权限/隐私阻塞项；源码、构建和截图清单分别核验，不把测试绿灯等同于视觉完成。正式 `admin-desktop.png` 本轮已等待后台加载；`invoice-snapshot-after-settings.png` 仍处详情刷新，只作为功能/历史快照证据，不作加载完成的视觉验收。

本阶段不是“管理员所有功能已齐”。客户创建/停用/角色调整/密码重置/会话撤销、单笔订单详情、商品创建/复制/规格编辑和完整分页仍未实现；见 [管理员管理边界](ADMIN_MANAGEMENT.md)。没有任意改状态、人工标已付、退款或真实资源控制。最终提交完整门禁、原生安装/systemd/Nginx/TLS、升级/备份恢复/回滚、Linux 严格浏览器及正式双构建/Release/可下载资产校验仍待完成。上一阶段 Go 扫描的 19 个模块级告警不因本轮 npm 为零而消失，未宣称依赖全无漏洞。

## 2026-10-20：新环境接手、支付连接夹具与 Ubuntu 22 阶段验证

工作区最初为空、无提交或用户本地修改。只读核对远端后检出 `codex/payment-connection-test`，基于 main `a203f478df950b3884da65785cbbeeb60f8ca3c6`；`v0.5.4` 剥离标签确为 `6a21841d7458c95b29fdae1830f99d59c9652ddc`。保留主干客户首页、套餐比较、导航、手机底栏及账单刷新付款修正，未带入或声称包含缺失的未推送提交 `86e57114dd97139e7a3ac4aff368e4a1bee7e0e9`。本轮是未提交工作树的阶段验证，没有推送、改标签、发布或制造发行来源。

`v0.5.5` 标签仍指向 `a203f478`；只读获取的 [正式构建 39280920528](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/39280920528) 在真实 MySQL 回归阶段因 `payment_linux_test.go:469: runtime plugin unavailable` 失败，发布 job 跳过，未查到该版本正常 Release。不能将该标签或历史 CI 成功作本轮验收。Core 声明、插件管理器默认版本和安装示例仍保持已有 `0.5.5` 候选口径，不覆盖旧标签；最终发行版本与完整门禁须在后续确认。

### 支付连接测试：生产协议未修改

仅修改 `TestPaymentServerRequiresOneOperationOnHandshakenConnection`：两个客户端分别预拨并固定一条 Unix 连接，one-shot `DialContext` 和拨号计数拒绝静默重拨；POST 无 `GetBody` 或幂等 HTTP header。测试独立完整读取有上限的 HTTP 响应，拒绝须为完整 409 和 `handshake required\n`，网络错误、截断或超长响应均失败。覆盖 checkout→checkout、checkout→verify、verify→checkout、verify→verify；每例依次检查未握手拒绝、握手不能转移到第二条连接、首次操作成功及重放拒绝，create/verify 分别计数且仅对应处理器调用一次。未修改生产 `exchange`、握手、服务端状态或超时。

新主机为 Ubuntu 22.04.5 / amd64、内核 5.15、systemd 249、Python 3.10.12。Go `1.27.2` 从官方元数据核对大小与 SHA-256 后仅解压在独立测试目录；以无登录非特权用户执行。聚焦 `-race` 连续 100 次，400 个操作组合全部通过（含父测试为 500 PASS），0 FAIL / 0 SKIP；完整 runtimeplugins 包为 87 PASS，pluginserver/pluginapi 相关包为 16 PASS，均无失败或跳过，相关 `go vet` 通过。计数包含顶层和子测试，不当作 500 个不同用例。

另外以临时 Go overlay 注入五类错误：放行重放、跨连接共用 session、不返回 HTTP 就关闭连接、截断 409、完整 409 后强制关连接。新测试五类均实际失败并被探针检出；编译错误或超时不计作检出。生产文件字节不变。原测试在新机器的 Go 1.26.4 交叉编译程序及 Go 1.27.2 重编译程序重复运行均通过，**本机未重现历史 469 失败**；历史构建日志与本次结果分别保存，不冒称重新复现。

首次整包误以 root 身份执行时，既有插件选择拒绝 UID 0，造成 23 PASS / 64 FAIL；该失败日志保留。改用专用非 root 用户后全部通过，没有放宽 UID 或路径保护。独立 MySQL 夹具首次启动另因 `runuser` 继承不可进入的 `/root` 工作目录而停止于 Go 版本检查之前；显式进入 CASE 后再启动，保留该夹具错误，不算数据库或产品测试失败。

### UI 功能修正与浅色视觉候选

实际读取本地 Ponytail、UI UX Pro Max、OilOil、Apple Design 的 `SKILL.md` 及相关参考，确认本地搜索脚本和参考目录可用；不声称与外部仓库最新版一致。保留既有设计语言，无新统计卡、外部字体、炫光或营销内容。改动限于：首页首次加载失败不显示确定空态、不落入 404，已有数据刷新失败继续可读并明确提示可能过期；费用明细超过四项时显示中英文范围提示及已有服务导航；320px 下财务输入只缩小横向 padding，仍保留 17ch 文本容量和原控件高度，避免 CNY 被挤到下一行。未修改金额解析、汇总、币种、付款幂等或历史快照。

本机固定 Node `24.19.0` 从官方 SHASUMS 校验；内置 npm 为 `11.17.0`，与历史环境不同。锁定安装后锁文件未变。新增六条组件回归在旧代码上 6 FAIL；首次修正虽组件全绿，人工看图发现错误态落入 404，补中英文拒绝该兜底的断言后修正模板分支。最终前端类型、16 文件 / 451 项组件（`--maxWorkers=2`，无跳过）、生产构建与格式检查通过。项目仍无独立 lint 脚本。

功能边界修正阶段的真实后端浏览器回归使用 Ubuntu 22 回环合成 demo API 与本机 Chrome `155.0.8059.82`，沙箱开启；Mac 执行现有完整 e2e 退出 0，50 组中英文响应式边界、28 组 checkout 探针及原有业务/导航/返回断言通过，30 张截图保留。另有 GET-only 合成 10 场景 / 15 张图及 28 组 checkout，通过；实际看过首次错误、缓存错误、费用范围、手机套餐、320px 财务输入，以及真实 demo 的桌面套餐/手机首页，未把其他截图的元数据当作人工看图。原字节 e2e 曾在 320px CNY 金额同排处失败，未改断言；保留失败及 CSS 修复后的结果。上述是 Mac 浏览器开发回归，不是 Linux 严格 supervisor 门禁。

用户随后明确否定现有视觉审美，要求实际参考 Akile 并向 Apple 风格改造。除公开首页、套餐配置、轻量／独立服务器目录及登录页外，也在用户已有登录会话中只读查看仪表盘、服务列表与订单中心；手机检查使用原生 Chrome 设备工具的实际 375×812 CSS 视口，不把早期内置浏览器实际 584px 的截图冒称 375px。观察浅色控制台的信息层级、服务卡片、分组导航和空态，也记录手机订单横表隐藏金额／状态、导航后抽屉不关闭等反例；不照搬营销首页、统计卡或其布局缺陷。未操作付款、续费、重装、开关机或账户设置，不将账号、资产与凭据截图写入设计产物；检查后关闭自建参考窗口并保留用户原窗口。**前述测试与截图只验功能/布局边界，不是新视觉验收；浅色 Apple 视觉候选另行迭代并绑定新源码和截图，未获用户确认前不宣称设计完成。**

浅色候选现已冻结供用户审美确认：系统字体、浅灰画布、白色任务分组、石墨色文字及克制的主题强调色；重排客户首页、套餐比较和目录卡片。手机订单保留原表格数据／语义与取消操作，显示层重排使套餐、编号、完整历史金额及币种、状态、日期和操作不藏在横表右侧；服务卡片只改善字号层级、KV 与长值换行。手机账单摘要约从 341px 压至 217px，未删付款确认／避免重复付款提示，未缩小正文或操作。四个 Vue script 块独立逐字核对与 main 完全相同；未修改金额、币种、快照、取消或付款逻辑，没有新依赖、锁文件变更、额外统计卡或新增 `!important`。

迭代失败全部保留：主按钮扩大圆角导致既有角落点击探针失败，恢复原 9px 而未改探针；新画布使三主题控件边界对比度实际为 2.978，深色调整后重测，不四舍五入放行。超长金额虽无整页溢出，却把最近订单标题／编号挤成竖列，人工看图后将长金额独立整行并补标题／编号可读断言；目录截图又暴露手机订单横表藏字段，再作显示层重排及中英文回归。未把每次组件全绿当作视觉完成。

冻结候选前端类型、格式、生产构建及 **16 文件／453 项组件回归全部通过、0 SKIP**；本次重新运行 `npm audit --audit-level=high` 退出 0、0 漏洞。使用重新启动的 Ubuntu 22 回环合成 demo API，原字节 e2e 唯一最终运行退出 0：**50/50 响应式边界、28/28 checkout、既有完整业务／导航／焦点／返回断言与 30 张截图**；Mac Chrome 沙箱保持开启。API 首次重启因测试命令把 provider 误写为 `disabled` 被既有配置校验拒绝，修正为有效 `payment.disabled`／`email.disabled` 后启动，不改产品校验。另有 GET-only 合成 **23 场景／35 张图** 与 28 checkout 探针通过，无跨源请求或 pageerror。源码与构建产物在浏览器运行前后哈希一致；不是重新构建后复用旧结果。

实际人工查看了桌面首页／套餐、手机首页／套餐、超长金额、订单中英、服务和账单等子集，并独立复核新目录图；未冒称人工看完全部图、全部浏览器或完整 WCAG 审查。前后实页截图、源码／构建哈希和命令退出码在 `artifacts/handoff-20261020/apple-visual/`。用户最终审美确认、最终提交及 Linux 严格浏览器门禁仍未完成；本轮不推送或发布。

用户随后再次指出抽屉与弹窗简陋：上述内容区候选不能作为这两类覆盖层的设计验收。另立 `overlays-visual/` 保存真实页面前后证据，专门续办其标题、分组、账户／操作区和手机层级；此前 453 项及浏览器结果仍只绑定上一份冻结源码，不提前充作新覆盖层候选通过。

覆盖层候选现已独立完成开发回归并冻结，仍待用户审美确认：抽屉将品牌／工作区合并、导航与账户支持分组、账户与连接提示集中；订单、取消、账单、帮助及扩展详情原生 dialog 统一 token 和标题／正文／操作层级。账单去掉重复标题、双套框，唯一总额移至编号／状态后，保留原开具方、日期、项目及付款说明。**全部 9 个 Vue script 块逐字节与 main 相同**，原生 dialog、金额快照、支付准备／跳转、保存时关闭限制、导航焦点／inert／返回脚本未改；模板只包装、重排和自然化展示文字，无新依赖。

本轮实际看图又确认并修复两项显示缺陷，而非只换圆角：旧手机长金额弹窗宽 341px、scrollWidth 1145px，项目金额／总额被裁；以独立副本重建前版、同一维护断言实际先红后绿，完整字串与 Range 片段逐项核对，不靠隐藏溢出消绿。375×480 的支付准备完成态原正文仅约 40px、dialog 内容高 528px 超过 454px，底部提示被裁；操作区加有界独立滚动后，在 375×480 与 844×375 实际滚到服务商入口、完整安全说明及税务提示，关闭和正文仍可达。迭代中两次 grouped selector 误命中造成项目／金额粘连的失败图也保留，精确修正规则后重新看图。

新增维护入口 `frontend/tests/overlay-boundaries.mjs`，由现有 e2e 导入调用，15 场景覆盖覆盖层、金额全字串／Range、键盘与关闭、抽屉路由／返回及短屏。除两个明确的支付准备 POST 由隔离路由返回合成响应外，只读合成接口；不访问支付域、不真实付款、不修改 demo API。保留原 50 组边界、28 组 checkout、所有既有断言与原 30 张截图清单，不增加 shot 名称或放宽正式 supervisor 的 240 秒期限。

最终覆盖层源码实际运行：前端类型、**16 文件／457 项组件、0 SKIP**、格式和生产构建全部通过；新鲜 Ubuntu 22 回环 demo API 配合 Mac 沙箱 Chrome 的唯一最终完整 e2e 退出 0，**50/50 响应式、28/28 checkout、15/15 overlay** 全通过，原 30 张正式截图保留。源码与构建在浏览器前后 SHA 相同。日志创建至退出状态文件创建约 131.24 秒，仅为文件捕获区间、不是单调计时；不将低于 240 秒或 Mac 通过冒充 Linux 严格 supervisor 通过。npm audit 首次沙箱 DNS 拒绝日志保留，经批准联网实际重跑退出 0、0 漏洞。误在 Mac 运行 Linux-only supervisor 单测的平台失败另行保留，不能列为通过。

本轮实际人工查看覆盖层前后桌面／手机图、正常与长金额、短屏和横屏滚动末端，并独立复核指定子集；没有宣称全部截图或全部平台人工验收。`overlays-visual/final-handoff.json`、前后源码／构建／截图哈希及失败记录单独保存，未覆盖上轮内容区证据。自建预览／测试浏览器与远端合成 demo 已精确清理；未推送、改标签、发布或启动后续自动监测。

最后另将最终 e2e 与新增 overlay helper 同步到非 root Ubuntu 22 clean 工具副本，重新执行浏览器工具单测 **43/43，exit 0**，六个相关输入前后哈希一致；并只读核对原 30 个命名截图、50/28 静态矩阵与新 helper 默认不额外截图的契约。此处是新的 Linux 工具／静态契约回归，不冒充真实 Linux 浏览器执行；真实 50/28/15 结果仍来自上段 Mac Chrome 实跑。证据在 `tools/linux22/final-overlay-contract/`。

### 工具回归与另一处测试夹具竞争

本机 Python 3.9／Bash 3.2 运行现有工具测试时出现 Linux 能力、回环绑定和 Bash 关联数组限制，原始失败与受批准的回环重跑分别保留，没有把 Mac 平台错误当作产品通过。随后在 Ubuntu 22 的 Python 3.10.12／Bash 5.1、非 root 用户执行 18 个现有离线工具／发布器测试入口。初始上传树带 macOS AppleDouble `._*`，使两个迁移清单断言误计 SQL；真实 Git 文件与本地逐字一致。以 253 个真实 Git 输入另建干净工具副本、保持正式断言后，**17 个完整入口共 487 项通过，0 SKIP**；34 个 Python 文件编译、11 个 shell 语法及 mock backup 通过。这些是 mock／夹具验证，不是发布或真实备份恢复。

余下 MySQL 原生打包测试入口被 Python 3.10 触发的 **DEB 安全提取器要求 Python ≥3.12** 提前拒绝，22 项中 21 条原有断言失败，未放宽要求，也不能称打包验证完成；与此前实际 MySQL 8.4.11 的 940 项业务回归是不同入口。初始工具夹具缺 git，由既有 Ubuntu 签名仓库安装固定 git `1:2.34.1-1ubuntu1.18` 及其依赖后，clean 副本的 `test_check` 8 项通过；未重启主机或另行升级内核。

初轮浏览器 supervisor 的 fake 生命周期有一次 `/proc` 退出断言失败，未用 clean 轮 41 项通过覆盖它。该次未保留 marker 内容，不能倒推其值；后续只确认了一类可确定性复现的夹具误报。探针强制 PID 文件 create→延迟→write：原 `wait_for` 仅检查存在，提前发 SIGTERM 时文件仍空；末尾实际检查 `/proc/` 目录，造成假“进程仍在”失败。该确定性探针中生产 helper 的 `cleanup=true`，四个真实自有进程的 pidfd 均证实退出，无须补发信号。仅修改 `test_browser_demo.py` 的 PID 就绪条件为完整、有界的正整数，12 秒／20ms 原等待界限、生产 supervisor、所有清理断言与沙箱保持不变。两条新增回归在旧实现中一条失败（9 个非法子场景），新实现 2/2 通过；Ubuntu 22 完整该工具测试 **43/43** 及一次强制延迟夹具信号场景通过。诊断中另一次探针自身在读取空 PID 时提前报错，也保留并精确清理自有 pidfd，不伪称产品复现。后端支付与前端源码不受此测试夹具修改影响。

### Ubuntu 兼容证据、扫描与未完成门禁

在独立、非 root、TCP 关闭的 Oracle MySQL **8.4.11** 私有 socket 上，实际执行全后端 `go test -mod=readonly -p=1 -race -count=1 -v -timeout=10m ./...` 与 `go vet -mod=readonly -p=1 ./...`：**940 PASS、0 FAIL、0 SKIP，vet 退出 0**。主机仅约 1GB 内存，采用低内存夹具参数，不降低业务断言。四个 jammy DEB 从 Oracle 官方 HTTPS 元数据固定版本、大小和 SHA-256 后校验，仅解压到本轮目录；本地未另做 InRelease 签名验证，不冒充正式 CI 的预审固定清单。未安装系统 MySQL、未触碰既有实例；夹具进程已核对清理，合成数据目录私有保留。这里证明 Ubuntu 22 的真实数据库回归，不是安装、升级、备份恢复或回滚验收。

Ubuntu 22 实机能力探针调用生产 `Native.verify_empty_service_hooks`：空数组接受，六种非空钩子分别被实际 typed D-Bus 响应拒绝；七个随机 runtime unit 只加载、不启动，核对后全部清理。真实单 FD `close_range(CLOEXEC)` 保持 FD 开放并成功设置 CLOEXEC。三个 systemd 模板的安全指令在仅替换 ExecStart 程序路径的探针中通过；原模板因产品尚未安装退出 1，**不将指令探针算作原始 unit 启动或原生安装验收**。Ubuntu 22/24 为部署验证目标，其他发行版与架构按 [兼容矩阵](INSTALL.md#前置确认) 明示未验证，不改变正式 Ubuntu 24 GitHub-hosted 门禁或数据库包校验。

`npm audit --audit-level=high` 退出 0、0 漏洞。固定 `govulncheck@v1.9.0 -format=text -show=version,verbose ./...` 实际连接漏洞库并退出 0：可达符号 0、导入包 0，但 required module 层有 **19 条告警**，不能简写为“Go 零漏洞”；未随本轮修复自动升级依赖。操作系统依赖的完整安全审计未执行。

仍未完成：最终提交的全入口检查、Linux 严格浏览器门禁、Ubuntu 22 整套原生安装/systemd 实际运行/Nginx/TLS/升级/备份恢复/回滚、当前候选的正式 Ubuntu 24 原生门禁、完整 Python/发布工具入口及工作流检查、正式双构建/Release/全部安装资产重新下载校验。未测试真实支付渠道、外部邮件投递或真实资源开通。本轮合成 demo、MySQL 回归、能力探针与 Mac 浏览器结果不能替代这些项目，也没有放宽保护、伪造 GitHub runner 身份或绕过浏览器沙箱。

本地非敏感日志、失败记录、源码 SHA-256、脚本和前后截图保存在 `vps-billing/artifacts/handoff-20261020/`（被 Git 忽略的阶段交付目录，不是发行资产）。逐项结果绑定实际源码哈希，不借用历史记录充数。

## 2026-10-17：本地严格浏览器验收续办

从干净的本地 main `12086b985e5f85889fe60d0cfaedcee4ce8fb757` 继续，原 UI 候选及 `a1c88a5` 的两份本地发行候选保持原字节。未新建分支、推送、改标签或发布。当前 checkout 没有远端跟踪引用或 main upstream，本轮没有联网复核远端，不把本地状态冒充今日远端 SHA。

已读取实际工作流并纠正阶段边界：全部本地发行检查不是 Git commit 或 push 的源码硬前置；main push 会自动触发只读 CI，候选的 backend 版本文件还会命中性能工作流，两者都可能重新访问已被拒绝的官方依赖。按本轮禁止触发重试及暂不推送的要求，仍不执行 push。正式发行另须稳定标签精确源码的完整门禁、双构建、真实 Actions 来源与独立发布器核验；本地检查通过不代表已满足正式 Release 条件。

修正版本解析后实际重跑原严格入口，原装 Chromium `154.0.8037.57 built on Debian GNU/Linux 13 (trixie)` 已通过可信文件与版本检查，并启动真实回环 demo API 和沙箱浏览器。首次在移动导航返回工作台的按钮断言失败；有界页面诊断确认路由为 `/overview`、语言为中文、main 不处于 inert，页头与无服务空态各有一枚同名“选购套餐”按钮。demo 初始无服务，属于全页选择器歧义，不是产品入口消失。派生诊断不计作验收通过。

`fdd1ef8edf71d6e52653b798c222eda8fc86a355` 将该断言限定到页头，另保留空态按钮可见断言，并实际点击空态入口、进入套餐页、使用浏览器返回工作台；未使用 `.first()` 掩盖歧义，未改产品文案或业务代码。该干净提交的原严格入口实际 exit 0，`passed=true`、`functional_passed=true`、`stage=complete`、`cleanup=true`，全部 50 组中英文响应式边界通过，原有 30 张截图清单完整。可信文件、版本、沙箱、源码前后 clean、功能、截图上限与自有资源清理条件均保留，没有改浏览器属主、系统安全或网络配置。人工实际查看了桌面空态工作台、手机工作台和手机套餐三张整页截图；不把其余截图元数据检查写成全量人工视觉验收，也不将空态 demo 视为全部生产数据场景。

本轮从已有依赖执行前端格式、类型、16 文件／441 项组件和生产构建，全部通过，无组件失败或跳过；项目仍没有独立 lint 脚本。固定 Go `1.27.2` 离线 race 测试为 723 PASS、158 SKIP、0 FAIL，vet、缓存完整性及独立静态构建通过，保留数据库／权限依赖的跳过，不能作为 `check.sh` 必须无跳过的真实 MySQL 门禁通过。Python `3.12.14` 离线 CI 回归 301 项，以及发布清单 10、插件管理器 47、更新管理器 45、Core 准备器 23、主机操作器 83 项分别通过；语法、mock backup 和配置冒烟通过。这批检查开始于 `12086b9`；期间仅提交上述 e2e 测试修正，产品源码、组件测试与构建输入不变，各项结果保留实际执行时间和提交绑定。

漏洞库原错误只记录 `Forbidden`，MySQL 原错误为 HTTP 403／curl exit 22，均没有响应头体或审计关联信息，拒绝层尚不能确定。当前执行器声明网络 enabled，不等于原目标可达，也不能据此认定用户撤回授权或已可重试；没有保存环境网络设置或出口审计的管理工具。本轮没有再次请求这些目标、Actions 或旧 Release，没有检查 `/root/.curlrc`。剩余依赖须由保存环境所有者核对有效网络规则、平台管理员查原出口审计、GitHub 管理员核对相关安装权限；完整原生验收仍需真实 Ubuntu 24.04 临时 systemd／Nginx 运行器，当前 PID 1 为 tail 的环境不能替代。正式网络／原生条件恢复并解除当前重试限制后，才继续 main 同步与其完整 CI，再进入正式标签构建和发布。

## 本地工具修正：Debian Chromium 构建标注解析

在 `a1c88a5e14c83ea93c894efe1149131ecef6c050` 的严格浏览器入口拒绝后，仅检查本地版本输出、安装包记录和校验器源码。实际版本为 `Chromium 154.0.8037.57 built on Debian GNU/Linux 13 (trixie)`；后缀由发行版原装 wrapper 设置，wrapper 与本机 dpkg 记录匹配。原入口本已接受 Chromium，却仅识别上游 channel 文本，属于构建标注解析遗漏；没有找到禁止该发行标注的通用浏览器规则。正式 CI 的 Ubuntu/预装 Chrome provisioner 限制仍独立保留。

修正保留原上游品牌、四段有界数字和 channel 正则，只增加严格完整匹配的 Chromium Debian 构建标注分支；保留 raw 版本原文。没有增加或取消数值最低版本（原入口没有该断言），没有修改可信文件、ASCII/大小/时限、源码 clean、沙箱、功能、50 组边界、截图或清理要求，也没有改 wrapper、环境变量或浏览器路径。

41 项离线工具测试通过，包括原格式、实际 Debian 字符串、伪造尾文/换行/畸形版本拒绝，以及完整 fake 生命周期和沙箱失败仍拒绝。未重新运行真实浏览器门禁；版本解析或 fake 测试成功不能代替实际功能、几何、截图与清理通过。本轮没有重试漏洞库、MySQL、Actions 或旧 Release 的拒绝请求，没有推送、改标签或发布；已有 `a1c88a5` 版本包保持原字节和原来源，不重建或伪造 `PROVENANCE.json`。

## Core 0.5.5 UI 候选：本地复核与发布阻断

2026-10-16，从本地与远端一致的 `6a21841d7458c95b29fdae1830f99d59c9652ddc` 恢复。此前 11 个候选文件的哈希与交付清单完全一致，先作独立快照再继续；未带入另一任务的管理员概览候选。目标为 Core `0.5.5`，SMTP 仍为 `1.1.0`，支付最低 Core 仍为 `0.5.3`。版本与文档是发布准备，不表示正式 Release 已完成。

客户首页围绕服务、续费和待处理账单重排；套餐改为对齐规格与价格的比较行、选择状态和订单摘要，手机保留底部核对入口。共用导航改为文档 grid 轨道、视口内独立滚动侧栏及 1024px/1025px 抽屉边界；关闭按钮、焦点、返回、长品牌/用户名和顶栏滚动留白同步处理。账单窄屏重排与客户/后台共用文字、表面和间距。本次不新增业务功能，不修改付款、金额、安全、多币种或数据库更新规则。三个指定设计参考的 SKILL 与相关专项规范已实际读取；设计质量按实际页面和用户反馈评审，不由测试通过代替。

完整前端检查使用 Node `24.19.0`、npm `11.9.0`。原工作区的格式、类型、16 文件/441 项组件测试及生产构建通过；隔离副本又执行锁定安装，依赖版本与原目录一致，格式、类型和构建通过，两次生产输出 SHA 相同。新安装全量测试曾在 16 worker 并行负载下发生一项 5000ms 超时，失败记录保留；限制为 2 worker、保持原超时与全部断言后 441/441 通过，无跳过。初始全量 updates 集成夹具出现 4 条 Vue watcher 警告，已定位为 jsdom 缺少原生 dialog 方法；仅补齐测试夹具并在每例后恢复原描述符，没有改生产逻辑或放宽断言。修正后聚焦 28 项及当前工作区全量 441 项在显式输出 console 的 reporter 下均通过，Vue 警告为 0。项目没有独立 lint 脚本，未声称运行 ESLint。

最终实页复核在本云端执行器运行，浏览器沙箱开启，18 场景/27 张截图覆盖客户首页、套餐、账单、后台、桌面/手机、中英文、空态、长名称/金额及 1024px/1025px。八个 UI 源文件哈希与候选一致，无新增视觉故障、运行错误、外部请求或写操作。此为合成接口数据的真实 Vue 页面，不能代替真实后端浏览器门禁。此前四组实际鼠标抽屉检查中正文 Y=500 在开关与侧栏滚动后保持不变，Tab/回绕、关闭、订单导航/返回及断点语义通过；桌面跳到正文也未被顶栏遮挡。整页图与单视口图分别标注。

离线 Python CI 回归最终在固定 Go `1.27.2`、Python `3.12.14` 下 297/297 通过。过程中发现 peer-probe 测试服务端未读取请求就立即关闭的夹具竞争，仅同步夹具的有界请求读取与响应半关闭，保留任何响应必须拒绝的原断言；聚焦连续 10 次及最终全套通过，未改生产守卫。此前错误 Go 路径、并发负载下的 backup scanner 超时及误选系统 Python 3.13 的失败均保留，不计作通过；最终回归保持原断言和超时。发布 manifest 10 项、插件管理器 47 项、更新管理器 45 项、Core 准备器 23 项、主机执行器 83 项及 mock backup 回归分别通过；shell 11 文件、Python 34 文件语法检查通过。这些 mock/离线结果不代替原生数据库或服务验收。

严格浏览器门禁初次在启动任何后端或浏览器之前拒绝：新增的两个重复截图名称使清单从 30 增到 32。修正仅移除 1024/1025 的两次重复截图采集，保留这些宽度的全部几何断言及原有 30 张上限；新增实际 e2e 清单与守卫契约、31 张拒绝测试。客户概览的取消后刷新断言也更新为新呈现结构，管理员断言保持。该入口还要求干净提交和受支持的浏览器版本接口；系统 Chromium 的 Debian 版本后缀不满足现有格式，未放宽规则或修改浏览器。

已从官方 Go 元数据取得并按大小/SHA-256 核对 Go `1.27.2`，仅放在任务工作目录；模块下载与校验通过。npm audit 报告 0 漏洞，固定 actionlint 检查通过。Go 漏洞扫描器实际启动后，从 `https://vuln.go.dev/index/modules.json.gz` 读取数据库被 `Forbidden` 拒绝；扫描未完成，不等同于零告警，未重试或切换数据库。真实 MySQL 入口按既有固定清单请求官方 Ubuntu 包，首个 server-core 请求返回 403，尚未启动数据库或执行数据库测试，未重试或换镜像。无数据库 DSN 的独立 Go race 测试退出 0，723 项通过、158 项跳过，无失败；Go vet 通过。存在跳过，因此不能作为真实 MySQL 门禁通过。

当前执行器 PID 1 为 `tail`，不是受支持的临时 GitHub-hosted systemd/Nginx 原生验收环境。既有正式 0.5.2 升级基线资产下载与 Actions 读取此前已被拒绝，按用户限制没有重试、换路或伪造环境来完成门禁。精确来源成功构建、原生安装/HTTPS/升级回退/恢复、完整 Go 扫描和正式发布器来源仍须分别完成。Linux amd64 本地候选及双构建结果必须绑定实际提交；缺少官方 Actions 来源时不得制造 `PROVENANCE.json` 或声称六资产正常 Release 已完整交付。

自动化证据、失败记录与恢复快照保存在本任务执行器的独立输出目录，最终事实以各项退出码、来源摘要和正式 API 状态为准。未满足上述门禁时，不推送发布标签、不上传或公开正式 Release，不删除或覆盖旧版本。

各项结果绑定实际执行时的提交、日期和 GitHub run；后续源码或界面修改不沿用旧提交的通过结论。

## Core 0.5.4：登录语言修复与维护补丁

本轮从已通过完整 CI 的 `39f7b085a918748650712fd10ef7334a25a59088` 继续。父会话回报 Mac 已对固定 `cea24f60fa35e0b5bb0890e3a2cd14bd64a51bef` 完成实际复验；该提交与 `39f7b08` 的前端树相同，为 `663cbc46a40d61b2a0838cd01823ea230bf481a4`。48 组静态边界测量覆盖六个宽度、中英文及最大文本／金额，没有整页横向溢出或金额截断；实页移动导航、焦点返回和路由滚动恢复通过。五张实际产品截图已由该任务保存到 Library 并交付用户，本地测试环境与截图已清理。支付异常仅验证静态文案布局，不是实际付款或真实请求超时测试，也不把它等同于云端 50 项自动几何断言。

Mac 另发现登录失败后切换 English，既有错误仍显示中文。原因是捕获异常时已将错误格式化为字符串，后续语言变化没有重新计算展示。修复限定登录／启动错误的瞬时状态与当前语言下的文案计算，保留请求编号；切换语言不重新提交登录、恢复连接或改变加载状态。中英文双向切换，以及错误、加载、手动重试和成功清理须由组件回归实际验证。布局和交互视觉证据仅沿用上述未改部分，不声称修复后重新看过截图。

先在旧 App.vue 上执行新增的七项组件回归，实际为 5 FAIL／2 PASS：初始化网络错误、登录鉴权错误的双向切换，以及加载期间切换后收到旧语言响应均复现文案固化；两项成功重试进入工作台的已有行为通过。此处为组件 mock HTTP 证据，没有真实网络、付款或浏览器视觉验收。

修复后原七项通过，补充未知 API 错误双语兜底、请求编号与 `undefined`／`null` 异常边界后，新文件 10／10 通过；与现有 App 回归合计两个文件 67／67 通过。类型、全前端格式及生产构建通过。最终前端差异仅为 App.vue 的 script 状态／文案计算及新增测试；template 内容逐字一致，内容 SHA256 为 `e7d9ae5ae57f28aa2e82280dba046c8d739206f26b47da554466d439618c84a5`，没有 style 块，现有 CSS、组件、API／语言传输、导航和滚动逻辑不变。这里沿用 Mac 对这些未改布局的限定证据，新增行为仅由组件回归证明，不声称重新人工看图。

Core 版本与主机插件管理器缺省版本已共同更新为 0.5.4，最低支付版本仍为 0.5.3。本地插件管理器 47 项及发布器／运行包 86 项回归通过；发布说明仅对 Core 0.5.4 说明本次维护，保留 0.5.3 与 SMTP 的既有生成行为、资产清单和 latest 规则。上述本地结果不代替最终精确源码的完整 CI 与发布构建。

发行目标为兼容补丁 Core `0.5.4`，支付协议与最低 Core `0.5.3` 保持，官方 SMTP 仍为 `1.1.0`。本轮不混入下一轮管理员概览优化，不添加真实渠道或新功能。最终源码须完成自身的完整 CI；正式发布还须经精确标签双构建、独立发布器校验和实际公共下载核验，不能用旧提交的绿灯或版本准备文案冒充发布成功。

## 主干维护：支付、响应式边界与性能

本轮从已正式发布的 `90e1dd93c5c7d7751522c8070848ef12771a1358` 开始，工作区与远端 main 初查一致且干净。Core 0.5.3、Core 0.5.2 与 SMTP 1.1.0 均已存在，不重复发布或覆盖旧资产。

已先复现两项运行时故障隔离缺陷：对健康 worker 连续发起三次已取消调用，会在零业务派发的情况下错误熔断；旧批准项的三个在途失败，也会在新批准项已成功后错误熔断新 worker。修复后调用方主动取消不计 worker 故障，结果按完整支付 Selection 归属，旧成功和旧失败均不能修改新选择的熔断。仅 SMTP 策略修订不会改变支付选择身份。本地完整 runtime、pluginserver 与协议包的 race 测试通过且无跳过；真实 RPC 截止约 12.011 秒、静默握手截止约 2.003 秒，超时仍计故障且 Core 调用槽已释放。

前端以真实 `AbortSignal.timeout(15000)` 分别复现中英文错误提示缺口：请求超时只显示通用“稍后重试”。修复限定支付入口错误处理，改为核对原渠道、刷新账单并避免重复支付。两语言的实际超时、已有入口不可用和过期入口六条相关回归通过，没有自动重发、声称未扣款或解除支付尝试。此为组件行为证据，不代表浏览器视觉验收。

新增真实 MySQL 的 COMMIT 确认丢失用例：测试连接在服务器成功提交后合成返回连接错误，分别验证预留不派发且持续阻止重复付款，以及已结算通知原样重放只保留一份账务、回执、审计和 outbox。测试仅使用隔离临时库；本地未配置合成 MySQL DSN，两例明确跳过。随后提交 [`c6f45d0`](https://github.com/xiaoqiqixi/betteridcsys/commit/c6f45d0338b800ce405356d61d392a812dc8cc94) 的 [CI 38821210456](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38821210456) 在真实 MySQL 8.4.11 中实际通过两例，Go 共 850 PASS、0 FAIL、0 SKIP；前端 14 文件／428 项及 Python 491 项通过。原生 HTTPS、独立支付进程、结算重放和取消互斥、历史迁移、中断与人工恢复、非空支付数据备份恢复也通过，自有资源清理成功。

该 CI 先采集布局基线，未修改样式。Chrome 155.0.8059.39 在启用沙箱的环境中完成既有功能检查和 30 张截图元数据检查；新增 50 项边界诊断中 37 项失败、13 项通过，因此既有 CI 成功不代表这些边界已通过。实测确认：721px 中英文面包屑碎行；120 字节英文品牌与 240 字节公司名撑宽登录页或工作区；24 字节金额前后缀把输入内容区挤到 0px。侧栏品牌本身已有正常换行，未将其误记为缺陷。最大金额仅为 DOM 布局夹具，没有把六位精度金额写入默认两位精度 CNY。

随后按上述基线修复换行、网格最小宽度和金额输入保留空间，并将完整边界矩阵升级为必过断言。输入值可见性使用浏览器原生 scrollWidth／clientWidth；Canvas 字宽只作辅助。普通 CNY 另检查 320／375／720／721／768px 下前缀、金额和币种同一行。修复后的实际结果须绑定新的精确提交，不沿用基线通过项。

本机现有 Chromium 文件属主为 UID 65534，不满足仓库要求的 UID 0 或执行者 UID 1000；现有入口在启动 API 和浏览器之前拒绝，没有生成截图。未修改属主、复制程序以规避检查、关闭沙箱，或绕过先前的 Actions 截图下载限制。受支持 CI 中的几何断言、截图元数据和人工实际看图分别记录；本轮尚无人工实际看图结论。

性能核查确认当前没有 Redis 客户端、连接配置或预留适配器。数据库池上限为 20 个连接；列表目前由后端限制最多 1000 条，前端对已载入数据筛选，并非服务端分页。登录限流与官方更新检查结果使用进程内状态，不是业务 Redis 缓存。既有并发支付测试证明幂等和取消互斥，不能换算为 HTTP 吞吐或生产容量。本轮已增加隔离 MySQL、回环 HTTP、独立 API 进程的可重复合成基线；先测量，再依据查询计划选择优化，不先增加 Redis 依赖。

合成性能配置固定为 100 个客户、1 个管理员、10000 组未付订单／账单，以及 1000 个独立历史服务；没有把未付订单标为已开通。会话在测量前写入，测量期间仅通过真实 HTTP 读取会话、客户订单、客户概览及管理员概览。并发为 1／8／32，每组暖机 1 秒、测量 3 秒、重复两次。吞吐以包含在途请求收尾的实际时间计算，延迟包括完整响应读取和错误；API CPU 单独统计，RSS／Go heap／连接池峰值按 25ms 采样。数据库等待是独占合成 MySQL 的全局计数增量，查询计划为 EXPLAIN 估计，不是逐语句实际耗时。

优化后的对照使用同一 runner、同一数据库和同一测试代码，依次运行固定基线／候选／固定基线，核对完整数据摘要不变。报告同时绑定生产源码、测试代码和程序摘要；基线附加同一测试文件只是测量构建，不冒充正式发行程序。API、数据库和负载生成器共用一台机器，回环 HTTP 不包含 Nginx、TLS、真实网络和写入负载；短时结果不能作为生产容量或长期稳定性承诺。指标仅在严格验证后写入日志与证据文件，不公开会话值、连接串或原始进程日志。

待讨论的改进是派发前原子保留内部调用额度，避免明确尚未派发的失败也留下保守在途记录。现有行为符合不释放未知尝试的合同，没有造成重复入账；改进需要调整内部调用边界及事务持槽时间，本轮不通过自动解锁或直接改数据库实现。渠道查询、撤销、退款与人工核销仍须单独设计和讨论。

### 首轮 HTTP 性能基线

[`58961d4`](https://github.com/xiaoqiqixi/betteridcsys/commit/58961d4ee6710a7bb249b98bbe0141227c3ffa79) 的[性能运行 38824505359](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38824505359) 成功。Ubuntu 24.04／Linux amd64、AMD EPYC 7763、4 个逻辑 CPU、MemTotal 16373452kB，Go 1.27.2、GOMAXPROCS=4、MySQL 8.4.11。cgroup CPU／内存上限文件不可读，报告为 unavailable，不能据此认定没有限制。24 组测量共 90906 次请求，HTTP 与传输错误均为 0；完整数据摘要前后一致，临时数据库、进程和目录清理已复查成功。

以下范围是同配置两次测量的实际值，不合并或平均分位数：

| HTTP 路径  | 并发 1，请求/秒 | 并发 32，请求/秒 | 并发 32，p95 毫秒 |
| ---------- | --------------: | ---------------: | ----------------: |
| 会话       |       1438–1439 |        4853–5311 |       10.61–11.61 |
| 客户订单   |         393–404 |          729–735 |       71.69–72.45 |
| 客户概览   |         210–215 |          510–511 |       81.87–83.62 |
| 管理员概览 |         105–106 |          241–242 |     178.72–179.02 |

API 最大采样 RSS 为 30081024 字节、Go heap 为 6289408 字节。所有测量的 MySQL 行锁和表锁等待增量为 0；并发 32 时数据库池有等待，例如客户概览每个约 3 秒窗口的累计连接等待约 33.1 秒，这是所有请求相加的等待量，不是单请求延迟。EXPLAIN 显示客户订单选择现有 user/date 索引、估计约 100 行，管理员账单汇总估计约 10000 行全扫描。概览代码实际加载该客户 100 条订单后仅保留 5 条，因此先消除这一单变量的无用读取；连接池、Redis 和索引保持原状。

### 客户概览的同机前后对照

[`1c3419a`](https://github.com/xiaoqiqixi/betteridcsys/commit/1c3419a73a2abd15aad482a1935472e33a06eae8) 的[性能运行 38825498953](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38825498953) 成功，以 `58961d4` 为固定基线，依次运行基线／候选／基线。三轮共 72 组测量、284374 次请求，HTTP 与传输错误均为 0；机器、软件及数据量沿用上述配置。完整数据摘要均为 `3d57d545034fb82e78437e2d7be0cb0709217eabb5d81193054ca759c343cdb0`，基线前后使用同一程序摘要，临时数据库、进程和目录清理均复查成功。

生产改动仅将客户概览改为按本人、时间和 ID 倒序直接取最近 5 条；返回历史继续跨币种，金额与历史快照不重算。内存实现维护至多 5 个候选并只深拷贝选中项。原订单列表、会话查询、管理员汇总和数据库结构未改。两种 SQL 都选择原有索引；EXPLAIN 的行数估计仍为 100，不能把 LIMIT 5 解释为已测得只扫描 5 个物理行，也不从当前报告推断是否存在 filesort。

以下 QPS 为各轮两次重复的合计请求数除以合计实际时间；分位数保留每个窗口的范围，不平均分位数：

| 客户概览并发 | 前基线 QPS | 候选 QPS | 后基线 QPS | 基线 p95 毫秒范围 | 候选 p95 毫秒范围 |
| ------------ | ---------: | -------: | ---------: | ----------------: | ----------------: |
| 1            |     215.83 |   282.92 |     214.85 |       5.230–5.263 |       3.821–3.825 |
| 8            |     518.49 |   896.42 |     510.73 |       19.96–20.70 |       11.40–11.48 |
| 32           |     517.65 |   986.50 |     516.01 |       82.20–82.71 |       42.25–42.48 |

并发 32 的 p50 为基线 60.40–61.35ms、候选 31.93–31.96ms，p99 为基线 91.25–93.57ms、候选 47.09–48.57ms。候选吞吐较两侧基线提高约 90.6%–91.2%，API CPU 从每请求 3.24–3.27ms 降至 1.09ms；累计连接等待折算为每请求约 21.08ms → 11.25ms，三个 MySQL 锁等待计数增量仍全为 0。该路径的 RSS 采样峰为基线 25886720／27504640 字节、候选 24268800 字节；Go heap 为 3634192／3803712、候选 3426840 字节。这些是采样峰，不能当作瞬时峰或整个系统内存。

对照路径的并发 32 QPS：订单为 742.16／743.95／743.78，管理员概览为 252.17／252.80／249.46；会话为 5132.96／5592.36／5565.84，前后基线本身存在约 8.4% 波动，不能声称会话被本次优化。客户概览两侧基线漂移约 0.3%，收益显著大于该轮漂移，但仍只是短时合成只读测量，不是统计显著性检验或生产容量认证。完整正确性与响应式结果以相应精确源码的完整 CI 为准，不能用性能任务成功替代。

### 订单快照解码的范围与微基准

本轮固定起点为 `74f77fc3efeb08e8ae6a085c9bfdd864365b740b`。先审查管理员概览和订单列表：概览成功请求重复读取三次币种配置，但各次读取显式使用只读 RR 事务；当前连接配置不强制服务器隔离级别，因此不以删除事务来减少开销。订单列表在连接仍被 Rows 占用时逐行解码历史快照，完整快照每行执行四次 JSON 解析，其中币种校验占两次。

新增微基准通过只读固定行驱动调用实际 `MySQL.Orders`／`Rows.Scan`／快照解码，不连接数据库或监听网络。相同快照与全不同快照各读取 100 条，计时外核对完整结果。起点的独立 CPU profile 中，`decodeCurrencySnapshot` 累计占 45.01%，包含在 JSON 解码累计 74.15% 中，二者不能相加。机器为 Xeon 8573C，Go 1.27.2，单线程基准；这些只证明合成解码热点，不能当作 MySQL 或 HTTP 容量。

候选仅在单次 Orders 调用内保留上一份成功校验的币种格式，命中同时要求实际币种和完整原始 JSON 字节一致；保存的字节独立复制，格式按值返回。Issuer 与套餐快照仍逐行解码，其他读取和财务写入继续使用原严格解码。没有全局业务缓存、SQL／索引／连接池调整或事务隔离变更。

新的 HTTP 报告使用 `aster.http-performance/2`：保留原四条路径及固定数据，增加管理员订单列表，先检查管理员身份、1000 条结果、客户覆盖和时间倒序，并记录原查询的安全 EXPLAIN。每轮 30 组，前基线／候选／后基线共 90 组；同一新夹具覆盖到起点与候选源码。原报告没有管理员订单结果，不能冒充这条路径的已测基线。实际收益、错误数及数据摘要须以新运行结果为准。

候选 [`39c2b7d`](https://github.com/xiaoqiqixi/betteridcsys/commit/39c2b7de7a996f2b4f75cd5ba0b51b27064eba3d) 的[性能运行 38838425033](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38838425033) 在 preflight 阶段失败，没有完整或有效的吞吐报告。新增夹具错误地从 HTTP JSON 读取有意不序列化的 `Order.UserID`，使合法响应也被拒绝；这是测量预检缺陷，不能据此判定产品权限故障。该轮自有进程和临时目录已清理，测量及整轮成功回执仍为失败。后续仅修正夹具，从固定合成订单 ID 验证范围与排序，保留产品字段隐私规则；修正后的结果须绑定新的提交与运行。

本地使用同一微基准，`-cpu=1 -benchtime=2s -count=3 -benchmem`；后基线直接执行保存的原基线二进制。每批 100 条的三次耗时中位数如下，独立 profile 运行不参与这些时延数字：

| 快照分布 | 前基线毫秒 | 候选毫秒 | 后基线毫秒 | 每批分配次数，基线 → 候选 |
| -------- | ---------: | -------: | ---------: | ------------------------: |
| 完全相同 |      0.849 |    0.379 |      0.673 |               3021 → 1933 |
| 全部不同 |      1.064 |    0.717 |      0.690 |               3141 → 3142 |

相同快照的分配量约 245KB → 188KB。全不同快照没有复用收益，多约 96 字节及一次自有缓冲分配；候选耗时较后基线高约 3.9%。前后基线本身漂移明显，不能宣称所有分布都加速或完全无退化，也不能将此微基准换算为接口吞吐。严格差分、缓冲原位修改与行／调用隔离的六项顶层测试在本地 race 检查通过。新增两项真实 MySQL 顶层测试已在 `39c2b7d` 的 [CI 38838425031](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38838425031) 实际通过，包含历史 JPY／KWD、所有权、返回值隔离和坏快照；该轮后续因新提交取消，只能采用已完成的回归证据，不能记作完整 CI 成功。

### 订单列表的同机 HTTP 实测

修正后的 [`cea24f6`](https://github.com/xiaoqiqixi/betteridcsys/commit/cea24f60fa35e0b5bb0890e3a2cd14bd64a51bef) 的[性能运行 38839166313](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38839166313) 成功。Ubuntu 24.04／Linux amd64、AMD EPYC 7763、4 个逻辑 CPU、MemTotal 16373452kB，Go 1.27.2、GOMAXPROCS=4、MySQL 8.4.11；cgroup cpuset 为 0–3，CPU／内存上限文件不可读，不能据此认定没有限制。两侧基线均为 `74f77fc3efeb08e8ae6a085c9bfdd864365b740b`，基线二进制摘要相同；三阶段采用同一新版夹具，其 SHA256 为 `15da5eb9e174c09cd5e0245f13f2045b7ce616b35faa3a03b6057c11a33ab8f8`。

三阶段分别完成 98361／102980／100845 次测量请求，合计 90 个窗口、302186 次请求，HTTP 与传输错误均为 0。完整数据摘要在各阶段及测量前后均为 `3d57d545034fb82e78437e2d7be0cb0709217eabb5d81193054ca759c343cdb0`。临时数据库、API 子进程、原生 MySQL 与自有目录清理均已核验成功。以下 QPS 按各阶段两次重复的总请求除以总实际耗时计算；基线 p95 取两侧四个窗口的范围，候选取两个窗口的范围，不平均分位数。

| 路径／并发     | 前基线 QPS | 候选 QPS | 后基线 QPS | 基线 p95 毫秒范围 | 候选 p95 毫秒范围 |
| -------------- | ---------: | -------: | ---------: | ----------------: | ----------------: |
| 客户订单／1    |     402.11 |   464.29 |     403.08 |       3.009–3.041 |       2.632–2.646 |
| 客户订单／8    |     749.98 |   903.10 |     747.67 |     16.044–16.306 |     13.209–13.358 |
| 客户订单／32   |     757.40 |   922.14 |     752.79 |     70.122–71.733 |     56.929–57.329 |
| 管理员订单／1  |      33.41 |    38.35 |      33.23 |     31.159–32.207 |     27.721–28.173 |
| 管理员订单／8  |      68.14 |    79.01 |      68.14 |   145.411–156.658 |   134.008–135.758 |
| 管理员订单／32 |      71.16 |    82.92 |      70.86 |   675.019–706.524 |   573.862–578.857 |

并发 32 时，相对两侧合并基线，客户订单吞吐提高约 22.1%，API CPU 每请求约 2.94 → 2.14ms；管理员订单提高约 16.8%，API CPU 每请求约 24.27 → 16.80ms。累计连接等待折算为每请求分别约 11.00 → 8.73ms、114.75 → 98.68ms，不能将其当作 p95 的可直接相加组成或 SQL 实际耗时。并发 1／8 没有连接池等待；所有窗口的 MySQL 行锁及表锁等待增量均为 0。管理员订单的采样 RSS 峰为前基线 68870144／候选 67891200／后基线 62189568 字节，heap 峰为 45464304／41544064／39202296；分配减少没有带来稳定的采样峰下降，不承诺总内存降低。

订单路径两侧基线吞吐漂移均不超过约 0.7%。并发 32 的对照路径 QPS：客户概览 995.96／998.52／1000.01，管理员概览 254.49／252.32／253.69，会话 5615.68／5518.77／5587.62；管理员概览没有本轮收益。会话并发 8 的两侧基线自身漂移约 9.3%，不能将它的候选增长归因于本次订单优化。

安全 EXPLAIN 显示客户订单使用既有 user/date 索引，估计 100 行；管理员订单选择 ALL，估计 10000 行，仍只返回最新 1000 条。管理员账单聚合也估计 10000 行全扫描。本轮没有改变这些 SQL 或索引，报告不包含实际 SQL 分段耗时、物理扫描量、mysqld CPU／IO 或 filesort 证据。下一优先项是补测管理员概览的逐语句耗时，再判断是否值得合并重复币种读取；保留 RR 一致性，不直接删除事务。

此数据全部为相同 CNY 历史快照的合成只读负载，客户／管理员分别返回 100／1000 条。它不覆盖全不同快照的 HTTP 分布、写入和真实支付、不包含 TLS／反向代理／外部网络，也不构成生产容量或长期稳定性认证。完整正确性以最终精确源码 CI 为准；本轮未修改界面，既有人工视觉结论和发布门槛不由这些性能结果替代。

同一 `cea24f6` 的[完整 CI 38839166097](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38839166097) 三个 job 全部成功。Go 为 936 PASS（228 项顶层、708 项子测试）、0 FAIL／0 SKIP；前端 14 文件／428 项，Python 主回归 522 项通过，原生基础与恢复后 HTTPS 各 22 项另列。新增 MySQL 快照、严格差分及预检九个子场景均实际执行；预留、结账、结算三个 COMMIT 确认丢失用例也通过。原生合成 HMAC 结算、重放、交易唯一性、取消互斥、插件故障隔离、非空支付数据备份恢复与清理通过；恢复含两份支付尝试和一份结算，完整数据一致。Core／SMTP／支付 worker 使用不同 UID，故障检查期间 Core PID 不变，没有真实支付或外部投递。

该 CI 在 Chrome 155.0.8059.39 启用沙箱后完成 50 项必过几何边界断言，30 张截图仅完成元数据核对，不能写成人工看图通过。父会话另在 Mac 对固定 `cea24f6` 做实际视觉复验；本轮后续只更新文档，前端树固定为 `663cbc46a40d61b2a0838cd01823ea230bf481a4`。视觉结论仍等待父会话带回，暂不发布；任何后续源码变化继续绑定其自己的完整 CI。

## Core 0.5.3：支付运行时接入与界面复验

### 发布前已确认的源码与视觉范围

固定源码 [`c5257e1cc8185db81e3a99bf7400fcb7cd4214e8`](https://github.com/xiaoqiqixi/betteridcsys/commit/c5257e1cc8185db81e3a99bf7400fcb7cd4214e8) 的 [完整 CI 38724893739](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38724893739) 全部成功。Go 为 836 PASS、0 FAIL、0 SKIP，包含真实 ACL/inode 测试；前端 14 文件、426 项通过，部署脚本回归 478 项通过。原生支付 HTTPS/MySQL 结算、并发取消与重放、独立插件故障恢复、历史升级、备份与非空支付记录恢复均通过。沙箱浏览器完成导航、历史滚动、财务布局等断言并保留 20 张截图；原生、核心和浏览器资源清理成功。

独立 Mac 任务随后对同一源码完成实际截图与交互复验：720/721/768px 顶栏、普通导航回顶与历史恢复、桌面和手机 CNY 单行、44px 财务按钮与焦点、财务中英文副标题均通过，手机支付结果不确定提示完整。该任务回报临时环境和进程已清理。此处采用该独立任务的限定结论，不将云端截图元数据当作人工视觉通过。

721px 面包屑换行偏碎属于已记录的非阻断显示问题。外层长品牌、极端合法金额与最长前后缀组合、支付超时和过期入口没有 Mac 预览全覆盖；已有自动测试分别覆盖部分长品牌布局、数值精度、运行时超时和中英文状态提示，不证明这些极端组合的完整视觉效果。发布准备仅更新说明和文档，不为此调整已验收 UI；新的正式来源、双构建和发布状态仍以对应流水线及实际 Release 为准。

### 支付实现与失败迭代

支付实现从 [`c9b86af`](https://github.com/xiaoqiqixi/betteridcsys/commit/c9b86af11a4c99531dd38c358b60ce371a67ef7f) 引入独立 `aster.payment/1` 插槽、受保护审批、商户身份与程序摘要、合成插件，以及持久化支付尝试和原子结算。Core 根据自身订单、账单、金额、币种精度和所有权核对已验签证据，事务内处理重放、交易唯一性、财务状态、审计和 outbox；取消与支付尝试互斥。渠道验签程序仍是经过管理员批准的信任边界，仓库没有真实第三方支付渠道。

[`207e9da`](https://github.com/xiaoqiqixi/betteridcsys/commit/207e9dada4b1da518e4392ff31c7f3fe4dc5f98b) 进一步让审批读取、握手检查与支付 RPC 共用 8 个并发名额。程序哈希读取本身不可中途取消，12 秒期限约束 RPC。支付结果不确定或会话过期不会释放尝试，也不能据此取消订单；渠道查询、撤销、退款和特定渠道应答格式仍需后续明确设计，见[支付插件指南](PAYMENT_PLUGINS.md)。

该提交的 [CI 38720882187](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38720882187) 已完成真实 MySQL 全量回归、前端检查、部署脚本回归、Go 漏洞检查和官方元数据检查。原生 API／Nginx HTTPS 以及 SMTP 独立进程的 9 项场景通过，但合成支付进程启用后的握手返回 503，完整 CI **失败**；支付结算、备份恢复和浏览器阶段不能据此记为通过。该轮自有资源清理成功。后续修复的实际结果须绑定其自己的精确提交与运行，不改写这次失败记录。

随后源码复核发现合成插件对 systemd 凭据权限的兼容缺陷：[systemd v255](https://github.com/systemd/systemd/blob/v255/src/core/exec-credential.c) 通过专属用户 ACL 交付只读凭据，ACL mask 会影响 `stat` 的组权限位。修复验证仅授予当前 worker 的只读 ACL，继续拒绝普通组可读或授予其他身份的文件，没有简单放宽为任意 `0440`。

修复提交 [`8a7e8ed`](https://github.com/xiaoqiqixi/betteridcsys/commit/8a7e8ed812fb043d0b991ffb0d64ce8772d32f4d) 的 [CI 38722990463](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38722990463) 已实际通过 ACL／打开 inode 测试，以及完整原生支付、历史数据库升级、备份与空库恢复。支付进程与 API、SMTP 使用不同 UID；HTTPS／MySQL 合成 HMAC 结算、金额核对、并发重放、交易 ID 冲突、取消互斥、插件升级回退、篡改拒绝和崩溃恢复均通过，Core PID 保持。恢复后的支付尝试与结算记录非空，完整数据摘要一致；原生资源清理成功。Go 836 PASS、0 FAIL、0 SKIP，前端 421 项及部署脚本回归通过。

该轮完整 CI 最终仍**失败**：浏览器键盘脚本期望移动菜单关闭，实际仍展开，工作流语法检查尚未运行。复核发现断点切换后没有等待应用移动状态、打开菜单后没有等待自动聚焦完成；后续脚本修复保留原有关闭、焦点与路由断言，不关闭沙箱、不增加超时。支付提示另作文字修正，避免在渠道停用、响应丢失或链接过期时宣称“未扣款”或“账单状态未改变”。上述后续修改仍须绑定各自的新提交验收。

[`4381287`](https://github.com/xiaoqiqixi/betteridcsys/commit/43812876eba53e9086fa7e5f5c8d73aaa4788c4c) 修复 Mac 反馈的页头高度、路由滚动、币种标签、按钮样式与财务副标题。其后支付提示修订并入 `c5257e1`，实际 Mac 复验的范围以上方固定提交记录为准。

本轮发布准备开始时核对的正式 Release 为 [Core v0.5.2](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.2) 和 [SMTP smtp-v1.1.0](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/smtp-v1.1.0)，其资产未覆盖，Git 标签保留。下方旧记录中的“清理尚未执行”和编译期支付示例描述属于当时状态，不代表当前主干实现或后续 Release 集合。

## Core 0.5.3：先前界面与文档迭代（历史记录）

已发布 Core 0.5.2 的标签和六资产保持固定；主干版本进入尚未发行的 0.5.3。界面修复包括三主题颜色与焦点、移动导航键盘及滚动、财务表单名称和错误关联、SMTP UTF-8 校验反馈、账单刷新生命周期、准确的选购文案，以及五种列表的已载入／可见范围和筛选 CSV 提示。财务通用服务器拒绝不再声称存在字段标注。README 和安装升级说明按实际版本与操作结果重写；支付文档区分编译期接口、已有结账、回调校验谓词与仍须集成的持久化结算，不把 SMTP 运行时协议冒作支付接口。

本地前端 14 文件／421 项全部通过，类型、格式、生产构建及浏览器脚本语法检查通过。颜色回归直接读取实际共享 CSS 和套餐 scoped 样式，检查三主题的普通文字、必要控件边界、焦点、选中态、状态及嵌套主题别名；属于不透明颜色计算，不是浏览器合成或视觉认证。独立复核补修了错误提示及套餐选中态对比度、移动侧栏纵向滚动和嵌套主题测试遗漏。

完整工具编排 237 项在本地实际通过（51.240 秒）。另运行的 38 项原生夹具护栏／TLS 运输测试、22 项准备器及 80 项执行器本地回归共 140 项通过；它们不是实际 systemd/MySQL 安装证据，也不与完整编排重复累加。支付契约示例只在 Go 测试构建注册、始终拒绝 checkout 和未验签回调；现有接口与协议包的 Go race 回归通过，无第三方新增依赖、密钥、网络、数据库写入或付款。

新的原生验收固定正式 Core 0.5.2、来源 `9ed610a283020f50e2cfc529f8b5145b4a9c8fc3`、Release 413559379、构建 38702170922 和六项精确资产为升级起点。候选须严格高于基线，保留完整安装、实际升级、独立旧 schema、备份恢复、中断、兼容回退及两类未知 schema 人工恢复场景；不因旧 Release 清理而省略验收。

### 已完成的精确源码 CI

实现提交 [`96db42107cb50ee3870b987aa6cc8fdc7d51263b`](https://github.com/xiaoqiqixi/betteridcsys/commit/96db42107cb50ee3870b987aa6cc8fdc7d51263b) 的 [CI 38708160584](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38708160584) 已完成成功。[主检查 116332632944](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38708160584/job/116332632944)、[官方元数据 116332633101](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38708160584/job/116332633101) 和[Go 扫描 116332633222](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38708160584/job/116332633222) 均通过：

| 检查               | 96db421 实际结果                                                                                                                                                                                                               |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Go / MySQL         | race 644 个 PASS，含 169 个顶层测试及子测试，0 FAIL／0 SKIP；真实隔离 MySQL、vet、静态 Linux amd64 构建及依赖边界通过                                                                                                          |
| 前端               | 14 文件／421 项通过，类型、格式与生产构建通过；包括新增列表范围、筛选导出及财务服务器拒绝反馈回归                                                                                                                              |
| Python             | 237、8、39、44、22、80、9、14 项编排与回归，再加基础／恢复后 HTTPS 各 22 项，共 497 项通过，不累加重复本地运行                                                                                                                 |
| 正式基线与安装升级 | 正式 v0.5.2 六资产与真实来源完整核验，prepare 不执行程序；实际新装和 v0.5.2 → 0.5.3 候选升级成功，非 root PID、HTTPS、管理员只读回执与终态幂等通过                                                                             |
| 恢复及失败保护     | 独立旧 schema、实际备份／同一 MySQL 的独立空库恢复、停机后 SIGKILL 续跑、端口故障兼容回退、激活前及激活后未知 schema 停写与人工修复均通过；激活后修复使用原 ID，迁移仅一次、目标启动总两次、完整数据摘要不变，不自动恢复数据库 |
| 插件、系统与供应链 | 9 项独立插件场景通过，Core PID／摘要保持；TLS 回环只接收两封合成邮件、外投为 0；实际 typed hook 与自有默认 ACL 冒烟通过。npm audit 为 0；Go 受影响代码 0、导入包 0、模块级未调用告警 19                                        |
| 浏览器与清理       | sandbox Chrome `154.0.8037.97` 功能脚本通过，包含键盘与 720／721px 切换；16 项截图元数据有效，未查看图片。内外层自有资源清理成功，测试及最终退出码均为 0                                                                       |

该次候选 API 原始、磁盘及存活程序 SHA-256 为 `28dc8db1f04b66dd0d1edb8f5c02bf71a02b425c41b72aa55c920ee9bb9c13cb`，插件宿主为 `3c74744f3b008dbd09f5ce3f9f9bc1b323610a920093bf97f109a7e6dd73392e`；它们是验收构建，未发布成正式资产。保存的解码主检查日志 SHA-256 为 `5abdd42663104cfbca7145605a9cd6bb168ce35987c97e452d54f2f8affabb25`。

日志复核确认该次只实际运行了 `ExamplePaymentProvider`；第二个示例的 `Output` 与说明注释处于同组，因此只编译未执行。随后修正注释分组，本地 `go test -race -v ./internal/integrations -run '^Example'` 已实际执行并通过两个示例（1.030 秒）。此修正不改变应用行为，也不倒改上方 CI 的实际计数。

发布后的 [`b8ad28d`](https://github.com/xiaoqiqixi/betteridcsys/commit/b8ad28d5f6bf0b92e2f4152667c07ecf13c280da) / [CI 38704975698](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38704975698) 实际成功核验并准备公开 Core 0.5.2，`source_and_integrity_verified=true`，没有程序执行、服务／数据库操作或切换活动版本。该次完整 CI **失败**：后续仍要求 0.5.1 的夹具收到复用的 0.5.2 路径，在入口护栏被拒绝。Go 643/0/0、前端 358/13、Python 494、基础与恢复后 HTTPS、插件隔离已通过；执行器主场景和浏览器未执行，外层自有资源清理成功。后续基线调整与该失败历史分别保留，不将其写成完整成功。

人工视觉验收尚未完成：本地 Chromium 的 SUID helper 所有权不符合沙箱要求，先前启动在页面前失败；当前未通过关闭沙箱或修改权限重试。Actions 截图下载曾被拒绝，未改用其他路径或编码重试。托管浏览器的功能脚本与截图元数据不能替代实际看过桌面／手机图片；因此本轮不发布 UI 完成结论。

用户后续明确授权仅保留 Core v0.5.2、SMTP 1.1.0，删除其余六个 Release／草稿及 32 个附件，全部 Git 标签、源码与 LICENSE 保留。实际删除尚未执行：连接器没有 Release 删除接口，本地 `gh` 检查报告已有认证失效；保留集合共 11 附件，未创建更高版本 Release。清理与正常发布分开，不通过扩大流水线写权限处理。

## Core 0.5.2：原生安装与升级执行

[Core v0.5.2](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.2) 已于 **2026-10-15 08:09:12 UTC** 正常公开，Release ID **413559379**，非草稿、非预发布，latest 指向 `v0.5.2`。标签及发行源码均为 **`9ed610a283020f50e2cfc529f8b5145b4a9c8fc3`**（下称 R）；[完整源码 CI 38701016075](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38701016075)、[标签构建 38702170922](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38702170922) 与[发布 38703547581](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38703547581) 均完成成功，相关全部 job/步骤通过。发布只生成正常发行资产，没有执行部署。新增激活后未知 schema 的停写、人工修复及目标重新激活场景由 R 的主干 CI 和标签构建分别真实隔离验证；各次结果绑定自己的源码和运行。

### 实施范围

管理员在受保护目录明确运行 root CLI，以只读 `plan` 的摘要确认 `apply`，中断后使用同一计划 `resume`。执行器检查专用原生布局、身份、配置和程序来源，建立独立非 root API 身份及数据库角色，执行迁移、首位管理员、systemd/Nginx 和 HTTPS 验证；开机启用必须在计划中显式选择。Web/API 仅读取严格公开回执，运行身份不能写回执及其祖先目录；没有 Web root 命令入口。秘密从私密文件读取，不写命令参数或公开回执。

生产入口只接受源码绑定、完整核验的正式 prepared 包；隔离验收使用明确标记的源码构建候选，经验证入口注入，不伪造正式 PREPARED/PROVENANCE。正式 0.5.1 基线独立取得并验证。服务验证检查 unit 字节、生效配置、确定对象路径的 typed `Unit.Id`、六个空 Exec hook 数组，以及实际非 root PID、程序摘要和 HTTPS。仅有通用 readyz 或空文本属性不构成通过。

升级在实例锁、持久化计划/步骤日志和输入重验下执行，维护开启且 API 确认停止后才备份和迁移。备份须核验元数据、gzip/摘要、旧源码及迁移前缀，续跑时重新读取；迁移结果不明时不盲目重跑。未知 schema 保持维护并要求人工修复；程序回退须先证明迁移结果已知、旧程序兼容当前 schema，并重新验证旧进程和 HTTPS，**绝不自动导入旧数据库备份**。成功回执仍要求运营者验证业务。

全新安装仅初始化 CNY；既有 USD 独立价格和历史快照保留，没有汇率或自动换汇。0.5.1 与 0.5.2 均为七项 SQL、14 张基础表，SQL/checksum 未改；版本升级是 7→7，另设独立旧 schema 6→7、13→14 表夹具。

### 发行源码 R 的完整隔离证据：通过

R 的[主检查 116308927521](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38701016075/job/116308927521)、[Go 扫描 116308927294](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38701016075/job/116308927294) 和[官方元数据 116308926954](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38701016075/job/116308926954) 均为 `completed/success`。以下只使用 R 的实际日志和脱敏事实；截图文件元数据不等于查看过图片。

| 检查                             | R 的实际结果                                                                                                                                                                                                                                                                                                                                                                                                   |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go / MySQL                       | race 643 个 PASS、0 FAIL、0 SKIP，含 168 个顶层测试及子测试；真实隔离 MySQL、vet、静态 Linux amd64 构建与依赖边界检查通过                                                                                                                                                                                                                                                                                      |
| 前端                             | 13 文件、358 项全部通过；类型、生产构建及格式检查通过                                                                                                                                                                                                                                                                                                                                                          |
| Python / 工具                    | 234、8、39、44、22、80、9、14 项 required regressions 共 450 项；再加基础原生 HTTPS 22 与恢复后 HTTPS 22，共 494 项全部通过；局部或重复运行不额外累加                                                                                                                                                                                                                                                          |
| 语法及供应链                     | Shell/Python/工作流检查与 mock 备份回归通过；npm audit 为 0 vulnerabilities；独立 Go 扫描为受影响代码 0、导入包 0、依赖模块漏洞 19，不能写成模块完全无漏洞                                                                                                                                                                                                                                                     |
| 真实 OS 冒烟                     | typed `Unit.Id` 身份匹配，六个 Exec hook 为真实空 typed 数组，实际非空 hook 被拒绝；自有 unit 未启动且已清理。默认 ACL 实际受支持，旧式创建得到 `0666`，新辅助函数得到 `0600`、目录 `0700` 且新默认 ACL 不存在；umask 已恢复，自有路径已清理，未改全局 ACL                                                                                                                                                     |
| 全新候选执行器安装               | 通过隔离 Python API 的明确候选校验入口执行真实 root 安装逻辑，未伪造正式来源；`succeeded/complete`，恰一位管理员、仅 CNY 初始价格、实际 HTTPS 登录、非 root API、三个数据库角色及含下划线库名的字面授权验证；运行角色可读 CNY、不能写回执/祖先，管理员 GET 与回执精确一致；显式开机启用和终态幂等续跑通过                                                                                                      |
| 正式 0.5.1 → 候选                | 实际公开基线 `v0.5.1` / `91cc3c18e50b51f01ca0003e3d18de3ade69f25d`，Release 413373046、构建 38672929446、六资产来源完整核验；先由未经修改的本轮执行器 CLI 安装正式旧版程序，再完成 7→7 升级、实际候选进程/HTTPS/只读回执及幂等续跑验证                                                                                                                                                                         |
| 独立旧 schema                    | 真实候选 migrate 完成 6→7、13→14 表；正式旧程序在迁移后 preflight 为 `current`，历史 USD 价格保留，重复 migrate 的全表数据摘要不变                                                                                                                                                                                                                                                                             |
| 备份与实际恢复                   | 基础原生备份/空库恢复为 14/14 表、全表数据一致、恢复后 22 项 HTTPS 通过；另将正式 0.5.1 源码绑定脚本生成的备份实际导入同一 MySQL 的独立空库，全表摘要一致、实际旧程序预检为 `current`。未在独立服务器恢复                                                                                                                                                                                                      |
| 中断与兼容回退                   | 真实服务停止后 SIGKILL 执行器，同一 operation ID 续跑到迁移及候选启动；实际占用 API 端口造成就绪故障后，仅经旧程序当前 schema 兼容验证而回退程序，维护保持开启，没有数据库自动恢复                                                                                                                                                                                                                             |
| 激活前未知 schema                | 真实修改必需列类型后，续跑持续拒绝，未核实的旧程序回退被阻止，维护开启、API 停止、迁移命令仅一次；人工恢复列类型后成功续跑，严格回执、实际候选进程/HTTPS及终态 PID 幂等均验证。激活后的独立场景另列于下一行                                                                                                                                                                                                    |
| 激活后未知 schema 与目标重新激活 | 完成真实目标激活后，将本夹具必需列改为 SMALLINT；失败和未修复续跑均保持目标链接、停止 API、HTTPS 503、数据不变，旧正式程序预检实际为 `incompatible`。私密 checkpoint 明确记录已停机待重新激活，迁移仅一次、目标启动尝试仍为一次。人工恢复 INT 后，同一 ID 成功重新激活目标：总启动尝试两次、迁移仍一次，全表数据摘要与故障前一致；严格 journal/管理员回执、非 root 存活 PID 和终态幂等均通过，无自动数据库恢复 |
| 原有业务及插件                   | API 重启保留六项订单；独立 SMTP 插件 9 项、0 失败/跳过，11 阶段 Core 摘要一致且 PID 不变；合成 TLS 回环仅接收两封、未外投，DATA 后受控崩溃不自动重发；测试 1.1.1 不是正式插件发行                                                                                                                                                                                                                              |
| 浏览器与清理                     | sandbox Chrome `155.0.8059.39` 的固定源码回环 demo 功能脚本及清理成功，未报告数值功能测试计数；16 份截图元数据字段完整、唯一且格式有效，未下载或查看图片，未进行人工视觉验收。原生验收内层 `test_completed=true`，外层 `test_exit_code=0/exit_code=0`，两层自有资源清理均成功                                                                                                                                  |

R raw、磁盘及存活 API SHA-256 为 `6f68210b80f96eccd058d52b9c2b7a356ce4cf01a25c2d1de3f2dc2de888fb81`，与正式公开 API 摘要一致。此处运行事实绑定 R；静态页面检查仅核对 `index.html` 摘要，不证明完整前端资源或视觉效果。保存的解码主检查日志 SHA-256 为 `349a2c21f29f4065f284914fdad1faa649d0d7de16802e8fd8ec13a60212a2cb`。源码 CI 和标签构建中的公开 prepare 均实际选中当时已公开的 Core 0.5.1，原生验收的新程序则为 R 现场构建的 0.5.2 候选；来源核验、候选安装验收与正式发行资产分别记录。

### 迭代记录与修复依据

A–A10 的实际验收终态均为失败；A11 后来成功不改写它们。A9/A10 各有一次 job 初始化基础设施失败，未执行项目代码，另列于表尾。诊断尚不充分的旧运行保留其不确定性。

| 候选 / 实际运行                                                                                                                                                                                                                                                                                                                                                                                  | 实际失败、修复依据与范围                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A [`f79a409`](https://github.com/xiaoqiqixi/betteridcsys/commit/f79a40978607376bf08be9288e71b3cebdb66dc4) / [38678767929](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38678767929)                                                                                                                                                                                                   | `fresh-install` 固定 `Rejected`；原日志无私密异常正文。后续本地复现为夹具受 umask 077 影响，使预期公开目录变成 0700；只修正夹具模式，未放宽生产保护                                                                                                                                                                                                                                                                                                 |
| A2 [`b8763ff`](https://github.com/xiaoqiqixi/betteridcsys/commit/b8763fffa2d32969b8dc9723687a681e9929235e) / [38679763384](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38679763384)                                                                                                                                                                                                  | 到达激活，迁移和活动目录切换完成，API 停止、维护开启、HTTPS 未就绪；当次未取得细分原因，不倒填后续诊断                                                                                                                                                                                                                                                                                                                                              |
| A3 [`b11f6bf`](https://github.com/xiaoqiqixi/betteridcsys/commit/b11f6bfd3ff72a4c40c89ccfd300e417210a6e5a) / [38680814585](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38680814585)；A4 [`b7fbc8c`](https://github.com/xiaoqiqixi/betteridcsys/commit/b7fbc8cf6db35c42f2a85048cf13a2c1fbb508bd) / [38681785020](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38681785020) | 生效配置验证严格拒绝四个缺失的空 Exec hook 文本属性；A4 证明 `systemctl show --all` 仍会省略它们，其他有值属性匹配。改为 typed D-Bus 验证，未把缺失当作空数组                                                                                                                                                                                                                                                                                       |
| A5 [`01a132e`](https://github.com/xiaoqiqixi/betteridcsys/commit/01a132ef604f9313bffd32feb99495286e32d4a9) / [38683356692](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38683356692)                                                                                                                                                                                                  | 早期空 typed hook 冒烟失败，验证属性数为 0；没有调用形状诊断，不声称已定位 GetUnit 具体失败，主原生场景未启动                                                                                                                                                                                                                                                                                                                                       |
| A6 [`4398af9`](https://github.com/xiaoqiqixi/betteridcsys/commit/4398af98d78425300b4280554d97bf52a5c24ad1) / [38684075551](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38684075551)                                                                                                                                                                                                  | 首个 GetUnit 退出 1、无有效 JSON，busctl 元数据正确；未公开原始错误。源码复核后改用固定对象路径读取 typed `Unit.Id` 及六个 hook，不把生命周期解释写作当次原始错误                                                                                                                                                                                                                                                                                   |
| A7 [`918075c`](https://github.com/xiaoqiqixi/betteridcsys/commit/918075c496bcad459c962ae999d0f6960167169a) / [38684274992](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38684274992)                                                                                                                                                                                                  | typed 冒烟和新安装通过；独立旧 schema 场景固定 `Rejected`，无子阶段证据，不能判定具体调用；后续升级/故障/浏览器未执行                                                                                                                                                                                                                                                                                                                               |
| A8 [`c216876`](https://github.com/xiaoqiqixi/betteridcsys/commit/c216876c781dbef58774d940cbb1216414a90ee8) / [38685312437](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38685312437)                                                                                                                                                                                                  | 候选 6→7 migrate 和 current 预检已成功，正式旧程序 preflight 在实际程序执行前被拒绝。后续本地实际许可证路径复现 `@` 与哈希入口语法冲突；仅修正受校验资源成员路径，管理员配置路径和保护不变                                                                                                                                                                                                                                                          |
| A9 [`2e531f3`](https://github.com/xiaoqiqixi/betteridcsys/commit/2e531f37f2a79092992da32b3cddea6e27e86970) / [38686503846 attempt 2](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38686503846/job/116262796445)                                                                                                                                                                       | 新安装、独立 6→7/旧程序预检/重复迁移及由未修改执行器 CLI 安装正式 0.5.1 通过；upgrade plan 返回且 API 已停止，随后在新 journal 产生前 `protected_mode_rejected`。阶段标签仍为旧 `upgrade-plan`，不能据此写成计划失败；当次没有被拒路径                                                                                                                                                                                                              |
| A10 [`f08a0c6`](https://github.com/xiaoqiqixi/betteridcsys/commit/f08a0c6001dff106b36b64899146f893536657f3) / [38687662294 attempt 2](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38687662294/job/116266567767)                                                                                                                                                                      | 只增诊断，实际定位到恢复前 `backup_checksums/SHA256SUMS` 的保护拒绝；四个备份成员均 root 所有、单链接普通文件、0666，三层私密目录均 0700。未执行导入及后续升级/故障/浏览器；当次未读取 ACL，不能追认其实际 ACL                                                                                                                                                                                                                                      |
| A11 [`0a59b69`](https://github.com/xiaoqiqixi/betteridcsys/commit/0a59b69283f9e9f2ad0f70e23c1ec83222bfe2fe) / [38689586746](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38689586746)                                                                                                                                                                                                 | 描述符绑定地清除/复核本次新建私密目录的默认 ACL，完成成员必须 0600；真实旧版备份脚本字节不变，不修改既有备份、全局 ACL 或 SQL。真实 OS 冒烟复现旧 0666/新 0600，完整验收成功；仍不覆盖其后发现的激活后就绪恢复缺口                                                                                                                                                                                                                                  |
| A12 [`f1d1e01`](https://github.com/xiaoqiqixi/betteridcsys/commit/f1d1e01e707bfb7b2576c77a3bbb1c897632bdfb) / [38699726947](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38699726947)                                                                                                                                                                                                 | A11 后的离线对照确认：已完成激活随后因未知 schema 停机，修复后续跑会跳过激活、回退旧程序。A12 只在实际停机后持久记录待重新激活，并严格重验维护/服务/schema；80 项离线执行器回归及独立真实激活后故障、未修复拒绝、人工修复和同 ID 幂等恢复通过。迁移和首位管理员步骤不重放                                                                                                                                                                           |
| R [`9ed610a`](https://github.com/xiaoqiqixi/betteridcsys/commit/9ed610a283020f50e2cfc529f8b5145b4a9c8fc3) / [38701016075](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38701016075)                                                                                                                                                                                                   | 最终发行源码的完整 CI 再次独立通过 Go 643/0/0、前端 358/13、Python 494 及全部原生/浏览器场景，包含激活后未知 schema 的停写、未修复续跑拒绝及人工修复后目标重新激活；后续标签构建与发布的实际结果见下节                                                                                                                                                                                                                                              |
| A9 / A10 初始化重跑                                                                                                                                                                                                                                                                                                                                                                              | [A9 attempt 1，116262448835](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38686503846/job/116262448835) 与 [A10 attempt 1，116266115275](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38687662294/job/116266115275) 均在 Set up job 下载固定 SHA 的 `actions/setup-go` 归档失败；项目未执行、fixture 未创建。只重跑失败 job，源码、workflow 和 pin 未变；attempt 2 的实际项目失败分别如上，不把初始化网络故障算作项目代码失败 |

A–A4、A7–A10 已执行主场景的失败运行均保留 `test_completed=false`、内外层自有资源移除成功及外层测试/总退出码 1；退出码 1 表示验收失败，不改写为清理失败。A5/A6 仅有早期冒烟：自有 unit 文件已删除、服务从未启动，不套用尚未启动的主场景清理指标。A11、A12 和最终发行源码 R 各自完整执行浏览器脚本，不借其结果补齐旧失败运行。

### 本地重复构建与正式发行

A12 提交前，本地完整编排 234 项和执行器 80 项实际通过；35 项验收护栏属于编排套件，不能重复相加。另以 A11 精确已提交源码和修复后源码做离线对照：独立修复夹具 schema 后，旧代码续跑为 `rolled_back`，新代码为 `succeeded`；迁移均一次、未重建管理员。该对照使用 Python 主机/数据库替身；上方 R 的托管证据另行完成了真实场景。

R 的两次独立本地构建五项未封装输出（API、插件宿主、资源归档、BUILD 与本地 SHA256SUMS）逐字节一致；177 个资源文件的源码复制与 R 匹配，含七项 SQL，资源内 ELF 为 0，没有伪造正式 `PROVENANCE.json`。两个 raw ELF 的本地摘要与下表正式资产一致；以下三项为本地实际值，与正式封装值分别记录：

| 本地未封装输出                                  | 大小（字节） | 实际 SHA-256                                                       |
| ----------------------------------------------- | -----------: | ------------------------------------------------------------------ |
| `aster-core-0.5.2-linux-amd64-resources.tar.gz` |       378257 | `864b678fcb6ba8e9109a2182e2f824f8153f259a59c106a1bdb6b28c736a6b1f` |
| `BUILD.json`                                    |         7068 | `35e6c51f9d106cad0af0a6984eef743e1698827cd8878b71990e0c2658375854` |
| `SHA256SUMS`                                    |          391 | `c35751b6c1c6cee120f749a503399ec9925dd46540b8bfefa5f0e9477b840f3a` |

实际[标签构建 job 116312726901](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38702170922/job/116312726901) 在 R 上重新完整通过 Go 643/0/0（168 顶层）、前端 358/13、Python 494、原生安装/升级/恢复/中断/回退及两类未知 schema 人工恢复场景、插件 9/0/0 和零退出清理；sandbox Chrome `154.0.8037.97` 功能脚本及 16 份截图元数据检查通过，未查看图片。两次干净 Linux amd64 构建、两次真实来源封装及封装前后递归字节比较均通过，来源绑定该实际构建 run。上述为独立重复门禁，不将重复通过数量累加到主干 CI。

本地记录 npm `11.9.0`，托管构建为 npm `11.17.0`；本地资源为 378257 字节 / `864b678fcb6ba8e9109a2182e2f824f8153f259a59c106a1bdb6b28c736a6b1f`，正式资源为 378082 字节 / `fd9e4e68b71452f19e289d78c07408304f7c9213c81d6414d5acc91e7a82acd4`，BUILD 也不同。因此只声明各环境内部重复构建一致，以及两个 raw ELF 与正式值一致，不声明跨环境资源或 BUILD 完全相同。

[发布 job 116317260626](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38703547581/job/116317260626) 重新验证上游构建、策略、全部字节及真实来源后成功公开 Release 413559379。以下为实际正常公开 Release 六资产的 ID、大小与 GitHub API SHA-256；安装核验以这些正式值及对应真实来源为准：

| 正式资产                                        |   资产 ID | 大小（字节） | 公开 SHA-256                                                       |
| ----------------------------------------------- | --------: | -----------: | ------------------------------------------------------------------ |
| `aster-api-0.5.2-linux-amd64`                   | 643169604 |      8941728 | `6f68210b80f96eccd058d52b9c2b7a356ce4cf01a25c2d1de3f2dc2de888fb81` |
| `aster-plugin-host-0.5.2-linux-amd64`           | 643169878 |      4853920 | `ee131a8b0fba629062a583716e9dfa28028e602c74ab20955df6f5ee3b1ed59d` |
| `aster-core-0.5.2-linux-amd64-resources.tar.gz` | 643169760 |       378082 | `fd9e4e68b71452f19e289d78c07408304f7c9213c81d6414d5acc91e7a82acd4` |
| `BUILD.json`                                    | 643169311 |         7068 | `7e5be266a6dd6aa37fd1a8773e5a3ffe37370a7cf32e3698235ac4d20f03b0cf` |
| `PROVENANCE.json`                               | 643169411 |         3853 | `38df192a2154c56a50b11e481133a827bd72dd4b340c49cf99bc10a187235743` |
| `SHA256SUMS`                                    | 643169509 |          467 | `32bfed3092495015ba15976fb84501ae9dbd4a0aa3d11990f53f8373b9c1b05e` |

Release API 实际为 `draft=false`、`prerelease=false`、`immutable=false`，latest 为 `v0.5.2`；不声称 GitHub 平台已经启用不可变锁定。本次没有移动或强制更新旧标签、覆盖旧资产或部署用户主机。最终 Release 库存为八个条目 / 43 资产；发布前七个 Release / 37 资产及七个旧标签均保持不变，含 Core 0.5.1 和 SMTP 1.1.0；两个历史 0.3.0 RC 仍保留草稿及预发布状态。只新增 Core 0.5.2 六资产，没有另附源码包；LICENSE SHA-256 仍为 `c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4`。

### 验证边界

支持范围是经计划确认的专用原生布局；旧手工安装不能未经审阅自动接管。隔离运行采用 GitHub-hosted Ubuntu 24.04、MySQL 8.4、私密本地 Unix socket 与 `skip_networking=1`，不改变数据库全局安全设置。真实公开仓库元数据已确认 `private=false`，此记录不假定私有仓库。

没有用户或生产主机部署、任意宝塔面板接管、Mac、Docker、真实支付/退款、外部邮件、VPS/控制器动作、生产容量/RPO/RTO、公共 CA 签发/续期、数据库进程或整机重启、独立服务器/异地恢复、PITR 或断电持久性验证。开机启用状态不等于实际重启；程序兼容回退不等于数据库降级；同一 MySQL 的独立空库恢复不等于独立服务器恢复。浏览器为外部请求受限的回环内存 demo，功能执行和截图元数据不等于人工视觉审查。摘要、回执、schema 及来源验证不构成运营认证或独立发布者签名。

未进行 Contributors 验证，未重试被拒绝的 Actions artifact、截图或相关下载，也未向 Support 发送消息。历史标签、发行资产及 LICENSE 保留；0.5.1 及更早验证正文原样保留。

## Core 0.5.1：安装准备、只读预检与未付取消

[Core v0.5.1](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.1) 已于 **2026-10-15 02:57:17 UTC** 正常公开，Release ID **413373046**，非草稿、非预发布，latest 指向 `v0.5.1`。标签与发布来源均为 `91cc3c18e50b51f01ca0003e3d18de3ade69f25d`（下称 F）；[源码完整 CI 38672137982](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672137982)、[标签构建 38672929446](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672929446) 和[发布 38673669158](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38673669158) 均完成成功，相关全部 job/步骤通过。发布仅生成正常发行资产，没有执行部署。

### 实施范围与边界

本轮复用既有系统，补齐明确版本的主机准备器 `core-manager.py prepare`、只读主机事实 `doctor`、生产 CLI 的 `-preflight` / `-schema-status`、自动机器备份元数据 `BACKUP.json`，以及权限和状态受限的未付订单取消。取消只在付款适配器禁用、订单/唯一配对账单和未处理创建事件一致、无未知服务或处理历史等条件下原子执行，同时作废配对账单并保留金额、历史快照和审计；不能用于退款、修改已付状态或撤销真实开通。

全新安装默认 CNY、运营者配置独立币种/价格、升级保留既有 USD 与历史快照的规则保持不变；没有汇率或自动换汇。当前仍为七项迁移、14 张基础表，本轮没有新增迁移或破坏性降级。

`prepared` 只表示经官方来源/资产验证的新完整目录已准备。prepare 不执行包内程序、不切 `current`、不启停服务、不创建身份/秘密、数据库或首位管理员；`manifests/PREPARED.json` 是明确标记的本地准备回执，不冒充官方资源清单或独立发布者签名。首次使用须审阅新资源中的工具和摘要，旧 0.5.0 检查器的受信清单不会自行升级或放宽。`doctor` 只读主机事实，不运行程序、不连接数据库或占用端口；数据库预检只读且不等于服务、业务、完整授权或恢复已验证。系统/数据库身份、秘密、迁移、首次管理员、systemd/Nginx 与真实 HTTPS 切换仍由主机管理员明确执行。Web 更新入口继续只做版本检查与审计交接，主机 updater 继续只做 stage/status；没有 Web root 权限、完整安装/升级执行器、自动应用或数据库自动回滚。

### F 的完整源码 CI：通过

[主检查 116217489488](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672137982/job/116217489488)、[Go 扫描 116217489637](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672137982/job/116217489637) 和[官方元数据 116217489641](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672137982/job/116217489641) 均完成成功。下表只使用 F 的实际日志和脱敏 JSON，不沿用失败候选或历史版本的结果：

| 检查                  | F 的实际结果                                                                                                                                                                                                                                 |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go / MySQL            | race 559 个 PASS、0 个 FAIL、0 个 SKIP，含 158 个顶层测试及子测试；真实隔离 MySQL、vet、静态 Linux amd64 构建与依赖边界检查通过                                                                                                              |
| 前端                  | 12 个测试文件、305 项通过；类型、生产构建及格式检查通过；曾单独运行的 97 项为该 305 项中的子集，不重复累加                                                                                                                                   |
| Python / 工具         | 编排 200、清单 8、插件管理 39、updater 44、Core 准备器 20、生产配置 9、回环安全/HTTP 14 项，共 334 项 required regressions 全部通过；再加原生 HTTPS 22、恢复后 HTTPS 22，共 378 项全部通过                                                   |
| 语法与供应链检查      | Shell/Python/工作流检查、mock 备份回归通过；npm audit 报告 0 vulnerabilities；Go 扫描实际报告受影响代码 0、导入包 0、依赖模块 19 项漏洞；19 项未发现被本项目代码调用，不等于模块完全无漏洞                                                   |
| 实际公开 Core prepare | 选中正常公开 Core **0.5.0**，源码 **`6be81aaed4868adb3c60b30accbfd87d0bb899c5`**；真实官方 Release/成功构建/来源/六资产完整性核验后得到 `prepared`，未执行所下载程序、未切活动版本，数据库和服务动作均为 `none`                              |
| 只读主机与空库预检    | `HOST_DOCTOR_JSON` 为 `read_only_host_facts`；`PREFLIGHT_EMPTY_JSON` 为 schema 0、`empty_target_scope=base_tables`、表可见性已验证；预检后另断言 `information_schema.TABLES` 中该库的表/视图计数仍为 0；该断言不覆盖存储过程、事件等其他对象 |
| 当前库预检            | `PREFLIGHT_CURRENT_JSON` 为 schema 7、管理员数 1、待迁移为空；七项已应用迁移 checksum 均验证                                                                                                                                                 |
| 备份与恢复            | `BACKUP_METADATA_JSON` 绑定实际 F 源码 BUILD、schema 7 和七项匹配预检的 checksum；源库与恢复库均为 14 张表，完整数据比较一致，恢复后 22 项 HTTPS 回归通过                                                                                    |
| 服务持久化与目录切换  | 重启前六项订单持久化；API 重启和同 schema 的 A→B→A 受控目录切换通过，不证明跨版本数据库降级                                                                                                                                                  |
| 独立 SMTP 插件        | 9 项检查、0 失败、0 跳过；11 阶段磁盘/存活 Core API 摘要均与 F raw API 一致，PID 不变，插件安装/升级/回退/受控崩溃过程中未重编或重启 Core                                                                                                    |
| 原生清理              | `test_exit_code=0`、`exit_code=0`、`owned_resources_removed=true`；runner 自有临时资源清理成功                                                                                                                                               |
| 浏览器                | Chrome `155.0.8059.39`、要求 sandbox、固定 F 源码的脚本执行成功且清理成功；16 项截图文件名/大小/SHA-256 元数据有效，未下载或查看图片，不冒充人工视觉审查                                                                                     |

F 的实际 raw、磁盘及存活 API SHA-256 均为 `98db08567ec86c0e1d0745e94c6ead3a8c5c1ffbcf2378ed74afd553a53a9d7b`。实际公开 prepare 的对象是旧公开 Core 0.5.0，原生服务/备份/恢复的对象则是 F 源码现场构建的 Core 0.5.1；二者各有来源绑定，不能把旧版本 `prepared` 写成新版本已经安装。公开 prepare 使用经护栏确认的一次性 GitHub-hosted runner 和最小只读 token，不使用伪造 `PROVENANCE.json`，不执行下载的二进制，不据来源/完整性检查宣称存在独立发布者签名。

五类新证据 `HOST_DOCTOR_JSON`、`PUBLIC_CORE_PREPARE_JSON`、`PREFLIGHT_EMPTY_JSON`、`PREFLIGHT_CURRENT_JSON`、`BACKUP_METADATA_JSON` 均在 F 对应阶段完成后实际输出。备份绑定的迁移版本 1–7 checksum 分别为：

| 版本 | SHA-256                                                            |
| ---: | ------------------------------------------------------------------ |
|    1 | `820c7dd58337da334097016a5278f2e31919766c76de896dafe199f2319a353c` |
|    2 | `43713837a1d1649f6011e0b7210ea063a03e5b308b71c17cf01120bf96f85fa8` |
|    3 | `a934b302afadb514de6e792d86d401d32185c974f93296889ca891709999b14e` |
|    4 | `8299e2463116256c84340cc578c209cde4685fd1972f636f5788b1014d397feb` |
|    5 | `38db56389f071bf9a81b8885128930a8039eb1908c61e32fd70d910678ceb083` |
|    6 | `60531806ef1ac55ce38bad09e95a3c03789bc46e2db7869f7c212a606381feab` |
|    7 | `a4e073da363299a5c0f6d231219a30003d57afa33c0fdae2bc38e244b06ba5bf` |

SMTP 原生场景为 `1.1.0 → 测试 1.1.1 → 1.1.0`，只接受两封回环 TLS 合成消息，未转发到外部；DATA 后受控崩溃保持“投递不确定”且不自动重发。测试 1.1.1 不构成正式插件发行，SMTP 1.1.0 正式资产没有更新。

浏览器摘要本身不逐项枚举取消场景。对固定 F 源码的 `frontend/tests/e2e.mjs` 成功执行和源码复核，确认其客户/管理员真实 UI 取消调用（第 897/917 行，共用 helper 第 130 行）包含对话框校验、一次 POST、订单取消/账单作废、概览金额、服务保持、单条审计与幂等重放断言。浏览器运行于回环内存 demo，外部请求被阻断；不能据此替代原生持久化数据库回归或真实付款测试。

### 失败迭代与修复依据

保留失败和取消记录，不将后续修复回填为旧 run 成功：

| 源码 / 运行                                                                                                                       | 实际终态、根因及修复范围                                                                                                                                                                                                                                                                                                                                          |
| --------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A `93f6d994f4b4fd9e8623ae2d81a655e005cecf05` / [38668093106](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38668093106) | 失败；Go 498 PASS、15 FAIL、0 SKIP，包含父/子测试，两个顶层失败。取消异常夹具在同一所有者下留下损坏邻居快照，12 个后续子场景在建立夹具时被全量订单读取提前阻断；修复夹具隔离/读取，不接受损坏数据。schema 前缀检查误把迁移 4 才引入的 `services_currency` 约束要求于前缀 3；按真实迁移生命周期修复，SQL/checksum 未改。前端/Python/原生/浏览器未执行到            |
| B `8a0ee4d34f76dd6495b595a8f7d7c38d2a040d98` / [38669136092](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38669136092) | 失败于生产配置冒烟；Go 516/0/0（154 顶层）、前端 305/12、Python 199+8+39+40+20 及 mock 备份通过。9 项启动拒绝测试中 2 项因错误文案不符失败：未知 provider 已在 DB 前拒绝，但返回 `invalid integration provider selection`，旧断言要求 `integration configuration`。C 恢复既有通用消息，早期拒绝不变。原生/公开 prepare/浏览器未执行到                             |
| C `54ec61a11ee104dbfcb8e8c2e4fb7fddec9123d3` / [38669842637](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38669842637) | 主回归通过，原生失败；Go 516/0/0（154 顶层）、前端 305/12、Python 329 全通过。实际源码 raw 包构建后，旧 GitHub token 格式限制误拒临时只读凭据，未进入公开版本选择/doctor/prepare、原生 preflight/HTTPS/备份/插件/浏览器。清理为 1/1、owned resources 已移除                                                                                                       |
| D `cf908205781ec4a87552152c92f066aee36e3293` / [38670909306](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38670909306) | 实际终态 cancelled，主检查第 6 步期间取消，没有观察到测试失败或取得完整通过；独立扫描/元数据和 runner 清理成功。不把取消写成路径故障                                                                                                                                                                                                                              |
| E `e7ad7a5dde202f2fa96fc5150ee194c4a5714675` / [38671113923](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38671113923) | 主回归通过、原生备份失败；Go 559/0/0（158 顶层）、前端 305/12、Python 333、原生 HTTPS 22、插件 9/0/0 通过，API 重启/同 schema A→B→A 完成。公开 prepare 与预检断言执行，但失败前未打印实际选择版本/来源及新 JSON 正文，不能补造为预期 0.5.0。实际备份报 metadata 不可读/无效，恢复未执行到；清理 1/1、owned resources 已移除；npm/浏览器/该 run 工作流检查未执行到 |

本轮实际资源双构建还发现历史归档兼容缺口：101 字节 npm 许可证完整路径会让 GNU tar 产生 `././@LongLink` 的 `L` 名称记录。读取器仅接受严格限长 ASCII、单一末尾 NUL、零填充、固定 marker/头部、立即匹配普通成员的历史形式；继续拒绝链式扩展、链接、PAX、稀疏记录、穿越、重复及资源内 ELF。新生产者改用 ustar 前缀格式。实际 A 资源 176 文件和旧 0.5.0 资源 174 文件通过同一受限解析 helper；F 的实际公开 prepare 另证明旧公开版本的完整来源/资产核验。归档恶意头部回归及独立复核的 33 个畸形变体全部拒绝，不把辅助复核数量累加到主套件。

C 的 token 故障修复依据为实际读取的 [GitHub 2026-04-24 官方通知](https://github.blog/changelog/2026-04-24-notice-about-upcoming-new-format-for-github-app-installation-tokens/)：安装 token（包括 Actions）转为长度可变、约 520 字符的不透明格式。Go/Python 读取器现接受 1–4096 字节 RFC 6750 ASCII token，可有一个末尾 LF/CRLF，原始受限文件上限 4098 字节；不假定前缀或内部声明，不解析 JWT。私密文件属主/权限/链接、固定官方来源、重定向凭据隔离检查保持严格，没有查看或打印实际凭据。最大长度正向用例曾因本地根目录 UID 65534 不满足原安全夹具而跳过 1 项；F 托管 Go 回归实际 0 SKIP，未放宽根目录信任。

E 的固定 `/releases/latest` 读取路径缺口在 D 相关步骤执行前离线发现；只增加该固定官方仓库的只读动作并检验精确 URL/MAX_JSON。query、额外路径、列表或其他动作继续拒绝，生产 prepare 仍必须显式版本，不增加 latest 自动安装或任意 URL/执行器。

E 的备份失败由实际资源权限确认：umask 077 加 `tar --no-same-permissions` 后得到 0700/0600，再由 sudo 复制使 BUILD/SQL 成为 root 私密文件，普通备份身份无法读取；实际 BUILD、七项 SQL、14 张表内容有效。F 只修正 CI 完整公开资源夹具的模式（目录 0755、文件 0644、六个批准程序/脚本 0755），以普通 runner 实测 BUILD/SQL 可读，并增加真实权限回归。生产备份保护不放宽；新 JSON 改为阶段完成即输出，避免后续失败掩盖已完成事实。F 完整恢复通过另有实际证据，不把 E 的失败改写为成功。

较早本地全量 Go 曾为 366 PASS、121 DB SKIP、0 FAIL，只作辅助工作区记录；F 真实 MySQL 的 559/0/0 才是冻结源码门禁。四份部署指南曾检查 50 个本地链接/锚点和 24 个 shell 片段；本地静态检查不能替代用户主机部署。

### F 的本地重复构建：已完成

F 两次独立 Core 构建五项未封装输出对应字节一致；176 个资源文件的源码复制与该 Git 提交匹配，含七个 SQL，资源内 ELF 为 0。以下为本地实际摘要与大小；F 原生运行的磁盘/存活 API 摘要另经 CI 记录，与该 raw API 一致，与下方正式 Release 六资产核验分别记录：

| F 的本地未封装产物                              | 大小（字节） | 实际 SHA-256                                                       |
| ----------------------------------------------- | -----------: | ------------------------------------------------------------------ |
| `aster-api-0.5.1-linux-amd64`                   |      8921248 | `98db08567ec86c0e1d0745e94c6ead3a8c5c1ffbcf2378ed74afd553a53a9d7b` |
| `aster-plugin-host-0.5.1-linux-amd64`           |      4853920 | `446b763589036d2221ad309ac9c442127bb5e710b1fb90d21b3403ce3a4c6dd4` |
| `aster-core-0.5.1-linux-amd64-resources.tar.gz` |       340898 | `23dd65a22503371d22050d96dca24258bc3e3bd119ab32d12c649047cf0c9ee4` |

五项本地输出为 API、插件宿主、资源、BUILD 与本地 SHA256SUMS，没有真实 `PROVENANCE.json`。实际标签构建另完成两次干净构建、两次来源封装，封装前和封装后的递归字节比较均通过；正式封装绑定实际标签构建 run。两种环境各自的双构建字节一致，不声明跨环境资源归档/BUILD 字节一致，也不把来源证明写成独立发布者签名或用户主机安装证明。

### 正式发行六资产与构建边界

实际标签构建 [job 116219973662](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38672929446/job/116219973662) 重新完整通过 Go 559/0/0、前端 305/12、Python 378、插件 9/0/0、14/14 表数据一致恢复、sandbox 浏览器 16 项截图元数据及零退出清理；其公开 prepare 实际仍选发布前已公开的 Core 0.5.0。两次构建/封装比较及上传成功后，[发布 job 116222282969](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38673669158/job/116222282969) 校验策略和来源并成功公开 Release 413373046。发布后文档提交的验证状态以该提交的 GitHub commit status 与外部交接记录为准；源码/标签运行中的旧版 prepare 结果不冒充公开 0.5.1 的准备结果。

以下为实际正常公开 Release 的六资产大小与 GitHub API SHA-256；安装核验以这些正式发行值及对应真实来源为准：

| 正式资产                                        | 大小（字节） | 公开 SHA-256                                                       |
| ----------------------------------------------- | -----------: | ------------------------------------------------------------------ |
| `aster-api-0.5.1-linux-amd64`                   |      8921248 | `98db08567ec86c0e1d0745e94c6ead3a8c5c1ffbcf2378ed74afd553a53a9d7b` |
| `aster-plugin-host-0.5.1-linux-amd64`           |      4853920 | `446b763589036d2221ad309ac9c442127bb5e710b1fb90d21b3403ce3a4c6dd4` |
| `aster-core-0.5.1-linux-amd64-resources.tar.gz` |       340896 | `899565236ac310c2a45c77f44e2bb7fff6e20a6529083e223a10f884e6725bcf` |
| `BUILD.json`                                    |         6980 | `7cf46cfee6e424c632ecd90afc092454da8f6f6cc626ef73c48db480d04b33d9` |
| `PROVENANCE.json`                               |         3480 | `69f21c8f3ec97399fdb0fc1e1a4ad36214264f1d2b0cbb381d1b506b236736c2` |
| `SHA256SUMS`                                    |          467 | `880769cc47e4a135b0888262a4835524f6b30945d0fd7b4da811b30b99d909fe` |

两个 raw ELF 的公开摘要与本地核验字节一致；其中 API 摘要还与原生验收的磁盘及存活程序一致。公开资源为 `899565236ac310c2a45c77f44e2bb7fff6e20a6529083e223a10f884e6725bcf` / 340896 字节，和本地资源 `23dd65a22503371d22050d96dca24258bc3e3bd119ab32d12c649047cf0c9ee4` / 340898 字节不同；BUILD 也不同。本地 BUILD 记录 npm `11.9.0`，实际标签构建日志记录 npm `11.17.0`，工具元数据环境不同，因此只声明各环境内部的重复构建一致，不声称跨环境资源或 BUILD 完全相同。

本轮仅发布 Core `v0.5.1`，仍为原始 API、插件宿主、资源归档及三项元数据共六资产；SMTP `smtp-v1.1.0` 仍为五资产，没有重发或修改。独立公开库存核验确认六个既有 Release（`v0.3.0-rc.3`、`v0.3.0-rc.4`、`v0.4.0`、`v0.5.0`、`smtp-v1.0.0`、`smtp-v1.1.0`）及全部原附件保持不变。Git 源码和 LICENSE 保留，没有手动附加新源码包，也没有下载被拒绝的 Actions 候选 ZIP 或截图。

### 参考阅读与未验证范围

实际参考范围见 [REFERENCE_SCOPE.md](REFERENCE_SCOPE.md)。用户指定的 `https://www.idcsmart.com/wiki_list/479.html` 没有读到内容：浏览器工具不可访问，之后一次普通请求遇到网络隧道 403，已停止且没有重试绕过。官方安装、下载、升级、迁移恢复、订单、账单章节已分别实际读取；相关章节不能写成已读取原 479 页面，也不据产品页面推导对方内部安全实现或本项目验收结论。

全部验证限 GitHub-hosted Ubuntu 24.04 隔离环境和合成数据，浏览器为回环内存 demo。没有用户服务器或宝塔实际面板部署，没有 Mac、真实付款/退款、外部邮件、VPS/控制器动作、生产容量或人工截图视觉验收。私有 CA 的 localhost HTTPS 不证明公共证书签发/续期；API 重启和同 schema A→B→A 不证明数据库进程/整机重启、真实跨版本降级、独立实例/异地恢复、PITR、断电持久性或生产 RPO/RTO。预检只证明其限定配置、可见基础表和已知迁移前缀，不能证明完整业务/授权/运行时就绪。BACKUP.json、prepare 回执、文件摘要和 schema 结果均不构成运营认证。既有 Contributors/下载拒绝未重试绕过，未下载被拒绝的 Actions 归档或截图。

## 历史 Core 0.5.0 / SMTP 1.1.0 的验证与来源

已验证源码：`6be81aaed4868adb3c60b30accbfd87d0bb899c5`，执行日期为 2026-10-14 UTC。[完整 CI 38655661440](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38655661440) 的[应用、原生与浏览器检查](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38655661440/job/116164687273)、[独立 Go 扫描](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38655661440/job/116164687106)及[官方发布元数据读取](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38655661440/job/116164687294)均成功。以下结果绑定该源码，不沿用历史版本或未提交修改的结论。

| 检查           | 实际结果与范围                                                                                                                                                                                |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go / MySQL     | 403 个 PASS 事件（含子测试），0 失败、0 跳过；race、vet、编译及真实 MySQL 财务迁移、报价确认、历史快照、事务回滚与权限回归通过                                                                |
| 前端与工具     | 前端 194，Python CI 198，清单 8、插件安装 39、更新暂存 33、配置 9、回环 HTTP 14 项通过；类型、格式、生产构建及 actionlint 通过                                                                |
| 原生业务与恢复 | Ubuntu 24.04 / amd64，MySQL 8.4.11；systemd API、验证证书的 Nginx HTTPS、DML/迁移权限分离通过；7 次迁移，原库和恢复库各 14 表完整数据哈希一致，HTTPS 各 22 项通过                             |
| 财务与状态保留 | 先验证新库仅 CNY，再由管理员明确登记其他币种及独立价格；API 重启及同 schema A→B→A 保留 6 个订单；加密 SMTP 设置恢复后可解密，保持关闭且未外发                                                 |
| 独立 SMTP 插件 | 9 项检查、0 失败、0 跳过；双摘要安装、危险审批拒绝、独立 UID/进程隔离及受控崩溃恢复通过；`1.1.0 → 测试夹具 1.1.1 → 1.1.0` 的 11 阶段 Core PID、磁盘与存活程序哈希不变，健康及订单接口持续正常 |
| 浏览器         | Chrome 155.0.8059.39，沙箱开启；桌面/手机及财务配置等自动交互通过；日志记录 16 张合成截图的名称、大小、SHA-256，未下载截图或逐张人工视觉审阅                                                  |
| 扫描与清理     | npm 审计 0；Go 可达函数 0、导入包 0、模块级告警 19；原生及浏览器自有资源清理成功，原生测试与清理退出码均为 0                                                                                  |

插件回环 TLS 夹具只接受两封固定合成消息，没有外部中继。投递到 DATA 后发生一次受控 worker 崩溃，Core 保留“投递不确定”结果且未自动重发；systemd 重启 worker，Core 没有因安装、替换、回退或崩溃而重编或重启。`1.1.1` 是未发布的测试版本。Go 扫描使用 Go 1.27.2 / govulncheck 1.9.0；19 项模块级告警不等于可达漏洞，也不能据此概括为全部依赖无已知漏洞。

原生验收脚本编译的未剥离 Core SHA-256 为 `af2615c54857753c76367c7710678f564a07c6b8b733a0e2539b38717c46ae19`。同一冻结源码另行构建的精简 raw Core SHA-256 为 `18a7f7da11f5f500b3c07265d6e8d804d8c79941cc6826330271f31a44333254`，该实际程序通过 9 项配置和 14 项回环 HTTP 检查，程序哈希保持不变且测试资源已清理；这项补充检查使用内存存储，没有运行 MySQL、systemd 或原生安装。

本地 Core、SMTP 各独立构建两次，所有对应产物字节一致；已核对静态 Linux amd64 ELF、资源包不含 ELF、所选源码的全部七个 SQL 及复制文件。未封装真实 Actions 来源证明的本地输出分别为 Core 5 项、SMTP 4 项，不含真实 `PROVENANCE.json`，不作为官方发布证据。最终 Release 的程序摘要、资源摘要和来源以实际公开的 `BUILD.json`、`PROVENANCE.json`、`SHA256SUMS` 为准。

首轮源码 `c1bf8a952af38c3ea8dcaedefcf59e0140e959ce` 的 [CI 38654191416](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38654191416) 因浏览器仍断言旧 `0.4.0` 而整体失败，未放行发布。夹具改为读取所选源码版本并核对 API/页面后，上述 `6be81aa` 完整运行通过；旧失败记录保留。

验收使用隔离 runner、私有测试 CA 和合成数据；没有用户服务器部署、真实付款或外部邮件。A→B→A 使用相同程序与 schema，不证明数据库降级；恢复使用同一 MySQL 实例的新空库，未验证 MySQL 进程重启、整机重启、异地恢复、PITR 或生产 RPO/RTO。浏览器结论限于自动化检查，宝塔实际面板仍未验收。

### Core v0.5.0 正式交付

[Core v0.5.0](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.0) 已于 2026-10-14 23:56:26 UTC 公开，Release ID `413283648`，源码为上述 `6be81aa`。[标签构建 38656286815](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38656286815) 独立重跑完整门禁，应用/真实数据库、原生恢复、浏览器、依赖扫描及工作流检查均通过；测试数量与上表一致。该运行另完成 raw 产物重复构建、封装后的字节比较与实际 Actions 来源绑定。[发布 38657033711](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38657033711) 成功，GitHub API 已核对 `draft=false`、`prerelease=false`、latest 指向该 Core Release，且恰有下列六项已上传附件。

| Core 公开附件                                   | GitHub SHA-256 摘要                                                |
| ----------------------------------------------- | ------------------------------------------------------------------ |
| `aster-api-0.5.0-linux-amd64`                   | `18a7f7da11f5f500b3c07265d6e8d804d8c79941cc6826330271f31a44333254` |
| `aster-plugin-host-0.5.0-linux-amd64`           | `d439a39214ba0d238b4dcb01806ff7b9ff71786b8095539e7460cc49386d64bc` |
| `aster-core-0.5.0-linux-amd64-resources.tar.gz` | `ed83f461b7db3199a948dc0530d518aeda06a6d4c44abd9e978905c767f6a2d5` |
| `BUILD.json`                                    | `0fc77cc49ab8bf394eca1e657e5bf908068e38a5b0cac77af57b9a797de1fa4a` |
| `PROVENANCE.json`                               | `35204c2bb88beba95e34b85133f78235f9337a4a38af7721c04493191eef91c2` |
| `SHA256SUMS`                                    | `583f5515f833c5b57c48021b33ec248c72294daa6f7ebfbb5437a4f65304ef74` |

已公开 API 程序摘要与上述实际 raw 程序冒烟测试一致。资源包使用此次正式构建的摘要，不以本地构建摘要代替。Core 0.5.0 交付范围为可配置币种、独立价格、报价确认、订单/账单快照及相关审计；尚未实现完整财务账本、结算、续费、退款或控制器自动开通，正式版本发布不代表生产就绪认证。

### SMTP smtp-v1.1.0 正式交付

[SMTP smtp-v1.1.0](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/smtp-v1.1.0) 已于 2026-10-15 00:06:23 UTC 公开，Release ID `413288577`，与 Core 使用相同源码 `6be81aa`。[独立标签构建 38657298153](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38657298153) 重跑完整应用、真实数据库、原生恢复、扫描及浏览器门禁，并完成重复构建与实际 Actions 来源绑定；[发布 38658019739](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38658019739) 成功。GitHub API 已核对 `draft=false`、`prerelease=false`、恰有五项已上传附件；latest 仍为 Core `v0.5.0`。

| SMTP 公开附件                                   | GitHub SHA-256 摘要                                                |
| ----------------------------------------------- | ------------------------------------------------------------------ |
| `aster-smtp-1.1.0-linux-amd64`                  | `70127fd84e8d8c6339318b52da892b554d5644145da7a7b2587ca30bf570c068` |
| `aster-smtp-1.1.0-linux-amd64-resources.tar.gz` | `c597dbd736820be9d618afd580b8cbb3264ff557f7fd7db7e4fc4a7088ab9577` |
| `BUILD.json`                                    | `625e3f83ad689b4c3e7e1949ae0109a6b63894d953e35df85617ff4cd171fd08` |
| `PROVENANCE.json`                               | `44d9f94929626fedf4055822dea20fa962cef52d96aa79fd438bc7b39b676976` |
| `SHA256SUMS`                                    | `34491447e78ff7c3d88ba43bc7b9285da2d7a64c4859d2047c4ef3ca70f5be13` |

GitHub API 返回的三个公开 raw 程序摘要均与本地已核验程序一致。历史 `v0.3.0-rc.3`、`v0.3.0-rc.4`、`v0.4.0`、`smtp-v1.0.0` 的 Release 与附件元数据、摘要对照未变；本次核对未下载 Actions 归档或截图。SMTP 仍须独立安装、审批及启用，既有 Core 0.4.0 应先按 [升级说明](UPGRADE.md) 处理插件兼容性，不能因两个版本已公开而跳过迁移备份或自动启用邮件。

后续仅文档提交的 CI 结果单独记录；本节的程序、资源与发布结论始终绑定上述冻结源码和实际运行。

## 历史 Core 0.4.0 / SMTP 1.0.0 正式交付

交付源码：`b7699f98d926458f2b96c809c3be3ce66920f4be`。[完整 CI 38608932956](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38608932956)的[应用、真实数据库、原生与浏览器检查](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38608932956/job/116006961638)和[独立 Go 扫描](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38608932956/job/116006961025)均成功。以下数量属于该提交，历史 rc 结果不计入。

| 检查            | 实际结果与范围                                                                                                                                    |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go / MySQL      | race 91 个顶层与 125 个子测试，0 失败、0 跳过；包含真实 MySQL 更新交接审计，vet 与 Linux amd64 编译通过                                           |
| 前端            | 123 项测试、类型、格式与生产构建通过                                                                                                              |
| 工具与编排      | Python CI 193 项，插件安装工具 34 项，更新暂存工具 30 项通过；另有清单 8、配置 9、演示 HTTP 13 项                                                 |
| 原生业务 / 恢复 | 原库和恢复库各通过 21 项 HTTPS 检查；13 张表完整数据哈希一致，加密 SMTP 设置保持关闭并可解密；同 schema A→B→A 与 API 重启保留 4 个订单            |
| 独立插件        | 8 项检查、11 个 Core 不变检查阶段通过；实际安装、替换、回退、危险审批拒绝和一次受控崩溃恢复                                                       |
| 浏览器          | Chrome 155.0.8059.39，沙箱开启；桌面/手机交互及更新页本地版本状态通过，14 张合成截图与 SHA-256 留存在该 run 的浏览器 artifact；未逐张人工视觉审阅 |
| 清理与扫描      | 原生和浏览器自有资源清理成功；npm 审计 0；Go 可达函数 0、导入包 0、模块级告警 19（未导入的 SSH/OpenPGP 代码），不能概括为全部依赖零漏洞           |

插件验收中 Core UID `999`、worker UID `995` 为一次性测试身份。11 个阶段的 Core PID 均为 `25987`；磁盘文件及 `/proc/25987/exe` 的 SHA-256 均为 `b0be65091026923573d33ee416c8bbbc5e5aa36001fcb3e254bb17c4f3300057`，健康与已认证订单接口持续正常。Core 没有因插件安装、`1.0.0 → 1.0.1 → 1.0.0` 替换/回退或崩溃重编、重启。`1.0.1` 是同一受审源码的测试版本，未作为官方 Release 发布。

原生检查实际证明额外非 CLOEXEC 描述符未传入插件、环境被清除、错误 peer UID 被拒绝、API 秘密不可读、未知程序被动发现不执行、安装不自动启用、已有版本不可覆盖，以及软链接策略/改动程序摘要被拒绝。回环 TLS 夹具只接受两封固定合成消息，没有外部转发。第二次发送到达 DATA 后杀死 worker 一次，Core 报告投递不确定且未重发，systemd 出现新 worker PID；三次/60 秒及五秒重启参数保留，未实测反复崩溃耗尽启动次数。

上述 Core 摘要来自原生脚本编译的 API，未带正式包的 `-ldflags='-s -w'`；**不是最终 Release 内程序的摘要，也不是以最终归档完成原生安装的证明**。正式包另以同一冻结源码双构建，检查静态 ELF、版本、许可证、复制文件和全部四项产物字节一致性。两种构建的证据分别记录，不混用 SHA。

| 正式 Release                                                                            | 成功构建 / 公开核验                                                                                                                                                               | Linux amd64 程序归档 SHA-256                                       |
| --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| [Core v0.4.0](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.4.0)           | [构建 38609890466](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38609890466) / [发布 38610823637](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38610823637) | `ab6b9e7a91a9058b360d202e287c2e64135d157d69b7eb6620696b5a181eb662` |
| [SMTP smtp-v1.0.0](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/smtp-v1.0.0) | [构建 38611074961](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38611074961) / [发布 38612615460](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38612615460) | `719d2047f24433bf9141ecad6222a5f974cda6306bbbf06741e48ecd691fdb81` |

两个 Release 已经 API 核对 `draft=false`、`prerelease=false`、相同交付源码及精确四项附件：对应程序包、`BUILD.json`、`PROVENANCE.json`、`SHA256SUMS`。Core 先公开并保持 latest，SMTP 未取得 latest。没有手动上传新源码归档，历史标签和附件保持原样。正式发行是版本交付状态，不是生产就绪结论。

更新接口和真实 MySQL 审计、隔离 HTTP/资产回归已通过；浏览器仅检查本地版本页，主机工具只核验、锁定和暂存，**未执行用户主机安装、迁移或自动回退**。文件备份 SHA/gzip/metadata 检查不证明数据库内容或隔离恢复。当前工作区不带凭据的正式客户端实读返回不可用，独立 DNS 查询无法解析固定 `api.github.com`；未改 DNS/代理/传输策略，也不把该实读写为成功。来源一致性和 TLS 不是独立发行签名，具体边界见 [UPDATES.md](UPDATES.md)。

此前 [a48cee8 CI 38606157488](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38606157488) 在描述符 canary 身份阶段失败，未放行发布。修复隔离测试身份、启动与诊断后，重新以 `b7699f9` 完成上述检查；旧失败不重写为通过。下列 rc 记录仍作为历史证据保留。

已纠正的历史测试缺陷：旧 `test-backup-native.sh` 在 `if rg ...` 中把缺失工具的退出码 127 当成“无匹配”，因此此前该夹具输出的泄露扫描 PASS 不能作为扫描已执行的证据。此问题不替代或否定独立原生恢复中记录的表数据哈希，但相关泄露扫描必须在修复后重新执行并以新 run 为准。

## 选购流程与完整云端验收

已验证源码：`97a2f72b36f4aa4a0954a1406f36a65c64c21dc4`。[Run 38575990819](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38575990819) 的[应用与浏览器检查](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38575990819/job/115893100080)和[独立 Go 扫描](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38575990819/job/115893100530)均成功。

| 验证项         | 实际结果                                                                                                                                                               |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go 与数据库    | race 64 个顶层 + 75 个子测试，0 失败、0 跳过；真实 MySQL、vet 与 Linux amd64 编译通过                                                                                  |
| 前端           | 97 项测试、类型、生产构建和格式检查通过；新增应用级延迟币种响应回归，修复前可复现组件卸载和选择丢失                                                                    |
| 独立回归       | Python CI 158 项、许可证清单 4、启动配置 9、演示 HTTP 13 项通过                                                                                                        |
| 备份测试扫描   | 无 ripgrep 时执行真实 grep 扫描；缺失 grep、错误退出码 2/127、实际注入标记均被对应回归正确拒绝；本次日志没有缺失命令错误                                               |
| 原生服务与恢复 | systemd、验证证书的 Nginx HTTPS、DML/迁移权限分离、API 重启与同 schema A→B→A 通过；原库和恢复库分别通过 21 项 HTTPS 测试，4 个已有订单保留                             |
| 数据与 SMTP    | 13 张表完整数据哈希一致；关闭状态的加密 SMTP 设置恢复后可解密且配置有效，未发送外部邮件                                                                                |
| 浏览器         | Google Chrome 155.0.8059.39；沙箱开启；1440×1100 桌面与 390×844 手机交互、地区筛选、币种切换保留选择、摘要金额/布局、下单与历史快照、管理员设置、SMTP 默认关闭检查通过 |
| 浏览器证据     | 12 个合成截图及各自 SHA-256 记录在该 run 的 `browser-acceptance-38575990819-1` artifact；未逐张人工视觉审阅                                                            |
| 清理与扫描     | 原生和浏览器自有资源清理成功；npm 审计 0；Go 可达函数/导入包 0，模块级告警 19，范围见下文                                                                              |

Chrome 执行前校验了已安装包的 255 个文件，并收紧可写权限；这是同一 runner 内受保护的安装记录一致性检查，不是新增包来源认证。版本命令的标准输出与私有警告分离，未关闭沙箱或改变系统沙箱策略。浏览器连接独立回环内存 demo，不将其计入真实 MySQL 的 HTTPS 测试；该 run 没有向外部服务商发送邮件或发起真实付款。

## SMTP 首轮原生验收

已验证源码：`86288e3627bdc8f4cb937c1c235633e8c0db4af4`。[Run 38563825244](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38563825244) 的[应用检查](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38563825244/job/115851338909)和[独立 Go 漏洞扫描](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38563825244/job/115851339569)均成功。这是 SMTP 首轮验收的历史证据；新的选购界面与浏览器结果见上方记录。

| 验证项           | 实际结果                                                                                                               |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| 环境与数据库     | Ubuntu 24.04 / amd64；四个官方 MySQL 8.4.11 DEB 按固定大小、SHA-256 校验后提取，原生初始化与 Unix socket 连接通过      |
| Go               | Go 1.27.2；race 64 个顶层 + 75 个子测试通过，0 失败、0 跳过，包含真实 MySQL 回归；vet 与 Linux amd64 编译通过          |
| 前端             | Node.js 24.19.0；84 项测试、类型检查、格式检查和生产构建通过                                                           |
| 独立回归         | Python CI 112 项；许可证清单 4、启动配置 9、演示 HTTP 13 项通过                                                        |
| 原生服务与业务   | systemd API、Nginx HTTPS、私有测试 CA 和权限检查通过；21 项 HTTPS 测试通过                                             |
| 重启与切换       | API 重启及同二进制/schema 的 A→B→A 目录切换通过，4 个已有订单保留                                                      |
| SMTP 配置与恢复  | 保存关闭状态的加密凭据；恢复后管理清单 `configured=true`，证明匹配密钥下凭据可解密且配置有效，保持关闭且未发送外部邮件 |
| 备份与数据一致性 | 实际备份、checksum/gzip 校验、同一 MySQL 实例中新空库恢复通过；包括 SMTP 设置的 13 张表完整数据哈希一致                |
| 恢复后业务       | API 切换到恢复库，再通过 21 项 HTTPS 测试                                                                              |
| 清理             | 测试与清理退出码均为 0；自有测试资源已移除                                                                             |
| 审计与工作流     | npm 审计 0 项已知漏洞；工作流检查通过；独立 Go 扫描范围如下                                                            |

Go 扫描报告 **0 项可达函数、0 项导入包、19 项模块级告警**；余下告警来自应用未导入的 `x/crypto` SSH/OpenPGP 包，不能概括为“依赖无已知漏洞”。结果仅覆盖对应源码、构建配置和当次漏洞库快照，详细工具版本与输出见扫描 job。

SMTP 回归使用回环 SMTP/TLS 夹具，覆盖管理员 HTTP、加密保存、本机可信测试 CA 下的传输与接受，以及失败边界；没有向外部服务商或真实收件人发送邮件。SMTP 的 `accepted` 仅表示服务器接受，不能证明收件箱投递、SPF/DKIM/DMARC 或实际服务商配置有效。

## 验证范围

原生验收使用一次性 GitHub-hosted runner 和合成数据，没有连接外部业务主机、真实支付或 VPS 服务。HTTPS 使用 localhost 与临时私有 CA，没有测试真实域名、公共 CA 签发或续期。API 重启和 A→B→A 使用相同二进制及 schema，不证明不同应用版本间的迁移回滚。

备份恢复目标是同一实例的另一新空库；没有重启 MySQL 进程或整机，也没有验证独立实例、异地加密副本、断电恢复、PITR、容量或生产 RPO/RTO。当前浏览器结论是自动化交互和布局断言，不代表所有设备、完整无障碍或人工视觉验收。宝塔适配操作见 [BAOTA.md](BAOTA.md)，尚无实际面板运行结论。

具体发布与双构建结果以对应 Release 中的 `PROVENANCE.json` 和构建运行链接为准；只读 CI 不创建 Release，也不部署主机。新源码按 [发布要求](RELEASE.md) 重新生成并核对归档，历史候选的原文件和哈希保持不变。

## 应用与原生验收基线

以下为 SMTP 实现之前的历史基线，源码 `00654f431b070c44b8442fff71a18cdd65b29999`：[Push run 38557196471](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38557196471)与[PR run 38557186363](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38557186363)均通过应用检查和 Go 扫描。[Push 检查日志](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/38557196471/job/115829150826)保存原生验收摘要及脱敏 JSON；PR 使用合并测试引用，不与分支 checkout 混为同一源码。

该基线通过 Go race 38 个顶层和 50 个子测试（0 失败、0 跳过）、前端 62 项、Python CI 93 项、许可证清单 4 项、配置 8 项、演示 HTTP 13 项。原生与恢复库各通过 20 项 HTTPS 测试，12 张表完整数据哈希一致；API 重启及同 schema A→B→A 保留 4 个订单，最小权限负向检查通过，自有测试资源清理退出码为 0。这些数量不计入新的 SMTP 验收，也不证明 SMTP 行为。

此后历史候选归档处理新增 19 项离线回归，与原发布守卫 59 项一同通过；旧结果不替代新源码的完整 CI 或实际 Release 上传证据。

## 浏览器与历史候选

历史候选 `0.3.0-rc.1` 的云端 Chromium E2E 通过，浏览器沙箱保持开启。测试经回环 demo API 覆盖中文/English、CNY/USD 独立偏好、下单及历史快照、禁用支付、手机导航与管理员设置保存；检查了 1440×1100 和 390×844 截图，未出现页面错误或手机横向溢出。这不代表所有设备、完整无障碍、当前界面、宝塔环境或真实业务主机已验收。证据为 `/workspace/aster-validation-20261014/browser-final.log` 与同目录 `browser/`。

旧版本的构建、失败排查和阶段交付详情保存在 Git 历史与对应 Actions 记录中，不作为当前安装步骤。历史候选不覆盖、不改名；新源码重新构建并记录来源及哈希。
