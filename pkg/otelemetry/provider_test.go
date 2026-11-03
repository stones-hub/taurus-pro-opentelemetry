package otelemetry

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestProvider 创建一个只在 Shutdown 时才导出的提供者：批处理周期设为 1 小时，
// 导出器和批处理器的超时设为 30 秒，确保测试里观察到的截止时间只来自传入的 ctx
func newTestProvider(t *testing.T, protocol ExportProtocol, endpoint string, extra ...Option) (*OTelProvider, func()) {
	t.Helper()
	opts := append([]Option{
		WithServiceName("otelemetry-test"),
		WithExportProtocol(protocol),
		WithEndpoint(endpoint),
		WithInsecure(true),
		WithTimeout(30 * time.Second),
		WithExportTimeout(30 * time.Second),
		WithBatchTimeout(time.Hour),
	}, extra...)
	p, cleanup, err := NewOTelProvider(opts...)
	if err != nil {
		t.Fatalf("NewOTelProvider: %v", err)
	}
	return p, cleanup
}

func endSpan(p *OTelProvider, scope, name string) {
	_, span := p.Tracer(scope).Start(context.Background(), name)
	span.End()
}

// captureLog 把标准库 log 的输出重定向到缓冲区，测试结束时恢复
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDefaultShutdownTimeout(t *testing.T) {
	if DefaultShutdownTimeout != 5*time.Second {
		t.Fatalf("DefaultShutdownTimeout = %s, want 5s", DefaultShutdownTimeout)
	}
	if got := defaultOptions().shutdownTimeout; got != DefaultShutdownTimeout {
		t.Fatalf("default shutdown timeout = %s, want 5s", got)
	}
	o := defaultOptions()
	WithShutdownTimeout(0)(o)
	WithShutdownTimeout(-time.Second)(o)
	if o.shutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("non-positive timeout should keep default, got %s", o.shutdownTimeout)
	}
	WithShutdownTimeout(time.Second)(o)
	if o.shutdownTimeout != time.Second {
		t.Fatalf("shutdown timeout = %s, want 1s", o.shutdownTimeout)
	}
}

// TestShutdownFlushes 验证 span 只在 Shutdown 时被真实上报到 collector（HTTP 和 gRPC）
func TestShutdownFlushes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol ExportProtocol
		start    func(*testing.T, *spanStore) string
	}{
		{"http", ProtocolHTTP, startHTTPReceiver},
		{"grpc", ProtocolGRPC, startGRPCCollector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSpanStore(false)
			p, _ := newTestProvider(t, tc.protocol, tc.start(t, store))

			endSpan(p, "flush-scope", "flush-span")
			if got := store.snapshot(); len(got) != 0 {
				t.Fatalf("spans exported before Shutdown: %v", got)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if !store.has("flush-scope", "flush-span") {
				t.Fatalf("span not received after Shutdown, got %v", store.snapshot())
			}
		})
	}
}

// TestCleanupFlushes 验证 NewOTelProvider 返回的 cleanup 也会完成 flush，且成功时不打错误日志
func TestCleanupFlushes(t *testing.T) {
	logs := captureLog(t)
	store := newSpanStore(false)
	p, cleanup := newTestProvider(t, ProtocolHTTP, startHTTPReceiver(t, store))

	endSpan(p, "cleanup-scope", "cleanup-span")
	cleanup()
	if !store.has("cleanup-scope", "cleanup-span") {
		t.Fatalf("span not received after cleanup, got %v", store.snapshot())
	}
	if strings.Contains(logs.String(), "shutdown tracer provider failed") {
		t.Fatalf("unexpected error log: %s", logs.String())
	}
}

// TestShutdownRepeatedAndConcurrent 验证重复、并发调用 Shutdown 和 cleanup 都安全，span 只上报一次
func TestShutdownRepeatedAndConcurrent(t *testing.T) {
	logs := captureLog(t)
	store := newSpanStore(false)
	p, cleanup := newTestProvider(t, ProtocolHTTP, startHTTPReceiver(t, store))
	endSpan(p, "repeat-scope", "repeat-span")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			errs <- p.Shutdown(ctx)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Shutdown: %v", err)
		}
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("repeated Shutdown: %v", err)
	}
	cleanup()
	if strings.Contains(logs.String(), "shutdown tracer provider failed") {
		t.Fatalf("cleanup after Shutdown logged error: %s", logs.String())
	}
	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("want exactly 1 span exported, got %v", got)
	}
	// Shutdown 之后获取追踪器、创建 span 都不应 panic，也不会再上报
	endSpan(p, "repeat-scope", "after-shutdown")
	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("span exported after Shutdown: %v", got)
	}
}

