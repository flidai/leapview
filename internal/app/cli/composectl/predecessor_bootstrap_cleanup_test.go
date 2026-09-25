package composectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPredecessorPreparationPreservesExistingState(t *testing.T) {
	for _, name := range []string{appEnvName, ".host-install.json", "predecessor-provider-tls", "predecessor-pool", "container", "volume"} {
		t.Run(name, func(t *testing.T) {
			executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
				require.Equal(t, "ls", request.Arguments[1], "must not mutate existing resources")
				if request.Arguments[0] == name {
					return []byte("existing"), nil
				}
				return nil, nil
			})
			c := newNativeNetworkController(t, "predecessor", "", executor, &nativeNetworkRuntimeFixture{})
			payload := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(payload, "leapview.env.example"), []byte("seed\n"), 0o600))
			init := filepath.Join(payload, "init.sh")
			require.NoError(t, os.WriteFile(init, []byte("#!/bin/sh\n"), 0o700))
			if name != "container" && name != "volume" {
				require.NoError(t, os.WriteFile(c.path(name), []byte("existing\n"), 0o600))
			}
			_, err := c.PrepareRevision019(t.Context(), payload, init, "ghcr.io/flidai/leapview@sha256:"+strings.Repeat("a", 64))
			require.ErrorContains(t, err, "existing")
			if name != "container" && name != "volume" {
				require.Equal(t, "existing\n", string(mustRead(t, c.path(name))))
			}
		})
	}
}

func TestPredecessorPreparationCleanupRetainsGuardOnFailure(t *testing.T) {
	for _, fail := range []string{"", "container", "volume"} {
		t.Run("failure-"+fail, func(t *testing.T) {
			var removed []string
			executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
				args := request.Arguments
				if args[0] == "compose" {
					return nil, nil
				}
				if args[1] == "ls" {
					return []byte("owned"), nil
				}
				require.Equal(t, "rm", args[1])
				removed = append(removed, args[0])
				if args[0] == fail {
					return nil, errors.New("cleanup unavailable")
				}
				return nil, nil
			})
			c := newNativeNetworkController(t, "predecessor", "owned\n", executor, &nativeNetworkRuntimeFixture{})
			for _, name := range []string{"predecessor-provider-tls", "predecessor-pool"} {
				require.NoError(t, os.Mkdir(c.path(name), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(c.root, name, "owned"), []byte("owned"), 0o600))
			}
			require.NoError(t, os.WriteFile(c.path("unrelated"), []byte("keep"), 0o600))
			preparation := predecessorPreparation{controller: c, project: "predecessor", deployment: []byte("original\n"), networkAttempted: true, providerAttempted: true}
			err := preparation.cleanup()
			require.Equal(t, "keep", string(mustRead(t, c.path("unrelated"))))
			if fail != "" {
				require.ErrorContains(t, err, "cleanup unavailable")
				require.FileExists(t, c.path(appEnvName))
				require.DirExists(t, c.path("predecessor-provider-tls"))
				if fail == "container" {
					require.Equal(t, []string{"container"}, removed)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"container", "volume"}, removed)
			require.NoFileExists(t, c.path(appEnvName))
			require.NoDirExists(t, c.path("predecessor-provider-tls"))
			require.NoDirExists(t, c.path("predecessor-pool"))
			require.Equal(t, "original\n", string(mustRead(t, c.path(deploymentEnvName))))
		})
	}
}
