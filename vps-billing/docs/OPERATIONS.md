# 单服务器原生 Linux 运维手册

正式 CI 与发布门禁的部署基线仍为 Ubuntu 24.04 / amd64；Ubuntu 22.04 / amd64 为新增验证目标，不等于完整支持验收已完成，其他发行版与架构的边界见 [安装前置确认与兼容矩阵](INSTALL.md#前置确认)。采用原生部署，同一台服务器运行 Nginx、systemd API 和 Oracle MySQL 8.4 LTS。高可用暂缓。原生自动化的实际结果见 [原生验证记录](VALIDATION.md)；外部主机的资源、已有服务、真实域名、公共 CA 证书、开机恢复及运维计划需与该环境匹配。历史 `0.3.0-rc.1` 产物保持原提交和哈希。

初次安装见 [INSTALL.md](INSTALL.md)，宝塔环境见 [BAOTA.md](BAOTA.md)，升级回滚见 [UPGRADE.md](UPGRADE.md)，备份恢复见 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md)，站点与计价见 [SITE_SETTINGS.md](SITE_SETTINGS.md)。实际证据见 [VALIDATION.md](VALIDATION.md)，最终放行见 [RELEASE.md](RELEASE.md)。以下步骤供管理已安装的原生实例使用，不会由应用或 CI 自动在外部服务器执行。

## 1. 组件与边界

| 组件 | 原生路径或连接 | 权限及职责 |
|---|---|---|
| Nginx | 浏览器 HTTPS → `/opt/aster/current/web`，接口转发至 `127.0.0.1:18080` | 终止 TLS，静态资源与 API 同源；只暴露已审查入口 |
| SMTP 插件 | `/opt/aster/plugins/email.smtp/VERSION/plugin`、`aster-smtp.service` | 独立 `aster-smtp` UID、`aster-plugins` socket 组，root 审批后由宿主启动 |
| API | `/opt/aster/current/bin/aster-api`，`aster-api.service` | 无登录权限的 `aster` 身份；应用目录只读，日志写 journal |
| MySQL | 同机 Unix socket，路径按发行版核实 | 持久化业务数据；不公开数据库端口，不给 API 读取数据库数据目录 |
| 版本与配置 | `/opt/aster/releases/VERSION`、`/opt/aster/current`、`/etc/aster/api.env` | root 管理版本/链接，独立记录配置变更；配置不含秘密值 |
| 秘密 | `/etc/aster/secrets/` 与独立的运维秘密位置 | 运行身份仅读取运行秘密，不能读取迁移/备份身份秘密 |

单机不是故障域隔离：磁盘、系统或主机故障可能同时影响 API 和数据库。异地备份、恢复演练与明确维护窗口仍必需，不能用两个本机目录代替灾备。尚无容量测试或 SLA，不从示例配置推导可承载用户数。

当前原生方案以本机 Unix socket 连接数据库。后端要求显式设置 `MYSQL_ALLOW_INSECURE=true` 才接受无 TLS 的连接，且该例外会强制检查 DSN 使用绝对路径的本机 Unix socket；不能用于无 TLS 的 TCP。管理员仍须核对 socket 的真实位置、权限及数据库身份。若改成 TCP/跨主机，应先重新审查连接边界，使用证书验证的 `tls=true` 并关闭此例外，不能沿用明文设置。

运行数据库身份仅有本库 SELECT、INSERT、UPDATE、DELETE；迁移使用独立身份，DDL 不在日常 API 启动中自动执行。备份身份按实际选项审查权限，不共享运行凭据，不使用数据库 root 作为应用账号。

## 2. 配置和服务管理

Core 0.5.3 的新装和受支持实例升级优先使用 [安装](INSTALL.md)及 [升级](UPGRADE.md)中的管理员受限执行器。它管理专用 API/可选独立边缘 unit，实际执行初始化、迁移和激活；网页仅只读回执。本节的日常服务命令用于已核对实例的维护，不代替执行器计划、备份或迁移检查点。独立边缘与共享面板 Nginx 使用各自明确的管理方式，不能混用服务名。

原生配置样例包括 `deploy/systemd/aster-api.service`、`aster-smtp.service`、`api.env.example` 和 `deploy/nginx.native.conf`。插件安装/策略管理用 `deploy/scripts/plugin-manager.py`，明确服务操作由管理员执行。运行目录无须可写；MySQL 数据目录、journal 和备份由各自服务/运维身份管理。不要为解决读写错误把 `/opt/aster`、`/etc/aster` 或数据库目录递归改为可写。

