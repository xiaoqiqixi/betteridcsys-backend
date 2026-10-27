# W07 身份、权限与会话：下一轮可执行设计

日期：2026-10-25。只读审查基点为 `9858f1ce4be21c23741f747bc445468413a6951a`，同时查看了本轮 0.5.10 / 主控工作树（首批后提交为22e1bf8）；**本文件是设计，不是新增权限功能已实现/已验收的声明**。本次只写本文件，不改生产代码、SQL、发行版本、环境或 Git 历史。

## 1. 先做哪一项

**先交付 W07-A：主控“成员与权限”完整游标分页。** 当前账号超过100个时，后端只返回最早100个，界面只提示截断，无法访问后续成员。缺口明确、只读链短，可独立交付，不碰财务、授权范围、密码算法、VM操作或新供应商。

后续按独立切片开发，不能把整套 W07 塞进一次不可审查的改动：

```text
W07 身份与权限
├─ A 主控完整成员分页 ← 第一包
│  ├─ 保持原排序、稳定游标、完整到达末页
│  ├─ 服务端权限、异常查询、前端上一页/下一页
│  └─ 205人/同时间戳/权限撤销/真实MySQL与浏览器验收
├─ B 主控账号并发修订与显式会话撤销
│  ├─ revision防止旧表单覆盖新授权
│  ├─ 单成员读取、撤销所有会话、理由与审计
│  └─ 最后管理员/有界委派/角色变更后旧会话负例
├─ C 财务权限基础 → 只读员工组 → 后续受控写权限
│  ├─ admin/customer原有语义不变；新增staff为独立账号类型
│  ├─ 权限白名单、权限组、成员关系、明确数据域
│  ├─ 先只读财务/客服，不开放账务任意改写
│  └─ 同一角色内授权变化也撤销旧会话和清空旧UI数据
├─ D 两系统账户安全中心
│  ├─ 本人改密、会话列表/逐个撤销、敏感动作重新认证
│  └─ MFA/恢复码：先定恢复合同，未实现前不展示可用入口
└─ E 客户跨系统登录/业务邮件恢复
   ├─ 明确客户owner映射后才发短期一次性票据
   ├─ 不继承主控管理员角色，不共享管理员Cookie
   └─ 邮箱验证/找回依赖真实业务邮件闭环；不把SMTP测试当投递能力
```

A不宣称解决B/C/D/E。当前0.5.10候选的“无新增财务SQL、schema13”合同保持；后续需要迁移的身份包进入下一次明确发行决策，不能静默修改旧包或旧迁移。

## 2. 已实现事实与缺口（源码证据）

下列位置是本次实际读取的代码，不把历史规划当实现。行号会随后续提交移动，实施时复核函数名。

| 模块 | 当前事实 | 证据 |
| --- | --- | --- |
| 财务角色 | 仅`admin/customer`；不是可配置RBAC | `vps-billing/backend/migrations/001_initial.sql:3`；`backend/internal/billing/admin_customers.go:94,110`（后者路径均位于vps-billing） |
| 财务管理域 | `/admin/`先判断admin，随后把查询uid置空；客户路径保持本人ID。不能给staff直接套这个“置空即全域”的逻辑 | `vps-billing/backend/internal/billing/http.go:292–340` |
| 财务账户操作 | 创建、读取、姓名/角色/停用、管理员重置密码、撤销会话；邮箱仅在创建时输入，不是已实现的邮箱变更 | `vps-billing/backend/internal/billing/admin_customers_http.go:94–200`；`admin_customers.go:100–135,329–392` |
| 财务完整列表 | q/role/status/limit/cursor，默认50、最大100，ID降序keyset；HMAC游标绑定actor/role/revision/筛选/页长 | `vps-billing/backend/internal/billing/admin_customers_http.go:15–70,105–129`；`admin_customers.go:265–309` |
| 财务并发 | account revision、同事务当前admin复核、最后活跃admin、自我降权/停用拒绝；用email_settings单行串行角色写，不可随意改锁顺序 | `vps-billing/backend/internal/billing/admin_customers.go:139–166,338–386` |
| 财务会话 | 每请求查当前用户；密码/角色/停用/显式撤销会删除目标会话；登录发会话前校验当前用户revision，防在途旧凭据复活 | `vps-billing/backend/internal/billing/http.go:98–114,584–613`；`mysql.go:310–335`；`admin_customers.go:380–386` |
| 财务已有负例 | 并发互相降权只允许一方成功；审核失败回滚账号及会话、旧登录/会话撤销已有测试 | `vps-billing/backend/internal/billing/admin_customers_test.go:233–286`；`admin_customers_mysql_test.go:84–168` |
| 财务前端 | 账户分页/编辑/密码/撤销及未知结果核对已有；当前会话数据代次只监听mode、userID、role，不支持同role权限变化 | `vps-billing/frontend/src/pages/OperationsPage.vue:424–433,813–1014`；`src/composables/useWorkspace.ts:136–149` |
| 主控角色 | 12项明确权限；admin全量，operator/viewer固定子集，custom显式白名单；无隐藏“*”宿主权限 | `aster-cloud/internal/identity/types.go:26–62,114–142` |
| 主控会话/CSRF | HTTPS、固定Origin、Secure/HttpOnly主机Cookie、当前DB授权和写操作CSRF；不等于MFA | `aster-cloud/internal/identity/http.go:48–145`；`store.go:309–332` |
| 主控有界委派 | `users.manage`不是任意提权：非admin不能管理admin，目标现有与拟授予权限都必须是操作者权限子集 | `aster-cloud/internal/identity/store.go:114–145,167–180,249–250` |
| 主控最后admin | identity singleton锁串行写；不允许自改权限/禁用自己，保护最后enabled admin，安全字段变化删除会话 | `aster-cloud/internal/identity/store.go:47–49,205–208,252–284` |
| 主控登录竞态 | 登录发行会话与角色/密码变更共用identity锁，核对最新密码；按IP与邮箱持久限流 | `aster-cloud/internal/identity/store.go:385–421,462–497` |
| 主控列表缺口 | `ORDER BY created_at,id LIMIT 100`，没有next cursor，接口固定`truncated_possible:true`；所有identity查询参数当前统一拒绝 | `aster-cloud/internal/identity/store.go:286–300`；`http.go:209–214,328–335` |
| 主控UI缺口 | UsersPage固定请求`/users`，只显示最多100人；UsersResult无游标，验证器仅检查现有字段类型 | `aster-cloud/frontend/src/pages/UsersPage.vue:16–17,117–120`；`src/api/types.ts:133–137`；`src/api/client.ts:290–295` |
| 主控并发缺口 | UpdateUser没有revision；全局锁防止同时破坏最后admin，但旧表单仍可顺序覆盖已经改过的用户字段 | `aster-cloud/internal/identity/types.go:145–159`；`store.go:188–284`；`aster-cloud/frontend/src/components/UserEditor.vue:95–110` |
| 主控对象范围 | 当前console是运营管理面：instances.read等面向全局资源，不是按客户tenant过滤的客户门户。owner_ref存在不等于已完成客户身份授权 | `aster-cloud/internal/control/console.go:47–54,163–236,454–491` |
| 网络权限 | 网络/IP读复用nodes.read，具体写动作再查多个明确权限；拆出network/IP/recovery权限需要全路由矩阵，不纳入A | `aster-cloud/internal/control/console.go:48–54`；`internal/control/public_ip_binding_dispatch_mysql_test.go:31` |
| MFA/恢复/SSO | 本次路由/类型/README检索未找到MFA、恢复码或外部身份提供方完成实现 | `aster-cloud/internal/identity/README.md:44,64`；`identity/http.go:219–325`；`vps-billing/backend/internal/billing/http.go:228–257` |

