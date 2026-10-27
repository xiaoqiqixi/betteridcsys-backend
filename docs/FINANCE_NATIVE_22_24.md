# W04：财务 Ubuntu 22/24 原生验收合同

状态：`128a86d091d806c39b9c33c8a945c0fc20b19f06`两平台隔离原生CI已通过（财务5/5），不是用户主机或生产环境验收。只支持这两个LTS，不顺便扩到26。新修改仍须自身精确SHA完整门禁。

## 不变边界

- 只改原生 CI 夹具、平台选择、发行门禁及当前未发行候选的精确 prepare 信任清单；不改变金额、付款、幂等、多币种、历史快照和既有 SQL。
- Ubuntu 24 仍使用原已审核 MySQL 8.4.11 四包 pin，不为支持 22 暗中升级已验基线。
- Ubuntu 22 使用独立官方 Jammy MySQL 8.4.13 四包 pin；不拿 Noble 二进制硬跑 Jammy。
- 这不是生产机器安装器的提权开关。所有系统操作仍仅允许指定仓库、临时 GitHub-hosted runner、明确 opt-in、普通用户入口和独立 scratch。
- 不安装 MySQL/Nginx 宿主服务，不换软件源，不升级整套系统或关闭 APT 签名、Chrome sandbox、TLS 验证。

## 平台准备

1. 从系统 os-release 严格识别 22.04/24.04，拒绝 Debian、26、其他版本；不接受环境变量伪装平台。
2. Jammy使用非t64运行库及经官方签名metadata审查、固定大小/SHA256的Nginx stable1.30.6 Jammy包；
   不能再用缺少ssl_reject_handshake的Ubuntu nginx-core1.18。Noble保留t64运行库及已认证APT的nginx包。
   两平台均核对Package/Architecture（Jammy另核对精确Version），解到本轮目录，不执行maintainer scripts；不向宿主添加nginx.org源或签名信任。
3. MySQL 先验证所选固定 manifest、精确四包/版本/架构/正整数大小/SHA256，再全部下载校验，之后才解包。
   CLI 不接受新 URL、hash、manifest 或已解压目录覆盖。
4. 不用 Python 大版本号代替 tar 安全能力：要求有效 `data_filter` 和 extractall 的 filter 参数，始终显式 `filter="data"`。
   对路径穿越、外链、设备的拒绝继续保留；无安全 backport 时在下载/执行前失败，不降级为无 filter 解压。

## 完整验收链

- 保留原 Ubuntu24 `checks`，新增明确命名的 Ubuntu22 原生任务，不用一次矩阵绿掩盖另一平台失败。
- 两平台均执行同一 `check.sh`（完整 Go/真实 MySQL、前端检查与安全 smoke）及同一 native acceptance：
  实际 systemd 硬化、Nginx/TLS、运行/迁移权限分离、付款插件合成事务、SMTP loopback、进程重启、备份恢复数据一致、
  正式旧版资产下载/hash/来源验证、源码候选完整 Core 执行器升级与回滚边界、故障保持维护状态、最终清理。
- 不重写旧版来源合同；缺少公开资产或下载失败直接失败，不把离线造包当正式安装通过。
- 原实际 MySQL 财务浏览器门继续独立必需；新22任务保留本机 sandboxed demo 浏览器检查，但它不代替真实API门。
- 每平台单独命名 artifact，报告由真实 os-release 生成平台字段；不得在22报告里硬编码24。
- Cloud/财务发行策略都要求同一 SHA 的新增22任务成功，必需任务总数由15增为16。缺失/失败/重复/陌生任务仍拒绝。
  财务 builder/publisher 原先只独立重跑24；新增只读精确五任务检查，在构建与发布前后复核。prepare 仅扩充未发行0.5.10的精确信任清单，
  不改变0.5.0–0.5.9历史清单，不把历史旧版改成必须携带新文件。

## 证据与未验项

官方来源：[Oracle Jammy Packages](https://repo.mysql.com/apt/ubuntu/dists/jammy/mysql-8.4-lts/binary-amd64/Packages)、
[Python tarfile 安全 filter](https://docs.python.org/3.10/library/tarfile.html#extraction-filters)。
2026-10-26 已下载四个官方22包并逐个核对固定大小/SHA256，尚未本机解压或执行；这不是 PGP InRelease 验证，也不是 Ubuntu22 运行成功。
本机是 macOS，不伪装成 systemd/Linux 验收。必须以新提交的 Ubuntu22/24 CI 实际记录与清理回执完成本包。
eb1e81f实际达到15/16门：24完整原生链成功，22停止在Nginx1.18安全最低版本检查，没有降低检查或把失败计通过。
27日已核验官方stable1.30.6签名Release→Packages大小/SHA256→固定.deb大小/SHA256，篡改Release负例拒绝。
PGP密钥束取自vendor HTTPS，固定全文件SHA并包含官方文档给出的主指纹；不宣称独立Web of Trust认证。
OpenPGP.js只在私有临时验证目录，不成为项目依赖。新22包尚未本机提取/运行，仍须新精确SHA完整native链。
精确pin、签名审查材料及失败记录见[27日续记](EXECUTION-20261027.md)。
942f62e已实际通过22上的新Nginx/TLS/edge与完整Go/MySQL/race、前端及合同检查；
后续SMTP崩溃注入因systemd249不识别固定`--kill-whom`失败，完整22链仍未通过。
修复为预先读取实际help、选择精确支持参数并保持一次KILL/严格失败；合成支付和清理一并修，子报告真实OS同步。
128a86d实际通过完整22/24链：SMTP报告9项、合成支付结算、源码候选Core安装/升级/备份恢复/回滚/故障保持与清理均完成。
22实际选`--kill-who`、24选`--kill-whom`，两份报告均为真实OS，Core PID不变，清理退出0；独立真实MySQL财务浏览器亦成功。
22原生ZIP SHA256为`2e53475f2dad8babf7f850eebd650c0c52263dfeb4fd3fcc43f6426967262716`，
24为`1aeab9467163709a4c9fa67388d6672b9192ac174f86125db6638c8630697768`，已核验完整大小/摘要/安全ZIP后读取。
这不等于正式0.5.10公开下载已完成，也不能覆盖同SHA Cloud宿主网络保护失败。完整记录见[27日续记](EXECUTION-20261027.md)。
doctor的“支持目标”不等于已验本机；它不查询CI、不改变安装授权，也不会因候选版本而将失败CI记通过。
全新物理机器、真实公网证书、负载/灾备和 KVM 生产验收另列，不由 hosted runner 的隔离服务测试替代。
