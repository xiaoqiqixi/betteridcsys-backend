# Cloud 首个 RC：公开下载与生产 plan 验收

2026-10-27，[cloud-v0.1.0-rc.1 Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/cloud-v0.1.0-rc.1) 已实际公开，Release ID 为 `425041579`，发布时间 `2026-10-27T04:41:38Z`。
它是独立 **预发布版**，不是 stable；`production_ready=false`、`real_kvm_accepted=false`。财务金额、支付与历史快照不变。

## 来源与实际任务

- 发行源码：`8092c3cc0517c69562e8a5cdd94bc5e389e1bc8e`。
- 新注解标签对象：`400333ef9886773cf414f3fb0763500cb5253c5d`；tagger 为仓库所有者，未覆盖任何旧标签。
- 同一 SHA 的[Cloud 40448316272](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40448316272) **11/11** 与[财务 40448316285](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40448316285) **5/5** 全部成功；生产发布策略再次读取并确认完整16门。
- [可复现构建与独立签名 40450543793](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40450543793) **2/2成功**：两次实际独立构建比较完整产物，独立 attest job 签名并实际复验。
- [独立发布与公开验收 40451094771](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40451094771) **3/3成功**：新草稿/九资产/公开 prerelease，以及主控和被控各自的真实匿名下载、Sigstore 复验和生产只读 plan。
- 发布源码冻结至上述任务全部成功；后续main变更不改写这些已发布资产，也不能继承其CI结果。

## 公开九资产

全部为实际完整匿名下载的字节，不是 HEAD、文件名检查或模拟HTTP。客户端使用原生产 `fetch/public_release_metadata`：
固定仓库/标签、HTTPS/证书验证、禁代理与认证、限定重定向、体积与时限；下载前后元数据一致。
以下摘要同时与公开资产、签名清单、完整性记录对应：

| 文件 | 字节 | SHA256 |
| --- | ---: | --- |
| `SHA256SUMS` | 614 | `f198fb24da428f5b1ba4dc73599501e7fb8af20d946071e7d4e970301256793f` |
| `aster-cloud-bootstrap-0.1.0-rc.1.py` | 39436 | `14b9a22b9c06ac7f743621adf650197064d0c971383935ccb861db1d6bbd397f` |
| `aster-cloud-controller-0.1.0-rc.1-linux-amd64.tar.gz` | 8459059 | `1feebda62813f91b4aa9eca97d52f62e991593c5ff7bfe45d4a38c699856292a` |
| `aster-cloud-installer-0.1.0-rc.1.tar.gz` | 44951 | `bdc0361bb50fa675a0129505ca2e01442328e573a9203e54423af8794df9c5a5` |
| `aster-cloud-node-0.1.0-rc.1-linux-amd64.tar.gz` | 11728737 | `cf92656ba8daf210815dfe8fa8d04d36ef46a9cd27c5f488c52780f511ff5544` |
| `attestations.jsonl` | 12482 | `1c96037b0fbd0e540e886f4baa8ec573eb92e03f73cc3b49cf130d0daba56613` |
| `provenance.json` | 411 | `b68fbc5dcf8ff41b4aa4878119e060113559be360325cc157b19c2f95e12a3cd` |
| `release-manifest.json` | 1629 | `7238c7712a97875a0a3da71bca556d22b05b768ffa7bd0a45c9fcf8cb6f61609` |
| `release-trusted-root.json` | 34634 | `65ca537f6ed8a47fd0e560c421baa1f6c1efb8b25fc200d8c5c02c0e92eb2b9c` |

本机另以未修改的生产数据校验函数检查完整 manifest、checksum、固定信任根、bootstrap/capsule与精确Git源码、两个包清单及静态ELF。
这只是本机字节/来源一致性验证，**本机没有执行Linux Sigstore CLI或root生产plan**；真实密码学复验与plan来自上面的独立Linux任务，不能混称。

## 两角色原始公开验收回执

本次发布器后的两个任务运行于 **Ubuntu24.04**。每份ZIP的完整摘要、唯一 `anonymous-download.json`、CRC、精确13字段、
角色/版本/发行ID/源码和角色包摘要均已重新下载核对：

| 角色 | GitHub artifact ID | ZIP字节 | ZIP SHA256 |
| --- | ---: | ---: | --- |
| controller | 12710502064 | 446 | `86ca61ac3dd8d256bf7399fd4a92407637ee67c69cd4bcb2cd9bc9ef180e6a7d` |
| node | 12710397639 | 445 | `df6139df1f1bfbc9006cfa6dbef95533aa699ee48a6270ad72c0b8f3d6b9f4c1` |

