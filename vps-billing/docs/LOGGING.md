# 生产日志与审计

本页描述当前源码候选，不代表正式 Release 或完整主机验收已完成。日志复用 Linux journald 与 MySQL，不引入日志守护进程、Redis 或浏览器读取主机 journal 的接口。

## 1. 三类记录，不混用

| 类别 | 位置 | 用途与边界 |
|---|---|---|
| 业务审计 | MySQL `audit_events`；后台「审计日志」/客户「操作记录」 | 业务操作及已有事务审计。客户仅能读本人事件，管理员可读全站。不是财务总账，也不保证每一个 HTTP 请求都有业务事件。 |
| API 运行诊断 | `aster-api.service` stderr → journal，JSON | 请求完成、受控错误分类、panic 安全调用位置与服务启停。不会把 SQL、错误原文或客户请求内容写入请求日志。 |
| HTTP 边缘 | Nginx → 本机 `/dev/log`，tag `aster_edge`，JSON | 响应状态、流量、耗时、固定路由模板及独立请求 ID；包括 API 未启动时的 502。 |

业务审计保留已有操作者、事件、目标及详情。管理员代客户操作时，`owner_id` 是事件所属客户，`actor_email` 才是记录中的操作者；历史数据没有的 actor ID 不补造。已有详情可能含客户标识或用户填写的取消原因，按敏感业务数据控制访问，不承诺对所有历史自由文本自动脱敏。不提供删除/清空审计的 Web 按钮。

## 2. 请求定位

每个进入 API handler 的请求由服务端新建 32 位小写十六进制 ID，返回 `X-Request-ID`。不沿用请求者传入的同名头。界面错误可显示真实响应 ID；没有响应的网络错误不能伪造 ID。

API 的 `http_request_completed` 记录包括 `request_id`、固定 `route`、受限 `method`、`status`、实际写入的 `bytes`、`duration_ms`，已鉴权时附实际 `actor_id`/`actor_role`；失败时可附 `error_code`/固定 `error_class`。4xx 为 WARN，5xx、异常中断、取消或写失败为 ERROR。成功健康探测不重复刷日志，失败的 `/readyz` 仍记录。

Nginx 每次请求独立生成 `edge_id`；只有上游返回严格合法的 API ID 时才写 `api_request_id`。502 或非 API 路径通常没有 API ID，这是正常边界，不是丢了一个可以重试的业务操作。

```sh
# 原生 systemd 249（Ubuntu 22）支持这些 journalctl 选项。
sudo journalctl -u aster-api.service --since '30 minutes ago' -o cat --no-pager
sudo journalctl -t aster_edge --since '30 minutes ago' -o cat --no-pager

# 将占位 ID 换成界面提供的 32 位 ID；不要粘贴完整 URL、Cookie 或密钥。
sudo journalctl -t aster-api -t aster_edge --since today -o cat --no-pager \
  --grep '0123456789abcdef0123456789abcdef'

# JSON 的 level 与 journal 的 PRIORITY 不是一回事；stderr 不保证映射成原生 ERROR priority。
sudo journalctl -u aster-api.service --since today -o cat --no-pager \
  | python3 -c 'import json,sys
for line in sys.stdin:
 try: row=json.loads(line)
 except ValueError: continue
 if row.get("level")=="ERROR": print(json.dumps(row,ensure_ascii=False))'
```

未提交响应的 panic 返回通用 500；已提交响应的 panic 中断连接，不能伪装成完整 500 或追加第二个 JSON。记录状态可能仍为已经发送的 2xx，必须同时看 `aborted`/`panic`/`write_failed`。panic 只记函数、源文件 basename 和行号，不记 panic 值、实参或完整运行时 dump。net/http 的原始诊断也不透传用户请求文本。

API 在 SIGTERM 时停止接收新连接并等待在途请求，排空上限 10 秒；超时强制断开并以失败退出。systemd `TimeoutStopSec=30` 不等于所有业务一定已成功完成。面对支付/下单不确定结果，查询权威状态并复用原幂等键，不能根据日志超时就新建操作。

## 3. 采集与保留

