package service

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/oauthsession"
	appoauth "go-admin/internal/oauth"

	_ "github.com/mattn/go-sqlite3"
)

func setupOAuthServiceTest(t *testing.T) (*ent.Client, *appoauth.SecretCipher) {
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { _ = client.Close() })
	cipher, err := appoauth.NewSecretCipher(bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	return client, cipher
}

func TestOAuthSessionServiceConsumesStateAndLoginCodeOnce(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	service := NewOAuthSessionService(client, cipher, func() time.Time { return now })

	started, err := service.Start(t.Context(), StartOAuthSessionRequest{
		Provider:    "github",
		SubjectType: "user",
		FlowType:    "login",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	stored, err := client.OAuthSession.Query().Only(t.Context())
	if err != nil {
		t.Fatalf("query session: %v", err)
	}
	if stored.StateHash == started.State || stored.PkceVerifierCiphertext == started.Verifier {
		t.Fatal("state or PKCE verifier stored in plaintext")
	}

	claimed, err := service.ConsumeCallback(t.Context(), started.State)
	if err != nil {
		t.Fatalf("ConsumeCallback: %v", err)
	}
	if claimed.Verifier != started.Verifier || claimed.SubjectType != "user" {
		t.Fatalf("unexpected claimed session: %#v", claimed)
	}
	if _, err := service.ConsumeCallback(t.Context(), started.State); !errors.Is(err, ErrOAuthSessionInvalid) {
		t.Fatalf("expected replay rejection, got %v", err)
	}

	code, err := service.CompleteLogin(t.Context(), claimed.ID, "user-uid")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	grant, err := service.ConsumeLoginCode(t.Context(), code)
	if err != nil {
		t.Fatalf("ConsumeLoginCode: %v", err)
	}
	if grant.SubjectType != "user" || grant.SubjectUID != "user-uid" {
		t.Fatalf("unexpected grant: %#v", grant)
	}
	if _, err := service.ConsumeLoginCode(t.Context(), code); !errors.Is(err, ErrOAuthLoginCodeInvalid) {
		t.Fatalf("expected login code replay rejection, got %v", err)
	}

	status, err := client.OAuthSession.Query().Where(oauthsession.IDEQ(claimed.ID)).Select(oauthsession.FieldStatus).String(t.Context())
	if err != nil || status != string(oauthsession.StatusConsumed) {
		t.Fatalf("unexpected final status %q, err=%v", status, err)
	}
}

func TestOAuthSessionServiceRejectsExpiredState(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	service := NewOAuthSessionService(client, cipher, func() time.Time { return now })
	started, err := service.Start(t.Context(), StartOAuthSessionRequest{Provider: "github", SubjectType: "admin", FlowType: "bind", SubjectUID: "admin-uid"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.ConsumeCallback(t.Context(), started.State); !errors.Is(err, ErrOAuthSessionInvalid) {
		t.Fatalf("expected expired state rejection, got %v", err)
	}
}

func TestOAuthSessionServiceAllowsOnlyOneConcurrentStateAndCodeConsumer(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	service := NewOAuthSessionService(client, cipher, func() time.Time { return now })
	started, err := service.Start(t.Context(), StartOAuthSessionRequest{
		Provider: "github", SubjectType: "user", FlowType: "login",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	type callbackAttempt struct {
		claim *ClaimedOAuthSession
		err   error
	}
	callbacks := make(chan callbackAttempt, 8)
	for range 8 {
		go func() {
			claim, err := service.ConsumeCallback(t.Context(), started.State)
			callbacks <- callbackAttempt{claim: claim, err: err}
		}()
	}
	var winningClaim *ClaimedOAuthSession
	callbackSuccesses := 0
	for range 8 {
		attempt := <-callbacks
		if attempt.err == nil {
			callbackSuccesses++
			winningClaim = attempt.claim
		}
	}
	if callbackSuccesses != 1 || winningClaim == nil {
		t.Fatalf("concurrent state consumption succeeded %d times", callbackSuccesses)
	}

	code, err := service.CompleteLogin(t.Context(), winningClaim.ID, "user-uid")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	codes := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := service.ConsumeLoginCode(t.Context(), code)
			codes <- err
		}()
	}
	codeSuccesses := 0
	for range 8 {
		if err := <-codes; err == nil {
			codeSuccesses++
		}
	}
	if codeSuccesses != 1 {
		t.Fatalf("concurrent login-code consumption succeeded %d times", codeSuccesses)
	}
}
