package otelemetry

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// receivedSpan 记录 collector 实际收到的 span
type receivedSpan struct {
	scope string
	name  string
}

// spanStore 保存收到的 span，并可选地让导出请求阻塞，用来模拟卡住的 collector
type spanStore struct {
	mu           sync.Mutex
	spans        []receivedSpan
	contentTypes []string // HTTP 请求的 Content-Type

	block    bool          // true 时每个导出请求都阻塞，直到请求被取消或 release 关闭
	entered  chan struct{} // 第一个导出请求到达时关闭
	canceled chan struct{} // 第一个阻塞中的请求观察到取消时关闭
	release  chan struct{} // 关闭后放行所有阻塞的请求
	once1    sync.Once
	once2    sync.Once
}

func newSpanStore(block bool) *spanStore {
	return &spanStore{
		block:    block,
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
}

// handle 处理一次导出，返回 false 表示请求在阻塞期间被调用方取消
func (s *spanStore) handle(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) bool {
	s.once1.Do(func() { close(s.entered) })
	if s.block {
		select {
		case <-ctx.Done():
			s.once2.Do(func() { close(s.canceled) })
			return false
		case <-s.release:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				s.spans = append(s.spans, receivedSpan{scope: ss.GetScope().GetName(), name: sp.GetName()})
			}
		}
	}
	return true
}

func (s *spanStore) snapshot() []receivedSpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]receivedSpan(nil), s.spans...)
}

func (s *spanStore) has(scope, name string) bool {
	for _, sp := range s.snapshot() {
		if sp.scope == scope && sp.name == name {
			return true
		}
	}
	return false
}

// startHTTPReceiver 启动一个真实解析 OTLP/HTTP protobuf 请求的 receiver，返回 host:port
func startHTTPReceiver(t *testing.T, store *spanStore) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		store.mu.Lock()
		store.contentTypes = append(store.contentTypes, r.Header.Get("Content-Type"))
		store.mu.Unlock()
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer gz.Close()
			body = gz
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := &coltracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(raw, req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !store.handle(r.Context(), req) {
			return
		}
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	// Close 会等待处理中的请求，先放行阻塞的请求
	t.Cleanup(func() {
		closeOnce(store.release)
		srv.Close()
	})
	return strings.TrimPrefix(srv.URL, "http://")
}

type grpcTraceService struct {
	coltracepb.UnimplementedTraceServiceServer
	store *spanStore
}

func (g *grpcTraceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if !g.store.handle(ctx, req) {
		return nil, ctx.Err()
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// startGRPCCollector 在本地端口启动一个真实的 OTLP/gRPC collector，返回 host:port
func startGRPCCollector(t *testing.T, store *spanStore) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(srv, &grpcTraceService{store: store})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		closeOnce(store.release)
		srv.Stop()
	})
	return lis.Addr().String()
}

// unreachableEndpoint 返回一个刚刚释放、当前没有监听者的本地地址
func unreachableEndpoint(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// waitClosed 在 timeout 内等待 ch 被关闭
func waitClosed(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}
