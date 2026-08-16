package controller

import (
	"context"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/secrets"
)

type WebhookSecretSource struct {
	Store     secrets.Store
	Reference string
}

func (source WebhookSecretSource) WebhookSecret(ctx context.Context) ([]byte, error) {
	if source.Store == nil || source.Reference == "" {
		return nil, fmt.Errorf("webhook secret source is unavailable")
	}
	value, err := source.Store.Get(ctx, source.Reference)
	if err != nil {
		return nil, fmt.Errorf("webhook secret is unavailable")
	}
	return value, nil
}
