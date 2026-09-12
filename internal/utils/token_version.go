package utils

// TokenVersionMatches 校验已解析 JWT 的版本。旧令牌未携带版本，仅兼容从未吊销的账号。
func TokenVersionMatches(value any, current int) bool {
	if value == nil {
		return current == 0
	}
	version, ok := value.(float64)
	return ok && version == float64(current)
}
