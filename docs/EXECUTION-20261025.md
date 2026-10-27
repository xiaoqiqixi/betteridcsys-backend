# 首批执行记录 · 2026-10-25

起始 main：`9858f1ce4be21c23741f747bc445468413a6951a`。
这是本批提交前的源码/本地验证记录，**不宣称整个功能表完成，也不以本地检查替代GitHub、真实MySQL、浏览器或真机验收**。

## 计划与范围

- [执行计划](EXECUTION_PLAN.md)：18个按依赖排列的工作包，每个先定状态/接口、写负例，再实现和验收。
- [逐项台账](execution-tracker.json)：269个唯一审查条目、562条验收要求，保留原代码观察/证据、下一步及所属工作包。
- 1625个完整功能树末级条目还包括字段/文案/安全约束，不当作已完成数。原规划图片与用户 `.DS_Store` 未纳入业务提交。

## 已实际修改

| 工作包 | 本批实现 | 验收尚欠 |
| --- | --- | --- |
| W01 | Cloud安装/依赖准备/TCG白名单/CI收敛Ubuntu22/24，26拒绝；不放松升级systemd等安全条件 | 新SHA22/24原生CI与正式制品安装；财务22完整原生链另列W04 |
| W02 | Core0.5.10候选；资源0.1.1/minCore0.5.10；严格来源和PREPARED收据升级回归；旧版本可信清单不变，新版绑定真实MySQL浏览器门禁源码 | Linux完整prepare/升级fixture、新Release、公开六资产核验与实际prepare；旧分叉资产仍不能新prepare |
| W05 | 通用账本完整原子恢复，移除64条截断/新增限制，用1MiB预算防资源滥用；其余网络账本空串/预算/写前payload与key校验，存储失败不发写请求 | 新SHA真实浏览器；不声称取消其他网络账本128条结构边界或提供无限容量 |
| W06 | 报表与购买币种说明分开、读取失败与写入未知分开、30种审计标题自然化并保留原code、11处关闭ARIA跟随语言 | 新SHA实际中英桌面/手机截图和人工复核 |
| W16切片 | 财务新真实MySQL浏览器runner，接独立只读CI和正式构建；Cloud发行强制新job通过，旧demo门禁不删除 | 首次Linux真实执行、20项UI断言/12截图、数据库历史核对、实际TLS负正证明 |

W16入口只接受owned原生MySQL Unix socket和真实datadir，新建随机独占测试库；Go overlay使用真实生产Handler、Secure Cookie和CSRF。
浏览器只登录/登出、读页面与设置本地偏好；合成待付款订单由隔离fixture准备，不付款/不开通。
所有非session表前后摘要一致才通过。Firefox私有profile先证明未知CA被拒绝，再导入限localhost测试CA；不改用户钥匙串、系统信任或绕过TLS。
仅固定schema报告和12张合成PNG可导出；凭据/原始日志/私钥/profile留在私有scratch并清理。

## 本地实测

| 范围 | 结果 | 限制 |
| --- | --- | --- |
| Cloud前端 | format/typecheck/build；39文件958 tests PASS | mock HTTP + jsdom，不是真实MySQL/浏览器 |
| 财务前端0.5.10 | format/typecheck/build；23文件1034 Vitest +35 Node PASS | 新浏览器脚本语法/import检查不是实际浏览器运行 |
| Cloud安装/打包/发行合同 | 19+111+25+28+19+43 = **245 tests PASS** | 离线模型；输出中的虚构release-id和下载状态不是真实发行 |
| 财务发行控制器/runtime | **93 tests PASS** | 网络/资产为合成fixture |
| 财务update-manager | **46 tests PASS**，实际回环HTTP部分已执行 | 默认macOS `/var`链接被保护路径检查拒绝，使用canonical `/private/tmp`重跑；沙箱阻止loopback后经许可执行，不放宽生产规则 |
| 真实MySQL浏览器runner边界 | **36 tests PASS**；显式Linux overlay交叉编译、bash/Node语法通过 | 实际MySQL、Firefox、Linux race尚未运行；Darwin入口明确拒绝并不创建产物 |
| 财务受跟踪Go | 20包Linux build/cross-vet、API/host静态ELF与LinuxRPC测试编译通过；11 portable包181 PASS事件 | Darwin全race未通过：3包Linux依赖setup失败，另6包平台排除；不是全量Linux/DB通过 |
| Workflow/来源 | actionlint1.7.7、diff检查；当前15项精确job合同 | CI未运行前不能写15/15通过 |

