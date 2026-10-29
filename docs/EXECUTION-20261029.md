# 2026-10-29 实施记录

完整目标仍为269项主清单、1625个末级细节和18个工作包。本文记录W13/R06迁移切片，
不把它替代财务、主控/被控、内置升级、UI、三个新仓库发行及其他清单。

## 原生配置发布结果

云 `5105b828aea145d8d096a2285fc72a5c97cc1ca5` 的
[CI40716488187](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40716488187)
已终态失败，13/18：

- Ubuntu22/24的数据恢复和会话撤销场景均通过；Ubuntu22配置计划/批准发布/只读重入/显式中断恢复通过。
- 以上5份ZIP已下载并复核大小及GitHub SHA256，原生报告确认原封存保持。详细资产ID和摘要在tracker中。
- Ubuntu24配置场景在启动VM前被宿主网络变化保护拒绝，没有来宾报告；不忽略保护或声称平台通过。
- 4项Linux/独立MySQL任务均触发controller包10分钟累积超时。完整源验证用例自身耗时7–8分钟，
  前面其他归档用例已占约3分钟；后继调度将完整源验证测试单独运行，其余CLI明确排除它后执行，
  最后运行其他包。所有子例、真实加密、race、10分钟限制保持，不重试、不删除覆盖。
- 场景源主机元数据仍为合成夹具；不能称为真实双主机迁移、重启、节点接管或浏览器新登录完成。

后端前一文档提交 `50c773512a82d9dbaedb5427c4abb4d4b182b4bb` 的CI40716722351已成功。

## 受限验证HTTP边界

独立 `NewVerificationConsole` 使用原identity服务和真实数据库，不借用普通serve启动：

1. 允许原账户重新登录，查询当前权限和明确列出的本地资源/身份/网络记录。
2. 所有业务写、用户/权限/密码变化、任务派发/恢复和节点远端观测默认拒绝；管理员也不能越过。
3. 新验证状态接口要求真实会话，明确禁止业务写与节点联系；节点列表保留历史心跳但不宣称在线。
4. 真实TLS/MySQL测试覆盖路径别名、方法绕过、错误Origin/Host、无CSRF、纯HTTP、退出和撤销后的Cookie。
5. 只读/拒绝前后全库行、列及DDL摘要一致；登录/退出只允许3张认证运行表变化，历史审计前缀保持。

当前这是HTTP组件，不是已可启动的迁移服务。专用最小SQL身份、原后像绑定的验证阶段、
非特权运行身份、宿主许可/租约/到期撤销与排空尚须接入。普通主控仍保持封存，未放行serve。
实际源归档旧Cookie失效与新登录浏览器、节点/财务重连、单活接管、回退和迁移管理UI仍未验收。

## 边界

没有修改财务金额、支付、币种、历史快照；没有新Release或旧仓库删除。原仓库221个非Finder用户文件
再次按原摘要核对无变化。测试使用临时私有Unix socket MySQL，不开放TCP、不安装系统服务或修改证书信任。
本地完整回归与清理结果在完成后追加，不预填成功。

## 本地完整回归结果

云本地main提交 `9c111a9c199ca71bba8af642a30b3fb9345a87a1` 已包含上述HTTP边界和新测试调度。
实际私有MySQL8.4.13/macOS arm64全套race通过520个顶级测试、0运行时SKIP，总574.561秒；
5个Linux/root/OVS/NAT专属项明确交给其独立Ubuntu门禁，不算本机执行。
完整源验证独立266.748秒、其余CLI89.198秒、control215.383秒、backup106.874秒。
go vet、4个Linux amd64程序构建、Linux controller测试编译、3项runner调度合同通过。
Linux交叉编译不等于实际运行，新调度仍须新SHA的Ubuntu CI证明。

测试库核对仅余预期空基础测试库与系统库，没有遗留随机测试库；凭据日志扫描通过。
本轮私有MySQL已正常停止，随机专用数据/凭据移除，未改变系统服务或证书信任。
源归档到新登录的完整浏览器、宿主验证运行、接管/回退、升级和三个新仓库正式发行仍未完成。

前轮CI终态确认后，`9c111a9`已快进推送云main；
[同SHA CI40725448468](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40725448468)
已确认运行，未提前计通过。其运行期间不推后继云提交取消它；其他工作包与本地开发继续。

## 后续：原生六场景已核验，完整CI仍有两个失败

9c111a9的CI40725448468已结束，16/18。Ubuntu22/24的数据、会话撤销与配置发布共六场景实际通过，六ZIP均核验GitHub大小和SHA256；原目标封存仍保留。源主机材料是合成元数据，不是源主机导出/节点接管/迁移登录。两平台独立MySQL与现有控制台浏览器通过。
两项Linux任务不再是Go包超时；实际失败在root目标测试的 `malformed-data-before-provision` 拒绝确认。旧输出没有具体动作/错误码，当前只补固定安全诊断，不扩充可接受错误、不把失败算成功。后端230165a的CI40725689730已通过。

## 专用SQL身份与认证并发（本地增量）

