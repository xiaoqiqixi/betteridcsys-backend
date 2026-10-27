# W07-C：财务员工权限组实施合同

状态：实施中，授权快照域模型已在本机实现；尚未接入登录、数据库、HTTP或UI，不是现有员工功能声明。以当前main/schema13为审查基点；W07-B2账户原会话边界和W04平台门已另有精确源码证据。
需要新SQL的本包拟进入**下一独立Core0.5.11候选/schema14**，切换时一次同步API版本、前端、默认安装版本和发行说明。
当前0.5.10/schema13无新增SQL合同不暗改；旧001–013不重写，旧标签/资产不重发。本文件不表示0.5.11已存在或可下载。

## 1. 权限边界与首包范围

现有admin/customer保持原含义；新增staff，不把staff当半个admin，也不把`/admin/`的uid置空逻辑直接开放给它。
管理员仍管理全域业务，但不能越过支付状态机、审计、来源或金额快照约束。

首包staff只读。白名单拟为：

| 权限 | 确定数据域 | 不包含 |
|---|---|---|
| customers.read_global | 全站客户ID、姓名、邮箱、启用状态 | 密码hash、会话、密钥、SMTP配置 |
| orders.read_global | 全站订单清单/单笔详情和历史商品/金额快照 | 改价、改状态、取消、下单、开通或支付 |
| invoices.read_global | 全站账单清单/详情及已记录付款结果 | 付款、退款、余额调整、回调重放、收银台令牌 |
| services.read_global | 全站已记录服务/交付状态 | 创建/控制VM、同步或重发任务、主控凭据 |
| catalog.read_global | 商品/销售分类/可售状态/已配置独立报价 | 编辑规格、改价、上下架、私密交付配置或主控凭据 |
| reports.read_global | 按获准币种读取已有汇总 | 自动换汇、修改精度/账务、创建账本或导出未授权明细 |
| audit.read_global | 脱敏业务审计及原有只读筛选 | 原始宿主日志、请求体、密码、Cookie/DSN |

无`*`、权限名通配或隐式包含。无对应权限的请求服务端403，不能靠隐藏导航授权。
customers.read_global仅查询当前customer账号，不开放管理员/员工目录或其凭据；管理员管理成员仍走独立admin接口。
订单/账单的客户邮箱与姓名只在同时具备customers.read_global、且关联账号属于客户域时投影；否则仅返回owner ID，不能先全取再前端藏列。
历史账单中的收件/联系人快照也受相同投影约束；不能通过嵌套snapshot、关联任务或审计详情漏出被隐藏的个人资料。
员工审计采用独立显式字段投影，不返回任意旧`Audit.Detail`或其自由文本reason；不能靠正则从自由文本猜测并清除个人资料。
ActorEmail只允许在具备customers.read_global且关联actor确属customer时投影，不由该权限开放admin/staff目录。
没有该权限时，订单/账单关联账户仅保留owner ID，不加载完整User再交给前端隐藏；详情中的关联审计/支付记录也遵守同样边界。
权限也不沿业务关联隐式扩张：订单详情没有invoices.read_global时只给关联账单ID，不嵌入完整账单；反向同理。
reports.read_global只给汇总，无orders.read_global时不附“最近订单”；customers.read_global不默认返回该客户的订单/服务明细。
审计事件不嵌入原始请求、产品私密配置、未获授权的关联对象或自由文本，以免绕过这些独立数据域。
首包没有按客户分派的数据域，UI必须明确“全站只读”，不得让操作者误以为只授某客户。
staff不得借客户端接口创建/取消订单、付款、改账户或获取管理设置；公开商品目录仍可匿名浏览。
授权数据域首包只有`all_customers`，不接受`assigned_customers`或浏览器自报owner；权限ID中的global也是防误授权提示。
可给“财务只读”“客户支持只读”预填组合，但它们只是显式权限的模板，不是新角色或隐含audit授权；默认不勾选全部。

## 2. 表结构与迁移检查

