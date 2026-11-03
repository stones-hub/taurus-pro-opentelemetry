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

package otelemetry

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const redisSpanKey = contextKey("redis_span")

// RedisHook Redis 的调用链监控钩子
type RedisHook struct {
	Tracer trace.Tracer
}

// BeforeProcess 在命令执行前创建 span
func (h *RedisHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	spanName := "redis." + cmd.Name()
	ctx, span := h.Tracer.Start(ctx, spanName,
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", cmd.Name()),
			attribute.String("db.statement", cmd.String()),
		))
	return context.WithValue(ctx, redisSpanKey, span), nil
}

// AfterProcess 在命令执行后结束 span
func (h *RedisHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	if span, ok := ctx.Value(redisSpanKey).(trace.Span); ok {
		defer span.End()
		if err := cmd.Err(); err != nil && err != redis.Nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}
	return nil
}

// BeforeProcessPipeline 在管道命令执行前创建 span
func (h *RedisHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	ctx, span := h.Tracer.Start(ctx, "redis.pipeline",
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "pipeline"),
			attribute.Int("db.redis.num_cmd", len(cmds)),
		))
	return context.WithValue(ctx, redisSpanKey, span), nil
}

// AfterProcessPipeline 在管道命令执行后结束 span
func (h *RedisHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	if span, ok := ctx.Value(redisSpanKey).(trace.Span); ok {
		defer span.End()
		for _, cmd := range cmds {
			if err := cmd.Err(); err != nil && err != redis.Nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				break
			}
		}
	}
	return nil
}

// DialHook 实现 v9 版本要求的接口
func (h *RedisHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook 实现 v9 版本要求的接口，包装单条命令的执行
func (h *RedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		ctx, _ = h.BeforeProcess(ctx, cmd)
		err := next(ctx, cmd)
		spanErr := err
		if spanErr == nil {
			spanErr = cmd.Err()
		}
		endRedisSpan(ctx, spanErr)
		return err
	}
}

// ProcessPipelineHook 实现 v9 版本要求的接口，包装管道命令的执行
func (h *RedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, _ = h.BeforeProcessPipeline(ctx, cmds)
		err := next(ctx, cmds)
		spanErr := err
		// next 返回 nil 或首个 redis.Nil 时，后续命令仍可能有真实错误
		if spanErr == nil || errors.Is(spanErr, redis.Nil) {
			for _, cmd := range cmds {
				if cerr := cmd.Err(); cerr != nil && !errors.Is(cerr, redis.Nil) {
					spanErr = cerr
					break
				}
			}
		}
		endRedisSpan(ctx, spanErr)
		return err
	}
}

// endRedisSpan 结束 ctx 中的 span；redis.Nil 不视为服务错误
func endRedisSpan(ctx context.Context, err error) {
	span, ok := ctx.Value(redisSpanKey).(trace.Span)
	if !ok {
		return
	}
	defer span.End()
	if err != nil && !errors.Is(err, redis.Nil) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
