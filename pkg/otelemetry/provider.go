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

// Package otelemetry 提供分布式追踪功能
package otelemetry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

// Protocol 定义了支持的协议类型
type Protocol = ExportProtocol

// OTelProvider OpenTelemetry 追踪提供者实现
type OTelProvider struct {
	opts           *options
	tracerProvider *sdktrace.TracerProvider // 追踪提供者实例
	processor      sdktrace.SpanProcessor   // 批处理器，Shutdown 因 ctx 失败时用于停止其后台协程
	exporter       sdktrace.SpanExporter    // 导出器，Shutdown 因 ctx 失败时用于中断仍在进行的导出

	// 关闭状态：第一个调用 Shutdown 的是 owner，负责真正关闭；
	// 其余调用等待 owner 完成并返回它的结果，最多等到各自的 ctx 结束
	shutdownMu   sync.Mutex
	shutdownDone chan struct{} // owner 完成时关闭
	shutdownErr  error         // owner 的结果，在 shutdownDone 关闭前写入

	// shutdownWaitHook 非 owner 调用即将开始等待 owner 时调用，仅供测试观察，生产代码中始终为 nil
	shutdownWaitHook func()
}

// ErrShutdownIncomplete 负责关闭的那次 Shutdown 调用没有正常返回（例如发生了 panic），
// 关闭流程是否完成未知。等待它的其他调用以及之后的调用都会返回该错误
var ErrShutdownIncomplete = errors.New("otelemetry: shutdown did not complete normally")

var (
	// Provider 历史遗留的包级变量，本包从不给它赋值，它始终为 nil。
	//
	// Deprecated: 请使用 NewOTelProvider 返回的 *OTelProvider，或通过
	// otel.GetTracerProvider() / GetTracer 获取追踪器。保留该变量只为不破坏编译。
	Provider *OTelProvider
)

// NewOTelProvider 创建新的 OpenTelemetry 追踪提供者
// 该函数会初始化所有必要的组件，包括导出器、资源属性和采样器
func NewOTelProvider(opts ...Option) (*OTelProvider, func(), error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	p := &OTelProvider{
		opts: options,
	}

	// 1.创建资源 (采集的数据的标记，让后续的分析可以识别)
	// 先于导出器创建：资源创建失败时还没有需要关闭的连接
	res, err := p.createResource()
	if err != nil {
		return nil, nil, fmt.Errorf("setup provider failed: create resource failed: %w", err)
	}

	// 2. 创建 OTLP 导出器（决定数据导出/发送到哪里）
	exporter, err := p.createExporter()
	if err != nil {
		return nil, nil, fmt.Errorf("setup provider failed: create exporter failed: %w", err)
	}

	// 3. 创建追踪器的提供者, 将导出器和资源属性传递给追踪器提供者
	p.exporter = exporter
	p.tracerProvider, p.processor = p.createTracerProvider(exporter, res)

	// 将我们创建的追踪提供者设置为全局默认提供者
	// 这样其他包就可以直接使用 otel.GetTracerProvider() 获取到这个提供者
	// 就像一个总控制台，告诉系统："这就是我们要用的追踪系统"
	otel.SetTracerProvider(p.tracerProvider)

	// 设置如何在服务之间传递追踪信息
	// 约定一个通用的交流方式，让不同的服务能互相理解追踪信息
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C 标准的追踪上下文传播
		propagation.Baggage{},      // 用于传递自定义属性
	))

	// cleanup 使用有限的超时时间关闭，避免 collector 不可达或阻塞时无限等待；
	// 需要自定义截止时间的调用方应直接调用 Shutdown(ctx)
	shutdownTimeout := options.shutdownTimeout
	return p, func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			log.Printf("otelemetry: shutdown tracer provider failed (timeout %s): %v", shutdownTimeout, err)
		}
	}, nil
}

// exportEndpoint 返回导出地址，未设置时按协议使用 OTLP 标准端口：gRPC 为 4317，HTTP 为 4318
func exportEndpoint(protocol ExportProtocol, endpoint string) string {
	if endpoint != "" {
		return endpoint
	}
	if protocol == ProtocolHTTP || protocol == ProtocolJSON {
		return "localhost:4318"
	}
	return "localhost:4317"
}

