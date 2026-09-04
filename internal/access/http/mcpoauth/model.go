package mcpoauth

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ory/fosite"
)

const (
	sessionAuthorizeCode = "authorize_code"
	sessionAccessToken   = "access_token"
	sessionRefreshToken  = "refresh_token"
	sessionPKCE          = "pkce"
)

type cachedClient struct {
	client    storedClient
	expiresAt time.Time
}

type storedClient struct {
	ID                      string
	Name                    string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scopes                  []string
	Audience                []string
	Public                  bool
	SecretHash              []byte
	TokenEndpointAuthMethod string
	PrincipalID             string
}

// StoredClient is the durable client record used by the PostgreSQL OAuth
// state adapter.
type StoredClient = storedClient

type persistedRequest struct {
	ID                string                 `json:"id"`
	RequestedAt       time.Time              `json:"requestedAt"`
	ClientID          string                 `json:"clientId"`
	RequestedScope    []string               `json:"requestedScope"`
	GrantedScope      []string               `json:"grantedScope"`
	Form              map[string][]string    `json:"form"`
	Session           *fosite.DefaultSession `json:"session"`
	RequestedAudience []string               `json:"requestedAudience"`
	GrantedAudience   []string               `json:"grantedAudience"`
}

func encodeRequester(request fosite.Requester) (string, error) {
	session, ok := request.GetSession().(*fosite.DefaultSession)
	if !ok {
		return "", fmt.Errorf("unsupported OAuth session type %T", request.GetSession())
	}
	persisted := persistedRequest{
		ID: request.GetID(), RequestedAt: request.GetRequestedAt(), ClientID: request.GetClient().GetID(),
		RequestedScope: append([]string(nil), request.GetRequestedScopes()...),
		GrantedScope:   append([]string(nil), request.GetGrantedScopes()...),
		Form:           map[string][]string{}, Session: session.Clone().(*fosite.DefaultSession),
		RequestedAudience: append([]string(nil), request.GetRequestedAudience()...),
		GrantedAudience:   append([]string(nil), request.GetGrantedAudience()...),
	}
	for key, values := range request.GetRequestForm() {
		persisted.Form[key] = append([]string(nil), values...)
	}
	encoded, err := json.Marshal(persisted)
	return string(encoded), err
}

func (client storedClient) fositeClient() fosite.Client {
	return &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{
			ID: client.ID, Secret: client.SecretHash, RedirectURIs: client.RedirectURIs,
			GrantTypes: client.GrantTypes, ResponseTypes: client.ResponseTypes,
			Scopes: client.Scopes, Audience: client.Audience, Public: client.Public,
		},
		TokenEndpointAuthMethod: client.TokenEndpointAuthMethod,
	}
}