**不是已发现漏洞的笼统宣称：** 主控分页截断与缺revision是可复现功能/并发缺口；全局运营者能查看全部实例是当前管理域合同，不能误报成客户越权。未来客户端才需要新owner绑定查询。现有权限检查不能因新UI隐藏按钮而被删掉。

## 3. 全部切片共同不变量

1. 授权顺序为有效会话 → 当前权限 → 对象/数据域 → 业务状态。管理员拥有全部**定义的业务动作**，不能绕过支付证据、幂等、资源未知状态、批准的主机策略或执行任意SQL/shell。
2. 财务和主控的用户ID、角色、Cookie、会话、审计库分别独立；email相同不是同一个主体。浏览器送来的role、permissions、owner_ref不作为授权证据。
3. 客户路径只可本人对象；账号对象读不到时统一404，避免利用存在性探测其他客户。管理入口无权限为403，失效会话为401；不要把403当退出后自动重发原写请求。
4. 用户变更、会话处理与身份审计在同一事务成功或一起失败。网络超时、HTTP响应断裂不是失败证明；不能重新生成意图/密码并盲重发。
5. 账户禁用不删除订单、账单、服务、历史快照或审计；不取消义务、不改变支付尝试、不触发VM停机。权限变化也不重估价格、不换汇。
6. 没有MFA/SSO/密码找回实现时，不放可点击的“已启用”“绑定成功”占位。任何能力由后端真实返回，并有失败/未配置状态。
7. 管理员、员工与客户的私密信息投影不同；密码/hash、Cookie、CSRF、一次性票据、MFA密钥/恢复码绝不进入通用DTO、日志、审计详情或前端持久存储。

## 4. W07-A：主控成员分页的完整接口合同

### 4.1 范围与非目标

包含：后端keyset分页、必要只读索引、API严格验证器、前端上一页/下一页、身份变化清理、真实MySQL与浏览器测试。

不包含：改角色、改现有授权范围、批量账号操作、用户搜索、任意页号跳转、导出、MFA、供应商或新资金规则。搜索/角色/状态筛选可在A完成后独立加入，不能将未实现的筛选控件摆出来。

### 4.2 请求和响应

```http
GET /console/v1/users
GET /console/v1/users?cursor=u1.<base64url>
```

- 仍需要当前有效会话和`users.manage`；允许管理用户的custom操作者可以查看同一个当前运营账号列表，但其写授权仍走mayManage，分页不扩大可编辑对象。
- 初始页100条，保持已有`created_at ASC, id ASC`排序；不为了省索引偷偷改成随机ID排序。
- 第一包只允许一个非空cursor；其他参数、重复cursor、分号错误查询、超长/非法编码，均400 `INVALID_QUERY`。不放开整个identity Handler的query限制，只豁免**精确GET /console/v1/users**。
- POST/PATCH/auth/roles依旧拒绝所有查询参数，避免本轮顺手降低输入边界。

```json
{
  "items": [{"id":"32位小写hex","email":"member@example.invalid","name":"成员","role":"viewer","enabled":true,"permissions":["instances.read","nodes.read","storage.read"]}],
  "limit": 100,
  "next_cursor": "u1.<下一位置或null>",
  "truncated_possible": true
}
```

- 实际`next_cursor`类型为string或JSON null；末页必须null，空页items必须`[]`，不能null。上例文字只用于说明，不是可复制的真实游标。
- `truncated_possible`暂保留兼容旧UI，改为真实`has more`结果；新UI只依赖next_cursor，不能继续显示“最多100人”即结束。以后移除此字段须单独兼容审查。
- SQL读取101条判断是否还有下一页，返回前100条。cursor只在有下一页时生成，指向**最后一条已返回项**，不能指向第101条造成漏项。
- 同一数据库读取中保留创建时间作私有分页位置，不必把created_at新增为用户DTO字段，也不重新查询边界用户（边界用户被删除不应使页链断裂）。

### 4.3 游标/SQL

建议`u1.`版本化游标，base64url内是固定对象`{"at":"UTC微秒时间","id":"32hex"}`；限定总长512字节，严格拒绝未知/重复/null字段、非规范base64、时间非UTC/非法精度、非法ID。使用参数化SQL：

```sql
WHERE (created_at > ? OR (created_at = ? AND id > ?))
ORDER BY created_at ASC, id ASC
LIMIT 101
```

游标是**不可信分页位置，不是授权票据**。本切片可沿用现有主控资源游标思路，无需引入签名密钥/供应商：合法位置被手工修改最多改变所查看的分页位置，不能改变权限、行范围或SQL。不得把这种游标说成不可篡改授权。参考`aster-cloud/internal/control/audit_query.go:104–138`的严格编码与字段验证，但不要搬入无关审计source字段。

