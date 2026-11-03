# Taurus Pro OpenTelemetry

[![Go Version](https://img.shields.io/badge/Go-1.27.3+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-Apache%202.0-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/stones-hub/taurus-pro-opentelemetry)](https://goreportcard.com/report/github.com/stones-hub/taurus-pro-opentelemetry)

一个基于 OpenTelemetry 的 Go 语言分布式追踪库，为 Taurus Pro 项目提供完整的可观测性解决方案。

## 📖 概述

Taurus Pro OpenTelemetry 是一个专为 Go 应用设计的分布式追踪库，基于 OpenTelemetry 标准构建。它提供了简单易用的 API，帮助开发者快速集成分布式追踪功能，实现微服务架构的完整链路追踪。

### ✨ 主要特性

- **🚀 开箱即用**: 提供默认配置，无需复杂设置即可开始使用
- **🔧 灵活配置**: 支持多种配置选项，可根据需求自定义
- **📊 多协议支持**: 支持 OTLP gRPC 和 OTLP HTTP（protobuf 编码）导出；`ProtocolJSON` 只是 HTTP 的历史别名，不发送 JSON
- **🗄️ 数据库集成**: 内置 MySQL (GORM) 和 Redis 追踪支持
- **🌐 HTTP 示例**: `example/http` 演示如何自行编写 HTTP 追踪中间件（本包不提供中间件）
- **⚡ 高性能**: 基于 OpenTelemetry SDK，性能优异
- **🔒 生产就绪**: 支持采样、批处理、错误处理等生产环境特性

## 🚀 快速开始

### 安装

```bash
go get github.com/stones-hub/taurus-pro-opentelemetry
```

### 基础使用

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
)

func main() {
    // 创建 OpenTelemetry 提供者
    provider, cleanup, err := otelemetry.NewOTelProvider(
        otelemetry.WithServiceName("my-service"),
        otelemetry.WithServiceVersion("1.0.0"),
        otelemetry.WithEnvironment("production"),
        otelemetry.WithEndpoint("localhost:4317"),
    )
    if err != nil {
        log.Fatal(err)
    }
    // cleanup 最多等待 5 秒（可用 WithShutdownTimeout 调整），失败会写日志；
    // 也可以不用 cleanup，直接调用 provider.Shutdown(ctx) 自己控制截止时间
    defer cleanup()

    // 获取追踪器
    tracer := provider.Tracer("my-service")

    // 创建 span
    _, span := tracer.Start(context.Background(), "main-operation")
    defer span.End()

    // 你的业务逻辑...
    time.Sleep(10 * time.Millisecond)
    log.Println("Hello, OpenTelemetry!")
}
```

## 📚 详细使用指南

### 配置选项

#### 基础配置

```go
provider, cleanup, err := otelemetry.NewOTelProvider(
    // 服务信息
    otelemetry.WithServiceName("user-service"),
    otelemetry.WithServiceVersion("2.1.0"),
    otelemetry.WithEnvironment("staging"),
    
    // 导出配置
    otelemetry.WithExportProtocol(otelemetry.ProtocolGRPC),
    otelemetry.WithEndpoint("otel-collector:4317"),
    otelemetry.WithInsecure(false),
    otelemetry.WithTimeout(10 * time.Second),
    
    // 采样配置
    otelemetry.WithSamplingRatio(0.1),
    
    // 批处理配置
    otelemetry.WithBatchTimeout(5 * time.Second),
    otelemetry.WithExportTimeout(30 * time.Second),
    otelemetry.WithMaxExportBatchSize(512),
    otelemetry.WithMaxQueueSize(2048),

    // 退出配置：cleanup 关闭时的超时时间
    otelemetry.WithShutdownTimeout(5 * time.Second),
)
```

#### 默认值

| 选项 | 默认值 |
|------|--------|
| `WithServiceName` | `unknown-service` |
| `WithServiceVersion` | `1.0.0` |
| `WithEnvironment` | `development` |
| `WithExportProtocol` | `ProtocolGRPC` |
| `WithEndpoint` | `localhost:4317`。注意默认值不随协议变化，使用 HTTP 协议时请显式设置为 `host:4318`；显式传入空字符串时按协议取 `localhost:4317`（gRPC）或 `localhost:4318`（HTTP） |
| `WithInsecure` | `true` |
| `WithTimeout`（导出器单次请求超时） | `5s` |
| `WithSamplingRatio` | `1.0` |
| `WithBatchTimeout` | `5s` |
| `WithExportTimeout`（批处理器单次导出超时） | `30s` |
| `WithMaxExportBatchSize` | `512` |
| `WithMaxQueueSize` | `2048` |
| `WithShutdownTimeout`（cleanup 关闭超时，≤0 时取默认值） | `5s`（`DefaultShutdownTimeout`） |

#### 支持的协议类型

- `ProtocolGRPC`: gRPC 协议（默认）
- `ProtocolHTTP`: HTTP 协议（protobuf 编码）
- `ProtocolJSON`: 历史别名，行为与 `ProtocolHTTP` 完全相同，发送的是 **protobuf** 编码（`Content-Type: application/x-protobuf`），并不会发送 JSON。保留该常量只为兼容旧代码编译

### 获取追踪器：RegisterTracer / GetTracer

```go
// 启动时按名称注册（可并发调用）
otelemetry.RegisterTracer("http-server", provider.Tracer("http-server"))

// 任意位置按名称获取（可并发调用）
tracer := otelemetry.GetTracer("http-server")
```

- `GetTracer` 对未注册的名称返回 `otel.Tracer(name)`，即 OpenTelemetry 全局 TracerProvider 的追踪器，任何时候都不会返回 nil，也不会 panic：
  - `NewOTelProvider` 之前：返回全局委托追踪器，此时创建的 span 不会被记录；初始化后同一个追踪器会自动转发到真正的提供者。
  - `NewOTelProvider` 之后：`NewOTelProvider` 会调用 `otel.SetTracerProvider`，返回的追踪器正常上报。
  - `Shutdown` 之后：返回 noop 追踪器，span 不再上报。
- 未注册名称的 span 的 instrumentation scope 名就是传入的 `name`。
- 同名重复注册会覆盖旧值并打日志；注册 nil 会被忽略并打日志。

#### 包级变量 `Provider`（已废弃）

包级变量 `otelemetry.Provider` **从未被本包赋值，始终为 nil**，保留只为兼容旧代码编译。请使用 `NewOTelProvider` 返回的 `*OTelProvider`、`GetTracer` 或 `otel.GetTracerProvider()`。对 nil 的 `*OTelProvider` 调用 `Tracer` 会返回全局 TracerProvider 的追踪器，调用 `Shutdown` 直接返回 nil，均不会 panic。

### 退出与 Shutdown

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := provider.Shutdown(ctx); err != nil {
    log.Printf("otel shutdown: %v", err)
}
```

- `Shutdown(ctx)` 先导出批处理队列中剩余的 span，再关闭导出器，整个过程受 `ctx` 截止时间约束。
- **本次调用因 `ctx` 结束而没能完成时**（flush 中途到期，或调用前 `ctx` 就已取消/到期），返回的错误可用 `errors.Is` 识别为该 `ctx` 错误，并强制停止批处理器和导出器：
  - 中断仍在进行的 OTLP 请求；
  - 队列中尚未导出的 span **尽力而为**：批处理器停止时仍会做最后一次导出，它与导出器关闭之间存在竞争，这些 span 可能已经送达，也可能被丢弃（即使调用前 `ctx` 就已到期也是如此，不保证一定不 flush）；导出失败通过 otel 全局错误处理器写日志；
  - 批处理器的后台协程在有限时间内退出；
  - 之前拿到的追踪器之后结束的 span 不再入队、不再导出；
  - 停止批处理器、导出器时出现的非 `ctx` 错误会与 `ctx` 错误一起返回（`errors.Join`），不会被丢掉。
- **并发与重复调用**：只有第一次调用（owner）真正执行关闭。其他调用不会触碰导出器，而是等待 owner 完成并返回 **owner 的结果**（例如 owner 超时，之后的调用也返回 `context.DeadlineExceeded`，不会用 nil 掩盖未完成的关闭）；若自己的 `ctx` 先结束，则返回自己 `ctx` 的错误，**这不代表 owner 已完成**，进程如需确保 flush 完成，应等待 owner 的调用返回。owner 没有正常返回（例如 panic，panic 仍会传播给 owner 的调用方）时，其他调用和之后的调用返回 `ErrShutdownIncomplete`，不会永久等待，也不会得到 nil。
- 返回 nil 表示关闭流程已经执行完毕。导出失败（例如 HTTP collector 拒绝连接）不体现在返回值里：OpenTelemetry SDK 会把 flush 时的导出错误交给全局错误处理器（默认写入标准库 `log`）。gRPC 导出器会重试不可达错误直到截止时间，此时返回 `context.DeadlineExceeded`。
- `ctx` 为 nil 时按 `context.Background()` 处理，等待时长只受 `WithTimeout`、`WithExportTimeout` 约束，建议总是传入带截止时间的 `ctx`。
- `NewOTelProvider` 返回的 `cleanup()` 等价于用 `WithShutdownTimeout`（默认 5 秒）作为超时调用 `Shutdown`，失败时通过标准库 `log` 打印错误。
- Go 无法强制终止协程：超时只表示不再等待，已被中断的导出请求和后台协程会在随后很短时间内自行结束。

### 数据库追踪

#### MySQL (GORM) 追踪

```go
import (
    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
    "gorm.io/gorm"
)

// 创建 GORM 追踪钩子
hook := &otelemetry.GormTracingHook{
    Tracer: provider.Tracer("gorm"),
}

// 注册钩子到 GORM 实例
db.Use(hook)

// 现在所有的数据库操作都会被自动追踪
var users []User
result := db.Find(&users)
```

#### Redis 追踪

```go
import (
    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
    "github.com/redis/go-redis/v9"
)

// 创建 Redis 追踪钩子
hook := &otelemetry.RedisHook{
    Tracer: provider.Tracer("redis"),
}

// 创建 Redis 客户端并添加钩子
rdb := redis.NewClient(&redis.Options{
    Addr: "localhost:6379",
})
rdb.AddHook(hook)

// 现在所有的 Redis 操作都会被自动追踪
ctx := context.Background()
val, err := rdb.Get(ctx, "key").Result()
```

### HTTP 追踪

本包**不提供** HTTP 追踪中间件。`example/http/main.go` 中的 `TraceMiddleware` 是一个示例实现，可按需复制到自己的项目中。

## 📁 项目结构

```
taurus-pro-opentelemetry/
├── bin/                    # 可执行文件
├── example/                # 使用示例
│   ├── grpc/              # gRPC 示例
│   ├── http/              # HTTP 示例
│   ├── mysql/             # MySQL 追踪示例
│   └── redis/             # Redis 追踪示例
├── pkg/                    # 核心包
│   └── otelemetry/        # OpenTelemetry 实现
│       ├── provider.go     # 核心提供者
│       ├── options.go      # 配置选项
│       ├── handler.go      # 处理器
│       ├── otel_mysql.go   # MySQL 追踪
│       └── otel_redis.go   # Redis 追踪
├── go.mod                  # Go 模块文件
├── go.sum                  # 依赖校验文件
├── LICENSE                 # 许可证文件
└── README.md               # 项目说明文档
```

## 🔧 配置说明

### 配置来源

本包只通过代码中的 `With*` 选项配置，未设置的项使用上文“默认值”表中的值。本包不读取 `OTEL_SERVICE_NAME` 等自定义环境变量。

## 📊 性能特性

- **采样控制**: 支持可配置的采样率，减少追踪数据量
- **批处理**: 自动批处理追踪数据，提高导出效率
- **异步导出**: 非阻塞的异步导出机制
- **内存优化**: 智能的内存管理和资源回收

## 🚨 注意事项

1. **资源清理**: 退出前调用 `cleanup()` 或带超时的 `provider.Shutdown(ctx)`，否则批处理队列中尚未导出的 span 会丢失
2. **错误处理**: 生产环境中应妥善处理初始化错误
3. **采样配置**: 高流量环境中建议使用较低的采样率
4. **网络配置**: 确保 OTLP 接收器地址可访问

## 🧪 运行示例

### HTTP 示例

```bash
cd example/http
go run main.go
```

访问 `http://localhost:8080/user/1` 查看追踪效果。按 Ctrl+C（或发送 SIGTERM）退出：示例会在 5 秒内关闭 HTTP 服务器，再通过 `cleanup()` 在 5 秒内 flush 追踪数据。gRPC 示例的退出方式相同。

### MySQL 示例

```bash
cd example/mysql
go run main.go
```

### Redis 示例

```bash
cd example/redis
go run main.go
```

## 🤝 贡献指南

我们欢迎所有形式的贡献！请遵循以下步骤：

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

### 开发环境设置

```bash
# 克隆仓库
git clone https://github.com/stones-hub/taurus-pro-opentelemetry.git
cd taurus-pro-opentelemetry

# 安装依赖
go mod download

# 运行测试
go test ./...

# 运行示例
cd example/http && go run main.go
```

## 📄 许可证

本项目采用 Apache License 2.0 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

## 👥 作者

- **yelei** - *主要开发者* - [61647649@qq.com](mailto:61647649@qq.com)

## 🙏 致谢

- [OpenTelemetry](https://opentelemetry.io/) - 提供可观测性标准
- [GORM](https://gorm.io/) - Go 语言的 ORM 库
- [go-redis](https://github.com/redis/go-redis) - Go 语言的 Redis 客户端

## 📞 支持

如果您在使用过程中遇到问题或有任何建议，请：

1. 查看 [Issues](https://github.com/stones-hub/taurus-pro-opentelemetry/issues)
2. 创建新的 Issue
3. 发送邮件至 [61647649@qq.com](mailto:61647649@qq.com)

## 🔄 更新日志

### v1.0.0 (2025-06-13)
- 🎉 初始版本发布
- ✨ 支持 OpenTelemetry 标准
- 🗄️ 集成 MySQL (GORM) 追踪
- 🔴 集成 Redis 追踪
- 🌐 提供 HTTP 追踪中间件示例（`example/http`，本包不导出中间件）
- ⚙️ 灵活的配置选项

---

⭐ 如果这个项目对您有帮助，请给我们一个星标！