- `users.role`只在原`ENUM('customer','admin')`尾部追加staff，不重排旧成员；既有用户行/ID/密码hash/启停/revision保持原值。
- 暂拟迁移`014_staff_permission_groups.sql`；实施前确认号码空闲。新四表为`staff_permission_groups`、`staff_permission_grants`、
  `staff_group_memberships`和`staff_access_operations`，预计schema14/27表；不是当前已存在结构。
- 权限组：id、name、enabled、revision、policy_revision、明确权限集合、创建/修改时间。
  普通revision保护所有编辑；policy_revision仅在权限/启停变化时推进，改组名不踢出员工。
- 成员关系：staff user ID、group ID、enabled、独立关系revision；联合唯一键和两向检索索引，数据库FK必须匹配既有CHAR(32)定义。
  移除是保留关系的停用并推进revision，重新加入再推进，不删行重建为revision1；否则权限恢复会让旧seal复活。
  退出staff角色时原关系全部原子停用并审计，不留下日后重新成为staff即可继承的授权。
- sessions增加仅staff使用的非秘密授权seal；已有admin/customer行语义不变，NULL旧seal永远不能授权staff。
- seal使用64位小写hex、ASCII精确比较，DB也检查长度/小写及可UNHEX；应用认证读取仍作完整格式验证，不能把任意64字符当有效seal。
- grant列除ASCII精确白名单，还拒绝首尾空格。仅`permission IN (...)`可能因MySQL PAD SPACE接受尾空格别名，不能以该条件代替完整格式检查。
- 身份操作回执：原actor、操作ID、动作、target、base/result revision、完整公开intent、理由、关联audit ID、完成时间。
  无密码/密码摘要/会话token；首包只提供SELECT核对与一次事务INSERT，不提供更新/删除接口。
- 逐对象精确检查enum/列/索引/约束与记录校验和；支持正确的未记账部分DDL重入，错结构/漂移拒绝，不自动DROP重建。
- 新二进制严格检查schema14；旧schema13程序不得承诺可直接回滚。先停写备份，回退程序/数据库须匹配可验证的恢复点。
  灾备恢复不能使旧会话复活：恢复流程须在重新开放API前明确撤销恢复库中的会话，单换CSRF secret不能代替删除旧token记录。
  这是待W13实际验证的运维合同，不在当前0.5.10恢复工具中假称已有。

回执仍须明确区分应用append-only与数据库权限强制不可改；现有schema级DML不能被更窄table GRANT覆盖，逐表收敛随W13独立验。

## 3. 同角色授权变化也结束旧会话

对staff在一致性事务内按稳定顺序完整读取其所有已建立关系（**包括enabled=false的tombstone**）的revision/enabled及组policy_revision/enabled/权限集合，
生成绑定用户ID、带域分离的确定性seal；不截断为最早64/100组。有效权限另取关系与组均启用的交集，不能把该筛选复用于seal输入。
否则“初始零组会话→加入组→移除组”会重新得到空集seal；“组原本停用→启用→再停用”也会使旧停用态会话复活。
运行接口不物理删除关系；停用后仍参与seal，普通组名/普通revision不参与。与该员工从未有关系的组不纳入seal，避免全站无关改动统一登出。
已停用的历史关系仍绑定其组policy_revision，因此该历史组的权限/启停变动也可使关联员工会话失效；管理界面须明确这一影响，不能暗称只影响当前启用成员。
数据库发行会话时保存seal，每个认证请求重新核对当前seal。组停用、权限变化、移入/移出组均使旧会话401，不能直接给旧会话继承新权限。
恢复原权限也因policy_revision不同而不会复活旧会话。改组名只改普通revision，不影响权限seal。

登录在密码校验前读取授权快照，发行会话事务内再复核账户revision及seal，防在途旧登录获得变更后的新授权。
普通admin/customer不因此额外全局登出；密码/角色/停用/显式撤销继续保持既有session删除规则。
退出staff角色时原关系原子停用并审计，后来再转回staff不能悄悄继承历史组。旧订单、账单、金额和owner ID不随账号类型变化。
admin/customer既有已签发会话不因新增staff列全部失效；staff缺seal永远拒绝，不能把兼容路径授权给staff。

