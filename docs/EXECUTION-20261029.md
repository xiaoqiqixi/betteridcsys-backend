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

## 验证账号撤销与真实连接排空（本地进行中）

固定root撤销计划/执行/状态入口已接入：绑定原账号完成回执和独立明确批准，不接管部分准备状态。持久意图先于ACCOUNT LOCK；原准备/凭据检查在撤销开始后拒绝。只有实际连接清单可观察、无丢失、无旧会话或待认证连接，才记完成；不KILL/DROP/reset/unlock。完成后解锁漂移拒绝，不自动修复。
实际私有MySQL8.4.13探针证明锁账号不会断开已有连接；旧连接关闭事件监测后仍可见，新连接收到3118，原连接退出后才计0。临时探针账号/凭据已删除。此为SQL语义验证，不是root原生验收。
公开CLI先RED后GREEN8项，原生夹具合同61/安装合同111/单测19/包26/调度3及vet、4个Linux交叉构建通过，完整实库回归正在运行。Ubuntu22/24新增实际持有连接、锁定拒绝、排空及super_read_only恢复场景尚未执行。
云7bd9c98的CI40808686030仍运行；已失败的会话22任务为HOST_NETWORK_CHANGED_BEFORE_VM。已核验失败ZIP12869531146摘要22531e4366da9e9b6a36ed71fea4012f8dd577aa1a84c87f0a6cf9806d07cf09：宿主地址清单3变4、路由和防火墙未变，无客体结果且未确认QEMU启动。不得算通过，也不放宽检查；其余原任务继续，不推后继云提交取消。

## 撤销回归完成与独立工作流遗漏修复

撤销增量完整私有MySQL8.4.13/macOS arm64 race已完成：526顶级PASS、0运行时SKIP、574.648秒；5项Linux/root专属项不在本机执行。私有MySQL正常停止，测试库/验证账号/角色无遗留，专用数据和凭据移除；本机服务/证书信任未改。公开CLI8、安装单测19/合同111、包26、调度3、原生/工作流合同66、vet、4个交叉构建与actionlint通过。原仓库221个非Finder文件原摘要仍全部一致。

7bd9c98两平台Linux实际root目标前置测试现已通过。Ubuntu22/24原生配置客体也都完成账号准备及负例，但随后工作流的独立精确字段清单漏了7个准备证明字段，两项完整任务在该校验失败。两份产物12870082281/12870945657已下载并核验GitHub大小及SHA256；客体通过不等于任务通过，更不是新撤销场景通过。

本批补齐准备7字段和撤销4字段，不删除或放宽独立检查。新增回归实际提取并执行工作流原Python，先复现相同行20失败，再通过完整合同样本；缺失/错误类型/错误源码/未执行场景/多余字段/宿主隔离失败全部拒绝。真实旧报告未经修改：原校验失败，仅补准备字段的诊断副本通过，而新撤销校验仍拒绝旧报告，明确新场景尚未执行。

云原CI剩余任务仍在运行时，不推后继取消它。当前增量没有正式Release、验证HTTP宿主启动或节点接管；初始部分CREATE/GRANT恢复、验证运行期后像、浏览器迁移、单活接管/回退及全18工作包继续保留。


## CI 修复优先：不继续堆叠功能

按用户要求，未完成的验证数据阶段保留在本地，不混入本次推送。b841d24 的 CI40814014672 已结束15/18：Ubuntu24数据场景在启动客体前被宿主网络变化拦截；Ubuntu22/24配置场景实际收到不匹配的拒绝结果，旧脱敏器又漏掉了具体阶段。因此不能把前者简单归为代码错误，也不能把后两项推成平台偶发。

本轮仅推送 CI 修复到云 main `879907d836f0d5bdd489a608e0d9dd9d73b45970`：原生基线前增加有固定期限的只读网络就绪观察，原有完整网络保护不变；补齐固定验证阶段、错误码和拒绝不匹配诊断，仍严格要求原错误码/退出码/空标准输出。诊断只保留白名单和布尔值，不发布参数、原始stderr或凭据。真实子进程负例先RED后GREEN，原生/工作流合同68项、就绪9项和两工作流actionlint通过；不是原生验收。

前轮所有任务终止后才正常快进推送，没有取消未完任务。[新同SHA CI40817446210](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40817446210)正在运行，配置拒绝根因仍待精确诊断，当前不能称CI修复完成。两配置失败ZIP12872781032/12872892906已下载并核对GitHub大小与SHA256。财务金额/支付/历史快照、旧标签、旧资产未改。


## 配置场景的真实拒绝根因已复现并修正

879907d 的 CI40817446210 已结束16/18：原生网络就绪步骤实际运行，数据/会话恢复、libvirt、Linux、实库和浏览器任务均通过。两配置场景的新诊断一致：撤销后的只读恢复阶段，预期“尚无完成回执”，实际却仍被判断“会话未排空”。

本地真实MySQL8.4.13确认内部GTID压缩线程也是FOREGROUND，带进程ID且用户为空；所有真实客户退出后旧谓词仍计待认证1。生产查询现在只将完整、大小写严格匹配的已知内部形态独立计数；未知/部分匹配/真实账号/匿名握手继续阻止排空，NULL三值逻辑也有反例。修正后的实库计数为待认证0/内部1，真实未认证Unix连接出现时待认证仍为1，连接退出后才回0。

12个实库分类合同先RED后GREEN，真实已认证与匿名握手探针通过；安装单测19、安装合同111、包26、宿主入口8、原生/工作流68、网络就绪9和actionlint通过。私有MySQL正常停止、临时凭据/账号/数据已清理。新增Ubuntu22/24原生证明必须同时检查实际内部线程和匿名握手拒绝，不能只继承旧结果。