每次请求重新鉴权。上一页缓存只属于当前session epoch；失效会话、权限改变、换账户、logout后清空。一个当前仍有相同管理权限的新会话可从任意合法位置开始，这是导航而非会话延续证明。

并发语义：静态集合从第一页翻到末页不漏不重；创建/修改期间不是全量快照，UI不要宣传一致性导出。保持不可变排序键；姓名/角色变化不影响位置。新创建用户在其时间位置出现；需要新的完整视图时“刷新”回第一页，不承诺跨长时间分页冻结整库。

### 4.4 数据迁移及版本

当前Cloud已有001–010。暂拟下一可用 `011_identity_user_paging.sql`，仅新增 `controller_console_users(created_at,id)` 非唯一索引；实施前核对号码未被其他工作包占用，有占用就顺延，不能覆写002或已发SQL。

- 迁移检查准确索引名、列顺序、非唯一性；同名异结构拒绝，不把重复DDL错误当成功。
- 不重写用户、密码、角色、会话或财务数据；验证旧用户行摘要保持不变。
- 用真实MySQL `EXPLAIN` 验证seek查询走目标索引，并测首/深页；不能只提高LIMIT或把全部用户拉到前端切页。
- 新发布的主控程序/资源/迁移清单同时推进；财务schema13和Core 0.5.10不因此改动。
- 正式升级按Cloud现有迁移记录/校验和流程；旧程序对新增schema是否允许运行，以实际启动检查为准，不能承诺只删索引即可数据库降级。

### 4.5 前端实施细节

- `UsersResult`扩展next_cursor，验证items≤100、limit===100、ID唯一、cursor非空且与`truncated_possible`一致；旧响应缺cursor视为合同未匹配，不假装只有一页。
- 页面顶部“成员与权限”，列表底部“上一页 / 下一页”和“本页N位”，不伪造total。首次页禁用上一页，末页禁用下一页。
- cursor历史栈限于当前session内存，不写localStorage；浏览器返回恢复已看页/滚动，不重复提交编辑。若使用route query，须先清掉旧身份的query并由当前会话重新读取，不能直接展示旧缓存。
  - **A实施拆分记录（2026-10-25）**：本包不改变全局router `scrollBehavior`或引入跨路由缓存。成员页内上一页/下一页、刷新回第一页、失败重试与身份清理由A交付；离开路由后浏览器返回当前仍重新读取第一页并回顶部。已看页码/滚动恢复移交W15，保持待开发/待浏览器验收，不能把此总体验项标为完成。
- 换页中保持旧表格并显示轻量加载态/aria-busy；禁止重复翻页，晚到响应按generation丢弃。失败保留原页并提供“重新加载”，不能清空成“暂无成员”。
- 320/375/390px手机使用现有member card，姓名/长邮箱可断行；角色/状态/编辑按钮不被侧栏压出。桌面保留表格节奏，不做营销卡片。
- 中文/English、明暗模式、200%缩放、Tab焦点、分页读屏提示；变更后焦点回列表标题或原编辑触发按钮，不随机落到页面顶部。
- 保存成员后刷新当前页；创建后回第一页并提示“成员已创建”，不在未取得响应时乐观插入。最后一项被修改后若本页为空，可以提示返回上一页，不自动循环抓取多页。
- 当前UserEditor的风险/权限逻辑保持，不借分页改授权或密码规则。

### 4.6 A的直接开发文件边界

| 类别 | 建议文件 |
| --- | --- |
| 查询类型/解析 | 新`aster-cloud/internal/identity/users_query.go`及单测；types.go新增UserPage但不改User写字段 |
| HTTP | `aster-cloud/internal/identity/http.go`精确query豁免、page响应；其他identity接口不变 |
| Store | `aster-cloud/internal/identity/store.go`的listUsers改有界query/page，保持只读 |
| 迁移 | 下一个未占用`aster-cloud/migrations/NNN_identity_user_paging.sql`，加精确schema验证/重入测试 |
| UI | `aster-cloud/frontend/src/api/types.ts`、`api/client.ts`、`pages/UsersPage.vue`；复用现有Button/StateBlock，不新增系统原生下拉 |
| 测试 | identity/query纯函数、MySQL分页/授权、users page组件与浏览器矩阵、公开接口文档 |

完成标准：固定205用户（含多个相同created_at），经真实HTTP登录后连续读取100/100/5，全部ID各一次；最后cursor=null。旧role/CSRF/最后admin/委派测试全保留。不得用内存fixture或只测store替代真实HTTP/MySQL。

## 5. W07-B：主控并发修改与会话撤销

A之后单独实现，避免分页夹带写协议变化。

### 5.1 乐观并发

- 当前singleton锁只保证串行，**不识别来自旧页面的陈旧意图**。新增用户`revision BIGINT`（1至JS安全整数上限）并加入User DTO；暂拟下一可用Cloud迁移，不改002。
- `GET /console/v1/users/{id}`返回当前User，仍需users.manage；供编辑前刷新与未知结果核对，不返回凭据。
- 现有`PATCH /users/{id}`增加必填`revision`和`reason`，服务端在identity锁内比较当前版本；冲突409 `REVISION_CONFLICT`，无写入、无会话撤销、无成功审计。任何有效修改只增一次revision。
- 缺revision拒绝，不默认当前值。旧UI PATCH因此不能安全兼容，必须前后端原子发布、版本化合同说明；保留现有HTTP v1路径，不另复制一套认证。
- 自我降权/禁用、最后admin、有界委派、角色白名单的原语义不变；admin≠可任意覆盖陈旧数据。
- 未知写结果只GET核对，不自动重发PATCH。相同字段已出现不必然证明是谁的操作；若需自动判定“我的意图已完成”，下一小包引入独立身份operation ID/摘要回执，不能拿已有VM任务ID冒充。

### 5.2 显式会话撤销

```http
POST /console/v1/users/{id}/revoke-sessions
{"revision":7,"reason":"移交值班账号"}
```

