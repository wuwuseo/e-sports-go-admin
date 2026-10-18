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

// moneyAccountLockKey 按账本身份生成账户锁键，供「求和 → 写负数流水」路径统一取锁：
// 客户余额 → balance:<ownerID>、储值卡（含预付款批次）→ card:<id>、猫粮 → catfood:<playerID>。
// 同一账户必须用同一把锁，否则跨路径并发仍可超扣（§4.2 / §11.6）。
func moneyAccountLockKey(accountType, accountID string) string {
	switch accountType {
	case walletStoredValueCard, storedValuePrepayAccount:
		return "card:" + accountID
	case walletPlayerCatfood:
		return "catfood:" + accountID
	default:
		return "balance:" + accountID
	}
}
