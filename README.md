# Taurus Pro OpenTelemetry

[![Go Version](https://img.shields.io/badge/Go-1.24.2+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-Apache%202.0-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/stones-hub/taurus-pro-opentelemetry)](https://goreportcard.com/report/github.com/stones-hub/taurus-pro-opentelemetry)

一个基于 OpenTelemetry 的 Go 语言分布式追踪库，为 Taurus Pro 项目提供完整的可观测性解决方案。

## 📖 概述

Taurus Pro OpenTelemetry 是一个专为 Go 应用设计的分布式追踪库，基于 OpenTelemetry 标准构建。它提供了简单易用的 API，帮助开发者快速集成分布式追踪功能，实现微服务架构的完整链路追踪。

### ✨ 主要特性

- **🚀 开箱即用**: 提供默认配置，无需复杂设置即可开始使用
- **🔧 灵活配置**: 支持多种配置选项，可根据需求自定义
- **📊 多协议支持**: 支持 gRPC、HTTP、JSON 等多种导出协议
- **🗄️ 数据库集成**: 内置 MySQL (GORM) 和 Redis 追踪支持
- **🌐 HTTP 中间件**: 提供 HTTP 请求追踪中间件
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
    
    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
    "go.opentelemetry.io/otel/trace"
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
    defer cleanup()

    // 获取追踪器
    tracer := trace.Tracer("my-service")
    
    // 创建 span
    ctx, span := tracer.Start(context.Background(), "main-operation")
    defer span.End()
    
    // 你的业务逻辑...
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
    otelemetry.WithProtocol(otelemetry.ProtocolGRPC),
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
)
```

#### 支持的协议类型

- `ProtocolGRPC`: gRPC 协议（默认）
- `ProtocolHTTP`: HTTP 协议
- `ProtocolJSON`: HTTP/JSON 协议

### 数据库追踪

#### MySQL (GORM) 追踪

```go
import (
    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
    "gorm.io/gorm"
)

// 创建 GORM 追踪钩子
hook := &otelemetry.GormTracingHook{
    Tracer: trace.Tracer("gorm"),
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
    Tracer: trace.Tracer("redis"),
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

### HTTP 追踪中间件

```go
import (
    "github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
    "net/http"
)

func main() {
    // 获取追踪器
    tracer := trace.Tracer("http-server")
    
    // 创建追踪中间件
    traceMiddleware := otelemetry.TraceMiddleware(tracer)
    
    // 应用中间件
    mux := http.NewServeMux()
    mux.HandleFunc("/api/users", userHandler)
    
    handler := traceMiddleware(mux)
    
    // 启动服务器
    http.ListenAndServe(":8080", handler)
}
```

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

### 环境变量支持

| 环境变量 | 描述 | 默认值 |
|---------|------|--------|
| `OTEL_SERVICE_NAME` | 服务名称 | `unknown-service` |
| `OTEL_SERVICE_VERSION` | 服务版本 | `1.0.0` |
| `OTEL_ENVIRONMENT` | 运行环境 | `development` |
| `OTEL_ENDPOINT` | OTLP 接收器地址 | `localhost:4317` |
| `OTEL_PROTOCOL` | 导出协议 | `grpc` |
| `OTEL_INSECURE` | 是否使用非安全连接 | `true` |
| `OTEL_SAMPLING_RATIO` | 采样率 | `1.0` |

### 配置优先级

1. 代码中的配置选项（最高优先级）
2. 环境变量
3. 默认配置（最低优先级）

## 📊 性能特性

- **采样控制**: 支持可配置的采样率，减少追踪数据量
- **批处理**: 自动批处理追踪数据，提高导出效率
- **异步导出**: 非阻塞的异步导出机制
- **内存优化**: 智能的内存管理和资源回收

## 🚨 注意事项

1. **资源清理**: 使用完毕后务必调用 `cleanup()` 函数释放资源
2. **错误处理**: 生产环境中应妥善处理初始化错误
3. **采样配置**: 高流量环境中建议使用较低的采样率
4. **网络配置**: 确保 OTLP 接收器地址可访问

## 🧪 运行示例

### HTTP 示例

```bash
cd example/http
go run main.go
```

访问 `http://localhost:8080/api/users/1` 查看追踪效果。

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
- 🌐 提供 HTTP 追踪中间件
- ⚙️ 灵活的配置选项

---

⭐ 如果这个项目对您有帮助，请给我们一个星标！