- 当前users.manage与mayManage双校验；单独允许操作者明确撤销**自己的**会话，但不因此开放自改角色/禁用。
- 同事务rev++、删除目标全部会话、身份审计。若撤销自己，响应后清除Cookie/CSRF并进入登录页；响应丢失也不能显示仍已登录。
- 其他成员旧会话下一请求401。显示“已要求重新登录”，不写“已从所有设备安全退出”（已开始的请求可能已经完成）。
- identity audit追加受限详情/理由的独立迁移与读取投影：只记动作、actor/target、前后revision、角色/启用变更、授权变化摘要、理由；无password、Cookie或整个请求JSON。
- 成员停用不删除其资源/历史动作。撤销会话本身也不取消进行中的VM任务，任务结果仍按现有核对流程处理。

## 6. W07-C：财务权限组基础与只读最小切片

**2026-10-27设计细化，以[FINANCE_STAFF_ACCESS.md](FINANCE_STAFF_ACCESS.md)为本切片唯一实施合同。**
此前本节的接口/权限名/新锁表/全会话失效只是草案，未发布为协议；由详细合同替换，不让两套相冲突的设计并存。
既有叶子映射与C-01至C-07验收编号继续保留，具体实现仍须逐项记证据。

- 当前仍为Core0.5.10/schema13、admin/customer；先完成W07-B2和W04，再独立切换下一Core0.5.11/schema14。
  此处不是staff、权限组或0.5.11已实现/可下载的声明，001–013不改。
- 新staff默认零授权、全站只读数据域；七项明确read_global权限，客户/订单/账单/服务/目录/报表/审计分别控制。
  不开放员工写账务、付款、任意改状态或宿主操作，不把staff直接放入旧`/admin/`置空uid分支。
- 组与成员维护采用既有POST/Origin/CSRF风格、revision、理由、唯一操作回执和完整游标；写结果不明只GET核对，不重POST。
- 复用既有账户singleton保持一致锁序，B2原session/owner/锁后expiry边界适用于新增组写；
  staff业务查询在自身只读事务内重新核对当前seal/权限，不能只信HTTP先前缓存。
- 成员停用/重加和权限停用/恢复推进授权版本，旧会话不得复活；只改组名不登出员工，admin/customer旧会话语义不变。
- 前后端、迁移、七权限读取矩阵、真实MySQL并发、会话换代清缓存、手机/桌面/明暗/中英和实际截图必须闭合；
  不把设计文档或单项API算完成，金额、支付和历史快照原测试保留。

## 7. W07-D/E：安全中心与客户SSO，明确后置合同

这些功能本次只规划，不创建占位接口、假开关或新身份供应商。

### D1 本人账户与会话

- 本人改密须验证当前凭据，不能复用“管理员重置其他人密码”API。变更成功轮换当前会话并撤销其他会话；丢响应后只核对登录态，不自动再次改密。
- 会话列表只返回不透明会话ID、创建/到期时间、是否当前、受限设备摘要；不返回token/hash/raw User-Agent/IP全量。列表和撤销只可本人；管理员撤销他人走B/C的审计权限。
- 敏感动作重新认证产生短时、绑定当前session/actor/动作类别的确认态；不在localStorage持久化密码，不能仅靠“最近打开过弹窗”。

### D2 MFA与恢复码

状态设计：未配置 → 待绑定验证 → 已启用 → 待安全重置/停用；没有成功验证不能启用。恢复码显示一次、只保存摘要、逐个原子消费；重生新组使旧码全部失效，审计不记明文。并发使用同一码只有一次成功。

因子类型（本地标准TOTP/WebAuthn或既有组织身份服务）、密钥保管、丢失所有因子的人工恢复、重新认证有效期及防锁死机制须独立ADR评审后开发，不擅自选择供应商。最后管理员失去因子不能靠匿名接口直接重置；恢复流程不得绕过审计、持有证据与限流。

### E1 财务客户→主控客户入口

只有W08提供**可验证的财务user ↔ owner_ref ↔ instance**映射后才开发。主控现有运营console不能直接作为客户SSO落点，必须新建客户域路由和最小权限会话。

- 票据是短期一次性opaque随机码；数据库仅保留摘要、issuer/audience、绑定客户/owner、到期、消费状态，不包含管理员role。
- 浏览器登录意图state和接收端绑定必须验证；票据只经固定回调/受批准的服务间校验，不接任意redirect URL，不把code打印到日志。
- 原子消费，过期/重复/错误audience/错误owner均失败；失败不会产生管理员Cookie、付款或VM操作。旧密码/Cookie/CSRF不跨系统转发。
- 状态：issued → consumed或expired/revoked；消费后的网络错误不能重新发相同意义但新身份的授权，需根据绑定状态重新开始独立登录意图。
- 具体发码/兑换接口与TTL待上述映射及威胁模型审查；在此之前UI不显示“自动登录成功”。

### E2 业务邮件与恢复

当前SMTP只是管理员固定测试，不是验证/找回队列。先定义邮箱验证token、一次性消费、到期、发送失败/重试、收件域限制及不泄露账户存在性的响应；再接当前独立SMTP能力，不能为W07擅自加新邮件供应商。客户改邮箱须验证新邮箱、通知旧邮箱并撤销相关会话，不能直接复用管理员资料PATCH。

## 8. 必测负例和可执行验收清单

