# W02：公开 Core 六资产与正式 prepare 验收

状态（2026-10-27）：正式0.5.10公开六资产与Ubuntu22/24生产prepare已实际通过；不是已安装或整机生产验收声明。
源码候选native测试、正式构建、公开下载与目标主机安装分别记账，不能相互替代。

## 固定范围

- 入口为`.github/workflows/core-public-prepare.yml`，只允许固定官方公开仓库的main手动触发。
- 第一版只验正常`v0.5.10`，输入为已审查的完整40位source SHA；不接受latest/分支名、不自动选其他版本。
- Ubuntu22.04/24.04 amd64各独立运行；实际读取os-release并与matrix核对，不用宿主版本冒充guest兼容。
- job仅`contents:read`、`actions:read`；无发布、OIDC签名、仓库写、服务器密码或额外第三方账户权限。
- 必须先有真实正常Release与完整六资产；尚未发布、草稿、源不符、资产或来源校验失败均拒绝，不SKIP、不切换旧版代替。

## 执行链与边界

1. 从精确workflow main checkout加载既有`core-operation.py`及其core/update/plugin控制模块；不执行下载资源里的Python、ELF或shell。
2. 使用生产`release_descriptor`验证正常Release、固定仓库/标签/main祖先、完整六资产清单及其大小/摘要。
3. 使用独立且**不带token**的生产`Official()`逐个匿名下载六资产。元数据读取可用只读CI token，
   仅经stdin进入，env清空、不进argv/磁盘/回执/日志；下载client没有该token。TLS、重定向域名与响应预算沿用生产检查。
4. 完整大小和SHA256通过后，写入本轮`/run/aster-public-core.*`内0700私有目录；无缓存、跳过或网络失败成功替身。
5. 调用原生产`manager.prepare`，使用真实公开资产和完整官方来源验证，保留默认root文件信任与Linux原子发布；
   不注入`_Trust`、不替换GitHub/ELF/source验证器，不制造PREPARED收据。
6. 再调用生产只读`prepared_release`，遍历全部已准备字节/模式、完整文件清单、静态ELF、BUILD/source映射和迁移前缀。
   另核对真实收据的source/version/release/assets/build run及其完整hash；必须正好13份原迁移。
7. 重新核对公开资产未变化，退出后仅删除本轮新建私密目录；清理失败不得产生通过报告。

不执行`apply`、`resume`、服务操作、APT、创建系统用户、迁移、数据库连接、付款或实例开通；不修改系统/用户证书信任。
准备目标只在本轮私密scratch，不写`/opt/aster`、不切current，不使用用户财务机。
Core现有provenance不带独立发行签名，报告保持`independent_publisher_signature:false`，不夸大为Sigstore验证。

## 运行与证据

先核对正式Release、构建及发布器实际成功，再执行（`SOURCE_SHA`须替换成真实已发布源码，不是最新main的猜测）：

```sh
gh workflow run core-public-prepare.yml \
  --repo xiaoqiqixi/betteridcsys --ref main \
  -f source_sha="$SOURCE_SHA"
```

两个job均须成功，核对其`public-core-prepare-*` artifact的完整ZIP大小/SHA256后，读取唯一脱敏JSON。
报告包含实际source/release/build run、六资产大小/hash、准备文件总数、13迁移、实际Ubuntu版本和清理结果；
必须显示匿名下载/来源校验/真实prepare/完整收据复核均true，程序执行/安装均false、服务和数据库操作均none。
失败报告只输出固定失败码，无token、私密路径、原始HTTP响应或来源文件内容。

`test_public_core_prepare.py`为离线接线/负例测试，不是下载或Linux原子prepare实际运行。
真正执行的收据和CI链接取得前，本项保持未验。该独立发布后工作流也不补位main精确SHA的5个财务或16个总门。
通过只证明公开资产和受保护准备目录，不证明正式安装/升级、用户服务器可用、跨分叉旧目录兼容或完整生产验收。

## 首次实际验收

- 公开Release `424987299`、标签`v0.5.10`、精确source `dab0167d58cd99a19fc4a0eafff23bbe7ff95cf0`，正常非草稿、非预发布。
- [正式构建40439181617](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40439181617)与[独立发布器40441128635](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40441128635)均成功。
- [公开prepare40441393371](https://github.com/xiaoqiqixi/betteridcsys/actions/runs/40441393371)：Ubuntu22/24 **2/2成功**，控制源码为main `b132123`，不是把控制器SHA误当Release源码。
  各自真实准备并复核202文件、13迁移；匿名下载/完整来源/正式prepare/原始收据复核与清理均true，程序执行/安装false、服务/数据库动作none。
- 已下载并完整核对两份单JSON的966字节ZIP：22 SHA256 `d2b0b1fea3546b28a5603f0e5ab75a4cbec2bb83954d476a52db8e04e8448fb6`，
  24 SHA256 `a0231b00216686e0c94680b371ee4be6cc54e2a4d861ff25ac23061ed14570f7`。
- 本机也实际匿名下载完整六资产，经未修改的生产内存验证函数核对BUILD、PROVENANCE、源码映射/祖先和13迁移。
  元数据走只读GitHub API，资产下载client无token；本机未做Linux受保护prepare，不能用这部分替代上面的两个平台结果。
  原始下载与回执保存于ignored的`vps-billing/artifacts/execution-20261027/core-public/`。

资源包内README为构建前的源码快照，部分状态文字仍写候选/未发布；它不是实时Release状态。
main与发布后补记说明当前真实状态，不修改该标签、资源字节或SHA256。安装版本、协议、默认版本与六资产均为0.5.10；
后续版本的README采用版本合同/发行条件，避免把构建前临时状态固化成安装结论。
