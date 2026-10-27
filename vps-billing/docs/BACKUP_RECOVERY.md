# 原生单机备份与隔离恢复

本文面向 Ubuntu 24.04 / amd64 单机原生部署：systemd 管理 API、Nginx 提供 HTTPS、MySQL 8.4 位于同一主机。备份通过本机 Unix socket 读取数据库。单机不等于高可用；本文不承诺 RPO、RTO 或 SLA，恢复时间和可接受数据损失必须通过目标环境演练确定。

本项目提供逻辑备份脚本与人工恢复步骤，没有自动计划任务、加密、异地复制、保留清理、binlog/PITR 或自动灾备切换。原生自动化使用隔离测试数据，实际结果及范围见 [原生验证记录](VALIDATION.md)。API 重启、同实例空库恢复、独立实例恢复和整机重启是不同验证项，不能相互替代。

## 范围与前提

`deploy/scripts/backup-native.sh` 用于本项目当前的 **InnoDB 基础表**：客户、会话、套餐与独立币种价格、订单、账单、站点、SMTP 与币种设置、独立价格 revision、金额/套餐快照、幂等请求、审计、outbox、迁移记录。它不是通用 MySQL 全实例备份工具，不备份系统账号、权限、其他库、服务器配置、日志或秘密。

脚本在导出前后只读查询真实 MySQL 8.4 的版本、厂商、server UUID、选中库、迁移记录及 `information_schema`。迁移必须是所选发行目录 SQL 的非空、连续、checksum 一致前缀，表和列清单须与该前缀匹配；未知版本、改动的 checksum、额外/缺失的表或列、部分执行的下一次迁移会停止备份。视图、非 InnoDB 表、触发器、例程或事件也会停止；导出明确跳过这些对象。**元数据可见性取决于账号权限**：DBA 必须先核对备份账号的 grants，并将可见对象清单与经授权的完整 schema 清单比较。看不见的对象可能无法被此检查发现，查询返回 0 不能代替权限审查。自定义数据库对象需另行设计、验证完整备份方案后才能使用。

表/列清单与发行 SQL 的匹配不等于完整 DDL 定义验证。脚本另外记录列类型/默认值、约束、索引及外键的前后观测值，要求导出前后不变，但不据此证明所有定义均与发行源码一致，也不验证业务数据。可另用同版本 API 的 `-preflight` 核对必要物理定义；它仍不替代 DBA 审查和隔离恢复。

备份账号独立于 API 与迁移身份。按实际 MySQL 版本审查本库的 SELECT、SHOW VIEW、TRIGGER、EVENT 及全局 SHOW_ROUTINE 元数据可见权限；这些权限用于完整识别对象，不把备份凭据交给 API，也不默认使用 root。若不接受所需元数据权限，就停止此流程并采用经 DBA 审查的替代备份方案。脚本不会创建账号或授予权限。

导出使用 `--single-transaction --quick`。备份期间禁止迁移和其他 DDL；所有业务表必须是 InnoDB。前后观测相同不能防止两次查询之间发生再恢复原状的 DDL，也不构成迁移锁。它不保证跨库、外部系统或文件的一致性。相关选项与权限要求见 [MySQL 8.4 mysqldump 官方说明](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html)。

Core/插件发布包、源码提交、配置版本、非秘密插件策略/manifest、TLS 证书和必要秘密另行受控备份，秘密单独加密保护，保证能恢复对应版本。不要把数据库备份、凭据或秘密引用清单上传到 Git、Release、网页根目录或普通日志。

## 准备私密凭据与目录

需要 Bash、GNU coreutils、Python 3、gzip、与服务器兼容的 MySQL 8.4 `mysql`/`mysqldump`。使用受控系统 PATH，不从当前目录或可被其他用户修改的目录加载命令。

由管理员预先准备专用备份操作身份及两个目录，例如 `/etc/aster-backup/`、`/var/backups/aster/`；后者必须已经存在、归执行脚本的账号所有、权限为 0700。脚本不创建输出根目录，不调整现有目录权限，也不删除旧备份。目录及其祖先必须受可信管理员控制，不能让其他用户替换路径。