// createExporter 创建 OTLP 导出器
func (p *OTelProvider) createExporter() (sdktrace.SpanExporter, error) {
	endpoint := exportEndpoint(p.opts.protocol, p.opts.endpoint)

	switch p.opts.protocol {
	case ProtocolGRPC:
		var exporterOpts []otlptracegrpc.Option
		if p.opts.insecure {
			exporterOpts = append(exporterOpts,
				otlptracegrpc.WithInsecure(),
				otlptracegrpc.WithEndpoint(endpoint))
		} else {
			exporterOpts = append(exporterOpts,
				otlptracegrpc.WithEndpoint(endpoint))
		}

		if p.opts.timeout > 0 {
			exporterOpts = append(exporterOpts, otlptracegrpc.WithTimeout(p.opts.timeout))
		}

		return otlptracegrpc.New(context.Background(), exporterOpts...)

	case ProtocolHTTP, ProtocolJSON:
		// ProtocolJSON 是历史别名：与 ProtocolHTTP 完全相同，发送的是 protobuf 编码
		var exporterOpts []otlptracehttp.Option
		if p.opts.insecure {
			exporterOpts = append(exporterOpts,
				otlptracehttp.WithInsecure(),
				otlptracehttp.WithEndpoint(endpoint))
		} else {
			exporterOpts = append(exporterOpts,
				otlptracehttp.WithEndpoint(endpoint))
		}

		if p.opts.timeout > 0 {
			exporterOpts = append(exporterOpts, otlptracehttp.WithTimeout(p.opts.timeout))
		}

		return otlptracehttp.New(context.Background(), exporterOpts...)

	default:
		return nil, fmt.Errorf("unsupported protocol: %v", p.opts.protocol)
	}
}

// createResource 创建资源属性
func (p *OTelProvider) createResource() (*resource.Resource, error) {
	return resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(p.opts.serviceName),
			semconv.ServiceVersion(p.opts.serviceVersion),
			attribute.String("environment", p.opts.environment),
		),
	)
}

// createTracerProvider 创建追踪提供者实例，同时返回其中的批处理器
// 配置如何收集、处理和导出追踪数据
func (p *OTelProvider) createTracerProvider(exp sdktrace.SpanExporter, res *resource.Resource) (*sdktrace.TracerProvider, sdktrace.SpanProcessor) {
	// 1. 配置批处理导出器（等价于 sdktrace.WithBatcher，自己持有批处理器以便 Shutdown 时能单独停止它）
	processor := sdktrace.NewBatchSpanProcessor(exp,
		// 一次最多导出多少条数据
		// 比如设置为 512，就是凑够 512 条数据就会导出一次
		sdktrace.WithMaxExportBatchSize(p.opts.maxExportBatchSize),

		// 多久导出一次数据
		// 比如设置为 5s，即使数据没凑够 512 条，5s 后也会导出
		sdktrace.WithBatchTimeout(p.opts.batchTimeout),

		// 最多能缓存多少条待导出的数据
		// 比如设置为 2048，超过后新的数据就会被丢弃
		sdktrace.WithMaxQueueSize(p.opts.maxQueueSize),

		// 导出超时时间
		// 比如设置为 10s，如果导出超时，数据就会被丢弃
		sdktrace.WithExportTimeout(p.opts.exportTimeout),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),

		// 2. 设置资源属性
		// 就像前面说的快递单，每条数据都会带上这些标签
		sdktrace.WithResource(res),

		// 3. 配置采样策略
		sdktrace.WithSampler(
			// 使用基于父 Span 的采样策略
			sdktrace.ParentBased(
				// 设置采样比例
				// samplingRatio 范围是 0-1
				// 0 表示不采样，1 表示全采样
				// 0.1 表示采样 10% 的数据
				sdktrace.TraceIDRatioBased(p.opts.samplingRatio),
			),
		),
	)
	return tp, processor
}

// Tracer 返回指定名称的追踪器
// p 为 nil（例如历史遗留的包级变量 Provider）时返回全局 TracerProvider 的追踪器；
// Shutdown 之后返回的是不做任何事的 noop 追踪器
func (p *OTelProvider) Tracer(name string) trace.Tracer {
	if p == nil || p.tracerProvider == nil {
		return otel.GetTracerProvider().Tracer(name)
	}
	return p.tracerProvider.Tracer(name)
}

