package cliapi

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// RemoteOptions are the capability-agnostic credentials accepted by remote
// commands. Resolution of saved targets remains an application concern.
type RemoteOptions struct {
	Target    string
	Token     string
	ProjectID string
}

// AddFlags binds remote target flags to a command.
func (options *RemoteOptions) AddFlags(command *cobra.Command) {
	command.Flags().StringVar(&options.Target, "target", "", "LeapView server URL")
	command.Flags().StringVar(&options.Token, "token", "", "API token")
	command.Flags().StringVar(&options.ProjectID, "project-id", "", "target-bound Project identity")
}

// Credentials returns the unresolved credentials supplied by the user.
func (options RemoteOptions) Credentials() Credentials {
	return Credentials{Target: options.Target, Token: options.Token, ProjectID: options.ProjectID}
}

// PaginationOptions are the common cursor pagination flags.
type PaginationOptions struct {
	Limit     int
	PageToken string
}

const (
	MaxPageLimit = 200
)

// AddFlags binds cursor pagination flags to a command.
func (options *PaginationOptions) AddFlags(command *cobra.Command) {
	command.Flags().IntVar(&options.Limit, "limit", 0, "maximum items to return (1-200)")
	command.Flags().StringVar(&options.PageToken, "page-token", "", "opaque page token")
}

// Validate enforces the API pagination contract while preserving omitted
// limits (zero means omitted until the flag is explicitly changed).
func (options PaginationOptions) Validate(command *cobra.Command) error {
	if command != nil && command.Flags().Changed("limit") {
		if options.Limit < 1 {
			return fmt.Errorf("limit must be at least 1")
		}
		if options.Limit > MaxPageLimit {
			return fmt.Errorf("limit must not exceed %d", MaxPageLimit)
		}
	}
	if strings.TrimSpace(options.PageToken) == "" {
		return nil
	}
	return nil
}

// LimitPtr returns the generated-client pointer projection. Zero remains nil
// so omitted flags retain server-side default semantics.
func (options PaginationOptions) LimitPtr() *int32 {
	if options.Limit <= 0 {
		return nil
	}
	value := int32(options.Limit)
	return &value
}

// Query returns the non-empty pagination query parameters.
func (options *PaginationOptions) Query() url.Values {
	query := url.Values{}
	if options.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", options.Limit))
	}
	if options.PageToken != "" {
		query.Set("pageToken", options.PageToken)
	}
	return query
}
