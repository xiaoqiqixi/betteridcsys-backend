# 实施与验收续记 · 2026-10-26

起始 main：`4626e321803f03be8d414f8f7cd29b77481eca97`，远端仅 main，无新标签或 Release。
用户原规划图片和三项 `.DS_Store` 保留；本轮不改财务源码或数据库 SQL。

## 上一批已取得的同 SHA 结果

| 4626e32 的实际门禁 | 结果 | 不能外推的范围 |
| --- | --- | --- |
| [Cloud CI 40188150956](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40188150956) | 11/11 SUCCESS | 不是正式 Cloud 发布或 KVM 真机验收 |
| [财务 CI 40188151010](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40188151010) | 4/4 SUCCESS | 不是生产部署或新 Core 公开资产已下载 |
| Cloud 真实 MySQL / HTTPS 浏览器 | 163 业务、53 布局、18 成员检查；70 PNG，0 console error | 节点 RPC 为隔离替身，不是真 VM 数据面 |
| 成员分页 | 205 人三页，桌面/手机均完整无漏重；匿名401、viewer403，成员业务写0 | 不含搜索/导出、MFA/SSO 或跨路由页码/滚动恢复 |
| 财务真实 MySQL / HTTPS 浏览器 | 20 检查、12 PNG；业务写0，历史表摘要不变，测试库删除与进程清理通过 | 这批财务图是暗色合成账户，不冒充全站/浅色视觉完成 |
| Ubuntu22/24 libvirt 准备 | 真实 APT、systemd、libvirt 准备通过；宿主网络未变，拒绝无 KVM | 官方镜像 TCG guest，`kvm_ready=false`；Ubuntu26仍不支持 |
| 财务原生链 | Ubuntu24 隔离 runner 的 systemd/Nginx/TLS、业务重启持久化、23表备份恢复及合成支付重放通过 | 未整机重启、未重启数据库进程、未验生产RPO/RTO、未真实付款/邮件/开通；Ubuntu22财务完整链仍待W04 |

浏览器和 libvirt 证据 ZIP 先核对 GitHub artifact 的 source SHA 与 SHA256，再解包；不是复用旧失败 run 的截图。
已实际查看成员桌面浅色、手机浅色及手机英文深色图；中文/英文标题、状态和上一/下一页可读，320px导航测试保留。
成员测试姓名本身是中文合成数据，切英文不翻译客户输入。长页截图中的固定栏位置不代表实时滚动行为已由静态图片证明。

## 本包：W07-B1 会话事务 fence

先写[具体设计与非目标](IDENTITY_SESSION_FENCE.md)，再在原生产 HTTP Handler 和真实 MySQL 上复现。
旧实现的6个 POST/PATCH × 注销/改密/到期用例全部 RED：新增仍返回201，修改仍返回200。
这不是网络波动或测试重连，也没有把网络错误算作拒绝。

实现：Require 传递不导出的 hash/subject 证明；账号创建/修改不再接受缺失证明的内部调用。
事务拿 identity_lock 后以 `FOR UPDATE` 当前读确认同一会话及 owner，锁等待完成后再判到期，随后继续原有权限/委派/最后管理员及审计。
注销统一 identity→session 锁序。原 Cookie 不入 DTO、日志、回执或持久任务，不添加生产测试 bypass。

验收增加：恰好到期、等待 identity 锁期间到期、session owner改变/变匿名、会话存储故障应503而非成功或认证拒绝；另一已有效设备/重新登录仍能正常管理。
最后管理员并发回归升级为两条真实 HTTP 会话先同时通过入口认证，结果必须一条200、一条完整401，且始终只有一名有效管理员。
Store 级旧测试仅在测试文件内准备真实持久会话并按 token 清理，没有放宽生产权限或最后管理员断言。

### 本地环境与边界