Core 0.5.2 的备份脚本在写入数据前，只清除本次新建 0700 暂存目录继承的默认 POSIX ACL，并确认已清除；执行器为旧版已核验脚本新建的私密备份输出目录采用同样保护。不会修改输出根目录、已有备份或历史发行脚本。默认 ACL 会参与子文件权限计算，不能只依赖 `umask 077`。只有确认默认 ACL 不存在，或文件系统明确不支持该 ACL，才继续；其他错误停止本次备份，不发布完成目录。已有目录的访问 ACL 与运营者权限仍须独立审查。相关语义见 [Linux ACL 说明](https://man7.org/linux/man-pages/man5/acl.5.html)。

凭据文件例如 `/etc/aster-backup/client.cnf`，必须是执行账号所有的普通文件、0400 或 0600、无符号链接或额外硬链接，所有祖先目录必须归执行账号或 root 所有且不可被组或其他用户写入；仅对 root 所有的 sticky 目录（例如 /tmp）允许受保护的私密子目录。不要复用 API 的 DSN 文件。

凭据文件仅允许：

- 一个 `[client]` section。
- 一个 `user=` 项：实际已创建的备份账号，1–32 个 ASCII 字母、数字、下划线、点或连字符，首字符不为点或连字符，不加引号。
- 可选的 `password=` 项：从批准的秘密管理途径写入真实密码，遵循 MySQL option-file 的引号和转义规则；使用经审查的本地认证方式时可省略。
- 空行和以 `#` 或 `;` 开头的独立注释行。

不支持 include、其他 section、重复选项或 `host`、`socket`、`force`、`init-command` 等附加项。socket 由脚本参数明确指定，传输固定为 SOCKET；脚本将 `--defaults-file` 放在客户端第一个参数，禁用 login-path，清除相关 MySQL 环境变量。密码不会进入命令行或归档。不要使用 `bash -x`、打印凭据文件或把含秘密的诊断粘贴到公共日志。

## 创建与检查备份

先按 [安装手册](INSTALL.md)独立核验当前安装的同版本程序与资源来源，并解析出具体发行目录。`--release-directory` 是必填参数，须指向已组装、管理员控制的完整 Core 目录，包含 `manifests/BUILD.json` 与 `migrations/`，不能填候选新版本或临时手写的 BUILD。脚本核对 Core 版本、源码提交、平台、分发格式、完整迁移映射及本地 SQL 摘要；当前 BUILD 并不为每个 SQL 提供独立摘要，路径映射和本地文件自身不能证明发行来源可信。不能为通过检查而编造提交、版本、迁移或 metadata。

核对来源库名、socket 和备份账号后，由备份操作身份执行。另行核对 socket 及其父目录归获批准的 MySQL 服务身份管理，其他用户不能替换；脚本不会把 mysql 所有的正常 socket 路径误判为必须归备份账号所有，也不替代此目标核对。下列路径、`VERSION` 和 `aster` 是需要按已批准环境核实的示例，不会自动安装或启动任何服务：

```sh
/opt/aster/releases/VERSION/deploy/scripts/backup-native.sh \
  --client-config /etc/aster-backup/client.cnf \
  --socket /run/mysqld/mysqld.sock \
  --database aster \
  --output-dir /var/backups/aster \
  --release-directory /opt/aster/releases/VERSION
```

库名只允许 1–64 个 ASCII 字母、数字和下划线，首字符为字母或下划线；系统库不允许导出。脚本只连接本机 socket，不能用于远端数据库。所有路径必须为绝对路径。

每次执行创建一个新的私密目录，例如：

```text
aster-aster-20261014T000000Z-RANDOM/
  database.sql.gz
  BACKUP.txt
  BACKUP.json
  SHA256SUMS
```

`BACKUP.json` 自动生成并采用更新工具接受的 schema 1，精确包含 `schema`、`source_commit`、`core_version`、`database`、`schema_version` 与 `migration_checksums` 六项。提交与 Core 版本来自已核验的当前发行 BUILD，库名与 schema 版本来自实际目标，checksums 为从 `"1"` 到实际当前版本的真实完整映射；较旧的合法前缀不会被写成候选版本。无需人工构造 JSON。metadata 不宣称 SQL 内容已验证或隔离恢复已通过。

`SHA256SUMS` 精确覆盖 `database.sql.gz`、`BACKUP.txt` 与 `BACKUP.json`。时间加随机后缀区分同秒并发执行。临时目录内完成导出、前后 schema/发行输入一致性、压缩检查和校验和检查后，整个目录在同一文件系统中原子发布；目标已存在则拒绝覆盖。正常错误或可捕获信号会清理本次临时目录，不触碰已有备份。断电、SIGKILL、磁盘或文件系统故障仍可能留下 `.partial` 目录；它们不是已完成备份，应保留现场后人工检查，不得据文件存在推定成功。原子可见性也不等于断电持久性保证。

所有备份目录为 0700，文件为 0600。SQL、配置或数据库诊断不会输出到控制台；成功时的 mysqldump 警告仅记录 `dump_warnings=yes`，原始 stderr 不发布。如果出现警告，应通过受控的私密诊断核实原因后再接受该恢复点。

将整个目录复制到受限离机位置后，从该目录验证。校验清单只含 basename，移动目录后仍有效：

```sh
cd /private/copied-backup/aster-aster-TIMESTAMP-RANDOM
sha256sum --check SHA256SUMS
gzip -t database.sql.gz
```

归档是**未加密的敏感数据**，包含会话状态和客户资料。按运营策略尽快加密、异地复制，并验证解密和密钥恢复方法。SHA-256、gzip 与自动 metadata 检查不证明备份 SQL 内容、业务可恢复或已完成隔离恢复；同一主机或同一磁盘的副本不能承担主机故障时的恢复责任。不要在共享终端直接输出解压后的 SQL。用于 [更新交接](UPDATES.md)时，传入此目录的 `database.sql.gz`、对应真实 SHA-256 和 `BACKUP.json`；若工具要求 root 私密副本，保留原备份身份管理的恢复点，另复制经核对的文件，不递归改动原目录属主。

## Core 0.5.2 升级执行器的备份门禁

确认式 `core-operation.py` 升级在本站维护门禁生效、API 已停止后，调用已核验旧发行目录的备份脚本创建一个新的私密备份。输出保存在本次 root 操作目录的 `backups/` 下，核对实际 SQL gzip、摘要、自动 `BACKUP.json`、原版本/提交和完整迁移前缀。不要以候选目录冒充备份来源，或手写 metadata 替代真实检查。

升级规格必须明确 `acknowledge_backup_restore_limits=true`。这是管理员确认理解文件检查的限制，不是工具授予“已隔离恢复”的标志。管理员仍须完成本节规定的恢复演练、业务校验和时点/RPO 判断，保留旧程序、配置、插件及匹配密钥。备份脚本的元数据可见权限仍按前述范围审查；全新安装执行器会授予备份账号本库 SELECT/SHOW VIEW/TRIGGER/EVENT 和全局 SHOW_ROUTINE，后者的例程元数据可见范围不限于本库，管理员须在确认安装计划前接受。新实例生成的备份凭据由独立 root 私密操作资料保存，不能给 API。

执行器不会向任何数据库自动导入备份，也不删除新表/列或倒写迁移记录。失败后先查原 `PLAN.json`、`JOURNAL.json`、链接、实际 schema 和服务状态；只有实际旧 schema 仍兼容且旧程序就绪通过时才可回退程序。迁移结果不明或不兼容时保留维护，向前修复或按下述隔离恢复流程单独批准。不能删 journal、换 ID 重跑或把 gzip 验证当作安全数据库回滚授权。具体 CLI 见 [UPGRADE.md](UPGRADE.md)。

## 手工隔离恢复：先核实目标，再导入

这里不提供自动覆盖数据库的恢复脚本。恢复只允许导入可信、已核验的 SQL；SQL 本身能够执行操作，路径、校验和与文件名不是安全沙箱。

1. 保存备份来源提交、schema 版本、MySQL 版本、备份时点、文件大小与 SHA-256；记录演练负责人及开始时间。
2. 准备独立的原生 MySQL 测试实例，使用独立数据目录、socket 和端口，不对外公开，不复用运行实例的数据目录。由 DBA 创建全新的空恢复库及仅限该库的恢复身份；关闭测试实例的 event scheduler。仅使用同一实例的新库时，需要额外核实权限边界和资源影响，不能将其等同于完整实例隔离。
3. 单独准备恢复身份的 0600 `client.cnf`；只含 `[client]` 的 user/password。实例管理员先完成建库、授权和对象清单核对。恢复身份不应拥有运行库、系统库或其他库的写权限。
4. 对照来源记录确认新目标的服务器标识、socket、库名与运行目标不同。使用人工核对的恢复账号执行只读检查；出现错误或不匹配立即停止。

```sh
# 路径和库名均为示例，先换成已准备的隔离目标。
# --defaults-file 必须放在第一个参数；不要加入 --force。
mysql --defaults-file=/private/restore/client.cnf --no-login-paths \
  --protocol=SOCKET --socket=/run/mysqld-restore/mysqld.sock \
  --batch --database=aster_restore_review \
  --execute='SELECT @@hostname, @@port, @@socket, @@server_uuid, VERSION(), CURRENT_USER(), DATABASE(); SELECT COUNT(*) AS existing_objects FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE();'
```

`existing_objects` 必须为 0，同时由 DBA 确认没有例程、触发器、事件等既有对象，恢复目标已停止其他写入。确认恢复身份的元数据权限足够，不以权限不足造成的“空清单”当作空库证据。若已有任何对象，另建新的空库，不删除或覆盖未知数据。

5. 从已检查的备份目录执行导入，保持 `pipefail`。当前脚本导出不生成来源库的 CREATE DATABASE/USE，且禁用 DROP TABLE；这些选项不能代替目标核对或 SQL 来源审查。其他历史导出文件可能含 DROP、USE、库限定名称或任意 SQL，不能直接套用此流程。

```sh
set -euo pipefail
cd /private/copied-backup/aster-aster-TIMESTAMP-RANDOM
sha256sum --check SHA256SUMS
gzip -t database.sql.gz
# 此命令会向已确认的空恢复库写入数据；不能指向运行库。
gzip -dc database.sql.gz | \
  mysql --defaults-file=/private/restore/client.cnf --no-login-paths \
    --protocol=SOCKET --socket=/run/mysqld-restore/mysqld.sock \
    --database=aster_restore_review
```

导入中断可能留下部分对象，SQL 恢复并非整体事务。不要用 `--force` 忽略错误或向部分恢复库反复导入；保留诊断，重新准备另一空目标再执行。不要通过 DROP 运行库或覆盖其文件来“修复”恢复失败。

## 数据校验与切换门禁

先用与备份匹配的二进制检查副本，不要立即执行新版本迁移。测试 API 使用单独配置和端口，DSN 只指向恢复库；保留 `PAYMENT_PROVIDER=payment.disabled`，**不设置 `SMTP_ENCRYPTION_KEY_FILE`，不加载启用的插件策略、不启动 SMTP 服务**，不连接真实控制器。`EMAIL_PROVIDER=email.disabled` 不能关闭恢复库中的受管理 SMTP；缺少密钥或有效插件审批都会阻止发送；恢复验证同时使用关闭策略和出口限制，不仅依赖一个环境变量。恢复出的会话也属于敏感数据，测试站点不能公开。

至少检查：

1. 数据库更新记录与 checksum 和备份来源版本、提交一致。先按该恢复点对应版本的完整对象清单、表计数和数据哈希核对，旧版恢复点使用其原始清单；不要先套用新版本结构或执行升级。验证副本可由匹配版本读取后，再单独演练目标版本升级。
2. 客户、订单、账单之间的关联正确；每个登记币种分别核对整数金额及对应精度，不能混加；检查全新 CNY-only 或升级保留 USD 的实际来源。历史金额、币种格式、开具方和已有套餐快照没有被当前设置重写，旧缺失套餐快照不补造。
3. 原幂等键能返回原订单，冲突请求仍拒绝，不新增重复订单、账单、审计或 outbox。
4. 两个客户的资源隔离、管理员权限、设置版本冲突、CSRF、无效或过期会话、禁用支付均符合预期。
5. 核对币种登记/默认项、价格 revision、精度锁及未完成义务计数；停用不能绕过旧账单/订单/服务。
6. 在副本执行升级与重复迁移并核对新功能；API 运行身份无法 CREATE、DROP、ALTER 或 GRANT。

文件检查、SQL 导入成功、业务校验、原生服务开机恢复是不同证据。原生自动化将 SQL 导入同一 MySQL 实例中的另一全新空库，比较完整基础表数据并切换 API 到恢复库执行 HTTPS 回归；它不重启数据库进程或整机，也不验证独立实例、离机加密副本、定时备份或告警。实际完成结果见 [原生验证记录](VALIDATION.md)。目标环境的启动顺序、断电/重启恢复和独立实例演练应分别留存证据。

正式切换需要单独的停写窗口与批准：保存失败现场及当前数据副本，确认恢复点之后全部写入的处置方式，审查会话是否需要统一失效，再按 [升级与回滚](UPGRADE.md) 更换受控 DSN。不能把一次恢复测试视为自动切换授权。

## SMTP 密钥备份、恢复与轮换

数据库保存 SMTP 连接信息和 AES-256-GCM 凭据密文；`backup-native.sh` 不备份 `/etc/aster/secrets/smtp_encryption_key`。只恢复数据库不能恢复用户名与密码，只有密钥也无法还原配置。将原始 32 字节密钥存入独立受控、可解密的秘密备份，与相应数据库恢复点建立版本对应关系；不要把两者合装进普通备份目录、源码或 Release。保留期必须覆盖仍需恢复的数据库备份。

恢复时先省略密钥、关闭运行时插件策略且不启动 SMTP 服务，再启动隔离 API，限制网络出口，将恢复库的 SMTP 设置保存为关闭。随后将与该备份匹配的恰好 32 字节原始密钥恢复到新的受控普通文件，采用实际 API 运行身份所有、0400、无软/硬链接且祖先可信不可共享写入的路径，确认独立 worker 不可读取；完整约束见 [安装手册](INSTALL.md#可选smtp-加密密钥)。配置文件引用后再重启。管理清单的 `configured=true` 才表示配置有效且凭据可解密；`key_configured=true` 只证明某个密钥已加载。保持 `enabled=false`，无需向真实收件人发送邮件来验证解密。测试网络继续隔离，副本中的会话和凭据不能公开。

没有自动密钥轮换或历史密文重加密。计划轮换时：

1. 在管理设置中关闭 SMTP，确认在途测试结果；保存匹配的数据库和旧密钥备份，停止 API。关闭设置不会取消已经读取旧配置的在途请求。
2. 按首次生成要求创建一个**新路径**的 32 字节密钥，不覆盖旧文件；修改环境引用并重启 API。旧密文此时不可解密属于预期状态，不能用开关绕过。
3. 管理员保持关闭状态，同时重新输入用户名和密码，保存后形成新密钥加密的完整凭据；或同时清除两项并保持关闭。不能只更换其中一项来恢复不可解密配置。
4. 核对清单 `configured`，按需要启用并主动测试一次；先确认结果再决定后续操作。创建新的对应备份，按保留政策管理旧密钥，不因当前配置恢复就删除旧恢复点所需密钥。

若密钥丢失，不能从数据库推导或恢复原凭据。先禁用发信，恢复受控密钥备份；确实无法恢复时，需在服务商处重新取得或轮换 SMTP 凭据，再按上述完整替换步骤重建。重启、重新随机生成密钥或反复测试不会修复旧密文。疑似泄露时还需撤销服务商凭据；只换本机加密密钥不撤销已经外泄的用户名/密码。

## 插件程序与策略恢复

数据库不保存程序审批或 Unix UID。恢复主机时分别核验 Core/SMTP 包来源和摘要，以安装工具重新安装对应版本，重建无登录身份及共享 socket 组；UID 可能与旧机不同，不能直接复制旧策略后启动。根据新的 UID、socket 和获准目标明确 `select`，先保持关闭，核对程序/manifest 哈希和兼容区间。旧版程序、策略变更记录和包校验信息必须与恢复点对应。

插件包不含凭据，加密密钥仍只交给 Core。按上节确认数据库凭据可解密、配置有效且后台关闭后，才批准目标并启动兼容插件；主动握手不发信。恢复演练无需对真实邮箱发送测试。回退插件不是数据库回滚，也不能使丢失密钥恢复或撤回已接受邮件。

## 定期执行、保留与演练证据

在首次真实备份与隔离恢复验收后，再由管理员配置受控的 systemd timer 或其他调度，并为失败、缺失恢复点、磁盘不足与离机复制失败建立告警。脚本本身不会安装或启用计划任务，也不负责自动清理。

保留策略应根据业务写入频率、容量与恢复要求确定。删除前确认近期恢复点已实际演练且存在可解密的异地副本；不要让新的未验证备份替换唯一已知可恢复副本。记录恢复结束时间、实际耗时、数据检查结果、未通过项和后续行动，用演练结果确定目标 RPO/RTO。

源码附带 `deploy/scripts/test-backup-native.sh`：使用 fakebin 内的模拟 mysql/mysqldump，并将系统根目录属主建模为标准 root（私密文件和下层目录权限使用真实临时文件系统检查），验证并发目录、原子不覆盖、私密权限、配置约束、发行输入变更、schema 前缀/部分执行/导出期间变更、自动 JSON 与精确校验清单、dump/gzip/checksum 失败及可移动校验和。模拟查询拒绝非 SELECT；这些测试不连接数据库、不创建真实账号、不启动服务，也不证明真实 MySQL 或恢复验收通过。真实结果另见 [验证记录](VALIDATION.md)。当前版本仅提供原生部署备份流程。
