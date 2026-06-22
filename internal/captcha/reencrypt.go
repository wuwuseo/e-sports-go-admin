package captcha

import (
	"context"
	"fmt"

	"go-admin/internal/ent"

	"github.com/wuwuseo/cmf/secureconfig"
)

func NewSecretManager(
	client *ent.Client,
	lookup func(string) string,
) (*secureconfig.Manager, error) {
	if client == nil {
		return nil, fmt.Errorf("captcha secret manager requires database")
	}
	keyring, err := secureconfig.NewKeyringFromLookup(lookup)
	if err != nil {
		return nil, err
	}
	return secureconfig.NewManager(
		NewSystemConfigRepository(client),
		secureconfig.NewAESGCMCipher(keyring),
	), nil
}

func ReencryptProviderConfigs(
	ctx context.Context,
	client *ent.Client,
	lookup func(string) string,
	dryRun bool,
) (secureconfig.ReencryptReport, error) {
	manager, err := NewSecretManager(client, lookup)
	if err != nil {
		return secureconfig.ReencryptReport{}, err
	}
	return manager.Reencrypt(ctx, providerKeyPrefix, dryRun)
}