| 组 | 场景与必须结果 |
| --- | --- |
| A-01 | 0/1/99/100/101/205人；同created_at多行；静态翻页无漏/重，末页null，不靠truncated_possible猜测 |
| A-02 | duplicate/unknown/空/超长cursor、非法时间/ID/base64、GET以外带query均400；不进入SQL宽查询 |
| A-03 | 另一管理员在翻页前撤销当前管理者权限/会话，下一页401/403；旧行/旧响应不能重新写回UI |
| A-04 | 查询失败503不当空列表；取消/快速翻页/乱序响应只显示当前generation；身份切换清空页栈 |
| A-05 | SQL索引及分页边界在真实MySQL验证；受限运营角色不能因分页获得新增写权限 |
| B-01 | 两个管理员用同一revision改同一账号，只有一个提交；另一方409且不撤会话/不写成功审计 |
| B-02 | A/B互相降权或禁用，最终至少一名enabled admin；允许的错误必须明确，死锁/超时不能算安全拒绝通过 |
| B-03 | 自降权、自禁用、自改custom grants拒绝；custom持users.manage不能改admin，不能通过先降权限再重置高权账号绕过mayManage |
| B-04 | 角色/权限/禁用/密码/email变更后旧会话失效；并发登录不得用旧密码快照发出可用新会话 |
| B-05 | 审计插入失败时账号变更和会话撤销一起回滚；失败与未知UI区别，不能盲重PATCH |
| C-01 | admin、无组staff、只读财务、只读客服、customer A/B逐路由矩阵；直接URL/手造body不能越过 |
| C-02 | customer A访问B订单/账单/服务404；admin的客户入口不自动变全域；staff全域只读必须明确获授 |
| C-03 | 组停用/删除成员但role未变，旧会话仍401、旧cursor/详情缓存失效；不是只监听role改变 |
| C-04 | 组revision冲突、同组并发改权限、未知权限/重复关系/损坏scope/超过容量全部拒绝且无部分保存 |
| C-05 | 授予全白名单的staff仍不算最后admin，不能更新身份组/商户秘密/主机配置或任意mark-paid |
| C-06 | 旧session缺authz快照必须重新登录；登录与组变更竞争不得留下可用旧快照；真实MySQL锁序无死锁假成功 |
| C-07 | 迁移13→下一版后users已有角色/密码、财务表/金额/快照/原迁移摘要不变；旧程序启动合同明确拒绝不兼容schema |
| D/E | 重新认证过期/错误绑定、恢复码并发消费、票据过期/重复/错误audience/owner、恢复限流负例；功能未实现时标待开发而非SKIP通过 |
| UI | 明暗主题、中英、320/375/390与1440px、键盘焦点/读屏、抽屉Esc/遮罩/返回、脏表单保护、忙碌/未知/冲突、长邮箱/长姓名均实跑截图 |

账号操作期间对财务历史进行前后不可变摘要核验：订单、账单、支付尝试/回调、币种/报价、PlanSnapshot/格式快照/私密产品快照、服务与开通任务不得因权限UI操作变化。身份表/身份审计的预期变化单列，不能把所有表都排除后声称资金未动。

## 9. 当前实库浏览器fixture的复用边界（只读复核）

`vps-billing/deploy/ci/browser-mysql-fixture.gotxt`是本轮未提交CI overlay，不是生产Go包：

- `:92–118`要求显式非root Linux/amd64、私有目录、固定mysql-native Unix socket及真实datadir；不是任意传入生产DSN。
- `:119–150`成功创建随机唯一数据库后才认领清理所有权并迁移；W07不得改成按前缀批删数据库。
- `:151–174`用真实AddUser/CreateOrder准备合成客户/admin和一笔未付款订单，浏览器后续必须经真实登录；它不测试“管理员HTTP新增用户”。
- `:26–89,175`比较除短期sessions外全部表的只读前后摘要；适合当前只读界面，不代表已覆盖身份变更。
- `:199–222`只放行login/logout写，其他写先被fixture拦截。因此不能用这里返回的403证明后台RBAC写拒绝，也不能宣称角色修改/最后admin/UI保存已实测。

A属于Cloud，应使用其独立identity真实MySQL fixture和页面，不能靠财务两账号fixture代替。B/C要新建**明确允许有限身份写**的隔离测试场景，断言请求确实抵达生产handler、事务与权限校验；继续严格核对所有非身份业务表未变。不要把当前只读门禁直接“全部放开”再称安全验收。

## 10. 下一轮执行顺序与交付定义

1. 实施前复核工作树、迁移占号及上述证据；不覆盖并行变更。
2. 先写A查询/编码和HTTP拒绝测试，看到合理失败；再实现分页store与索引迁移，最后接UsersPage。
3. A交付同提交：Go/race、真实MySQL、前端检查、浏览器桌面/手机截图、迁移重入/结构漂移/回滚兼容说明。不会创建真实客户或动生产。
4. 完整CI通过后，按授权只更新main；发行与公共资产验收独立记录，不把提交推送成功当完成。
5. A验收完成，再做B的版本/会话合同；C先做权限矩阵/锁序与schema评审，再交只读staff。D/E依赖单独安全与恢复合同，不用空占位充数。

**本设计完成不等于W07完成。** 第一包可独立验收的结果是：第101位及之后的主控成员可以经真实权限校验找到并正常翻页，手机/桌面可用，现有授权及财务边界没有改变。

## 11. 对照完整功能思维导图的逐叶追踪

索引来源：`docs/planning-review-20261025/full-function-tree.json`，元数据基点`9858f1ce4be21c23741f747bc445468413a6951a`，1982节点/1625叶子。下列引用全部校验为**真实叶子ID**，共101项。全树原`status`是当时源码观察，不是本次或未来验收；本设计不修改原JSON，不把叶子`implemented`自动转换为“已通过”。

每个切片开工时按本表和第8节建立“叶子ID → 当前提交 → 用例/结果 → UI证据 → 未验项”记录。一个叶子内新增的分页、revision或MFA子要求未完成时，不能因为旧入口存在就划为全完成。A的分页细节原树没有独立子叶，作为`cloud-cloud.identity.accounts.01`和`cloud-cloud.ui.pages.10`的验收细分登记，不伪造原树ID。B的显式撤销同理挂在现有动态撤权/账号管理叶子下。


### 11.1 第一包A：主控成员可完整到达，保留原授权

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `cloud-cloud.identity.accounts.01` | 查看账号列表 | A待开发：现有叶子“已实现”只代表列表入口，不含100条之后的可达性 | A-01/02/03/05 |
| `cloud-cloud.ui.pages.10` | 账号/角色管理页 | A待开发：成员页上一页/下一页、末页、载入/错误及身份切换清页栈 | A-01/03/04；UI |
| `ui-ui-036-03` | 成员管理入口按权限显示可编辑动作 | A保留回归：可见成员不等于可编辑，原mayManage边界不扩大 | A-05 |
| `ui-ui-058-02` | 403 无权限不显示成没有数据 | A待实测：403显示无权限，不伪装空列表；未授权数据不得继续展示 | A-03/04；UI |

