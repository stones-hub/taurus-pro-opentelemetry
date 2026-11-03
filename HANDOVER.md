# 上线前验证交接

## 2026-11-03 最终状态

- 修复基线：`cfa7be9a34e1593d47917ed57760fb3f7bc80f0f`。本轮发布版本为 `v0.2.1`，对应本次钩子修复和回归测试。
- 用户授权：测试上线能力，不要过度设计；随后授权默认 Claude Code 窄修 MySQL/Redis 两个钩子并补测试；最终明确授权提交修复、测试和交接文档到 main，push，创建 v0.2.1 标签及正式 GitHub Release。未授权部署。
- 结论：本轮限定范围的本机候选验收通过，原先发现的两个阻断问题及 Pipeline 混合错误遗漏均已修复。可以进入发布准备/目标环境验收，不等于已部署或生产保证。

## 实际改动

- `pkg/otelemetry/otel_mysql.go`：Schema 可空时不读取 Table，原生 SQL 不再 panic，有模型时保持原命名和属性。
- `pkg/otelemetry/otel_redis.go`：v9 ProcessHook/ProcessPipelineHook 接入追踪，底层操作只调用一次、透传 ctx 和返回错误；Pipeline 首错 redis.Nil 时仍检查后续真实错误；redis.Nil 不作为服务错误。
- `pkg/otelemetry/hooks_test.go`：新增 GORM 原生 SQL/模型查询和 Redis 命令/Pipeline 成功、错误、缺失键、混合错误回归测试。
- `.pi/tasks/2026-11-03-hooks-readiness.md`：范围、授权和验收任务书。
- 依赖、公开 API、Provider 生命周期未改。README 已检查，现有使用说明无需因本次 bug 修复而修改。

## Pi 独立验证（最终候选）

- `go test -race -count=1 -timeout=180s ./...`：通过。
- 新增钩子测试带 race 连跑 20 次：通过。
- `go test -cover ./pkg/otelemetry`：通过，语句覆盖率 84.1%（原 66.3%）。
- `go vet ./...`、`go build ./...`：通过。
- Linux amd64 / arm64 交叉构建：通过（不是目标机器运行验收）。
- `go mod verify`：通过。
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`：No vulnerabilities found；未安装全局工具。
- `git diff --check`：通过。
- 新增回归测试放回旧基线时，确实分别复现 MySQL panic 和 Redis 无 span，非空测试。

## 隔离真实数据库及公开 API 验收

将基线打包到临时目录，覆盖最终两个源码文件并用 cmp 确认一致，独立消费模块通过 replace 引用该候选；未连接生产。

- MySQL 8.4.11 容器：迁移、CRUD、Exec、Raw、SQL 错误记录通过，观察到 11 个已结束 span。
- Redis 7.4.11 容器：普通命令、Pipeline、缺失键、WRONGTYPE、父子 trace 关系通过，常规场景观察到 9 个已结束 span。
- Pipeline 混合错误：GET missing 后 LPUSH 字符串 key；业务返回仍为 redis.Nil，span 正确记录后续真实错误，通过。
- 本地 OTLP HTTP/protobuf 接收端：收到两个 span，父子 span ID 对应，通过。
- 本地 OTLP gRPC 接收端：收到预期 span，通过。
- Collector 阻塞：Shutdown 在 150ms 截止时间附近返回 DeadlineExceeded，重复调用保留首次错误，通过。日志中的 context canceled / deadline exceeded 为刻意注入场景；SQL 不存在表的日志亦为错误场景预期结果。
- 本次临时容器、匿名卷及 /tmp/taurus-otel-* 测试目录在验收完成后清理，不保留临时凭证。

## 独立复审和执行器

- 编码：Claude Code，请求 sonnet，实际模型证据 `claude-sonnet-5-5`；session `c0e5df99-72d5-4862-8092-8f141df60646`。首轮及同会话窄修均正常完成。
- 新会话只读复审：session `88355f16-b7e0-4d24-bf2c-857786d36c9f`，实际模型 `claude-sonnet-5-5`。确认 MySQL 判空和 Redis ctx/调用次数/返回值没有新增问题，指出并认可混合 Pipeline 错误的最小修复；Pi 检查最终 diff 并真实重测。

## 剩余边界（不隐藏，也不扩大本轮）

- staticcheck v0.8.2 仍有 5 条存量告警，与旧基线对比完全一致：bin/main.go:40/79/118 的未使用 context 赋值（SA4006），shutdown_test.go:231/237 故意传 nil context（SA1012）。因此不能声称 staticcheck 全绿。
- Redis 下游 next 如果 panic，span 不会结束；panic 原样传播，复审认定为可选观测加固，本轮不处理。
- SQL 采集时机、属性语义、SQL/Redis 命令文本可能包含敏感业务数据，均为原有行为；上线方需按数据安全要求评估。
- 未做生产容量/长稳、目标 Collector 的 TLS/认证/网络及目标应用退出顺序验收。生产环境还需确认 Go >=1.27.3，HTTP endpoint 明确配置 4318。
- 数据库真实测试版本为 MySQL 8.4.11、Redis 7.4.11，不代替其他版本的兼容承诺。

## 续接

发布操作：将本次候选提交并推送 main，新增 v0.2.1 标签和正式 GitHub Release，不移动旧标签。实际远程结果以 GitHub 和 git ls-remote 为准。未部署、未重启服务（本次仅发布库版本）。

续接可说：检查 v0.2.1 的 GitHub 发布状态；如需部署或升级下游应用，另行确认目标和授权。
