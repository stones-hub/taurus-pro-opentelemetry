package otelemetry

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// idTracer 带编号的追踪器，用来按身份比较注册结果
type idTracer struct {
	noop.Tracer
	id int
}

var registryRun atomic.Int64

// uniquePrefix 为每次运行生成不同的名称前缀，-count 多轮运行时互不干扰
func uniquePrefix(kind string) string {
	return fmt.Sprintf("%s-%d-", kind, registryRun.Add(1))
}

// TestTracerRegistryConcurrent 让注册与读取在同一起跑点并发执行，
// 结束后逐个核对注册结果；没有同步时 -race 会报数据竞争，运行时也可能直接 fatal
func TestTracerRegistryConcurrent(t *testing.T) {
	captureLog(t) // 同名重复注册会打日志，这里只是静音

	const n = 64
	prefix := uniquePrefix("race")
	shared := prefix + "shared"
	want := make([]*idTracer, n)
	for i := range n {
		want[i] = &idTracer{id: i}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			RegisterTracer(prefix+strconv.Itoa(i), want[i])
			RegisterTracer(shared, want[i])
		})
		wg.Go(func() {
			<-start
			for range 100 {
				if GetTracer(prefix+strconv.Itoa(i)) == nil || GetTracer(shared) == nil {
					t.Error("GetTracer returned nil")
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()

	for i := range n {
		if got := GetTracer(prefix + strconv.Itoa(i)); got != trace.Tracer(want[i]) {
			t.Fatalf("tracer %d = %v, want %v", i, got, want[i])
		}
	}
	got, ok := GetTracer(shared).(*idTracer)
	if !ok || got != want[got.id] {
		t.Fatalf("shared tracer = %v, want one of the registered tracers", GetTracer(shared))
	}
}

func TestRegisterNilTracerIgnored(t *testing.T) {
	captureLog(t)
	name := uniquePrefix("nil") + "tracer"
	RegisterTracer(name, nil)
	tr := GetTracer(name)
	if tr == nil {
		t.Fatal("GetTracer returned nil after registering nil")
	}
	_, span := tr.Start(context.Background(), "nil-registered")
	span.End()
}

const lifecycleChildEnv = "OTELEMETRY_LIFECYCLE_CHILD"

// TestGetTracerLifecycle 在独立子进程里运行，保证 otel 全局提供者从未初始化状态开始：
// 初始化前、初始化后、Shutdown 后调用 GetTracer 都不 panic，
// 初始化前拿到的追踪器在初始化后会自动转发，未注册名称的 span 能真实上报
func TestGetTracerLifecycle(t *testing.T) {
	if os.Getenv(lifecycleChildEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGetTracerLifecycle$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), lifecycleChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("lifecycle child failed: %v\n%s", err, out)
		}
		return
	}

	captureLog(t)
	ctx := context.Background()

	// 1. 初始化前
	pre := GetTracer("pre-init-scope")
	if pre == nil {
		t.Fatal("GetTracer before init returned nil")
	}
	_, span := pre.Start(ctx, "before-init")
	if span.IsRecording() {
		t.Fatal("span before init should not be recording")
	}
	span.End()

	// 2. 初始化后
	store := newSpanStore(false)
	p, _ := newTestProvider(t, ProtocolHTTP, startHTTPReceiver(t, store))
	RegisterTracer("registered-scope", p.Tracer("registered-scope"))

	for _, tr := range []trace.Tracer{pre, GetTracer("unregistered-scope"), GetTracer("registered-scope")} {
		_, span := tr.Start(ctx, "after-init")
		if !span.IsRecording() {
			t.Fatal("span after init should be recording")
		}
		span.End()
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, scope := range []string{"pre-init-scope", "unregistered-scope", "registered-scope"} {
		if !store.has(scope, "after-init") {
			t.Fatalf("span from scope %q not received, got %v", scope, store.snapshot())
		}
	}
	if store.has("pre-init-scope", "before-init") {
		t.Fatal("span created before init should not be exported")
	}

	// 3. Shutdown 后
	before := len(store.snapshot())
	for _, name := range []string{"unregistered-scope", "registered-scope", "another-scope"} {
		tr := GetTracer(name)
		if tr == nil {
			t.Fatalf("GetTracer(%q) after Shutdown returned nil", name)
		}
		_, span := tr.Start(ctx, "after-shutdown")
		span.End()
	}
	if after := len(store.snapshot()); after != before {
		t.Fatalf("spans exported after Shutdown: %d -> %d", before, after)
	}
}
