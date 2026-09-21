package developmentprofile

import (
	"errors"

	"github.com/flidai/leapview/internal/analytics/connectionadmin"
)

func selectedProfileDigest(selected Selected) (string, error) {
	connections := make([]connectionadmin.DevelopmentProfileDigestConnection, len(selected.Connections))
	for index, connection := range selected.Connections {
		connections[index] = connectionadmin.DevelopmentProfileDigestConnection{
			ConnectionID: connection.ID, ConnectorKind: connection.ConnectorKind, Endpoint: connection.Endpoint,
			CredentialVariable: connection.Credentials.EnvironmentVariable, Unauthenticated: connection.Credentials.None,
		}
	}
	digest, err := connectionadmin.DevelopmentProfileDigest(selected.ProfileName, connections)
	if err != nil {
		return "", errors.New("encode selected development profile identity")
	}
	return digest, nil
}
