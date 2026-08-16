package settings

import (
	"context"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

type ServiceAccountReader interface {
	ListServicePrincipals(context.Context) ([]access.Principal, error)
	ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error)
}

func LoadServiceAccounts(ctx context.Context, reader ServiceAccountReader, selectedID string) (ServiceAccountsSignal, error) {
	state := ServiceAccountsSignal{Items: []ServiceAccountSignal{}, SelectedID: strings.TrimSpace(selectedID), Secrets: []ServiceAccountSecretSignal{}}
	if reader == nil {
		return state, nil
	}
	principals, err := reader.ListServicePrincipals(ctx)
	if err != nil {
		return state, err
	}
	sort.SliceStable(principals, func(i, j int) bool {
		left := strings.ToLower(firstAccessValue(principals[i].DisplayName, principals[i].Email, principals[i].ID))
		right := strings.ToLower(firstAccessValue(principals[j].DisplayName, principals[j].Email, principals[j].ID))
		return left < right
	})
	for _, principal := range principals {
		state.Items = append(state.Items, ServiceAccountSignalFromPrincipal(principal))
	}
	if state.SelectedID != "" {
		secrets, err := reader.ListServicePrincipalSecrets(ctx, state.SelectedID)
		if err != nil {
			return state, err
		}
		for _, secret := range secrets {
			state.Secrets = append(state.Secrets, ServiceAccountSecretSignalFromDomain(secret))
		}
	}
	return state, nil
}