RED→GREEN证据：第96条未知意图恢复原失败；5个显式非法key原失败；overlay写前校验/坏存储和三种公网账本预算负例原失败，修复后通过。
完整并行测试暴露迁移测试未等WebCrypto完成，改为等待一次写边界，未增加点击、业务重试或放宽生产校验。

## 资金和历史保护

- 13个受跟踪SQL文件与起始源码逐字节一致；没有新增财务迁移。
- 未修改财务金额、支付与历史快照业务代码。版本与资源兼容声明不改变付款事实。
- 最初支付checkout/verify四种重放测试保持原内容、固定Unix连接与完整409要求；本机只编译，真正执行留Linux门禁。
- 旧标签/Release/附件和用户修改不覆盖。Core0.5.10、资源0.1.1均仍候选，不预填发行成功。

## 下一批与明确未验项

后续按依赖推进身份/权限 → 财务资源任务与服务投影 → 节点/镜像/目录 → 实例日常管理 → OVS感知备份迁移 → 网络/公网隔离 → 运维灾备 → 完整UI与最终发行。
Ubuntu22财务全原生链、正式Cloud下载、真实KVM/双宿主物理网络、容量/异机灾备等没有在本批解决，台账继续保留，不能用首批测试数量掩盖。

## 首次同提交CI与探针修正

首批已正常推送main `22e1bf8082c087b0094c9039b01051945441a9a5`，未打tag/发Release。
[财务CI 40183911099](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40183911099)的新实库浏览器job失败在`real-api-ui-acceptance`阶段；该记录不能写成通过，也没有可验收的财务截图。
静态复核确认探针清空storage后只有hash导航，Vue内存仍可能沿用上一角色的USD/English偏好；现增加真实reload，保留默认CNY/中文断言。初次脱敏摘要没有具体断言位置，因此不把该已确认缺陷武断认定为唯一失败原因。
另补固定枚举的阶段诊断，仅允许phase/role/size/locale标签，拒绝额外字段、超长、重复JSON键与符号链接；不输出原始错误、URL、凭据或页面内容。修正后的45项门禁边界测试及完整财务前端检查通过，仍须新提交真实重跑。

下一切片W07-A已按[身份权限设计](IDENTITY_PERMISSION_PLAN.md)开始开发；该设计关联完整思维导图101个身份相关叶子，首包只解决主控第101位之后成员的完整分页，不能冒称MFA/SSO/财务RBAC已经实现。

同一首轮财务`checks`在Python门禁发现第二处兼容测试失败：旧断言把Core0.5.3可信清单等同“当前最新清单”。新增0.5.10浏览器证明后应明确版本化，而不能改旧资产合同。现保留0.5.2 executor、0.5.3–0.5.9四个支付新增文件的精确集合断言，并单独核对0.5.10新增五个浏览器输入；未修改生产来源校验以绕过测试。针对性回归及runtime26项通过，待新SHA完整重跑。

首批 `22e1bf8` 的 [Cloud CI 40183911138](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40183911138) 实际 **11/11通过**，包括Ubuntu22/24 TCG真实APT/systemd/libvirt软件准备、无KVM拒绝，以及真实MySQL网络浏览器的163业务/53布局断言、64截图、0console error。TCG不是KVM，节点RPC仍为隔离替身；不算物理VM/网络验收。相同SHA财务实库Go阶段1510 PASS事件、0FAIL/0SKIP，后续Python旧清单断言失败保留上述事实。