### 11.2 主控B及现有身份/角色安全回归

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `cloud-cloud.identity.accounts.02` | 创建管理员或运维账号 | B保留：创建账号的admin/委派权限边界；不因新revision放宽 | B-03 |
| `cloud-cloud.identity.accounts.03` | 编辑账号资料 | B待增补：单人读取、revision冲突、提交后真实回读 | B-01/05 |
| `cloud-cloud.identity.accounts.04` | 重设受管账号密码 | B保留并增补revision/审计理由；旧密码在途登录不能复活 | B-01/04/05 |
| `cloud-cloud.identity.accounts.05` | 启用或禁用账号 | B保留并增补revision；停用不触发VM或财务业务变化 | B-01/02/04 |
| `cloud-cloud.identity.accounts.06` | 最后管理员保护 | B保留：并发互相降权/禁用仍至少一名enabled admin | B-02 |
| `cloud-cloud.identity.accounts.07` | 自身敏感权限保护 | B保留：自降权、自禁用及自改custom grants拒绝 | B-03 |
| `cloud-cloud.identity.accounts.08` | 账号变更审计与界面回显 | B待增补：安全理由、revision、事务审计与未知结果核对 | B-05；UI |
| `cloud-cloud.permissions.roles.01` | 管理员全部显式权限 | A/B保留：管理员全量显式业务权限不等于任意宿主操作 | B-02/03 |
| `cloud-cloud.permissions.roles.02` | 运维角色 | A/B保留：operator固定权限不因UI或翻页扩大 | A-05；B-03 |
| `cloud-cloud.permissions.roles.03` | 只读角色 | A/B保留：viewer不可利用手造请求写入 | A-05；B-03 |
| `cloud-cloud.permissions.roles.04` | 自定义角色授权 | B保留：自定义权限白名单及现有/拟授权双向子集检查 | B-03 |
| `cloud-cloud.permissions.roles.05` | 实例普通动作权限 | B回归不扩权：普通实例动作仍查当前权限和业务状态 | B-04 |
| `cloud-cloud.permissions.roles.06` | 危险实例动作权限 | B回归不扩权：危险动作独立权限不被账号UI绕过 | B-03/04 |
| `cloud-cloud.permissions.roles.07` | 存储读写恢复分权 | B回归不扩权：storage read/manage/restore边界保持 | B-03/04 |
| `cloud-cloud.permissions.roles.08` | 迁移、节点、用户及审计分权 | B回归不扩权：migration/nodes/users/audit不自动继承 | B-03/04 |
| `cloud-cloud.permissions.roles.09` | 动态撤权立即生效 | B待补显式撤销入口；既有授权变化撤会话仍须回归 | A-03；B-04 |
| `cloud-cloud.permissions.roles.10` | 网络与公网独立细粒度权限 | 后置独立权限矩阵，不在A/B中借名顺改网络/IP授权 | 第2节网络边界；后续专包 |

### 11.3 两系统登录、CSRF与会话：保留功能，不将旧观察当本轮通过

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `cloud-cloud.identity.signin.01` | 账号密码登录 | B/D回归：真实凭据登录，禁止旧密码快照在撤销后发会话 | B-04 |
| `cloud-cloud.identity.signin.02` | 获取登录CSRF凭据 | A/B回归：登录前CSRF合同不被分页开放查询影响 | A-02；B-04 |
| `cloud-cloud.identity.signin.03` | 查询当前登录用户 | A/B回归：当前用户来自后端；新revision/权限能力逐步加入 | A-03；B-04 |
| `cloud-cloud.identity.signin.04` | 安全退出登录 | B/D回归：退出当前会话；全部/逐个撤销为独立新增合同 | B-04；D/E |
| `cloud-cloud.identity.signin.05` | 登录频率控制 | B/D回归：登录限流保持，恢复/二次认证另设限流 | B-04；D/E |
| `cloud-cloud.identity.signin.06` | 安全Cookie | A/B回归：Secure/HttpOnly及HTTPS/Origin条件不降级 | A-03；B-04 |
| `cloud-cloud.identity.signin.07` | 会话到期处理 | A/B回归：过期或撤销立即清理敏感内容，不只显示提示 | A-03/04 |
| `cloud-cloud.identity.signin.08` | 写请求CSRF校验 | B回归：所有身份写仍校验当前会话、Origin及CSRF | B-01/03/05 |
| `cloud-cloud.ui.pages.11` | 登录页与失效会话返回 | A/B增补验收：过期返回登录、回跳限站内、丢弃旧响应 | A-03/04；UI |
| `bill-billing-entry-3-1` | 读取当前会话、当前用户和 CSRF 令牌 | C回归并扩展：当前用户/CSRF；拟新增authz版本来源必须真实 | C-03/06 |
| `bill-billing-entry-3-2` | 邮箱规范化后校验密码 | C/D回归：邮箱规范化与密码不裁剪合同保留 | C-06；D/E |
| `bill-billing-entry-3-3` | IP 与邮箱维度登录频率限制 | C/D回归：既有IP/邮箱限流不等于新增恢复链已限流 | C-06；D/E |
| `bill-billing-entry-3-4` | 仅信任批准代理提供的真实客户端 IP | C回归：代理客户端IP只信批准链，不把UI提供IP当证据 | C-01/06 |
| `bill-billing-entry-3-5` | 退出并撤销当前会话、轮换令牌 | C/D回归：退出撤销；新会话中心不改变旧token安全边界 | C-03/06；D/E |
| `bill-billing-entry-3-6` | 登录失败/停用账户不暴露密码详情 | C/D回归：失败响应不泄露密码/停用细节或找回账户存在性 | C-01；D/E |
| `bill-billing-entry-4-1` | 未登录受保护资源返回401 | C回归：未登录401，不能由staff能力字段绕过 | C-01/06 |
| `bill-billing-entry-4-2` | 客户访问管理员接口返回403 | C扩展矩阵：customer仍403，staff逐路由显式授予而非放开/admin | C-01 |
| `bill-billing-entry-4-3` | 客户路径始终按自身账户隔离 | C保留：客户self对象边界不随staff全域只读变化 | C-02 |
| `bill-billing-entry-4-4` | POST校验同源Origin与CSRF | C保留：敏感写同源Origin/CSRF，权限组API亦遵守 | C-01/04 |
| `bill-billing-entry-4-5` | 会话切换后丢弃旧用户迟到响应 | C待增补：相同role授权变化也增加generation并丢旧响应 | C-03；UI |
| `bill-billing-entry-4-6` | 会话过期清理受保护页面，不保留旧数据 | C保留并验：失效会话清页面/缓存/游标，不保留旧客户数据 | C-03/06；UI |

