# 2026-10-27 执行续记

继承[26日记录](EXECUTION-20261026.md)；旧失败不改写为成功，只更新main，不覆盖标签或发行资产。

## 当前同SHA结果：eb1e81f

- [Cloud 40350833850](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40350833850)：11/11成功。
- [财务 40350833918](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40350833918)：4/5成功；合计15/16，不是全绿。
- Ubuntu24完整native systemd/TLS/业务/升级/备份恢复/回滚链成功，独立真实MySQL财务浏览器成功。
- Ubuntu22已完成平台准备、真实MySQL/race和前端回归，完整native链在edge fixture失败：
  `Nginx >=1.19.4 is required; TLS/SNI protections are not relaxed`。Jammy发行包1.18不支持现有安全SNI拒绝。
  这是真实兼容缺口，不记作网络波动，也不删除最低版本或`ssl_reject_handshake`保护。

## W04：固定官方Jammy Nginx包，保留安全保护

22改用官方stable `nginx_1.30.6-1~jammy_amd64.deb`，固定1,224,970字节与SHA256
`ddafa6d5decef787c7942e6c4a9c62cf881d7ea598ec42b9997ecbf298e1bde3`。
下载只允许HTTPS、限定时间和大小；在任何dpkg元数据读取、解包或执行之前验证完整大小/SHA256，随后核对Package/Architecture/Version。
24仍使用已认证APT包，不切源、不添加主机签名信任、不安装宿主Nginx服务或执行maintainer脚本。

实际审查不是只抄hash：验证官方签名Release → Packages大小/SHA256 → 固定.deb大小/SHA256，篡改Release负例拒绝。
使用私有临时OpenPGP.js6.3.2验证器，不加入项目依赖。密钥来自vendor HTTPS并固定全文件SHA，
不宣称独立Web of Trust认证；也不宣称每次CI下载都重新在线验证PGP，运行时使用已审查固定包pin。

| 审查材料 | SHA256 / 签名信息 |
| --- | --- |
| Release | `15cccb6e55ac93372b23db0b006191f0705cbf9324c7e1a2d48552e8a1d5ddd2` |
| Packages | `54126ae885067e3c05b746eb0f91dc3241ba01101db6f7fe98f38956d8425c68` |
| 官方签名密钥束 | `55385da31d198fa6a5012d40ae98ecb272a6c4e8fffffba94719ffd3e87de37a` |
| 实际签名key ID | `2fd21310b49f6b46`；密钥完整指纹`8540A6F18833A80E9C1653A42FD21310B49F6B46` |

