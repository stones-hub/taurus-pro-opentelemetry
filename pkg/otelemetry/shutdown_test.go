package otelemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var (
	errFakeStopped  = errors.New("fake exporter stopped")
	errFakeShutdown = errors.New("fake exporter shutdown failure")
)

// fakeExporter 记录导出与关闭情况的导出器。与真实 OTLP 导出器一致：
// Shutdown 会中断正在进行的导出，之后的导出立即失败
type fakeExporter struct {
	block       bool  // true 时导出阻塞，直到 release 关闭、被 Shutdown 中断或 ctx 结束
	shutdownErr error // 非 nil 时 Shutdown 返回 errors.Join(ctx.Err(), shutdownErr)

	mu          sync.Mutex
	exportCalls int // 收到的 ExportSpans 调用次数
	exported    int // 成功导出的 span 数
	interrupted int // 被 Shutdown 中断或在 Shutdown 之后被拒绝的导出次数

	entered    chan struct{} // 第一次 ExportSpans 时关闭
	release    chan struct{} // 关闭后放行阻塞的导出
	stopped    chan struct{} // 第一次 Shutdown 时关闭
	shutdownCh chan struct{} // 每次 Shutdown 调用发送一次
	enterOnce  sync.Once
	stopOnce   sync.Once
}

var _ sdktrace.SpanExporter = (*fakeExporter)(nil)

func newFakeExporter(block bool) *fakeExporter {
	return &fakeExporter{
		block:      block,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
		stopped:    make(chan struct{}),
		shutdownCh: make(chan struct{}, 16),
	}
}

func (f *fakeExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	f.mu.Lock()
	f.exportCalls++
	f.mu.Unlock()
	f.enterOnce.Do(func() { close(f.entered) })

	select {
	case <-f.stopped:
		f.addInterrupted()
		return errFakeStopped
	default:
	}
	if f.block {
		select {
		case <-f.release:
		case <-f.stopped:
			f.addInterrupted()
			return errFakeStopped
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.exported += len(spans)
	f.mu.Unlock()
	return nil
}

func (f *fakeExporter) Shutdown(ctx context.Context) error {
	f.stopOnce.Do(func() { close(f.stopped) })
	select {
	case f.shutdownCh <- struct{}{}:
	default:
	}
	if f.shutdownErr != nil {
		return errors.Join(ctx.Err(), f.shutdownErr)
	}
	return nil
}

func (f *fakeExporter) addInterrupted() {
	f.mu.Lock()
	f.interrupted++
	f.mu.Unlock()
}

func (f *fakeExporter) counts() (exportCalls, exported, interrupted int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exportCalls, f.exported, f.interrupted
}

// waitShutdownCalls 在 timeout 内等待导出器累计收到 n 次 Shutdown 调用
func waitShutdownCalls(f *fakeExporter, n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for range n {
		select {
		case <-f.shutdownCh:
		case <-deadline:
			return false
		}
	}
	return true
}

// TestShutdownPreCanceledStopsProcessor 调用前 ctx 已取消：
// 必须返回 ctx 错误、停止批处理器后台协程，之后旧追踪器结束的 span 不再导出，
// 再次 Shutdown 也不能用 nil 掩盖未完成的关闭
func TestShutdownPreCanceledStopsProcessor(t *testing.T) {
	const batchTimeout = 20 * time.Millisecond
	exp := newFakeExporter(false)
	p := newFakeProvider(t, exp, batchTimeout)
	tracer := p.Tracer("pre-canceled") // Shutdown 前拿到的 SDK 追踪器

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v, want context.Canceled", err)
	}

	// 一次来自本次 Shutdown 关闭导出器，一次来自批处理器后台协程退出后的收尾；
	// 后者只有在 processQueue/drainQueue 都返回之后才会发生
	if !waitShutdownCalls(exp, 2, 2*time.Second) {
		t.Fatal("batch span processor worker did not exit after pre-canceled Shutdown")
	}

	_, span := tracer.Start(context.Background(), "after-pre-canceled")
	span.End()
	time.Sleep(5 * batchTimeout)
	if calls, _, _ := exp.counts(); calls != 0 {
		t.Fatalf("ExportSpans called %d times after pre-canceled Shutdown, want 0", calls)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := p.Shutdown(ctx2); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Shutdown = %v, want the owner's context.Canceled", err)
	}
}

// TestShutdownMixedCtxDoesNotInterruptOwner owner 正在 flush 时，
// 带已取消 ctx 的旁观调用必须立即返回自己的 ctx 错误，且不能关闭导出器、打断 owner；
// 带有效 ctx 的旁观调用要等到 owner 完成并拿到 owner 的结果
func TestShutdownMixedCtxDoesNotInterruptOwner(t *testing.T) {
	exp := newFakeExporter(true)
	t.Cleanup(func() { closeOnce(exp.release) })
	p := newFakeProvider(t, exp, time.Hour)
	waiting := make(chan struct{}, 4)
	p.shutdownWaitHook = func() { waiting <- struct{}{} }
	endSpan(p, "mixed", "owner-span")

	ownerErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ownerErr <- p.Shutdown(ctx)
	}()
	if !waitClosed(exp.entered, 2*time.Second) {
		t.Fatal("owner flush never reached the exporter")
	}

	// 旁观者 B：ctx 已取消
	bctx, bcancel := context.WithCancel(context.Background())
	bcancel()
	begin := time.Now()
	if err := p.Shutdown(bctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("bystander Shutdown = %v, want its own context.Canceled", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("bystander Shutdown took %s", elapsed)
	}
	if n := len(exp.shutdownCh); n != 0 {
		t.Fatalf("bystander closed the exporter (%d Shutdown calls) while owner was flushing", n)
	}
	if !waitSignal(waiting, 0) {
		t.Fatal("bystander B did not go through the wait branch")
	}

	// 旁观者 C：ctx 有效，应等待 owner
	waiterErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		waiterErr <- p.Shutdown(ctx)
	}()
	// 同步点：C 已确认 owner 未完成并进入等待分支，此后它只能在 owner 完成或自身 ctx 到期时返回
	if !waitSignal(waiting, 2*time.Second) {
		t.Fatal("bystander C never entered the wait branch")
	}
	select {
	case err := <-ownerErr:
		t.Fatalf("owner returned before export was released: %v", err)
	case err := <-waiterErr:
		t.Fatalf("waiting bystander returned before owner finished: %v", err)
	default:
	}

	close(exp.release)
	for name, ch := range map[string]chan error{"owner": ownerErr, "waiting bystander": waiterErr} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s Shutdown = %v, want nil", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s Shutdown did not return after export was released", name)
		}
	}
	if _, exported, interrupted := exp.counts(); exported != 1 || interrupted != 0 {
		t.Fatalf("exported=%d interrupted=%d, want 1 and 0", exported, interrupted)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after owner finished = %v, want nil", err)
	}
}