- 原 SSH 连接缺少可验证的主机 key；未忽略校验、未发送服务器凭据，亦未改测试服务器。
- 改用本机私有临时目录的 Oracle MySQL8.4.11 arm64 原生包，核官方[下载清单](https://dev.mysql.com/downloads/mysql/?version=8.4.11)摘要及二进制 codesign；无 Docker、无系统安装。
- 数据目录与 socket 位于0700目录，仅当前用户；TCP/X禁用，`skip_networking=1`、无 mysqlx 插件。未改钥匙串或系统 CA。
- 第一次控制面本地运行用了不满足原安全命名合同的基础库，随后采用 CI 的确切 `aster_ci_test` 回归；不修改生产 DSN 白名单。一次手动中断停止了私有进程，重启原数据后重跑，不重新初始化掩盖问题。
- 六例实际 RED→GREEN 已完成；补充会话归属、锁等待到期、存储错误和严格证明后，最终身份/控制面实库 race 为669 PASS事件、0FAIL/0SKIP。独立证明单测、两包vet、Linux静态controller构建通过。新 SHA CI 尚待运行，不把本地 macOS 结果套成 Linux 验收。
- 当前本地 MySQL 是 macOS，不代替 Linux CI、Ubuntu22财务链、真 KVM 或正式下载验收。

## 清单继续保留

W07-A记为同SHA CI已证；W07仍进行中。B1不包含用户revision、显式全部会话撤销、撤销前在途登录代次、身份操作不可变回执/未知结果锁和新安全UI，详见[下一切片](IDENTITY_PERMISSION_PLAN.md)。
W08资源交付、W09节点目录、W10实例操作、W11存储迁移、W12网络安全、W13运维灾备、W14商业规则、W15完整UI及W16正式发行继续按依赖推进，不把本轮修复记成整个清单完成。

## 连续实施：W07-B 账户修订、全部会话撤销和原操作核对

本批从 `b2c273fd9bf8a1c82e0daf51bae34d8ee5d0e4dd` 继续，不在一次局部修复后停工。
B1已取得该源码Cloud11门及财务4门SUCCESS：
[Cloud 40288999864](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40288999864)、
[财务 40288999878](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40288999878)。这些旧结果不替本批新增schema12背书。

### 接口、事务和升级

先写[身份操作合同](IDENTITY_OPERATIONS.md)，再增加独立迁移012，旧001–011字节不改。
revision使用JavaScript安全整数范围；打开编辑器先GET最新版本，陈旧PATCH完整409，不覆盖草稿。
新增显式全部会话撤销；用户变更、session删除、audit和唯一操作回执在同一事务内提交。
已用key不重放，不存密码或密码摘要；核对只读原actor/target/完整公开intent对应的receipt，404不是未执行证据。
撤销前已开始的登录也在锁内复核revision/hash；普通改名不会踢掉已登录设备。

迁移覆盖11→12、column-only/table-only/both未记账DDL重入、结构/索引漂移拒绝、历史账户/session/audit及旧迁移收据不变。
实际核对安装器默认数据库collation后，012显式固定新表utf8mb4_bin，额外验unicode_ci默认库，避免只在bin测试库通过。
另以真实Console.Handler＋TLS复现新增receipt路径漏转发为404，再修复顶层dispatcher；不是仅直接测identity.Handler。

### 前端与Gemini分工

审美与三份纯视图原稿由Gemini App生成：连续ledger表单、冲突/未知notice、会话撤销行及确认层。
生成前已选择4 Argon/高；App卡住经用户重启后显示高阶额度限制，未给Flash-Lite再发设计任务。
用户随后允许限额时自行继续，因此补接待核对列表、状态、主题token和可访问性工程，没有发送仓库源码、旧UI图片、凭据或真实资料。

原稿SHA256（本地ignored `aster-cloud/artifacts/gemini-20261026/`）：

- IdentityEditorFields：`4943c44207e1e59a9183b13a09deb18112fb3daeba4068fa17c3ecf4ea33fe96`
- IdentityOperationNotice：`44435d1d866c89f9d08a55e35bd6f5e0c465be8b77a9e0416f464500b73f39e8`
- IdentitySessionRevocation：`ebf2410dcb01b66670c5e9f237ee7480b2aaf11c213cbd699234cffcddeb7377`

必要工程修正：理由按Unicode码点截500、不拆emoji；useId保证同app多实例ID；固定角色权限只读；
基础按钮variant对应现有合同；深浅色映射现有Workbench语义token；不添加假进度或成功计时器。
UiOverlay增加共享层栈，只有最上层处理焦点/Escape/Back；关闭子层不解除父层inert/滚动锁，身份变化同时销毁两层时消除本层历史条目。
临时TextEdit在取回原稿后已关闭。

身份账本与VM账本分开：先持久化完整公开意图，再唯一发送；actor+target锁、创建email关联锁、1MiB完整恢复、无64条截断。
密码只在短期请求/表单内存，关闭/失去身份/提交结束清空；跨身份迟到结果只可清自己匹配的记录，不在新账户界面报成功。
旧通用账本的opaque未核实意图仍保留并阻止绕过；不凭新GET404、当前资料相同或VM receipt清理它们。

### 本地结果与本批仍待门槛

- 前端format/typecheck/build通过，42文件/994测试通过；3份Gemini视图已真实编译进生产入口，不是只收需求或演示页面。
- 新身份账本22定向项覆盖180条完整恢复、存储损坏/配额、换理由/动作锁、双击、丢ACK、404、回执不匹配、跨身份与本人撤销。
- 补齐委派/秘密/审计失败及顶层路由后，本地两包真实MySQL/race最终 **701 PASS事件、0FAIL/0SKIP**；vet、Linux amd64静态controller构建与独立浏览器夹具编译通过。新源码Linux完整门禁仍须同SHA CI。
- 新增独立实库浏览器夹具和25检查/10图的强制合同，沿用私有NSS localhost信任，不改用户系统CA。
  它另建测试库，只通过可见UI做有限5次身份写；原205成员只读hash、163业务/53布局/18成员及原70图门槛完全保留。
- 新浏览器**尚待实际执行与人工看图**；当前不能把组件通过或已编译当成视觉验收，也不复用b2c273f截图。
- 无财务源码/SQL改动；未付款、未执行宿主或VM命令、未新建标签/Release、未改测试服务器。

当前runtime仍有专用数据库级DML，所以回执是应用append-only，不是DB强制不可篡改。逐表最小授权与升级收敛留在W13，不能靠较窄表GRANT掩盖既有更宽授权。
本包不等于整个W07或全清单完成：MFA/step-up/SSO、财务员工权限组、W08资源交付及后续工作继续按计划推进；真KVM验收仍后置。

### 首轮同SHA CI 的实际失败与修复（bcfb364）

[Cloud 40333597266](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40333597266)的独立MySQL门在
`internal/networking/TestMySQLNetworkHTTPRealSessionsPermissionsAndOriginalIDLookup`登录时503。
原因是这个独立网络测试夹具刻意只加载schema1–5，未跟随身份服务补11/12；其建号也仍缺新必填reason/key。
本地此前701结果仅覆盖identity/control两包，因此不能代表整个Cloud实库门。没有把503算认证拒绝或通过。

修复只更新该测试的身份准备，保留独立网络schema5的原断言和所有授权/幂等/回收测试，不给生产代码添加兼容缺省。
随后扩大本机真实MySQL/race到全部`cmd/... internal/... migrations`：**2538 PASS事件、0FAIL**；
四项实际OVS/NAT native测试在macOS明确SKIP，另migrations包无测试文件。Linux对应四项仍必须由22/24独立native job实际运行。
未用本地跳过代替Linux检查。

同时通过实际Vue SFC编译复现三份原稿的`:global([data-theme="dark"]) .component`会丢弃后半选择器，
错误地落到整个主题根。改为全限定compound放入`:global(...)`，保留Gemini配色/布局且不污染全站；
新增三个编译后选择器负例，先3 RED后GREEN。完整前端现为42文件/**997项**，format/typecheck/build通过。
原稿哈希不变，工程修订另计。bcfb364的CI不记全绿，修复后须重新跑完整同SHA门和新实际浏览器/看图。

### 第二轮实际浏览器暴露的问题（9cc4b1b）

[Cloud 40336034618](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40336034618)中，独立控制器MySQL已通过；
[财务40336034760](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40336034760)四门全部成功。
原网络浏览器163业务/53布局/18成员检查、205成员只读快照和70张PNG仍通过。
新身份浏览器实际仅完成22/25检查、4/5浏览器写和8/10张图，失败阶段为`theme`，整轮**未通过**。
下载的原始ZIP经GitHub元数据大小与SHA256核对：
`edffbad58e42d03c09a4b2d9c4058bf186ffe7ce0db7e3e15ba16d4e2f75523b`。

人工查看实际桌面冲突、会话撤销和手机英文深色图，发现两处视觉缺口：冲突提示留在滚动区顶部却未进入视野；
撤销确认被Teleport移到body后，原父层的局部CSS变量无法继承，导致原因框边界与警示条失效。
关闭阶段另追到刷新时旧弹窗history标记残留；以App启动回归先复现，而不是删除Back等待或放宽断言。

修复只处理界面生命周期与反馈：

- 启动时仅移除无存活层的本应用`cloud-32hex`标记，保留router状态、URL、历史长度、原身份待核对字节；
  不自动Back，不删账本，不触碰其他应用标记，不重置正在打开的弹窗。
- 冲突/未知/阻塞结果聚焦并立即滚入当前活动层；嵌套确认不向inert父层抢焦点，也不重复播报两份notice。
- 撤销确认body自己声明相同语义token；保留Gemini原设计，不换成系统控件。

定向回归先4 RED，修复后81项GREEN；完整前端42文件/**1001项**、format/typecheck/build通过。
真实浏览器合同扩展为**28检查/10张实际PNG**，新增刷新标记、关键反馈可见和Teleport实际计算样式，
五次有限写/严格TLS/原回归全部保留。失败只导出固定阶段/步骤与布尔、数量，不泄露异常原文或DOM文本。
以上仍为新提交前本地结果；新同SHA CI、最后两张图及完整身份实库回执尚待完成，不预填成功。

### 1620cdc：实库闭环通过，但整轮仍有失败

[Cloud 40340166622](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40340166622)最终11/11成功，
包含22/24 native libvirt、OVS/NAT、独立MySQL、Vue和实库浏览器。
新增身份夹具实际完成28检查、10张PNG、5次浏览器写、5条提交回执；目标revision5、操作者revision2，
旧目标会话全部401，密码hash与全部资源/财务/节点数据不变。严格TLS、外域请求0，没有VM/宿主动作。
原70图和旧163/53/18检查全部保留。下载artifact核对完整SHA256：
`c9e5c6c0ab7f494d85248f8c3e12ea413b508e07ebbf6bf6f969cd65566e4a28`，85文件/80张PNG。

已人工看过本批10张身份图，发现桌面撤销图恰好捕获Teleport入场过渡，确认层还透明；
手机fullPage图也会画出实际视口之外、固定shade没有覆盖的背景。不能只凭检查布尔宣称截图都合格。
因此截图器补等真实过渡class消失且opacity=1，固定按实际视口截图，新合同为29检查/10图；仍待新SHA运行和重新人工看图。

[财务40340166525](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40340166525)仅3/4成功。
失败是原登录Retry-After断言：120秒显示成121秒。不是重试掩盖；先用逐毫秒递增时钟稳定复现121，
再让响应处理只读取一次observedAt，用同一时刻计算deadline/剩余量。原120秒和禁止再次提交断言保留，
未改变后端限流或15分钟兜底。财务前端format/typecheck/build、24文件1042项和Node35项通过。
财务金额、支付处理、幂等、历史快照和SQL无变化；新增改动仅为登录提示时间计算。

### W04：财务22/24原生链与必需发行检查

先写[W04合同](FINANCE_NATIVE_22_24.md)，再补独立22任务、Jammy四包pin、安全tar能力检查、
平台运行库与Nginx实际二进制包选择。原24四包pin字节不改，22官方四包逐个下载核对大小/SHA256，
不把下载校验说成PGP验证或原生安装完成。无URL/hash/manifest调用者覆盖，也没有不安全解压回退。
新fixture负例包括穿越/绝对路径、FIFO/设备和逃逸软硬链接，必须在任何数据库执行前拒绝。

新增Ubuntu22任务执行同一个完整check.sh、真实systemd/TLS/MySQL/插件/升级/备份恢复/回滚/清理链，
artifact独立命名，真实OS写报告。26仍拒绝。财务builder/publisher新增精确main-SHA五任务只读检查，
Cloud也要求该五任务；必需总门由15增至16。未发行0.5.10 prepare精确信任清单同步新控制文件，
0.5.0–0.5.9旧清单、标签和资产不改。

本机已通过：发行策略74、raw runtime26、更新器46、平台/pin12、Cloud发行28、浏览器Node合同14；
workflow actionlint、shell语法、Python编译与diff检查通过。
最初更新器测试遇到macOS `/var`符号路径与sandbox监听限制；改用真实`/private/tmp`和仅测试端口权限后46通过，
没有修改生产路径保护。native acceptance离线测试在macOS仍有Linux xattr/127.0.0.2/3不可用的3项错误，
不将它记通过、不改系统接口或删断言；须由Ubuntu22/24 CI实际运行。
原生完整安装链及新16门**仍待新提交同SHA结果**，目前没有发布、移动标签、改用户测试机或主机CA信任。

### e566f20：29项身份浏览器已实跑；财务两平台被旧测试清单挡住

[Cloud40344463671](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40344463671)最终11/11成功。
29项身份检查、10张身份图、有限5写及独立实库持久性回执全部成功，原70图/163业务/53布局/18成员检查保留。
下载ZIP大小/精确SHA/摘要与安全路径再次验证，SHA256为
`b152674724e4ef5fe9572f9412640b16d3c14d8cc798de38c96c3c184c6d9e2c`。
已重新人工查看全部10张身份图，桌面确认层已完整入场，手机图严格是320×760或390×844实际视口；
输入边界、警示条、未知结果与冲突反馈可见。不是只改检查布尔或复用旧图。

[财务40344463985](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40344463985)实际3/5成功；
22原生与24原`checks`都失败在同一旧测试`test_new_payment_policy_preserves_exact_published_executor_inventory`。
它仍把0.5.10新增信任集写死为原五文件，漏记本包新增的CI策略及两个pin控制文件。
只同步当前未发行候选的精确差集，保留0.5.2/0.5.3/0.5.9原集合及所有相等断言，定向负例修复后通过；
未修改生产校验或放宽历史规则来求绿。

本轮22已实际完成平台准备、独立MySQL启动及Go实库/race阶段，且两平台的data_filter能力、进程清理、
路径穿越/FIFO/设备/逃逸软硬链接负例均在日志中明确`ok`。但流程在Python合同阶段中止，
后面的完整native systemd/TLS/升级/恢复链**尚未运行**，绝不因为22任务已存在或MySQL能启动就记全链成功。
本机整组payment-native合同也因Linux ELF不可在Darwin执行而失败；只有上述精确信任差集定向测试本地通过。
