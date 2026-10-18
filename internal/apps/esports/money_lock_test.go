package esports

import (
	"sync"
	"testing"
	"time"
)

func TestLockMoneyAccountSerializes(t *testing.T) {
	key := "test-account:" + t.Name()
	var wg sync.WaitGroup
	var current, peak int
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockMoneyAccount(key)
			defer unlock()
			current++
			if current > peak {
				peak = current
			}
			time.Sleep(time.Millisecond)
			current--
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("同一账户锁应串行，peak=%d", peak)
	}
}