// TestShutdownNilContext nil ctx 按 context.Background() 处理，正常 flush
func TestShutdownNilContext(t *testing.T) {
	exp := newFakeExporter(false)
	p := newFakeProvider(t, exp, time.Hour)
	endSpan(p, "nil-ctx", "nil-ctx-span")

	if err := p.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) = %v", err)
	}
	if _, exported, _ := exp.counts(); exported != 1 {
		t.Fatalf("exported = %d, want 1", exported)
	}
	if err := p.Shutdown(nil); err != nil {
		t.Fatalf("repeated Shutdown(nil) = %v", err)
	}
}

// TestShutdownPreCanceledReal 用真实 HTTP/gRPC collector 验证预取消：
// 返回 ctx 错误，之后旧追踪器的 span 不会到达 collector，再次调用返回 owner 的错误
func TestShutdownPreCanceledReal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol ExportProtocol
		start    func(*testing.T, *spanStore) string
	}{
		{"http", ProtocolHTTP, startHTTPReceiver},
		{"grpc", ProtocolGRPC, startGRPCCollector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			store := newSpanStore(false)
			p, _ := newTestProvider(t, tc.protocol, tc.start(t, store), WithBatchTimeout(20*time.Millisecond))
			tracer := p.Tracer("pre-canceled-real")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := p.Shutdown(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown error = %v, want context.Canceled", err)
			}
			_, span := tracer.Start(context.Background(), "after-pre-canceled")
			span.End()
			time.Sleep(200 * time.Millisecond)
			if got := store.snapshot(); len(got) != 0 {
				t.Fatalf("spans reached collector after pre-canceled Shutdown: %v", got)
			}
			if err := p.Shutdown(context.Background()); !errors.Is(err, context.Canceled) {
				t.Fatalf("second Shutdown = %v, want the owner's context.Canceled", err)
			}
		})
	}
}

