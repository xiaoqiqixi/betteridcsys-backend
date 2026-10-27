# 宝塔面板原生部署与更新

> **先核对选定正常发行。** 本页对应Core 0.5.10；只有正常Release、完整六资产及来源校验通过才可操作，公开prepare不等于宝塔目标环境已验收。旧v0.5.6–v0.5.9的新prepare/stage仍因来源分叉拒绝；旧目录必须保留真实收据，不能伪造。见[历史影响](../../docs/HISTORY_REWRITE.md)、[版本合同](RELEASE-v0.5.10.md)与[公开验收](../../docs/CORE_PUBLIC_PREPARE_ACCEPTANCE.md)。

本指南用于已安装宝塔 Linux 面板的单服务器：复用面板管理的 Nginx 和 MySQL，静态前端由 Nginx 提供，Go API 由 systemd 独立管理。应用验证基线为 Ubuntu 24.04 / amd64、MySQL 8.4 LTS；宝塔面板本身及其版本组合未在本项目 CI 安装验收。实际工程结果见 [原生验证记录](VALIDATION.md)。

本页按Core 0.5.10说明目标配置；须核对正常Release、构建与公开验收，不以历史版本绿灯代替本版。使用前仍须独立审查宝塔实际服务、权限、配置及恢复点，不能把隔离native CI算作这台宝塔主机验收。

