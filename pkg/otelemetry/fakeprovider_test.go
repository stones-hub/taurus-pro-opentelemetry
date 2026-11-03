package otelemetry

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// newFakeProvider 用给定的导出器构造提供者，走与 NewOTelProvider 相同的批处理器配置，
// 但不设置 otel 全局提供者
func newFakeProvider(t *testing.T, exp sdktrace.SpanExporter, batchTimeout time.Duration) *OTelProvider {
	t.Helper()
	o := defaultOptions()
	o.batchTimeout = batchTimeout
	p := &OTelProvider{opts: o, exporter: exp}
	p.tracerProvider, p.processor = p.createTracerProvider(exp, resource.Empty())
	return p
}