## 4. 新接口、锁顺序和未知结果

管理员组管理与员工只读资源走不同路由；员工路由必须有完整的独立白名单，而不是转发任意admin URL。

| 接口草案 | 权限/动作 |
|---|---|
| GET `/admin/permission-catalog` | 固定白名单/数据域/权限依赖提示，不由数据库字符串创建新权限 |
| GET `/admin/permission-groups`、`/{id}` | 当前有效admin，游标分页/单笔最新revision |
| POST `/admin/permission-groups` | admin，新操作ID与理由，创建默认停用组 |
| POST `/admin/permission-groups/{id}` | admin，原revision/理由/新操作ID，启停与完整权限替换 |
| GET `/admin/permission-groups/{id}/members` | admin，组绑定分页 |
| POST `/admin/permission-groups/{id}/members/{staffID}` | admin，组/用户/关系revision、enabled、理由/新操作ID；一次只改明确成员，无硬删除 |
| GET `/admin/customer-accounts/{staffID}/access` | admin，完整关系分页/有效权限/seal版本；不是批量无界客户列表 |
| GET `/admin/staff-access-operations/{id}` | 原有效admin/原actor/目标，严格公开intent核对 |
| GET `/staff/orders`、`/invoices`、`/services`、`/customers`、`/catalog`、`/reports`、`/audit`及允许的详情 | 当前staff seal及对应read_global；无写路由 |

接口路径在实现前再与现有dispatcher核对，不把本表当已经可用的API文档。
变更继续财务既有POST风格和严格Origin/CSRF，不为本包引入另一套PATCH/PUT/DELETE白名单。
新写统一遵守B2：现有账户singleton → 原session X/owner/expiry → 当前admin SHARE → 明确组/用户锁（稳定ID顺序）→ revision/角色校验 → 变更/审计/唯一回执 → commit。
最后admin和自我降权保护不动。不在等锁之后继续信任HTTP早先取得的role，也不通过当前另一个会话替代原请求。

staff读取也必须在真正查询业务的业务只读事务里复核原session/当前seal/明确权限，不只信HTTP入口缓存。
该事务需要SHARE锁，使用普通SQL事务而非`START TRANSACTION READ ONLY`；业务层白名单仅执行获准SELECT，不因数据库运行账号具备DML就允许员工写。
顺序为既有singleton SHARE → 原session SHARE → 当前user SHARE → 同一授权快照 → 获准投影/分页。
组/成员/账户写仍取singleton X，因此授权读取与变更有明确先后；单独logout只删session，不反向申请singleton。
staff登录发行在singleton SHARE下重新核对原密码校验所见账户revision与授权快照，再保存seal；不把admin/customer普通发行替换成新角色授权。
等session锁之后再以新语句检查数据库UTC期限，沿用B2边界。

逐项锁序/授权矩阵在实施前核对：

| 场景 | 事务边界与禁止事项 |
| --- | --- |
| 创建/修改组或成员 | singleton X → 原session X → 当前admin SHARE → 组/目标稳定锁序 → revision/授权 → 变更+审计+回执 |
| 既有账户安全修改 | 保留B2顺序；转离staff时同事务停用其关系，原财务资金事务不增加组写 |
| staff业务读取 | singleton SHARE → 原session SHARE → 当前user SHARE → 当前授权 → 同一事务的获准数据；不脱锁后借`uid=""`调用任意admin查询 |
| staff登录发行 | singleton SHARE → 当前user SHARE → 完整组授权快照 → 核对密码所见revision/seal → INSERT session |
| logout | 仅删除原token行，不能反向获取singleton/user业务锁 |

是否会与既有财务FK/用户锁或登录插入发生环，必须用真实MySQL并发证明，不能只凭此表宣布无死锁。

