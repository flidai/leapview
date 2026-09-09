package project

import (
	"encoding/json"
	"fmt"
	"sort"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// ResourceUIDInventory is a sealed, complete inventory of a validated portable
// bundle. It contains no allocated identities. Admission retains it as immutable
// generation evidence; only activation may allocate registry identities.
type ResourceUIDInventory struct {
	graphDigest  string
	graphBytes   []byte
	bundleDigest string
	encoded      []byte
}

type resourceInventoryEntry struct {
	AuthoredID        string            `json:"authored_id"`
	Kind              projectgraph.Kind `json:"kind"`
	ContractStatus    string            `json:"contract_status"`
	ContractProfile   string            `json:"contract_profile,omitempty"`
	ContractVersion   string            `json:"contract_version,omitempty"`
	CanonicalContract string            `json:"canonical_contract,omitempty"`
	ContractDigest    string            `json:"contract_digest,omitempty"`
}

// NewResourceUIDInventory preserves the compiler's complete resource set and
// uses the existing canonical projector when authored contract authority exists.
// Absence of a contract version is not permission to invent a version, profile,
// or digest. Such resources still receive registry identity, but carry no
// published-contract claim. FAI-622 owns publication/version authority.
func NewResourceUIDInventory(source projectartifact.SourceBundle) (ResourceUIDInventory, error) {
	// Reject the zero value and recheck the portable wire authority rather than
	// accepting a graph-only or caller-built list as a complete inventory.
	validated, err := projectartifact.Decode(source.Canonical())
	if err != nil {
		return ResourceUIDInventory{}, fmt.Errorf("resource inventory source bundle: %w", err)
	}
	graph := validated.Graph()
	manifest := validated.Manifest()
	resources := graph.Resources()
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	entries := make([]resourceInventoryEntry, 0, len(resources))
	for _, resource := range resources {
		entry := resourceInventoryEntry{AuthoredID: resource.ID.String(), Kind: resource.Kind, ContractStatus: "unversioned"}
		var projection contractprojection.Projection
		switch resource.Kind {
		case projectgraph.KindSource:
			var authored projectcontracts.Source
			if err := configschema.DecodeResource(configschema.KindSource, "resource.yaml", []byte(manifest.AuthoredResourceSources[entry.AuthoredID]), &authored); err != nil {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: %w", entry.AuthoredID, err)
			}
			if authored.Metadata.ID != entry.AuthoredID || authored.Metadata.Name != resource.Name {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: authored identity differs from graph", entry.AuthoredID)
			}
			if authored.Metadata.Contract != nil {
				projection, err = contractprojection.ProjectSource(authored, contractprojection.Contract{})
				entry.ContractVersion = authored.Metadata.Contract.Version
			}
		case projectgraph.KindModel:
			var authored projectcontracts.Model
			if err := configschema.DecodeResource(configschema.KindModel, "resource.yaml", []byte(manifest.AuthoredResourceSources[entry.AuthoredID]), &authored); err != nil {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: %w", entry.AuthoredID, err)
			}
			if authored.Metadata.ID != entry.AuthoredID || authored.Metadata.Name != resource.Name {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: authored identity differs from graph", entry.AuthoredID)
			}
			if authored.Metadata.Contract != nil {
				references, referenceErr := contractprojection.NewReferenceContext(graph)
				if referenceErr != nil {
					return ResourceUIDInventory{}, referenceErr
				}
				projection, err = contractprojection.ProjectModel(authored, contractprojection.Contract{}, references)
				entry.ContractVersion = authored.Metadata.Contract.Version
			}
		case projectgraph.KindSemanticModel:
			var authored projectcontracts.SemanticModel
			if err := configschema.DecodeResource(configschema.KindSemanticModel, "resource.yaml", []byte(manifest.AuthoredResourceSources[entry.AuthoredID]), &authored); err != nil {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: %w", entry.AuthoredID, err)
			}
			if authored.Metadata.ID != entry.AuthoredID || authored.Metadata.Name != resource.Name {
				return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: authored identity differs from graph", entry.AuthoredID)
			}
			// The current SemanticModel authoring schema has no contract-version
			// field. A publication authority must supply it before projection.
		case projectgraph.KindConnection, projectgraph.KindPipeline, projectgraph.KindDashboard:
			// leapview.contract/v1 deliberately defines no projection for these.
			entry.ContractStatus = "not_contract_bearing"
		default:
			return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: unsupported authored kind %s", entry.AuthoredID, resource.Kind)
		}
		if err != nil {
			return ResourceUIDInventory{}, fmt.Errorf("resource inventory %s: %w", entry.AuthoredID, err)
		}
		if projection != nil {
			canonical, canonicalErr := contractprojection.CanonicalBytes(projection)
			if canonicalErr != nil {
				return ResourceUIDInventory{}, canonicalErr
			}
			digest, digestErr := contractprojection.Digest(projection)
			if digestErr != nil {
				return ResourceUIDInventory{}, digestErr
			}
			entry.ContractProfile = contractprojection.Profile
			entry.ContractStatus = "canonical"
			entry.CanonicalContract = string(canonical)
			entry.ContractDigest = digest
		}
		entries = append(entries, entry)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return ResourceUIDInventory{}, err
	}
	return ResourceUIDInventory{graphDigest: graph.Digest(), graphBytes: graph.CanonicalBytes(), bundleDigest: validated.Digest(), encoded: encoded}, nil
}

func (value ResourceUIDInventory) GraphDigest() string  { return value.graphDigest }
func (value ResourceUIDInventory) BundleDigest() string { return value.bundleDigest }

// GraphCanonicalBytes retains the existing compiler graph proof, not a new
// contract digest. Storage can verify inventory completeness against its exact
// resource set, including an empty set, without enumerating serving assets.
func (value ResourceUIDInventory) GraphCanonicalBytes() []byte {
	return append([]byte(nil), value.graphBytes...)
}

// JSON returns a detached inventory copy. JSON decoding cannot construct a
// sealed inventory, and the zero value cannot become an empty-generation proof.
func (value ResourceUIDInventory) JSON() ([]byte, error) {
	if value.graphDigest == "" || value.bundleDigest == "" || len(value.encoded) == 0 {
		return nil, fmt.Errorf("resource inventory is not sealed")
	}
	return append([]byte(nil), value.encoded...), nil
}
