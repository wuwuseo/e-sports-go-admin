package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/oauthsession"
	appoauth "go-admin/internal/oauth"

	"golang.org/x/oauth2"
)

const (
	oauthStateTTL     = 10 * time.Minute
	oauthLoginCodeTTL = time.Minute
)

var (
	ErrOAuthSessionInvalid   = errors.New("OAuth session is invalid or expired")
	ErrOAuthLoginCodeInvalid = errors.New("OAuth login code is invalid or expired")
)

type StartOAuthSessionRequest struct {
	Provider    string
	SubjectType string
	FlowType    string
	SubjectUID  string
}

type StartOAuthSessionResult struct {
	ID       int
	State    string
	Verifier string
}

type ClaimedOAuthSession struct {
	ID          int
	Provider    string
	SubjectType string
	FlowType    string
	SubjectUID  string
	Verifier    string
}

type OAuthLoginGrant struct {
	SubjectType string
	SubjectUID  string
}

type OAuthSessionService struct {
	client *ent.Client
	cipher *appoauth.SecretCipher
	now    func() time.Time
}

func NewOAuthSessionService(client *ent.Client, cipher *appoauth.SecretCipher, now func() time.Time) *OAuthSessionService {
	if now == nil {
		now = time.Now
	}
	return &OAuthSessionService{client: client, cipher: cipher, now: now}
}

func (s *OAuthSessionService) Start(ctx context.Context, request StartOAuthSessionRequest) (*StartOAuthSessionResult, error) {
	state, err := randomOpaqueValue(32)
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	encryptedVerifier, err := s.cipher.Encrypt(verifier)
	if err != nil {
		return nil, err
	}
	create := s.client.OAuthSession.Create().
		SetStateHash(hashOpaqueValue(state)).
		SetProvider(request.Provider).
		SetSubjectType(oauthsession.SubjectType(request.SubjectType)).
		SetFlowType(oauthsession.FlowType(request.FlowType)).
		SetPkceVerifierCiphertext(encryptedVerifier).
		SetExpiresAt(s.now().Add(oauthStateTTL))
	if request.SubjectUID != "" {
		create.SetSubjectUID(request.SubjectUID)
	}
	created, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	return &StartOAuthSessionResult{ID: created.ID, State: state, Verifier: verifier}, nil
}

func (s *OAuthSessionService) ConsumeCallback(ctx context.Context, state string) (*ClaimedOAuthSession, error) {
	now := s.now()
	session, err := s.client.OAuthSession.Query().Where(
		oauthsession.StateHashEQ(hashOpaqueValue(state)),
	).Only(ctx)
	if err != nil || session.Status != oauthsession.StatusPending || !session.ExpiresAt.After(now) {
		return nil, ErrOAuthSessionInvalid
	}
	affected, err := s.client.OAuthSession.Update().Where(
		oauthsession.IDEQ(session.ID),
		oauthsession.StatusEQ(oauthsession.StatusPending),
		oauthsession.ExpiresAtGT(now),
	).SetStatus(oauthsession.StatusCallbackConsumed).SetCallbackConsumedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrOAuthSessionInvalid
	}
	verifier, err := s.cipher.Decrypt(session.PkceVerifierCiphertext)
	if err != nil {
		return nil, ErrOAuthSessionInvalid
	}
	return &ClaimedOAuthSession{
		ID:          session.ID,
		Provider:    session.Provider,
		SubjectType: string(session.SubjectType),
		FlowType:    string(session.FlowType),
		SubjectUID:  session.SubjectUID,
		Verifier:    verifier,
	}, nil
}

func (s *OAuthSessionService) CompleteLogin(ctx context.Context, sessionID int, subjectUID string) (string, error) {
	code, err := randomOpaqueValue(32)
	if err != nil {
		return "", err
	}
	affected, err := s.client.OAuthSession.Update().Where(
		oauthsession.IDEQ(sessionID),
		oauthsession.StatusEQ(oauthsession.StatusCallbackConsumed),
	).SetLoginCodeHash(hashOpaqueValue(code)).
		SetLoginSubjectUID(subjectUID).
		SetLoginCodeExpiresAt(s.now().Add(oauthLoginCodeTTL)).
		SetStatus(oauthsession.StatusCompleted).
		Save(ctx)
	if err != nil {
		return "", err
	}
	if affected != 1 {
		return "", ErrOAuthSessionInvalid
	}
	return code, nil
}

func (s *OAuthSessionService) CompleteBind(ctx context.Context, sessionID int) error {
	affected, err := s.client.OAuthSession.Update().Where(
		oauthsession.IDEQ(sessionID),
		oauthsession.StatusEQ(oauthsession.StatusCallbackConsumed),
	).SetStatus(oauthsession.StatusConsumed).Save(ctx)
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOAuthSessionInvalid
	}
	return nil
}

func (s *OAuthSessionService) FailCallback(ctx context.Context, sessionID int, errorCode string) error {
	affected, err := s.client.OAuthSession.Update().Where(
		oauthsession.IDEQ(sessionID),
		oauthsession.StatusEQ(oauthsession.StatusCallbackConsumed),
	).SetStatus(oauthsession.StatusFailed).
		SetErrorCode(truncate(errorCode, 100)).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOAuthSessionInvalid
	}
	return nil
}

func (s *OAuthSessionService) ConsumeLoginCode(ctx context.Context, code string) (*OAuthLoginGrant, error) {
	now := s.now()
	session, err := s.client.OAuthSession.Query().Where(
		oauthsession.LoginCodeHashEQ(hashOpaqueValue(code)),
	).Only(ctx)
	if err != nil || session.Status != oauthsession.StatusCompleted || session.LoginCodeExpiresAt == nil || !session.LoginCodeExpiresAt.After(now) {
		return nil, ErrOAuthLoginCodeInvalid
	}
	affected, err := s.client.OAuthSession.Update().Where(
		oauthsession.IDEQ(session.ID),
		oauthsession.StatusEQ(oauthsession.StatusCompleted),
		oauthsession.LoginCodeExpiresAtGT(now),
		oauthsession.LoginCodeConsumedAtIsNil(),
	).SetStatus(oauthsession.StatusConsumed).SetLoginCodeConsumedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrOAuthLoginCodeInvalid
	}
	return &OAuthLoginGrant{SubjectType: string(session.SubjectType), SubjectUID: session.LoginSubjectUID}, nil
}

func randomOpaqueValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashOpaqueValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
