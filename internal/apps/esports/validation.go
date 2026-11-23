package esports

// 幂等键长度区间（按字节）：AGENTS.md 的硬约束，所有写接口统一口径。
// 用 len 而非 rune 计数，保持存量校验语义不变。
const (
	idempotencyKeyMin = 8
	idempotencyKeyMax = 128
)

// validIdempotencyKey 判定幂等键长度是否合法（调用方负责先 TrimSpace）。
func validIdempotencyKey(key string) bool {
	return len(key) >= idempotencyKeyMin && len(key) <= idempotencyKeyMax
}
