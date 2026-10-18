package esports

import "sync"

// moneyAccountLocks 是单实例内的账户级串行锁，用于余额/储值卡/猫粮的
// 「先求和后写负数流水」路径。多实例部署时仍需数据库余额快照或行锁；
// 这里先消除同一进程内并发核销/提现造成透支的窗口。
var moneyAccountLocks sync.Map

func lockMoneyAccount(key string) func() {
	value, _ := moneyAccountLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}
