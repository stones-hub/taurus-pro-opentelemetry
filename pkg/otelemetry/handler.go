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
	"log"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// 注册配置的调用链

var (
	tracerMu       sync.RWMutex
	tracerRegistry = make(map[string]trace.Tracer)
)

// RegisterTracer 按名称注册追踪器，可并发调用；同名重复注册会覆盖旧值，nil 会被忽略
func RegisterTracer(name string, tracer trace.Tracer) {
	if tracer == nil {
		log.Printf("Tracer %s is nil, ignored", name)
		return
	}
	tracerMu.Lock()
	defer tracerMu.Unlock()
	if _, exists := tracerRegistry[name]; exists {
		log.Printf("Tracer %s already registered", name)
	}
	tracerRegistry[name] = tracer
}

// GetTracer 返回已注册的追踪器，可并发调用
// 名称未注册时返回 otel.Tracer(name)，即全局 TracerProvider 的追踪器：
// NewOTelProvider 之前是委托追踪器（初始化后自动转发到真正的提供者），
// 之后是 SDK 追踪器，Shutdown 之后是 noop 追踪器，任何时候都不会返回 nil
func GetTracer(name string) trace.Tracer {
	tracerMu.RLock()
	tracer, exists := tracerRegistry[name]
	tracerMu.RUnlock()
	if exists {
		return tracer
	}
	return otel.Tracer(name)
}