全部旧任务终止后，云main正常快进至`ebe7e1921087441f37aaf7386df31661a3f5d54c`，新原生结果仍待核验。未完成功能保持本地未提交，未改变金额、支付和历史快照，未发布Release。


## 本轮 CI 修复闭环

云 `ebe7e1921087441f37aaf7386df31661a3f5d54c` 的 [CI40821157870](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40821157870) 已终态成功18/18。Ubuntu22/24实际配置场景均确认账号锁定、旧连接不被伪造排空、匿名握手阻止完成、GTID内部线程正确识别、只读恢复和解锁漂移拒绝。两平台真实MySQL均实际执行12个分类合同并清理通过。六份原生恢复ZIP已下载核验GitHub大小/SHA256及源码绑定；源主机导出、节点接管、迁移浏览器、真KVM和完整迁移就绪仍为false。

后端当前main f56f53e 的CI40808935521也已成功。财务前端未修改。本次仅云main正常快进，没有改写历史、发布版本或删除旧仓库。原仓库221个非Finder用户文件仍与交接摘要完全一致。保存的未完成功能仍未提交；本次CI修复完成不代表269项/18工作包或三个新仓库首版交付完成。

## 验证数据阶段：本地完整回归通过，宿主接入仍待完成

已实现独立的归档/实库数据协议：先绑定原精确撤销后像，再验证批准区间内的新认证数据；不忽略三张认证表，不修改财务金额、支付、多币种或历史财务快照。修复最小 BIGINT 历史审计编号误拒绝，并覆盖原限流窗口累加、到期重置、24 小时清理、旧 Cookie、停用用户、会话时效、原审计前缀、账号/权限/业务/外键与额外对象漂移。

真实 MySQL8.4.13/macOS arm64 全套 race：526 个顶级 PASS、0 运行时 SKIP、579.762 秒；5 项 Linux/root 专属测试明确不在本机执行。vet、4 个 Linux amd64 交叉构建、安装 19/合同 111/包 26/宿主入口 8/原生工作流 68/网络就绪 9/调度 3 项及 actionlint 通过。交叉构建不算原生执行。只读外来 SQL 账号写探针实际返回 1142，数据入口仍正确拒绝其替代原绑定身份；没有删 CURRENT_USER 检查来让测试通过。

首次新增测试的“任意只读账号也应通过”预期与既有所有权合同不符，已纠正测试预期，生产身份检查未改。完整实库回归在该纠正后重新执行。所有负例均以恢复夹具后的成功核验收尾，不把断网当拒绝。

私有 MySQL 已正常停止，无遗留测试库、验证账号或角色；凭据日志扫描通过，专用数据与凭据已删除。系统服务和证书信任未动；原 221 个非 Finder 文件摘要仍一致。

当前新增代码未提交、未推送，不能引用 ebe7e19 的 18/18 CI 证明本增量。仍须固定宿主批准、非特权验证服务、有效期/重启/停止排空、阶段化账号撤销、真实恢复账号浏览器、单活接管与回退；完整目标及三个首发版本仍未完成。

## 宿主验证数据阶段：同源码 Ubuntu22/24 验收闭环

前述数据增量随后提交为 `2a76ea4b38f43f6df838ac1e08be1c322ba95edc`，其 [CI40831339778](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40831339778) 完整18/18成功，六份原生恢复产物已核验，不是继承 ebe7e19 的结果。

云 main 再正常快进至 `cdfb8210c205c1c768a963b854cf4691721203ee`：接入固定 `target-verification-data-begin/status`、不可变明确批准与数据意图，并让原账号撤销核验认证增量。无数据阶段仍保持原精确后像要求。没有放行普通 serve、解除目标封存、启动验证 HTTP 或接管节点。

本地完整私有 MySQL race 为526个顶级PASS、0运行时SKIP、599.397秒；5项Linux/root专属项不在本机执行。入口合同9、直接工作流/原生合同68、安装19/111、包26、调度3、vet、4个Linux交叉构建及actionlint通过。新增5个原生证明字段同步进入实际工作流校验；先复现遗漏失败再修复，未删除测试或放宽精确字段、错误码与安全保护。

[同源码CI40836912649](https://github.com/xiaoqiqixi/betteridcsys-cloud/actions/runs/40836912649) 已终态成功18/18。Ubuntu22/24实际 root CLI 与最小权限MySQL验证缺少批准、重复意图、认证增量、业务及批准漂移、只读状态，以及账号锁定和真实连接排空后的同阶段核验。六份ZIP均核对GitHub大小、SHA256、源码/平台/场景及新增5个证明字段；没有复用旧报告。本轮现有实库浏览器回归也通过，但不是迁移新登录浏览器验收。

三个连续云提交的完整CI均18/18成功；各轮均等前一精确提交终态后才推后继，没有取消旧任务、强推、重跑碰运气或覆盖标签。临时MySQL已清理，原221个非Finder用户文件再次逐项摘要一致。财务金额、支付、多币种及历史财务快照未改；没有新增Release或删除旧仓库。

仍须完成非特权验证服务及宿主运行许可、有效期/重启/停止排空、真实源归档新登录浏览器、单活接管与回退。其余18工作包、内置升级和三个新仓库首版发行保持原清单，完整目标未完成。真KVM/物理网络按用户决定后置，不拿原生安装或合成认证SQL冒充硬件、生产验收。