第二提交 `e38cc0e7705f23371eb42023ae6df0bad7823680` 只推四个财务测试文件修复；不包含正在开发的主控分页。它没有命中Cloud push路径过滤，不能套用22e的11项结果称新SHA全部验收；下一Cloud切片必须重新跑最终提交。

## W07-A 候选与财务实库探针后续修正

W07-A 的接口/状态设计先落在身份计划，随后实现主控固定100条的完整keyset分页：严格游标、参数化查询、每页当前权限复核；前端成功才切页，保留加载/失败时已读行，身份失效立即清除，重试使用原请求页。新增schema11精确索引与DDL已提交但receipt未落时的重入处理，不改旧001–010 SQL。升级后的库不能直接换旧binary回滚，须使用配套备份。

本地最终Cloud前端40文件969项、format/typecheck/build通过；identity/control race与vet通过，但127个真实MySQL相关SKIP明确保留，不作为SQL验收。新增205人同微秒fixture、18项成员检查及6张图复用现有隔离真实MySQL/HTTPS浏览器链，原163业务、53布局及64张图不减。尚待本批最终SHA的真实执行。跨路由返回恢复页码/滚动仍移交W15，不冒充已完成；MFA/SSO/财务新权限组也不属于本切片。

财务第二轮失败停在USD选择：新库默认只有CNY，不能靠UPDATE不存在的USD行搭夹具。`befeb7d`改为真实Store配置独立USD=6.00，并核对默认CNY、原CNY金额及历史订单不变；没有改变生产币种默认或金额代码。第三轮已越过币种/审计检查，但停在读取错误注入，因此这两轮都没有可验收的12张财务截图。

本地真实TLS正反探针确认：Firefox私有NSS信任不覆盖Playwright的Node请求。本批仅向受控acceptance子进程提供本轮生成的localhost受限CA，外部继承的信任覆盖仍拒绝；负向TLS浏览器检查和主机信任不变。故障响应不再继承真实响应的旧Content-Length/Encoding，只替换为正确帧长的无效DTO；必须先收到真实后端200及data，再故障注入并验证恢复。四项读取错误断言保留，增加固定阶段和有界计数诊断，不输出凭据或原始异常。

这一修正的49项Python守卫、财务前端24文件1042 Vitest及35 Node、format/typecheck/build通过。它是测试基础设施修复，不是放宽生产HTTP/TLS、响应校验、支付协议或改写财务结果。最终GitHub同SHA全链仍须运行，不预填结果。

## e967719 实库浏览器结果与成员探针修正

`e967719aca83900762716bd407998331e7f99f50` 的[财务实库浏览器](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40187478010)实际通过20项检查，导出12张中英桌面/手机PNG；真实MySQL历史表摘要不变、业务写0、临时库删除及进程清理通过。TLS先拒绝未知CA再用私有profile信任，主机信任未改。原始ZIP核对GitHub SHA256通过；实际看图确认本轮面板和审计页，不能外推浅色主题或全站视觉完成。该run其他门禁当时仍进行中，不提前称4/4。

同SHA的[Cloud浏览器](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40187478036)在新增成员读取探针失败：浏览器原生Fetch Response的`status`数值属性被误写成Playwright式`status()`。生产分页未因此改动。原162项业务检查与53项布局检查已执行通过，最后第163项总错误检查位于成员模块后，尚未执行；本轮没有成员图片，不写成通过。

仅修复该属性读取，并把同一个自足浏览器回调置于独立realm、用真实原生Response做200/401/403及无效JSON回归。3项新用例先RED、修正后GREEN，Node8项+Python9项守卫通过；原18成员、163业务、53布局及70张图门槛不减。仍需新SHA重跑真实浏览器与全部门禁，不能靠单测代替。
