package oauth

import "testing"

// 本文件针对 internal/oauth/github.go 中包私有的
// selectVerifiedGitHubEmail 行为进行直接单元测试。
//
// 原因：FetchIdentity 的端到端测试已覆盖"返回 ErrVerifiedEmailRequired"，
// 但 selectVerifiedGitHubEmail 内部的优先级（primary+verified → 任意 verified → 空）
// 任一分支漂移都不会被现有端到端断言捕获，需要直接验证。

func TestSelectVerifiedGitHubEmail_PrefersPrimaryAndVerified(t *testing.T) {
	emails := []gitHubEmail{
		{Email: "secondary@example.com", Primary: false, Verified: true},
		{Email: "primary@example.com", Primary: true, Verified: true},
	}
	if got := selectVerifiedGitHubEmail(emails); got != "primary@example.com" {
		t.Fatalf("应优先返回 primary+verified 邮箱，got %q", got)
	}
}

func TestSelectVerifiedGitHubEmail_FallsBackToFirstVerified(t *testing.T) {
	// 真实场景：用户有 primary 但未 verified，其他 verified 邮箱应被选中。
	emails := []gitHubEmail{
		{Email: "primary-unverified@example.com", Primary: true, Verified: false},
		{Email: "secondary-verified@example.com", Primary: false, Verified: true},
		{Email: "another-verified@example.com", Primary: false, Verified: true},
	}
	if got := selectVerifiedGitHubEmail(emails); got != "secondary-verified@example.com" {
		t.Fatalf("无 primary+verified 时应回退到首个 verified 邮箱，got %q", got)
	}
}

func TestSelectVerifiedGitHubEmail_EmptyWhenNoVerified(t *testing.T) {
	emails := []gitHubEmail{
		{Email: "a@example.com", Primary: true, Verified: false},
		{Email: "b@example.com", Primary: false, Verified: false},
	}
	if got := selectVerifiedGitHubEmail(emails); got != "" {
		t.Fatalf("无 verified 邮箱应返回空以触发 ErrVerifiedEmailRequired，got %q", got)
	}
}

func TestSelectVerifiedGitHubEmail_EmptyInputReturnsEmpty(t *testing.T) {
	if got := selectVerifiedGitHubEmail(nil); got != "" {
		t.Fatalf("nil 输入应返回空，got %q", got)
	}
}
