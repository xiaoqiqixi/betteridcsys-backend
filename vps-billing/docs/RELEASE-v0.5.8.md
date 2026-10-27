# Core v0.5.8 发布说明

本文记录 Core `0.5.8` 的改动与交付约束；**正式状态以 [v0.5.8 Release](https://github.com/xiaoqiqixi/betteridcsys/releases/tag/v0.5.8) 绑定的精确源码、实际构建运行及公共六资产核验为准**。正式对照基线为 Core `0.5.7`。本文不预报门禁成功，不把本地测试、旧 CI 或中间构建附件当作正常 Release。提交时检查与截图范围见 [VALIDATION.md](VALIDATION.md)；既有 [0.5.7 发布说明](RELEASE-v0.5.7.md)、标签和公开附件不改写或覆盖。

## 商品购买与管理

- 客户按 **类型 → 地区 → 节点 → 套餐** 选择云服务器；后台维护三层分类的名称、排序、启停及商品归属。节点是逻辑商品分类，不是服务器资源、线路或开通控制。
- 首次进入或切换分类后，默认选中当前节点首个公开套餐；桌面配置摘要与手机底栏同步显示其有效独立报价。只有一个分类也保留完整选中状态；不增加比较或“全部地区”入口。
- 首项没有当前币种报价时明确不可购买，不偷偷改选其他套餐或跨分类凑价。默认选择不自动下单；确认购买仍须明确操作。
- 新增、复制草稿、规格编辑、独立定价和上下架沿用商品管理入口。复制保留真实分类；发布和新订单校验完整启用父链，不能直接提交 `plan_id` 绕过停用节点。
- 被草稿或在售商品引用的分类不能直接删除或迁移父级；停用在售分类前须先下架商品。并发 revision 冲突先重读，结果不确定先核对，不自动重放写入。

完整字段、限制与错误码见 [CATALOG_TAXONOMY.md](CATALOG_TAXONOMY.md)。

## 数据库升级与历史边界

正式 Core `0.5.7` 使用 schema 10；本版应用 `011_catalog_taxonomy.sql`、`012_catalog_nodes.sql`，最终要求 **schema 12 / 20 表**。已有 schema 11 的开发实例仅需后续 012。既有 001–011 迁移原文和校验和保持不变，不改旧标签内文件。

011 将旧商品映射至“通用服务器”，按地区原文字节建立确定地区，不从套餐名猜类型。012 为每个已有地区建立“默认节点”：节点 ID 为 `n-` 加地区 ID 的 SHA-256 前 60 位十六进制，并回填当前商品的 `node_id`。默认映射不递增已有 taxonomy 或报价 revision；重复迁移不重复增版本、不重建价格或历史。后续分类编辑在同一事务中递增分类及全局报价 revision，使旧报价失效并要求重新确认。

价格、金额精度、独立币种报价、订单/账单/服务、旧 `PlanSnapshot` 和币种格式快照不重算。地区改名只更新当前商品标签；不以当前节点补造历史字段。既有合法幂等重放仍先返回原订单。默认中文/CNY，不自动换汇、不改支付幂等、认证/CSRF、角色/所有权、最后管理员保护或脱敏审计约束。

升级前安排维护停写、停止旧 API，完成可核验备份和隔离恢复，再迁移并核对结构、完整迁移前缀与校验和，同时切换匹配前后端。新商品写入要求 `node_id` 和分类 revision，缺字段旧请求明确拒绝，不隐式填写。schema 10/11 程序不能直接运行新库；恢复须停止写入并使用升级前已验证备份及匹配旧程序，不能删除迁移记录、伪降 schema 或只切旧二进制。执行器不自动降级或导入数据库。详见 [UPGRADE.md](UPGRADE.md) 和 [BACKUP_RECOVERY.md](BACKUP_RECOVERY.md)。

## 独立版本与资产

| 组件 | 本版目标与兼容 |
| --- | --- |
| Core | 新标签 `v0.5.8`；API、插件宿主与资源必须同为 `0.5.8` |
| SMTP | 保留独立 `smtp-v1.1.0`；`aster.smtp/1`、Core `>=0.4.0,<0.6.0`；本轮不重发 SMTP |

Core 正常 Release 须恰有六项资产：

1. `aster-api-0.5.8-linux-amd64`
2. `aster-plugin-host-0.5.8-linux-amd64`
3. `aster-core-0.5.8-linux-amd64-resources.tar.gz`
4. `BUILD.json`
5. `PROVENANCE.json`
6. `SHA256SUMS`

资源归档唯一根目录为 `aster-core-0.5.8-linux-amd64-resources/`，包含编译网页、全部迁移与配套主机工具，不含 ELF、秘密、数据库备份或测试输出。两个静态 Linux amd64 程序单独交付；BUILD schema 3 / `raw-binaries-v1`、源码、run/attempt、大小与 SHA-256 必须一致。来源记录未签名，不是 SLSA 认证；校验须同时确认固定官方仓库及下载渠道。GitHub 自带源码归档不是安装包。

## 构建、发布与验收

仅使用已审查并提交的精确最终 SHA，在 Linux 构建环境运行；输出目录须全新且在仓库外。下面是待执行示例，不代表已经构建：

```sh
bash vps-billing/deploy/scripts/package-release.sh \
  --commit FINAL_COMMIT_SHA --kind core --version 0.5.8 --output /workspace/core-058-a
bash vps-billing/deploy/scripts/package-release.sh \
  --commit FINAL_COMMIT_SHA --kind core --version 0.5.8 --output /workspace/core-058-b
diff -r /workspace/core-058-a /workspace/core-058-b
```

先让该最终提交的主干完整 CI 通过，再创建全新 `v0.5.8` 标签指向同一提交。标签触发只读 Runtime build，重新执行真实 MySQL、原生 systemd/Nginx/HTTPS、备份恢复与回滚、浏览器、依赖扫描及双构建；构建成功后由默认分支独立发布器严格核验数据并公开正常 Release。不要手工创建同名 Release 抢占发布器，不覆盖标签或资产；任何失败、不确定结果或遗留草稿先核查，不盲目重试写入。流程边界见 [CI_RELEASE_AUTOMATION.md](CI_RELEASE_AUTOMATION.md)。

完成标准分别是：精确源码门禁成功、Runtime build 成功、独立发布器成功、正常 Release 可见、六资产可匿名下载且摘要/大小/来源一致。动态 Release 正文绑定实际构建 run/attempt；这些事实不能从标签推送成功或静态文档推导。

原生部署不使用 Docker，运行应用无需 Go/Node.js；正式资产固定 Linux amd64。Ubuntu 22/24 的实际验证范围分别记录，不宣称所有 Linux 均支持。测试 CA、回环及合成支付/邮件不代表公共证书续期、真实渠道或生产容量验收。不新增真实 VPS 开通、资源管理、新支付渠道、Redis 或主控/被控；SMTP 仍默认关闭且只支持管理员固定测试邮件。实际运营另按 [安装](INSTALL.md)、[宝塔](BAOTA.md)、[插件](PLUGINS.md) 和 [验证记录](VALIDATION.md)核对。
