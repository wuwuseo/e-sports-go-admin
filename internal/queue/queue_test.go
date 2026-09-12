package queue_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wuwuseo/cmf/config"
	cmfqueue "github.com/wuwuseo/cmf/queue"
	"go-admin/internal/global"
	adminqueue "go-admin/internal/queue"
	"go-admin/internal/service"
)

func TestQueueConcurrentShutdown(t *testing.T) {
	previous := global.QueueServer.Swap(cmfqueue.NewServer(cmfqueue.DefaultConfig()))
	t.Cleanup(func() { global.QueueServer.Store(previous) })
	var wg sync.WaitGroup
	started := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc := service.NewQueueJobService()
		close(started)
		for n := 0; n < 10000; n++ {
			if _, err := svc.Handlers(context.Background()); err != nil && !errors.Is(err, service.ErrQueueUnavailable) {
				t.Error(err)
				return
			}
		}
	}()
	<-started
	adminqueue.Shutdown(time.Second)
	adminqueue.Shutdown(time.Second)
	wg.Wait()
	if global.QueueServer.Load() != nil {
		t.Fatal("server remains published after shutdown")
	}
}

func TestIsValidQueueName(t *testing.T) {
	for name, want := range map[string]bool{
		"default":               true,
		"high-priority":         true,
		"q_1":                   true,
		"Abc123":                true,
		strings.Repeat("a", 64): true,
		"":                      false,
		"bad name":              false,
		"a$b":                   false,
		"队列":                    false,
		"a}b":                   false,
		"asynq:{default}:t:x":   false,
		strings.Repeat("a", 65): false,
	} {
		if got := adminqueue.IsValidQueueName(name); got != want {
			t.Errorf("IsValidQueueName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestSetupRejectsInvalidQueueName 启动期 fail-fast：
// 非法队列名必须在触碰 Redis 之前被拒绝（配置地址故意不可达，若走到 Ping 说明校验位置不对）。
func TestSetupRejectsInvalidQueueName(t *testing.T) {
	saved := global.QueueServer.Swap(nil)
	t.Cleanup(func() { global.QueueServer.Store(saved) })

	cfg := &config.Config{}
	cfg.Queue.RedisConnection = "queue_redis"
	cfg.Queue.Queues = map[string]int{"bad name": 10}
	cfg.Redis.Connections = map[string]config.Redis{
		"queue_redis": {Addr: "127.0.0.1:1"},
	}

	err := adminqueue.Setup(cfg)
	if err == nil {
		t.Fatal("非法队列名应导致 Setup 失败")
	}
	if !strings.Contains(err.Error(), "非法") {
		t.Fatalf("错误信息应说明队列名非法，实际: %v", err)
	}
	if global.QueueServer.Load() != nil {
		t.Fatal("Setup 失败后不应挂载队列句柄")
	}
}