验证HTTP构造入口绑定同一最小授权数据库：仅本库读取、会话INSERT/DELETE、限流DML和四列审计INSERT。实际检查身份、库、版本、全部授权/角色/转授权，拒绝多余或缺失权限，不自动修复。普通身份写保留独占行锁；验证认证使用数据库级命名锁和共享行锁，不能为登录给用户表写权限。
实库测试覆盖10类非法SQL操作的明确权限错误、全局/库/表/列/转授权/缺失权限/未激活角色漂移、旧revision登录和普通撤权锁冲突、两个独立服务竞争第4096个匿名会话。请求取消负例发现服务器仍持命名锁：6秒后仍未释放。将受限连接的InnoDB等待固定为2秒后，同一实库负例约2秒释放并恢复正常请求；没有重试或放宽授权。
完整本地回归正在运行。root专用账号准备、宿主许可、恢复后像阶段绑定、完整迁移浏览器、接管与回退仍未完成。所有18工作包及内置升级、三个新仓库首版发行仍保留；旧仓库和历史资产不删除。

完整回归已结束：526个顶级PASS、0运行时SKIP、582.428秒；另5个Linux/root专属项不在本机执行。go vet、4个Linux amd64交叉构建、安装单测19/合同111/包26/宿主入口5/调度3项通过。专用库、验证账号与角色无残留，凭据日志扫描通过，私有MySQL正常停止并移除专用数据，未变更系统服务和信任。
原仓库221个非Finder用户文件再次逐项核对原摘要不变。云main已普通快进推送 `bbb7409f670091b63e849b7d141bf75aaa4642d5`，新同SHA [CI40803360408](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40803360408)已创建排队，未计通过。未新建Release、覆盖标签或删除旧仓库。

## Verification preparation plan: local increment, not host activation

The fixed root target-verification-plan entry now binds the original target seal, full revoked SQL image, published configuration/TLS, configuration/session intents and the exact minimum SQL grants. Root inventory remains the original two accounts. This action creates no account, starts no service and writes no configuration. SQL identity preparation, service startup and migration readiness remain false.
Public entry RED then GREEN: 6 parsing/capability checks; installer contracts111, package26 and native harness contracts61 passed. CLI privacy regression, vet and Linux controller cross-build passed. The added Ubuntu22/24 super_read_only and configuration-drift scenarios have NOT run. Prior six native results are not inherited.
Cloud bbb7409 CI40803360408 is still running. This increment remains in the worktree; no successor is pushed to cancel it. Account provisioning/revocation, independent verification data phase, host permission, browser migration, takeover/rollback and all18 work packages remain required.

Root target failure diagnosis: bbb7409 Ubuntu24 job123216420569 reached a terminal failure. Its fixed diagnostic identified target-configuration-publish returning HOST_MIGRATION_UNCONFIRMED before database provisioning. The local fix returns TARGET_DATABASE_STAGE_REQUIRED only when both plan and directory are absent and neither is a link. Partial/linked state is still unconfirmed; the native harness now checks that distinction without widening generic-error acceptance. The new native execution is pending; the current CI is not cancelled.

## 验证账号准备与状态：本地接入

固定 root prepare/status 入口已接入，绑定原恢复操作、已发布配置、完整后像和不可变管理员批准。仅干净的原两账号阶段可创建专用账号；原恢复库存检查没有放宽。验证专用三账号检查要求原意图、准确属性/权限和实际密码登录，之后才写完成回执；重复核验不重置密码、不修复授权。丢完成回执可在完整状态重新核验后补回，部分 CREATE/GRANT 中断仍保留并拒绝，尚未提供该部分状态的显式恢复/撤销。

公开入口7项、原生夹具合同61项、安装合同111项、安装单测19项、包合同26项和调度3项通过；这些不是新增 root 原生验收。完整当前工作树实库回归正在运行。Ubuntu22/24实际账号准备负例已加入原生场景，新增证明字段为必需，结果待执行。
已取回 Ubuntu22 失败 job123216420683 日志，确认与24相同：malformed-data-before-provision / target-configuration-publish / HOST_MIGRATION_UNCONFIRMED。修复只对计划和目录均缺失且非链接的情形给精确前置拒绝，部分状态仍不收编。修复原生待跑。
没有启动验证服务、解除封存、完成迁移页面或接管；18工作包、内置升级、三仓库首版发行均继续保留，旧仓库不删。

本轮完整回归已结束：526顶级PASS、0运行时SKIP、577.803秒；5项Linux/root专属测试仍由原生门禁承担。vet和4程序交叉构建通过。实际8.4.13探针确认随机密码ACK/metadata/列级授权及登录，非root原生验收。专用MySQL已正常停止，测试库/验证账号/角色无残留，日志凭据扫描通过，专用数据和密码已移除；未改变本机服务或证书信任。
bbb7409的CI40803360408现已结束16/18，不再有运行任务。后继增量不取消上一轮；仍须自己的root目标修复和新增验证账号原生场景通过。

上一版bbb7409的六个原生恢复ZIP已全部下载并核对GitHub大小/SHA256，实际报告均保持源主机导出/节点接管/迁移就绪为false；不能替代新账号准备场景。云本地main提交7bd9c98bdb4a67f476cd26288d3a445249d1d4c3包含本轮增量，推送确认与新CI另记。

云7bd9c98已普通快进推送main；[同源码CI40808686030](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40808686030)已实际进入运行。其运行期间不推后继云提交取消它。新增root场景尚未计通过，未发布或删除旧仓库。