`/etc/aster` 为 `root:root`、0755，非秘密 `/etc/aster/plugins.json` 由 root 管理；`/etc/aster/api.env` 为 `root:aster`、0640；运行秘密目录为 `root:aster`、0750，文件为 `root:aster`、0640。环境文件只保存 `*_FILE` 路径等配置，不作为 shell 脚本执行。MySQL DSN 文件必须与真实数据库用户密码同步；仅修改文件不会修改数据库账号。

审查配置后，可在维护窗口执行：

```sh
sudo systemctl status aster-api --no-pager
sudo journalctl -u aster-api -n 100 --no-pager
# 修改 unit 后才需要 daemon-reload；修改 api.env/秘密后需明确重启服务。
sudo systemctl daemon-reload
sudo systemctl restart aster-api
```

服务重启不是迁移、恢复或开机验收。MySQL 的 service 名称与 socket 路径取决于系统，先核实再配置启动依赖；`After=` 也不能证明数据库业务就绪。API 启动时校验配置和数据库连接，失败应保留错误并排查，不能自动切换到 demo。`Restart=on-failure` 有启动频率限制，不能代替就绪监控或故障响应。

完整启动、停止及重启服务器测试应在独立授权的窗口进行，记录 API/MySQL/Nginx 的启动顺序、恢复时间和业务结果。静态 unit 校验不等于系统已 enable 或重启后可用。

## 3. HTTPS、来源和静态文件

`APP_ORIGIN` 应与浏览器规范 HTTPS 来源精确一致；站点展示地址不控制监听地址、证书或 Origin。直接公开 HTTP IP 无法代替正常生产模式登录验收，`curl -k` 成功也不证明证书可信。

Nginx 在同机直接终止 TLS，只将 API 请求转发至回环地址。审查模板中的域名、证书、默认站点和 Host 限制，并核对已有全局 real_ip 配置及其他站点冲突。传给 API 的客户端 IP 必须来自可信连接对端，不能透传浏览器自填头。API 只信任精确回环代理来源；新增 CDN/负载均衡器时应重新设计完整信任链，不能扩大成所有私网或 `0.0.0.0/0`。

```sh
sudo nginx -t
# 仅在语法与冲突检查通过、证书存在且变更获准后 reload。
sudo systemctl reload nginx
curl --fail --show-error https://billing.example.com/healthz
curl --fail --show-error https://billing.example.com/readyz
```

实际 Nginx service 名称依发行版核实。模板指令兼容性检查、TLS 证书链/SNI/续期、伪造 Host/转发头拒绝都须实测。生产模式 API 已发送一年有效期的 HSTS；开放公众流量前应验证 HTTPS 并制定与静态站点一致的策略，不因 Nginx 未配置该头而误判为无 HSTS。不要为通过检查关闭浏览器安全设置。

网页根目录只能是 `current/web`；源码、配置、秘密、许可证清单、备份不能放进该目录。CSP 默认不允许外部脚本/字体和 inline script/eval；样式允许 inline 用于 Vue 动态样式。后续引入支付页面、分析或第三方脚本需单独审查 CSP 与数据传输，不直接改为通配符。

## 4. 健康、监控与故障判断

`/healthz` 表示进程存活，`/readyz` 检查数据库可用和完整 schema 编号/checksum；两者都不能证明业务数据正确。至少监控 5xx、延迟、登录失败、数据库连接、磁盘/inode、备份年龄和最近恢复结果、证书到期、迁移失败，以及服务反复重启。本项目未自动接入告警服务、外部值守或定时运维任务。

| 现象 | 优先检查 | 不应采用的绕行 |
|---|---|---|
| API 启动失败 | 秘密文件可读性、规范 Origin、socket 实际路径/权限、数据库用户及连接模式 | 切换 demo、打印 DSN、给整个目录开放权限 |
| 存活但不就绪 | 数据库状态、选定二进制的迁移编号/checksum、应用日志 | 修改迁移历史或忽略未知版本 |
| 登录/CSRF 失败 | 实际 HTTPS Origin、Cookie、Nginx Host 转发、系统时间 | 关闭 CSRF、改用公开 HTTP |
| 502/连接超时 | systemd 状态、回环监听、代理目的地址及数据库延迟 | 把 API 暴露到公网进行长期绕行 |
| SMTP 不可用 | 策略 revision/批准版本、SHA/兼容区间、不同 UID、socket 权限、服务退出/重启限制、精确目的地与后台配置 | 给插件读取 API 秘密、改 manifest 掩盖错误、盲目重发不确定邮件 |
| 磁盘空间不足 | 数据库/journal/备份占用和保留政策 | 随意删除 MySQL 文件、唯一备份或迁移记录 |

