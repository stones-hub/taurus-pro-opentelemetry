package otelemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

func newHookTracer(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, sr
}

type hookUser struct {
	ID   uint
	Name string
}

func TestGormHookRawExecAndSchema(t *testing.T) {
	tp, sr := newHookTracer(t)
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(&GormTracingHook{Tracer: tp.Tracer("t")}); err != nil {
		t.Fatal(err)
	}

	// 无 Schema 的原生 SQL 不应 panic
	if err := db.Exec("SELECT 1").Error; err != nil {
		t.Fatalf("raw exec: %v", err)
	}
	// 有 Schema 的操作保持可用
	var users []hookUser
	if err := db.Find(&users).Error; err != nil {
		t.Fatalf("find: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("want 2 ended spans, got %d", len(spans))
	}
	if spans[0].Name() != "gorm" {
		t.Errorf("raw span name = %q", spans[0].Name())
	}
	for _, a := range spans[0].Attributes() {
		if a.Key == "db.operation" {
			t.Errorf("raw span should not carry db.operation")
		}
	}
	if spans[1].Name() != "gorm.hook_users" {
		t.Errorf("schema span name = %q", spans[1].Name())
	}
	var op string
	for _, a := range spans[1].Attributes() {
		if a.Key == "db.operation" {
			op = a.Value.AsString()
		}
	}
	if op != "hook_users" {
		t.Errorf("db.operation = %q", op)
	}
}

type ctxKeyT struct{}

func TestRedisProcessHook(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		nextErr error
		cmdErr  error
		wantErr bool // span 状态是否为 Error
	}{
		{"ok", nil, nil, false},
		{"error", boom, nil, true},
		{"nil is not error", redis.Nil, nil, false},
		{"cmd err only", nil, boom, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tp, sr := newHookTracer(t)
			h := &RedisHook{Tracer: tp.Tracer("t")}
			calls := 0
			var gotCtx context.Context
			next := func(ctx context.Context, cmd redis.Cmder) error {
				calls++
				gotCtx = ctx
				if tc.cmdErr != nil {
					cmd.SetErr(tc.cmdErr)
				}
				return tc.nextErr
			}
			in := context.WithValue(context.Background(), ctxKeyT{}, "v")
			err := h.ProcessHook(next)(in, redis.NewStringCmd(in, "get", "k"))
			if calls != 1 {
				t.Fatalf("next calls = %d", calls)
			}
			if err != tc.nextErr {
				t.Errorf("err = %v, want %v", err, tc.nextErr)
			}
			if gotCtx.Value(ctxKeyT{}) != "v" || gotCtx.Value(redisSpanKey) == nil {
				t.Errorf("ctx passed to next lacks parent value or span")
			}
			spans := sr.Ended()
			if len(spans) != 1 || spans[0].Name() != "redis.get" {
				t.Fatalf("spans = %d", len(spans))
			}
			if got := spans[0].Status().Code == codes.Error; got != tc.wantErr {
				t.Errorf("error status = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

func TestRedisProcessPipelineHook(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		nextErr error
		cmdErr  error
		wantErr bool
	}{
		{"nil first then real cmd err", redis.Nil, boom, true},
		{"all nil", redis.Nil, redis.Nil, false},
		{"ok", nil, nil, false},
		{"error", boom, nil, true},
		{"nil is not error", nil, redis.Nil, false},
		{"cmd err only", nil, boom, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tp, sr := newHookTracer(t)
			h := &RedisHook{Tracer: tp.Tracer("t")}
			calls := 0
			var gotCtx context.Context
			next := func(ctx context.Context, cmds []redis.Cmder) error {
				calls++
				gotCtx = ctx
				if tc.cmdErr != nil {
					cmds[1].SetErr(tc.cmdErr)
				}
				return tc.nextErr
			}
			in := context.WithValue(context.Background(), ctxKeyT{}, "v")
			cmds := []redis.Cmder{redis.NewStringCmd(in, "get", "a"), redis.NewStringCmd(in, "get", "b")}
			err := h.ProcessPipelineHook(next)(in, cmds)
			if calls != 1 {
				t.Fatalf("next calls = %d", calls)
			}
			if err != tc.nextErr {
				t.Errorf("err = %v, want %v", err, tc.nextErr)
			}
			if gotCtx.Value(ctxKeyT{}) != "v" {
				t.Errorf("parent ctx value lost")
			}
			spans := sr.Ended()
			if len(spans) != 1 || spans[0].Name() != "redis.pipeline" {
				t.Fatalf("spans = %d", len(spans))
			}
			if got := spans[0].Status().Code == codes.Error; got != tc.wantErr {
				t.Errorf("error status = %v, want %v", got, tc.wantErr)
			}
		})
	}
}