// TestProtocolJSONSendsProtobuf ProtocolJSON 是历史别名，实际发送 protobuf
func TestProtocolJSONSendsProtobuf(t *testing.T) {
	store := newSpanStore(false)
	p, _ := newTestProvider(t, ProtocolJSON, startHTTPReceiver(t, store))
	endSpan(p, "json-scope", "json-span")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !store.has("json-scope", "json-span") {
		t.Fatalf("span not received, got %v", store.snapshot())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, ct := range store.contentTypes {
		if ct != "application/x-protobuf" {
			t.Fatalf("Content-Type = %q, want application/x-protobuf", ct)
		}
	}
}

// waitSignal 在 timeout 内从 ch 收到一个信号；timeout 为 0 时只检查已有信号
func waitSignal(ch <-chan struct{}, timeout time.Duration) bool {
	if timeout == 0 {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestShutdownKeepsNonContextStopErrors 强制收尾时，停止函数返回 errors.Join(ctx 错误, 真实错误)，
// 只能去掉 ctx 错误，真实错误必须保留，且返回值仍能用 errors.Is 识别 ctx 错误
func TestShutdownKeepsNonContextStopErrors(t *testing.T) {
	exp := newFakeExporter(false)
	exp.shutdownErr = errFakeShutdown
	p := newFakeProvider(t, exp, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v, want it to match context.Canceled", err)
	}
	if !errors.Is(err, errFakeShutdown) {
		t.Fatalf("Shutdown error = %v, want it to keep the exporter's non-context error", err)
	}
	if !waitShutdownCalls(exp, 2, 2*time.Second) {
		t.Fatal("batch span processor worker did not exit")
	}
}

var errInjectedPanic = errors.New("injected processor panic")

// panicProcessor 在 Shutdown 中等待放行后 panic，用来模拟 owner 没有正常返回
type panicProcessor struct {
	entered chan struct{}
	release chan struct{}
}

func (*panicProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (*panicProcessor) OnEnd(sdktrace.ReadOnlySpan)                     {}
func (*panicProcessor) ForceFlush(context.Context) error                { return nil }
func (pp *panicProcessor) Shutdown(context.Context) error {
	close(pp.entered)
	<-pp.release
	panic(errInjectedPanic)
}

// TestShutdownOwnerPanic owner panic 时：panic 传播给 owner 的调用方；
// 正在等待的旁观者（ctx 不会到期）不会永久挂起，而是得到 ErrShutdownIncomplete，之后的调用同样如此
func TestShutdownOwnerPanic(t *testing.T) {
	exp := newFakeExporter(false)
	p := newFakeProvider(t, exp, time.Hour)
	pp := &panicProcessor{entered: make(chan struct{}), release: make(chan struct{})}
	p.tracerProvider.RegisterSpanProcessor(pp)
	waiting := make(chan struct{}, 4)
	p.shutdownWaitHook = func() { waiting <- struct{}{} }

	ownerPanic := make(chan any, 1)
	go func() {
		defer func() { ownerPanic <- recover() }() // 仅测试中 recover，验证 panic 确实传给了调用方
		_ = p.Shutdown(context.Background())
	}()
	if !waitClosed(pp.entered, 2*time.Second) {
		t.Fatal("owner never reached the panicking processor")
	}

	waiterErr := make(chan error, 1)
	go func() { waiterErr <- p.Shutdown(context.Background()) }()
	if !waitSignal(waiting, 2*time.Second) {
		t.Fatal("bystander never entered the wait branch")
	}
	close(pp.release)

	select {
	case v := <-ownerPanic:
		if err, ok := v.(error); !ok || !errors.Is(err, errInjectedPanic) {
			t.Fatalf("owner recovered %v, want the injected panic", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not return")
	}
	select {
	case err := <-waiterErr:
		if !errors.Is(err, ErrShutdownIncomplete) {
			t.Fatalf("bystander Shutdown = %v, want ErrShutdownIncomplete", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bystander hung after owner panicked")
	}
	if err := p.Shutdown(context.Background()); !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Shutdown after owner panic = %v, want ErrShutdownIncomplete", err)
	}
}

// TestShutdownWaiterGetsOwnerResult 等待中的旁观者必须拿到 owner 的结果本身：
// owner 的结果是导出器关闭错误，旁观者返回 nil 或其他值都说明它没有真正等待 owner
func TestShutdownWaiterGetsOwnerResult(t *testing.T) {
	exp := newFakeExporter(true)
	exp.shutdownErr = errFakeShutdown
	t.Cleanup(func() { closeOnce(exp.release) })
	p := newFakeProvider(t, exp, time.Hour)
	waiting := make(chan struct{}, 4)
	p.shutdownWaitHook = func() { waiting <- struct{}{} }
	endSpan(p, "waiter", "owner-span")

	ownerErr := make(chan error, 1)
	go func() { ownerErr <- p.Shutdown(context.Background()) }()
	if !waitClosed(exp.entered, 2*time.Second) {
		t.Fatal("owner flush never reached the exporter")
	}
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- p.Shutdown(context.Background()) }()
	if !waitSignal(waiting, 2*time.Second) {
		t.Fatal("bystander never entered the wait branch")
	}
	close(exp.release)

	for name, ch := range map[string]chan error{"owner": ownerErr, "waiting bystander": waiterErr} {
		select {
		case err := <-ch:
			if !errors.Is(err, errFakeShutdown) {
				t.Fatalf("%s Shutdown = %v, want the owner's errFakeShutdown", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s Shutdown did not return", name)
		}
	}
}