请求 JSON 采用字段白名单，业务审计仍可能包含客户/订单标识及取消原因；两者不是同一保留系统。请求 ID 追踪、后台真分页、Nginx 原始 engine 日志取舍、备份安全错误及 journald 容量/权限见 [生产日志与审计](LOGGING.md)。不收集完整环境变量、秘密文件或 DSN 到工单。journal 保留和磁盘限制应结合目标主机政策制定，应用不会自动修改整机日志配置，也不保证 journal 无损保留。

## 5. 备份、恢复和秘密轮换

按 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md) 的原生路径审查备份账号、MySQL client 凭据文件和一致性选项，使用 `backup-native.sh`。禁止 DDL/迁移与逻辑备份并行；备份内容是敏感数据，需经过批准的加密、离机复制、校验和保留策略。定时任务与外部存储尚未部署。

每次数据库结构变化前完成隔离恢复演练，核对迁移历史、表/行数、客户关系、金额币种、历史开具身份、幂等记录及审计/outbox。恢复到不同的实例或明确隔离的新库，不能覆盖运行库；先使用与备份匹配的二进制检查，再考虑升级。真实切换需停写、最终备份、差异处理与批准。

RPO/RTO 应由业务可接受的数据损失和恢复时间确定并实测。历史小型合成库恢复用时不适用于原生目标主机或真实规模，单机备份文件也不能保证异地灾备。

数据库密码轮换先设计数据库账号、受限 DSN 文件和 API 重启的协调步骤，再验证就绪和业务；不要只改文件后反复重启。会话秘密轮换应评估已有会话失效范围并验证；迁移/备份秘密单独管理。SMTP 密钥轮换须配合完整凭据重加密及对应数据库恢复点，不能只替换随机文件；见 [密钥备份与轮换](BACKUP_RECOVERY.md#smtp-密钥备份恢复与轮换)。Core 原始程序/资源、SMTP 独立程序/资源及真实 root 审批策略也需按版本保留；Core 0.5.0 的迁移 007 不可破坏性降级。记录引用和轮换时间，禁止把真实秘密提交到 GitHub 或复制进发布包。

## 6. 财务配置与升级

后台版本更新按 [UPDATES.md](UPDATES.md)检查固定官方 Release、创建明确审计交接，再由主机工具独立核验与暂存；API不持有root或service控制能力，暂存/交接与实际应用分别记录。

币种与套餐价格通过管理员“财务设置”维护，先审核代码/精度再逐币种定价；已有使用锁精度、未完成义务阻止停用，不直接 SQL 改状态绕过。默认值和显示符号不改变历史金额。检查点与长期财务缺口见 [财务模型](FINANCIAL_MODEL.md)。

升级必须遵循 [UPGRADE.md](UPGRADE.md)：保存已核验版本目录、配置版本与备份时点，停写后迁移，再原子切换 `/opt/aster/current`。当前不承诺滚动升级或向下迁移；旧二进制可能拒绝新 schema，因此应用目录回退不等于数据库回滚。

支付保持 `payment.disabled`；保留旧注册表默认 `EMAIL_PROVIDER=email.disabled`，官方 SMTP 独立受 root 策略、兼容插件服务、精确目的地、后台设置和本机密钥控制。默认关闭，可由管理员保存后主动测试，不自动发送业务通知。紧急停用、在途结果、受限出口、TLS 与审计要求见 [PLUGINS.md](PLUGINS.md) 和 [SECURITY.md](SECURITY.md)。未知 provider ID 仍拒绝启动，不自动下载代码；新插件须审查、独立构建与验收；已有 Core 可安装/替换兼容插件，无需重编 Core。单独更新遵循 [插件版本切换](PLUGINS.md#3-独立更新与回退)，不随意重启 API 或在线覆盖程序。

当前审计范围为单机财务核心；完整账本、续费退款与自动账单通知仍未完成。高可用、控制器、资源开通和支付渠道不属于本阶段实现范围，不自动扩展基础设施或付费能力。
