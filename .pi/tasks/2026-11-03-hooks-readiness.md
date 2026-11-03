# 任务目标
窄修 MySQL 原生 SQL panic 和 Redis v9 钩子不生成 span，补必要回归测试，不增加框架或扩展功能。

# Risk
关键任务：共享库钩子可能影响业务请求稳定性。保持公开 API 不变、底层操作恰好调用一次、原错误原样传播、span 正常结束。增加错误/边界测试和一路新会话只读复审。

# Scope
基线 cfa7be9a34e1593d47917ed57760fb3f7bc80f0f；已有未提交 HANDOVER.md，不覆盖。
允许：pkg/otelemetry/otel_mysql.go、otel_redis.go、相应新增 *_test.go；README.md 仅必要事实同步。不得改 provider/生命周期、不升级依赖、不提交推送部署。新增测试如需要驱动依赖，优先使用 GORM 自带 DummyDialector 或最小测试 Dialector，不为测试修改 go.mod/go.sum。若无法合理测试则报告。

# 执行器与授权
Claude Code；请求模型 sonnet（本任务默认选择，实际模型以响应证据为准）。用户已选择授权两项窄修和回归测试。Pi 不直接修改正式代码或测试。

# 源码事实与最小方案
1. otel_mysql.go before 在 Schema 判空后仍从 db.Statement.Schema.Table 构造 db.operation。公开 GORM MySQL DryRun 下 db.Exec("SELECT 1") panic。
   方案：Schema 可空时安全处理，保留已有命名兼容；不要将错误的属性继续扩大为复杂 SQL 解析器。保证原生 SQL/有模型操作均安全。
2. otel_redis.go 的 ProcessHook/ProcessPipelineHook 直接 return next，未接 Before/After。公开钩子+同步内存 exporter 得到 0 span。
   方案：v9 钩子包装现有逻辑、传递产生的 ctx，next 恰好一次，保证 span 结束，返回底层实际 error。v9 的错误来自 next 返回值，不保证 cmd.Err 在该阶段已设置，测试这点，按需最小补齐；redis.Nil 不是服务错误。不得引入新注册器、配置开关或全局状态。

# Verify
编码器运行 go test -race -count=1 -timeout=180s ./...，go vet ./...，go build ./...。
回归覆盖：GORM 无 Schema 的 Exec 不 panic，有 Schema 保持可用；Redis 普通命令/Pipeline 成功和返回错误各产生并结束 span，ctx 传给 next，错误保留，redis.Nil 不标记服务错误。
Pi 独立重跑以上测试，检查完整 diff；新会话 Claude review 只读复审钩子及回归测试，重点 panic/错误/span 生命周期。

# 本机候选验收
从当前候选打包到临时模块，公开 API 验证 OTLP HTTP protobuf 接收和父子 span；MySQL 驱动 DryRun 验证原生 SQL；Redis v9 公开钩子契约验证。均使用合成数据、临时目录和回环地址，不触达生产。真实 MySQL/Redis 数据库及生产 Collector/TLS 无现成隔离环境时标注未验证，不冒充端到端生产验收。
测试日志在 /tmp/taurus-otel-*；结束清理。不得安装全局软件、访问生产凭证、SSH、commit/push/deploy。超出范围、执行失败或限流立即停止并报告，不重试或切换执行器。
