package service

import (
	"context"
	"errors"
	"testing"

	cmfqueue "github.com/wuwuseo/cmf/queue"

	"go-admin/internal/global"
)

// TestQueueTaskRejectsInvalidQueueName 队列名会拼入 asynq 的 Redis 键，
// 所有带队列参数的入口必须先过字符集白名单
// （校验先于可用性检查，因此可在无全局句柄的环境下断言）。
func TestQueueTaskRejectsInvalidQueueName(t *testing.T) {
	svc := NewQueueJobService()
	ctx := context.Background()
	for _, name := range []string{"bad name", "a$b", "队列", "a}b"} {
		if _, err := svc.ListTasks(ctx, &ListQueueTasksRequest{State: "pending", Queue: name}); !errors.Is(err, ErrQueueInvalidQueueName) {
			t.Errorf("ListTasks(queue=%q) err=%v, want ErrQueueInvalidQueueName", name, err)
		}
		if _, err := svc.GetTask(ctx, name, "id"); !errors.Is(err, ErrQueueInvalidQueueName) {
			t.Errorf("GetTask(queue=%q) err=%v, want ErrQueueInvalidQueueName", name, err)
		}
		if err := svc.RetryTask(ctx, name, "id"); !errors.Is(err, ErrQueueInvalidQueueName) {
			t.Errorf("RetryTask(queue=%q) err=%v, want ErrQueueInvalidQueueName", name, err)
		}
		if err := svc.DeleteTask(ctx, name, "id"); !errors.Is(err, ErrQueueInvalidQueueName) {
			t.Errorf("DeleteTask(queue=%q) err=%v, want ErrQueueInvalidQueueName", name, err)
		}
	}

	// 合法队列名应通过校验并继续走可用性检查（全局句柄未初始化 → 不可用）
	if _, err := svc.GetTask(ctx, "default", "id"); !errors.Is(err, ErrQueueUnavailable) {
		t.Errorf("合法队列名应通过校验并到达可用性检查，实际 err=%v", err)
	}
}

// TestQueueEnqueueRejectsInvalidQueueName 手动入队同样受白名单约束；
// 校验发生在任何 Redis 命令之前，因此可用未连接的句柄直接断言。
func TestQueueEnqueueRejectsInvalidQueueName(t *testing.T) {
	server := cmfqueue.NewServer(cmfqueue.DefaultConfig())
	server.Handle("t", func(ctx context.Context, task *cmfqueue.Task) error { return nil })
	client := cmfqueue.NewClient(cmfqueue.DefaultConfig())
	savedServer := global.QueueServer.Swap(server)
	savedClient := global.QueueClient.Swap(client)
	t.Cleanup(func() {
		global.QueueServer.Store(savedServer)
		global.QueueClient.Store(savedClient)
		_ = client.Close()
	})

	svc := NewQueueJobService()
	for _, name := range []string{"a b", "a$b"} {
		if _, err := svc.Enqueue(context.Background(), &EnqueueTaskRequest{Name: "t", Queue: name}); !errors.Is(err, ErrQueueInvalidQueueName) {
			t.Errorf("Enqueue(queue=%q) err=%v, want ErrQueueInvalidQueueName", name, err)
		}
	}
}
