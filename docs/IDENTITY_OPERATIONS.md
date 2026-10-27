# W07-B：账户修订、会话撤销和可核对身份操作

设计日期：2026-10-26。基线main b2c273f；本文件先于实现。保留W07-B1事务内会话fence，不改财务或VM协议。

## 接口与状态

- User新增`revision`，范围1…9007199254740991，所有真正账户修改及显式撤销只推进一次。普通姓名修改不撤销其他现有会话。
- `GET /console/v1/users/{id}`返回最新`{user}`，供打开编辑器/明确放弃冲突草稿后重读。仍需当前users.manage，非凭据接口。
- `POST /console/v1/users`：原创建字段，加必填`reason`及单一32位小写hex `Idempotency-Key`。创建结果revision=1。
- `PATCH /console/v1/users/{id}`：必填原`revision`、`reason`、稳定key。锁内版本不符409 REVISION_CONFLICT；不能默认当前版本或乐观覆盖。
- `POST /console/v1/users/{id}/revoke-sessions`：revision/reason/key，删除目标全部会话并rev+1。允许本人明确撤销，mayManage/最后管理员及自降权规则不放松。
- 写入结果`{user,operation}`，后端确认用户和不可变操作后才显示成功。自撤销响应清Cookie；响应丢失时前端也不继续显示已认证，保留待核对记录。
- `GET /console/v1/identity-operations/{key}`只读原操作。仅当前有效users.manage且属于该actor并仍可管理目标；外来actor或未知key为404。
- 已使用key一律409 OPERATION_USED，不自动重放写入，尤其不承诺两个密码请求等价。读取404也不是未执行证明。

`reason`为非空、首尾无空白、最多500个Unicode字符的单行说明，不接受NUL/控制字符。无原密码、Cookie、CSRF或密码摘要进入审计/回执。

## 不可变回执

新Cloud迁移012，旧001–011不动。新增用户revision和独立`controller_identity_operations`：
operation_id、actor_id、target_id、action、base_revision、result_revision、非秘密intent_json及其SHA256、reason、audit_id、completed_at。
intent保留实际提交的公开字段、revision、reason和password_changed布尔；密码内容及其任何摘要绝不保存。
客户端按原key/actor/action/target/base revision及完整非秘密intent逐字段核对，不用“当前资料碰巧相同”证明原操作。

同一事务完成用户写、会话处理、身份审计、回执。key有唯一主键；锁内查重只是错误语义，最终唯一INSERT仍是原子保护，冲突必须回滚其他变化。
顺序固定：identity_lock → 原当前session → 当前actor → 已用key → target/revision/双向委派/最后admin → 更新/撤session → audit → receipt → commit。
存储错误、审计错误、回执失败均不能留下部分修改。修订上限明确拒绝，不溢出/回绕。

登录在PBKDF前读取hash及revision，锁内同时复核，防显式撤销前的在途登录在撤销后重新发旧代次会话；新登录仍允许。
revision不是所有已发会话的通用失效版本，避免改名就踢掉所有设备。

012采用逐对象精确结构检查：允许DDL部分成功但receipt未落时正确重入；错类型/default/顺序、冲突索引、收据后漂移拒绝，不自动DROP重建。
应用只对回执表SELECT/INSERT，没有修改或删除接口。当前安装器仍授专用数据库级DML，
**尚未达到数据库强制的append-only权限**；W13必须把逐表最小授权、旧账户收敛和升级时序一起验收，
不能声称单独新增较窄table GRANT就能覆盖MySQL的既有schema级授权。旧binary严格拒绝schema12，回滚须匹配备份。

## 前端职责与交互

- UserEditor保留现有自定义控件/抽屉，打开先GET详情，版本来自该次回读；理由必填。加载、失败重试、409保留草稿及“重新读取最新资料”显式放弃入口。
- 自己的角色/启用危险项不提供可用入口；撤销自己明确说明包括当前设备，确认后才发送。未知结果不再自动生成key或改理由绕过。
- 专用身份意图账本独立于VM通用账本；按actor+目标ID（创建按规范化email）锁定未知操作，1MiB有界完整恢复，不截断、不淘汰。
- 非秘密原intent写sessionStorage成功才发送；密码只在短期表单/请求内存中，成功、关闭或失去身份即清空，永不存储密码或其摘要。
- 成员页集中展示本人待核对身份操作；已离开抽屉、刷新或重新登录仍能GET核对。损坏/配额错误阻止新写，保留原字节。
- 回执必须完整匹配原意图才能删除该项；只有明确400/401/403/业务冲突JSON能证明本次未提交。OPERATION_USED/网络/坏响应/503均保留待核对。
- 当前身份变化后不显示旧账号行/草稿/成功消息；迟到的有效回执最多清理它自己匹配的旧owner记录，不回填新账号界面。

## 验收（沿用生产HTTP/MySQL及Vue公开交互边界）

1. GET最新版本；两管理员同版本并发修改只有一次成功，另409且原稿不被覆盖。
2. 缺key/revision/reason、重复header、越界版本和未知字段拒绝；旧前端不能缺省放行。
3. 同key重复/换目标/换密码均无第二次变更；回执属主隔离、不可读外来记录，原intent不含任何秘密。
4. 其他人/本人撤全部会话，零条session也只推进一次；另一正常设备及新登录按合同可用。最后管理员仍存在。
5. 审计/receipt失败整体回滚；注销/改密/到期后的旧排队写仍完整401。真实回执可核对丢响应，404不得解锁。
6. 012升级/重复/部分DDL重入/结构漂移及旧数据不变；财务SQL/金额/支付历史字节不变。
7. 专用账本损坏/配额/重挂载/双点击/换账号；不发额外POST、不存密码、不用VM操作GET解锁。
8. 同SHA真实MySQL浏览器新增有限身份写fixture，桌面/手机、中英明暗、抽屉、冲突与未知结果核对；原只读205成员及网络163/53门槛不减。

完成B后继续C/D/E及其他工作包，MFA、财务员工权限组、客户SSO并不因本包完成而自动算实现。