新操作ID仅发送一次；完整公开意图持久化后才POST/PATCH/PUT/DELETE。
超时/5xx/坏JSON/丢响应为待核对，只GET原回执；404不是未执行证据，不改理由换ID重发。
409 revision冲突保留草稿，明确放弃并重读；旧回执只能核对其自身绑定，当前组资料碰巧相同不构成证明。

## 5. UI详细行为

- 管理页归入现有“客户与权限”信息架构，分开账户与权限组，不再堆一组统计卡、另起重复偏好入口或展示实现调试文案。
- 列表组名/启停/权限摘要/成员入口；50默认、100最大服务端页长，完整游标到末页，无隐藏总量截断。
- 组详情编辑先读最新revision；权限按业务分组、自定义复选框，每项明确“全站只读”；默认不选，不用“全权员工”模糊命名。
- 成员选择器搜索/分页，已有成员明确标记；只有staff可选。每次变更显示目标、影响与必填理由，不一次提交未展示的批量成员。
- 权限变动确认清楚说明相关员工需要重新登录；改名不弹危险确认。启停为显式状态，不把“已发请求”显示成已生效。
- staff登录后只出现其被授予的工作区；无权限时给明确权限空状态，不伪装“暂无订单”。缺单项权限在详情页服务端403并提供返回列表。
- 320/390单列或卡片、长邮箱/组名/金额不横向挤出；桌面保持列表密度。明暗/中英、键盘操作、焦点回落、Back/Escape、嵌套关闭与aria-busy逐项实测。
- 授权seal变化同步增加前端身份generation，取消旧请求、清缓存/分页/敏感详情；同userID/role不能保留旧授权数据。
- loading/busy/失败/空/无权/冲突/待核对都有统一组件，不用原生select/confirm/alert，也不加数据来源或真实API说明给终端用户。
- 新审美仍优先Gemini；只提供需求与公开API合同，不给源码、旧UI图、凭据或真实数据。限额时按用户授权沿已有Gemini设计系统继续。

## 6. 必需负例与交付证据

1. 原admin全权、customer本人域、金额精度、支付幂等、订单/账单状态与历史快照原回归全部保留。
2. 所有staff读接口逐权限200/403；伪造ID、跨域客户关联、详情越权、URL改为admin路径、POST/PATCH等未开放方法一律拒绝。
3. 空组、重复/未知权限、坏revision/游标、同role权限增减、组停用/再启用、成员变动、在途登录、过期/轮换/注销会话逐项覆盖。
   初始零组→加入→移除、原停用组→启用→停用、移除再加入、权限减后恢复均不得复活旧session；单改组名不使其失效。
   全部关系包含tombstone、205组且只改最后一项、输入排序变化与未知/重复权限分别覆盖。读取已开始/正在等锁与授权变更分别核验先后。
4. 同一目标并发两个编辑仅一次提交；审计/回执/会话写失败整笔回滚，原已用ID不重放，完整意图不被默认64上限清掉。
5. 13→14/部分DDL重入/已记账漂移拒绝，旧数据摘要不变；原候选schema13校验材料保留为历史验证点，不冒称已发旧版。
6. 真实MySQL/race、严格同源HTTPS浏览器、实际有限写与原始持久回执，手机/桌面/中英明暗截图并人工审查；最终同SHA全部必需CI和依赖扫描。

## 7. 2026-10-27实施前原型证据与待办

以下是域模型落地前的原型记录，不是完整实现或新增正式SQL。为避免当前0.5.10/schema13在正式构建中被暗改，候选DDL与可运行探针仅放在ignored的
`vps-billing/artifacts/execution-20261027/staff-seal/`；不是可直接安装的014迁移，不对既有库执行。

- stdlib Go探针实际复现active-only seal的两个复活反例（零组、原停用组）；改为纳入tombstone后均不复活。
  205组中仅最后一组变化能改变seal，组/权限排序不影响，单改组名不撤销，未知/重复权限与重复组拒绝。
  这是独立域模型运行，不是已实现staff认证/登录/事务或浏览器验收。
