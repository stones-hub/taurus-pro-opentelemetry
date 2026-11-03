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
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// TracingInterceptor 创建一个带有追踪功能的 gRPC 拦截器
func TracingInterceptor(tracer trace.Tracer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// 从上下文中获取调用信息
		p, _ := peer.FromContext(ctx)
		md, _ := metadata.FromIncomingContext(ctx)

		// 创建新的 span
		spanName := "grpc." + info.FullMethod
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.method", info.FullMethod),
				attribute.String("peer.address", p.Addr.String()),
			),
		)
		defer span.End()

		// 记录请求元数据
		for k, v := range md {
			if len(v) > 0 {
				span.SetAttributes(attribute.String("rpc.metadata."+k, v[0]))
			}
		}

		// 记录开始时间
		startTime := time.Now()

		// 调用实际的处理器
		resp, err := handler(ctx, req)

		// 记录调用结果
		duration := time.Since(startTime)
		span.SetAttributes(attribute.String("rpc.duration", duration.String()))

		if err != nil {
			st, _ := status.FromError(err)
			span.SetStatus(codes.Error, st.Message())
			span.RecordError(err)
		} else {
			span.SetStatus(codes.Ok, "success")
		}

		return resp, err
	}
}

// StreamTracingInterceptor 创建一个带有追踪功能的 gRPC 流式拦截器
func StreamTracingInterceptor(tracer trace.Tracer) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// 从上下文中获取调用信息
		ctx := ss.Context()
		p, _ := peer.FromContext(ctx)
		md, _ := metadata.FromIncomingContext(ctx)

		// 创建新的 span
		spanName := "grpc.stream." + info.FullMethod
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.method", info.FullMethod),
				attribute.String("rpc.stream.type", streamType(info)),
				attribute.String("peer.address", p.Addr.String()),
			),
		)
		defer span.End()

		// 记录请求元数据
		for k, v := range md {
			if len(v) > 0 {
				span.SetAttributes(attribute.String("rpc.metadata."+k, v[0]))
			}
		}

		// 包装 ServerStream 以传递追踪上下文
		wrappedStream := &wrappedServerStream{
			ServerStream: ss,
			ctx:          ctx,
		}

		// 记录开始时间
		startTime := time.Now()

		// 调用实际的处理器
		err := handler(srv, wrappedStream)

		// 记录调用结果
		duration := time.Since(startTime)
		span.SetAttributes(attribute.String("rpc.duration", duration.String()))

		if err != nil {
			st, _ := status.FromError(err)
			span.SetStatus(codes.Error, st.Message())
			span.RecordError(err)
		} else {
			span.SetStatus(codes.Ok, "success")
		}

		return err
	}
}

// wrappedServerStream 包装 grpc.ServerStream 以传递追踪上下文
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// streamType 返回流的类型
func streamType(info *grpc.StreamServerInfo) string {
	if info.IsClientStream && info.IsServerStream {
		return "bidirectional"
	} else if info.IsClientStream {
		return "client"
	} else if info.IsServerStream {
		return "server"
	}
	return "unary"
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("grpc demo failed: %v", err)
	}
}

// run 返回前一定会执行 cleanup：收到 SIGINT/SIGTERM 后先有界地停止 gRPC 服务器，
// 再 flush 追踪数据；log.Fatalf 只在 main 中、所有 defer 执行完之后调用
func run() error {
	// 1. 初始化追踪器提供者
	provider, cleanup, err := otelemetry.NewOTelProvider(
		otelemetry.WithServiceName("grpc-demo"),
		otelemetry.WithServiceVersion("v0.1.0"),
		otelemetry.WithEnvironment("dev"),
	)
	if err != nil {
		return fmt.Errorf("init telemetry provider failed: %w", err)
	}
	// cleanup 以有限超时（默认 5 秒）关闭提供者并 flush 剩余 span，失败时写日志
	defer cleanup()

	// 2. 获取追踪器
	tracer := provider.Tracer("grpc-server")

	// 3. 启动 gRPC 服务器
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// 使用追踪拦截器
	s := grpc.NewServer(
		grpc.UnaryInterceptor(TracingInterceptor(tracer)),
		grpc.StreamInterceptor(StreamTracingInterceptor(tracer)),
	)

	// 这里可以注册你的 gRPC 服务
	// pb.RegisterYourServiceServer(s, &yourServiceServer{})

	// 4. 启动 gRPC 服务器，等待退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(lis) }()
	log.Printf("gRPC server listening at %v", lis.Addr())

	select {
	case err := <-serveErr:
		return fmt.Errorf("failed to serve: %w", err)
	case <-ctx.Done():
	}

	// 5. 有界地停止 gRPC 服务器：GracefulStop 最多等待 5 秒，超时后 Stop 强制关闭剩余连接，
	// 并发调用 Stop 会让仍在等待的 GracefulStop 返回
	log.Printf("shutting down gRPC server")
	stopped := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		s.Stop()
		<-stopped
	}
	return nil
}
