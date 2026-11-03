package otelemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// spyExporter 包装真实的 OTLP 导出器，记录每次 Shutdown 调用。
// 批处理器后台协程只有在 processQueue/drainQueue 都返回后才会调用导出器的 Shutdown，
// 因此第二次 Shutdown 调用可以作为后台协程已退出的证据
type spyExporter struct {
	sdktrace.SpanExporter
	shutdowns chan error
	// beforeShutdown 非 nil 时在委托给真实导出器之前调用，用于构造确定的交错顺序
	beforeShutdown func()
}

func (s *spyExporter) Shutdown(ctx context.Context) error {
	if s.beforeShutdown != nil {
		s.beforeShutdown()
	}
	err := s.SpanExporter.Shutdown(ctx)
	select { // 不阻塞收尾流程
	case s.shutdowns <- err:
	default:
	}
	return err
}

// newRealSpyProvider 用真实导出器构造提供者：导出器和批处理器超时都是 30 秒、
// 批处理周期 1 小时，测试中 2 秒内发生的取消与退出只可能来自 Shutdown 的主动收尾
func newRealSpyProvider(t *testing.T, protocol ExportProtocol, endpoint string) (*OTelProvider, *spyExporter) {
	t.Helper()
	o := defaultOptions()
	o.protocol = protocol
	o.endpoint = endpoint
	o.insecure = true
	o.timeout = 30 * time.Second
	o.exportTimeout = 30 * time.Second
	o.batchTimeout = time.Hour
	p := &OTelProvider{opts: o}
	exp, err := p.createExporter()
	if err != nil {
		t.Fatalf("createExporter: %v", err)
	}
	spy := &spyExporter{SpanExporter: exp, shutdowns: make(chan error, 8)}
	p.exporter = spy
	p.tracerProvider, p.processor = p.createTracerProvider(spy, resource.Empty())
	return p, spy
}

func waitShutdowns(s *spyExporter, n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for range n {
		select {
		case <-s.shutdowns:
		case <-deadline:
			return false
		}
	}
	return true
}

var realCollectors = []struct {
	name     string
	protocol ExportProtocol
	start    func(*testing.T, *spanStore) string
}{
	{"http", ProtocolHTTP, startHTTPReceiver},
	{"grpc", ProtocolGRPC, startGRPCCollector},
}

// TestRealWorkerExitPreCanceled 队列中已有 span、collector 卡住、调用前 ctx 已取消：
// Shutdown 立即返回 ctx 错误，批处理器后台协程在 2 秒内退出；
// 若最后一次导出已经发到 collector，它必须被真实取消
func TestRealWorkerExitPreCanceled(t *testing.T) {
	for _, tc := range realCollectors {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			store := newSpanStore(true)
			p, spy := newRealSpyProvider(t, tc.protocol, tc.start(t, store))
			endSpan(p, "real-worker", "queued-span")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			begin := time.Now()
			err := p.Shutdown(ctx)
			if elapsed := time.Since(begin); elapsed > time.Second {
				t.Fatalf("Shutdown took %s", elapsed)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown error = %v, want context.Canceled", err)
			}
			if !waitShutdowns(spy, 2, 2*time.Second) {
				t.Fatal("batch span processor worker did not exit within 2s after pre-canceled Shutdown")
			}
			if isClosed(store.entered) {
				if !waitClosed(store.canceled, 2*time.Second) {
					t.Fatal("final export reached the collector but was not canceled")
				}
				t.Log("final export reached the collector and was canceled")
			} else {
				t.Log("final export failed before reaching the collector")
			}
		})
	}
}

// TestRealWorkerExitPreCanceledInFlight 与上一个测试相同，但让真实导出器的关闭先有界地等待
// 最后一次导出到达 collector，确定性地覆盖"预取消时请求已经发出"的交错：该请求必须被真实取消
func TestRealWorkerExitPreCanceledInFlight(t *testing.T) {
	for _, tc := range realCollectors {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			store := newSpanStore(true)
			p, spy := newRealSpyProvider(t, tc.protocol, tc.start(t, store))
			spy.beforeShutdown = func() { waitClosed(store.entered, time.Second) }
			endSpan(p, "real-worker", "queued-span")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			begin := time.Now()
			err := p.Shutdown(ctx)
			if elapsed := time.Since(begin); elapsed > 2*time.Second {
				t.Fatalf("Shutdown took %s", elapsed)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown error = %v, want context.Canceled", err)
			}
			if !isClosed(store.entered) {
				t.Fatal("final export never reached the collector")
			}
			if !waitClosed(store.canceled, 2*time.Second) {
				t.Fatal("in-flight final export was not canceled within 2s")
			}
			if !waitShutdowns(spy, 2, 2*time.Second) {
				t.Fatal("batch span processor worker did not exit within 2s")
			}
		})
	}
}

// TestRealWorkerExitDeadlineDuringFlush 队列中已有 span、collector 卡住、ctx 在 flush 中途到期：
// Shutdown 按截止时间返回 ctx 错误，已发出的请求被真实取消，批处理器后台协程在 2 秒内退出
func TestRealWorkerExitDeadlineDuringFlush(t *testing.T) {
	for _, tc := range realCollectors {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			store := newSpanStore(true)
			p, spy := newRealSpyProvider(t, tc.protocol, tc.start(t, store))
			endSpan(p, "real-worker", "queued-span")

			const deadline = 300 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			begin := time.Now()
			err := p.Shutdown(ctx)
			if elapsed := time.Since(begin); elapsed > deadline+time.Second {
				t.Fatalf("Shutdown took %s, deadline was %s", elapsed, deadline)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Shutdown error = %v, want context.DeadlineExceeded", err)
			}
			if !isClosed(store.entered) {
				t.Fatal("flush never reached the collector before the deadline")
			}
			if !waitClosed(store.canceled, 2*time.Second) {
				t.Fatal("in-flight export was not canceled within 2s")
			}
			if !waitShutdowns(spy, 2, 2*time.Second) {
				t.Fatal("batch span processor worker did not exit within 2s after the deadline")
			}
		})
	}
}

// isClosed 非阻塞地检查 ch 是否已关闭
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
