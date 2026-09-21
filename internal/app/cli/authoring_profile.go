package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	analyticsenvironment "github.com/flidai/leapview/internal/analytics/environment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	developmentprofile "github.com/flidai/leapview/internal/project/developmentprofile"
	"github.com/spf13/cobra"
)

const stableProfileGraphAttempts = 3

type localDevelopmentProfile struct {
	CheckoutRoot string
	SourceRoot   string
	GraphDigest  string
	Profile      developmentprofile.Selected
	Credentials  map[string]string
}

func reportLocalDevelopmentProfile(command *cobra.Command, profile localDevelopmentProfile) error {
	if command == nil {
		return errors.New("local development command is required")
	}
	var out io.Writer = command.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Development profile %s from %s\n", profile.Profile.ProfileName, profile.Profile.File); err != nil {
		return err
	}
	if len(profile.Profile.Connections) == 0 {
		_, err := fmt.Fprintln(out, "Upstream connections: none (portable fixtures only)")
		return err
	}
	for _, connection := range profile.Profile.Connections {
		host := connection.Endpoint.Host
		if host == "" {
			host = "<connector-default>"
		}
		if connection.Endpoint.Port != 0 {
			host = fmt.Sprintf("%s:%d", host, connection.Endpoint.Port)
		}
		if _, err := fmt.Fprintf(out, "Upstream connection %s (%s): %s\n", connection.Name, connection.ConnectorKind, host); err != nil {
			return err
		}
	}
	return nil
}

// prepareLocalDevelopmentCredentials resolves one profile against one stable,
// whole compiled graph and captures only the credential bundles named by that
// profile. It performs no Docker operation or target mutation.
func prepareLocalDevelopmentProfile(command *cobra.Command, args []string) (localDevelopmentProfile, error) {
	if command == nil {
		return localDevelopmentProfile{}, errors.New("local development command is required")
	}
	invocation, err := os.Getwd()
	if err != nil {
		return localDevelopmentProfile{}, fmt.Errorf("resolve local development directory: %w", err)
	}
	checkout, err := discoverLocalCheckout(invocation)
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	sourceRoot, err := command.Flags().GetString("source-root")
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	if len(args) == 1 {
		sourceRoot = args[0]
	}
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return localDevelopmentProfile{}, errors.New("analytics source root is required")
	}
	if !filepath.IsAbs(sourceRoot) {
		sourceRoot = filepath.Join(invocation, sourceRoot)
	}
	sourceRoot, err = filepath.Abs(sourceRoot)
	if err != nil {
		return localDevelopmentProfile{}, fmt.Errorf("resolve analytics source root: %w", err)
	}

	bundle, err := compileStableProfileGraph(filepath.Clean(sourceRoot))
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	catalog, err := developmentprofile.CatalogFromManifest(bundle.Manifest())
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	profileFile, err := command.Flags().GetString("profile-file")
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	profileName, err := command.Flags().GetString("profile")
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	selected, err := developmentprofile.Load(developmentprofile.LoadOptions{
		CheckoutRoot: checkout, InvocationDirectory: invocation, SourceRoot: sourceRoot,
		ProfileFile: profileFile, ProfileName: profileName, Connections: catalog,
	})
	if err != nil {
		return localDevelopmentProfile{}, err
	}
	credentials := map[string]string{}
	for _, connection := range selected.Connections {
		name := connection.Credentials.EnvironmentVariable
		if name == "" {
			continue
		}
		raw, exists := os.LookupEnv(name)
		if !exists {
			return localDevelopmentProfile{}, errors.New("selected development profile credential is unavailable")
		}
		if err := analyticsenvironment.ValidateCredentialBundle(raw); err != nil {
			return localDevelopmentProfile{}, errors.New("selected development profile credential bundle is invalid")
		}
		credentials[name] = raw
	}
	return localDevelopmentProfile{
		CheckoutRoot: checkout,
		SourceRoot:   filepath.Clean(sourceRoot),
		GraphDigest:  bundle.Graph().Digest(),
		Profile:      selected,
		Credentials:  credentials,
	}, nil
}

func compileStableProfileGraph(sourceRoot string) (projectartifact.SourceBundle, error) {
	var previous projectartifact.SourceBundle
	for attempt := 0; attempt < stableProfileGraphAttempts; attempt++ {
		current, err := projectcompiler.Compile(sourceRoot)
		if err != nil {
			return projectartifact.SourceBundle{}, err
		}
		if attempt > 0 && previous.Digest() == current.Digest() {
			return current, nil
		}
		previous = current
	}
	return projectartifact.SourceBundle{}, errors.New("analytics sources changed during profile preflight")
}

func discoverLocalCheckout(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	start = current
	for {
		marker := filepath.Join(current, ".git")
		if info, statErr := os.Lstat(marker); statErr == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return filepath.Clean(current), nil
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect checkout marker: %w", statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(start), nil
		}
		current = parent
	}
}