原始渠道：[官方Ubuntu安装/密钥说明](https://nginx.org/en/linux_packages.html)、
[Jammy Release](https://nginx.org/packages/ubuntu/dists/jammy/Release)、
[Jammy包索引](https://nginx.org/packages/ubuntu/dists/jammy/nginx/binary-amd64/Packages)。
新包本机未提取或执行，必须由新SHA的Ubuntu22完整链验证，不能把包审查当安装验收。

## W07-B2：财务账户原会话事务fence

先写[边界合同](FINANCE_IDENTITY_SESSION_FENCE.md)。四个账户写动作在等待全局账户锁后，
必须持有原请求的session X锁并复核owner/expiry，随后再复核当前admin、target/revision、最后管理员。
HTTP从原Cookie派生私有context，不接受客户端新增授权字段，不以同owner另一个会话续命。
原会话已撤销/过期时返回完整401，不改目标、目标会话或成功审计；不向响应补发清Cookie以误删刚轮换的新会话。

额外在真实MySQL8.4.11独立临时库复现一个边界：
锁定读取语句开始有效、等session锁期间已过期，语句内UTC_TIMESTAMP仍是开始时间，旧predicate错误接受过期行。
修复为先取得session X，再用下一条语句的数据库UTC时间检查expiry；实际验证后者拒绝该行，临时库已删除，生产表未动。
依据：[MySQL日期/时间函数求值规则](https://dev.mysql.com/doc/refman/8.4/en/date-and-time-functions.html)。

新增回归覆盖：严格TLS四动作注销竞态；缺失/伪context、错actor/owner/hash、过期/删除/同owner替换、cancel；
真实MySQL通过performance_schema观察实际锁等待后注销、过期、轮换；session行等锁期间过期；
本人改密/撤销与logout并发不死锁。所有拒绝读取完整有界JSON，网络错误/截断/尾随垃圾/非JSON/超限响应不算通过。
保留原CSRF、分页、密码成本、登录发行竞态、最后管理员并发、审计失败整笔回滚及业务表不变断言。

### 提交前本机结果

- Linux/amd64完整billing测试包交叉编译、Linux目标`go vet ./cmd/... ./internal/...`通过；它们不是测试执行。
- 发行策略74、runtime26、更新器46、Cloud发行28、平台/pin13项通过；浏览器Node合同14项通过。
- workflow actionlint、shell语法、diff检查通过。
- Darwin不能执行Linux runtimeplugins，因此完整B2 HTTP/真实MySQL/race仍待新SHA的22/24 CI。
  未执行的旧HTTP回归不写成RED→GREEN；本机实际RED能力仅证明上述SQL时间primitive，不冒充完整HTTP链。
- 财务001–013、金额计算、支付、币种与历史快照均不改；没有部署、付款、VM或物理网络动作。

## SSH现状与仍可推进的工作

按用户要求自行处理首次连接：仅允许本轮私有known_hosts首次登记，后续固定key并拒绝变化，
不关闭主机校验、不删用户全局known_hosts，也不声称首次信任等于独立带外验证。
当前22端口TCP可达，但SSH在服务端banner/key交换之前主动关闭，尚未收到主机公钥、未进入密码认证、未发送密码。
私有known_hosts仍为空，不能称已连接或已部署；不反复尝试口令。Linux GitHub CI仍可继续，本项不阻断代码与隔离测试。

下一步：普通main提交后跑新同SHA全部16门，逐一核对B2和Ubuntu22完整native结果；继续W07-C/D与其余工作包。
员工权限组、MFA、所有财务写的统一fence、数据库强制append-only、正式0.5.10/Cloud下载通道、真KVM/物理数据面仍未记完成。

### 3b93252调度核对发现的来源证据缺口

本轮普通main推送已触发财务与独立performance，但Cloud旧路径过滤排除了纯财务/文档变更，因此该SHA缺少Cloud push run。
发行器严格只认同SHA main push，这时手动dispatch不能补位；不能拿eb1e81f的11门拼成新SHA的16门。
新增调度合同先1 RED确认paths过滤仍在，再删除Cloud的paths过滤，保留main分支、全部11任务、只读权限与原cancel/严格来源策略。
今后两个必需工作流每次main push完整调度；这是补齐证据，不是放宽验收、伪造commit触发或更换事件条件。
Cloud发行合同由28增至29项；当前发行文档同步为Cloud11+财务5，不再一处写15、一处写16。
3b93252尚未取得全套新门，接下来的完整结果只认包含调度修复的新精确SHA。

### 942f62e：主控11门通过；22原生进入真实插件后暴露systemctl兼容问题

[Cloud40424839408](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40424839408)最终11/11成功。
新版身份仍实际29检查/5浏览器写/5提交回执/10张身份图；旧70图和原网络/成员回归保留。
完整浏览器ZIP为8,728,665字节，SHA256
`687e573d280239f69843596914ff578003a5e657376e2bac0ebb0d2830a6fcb0`，精确85文件/80PNG白名单核验通过。
按既有Node合同重新核对全部身份JSON和10张实际PNG；本次人工复看桌面撤销与手机本人撤销两张，布局和警示均正常。
先前e566f20的全部10图人工审查仍有边界保留，两提交间Cloud前端代码无变化，不把本次两图说成新一轮全站视觉验收。
本地摘要读取时曾误把`foreign_requests:0`当数组而断言失败，随后使用原有正式报告验证器通过；没有修改产物或放宽合同。

[财务40424839385](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40424839385)中，24的完整checks/native链、
Go漏洞及官方metadata成功；22同一个完整check.sh已成功，新增严格TLS/完整响应、所有B2会话与真实MySQL锁等待/race用例明确PASS。
22随后实际通过新Nginx/TLS/edge-observability与SMTP安装、升级、回滚、双UID/FD/完整性前七项检查，
但在SMTP DATA之后的崩溃注入处失败：`systemctl kill`实际退出1，不能算作已杀死或已恢复。
后续合成支付native与Core升级/恢复链未到达，不能记22完整通过。
失败产物10,062字节，SHA256
`82c46ff3f188a704daf75d9b2708f86e40d5a8b8d529169042c03ce13d7abcf9`；父层清理回执确认本轮资源已清理。
独立[HTTP performance40424577672](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40424577672)在3b93252成功，
它不是942f62e新增的同SHA性能结果，更不能补位任何必需门。

审查官方参数表：systemd249只有`--kill-who`，255为`--kill-whom`。
旧测试固定新拼写，不适用于Jammy；另全库找到合成支付崩溃注入和父层强制清理两处相同问题，一并修复。
依据：[systemd249源码](https://github.com/systemd/systemd/blob/v249/src/systemctl/systemctl.c)、
[systemd255源码](https://github.com/systemd/systemd/blob/v255/src/systemctl/systemctl.c)。
修复在注入前只读实际help，选择已明确列出的完整参数；只发送一次相同KILL、同一个owned main目标，
缺能力或kill非零仍失败，不先试错发信号、不重试、不删除崩溃/无自动重投/新PID/Core不变断言。
父层fallback清理也先选择能力，但仍只处理原已创建单元；不能将选项兼容变成任意宿主kill。
两个子报告另修掉遗漏的Ubuntu24硬编码，改读真实受支持os-release，并记录实际选择的kill参数。

本机新增能力/目标/报告负例、SMTP/TLS与守卫共14项通过，父层清理选择1项、合成支付继承同一守卫1项通过；
实际systemd kill与22后续原生链仍须新SHA Linux执行，不把mock断言算真崩溃验收。

同时将锁内到期fixture收紧：先观察原session真的等锁，锁持有夹具再设短DB期限，等数据库实际超过期限后释放；
去掉HTTP认证必须抢在固定4秒内完成的脆弱前提，不重试HTTP、不改401断言。
该确定性时间primitive在实际隔离MySQL再次复现旧predicate错误接受、锁后新语句拒绝，临时库已删除；
新完整Go包交叉编译/vet通过，但这份调整后的HTTP用例仍待新Linux运行。

当前0.5.10发布正文改写为“两平台同源码完整native检查是放行前提”，避免一面强制22门、一面发行正文永远写22未验。
doctor只报告22/24属于支持目标、24为既有基线，始终`this_host_validated=false`且明确自身不查询CI；
目标状态不再写成永恒pending，也不暗示当前失败CI已过或安装器放宽。对应回归通过。
后续[员工权限组详细合同](FINANCE_STAFF_ACCESS.md)仅为设计，明确下一独立0.5.11/schema14；本次未实现、未迁移、未发布。

财务浏览器最后因40分钟job期限取消，整轮实际为Cloud11成功、财务3成功/1失败/1取消，合计**14/16成功**，不是15/16或全绿。
实际日志显示不是UI断言失败：准备阶段从原有Ubuntu Azure签名源下载61.2MB CJK字体约耗22分钟，
随后Playwright系统依赖继续慢速下载，在真正Strict TLS/MySQL浏览器步骤开始前被取消，无本轮财务浏览器截图/回执可计通过。
没有删字体、跳过浏览器、拿demo补位或把旧SHA财务浏览器结果拼上。新提交修复明确的CLI兼容缺口后仍须完整重跑；
如果下载再次失败，仍保留真实失败，不将换runner当功能修复证据。
该GitHub一次性runner安装运行库时也因libudev依赖更新了同系列systemd255安全补丁；它不是未维护原镜像直接安装的证明，
不改变生产安装器的系统升级边界，更不能替用户测试服务器或新物理主机背书。

## 128a86d：财务22/24完整通过；主控宿主保护仍拦截，未发行

源码`128a86d091d806c39b9c33c8a945c0fc20b19f06`：

- [财务40430011285](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40430011285)：**5/5成功**。
  两平台均完成全部Go/MySQL/race、前端、安全合同、systemd/TLS、插件崩溃/恢复、Core安装/升级/备份恢复/回滚与清理；
  demo浏览器与独立真实MySQL财务浏览器也通过，没有拿942f62e超时结果补位。
- 22实际选择`--kill-who`，24选择`--kill-whom`；两平台SMTP报告各9检查、失败0/跳过0；
  合成支付真实HTTPS/MySQL结算回归、Core PID不变与清理退出0均有实际回执，不是仅mock命令。
- 已下载两份native ZIP并核对完整大小/SHA256/ZIP安全边界后读取：
  22为23,189字节、`2e53475f2dad8babf7f850eebd650c0c52263dfeb4fd3fcc43f6426967262716`；
  24为23,062字节、`1aeab9467163709a4c9fa67388d6672b9192ac174f86125db6638c8630697768`。
  证据保留在ignored的`vps-billing/artifacts/execution-20261027/128a86d/`，无生产凭据。
- [独立性能40430011350](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40430011350)也成功；
  仍与原生/浏览器/漏洞扫描分开，不充当其中任意任务。
- [Cloud40430011168](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40430011168)：**10/11成功**，实际MySQL与浏览器成功；
  24来宾完成实际APT/systemd/libvirt、签名镜像和明确无KVM检查，但宿主前后`addresses.length`变化，路由和防火墙hash不变。
  父层因此返回`HOST_NETWORK_OR_FIREWALL_CHANGED`，整轮仍是**15/16**，不把来宾成功算整项通过。

该Cloud失败ZIP为2,380字节、SHA256
`797d11836f289de367dfd11d42fb215126ff443591a70bde665dee812120b6d8`，完整核验后读取。
当前旧产物只有接口数量差异路径，没有接口结构或发生阶段，不能推断是Docker、DHCP、QEMU或安装器导致，**根因未归属**。
原保护维持失败，不添加忽略Docker/veth的白名单、不截断原快照、不移动baseline、不改成warning或重试求绿。

新增两项有界定位：原最初baseline保持，在VM启动前再次完整核对，结束后的核对保留；
两者任一变化都失败。诊断只输出接口数量、ifindex、固定名称类别、link type、地址族数量，不输出名称/MAC/IP。
至多32条诊断详情明确标出截断和真实总数，65个接口/仅第65条改变的回归证明实际比较和hash仍覆盖完整快照。
本机47项libvirt合同通过；独立workflow另要求两个检查点都真实通过。**新增定位不等于此宿主变动已经修复**，仍须新SHA实际检查。

W04与W07-B2可以按128a86d的各自实际范围记`ci_verified`；这不是用户服务器/公网/真实支付/KVM或整个功能清单完成。
Core0.5.10和Cloud RC仍未创建新标签或Release，正式六资产下载/prepare验收未完成。
安装与版本文档改用明确的发行放行条件，避免版本化安装包永远写“未发布”；同时纠正Python>=3.12的过时说法，
实际要求仍为安全tar能力且不放宽filter。此类文档更新不把候选提前宣布为已交付。

## dab0167：启动前的宿主变化已定位到阶段，进程归属仍待证

已确认远端main为`dab0167d58cd99a19fc4a0eafff23bbe7ff95cf0`，没有增加分支或创建标签。
[Cloud40435816788](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40435816788)的22任务121964114939在VM启动前拒绝。
脱敏ZIP完整校验：1,550字节，SHA256 `36363a015dbe14f3bfeba8239e1c80137e345d5c864a24d0a55c4f1b7a4d4aec`。
原始baseline为3个接口，pre-VM及最终均4个；新增ifindex4、ether、无IPv4/IPv6地址且有master。
路由及防火墙hash相同、progress空、guest_result_seen=false、qemu_process_verified=false；没有运行本轮来宾安装。
这只定位变化早于VM，不证明Docker或其他服务归属，更不能把此次失败当22安装回归通过。

确认并复现另一夹具缺陷：`finally`的网络失败覆盖先发的`HOST_NETWORK_CHANGED_BEFORE_VM`或来宾失败，
导致最终报告失去第一处实际原因。新增直接调用真实execute控制流的回归先RED：两项错误码被覆盖；
修复后保留先发失败且最终核对照常运行，末次变化/采集不可用仍不能成功，临时变化后来恢复也不救回先发失败。
原快照已拒绝后才追加独立详细link诊断，输出固定kind/master类别，不输出实际名称/地址/任意linkinfo内容，不忽略任何接口。
详细link内动态timer不引入原准入数据，诊断失败也不放行。50项libvirt合同与29项Cloud发行合同本机通过；
这不是新的Linux VM运行或宿主变化根因已修复。

测试服务器已再次由工具执行SSH只读握手，仍在密钥交换前对端关闭；无密码发送、private known_hosts为空。
首次信任记录仅在本轮私有文件使用accept-new，公钥一旦存在必须精确匹配，不关闭主机校验，也不冒称已独立验证指纹。
Mac仍锁屏，Gemini不可操作；没有发送源码、旧UI图或服务器资料，继续后端/测试而不是停下等桌面。

为补齐W02发布后的真正公开资产prepare，新增独立22/24只读工作流与`public-core-prepare.py`：
固定Core0.5.10/审查source，六资产用无凭据生产client下载，调用原正式prepare和完整prepared_release只读复核，
保护路径/文件/来源/13迁移验证不替换，只使用本轮root私密scratch并清理；不安装、不启动、不动DB。
13项离线负例/接线测试通过，包括资产缺失/重复/坏字节、错误source、假prepare、完整目录验证拒绝、错收据、变化的Release、
SQL数量、非Linux与坏token；**尚未执行真实新公开版本**，详见[验收合同](CORE_PUBLIC_PREPARE_ACCEPTANCE.md)。
财务发行74项、runtime26项回归按项目正常Python测试入口通过；首次误用`-I`导致同目录测试模块不可导入，
已按现有check.sh的发现/正常入口重跑，不因此修改测试或生产导入。全部workflow actionlint及diff检查通过。

dab0167最终[财务40435816730](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40435816730)为5/5成功，
两平台完整native链及独立实库浏览器均通过；Cloud最终10/11成功，仍15/16总门，22启动前宿主变化不改判。
新下载native ZIP并完整校验34文件清单、大小和hash，与前一轮受控清单一致：
24为23,041字节、`86a73a9a46eee2934f307342c2fc2d48d210ac8acd72395530ee9f613a424172`；
22为23,121字节、`065cf7b1a1f3797ab5cae4a4a92e4a3040ab77c8a72f585730ca098ce32d5d1b`。
实际SMTP/合成支付failures为0、新安装succeeded、owned资源清理为true；ignored归档保留，不把日志/秘密提交仓库。

以真实GitHub只读接口运行原生产`green_financial_ci`，确认精确dab0167、run40435816730/attempt1的五门均成功；
复核版本/标签/Release未占用后，按既有发布授权新建`v0.5.10`指向该源码。
正式[Runtime build40439181617](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40439181617)已实际运行，尚未完成，
不能称已发布。Cloud标签与Release仍未创建。本轮新增CI入口/宿主诊断不改40个财务构建/发布信任blob，
不改财务运行代码、SQL、金额、支付或历史快照；后续main本身仍须新的完整CI，不能借dab0167给新源码背书。

## W07-C：等待正式构建期间补实测设计边界，未激活新角色

重新读取当前Store/HTTP/原始SQL后，发现员工授权seal若按“启用关系”筛选，零组会话在获权后移除、
停用组会话在启用后再停用两种路径都可重新匹配旧seal。独立Go可运行模型已真实复现，并验证纳入完整tombstone关系可阻止；
205组末项、排序、改名、未知/重复权限也通过，详细合同已修正，不用全站统一登出掩盖相关对象版本缺失。

同一已确认存活的本机MySQL8.4.11/socket-only执行新建专属空库的DDL原型，三旧表/四新表结构与FK、ENUM尾部追加、
旧列/行/会话不变、关系revision保留已实测。新增精准负例先发现尾空格权限与非hex seal会被候选约束接受，
改进候选后14类SQL拒绝与七项合法权限/合法seal正向均通过，测试库全部删除。
没有把本机IPC沙箱的首次访问拒绝误判为数据库停止；在同一socket提权只读确认存活，没有另起实例或重启。
原型源、RED/GREEN JSON与DDL留在ignored artifacts，生产001–013和schema13/版本不动。
完整staff功能、27表预检、Linux实库并发/浏览器仍待正式实现，W07-C不是已完成；见[合同与原型范围](FINANCE_STAFF_ACCESS.md)。

## Core0.5.10正式交付与b132123完整16门

2026-10-27，源码`dab0167d58cd99a19fc4a0eafff23bbe7ff95cf0`的Runtime build40439181617、独立发布器40441128635均实际成功。
新正常Release `424987299` / `v0.5.10`已公开，目标为该完整source，不是后续控制器main。无旧标签/资产覆盖。
六资产均实际匿名完整下载，大小/hash、BUILD、PROVENANCE、源码与13迁移经未修改生产内存验证通过。
独立公开prepare40441393371在Ubuntu22/24 **2/2成功**，逐平台真实prepare/202发行文件及本地原始收据复核、清理成功；
程序执行/安装false，服务/数据库动作none。两份966字节ZIP完整hash及全部六资产摘要见[公开验收记录](CORE_PUBLIC_PREPARE_ACCEPTANCE.md)。
这仍不是用户机安装、真实旧分叉目录升级、宝塔、公共CA续期或全系统生产验收，不将W02局部闭环当整套目标完成。

随后main `b132123cc9bbc87bdcc2548db3bf8665460c40c5`取得财务40439673220 **5/5**、Cloud40439673255 **11/11**，
首次同一新源码全部16门成功。Cloud22/24签名来宾均完成实际APT/systemd/libvirt；两检查点宿主完整快照均不变，KVM仍false。
各ZIP完整验证：22为2,373字节、`45ae29ae1e15598aa535052cb1a6928b16db08c93ff56c1c744320de8c6a55ae`；
24为2,382字节、`65342ffddeb4d55f80c7da0290a3db346821fd3b6f88a5a257cd9ae9009eb5d5`。
这不将DAB启动前接口变动改判通过，也没有证明其进程归属；本轮是严格守卫真正未发生差异的新记录，不忽略接口或重置baseline。

实际管理台浏览器ZIP为8,711,607字节、`6a083e800c32d57aed4379db880995837111fcec59007ef52adf2741cefe77ea`，
完整核对85文件名单/hash后提取身份10 PNG/2 JSON；原Node独立validator实际通过29检查、5写、5条MySQL提交回执。
本轮人工复看1440桌面撤销及320手机编辑两图，无可见溢出/遮挡；不据截图推断键盘、性能或全站WCAG通过。
生产frontend src/package/lock/vite/index自e566f20完整10图人工审查后逐字节相同；这不冒称本轮人工看完10图或完成W15。
浏览器任务40分钟预算内实际进入Strict TLS/MySQL并完成，未删字体、换演示数据或延长现有超时。

发现root README、更新/宝塔/升级入口仍留有构建前的固定未发布状态；已改main为版本合同/条件及日期化实际记录，
旧资源文档仍保留源码快照，不能覆盖标签或重写已发布hash来纠正文案。新Release仅追加发布后复验及快照说明，
写后核对原正文前缀、source/tag及六资产ID/大小/digest均未变。新回归用真实b132123旧README先复现RED、再GREEN，
同时守住安装入口及Core版本声明；旧日期段落保留当时失败，不篡改成历史全绿。
本轮文档与新回归仍须其自身新main完整CI，不能把b13212316门直接用于后续Cloud发布。

入口排查扩展到更新/升级/宝塔、变更与验证记录及历史调整文档，当前措辞均改为条件和日期化实际证据；
新5项README/入口回归对真实旧b132123四入口复现RED后GREEN，197项本地定向检查、actionlint、JSON及diff检查通过。
历史日期段落保留原当时状态。当前远端只有main，GitHub公开contributors API仅xiaoqiqixi，无Codex；不以本地log代替该结果。

## 文档入口重整：先能安装和使用，再读工程证据

按用户给出的 [ZJMF-CBAP README](https://github.com/idcsmart/ZJMF-CBAP) 实际核对了能力介绍、安装入口、开发文档导航的组织方式。
链接的旧安装手册与插件手册本次未能通过浏览工具读取，不声称已经读完其全部文档；未下载或执行第三方安装脚本，未复制源码、素材或产品能力声明。

- 根README与两个子项目首页改为产品说明、实际能力、安装/使用和开发导航；历史作者调整保留简短升级提醒并链接原说明，SHA/计数/调试过程后置到验证记录。
- 新增`docs/README.md`按首次使用、财务运营、主控运维、二次开发、版本验收分流；不新增文档框架或运行依赖。
- 新增财务`GETTING_STARTED.md`：实际hash路由、安装邮箱/私密密码文件、站点与CNY、分类/草稿/复制/报价/上架、客户确认购买与待付账单、日常管理及异常处理。
- 新增主控`GETTING_STARTED.md`：独立账户/数据库、各角色安装结果、节点审批与维护、实例意图/派发/核对、网络/IPAM/公网、财务适配与恢复；不给未实现功能编造操作步骤。
- 以实际源码核对入口、首管理员字段和12份主控迁移。修正README、架构、KVM对照中残留的“当前schema11”；手工运维与安装向导能力分开，011分页增量仍作为历史保留。
- 修正资源/产品/插件当前说明中的Core0.5.10未发布措辞；明确Core正式发行与worker0.1.1独立候选是两件事，后者不在Core六资产中。历史日期记录保留当时事实，Cloud验证首页增加b132123精确16门证明，不借给本轮新源码。

文档回归从5项扩为11项：入口可达、仓库内链接存在、真实路由/引导字段一致、当前schema与迁移序列一致、资源兼容措辞和原发行快照约束。
先在未修改文档的329b168工作区执行新增测试，实际RED为14个子项失败/3个缺文件错误；补齐文档后11项全部GREEN。
检查过的377个本地Markdown路径均存在（此次后续记录链接增量另核对），不把路径存在当作界面/操作功能测试。

本机定向回归共214项PASS：公开Core准备13、原生libvirt安全50、Cloud发行29、财务发布74、运行时发行26、文档11、manifest11；
actionlint、Python AST、台账JSON与diff检查通过。均为本机合同/模型检查，不是重新执行真实MySQL、Ubuntu安装或全部浏览器验收。
Cloud329b168的40445796442已实际11/11成功；读取时同SHA财务40445796578尚有checks运行，未记录为16门全绿。
本批只改文档与文档测试，不改SQL、金额、支付、历史快照、运行时代码或UI，也未创建/覆盖任何旧标签、Release或资产。
新main提交仍须其自身完整CI；文档优化不将W07-C员工权限或W08–W17目标标记完成。

## 首个Cloud RC实际公开，继续W07-C与四组合复验

8092c3c最终取得财务40448316285 **5/5**、Cloud40448316272 **11/11**；观察器仅查询同一运行，未因观察超时重启CI。
按原生产`green_checks`经只读真实API再次确认16门、main和标签/Release不存在后，冻结该源码，创建新注解标签
`cloud-v0.1.0-rc.1`（对象400333ef9886773cf414f3fb0763500cb5253c5d，tagger仓库所有者），未移动旧标签。
真实双构建/独立签名40450543793 **2/2成功**，独立发布/匿名验收40451094771 **3/3成功**；
新预发布Release425041579于04:41:38Z公开，九资产已实际完整匿名下载，原生产数据验证器核对全部清单/源码/静态ELF通过。
本机不是Linux root验签/plan；真实Sigstore/认证后生产plan来自该发布器两个Ubuntu24角色任务。

两份实际回执ZIP分别446/445字节，完整hash、artifact ID、唯一JSON/CRC、严格13字段及角色包摘要均已重新下载核对；
全部旧九Release的关键字段/正文hash/全部资产和财务latest与发布前一致。源码、九摘要与限制见[公开验收](CLOUD_PUBLIC_ACCEPTANCE.md)。
检查实际包后发现源码相对文档链接与解压布局不完全匹配；不重打资产，新Release正文仅追加固定标签在线文档入口，写后再核对标签/源/九资产和财务latest未变。
下载通道已实际存在，但不是稳定生产版、整机安装或KVM/物理网络验收；后续包的离线文档布局仍须修复。

为不把Ubuntu24的公开plan当作Ubuntu22结果，本轮新增只读手动`cloud-public-prepare.yml`：22/24×主控/被控四组合，
使用真实无token bootstrap、Sigstore与生产plan，不传apply/start，也不安装系统依赖；区别固定已发行source8092c3c与后续验收控制源码。
独立checker绑定Release425041579、tag、源码、两包摘要及精确bool/13字段，并核对实际宿主版本。
先对未实现入口RED，补齐后6个离线顶级测试通过。另用OS边界模型复现完整stat比较误把读取atime更新当内容变更，
修为只忽略atime、继续锁定dev/inode/owner/mode/nlink/size/mtime/ctime后GREEN；不是忽略文件内容或权限漂移。
本段编写时四组合门尚未启动，须新main实际运行；原生软件准备不替代它。

W07-C新`internal/staffaccess`只实现完整不可变授权快照：七项精确权限、停用关系历史、205组不截断、用户绑定、
返回切片不可篡改、缺失/坏seal与坏策略整笔拒绝。13顶级/52 PASS事件、0 FAIL/SKIP，race/vet和10轮race通过；
四个隔离错误变体均触发真实断言失败；Linux/amd64测试ELF只交叉编译未执行。
会话/SQL/014迁移、API、PII投影和UI尚未接入，当前staff角色不开放；详见[实施合同](FINANCE_STAFF_ACCESS.md)。

本机本批395项定向Python回归全部通过，actionlint、Python AST、JSON、187条本地文档路径及diff检查通过；
原269项台账items逐字段未变，只更新W03证据、W07-C范围和分系统交付状态，不把全计划变完成。
这些新代码不能继承8092c3c的16门；新main完整CI、四组合真下载和后续W07-C闭环继续推进。

### 2020855：四组合公开复验已实跑成功，完整CI仍分别记账

main已推送20208555d95d72bd3f97f8b5efa2776083946696；公开四组合40454038287实际4/4成功，
Ubuntu22/24各自运行主控/被控的真实匿名下载、Sigstore复验及认证后生产plan。控制source2020855与发行source8092c3c严格分开。
四份992–996字节ZIP已全部重新下载，完整hash/双JSON/CRC、原运行与控制source、actual platform及全部根回执字段复核通过；
host_apply、service_started、real_kvm_acceptance均false，详见[公开验收续记](CLOUD_PUBLIC_ACCEPTANCE.md)。
原始ZIP/报告保存在ignored的cloud-public/matrix-2020855。读取时同源完整财务40453853089、Cloud40453853002仍在运行，未预填16门通过。
后续继续staff数据库/会话/读取/界面闭环；不把纯授权模型或公开plan充当生产资源验收。

随后该2020855源码财务40453853089 **5/5**、Cloud40453853002 **11/11**及独立性能40453853030全部成功；
未修改的生产green_checks再读实际API确认16项的名称、event/main/source/attempt与全部success，不拼接旧源码。
台账与公开验收续记据此更新；下一源码仍需自身CI，staff功能未开放、离线文档布局与其余W07/W08–W17继续实施。
