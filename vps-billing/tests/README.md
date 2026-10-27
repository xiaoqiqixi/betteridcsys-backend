# 测试与交付边界

源码仓库和源码归档保留自动化测试，以验证业务规则、安全边界及后续修改。Linux 运行交付使用独立白名单：原始可执行文件与资源包分别发布，核验后组装；资源包含前端静态文件、迁移、原生部署配置、使用文档、依赖清单和许可证，不包含测试源码、测试夹具、CI 工具、临时日志或数据库备份。

| 位置 | 用途 |
| --- | --- |
| `backend/**/*_test.go` | 认证、币种/精度/义务与报价快照、事务、迁移、适配器与官方插件回归 |
| `frontend/**/*.test.ts` | 页面交互、状态管理、双语与金额格式回归 |
| `tests/config_smoke.py` | 非法或不安全启动配置必须被拒绝 |
| `tests/security_smoke.py` | 演示 API 的认证、权限、CSRF 和幂等边界 |
| `deploy/ci/test_*.py` | 原生测试环境隔离、归档校验及发布权限边界 |
| `deploy/ci/accept-native.sh` | 隔离云端 runner 上的真实 MySQL、systemd、HTTPS、备份恢复与版本切换 |
| `deploy/ci/browser-demo.py`、`frontend/tests/e2e.mjs` | 沙箱浏览器、真实回环演示 API、桌面/手机购买与管理交互；截图仅留在 CI 证据中 |

前端检查从 `frontend/` 执行 `npm ci`、`npm test` 和 `npm run typecheck`。后端从 `backend/` 执行 `go test -race ./...`；数据库测试需要显式提供一次性测试库的 `ASTER_MYSQL_TEST_DSN`。CI 必须运行全部数据库用例，出现跳过即失败，不能把无数据库运行当成完整验收。

原生验收脚本只用于满足其隔离条件的临时云端 runner，不在业务服务器执行。测试使用合成数据和本地替身服务，不连接真实支付、外部邮箱或客户数据库。

执行日志由 Actions 和临时工作目录保存，不将每次输出提交到 Git。旧输出及当时的完整审查保留在 [原始记录](https://github.com/xiaoqiqixi/betteridcsys/tree/fd9cc2ce2f4f34d20137028e5e380bed3b95d3f7/vps-billing/tests) 中；实际验收范围见 [验证记录](../docs/VALIDATION.md)，安全发现见 [审查记录](SECURITY_REVIEW.md)。