宝塔官方公开说明提供网站、文件管理、SSL、数据库和反向代理能力；具体入口以已安装版本为准。以下 Nginx/systemd 配置是本项目的适配方案，不要求安装付费插件。没有面板的主机应先按 [宝塔官方安装说明](https://www.bt.cn/bbs/thread-19376-1-1.html) 单独规划面板安装；已有网站或数据库的服务器不能直接运行新装面板/重装环境脚本。

## 1. 核对已有环境

记录面板版本、系统、CPU 架构、Nginx/MySQL 实际版本、服务名称、可执行文件和配置路径，以及 80/443/18080 端口使用情况。不要在宝塔管理的环境中再执行普通 Ubuntu 安装手册里的 `apt install nginx mysql-server`，也不要卸载或接管同机其他服务。

```sh
cat /etc/os-release
uname -m
sudo ss -ltnp
systemctl list-units --type=service --all
```

MySQL 必须确认为 8.4 LTS；软件列表可选版本不等于现有实例已升级。用现有受控数据库管理连接查询 `SELECT VERSION(), @@socket, @@datadir;`，记录实际 socket。旧 MySQL/MariaDB 的数据应先在隔离副本验证迁移，不能在共享实例上直接切版本。应用不需要 PHP、Redis、Node 常驻进程或第二套 Nginx。

为本应用分配独立域名，例如 `billing.example.com`。下面所有域名、VERSION、路径和账号都是需要替换的值；保留原有站点、证书、数据库和管理入口。

## 2. 准备同版本程序与网页

目标 Core 0.5.10 的同版本正常 Release 应包含六项资产：原始 API、插件宿主、资源归档及 `BUILD.json`、`PROVENANCE.json`、`SHA256SUMS`。资源包含编译好的网页和主机工具，服务器无需 Go/Node.js。按 [安装手册第 1 节](INSTALL.md#1-核对来源与归档)核对官方来源、资产摘要、构建提交/run，并将已核验且审查过的 `core-manager.py`、`core-operation.py`、`update-manager.py`、`plugin-manager.py` 引导到 root 保护目录。旧正式工具不会自动认识后续的完整策略清单，不能直接代替此次引导。

使用普通受控上传途径时，把六资产放在网站目录以外，再由管理员核对为 root 所有、不可共享写入、无链接的独立输入目录；不要让面板 Web 身份执行 root 工具。以下 socket 是宿主机实际路径，须替换：

```sh
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-manager.py doctor \
  --release-directory /opt/aster/releases/0.5.10 \
  --socket /actual/mysql.sock --listen-address 127.0.0.1:18080
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-manager.py prepare \
  --version 0.5.10 --assets-directory /var/lib/aster-install/core-0.5.10
```

省略 `--assets-directory` 时工具自行从固定官方来源下载六资产；两种方式都独立校验当前官方 Release、源码/构建来源和完整文件树，再自动组装为新的完整版本目录，不需要手工复制 API、插件宿主或前端。`doctor` 只读主机事实；面板工具不在标准 PATH 时可能提示缺失，须独立核对其真实路径。它不运行 Nginx/MySQL、不连接数据库、不验证面板兼容性。

准备完成后的路径为：

```text
/opt/aster/releases/VERSION/bin/aster-api
/opt/aster/releases/VERSION/bin/aster-plugin-host
/opt/aster/releases/VERSION/web/index.html
/opt/aster/releases/VERSION/web/assets/
/opt/aster/releases/VERSION/manifests/PREPARED.json
/opt/aster/current -> /opt/aster/releases/VERSION
/opt/aster/plugins/email.smtp/PLUGIN_VERSION/
/etc/aster/api.env
/etc/aster/plugins.json
/etc/aster/secrets/
```

`status=prepared` 仅表示新目录已准备；本地 `manifests/PREPARED.json` 是运维回执，不是官方资源资产。工具不创建账号/秘密、配置面板、建库、迁移、初始化管理员、切 `current` 或启动服务。后续由获准管理员准备私密规格、审查计划并确认 `core-operation.py`，再由执行器完成受支持实例的身份、配置、数据库、管理员、服务与 HTTPS 检查；出现已有版本、校验失败或发布后持久化不确定时保留 `CORE_PREPARE.jsonl` 与回执，先查现状再处理。

需要自行构建时，另在独立构建环境按 [发布脚本](RELEASE.md)从同一已审查提交生成完整发行输入。自定义内容不得冒充未修改的官方资产；`prepare` 只接受固定官方 Release，不提供任意包或下载 URL。网页仍只取已编译 `web/`，不要把资源根目录作为站点根。

版本目录、文件和链接由 root 管理；API 与 Nginx 仅读取。网站根目录只使用 `/opt/aster/current/web`。不要把完整源码、压缩包、数据库备份或 `/etc/aster` 放入可下载目录，也不要把版本目录递归改成面板 Web 用户所有。新前端与后端按同一提交一同发布；自定义前端构建不得继续使用官方包原有校验和声称内容未变。

## 3. MySQL、秘密与 systemd

复用已核实的 MySQL 8.4 实例和实际 Unix socket，不安装第二套数据库。全新安装要求本应用数据库及三个同名前缀账号不存在，由执行器在确认后创建独立 runtime/migrator/backup 身份；运行账号仅本库 DML。不要预先通过面板创建一个全权限用户再冒充全新安装，也不要更改共享实例 root 密码或开放远程访问。已有库/账号/配置转入 [升级](UPGRADE.md)，不能删除后重新安装。

按 [安装手册第 2 节](INSTALL.md#2-数据库与秘密)准备 root 私密 DBA client 文件、管理员密码文件和已核验 TLS 文件。秘密不进入 argv、shell history、面板网站上传目录或普通日志。规格设置实际 `origin`、回环 `listen_address`、数据库/socket、root 保护的真实 MySQL/Nginx 程序路径；本机 socket 明确使用 `MYSQL_ALLOW_INSECURE=true`，不扩展到明文 TCP。支付与 SMTP 均默认关闭。独立运行时支付插槽 `aster.payment/1` 自 Core 0.5.3 起提供，第三方渠道按[支付插件接入指南](PAYMENT_PLUGINS.md)完成适配、独立构建与验收，无需重编 Core。启用前须核验程序及 manifest、明确批准商户和身份，再运行专用 service；不通过面板上传或只改环境变量安装。

API 仅由专用 systemd unit 管理，不添加面板进程守护、定时重启或第二份启动命令。执行器确认后生成受支持的 unit/配置、迁移并引导管理员；不接管任意自定义 unit/drop-in。已有 0.5.1 实例须通过回执、unit、配置/权限、schema 和插件门禁才可接纳。

面板 socket 位于 `/tmp`、依赖自定义 namespace 映射或服务/宿主 DSN 使用不同路径时，不属于当前规格的单一 socket 自动流程。先由数据库/主机管理员规划可验证的兼容路径，或采用独立审查的迁移方案；不能关闭 `PrivateTmp`、放宽权限或把两个不同目标写成同一 socket 以通过检查。

SMTP 1.1.0 继续独立安装、审批和运行，不是 Core 安装的隐式动作。worker 使用不同 UID 和共享 socket 组，不得进入可读 API 秘密的组。宝塔的 MySQL socket/数据目录可能不同，按 [插件指南](PLUGINS.md)审查实际不可达路径与 service namespace；不能因模板隐藏标准路径就声称面板数据库已隔离。普通升级不重新生成 SMTP 加密密钥。

## 4. 网站、HTTPS 与 Nginx

在面板的网站管理中为该域名建立独立静态 HTML 站点，网站根目录设置为 `/opt/aster/current/web`。使用站点证书功能配置匹配域名的有效证书，并核对证书链、到期时间和续期方式；站点 HTTPS 与面板登录 HTTPS 是两项独立配置。宝塔的 [官方功能与更新说明](https://www.bt.cn/bbs/thread-19376-1-1.html) 包含 HTML 站点与 SSL 管理；界面布局可能随版本变化。

编辑**本应用站点**的 Nginx 配置，先保存原文件。保留面板生成的证书路径、ACME 验证目录、证书续期 include 和必要安全规则。不要将 `deploy/nginx.native.conf` 的整个多站点文件粘贴到面板站点中：其中 `http` 级日志格式与 `default_server` 需要全局规划，直接套用会与已有站点冲突。

在本域名的 HTTPS `server` 中合并以下配置，并处理已有重复的 `root`、`location /`、缓存和反向代理规则。`/api/` 只能有一套代理配置；不再通过面板新增覆盖全站 `/` 的第二套反向代理。

```nginx
root /opt/aster/current/web;
index index.html;
autoindex off;
client_max_body_size 64k;
server_tokens off;
# 本站点先不继承可能包含 query 的默认访问日志。
access_log off;
if ($host != billing.example.com) { return 444; }

# 证书、HTTPS 与续期方案确认可用后启用；只作用于本域名。
add_header Strict-Transport-Security "max-age=31536000" always;
add_header X-Content-Type-Options nosniff always;
add_header X-Frame-Options DENY always;
add_header Referrer-Policy no-referrer always;
add_header Permissions-Policy "camera=(), microphone=(), geolocation=(), payment=()" always;
add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'" always;

location ^~ /api/ {
    if (-f /etc/aster/maintenance) { return 503; }
    proxy_pass http://127.0.0.1:18080;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host "";
    proxy_set_header Forwarded "";
    proxy_connect_timeout 5s;
    proxy_send_timeout 20s;
    proxy_read_timeout 30s;
    proxy_buffering off;
    proxy_cache off;
    proxy_hide_header Strict-Transport-Security;
}
location ~ ^/(healthz|readyz)$ {
    proxy_pass http://127.0.0.1:18080;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host "";
    proxy_set_header Forwarded "";
    proxy_connect_timeout 5s;
    proxy_read_timeout 30s;
    proxy_cache off;
    proxy_hide_header Strict-Transport-Security;
    access_log off;
}
location ^~ /assets/ { try_files $uri =404; expires 7d; }
location = /index.html { expires -1; }
location / {
    if (-f /etc/aster/maintenance) { return 503; }
    try_files $uri $uri/ /index.html;
}
```

`proxy_pass` 保持无尾斜杠，保留 `/api/` 前缀；SPA 深层页面回退到 `index.html`，不存在的静态文件返回 404。两者语义见 [Nginx proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass) 与 [try_files](https://nginx.org/en/docs/http/ngx_http_core_module.html#try_files)。保留针对隐藏文件的拒绝规则，但不要拦截面板用于证书验证的 `/.well-known/` 路径。

该方案假定 Nginx 直接接收浏览器连接。检查面板或插件是否配置了全局 `real_ip_header` / `set_real_ip_from`，不要信任任意来源的转发头。API 只信任精确回环代理；增加 CDN/WAF/前置代理须另行设计可信链，不能直接扩大为所有私网。不要更改 Cookie 的 Secure/HttpOnly/SameSite 属性或缓存 API。

HTTP 站点只保留面板现有的证书挑战 location/include 和固定域名跳转，不在 HTTP 下提供登录/API。下面片段合并到**仅监听 80 的本域名 server**，用于替换该站已有的普通 `location /`；若面板把 80/443 放在同一个 server，先只为本应用拆分 HTTP 与 HTTPS，不能把这段放入共同的 HTTPS location。不要增加 `default_server` 或修改其他站点。

```nginx
access_log off;
# 保留面板现有的 ACME 挑战 location/include；它应优先处理验证路径。
location / { return 308 https://billing.example.com$request_uri; }
```

HTTPS 片段统一设置一年 HSTS，仅限当前域名，不添加 `includeSubDomains` 或 `preload`；代理位置隐藏上游同名头，避免重复。启用前确认真实证书与续期方案，不能用关闭浏览器证书验证来完成检查。

访问日志应使用不含 query、Cookie、Authorization 和请求体的专用格式；该 `log_format` 只能在 Nginx `http` 层声明。无法确认格式前，可仅对本应用站点关闭 `access_log`，保留受限错误日志及 API journal；不要变更其他站点的日志策略。

上面的维护文件必须与规格 `config_root` 一致；`/api/` 与 SPA 入口在文件存在时返回 503，`/healthz`、`/readyz` 保留用于工具检查。门禁只控制本站请求，不阻止外部 SQL 作业，管理员须另行停写/禁 DDL。不要把门禁加到共享全局 server 或其他站点。

宝塔使用 `edge_mode=external`，不启动第二个 managed Nginx。规格额外提供实际 `nginx_binary`、其已审查 `nginx_sha256`、完整主配置 `external_nginx_config` 与 `external_nginx_sha256`；例如程序路径可能是 `/www/server/nginx/sbin/nginx`，必须以本机事实为准且全部祖先受 root 保护。现有配置应由管理员预先合并本站的 root/upstream/TLS/维护规则，完整配置直接包含可核对的本站路径、上游和维护文件。仅指向未知 include 的配置不等于已验证完整配置，不能通过加注释、改摘要或删检查绕过；面板自动生成/复杂 include 布局不能满足检查时停止，不声称支持自动接管。

执行器只对明确指定的程序/完整配置运行固定 `-t` 和 `-s reload`；它不调用面板 root API、不重写本站外的配置、不安装或替换面板 Nginx。确认计划前须审查所有 include、TLS、证书续期、其他站点及 reload 影响。配置或面板自动重写导致摘要变化时重新审查；不能误测系统另一份 `/usr/sbin/nginx`，也不关闭整机防护来通过门禁。

## 5. 上线核验与更新

```sh
curl --fail --show-error https://billing.example.com/healthz
curl --fail --show-error https://billing.example.com/readyz
curl --fail --show-error --head https://billing.example.com/
```

仅在真实 HTTPS 就绪、证书与网页资源通过、首位管理员登录成功后，交付实际 `APP_ORIGIN` 对应的 `https://billing.example.com/` 入口。`prepared`、只读检查和一次 service start 均不构成完成上线。

核对浏览器证书可信、静态资源和 SPA 刷新、登录/注销、Cookie/CSRF、客户隔离、管理员设置及运营者已配置币种的独立价格和历史格式快照。全新安装仅启用且默认使用 CNY；升级保留既有默认值、独立报价与历史金额，不自动换汇。更改展示域名不会修改 `APP_ORIGIN`。API 和数据库不公开监听。支付默认关闭，核对未配置渠道的结账请求被安全拒绝；自定义支付部署按[接入指南](PAYMENT_PLUGINS.md)另行审查。未使用 SMTP 时不配置其加密密钥。SMTP 配置不依赖面板邮件插件，不安装或修改面板邮局服务；测试仅连接已获准且证书有效的 SMTP 目标。具体项目见 [验收范围](ACCEPTANCE.md)。

后台版本入口查询官方 Release 并创建审计交接，见 [UPDATES.md](UPDATES.md)。它不调用面板 root、文件管理或进程守护接口。网页可读取独立 root 导出的脱敏实际回执，不能读取私密 journal、触发 sudo 或将交接当作安装成功。

新安装按 [INSTALL.md](INSTALL.md)创建 `action=install` 私密规格；已有实例按 [UPGRADE.md](UPGRADE.md)使用 `action=upgrade`，提供独立迁移 DSN、备份 client 文件并明确承认备份/恢复限制。两者均使用 external 配置并先审查计划，确认实际摘要后执行：

```sh
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py plan \
  --spec /root/aster-operation/spec.json
sudo python3 /var/lib/aster-bootstrap/0.5.10/core-operation.py apply \
  --spec /root/aster-operation/spec.json \
  --confirm-plan-sha256 REVIEWED_PLAN_SHA256
```

在明确维护窗口内，升级先启用并检查本站门禁、停止 API，创建并验证与旧版本匹配的新私密备份，然后检查/迁移、同提交 Core/网页切换、服务/HTTPS/静态入口核验。不在线覆盖 `current/web` 或正在运行的程序，不手写 BACKUP metadata。工具不证明公共证书续期或实际面板组合已验收，成功后仍需管理员登录和业务检查。

失败先保留私密 journal/备份、读实际 `status`，仅以原规格、ID、确认摘要执行 `resume`。实际旧 schema 兼容且旧程序就绪通过才允许程序回退，维护状态保留；不兼容或不明确时停止。自 Core 0.5.3 引入支付数据库结构后，Core 0.5.2 不能直接接回运行；0.5.4 和目标 0.5.10 均保留这一边界，应保持维护并按已核实的故障向前修复。没有自动数据库回滚，必要恢复须在隔离空库验证后单独批准停写/DSN 切换，不能只切旧二进制、手改迁移记录或向运行库导入 SQL。面板“网站备份”不能代替本库一致性、账号/秘密恢复与隔离演练。

SMTP 单独更新不上传新前端、不切换 `/opt/aster/current`，只安装独立兼容版本、按 revision 更新策略并重启 SMTP 服务，Core 文件和进程保持原样。失败按 [插件回退](PLUGINS.md#3-独立更新与回退)选择已有旧版本；不要删除凭据、重生成密钥或打开插件原始日志排障。

原生 CI 的历史基线已覆盖加固服务、私有测试 CA 的 HTTPS、API 重启、同 schema A→B→A 和同实例空库恢复。宝塔面板操作、公共 CA 续期、数据库进程/整机重启、独立实例灾备与生产容量仍需按部署环境分别验证，不能由一次 CI 成功推导。

## 可选支付插件

支付插槽 `aster.payment/1` 最低要求 Core 0.5.3，与 SMTP 分开审批、独立进程运行，部署方式见[支付插件接入](PAYMENT_PLUGINS.md)。使用专用非登录用户和 `aster-payment.service`，不读取 API 环境或数据库秘密。宝塔自定义 MySQL、备份、网站和凭据路径须纳入不可达规则，并核对共享组权限及实际出口；模板不能自动识别所有面板路径。先在隔离合成环境验证身份、故障、重复回调和恢复，再安排实际渠道测试。停用插件不解除在途交易，升级和备份须包含支付状态。当前没有官方真实渠道，也未提供渠道交易查询、撤销、退款及特定渠道要求的回调应答（ACK）；勿把合成示例配置为营业收款。