两份实际回执均为：`anonymous_download=true`、`publisher_signature_reverified=true`、
`production_installer_plan=true`；`host_apply=false`、`service_started=false`、`real_kvm_acceptance=false`。
验收使用原生产bootstrap和认证后的installer，不替换根所有权、真实验签、来源条件或安装器事实探测。

已将全部既有 **9个Release** 的ID/tag/目标/草稿与预发布标志、正文摘要和全部资产ID/名字/大小/digest/state
与发布前实际基线逐项比较，均未变化；财务 `latest` 仍是 **v0.5.10**。没有借Cloud发布改写旧财务资产。

## 继续补验，不扩大结论

- 上述公开plan是Ubuntu24的两角色，不冒称Ubuntu22也执行过公开下载。新增[四组合门](../.github/workflows/cloud-public-prepare.yml)
  覆盖Ubuntu22/24 × controller/node，独立记录**验收控制源码**与固定**已发行源码**；本段编写时尚未启动新门。
- 原始发行源码的22/24原生libvirt门已通过，但TCG软件准备不是真KVM，也不能代替公开包或整机安装验收。
- 没有在用户主机安装/启动Cloud服务；主控真实数据库/TLS/systemd整套安装、宿主重启/恢复、真实客户VM、跨物理节点、
  公网/隔离/限速和快照备份迁移仍须按计划分别验收。不能把下载、plan或pre-release称为生产已就绪。
- 独立资源worker0.1.1仍为候选，不因本次Cloud发行而进入Core六资产或获得自动开通worker能力。
- 另检查了实际角色包的文档布局：包内Markdown保留源码相对链接，但打包后改放在docs目录，部分链接不能离线直达。
  当前请使用固定标签下的[在线使用指南](https://github.com/xiaoqiqixi/betteridcsys/blob/cloud-v0.1.0-rc.1/aster-cloud/docs/GETTING_STARTED.md)；
  原包不替换，后续分发须补布局/链接回归。这是文档可用性缺口，不把包清单/哈希通过说成文档视觉与导航已验收。

原始本机资产、生产数据校验结果、发布前后基线与两份实际ZIP保存在ignored的
`aster-cloud/artifacts/execution-20261027/cloud-public/`。当前源码与后续进度见[Cloud验证](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/VALIDATION.md)，
使用入口见[主控入门](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/GETTING_STARTED.md)，[安装信任链](https://github.com/xiaoqiqixi/betteridcsys-cloud/blob/main/aster-cloud/docs/RELEASE_CHANNEL.md)保留完整规则。

## 随后补验：Ubuntu22/24 × 两角色实际4/4成功

[公开四组合40454038287](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40454038287)已实际 **4/4成功**，不是把前面24的结果改写为22。
验收控制源码为`20208555d95d72bd3f97f8b5efa2776083946696`；固定已发行源码仍为`8092c3cc0517c69562e8a5cdd94bc5e389e1bc8e`。
这直接验证main推进后既有RC仍可正确下载与验签，不要求把tag移动到新main。

四个ZIP已全部重新下载核对完整摘要、两个唯一JSON成员/CRC、源运行/控制源码及实际target facts，
再以原独立checker重验根回执的来源、角色、包摘要和严格13字段；没有拿离线fixture作实测。

| 平台 / 角色 | artifact ID | ZIP字节 | ZIP SHA256 |
| --- | ---: | ---: | --- |
| ubuntu-22.04 / controller | 12711737397 | 996 | `395c0cb147fca7990d99dce544b2fe7659569184aad49f5023673569343be764` |
| ubuntu-22.04 / node | 12711802212 | 992 | `dbcbda19c8050de3e904b145a004c5d8a571bd019b302426ef59b74e3d99fdc4` |
| ubuntu-24.04 / controller | 12711282204 | 996 | `597647292de8e94e76939fa26c327f30c9d5ba19c9b5866d00a16910e5b45600` |
| ubuntu-24.04 / node | 12711792038 | 993 | `5d3ea2adc0c4f50add3e42a296cc333c06a271b5bf00d52ece88ab805bdc69e0` |

四项实际均为匿名下载、真实Sigstore复验与认证后生产plan成功；host_apply/service_started/real_kvm_acceptance全部false。
原始ZIP、平台报告及矩阵复核JSON位于`aster-cloud/artifacts/execution-20261027/cloud-public/matrix-2020855/`。
此结果只扩大公开下载/只读plan的平台证据，不把新控制源码的完整16门、真实安装、staff集成或KVM验收填绿。

随后同控制源码2020855的[财务40453853089](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40453853089) **5/5**、
[Cloud40453853002](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40453853002) **11/11**均实际成功，原生产发布策略又经真实只读API复核完整16门。
独立性能40453853030也成功，但不计入或替代16项必需集合。上述结果仍不使staff数据库/接口/UI、真实安装或KVM成为已验收。
