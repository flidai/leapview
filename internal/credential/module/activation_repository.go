package module

import credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"

// ActivationRepository is the transaction-bound composition port for native
// delivery. It is never exposed through metadata or credential read APIs.
func (s *Services) ActivationRepository() *credentialpostgres.Repository {
	if s == nil {
		return nil
	}
	return s.repository
}