- 在本机socket-only MySQL8.4.11实际新建/删除专属空库，从现有001/010取得users/sessions/audit三表基座，
  测四新表/FK与候选约束。原customer/admin列、行、密码hash、会话和ENUM序号1/2保持；新增staff为序号3，旧会话seal为NULL。
  关系启用→停用→重新启用→停用的revision实际为1→2→3→4，保留tombstone。
- 首次补边界负例实际RED：尾空格权限及非hex seal被候选DDL接受。收紧后14类负例均收到精确MySQL错误码，
  不是把CLI/连接错误计作拒绝；全部七项合法权限、合法小写hex seal也正向通过，专属测试库已删除。
  收紧后候选DDL SHA256为`c5f57a901cc90831bf132b7ddc60fb19d9fd1205a70608a1b387cf12db26a82e`。

该MySQL探针只含三张既有关系表基座，不是完整schema13→14迁移、部分DDL重入、27表预检或财务数据全量不变证明。
还须实施版本切换/正式迁移校验、实际session seal/事务/存储与HTTP、最小数据投影、组与成员回执、前端和真实浏览器完整链。
不得将这些原型结果记作W07-C已交付，或用它们替代上述必需负例。

先交付可用的只读员工权限与admin管理闭环，不顺便开放员工账务写、第三方支付或主控root能力；受控写权限另立合同和验收。

## 8. 授权快照的实际模型切片（本机实现，尚未激活staff）

源码位于`vps-billing/backend/internal/staffaccess/`，只使用标准库，无数据库、HTTP、运行时配置或角色开关。
测试边界是本合同已有的“完整授权快照→seal/有效权限/旧seal核对”，不是读取私有函数或替换内部协作者。
`NewSnapshot(userID, []MembershipPolicy)`接收同一事务完整读取的全部关系，拥有输入副本；返回不可变快照。
`Seal/Permissions/Allows/MatchesSeal`不认证用户，不能代替原session、当前user/role/enabled和事务内权限复核。

- 固定七项全站只读权限，拒绝未知、重复、尾空格/别名、错误scope、坏ID及超界revision；无隐式read或admin权限。
- seal输入包含全部停用关系与当前组policy，权限只在关系和组均启用时生效；不存在最早64/100项截断。
- 输入排序不影响结果，调用方或返回切片的变更不影响已创建快照；零值快照和缺少/非小写hex/错误seal均拒绝。
- 版本1编码固定为`aster.staff-access/1`加NUL、32位用户ID加NUL，再依group ID升序写逐行紧凑JSON；
  字段依次为group_id、membership_revision、membership_enabled、policy_revision、group_enabled、scope、permissions。
  权限字典序排列，nil规范化为空数组；普通组名/revision不属于输入。输出为SHA256的小写64位hex。
- 两份已知seal向量由Python hashlib/紧凑JSON独立计算并写入测试；以后不能悄悄改格式，使既有会话不可解释。

实际先执行空授权新接口的RED，再最小实现GREEN；随后有效权限用例对初步实现RED，补齐完整策略后GREEN。
最终13个顶级测试/52个PASS事件、0 FAIL/SKIP，Darwin arm64 Go1.26.4 `-race`及vet通过，10轮race全部成功；
Linux/amd64测试ELF已交叉编译但未执行，不算Linux或MySQL验收。
隔离临时模块分别注入丢停用历史、64项截断、不绑定用户、返回可变授权别名四种错误，均触发实际断言失败，非编译/连接错误。
原始日志、已知向量和变异结果保存在ignored的`vps-billing/artifacts/execution-20261027/staffaccess/`。

本切片尚未被财务认证或业务查询调用；实际session seal、完整014迁移/结构验证、存储事务、组/成员回执、只读PII投影、HTTP、UI和真实浏览器仍须继续。
不能把域模型的全部关系遍历推定为未来SQL已完整读取，也不能以52个事件替代整个W07-C验收。
