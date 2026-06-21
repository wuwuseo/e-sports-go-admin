package service

import (
	"errors"
	"testing"
)

func TestThirdPartyUserService_RejectsSecondAccountForSamePlatform(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	svc := NewThirdPartyUserService()
	u := createTestUserForThirdParty(t, client, "same-platform@test.com")
	createTestThirdPartyUser(t, client, u.ID, "github", "github-1")

	_, err := svc.BindThirdPartyUser(t.Context(), &BindThirdPartyRequest{
		UserID:   u.ID,
		Platform: "github",
		Openid:   "github-2",
	})
	if !errors.Is(err, ErrThirdPartyBound) {
		t.Fatalf("expected ErrThirdPartyBound, got %v", err)
	}
}
