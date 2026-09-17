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

type serviceAccountSecretCountReader interface {
	CountServicePrincipalSecrets(context.Context) (map[string]int, error)
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
	secretCounts := map[string]int{}
	if counter, ok := reader.(serviceAccountSecretCountReader); ok {
		secretCounts, err = counter.CountServicePrincipalSecrets(ctx)
		if err != nil {
			return state, err
		}
	} else {
		// Compatibility for narrow test or extension readers. Production access
		// repositories implement the aggregate count capability above.
		for _, principal := range principals {
			secrets, secretErr := reader.ListServicePrincipalSecrets(ctx, principal.ID)
			if secretErr != nil {
				return state, secretErr
			}
			for _, secret := range secrets {
				if strings.TrimSpace(secret.RevokedAt) == "" {
					secretCounts[principal.ID]++
				}
			}
		}
	}
	sort.SliceStable(principals, func(i, j int) bool {
		left := strings.ToLower(firstAccessValue(principals[i].DisplayName, principals[i].Email, principals[i].ID))
		right := strings.ToLower(firstAccessValue(principals[j].DisplayName, principals[j].Email, principals[j].ID))
		return left < right
	})
	for _, principal := range principals {
		item := ServiceAccountSignalFromPrincipal(principal)
		item.SecretCount = secretCounts[principal.ID]
		state.Items = append(state.Items, item)
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