### 11.4 财务客户/角色管理逐叶对应C（既有功能保留、新权限明确区分）

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `bill-billing-customers-1-1` | 按名称或邮箱查询客户账户 | C保留既有名称/邮箱查询；staff读取需明确customers.read_global+all_customers | C-01/02 |
| `bill-billing-customers-1-2` | 筛选客户/管理员角色 | C按正式角色合同扩展staff筛选；未知角色拒绝，不能偷偷改旧枚举 | C-01/04/07 |
| `bill-billing-customers-1-3` | 筛选启用/停用状态 | C保留启停筛选；停用员工不改变财务义务 | C-01/03 |
| `bill-billing-customers-1-4` | 服务端有界分页与上一页/下一页 | C保留完整分页；新cursor须绑定authz版本/scope，旧cursor拒绝 | C-03/04 |
| `bill-billing-customers-1-5` | 查看账户身份及当前修订版本 | C增补独立authz版本，不让显示字段revision充当全部组授权版本 | C-03/04/06 |
| `bill-billing-customers-1-6` | 无结果、查询错误、权限失效分别反馈 | C回归错误互斥；staff无权不是空列表，失效响应不能复写 | C-01/03；UI |
| `bill-billing-customers-2-1` | 填写名称、邮箱、角色、初始密码和原因 | C账户创建只允许获准主体；admin/staff/customer界限明确 | C-01/05 |
| `bill-billing-customers-2-2` | 邮箱创建后不可直接改写 | C保留邮箱不可直接改；E验证改邮箱不复用旧更新API | C-01；D/E |
| `bill-billing-customers-2-3` | 初始密码为16–72 UTF-8字节 | C保留财务16–72 UTF-8字节，不混用主控密码规则 | C-07 |
| `bill-billing-customers-2-4` | 创建管理员需明确敏感身份确认 | C保留admin高权限确认；staff所有权限也不算admin | C-05；UI |
| `bill-billing-customers-2-5` | 密码不被裁剪、不进入日志或返回值 | C保留密码秘密边界；新增授权审计不得序列化整个账号对象 | C-04/05；UI |
| `bill-billing-customers-2-6` | 提交后重新读取真实账户信息 | C保留后端回读；未知结果先核对，不自动生成新账号重试 | C-04；UI |
| `bill-billing-customers-3-1` | 编辑名称 | C保留名称编辑；与authz revision分离减少无关撤会话 | C-04/06 |
| `bill-billing-customers-3-2` | 切换客户/管理员角色 | C在明确迁移之后新增staff；旧admin/customer意义不改变 | C-01/05/07 |
| `bill-billing-customers-3-3` | 启用或停用账户 | C保留停用语义；任何owner对象和历史账务保持原状 | C-01/03/07 |
| `bill-billing-customers-3-4` | 修改需携带版本与必填原因 | C保留账号revision/必填理由；权限组另有独立revision | C-04 |
| `bill-billing-customers-3-5` | 角色/停用变更撤销原会话 | C增补同角色组变更也使旧会话失效 | C-03/06 |
| `bill-billing-customers-3-6` | 敏感写入实时复核操作者管理员身份 | C保留财务写仍复核真实admin；不为只读员工重构金额事务 | C-01/05 |
| `bill-billing-customers-4-1` | 重置账户密码 | C保留管理员重置；D本人改密必须新增当前凭据验证合同 | C-01/06；D/E |
| `bill-billing-customers-4-2` | 撤销目标全部会话 | C保留目标全部会话撤销；D本人逐个撤销是不同新增功能 | C-03/06；D/E |
| `bill-billing-customers-4-3` | 禁止自我停用或降为客户 | C保留禁止自我停用/降权，不靠前端按钮禁用实现 | C-01/05 |
| `bill-billing-customers-4-4` | 禁止移除最后一个活跃管理员 | C保留至少一名真实active admin；权限全开的staff不计入 | C-05/06 |
| `bill-billing-customers-4-5` | 并发旧版本写入返回冲突 | C保留版本冲突；组/成员变更并发亦必须无部分保存 | C-04/06 |
| `bill-billing-customers-4-6` | 创建/更新/重置/撤销分别记录审计 | C保留同事务安全审计；新权限组事件只记录白名单字段 | C-04/06 |
| `bill-billing-customers-5-1` | 可自定义角色与权限组 | C待开发：权限组/成员/明确数据域；不是已有角色切换的改名 | C-01/03/04/06/07 |
| `bill-billing-customers-5-2` | 只读财务/客服/商品运营权限划分 | C首期只读财务/客服；商品运营写权限后续专包，不算首期完成 | C-01/05 |
| `bill-billing-customers-5-3` | 管理员全业务动作仍服从财务状态约束 | C持续不变量：admin全部业务动作仍服从金额、支付、历史状态 | C-05/07；全表摘要 |

