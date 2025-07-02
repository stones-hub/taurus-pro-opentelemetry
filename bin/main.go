package main

import (
	"context"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-opentelemetry/pkg/otelemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// 用户模块追踪器
type userModule struct {
	tracer trace.Tracer
}

func newUserModule(tracer trace.Tracer) *userModule {
	return &userModule{tracer: tracer}
}

func (m *userModule) createUser(ctx context.Context, username string) {
	ctx, span := m.tracer.Start(ctx, "user.create")
	defer span.End()

	span.SetAttributes(
		attribute.String("user.name", username),
		attribute.String("module", "user"),
	)

	// 模拟用户创建
	time.Sleep(100 * time.Millisecond)
	span.AddEvent("user.created")

	// 模拟发送欢迎邮件
	m.sendWelcomeEmail(ctx, username)
}

func (m *userModule) sendWelcomeEmail(ctx context.Context, username string) {
	ctx, span := m.tracer.Start(ctx, "user.send_welcome_email")
	defer span.End()

	span.SetAttributes(
		attribute.String("user.name", username),
		attribute.String("email.type", "welcome"),
	)

	time.Sleep(50 * time.Millisecond)
	span.AddEvent("email.sent")
}

// 订单模块追踪器
type orderModule struct {
	tracer trace.Tracer
}

func newOrderModule(tracer trace.Tracer) *orderModule {
	return &orderModule{tracer: tracer}
}

func (m *orderModule) createOrder(ctx context.Context, orderID string) {
	ctx, span := m.tracer.Start(ctx, "order.create")
	defer span.End()

	span.SetAttributes(
		attribute.String("order.id", orderID),
		attribute.String("module", "order"),
	)

	// 模拟订单创建
	time.Sleep(150 * time.Millisecond)
	span.AddEvent("order.created")

	// 模拟支付处理
	m.processPayment(ctx, orderID)
}

func (m *orderModule) processPayment(ctx context.Context, orderID string) {
	ctx, span := m.tracer.Start(ctx, "order.process_payment")
	defer span.End()

	span.SetAttributes(
		attribute.String("order.id", orderID),
		attribute.String("payment.type", "online"),
	)

	time.Sleep(80 * time.Millisecond)
	span.AddEvent("payment.processed")
}

// 库存模块追踪器
type inventoryModule struct {
	tracer trace.Tracer
}

func newInventoryModule(tracer trace.Tracer) *inventoryModule {
	return &inventoryModule{tracer: tracer}
}

func (m *inventoryModule) checkStock(ctx context.Context, productID string) {
	ctx, span := m.tracer.Start(ctx, "inventory.check_stock")
	defer span.End()

	span.SetAttributes(
		attribute.String("product.id", productID),
		attribute.String("module", "inventory"),
	)

	// 模拟库存检查
	time.Sleep(60 * time.Millisecond)
	span.AddEvent("stock.checked")

	// 模拟库存更新
	m.updateStock(ctx, productID)
}

func (m *inventoryModule) updateStock(ctx context.Context, productID string) {
	ctx, span := m.tracer.Start(ctx, "inventory.update_stock")
	defer span.End()

	span.SetAttributes(
		attribute.String("product.id", productID),
		attribute.Int("quantity.change", -1),
	)

	time.Sleep(40 * time.Millisecond)
	span.AddEvent("stock.updated")
}

func main() {
	// 1. 初始化追踪器提供者
	provider, _, err := otelemetry.NewOTelProvider(
		otelemetry.WithServiceName("Taurus"),
		otelemetry.WithServiceVersion("v0.1.0"),
		otelemetry.WithEnvironment("dev"),
		otelemetry.WithExportProtocol(otelemetry.ProtocolGRPC),
		otelemetry.WithEndpoint("192.168.3.240:4317"),
		otelemetry.WithInsecure(true),
		otelemetry.WithTimeout(10*time.Second),
		otelemetry.WithSamplingRatio(1.0),
		otelemetry.WithBatchTimeout(10*time.Second),
		otelemetry.WithMaxExportBatchSize(10),
		otelemetry.WithMaxQueueSize(10),
		otelemetry.WithExportTimeout(10*time.Second),
	)
	if err != nil {
		log.Fatalf("init telemetry provider failed: %v", err)
	}
	defer provider.Shutdown(context.Background())

	// 2. 初始化各个模块的追踪器
	userMod := newUserModule(provider.Tracer("user-module"))
	orderMod := newOrderModule(provider.Tracer("order-module"))
	inventoryMod := newInventoryModule(provider.Tracer("inventory-module"))

	// 3. 模拟完整的业务流程：创建用户 -> 检查库存 -> 创建订单
	ctx := context.Background()

	// 创建用户
	userMod.createUser(ctx, "test_user")

	// 检查库存
	inventoryMod.checkStock(ctx, "product_123")

	// 创建订单
	orderMod.createOrder(ctx, "order_456")

	log.Println("Demo completed")
}