// TestShutdownDeadlineBlockedCollector 验证 collector 卡住时 Shutdown 按 ctx 截止时间返回，
// 并且仍在进行的导出请求被真实中断（collector 端观察到取消），而不是在后台继续挂到导出超时
func TestShutdownDeadlineBlockedCollector(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol ExportProtocol
		start    func(*testing.T, *spanStore) string
	}{
		{"http", ProtocolHTTP, startHTTPReceiver},
		{"grpc", ProtocolGRPC, startGRPCCollector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSpanStore(true)
			p, _ := newTestProvider(t, tc.protocol, tc.start(t, store))
			endSpan(p, "blocked-scope", "blocked-span")

			const deadline = 500 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			begin := time.Now()
			err := p.Shutdown(ctx)
			elapsed := time.Since(begin)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Shutdown error = %v, want context.DeadlineExceeded", err)
			}
			if elapsed > deadline+time.Second {
				t.Fatalf("Shutdown took %s, deadline was %s", elapsed, deadline)
			}
			if !waitClosed(store.entered, time.Second) {
				t.Fatal("export request never reached the collector")
			}
			// 导出器与批处理器的超时都是 30 秒，2 秒内观察到取消只能来自 Shutdown 的中断
			if !waitClosed(store.canceled, 2*time.Second) {
				t.Fatal("in-flight export was not canceled after Shutdown deadline")
			}
			// 之后的调用返回 owner 的结果，不能用 nil 掩盖未完成的关闭
			if err := p.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("repeated Shutdown after timeout = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

// TestShutdownDeadlineUnreachable 验证 collector 不可达时 Shutdown 和 cleanup 都在限定时间内返回，且错误可见。
// HTTP 导出器对连接被拒不重试，flush 立即失败并通过 otel 全局错误处理器写日志，Shutdown 返回 nil；
// gRPC 导出器会重试 Unavailable，直到 ctx 到期，Shutdown 返回 context.DeadlineExceeded
func TestShutdownDeadlineUnreachable(t *testing.T) {
	for _, protocol := range []ExportProtocol{ProtocolHTTP, ProtocolGRPC} {
		t.Run(string(protocol)+"/shutdown", func(t *testing.T) {
			logs := captureLog(t)
			p, _ := newTestProvider(t, protocol, unreachableEndpoint(t))
			endSpan(p, "unreachable-scope", "unreachable-span")

			const deadline = 500 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			begin := time.Now()
			err := p.Shutdown(ctx)
			elapsed := time.Since(begin)
			if elapsed > deadline+time.Second {
				t.Fatalf("Shutdown took %s, deadline was %s", elapsed, deadline)
			}
			switch protocol {
			case ProtocolHTTP:
				if err != nil || !strings.Contains(logs.String(), "traces export") {
					t.Fatalf("want nil error and logged export failure, got err=%v log=%q", err, logs.String())
				}
			case ProtocolGRPC:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Shutdown error = %v, want context.DeadlineExceeded", err)
				}
			}
		})
		t.Run(string(protocol)+"/cleanup", func(t *testing.T) {
			logs := captureLog(t)
			const timeout = 300 * time.Millisecond
			p, cleanup := newTestProvider(t, protocol, unreachableEndpoint(t), WithShutdownTimeout(timeout))
			endSpan(p, "unreachable-scope", "unreachable-span")

			begin := time.Now()
			cleanup()
			elapsed := time.Since(begin)
			if elapsed > timeout+time.Second {
				t.Fatalf("cleanup took %s, timeout was %s", elapsed, timeout)
			}
			out := logs.String()
			switch protocol {
			case ProtocolHTTP:
				if !strings.Contains(out, "traces export") {
					t.Fatalf("export failure not logged, log output: %q", out)
				}
			case ProtocolGRPC:
				if !strings.Contains(out, "shutdown tracer provider failed") || !strings.Contains(out, context.DeadlineExceeded.Error()) {
					t.Fatalf("cleanup error not logged, log output: %q", out)
				}
			}
		})
	}
}

// TestNilProviderSafe 验证历史遗留的包级变量 Provider 保持为 nil，且调用其方法不会 panic
func TestNilProviderSafe(t *testing.T) {
	if Provider != nil {
		t.Fatal("package-level Provider is expected to stay nil")
	}
	if Provider.Tracer("nil-provider") == nil {
		t.Fatal("nil Provider returned nil tracer")
	}
	_, span := Provider.Tracer("nil-provider").Start(context.Background(), "nil-provider-span")
	span.End()
	if err := Provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Provider Shutdown: %v", err)
	}
	if err := (&OTelProvider{}).Shutdown(context.Background()); err != nil {
		t.Fatalf("zero OTelProvider Shutdown: %v", err)
	}
}