- API unit 使用 journal，禁止 core dump。生成的独立 Nginx unit 同样限制 core dump，生命周期输出标记为 `aster-edge-control`；手动使用共享发行版 Nginx 时须自行审查其 unit 限制。
- Nginx 专用 server 的原始 engine error log 显式丢弃：该格式可能包含完整 URL、query 或文件路径，无法用 access log 的字段白名单脱敏。保留结构化 access 事件、API 诊断与服务生命周期；代价是没有每次 TLS/upstream 失败的原始 engine 文本。配置检查失败的受控工具只报安全阶段/退出码，私下排障也不得将秘密材料发到工单。
- 请求日志不收集 Cookie、Authorization、CSRF、原始 path/query、IP、邮件地址、请求/响应体或 SMTP 凭据；未知路径在 API 记 `unmatched`、在边缘记 `OTHER`，未知 method 均记 `OTHER`。不要为“方便排障”启用 Nginx debug 或把 `$request` 加回 access log。
- `deploy/systemd/journald-aster.conf.example` 是**整台主机**的显式可选政策，不是应用专属设置。安装器不会自动复制或重启 journald。先核对其他服务、磁盘、合规保留期与远程转发需求，再由运维审核应用。
- 示例 persistent 日志上限 256 MiB、保留不超过 14 天、最低磁盘预留 1 GiB；服务与 journal 限流均为 30 秒/10000 条。容量、限流、异常断电与 syslog datagram 都可能导致丢失；监测 `Suppressed`、磁盘和转发状态，不能把 journal 当无损审计或唯一事故副本。
- 用 `journalctl --disk-usage`、`journalctl --list-boots` 检查实际保留。`Storage=auto` 且没有持久化目录时可能重启即丢失；样例文件存在不代表已启用。日志不是加密备份，root/`adm`/`systemd-journal` 及转发端权限均须核查。
- 业务审计的保留与恢复随 MySQL 备份政策，不随 journal vacuum 清除。不要自动 `TRUNCATE audit_events`，也不要覆盖迁移历史来“整理日志”。

备份脚本保持原成功 stdout 协议；失败诊断为安全 NDJSON（随机 `operation_id`、固定 phase/tool role、exit code），原始外部命令 stderr 不保留。不会再指向已删除的临时错误文件，也不记录命令参数、SQL、DSN 或秘密路径。主机操作还有独立的受限状态/交接 journal，不能以 HTTP 日志代替操作回执。

## 4. 业务审计查询

新页面使用真正游标分页，而非对旧 `/audit` 最多 1000 条结果作前端筛选：

- 客户：`GET /api/v1/audit-events`，服务端强制本人范围。
- 管理员：`GET /api/v1/admin/audit-events`，可加 `owner_id`；没有绕过管理员鉴权的通用参数。
- 每页默认 50，最大 100；精确 `action`/`actor_email`，`from` 含端点、`until` 不含端点，时间为 RFC3339 UTC、最多微秒精度。
- `(created_at DESC, id DESC)` 稳定排序，HMAC 游标绑定权限范围、筛选与页大小。修改筛选必须重新查询，篡改或不匹配返回 400；会话秘密轮换后旧游标失效。
- 首次查询时间是分页上界，不是长期 MySQL MVCC 快照；后续插入的回填历史事件仍可能改变可见集。提交/重置筛选从第一页建立新查询；当前页刷新/失败重试复用当前游标（第一页没有游标时取得新上界）。不能承诺跨页得到整个数据库同一事务快照。
- 页面时间明确为 UTC，详情按纯文本展示；鉴权过期/权限收回会清空结果并提示重新登录，不显示“没有日志”来掩盖失败。

迁移 `009_audit_query_indexes.sql` 新增三个查询索引，不改历史事件。部署前评估大表索引构建的时间、磁盘与维护窗口，先备份并在隔离库演练。中断可按严格索引定义恢复；同名异常索引拒绝继续。旧二进制可能拒绝 schema 9：程序回退必须先验证兼容，绝不通过删迁移记录或索引假装数据库回滚。

## 5. 验收边界

测试需分别覆盖 API 脱敏与完整响应、实际 MySQL 跨 1000 条分页/权限/索引恢复、实际 Nginx 头部和 syslog、浏览器窄屏/键盘/错误路径，以及目标主机的 journal 持久化、容量和重启行为。隔离 TLS 自签 fixture 不等于公共证书、systemd 安装或完整升级/恢复演练。当前实际执行与未验证项统一记录在 [VALIDATION.md](VALIDATION.md)，发布前不得省略。