// Shutdown 关闭追踪提供者：先把批处理队列中剩余的 span 导出，再关闭导出器。
//
//   - ctx 的截止时间同时约束 flush 和导出器关闭；本次调用因 ctx 结束而未能完成时
//     （flush 中途到期，或调用前 ctx 就已结束），返回包含 ctx 错误的错误，并强制停止
//     批处理器和导出器：中断仍在进行的 OTLP 请求，之后已有追踪器结束的 span 不再入队。
//     队列中尚未导出的 span 按尽力而为处理：批处理器停止时仍会做最后一次导出，
//     它与导出器关闭之间存在竞争，这些 span 可能已经送达，也可能被丢弃。
//   - 只有第一次调用（owner）真正执行关闭。并发或之后的调用不会触碰导出器，
//     而是等待 owner 完成并返回 owner 的结果；若在此之前自己的 ctx 先结束，
//     则返回自己 ctx 的错误，这并不代表 owner 已完成或失败。owner 没有正常返回
//     （例如 panic，panic 仍会传播给 owner 的调用方）时，其他调用返回 ErrShutdownIncomplete。
//     因此返回 nil 表示关闭流程确实已经执行完毕；但 flush 时的导出错误由 SDK
//     交给 otel 全局错误处理器（默认写标准库 log），不会体现在返回值中。
//   - ctx 为 nil 时按 context.Background() 处理，此时等待时长只受导出超时
//     （WithTimeout、WithExportTimeout）约束，建议总是传入带截止时间的 ctx。
func (p *OTelProvider) Shutdown(ctx context.Context) error {
	if p == nil || p.tracerProvider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.shutdownMu.Lock()
	done := p.shutdownDone
	owner := done == nil
	if owner {
		done = make(chan struct{})
		p.shutdownDone = done
	}
	p.shutdownMu.Unlock()

	if owner {
		return p.ownerShutdown(ctx, done)
	}

	// owner 已完成时优先返回它的结果，即使本次 ctx 也已结束
	select {
	case <-done:
		return p.shutdownErr
	default:
	}
	if p.shutdownWaitHook != nil {
		p.shutdownWaitHook()
	}
	select {
	case <-done:
		return p.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ownerShutdown 执行关闭并发布结果。无论 shutdown 是否正常返回都会关闭 done，
// 避免等待者永久阻塞；不正常返回时结果保持为 ErrShutdownIncomplete，panic 继续向上传播
func (p *OTelProvider) ownerShutdown(ctx context.Context, done chan struct{}) error {
	p.shutdownErr = ErrShutdownIncomplete
	defer close(done)
	err := p.shutdown(ctx)
	p.shutdownErr = err
	return err
}

// shutdown 由 owner 调用，执行真正的关闭
func (p *OTelProvider) shutdown(ctx context.Context) error {
	err := p.tracerProvider.Shutdown(ctx)
	ctxErr := ctx.Err()
	if ctxErr == nil || !errors.Is(err, ctxErr) {
		// 正常完成，或失败原因与本次 ctx 无关：批处理器和导出器已由 SDK 关闭
		return err
	}

	// 本次调用因自身 ctx 结束而没有完成关闭，SDK 留下了两种需要收尾的状态：
	//   - ctx 调用前已结束：SDK 已标记关闭，却没有调用批处理器的 Shutdown，
	//     其后台协程会一直运行，已有追踪器结束的 span 仍会入队并定期导出；
	//   - ctx 在 flush 期间到期：批处理器最后一次导出使用 context.Background() +
	//     ExportTimeout，不受 ctx 约束，会在后台继续阻塞。
	// 批处理器的 Shutdown 在 ctx 已结束时也会停止接收 span、通知后台协程退出，然后立即返回；
	// 后台协程退出前还会做最后一次导出。随后关闭导出器，中断这次导出以及仍在进行的
	// OTLP 请求，后台协程因此能在有限时间内退出。两者的 Shutdown 都只执行一次，
	// 已由 SDK 关闭过时这里是空操作。它们返回的错误中，ctx 错误已包含在 err 里，
	// 只追加其余的真实错误
	for _, stop := range []func(context.Context) error{p.processor.Shutdown, p.exporter.Shutdown} {
		if stopErr := withoutErr(stop(ctx), ctxErr); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
	}
	return err
}

// withoutErr 从 err 中去掉与 target 匹配（errors.Is）的部分，保留其余错误。
// err 由 errors.Join 等返回多个错误时逐个处理，因此 Join(target, 其他错误) 只会去掉 target
func withoutErr(err, target error) error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var rest []error
		for _, e := range multi.Unwrap() {
			if e = withoutErr(e, target); e != nil {
				rest = append(rest, e)
			}
		}
		return errors.Join(rest...)
	}
	if errors.Is(err, target) {
		return nil
	}
	return err
}
