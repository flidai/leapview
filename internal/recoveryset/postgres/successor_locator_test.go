package postgres_test

import (
	"strings"
	"testing"

	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/recoveryset/successor"
)

func TestSuccessorLocatorRejectsNonCanonicalResourceFields(t *testing.T) {
	input, _, _ := successorInputs(t, "minimal")
	base := input.Payloads.Manifest.Locator
	if err := base.Validate(); err != nil {
		t.Fatalf("minimal locator is invalid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*recoverypg.ValidatedLocator)
	}{
		{
			name: "account identity non NFC",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.AccountIdentity = "qualific\u0061\u0301tion"
			},
		},
		{
			name: "region surrounding whitespace",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Region = " test-region-1"
			},
		},
		{
			name: "bucket control",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Bucket = "qualification\x00"
			},
		},
		{
			name: "namespace non NFC",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Namespace = "e\u0301vidence"
				locator.Key = "e\u0301vidence/manifest.json"
			},
		},
		{
			name: "key control",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Key = "evidence/manifest\x00.json"
			},
		},
		{
			name: "account identity oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.AccountIdentity = strings.Repeat("a", successor.MaxTextBytes+1)
			},
		},
		{
			name: "region oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Region = strings.Repeat("r", successor.MaxTextBytes+1)
			},
		},
		{
			name: "bucket oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Bucket = strings.Repeat("b", 64)
			},
		},
		{
			name: "namespace oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Namespace = strings.Repeat("n", successor.MaxTextBytes+1)
			},
		},
		{
			name: "derived key oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Key = strings.Repeat("k", 1025)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator := base
			test.mutate(&locator)
			if err := locator.Validate(); err == nil {
				t.Fatal("noncanonical resource locator accepted")
			}
		})
	}
}

func TestSuccessorLocatorRejectsInvalidVersionAndProfileUUID(t *testing.T) {
	input, _, _ := successorInputs(t, "minimal")
	base := input.Payloads.Manifest.Locator
	if err := base.Validate(); err != nil {
		t.Fatalf("minimal locator is invalid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*recoverypg.ValidatedLocator)
	}{
		{
			name: "version surrounding whitespace",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.VersionID = " immutable-manifest "
			},
		},
		{
			name: "version control",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.VersionID = "immutable-\x00manifest"
			},
		},
		{
			name: "version non NFC",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.VersionID = "immutable-e\u0301"
			},
		},
		{
			name: "version oversized",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.VersionID = strings.Repeat("v", successor.MaxTextBytes+1)
			},
		},
		{
			name: "payload version mismatch",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.PayloadVersion++
			},
		},
		{
			name: "profile UUID uppercase",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.StorageProfileID = strings.ToUpper("abcdefab-cdef-4abc-8def-abcdefabcdef")
			},
		},
		{
			name: "profile UUID surrounding whitespace",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.StorageProfileID = " " + locator.StorageProfileID
			},
		},
		{
			name: "profile UUID malformed",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.StorageProfileID = strings.Replace(locator.StorageProfileID, "-", "", 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator := base
			test.mutate(&locator)
			if err := locator.Validate(); err == nil {
				t.Fatal("invalid version/profile locator accepted")
			}
		})
	}
}

func TestSuccessorLocatorRejectsNonCanonicalEndpoint(t *testing.T) {
	input, _, _ := successorInputs(t, "minimal")
	base := input.Payloads.Manifest.Locator
	if err := base.Validate(); err != nil {
		t.Fatalf("minimal locator is invalid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*recoverypg.ValidatedLocator)
	}{
		{
			name: "explicit default port",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = "https://evidence.example.test:443"
			},
		},
		{
			name: "trailing slash",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = "https://evidence.example.test/"
			},
		},
		{
			name: "non NFC host",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = "https://e\u0301vidence.example.test"
			},
		},
		{
			name: "surrounding whitespace",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = " https://evidence.example.test"
			},
		},
		{
			name: "control character",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = "https://evidence.example.\x01test"
			},
		},
		{
			name: "oversized endpoint",
			mutate: func(locator *recoverypg.ValidatedLocator) {
				locator.Endpoint = "https://" + strings.Repeat("a", 2049)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator := base
			test.mutate(&locator)
			if err := locator.Validate(); err == nil {
				t.Fatal("noncanonical endpoint accepted")
			}
		})
	}
}