### 11.5 D/E待开发与依赖边界（不是A交付内容）

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `bill-billing-home-3-3` | 客户自助修改资料 | D待开发：本人资料投影与敏感字段保护，不复用管理员任意编辑 | D1；D/E |
| `bill-billing-home-3-4` | 客户自助修改密码 | D待开发：本人凭据验证、改密及会话轮换 | D1；D/E |
| `cloud-cloud.identity.strong.01` | TOTP或WebAuthn多因素认证 | D待设计/开发：因子ADR与恢复先行；不擅选供应商或假启用 | D2；D/E |
| `cloud-cloud.identity.strong.02` | 恢复码生成与一次性消费 | D待开发：摘要保存、只展示一次、原子消费与重生撤旧 | D2；D/E |
| `cloud-cloud.identity.strong.03` | 高危操作二次认证 | D待开发：重新认证态绑定session/动作及到期，非普通确认框 | D1/D2；D/E |
| `cloud-cloud.identity.strong.04` | 设备解绑与MFA重置审计 | D待开发：重置/解绑审计、丢失因子恢复与最后admin保护 | D2；D/E |
| `bill-billing-extensions-1-1` | 自助注册与反滥用策略 | 后置业务评审：自助注册/反滥用不是当前管理员建号；不并入A | E2前置策略评审 |
| `bill-billing-extensions-1-2` | 邮箱验证和地址变更验证 | E待开发：验证新邮箱、通知旧邮箱、一次性token与失败闭环 | E2；D/E |
| `bill-billing-extensions-1-3` | 密码找回与安全恢复 | E待开发：找回/恢复不泄露账户存在，限流/到期/消费需实测 | E2；D/E |
| `bill-billing-extensions-1-4` | MFA与SSO | D/E待开发：该合并叶须分别登记MFA与SSO，不能只做其一划完 | D2+E1；D/E |
| `bill-billing-email-4-2` | 注册验证邮件与找回密码邮件 | E依赖：真实业务发信/重试/投递合同，SMTP固定测试不足以完成 | E2；D/E |
| `cloud-cloud.permissions.owners.01` | 资源owner精确绑定 | E依赖W08：保留资源owner精确绑定，email不能作为映射依据 | E1；D/E |
| `cloud-cloud.permissions.owners.02` | VPC与公网owner一致性 | E依赖W08：VPC/公网/实例所有权矩阵先闭合；A不修改网络 | E1；D/E |
| `cloud-cloud.permissions.owners.03` | 客户SSO票据兑换 | E待开发：固定接收端的一次性客户票据兑换，非管理员console | E1；D/E |
| `cloud-cloud.permissions.owners.04` | 客户独立资源列表与详情权限 | E依赖W08：独立客户域列表/详情逐对象授权，不能复用全域console | E1；D/E |
| `cloud-cloud.permissions.owners.05` | 客户会话撤销与跨系统退出 | E待开发：跨系统撤销合同独立；相同email不共享会话 | E1；D/E |
| `cloud-cloud.permissions.owners.06` | 客户资源归属转移 | 后置独立归属转移设计：跨客户资源转移不是本轮授权组操作 | W08转移/快照专项 |
| `cloud-cloud.permissions.owners.07` | 作用域客户API key | 后置独立scope/API key生命周期设计；不随SSO默默发行长期密钥 | 后续独立安全包 |
| `cloud-cloud.customer.actions.06` | 客户SSO与到期访问策略 | E与W08联合：SSO成功不代表欠费/到期状态允许资源操作 | E1；D/E |
| `cloud-cloud.customer.console.04` | 会话撤销后断开连接 | E与控制台专项联合：撤销客户会话还需切断既有连接 | E1；后续控制台验收 |

### 11.6 UI叶子：与身份接口同时验收，不新增演示开关

| 完整树叶子ID | 原叶子名称 | 本计划对应与当前边界 | 验收锚点 |
| --- | --- | --- | --- |
| `ui-ui-029-01` | 创建客户账户并设置角色 | C保留建号体验；staff/组选择只在后端能力就绪后出现 | C-01；UI |
| `ui-ui-029-02` | 编辑既有账户资料和访问角色 | B/C增补revision冲突与后端回读，不悄悄覆盖新授权 | B-01；C-04；UI |
| `ui-ui-029-03` | 停用账户时说明访问影响 | B/C保留停用访问影响说明，不暗示取消服务或删除财务 | B-04；C-07；UI |
| `ui-ui-029-04` | 撤销会话时说明全部旧会话失效 | B/C明确全部旧会话失效；不与仅退出当前设备混淆 | B-04；C-03；UI |
| `ui-ui-029-05` | 创建全站管理员时明确高权限身份提示 | B/C保留创建admin高权限确认；不能仅靠文案代替服务端检查 | B-03；C-05；UI |
| `ui-ui-029-06` | 当前账户降权后清空过期管理员数据 | B/C变化后清敏感数据；C同时覆盖role不变而组权限改变 | B-04；C-03；UI |
| `ui-ui-029-07` | 密码和令牌不回显在成功消息或日志中 | B/C/D待实际核验：秘密不进成功toast、日志、审计或持久缓存 | B-05；C-04；D/E；UI |
| `ui-ui-060-01` | 异步请求绑定当前账户与角色代际 | A/B保持代次；C加入真实authz版本，不只监听字符串role | A-04；C-03 |
| `ui-ui-060-02` | 身份变化后丢弃旧请求结果 | A/B/C丢弃旧身份迟到数据；翻页与详情请求均覆盖 | A-03/04；C-03 |
| `ui-ui-060-03` | 切账号清理旧管理员数据和消息 | A/B/C清账号页栈、详情、消息及权限缓存，不能带入新用户 | A-03/04；C-03 |
| `ui-ui-060-04` | 不确定写入保存完整原意图供核对 | B/C保留未知写入原意图，密码只存短期内存且不可回显 | B-05；C-04 |
| `ui-ui-060-05` | 核对原操作期间不自动生成新写入 key | B/C核对期不生成新写入；新增身份API不照搬资源幂等键 | B-05；C-04 |

**资金相关新增规则不在W07内。** 客户可读域/员工只读域是身份能力，不新增余额、授信、退款、税费、换汇、欠费豁免、历史重算或服务转移财务规则。若后续客户权限/SSO需求触及这些域，单独建业务与账务评审项，不在身份迁移内顺手实现。

## 12. 2026-10-26实施拆分

A已在4626e32取得真实MySQL和浏览器验收，详见[续记](EXECUTION-20261026.md)。B先拆出[会话事务fence](IDENTITY_SESSION_FENCE.md)：防止入口已认证的排队身份写入在注销/改密/到期后继续。此切片不需要迁移，不改公开DTO；后续才实施revision与独立身份操作回执。

不能只在事务外多查一次会话；必须在identity锁内以当前读锁定同一session并判有效期。也不能把通用VM操作回读、用户当前字段或404当作身份未知写入的成功/未执行证明。B的其余合同和上述叶子映射保留，未因此标为完成。
