package recoveryset

import (
	"fmt"
	"net/url"
	"strings"
)

func (r ObjectRoot) Validate() error {
	if err := canonicalText(r.Kind, "object root kind", 128); err != nil {
		return err
	}
	if len(r.URI) == 0 || len(r.URI) > 2048 || strings.TrimSpace(r.URI) != r.URI || strings.ContainsAny(r.URI, "\r\n\t") {
		return fmt.Errorf("%w: object root URI must be bounded and canonical", ErrInvalid)
	}
	if strings.Contains(r.URI, "..") {
		for _, part := range strings.FieldsFunc(r.URI, func(r rune) bool { return r == '/' || r == '\\' }) {
			if part == ".." {
				return fmt.Errorf("%w: object root path traversal is not allowed", ErrInvalid)
			}
		}
	}
	if strings.Contains(r.URI, "://") {
		u, err := url.Parse(r.URI)
		if err != nil || u == nil {
			return fmt.Errorf("%w: object root URI must be a supported absolute location without credentials, query, or fragment", ErrInvalid)
		}
		scheme := strings.ToLower(u.Scheme)
		if (scheme != "s3" && scheme != "gs" && scheme != "az" && scheme != "file") || (u.Host == "" && scheme != "file") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: object root URI must be a supported absolute location without credentials, query, or fragment", ErrInvalid)
		}
	} else if strings.HasPrefix(r.URI, "serving-artifacts/") {
		// Native publication retains this exact digest-derived file locator.
		// Local consumers must also bind their explicit trusted storage root;
		// this locator alone does not authorize a filesystem destination.
		if r.Kind != ObjectRootServingArtifact || r.URI != "serving-artifacts/"+strings.TrimPrefix(r.Digest, "sha256:")+".tar.gz" {
			return fmt.Errorf("%w: native serving-artifact locator must match its immutable digest", ErrInvalid)
		}
	} else if !strings.HasPrefix(r.URI, "/") && !strings.HasPrefix(r.URI, "./") && !strings.HasPrefix(r.URI, "objects/") && !strings.HasPrefix(r.URI, "artifacts/") {
		return fmt.Errorf("%w: object root must be an absolute or supported relative path", ErrInvalid)
	}
	if validationRemoteObjectRoot(r.URI) && r.ProviderRecoveryFrontier == "" {
		return fmt.Errorf("%w: remote object roots require provider recovery frontier", ErrInvalid)
	}
	if err := canonicalText(r.VersionID, "object root version", 512); err != nil {
		return err
	}
	if err := digest(r.Digest, "object root digest"); err != nil {
		return err
	}
	if r.ProviderRecoveryFrontier != "" {
		if err := canonicalText(r.ProviderRecoveryFrontier, "object root provider recovery frontier", 512); err != nil {
			return err
		}
	}
	return nil
}
