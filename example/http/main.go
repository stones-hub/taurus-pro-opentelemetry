// Copyright (c) 2025 Taurus Team. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Author: yelei
// Email: 61647649@qq.com
// Date: 2025-06-13

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// User 用户模型
type User struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TraceMiddleware 实现追踪中间件
func TraceMiddleware(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 从请求中获取父 span 的上下文
			ctx := r.Context()

			// 创建新的 span
			spanName := "http." + r.Method + "." + r.URL.Path
			ctx, span := tracer.Start(ctx, spanName,
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.url", r.URL.String()),
					attribute.String("http.path", r.URL.Path),
				),
			)
			defer span.End()

			// 记录请求开始时间
			startTime := time.Now()

			// 包装 ResponseWriter 以捕获状态码
			wrapped := wrapResponseWriter(w)

			// 调用下一个处理器
			next.ServeHTTP(wrapped, r.WithContext(ctx))

			// 记录响应信息
			duration := time.Since(startTime)
			span.SetAttributes(
				attribute.Int("http.status_code", wrapped.statusCode),
				attribute.String("http.duration", duration.String()),
			)
		})
	}
}

// responseWriter 包装 http.ResponseWriter 以捕获状态码
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// UserHandler 处理用户相关的请求
func UserHandler(w http.ResponseWriter, r *http.Request) {
	// 从路径中获取用户ID
	id := strings.TrimPrefix(r.URL.Path, "/user/")
	if id == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	// 获取当前 span
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.String("user.id", id))

	// 模拟用户数据
	user := User{
		ID:   1,
		Name: "test user",
		Age:  20,
	}

	// 返回 JSON 响应
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(user); err != nil {
		span.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("http demo failed: %v", err)
	}
}

// run 返回前一定会执行 cleanup：收到 SIGINT/SIGTERM 后先有界地关闭 HTTP 服务器，
// 再 flush 追踪数据；log.Fatalf 只在 main 中、所有 defer 执行完之后调用
func run() error {
	// 1. 初始化追踪器提供者
	provider, cleanup, err := otelemetry.NewOTelProvider(
		otelemetry.WithServiceName("http-demo"),
		otelemetry.WithServiceVersion("v0.1.0"),
		otelemetry.WithEnvironment("dev"),
	)
	if err != nil {
		return fmt.Errorf("init telemetry provider failed: %w", err)
	}
	// cleanup 以有限超时（默认 5 秒）关闭提供者并 flush 剩余 span，失败时写日志
	defer cleanup()

	// 2. 获取追踪器
	tracer := provider.Tracer("http-server")

	// 3. 创建 HTTP 处理器
	mux := http.NewServeMux()

	// 使用追踪中间件包装处理器
	handler := TraceMiddleware(tracer)(http.HandlerFunc(UserHandler))
	mux.Handle("/user/", handler)

	// 4. 启动 HTTP 服务器，等待退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: ":8080", Handler: mux}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Printf("HTTP server listening at :8080")

	select {
	case err := <-serveErr:
		return fmt.Errorf("run http server failed: %w", err)
	case <-ctx.Done():
	}

	// 5. 有界地关闭 HTTP 服务器：最多等待 5 秒处理中的请求，超时则强制关闭连接
	log.Printf("shutting down HTTP server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown http server: %w", err), srv.Close())
	}
	return nil
}
