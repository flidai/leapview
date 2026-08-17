package personalsettings

import "github.com/flidai/leapview/internal/access"

type capabilityDescriptor struct {
	label       string
	description string
	category    string
}

// capabilityOptionsSignal presents the canonical project/resource actions as
// one flat list. The browser may group by category, but no nested container
// selector is part of the token contract.
func capabilityOptionsSignal(effective []access.Capability) []CapabilityOptionSignal {
	allowed := make(map[access.Capability]struct{}, len(effective))
	for _, capability := range effective {
		allowed[capability] = struct{}{}
	}
	options := make([]CapabilityOptionSignal, 0, len(effective))
	for _, capability := range access.CanonicalCapabilities() {
		if _, ok := allowed[capability]; !ok {
			continue
		}
		descriptor := describeCapability(capability)
		options = append(options, CapabilityOptionSignal{
			Value: string(capability), Label: descriptor.label,
			Description: descriptor.description, Category: descriptor.category,
		})
	}
	return options
}

var capabilityDescriptors = map[access.Capability]capabilityDescriptor{
	access.CapabilityProjectAdmin:    {"Project administration", "Manage project-level access and settings.", "Administration"},
	access.CapabilityResourceUse:     {"Use resource", "Open and use the project resource.", "Resource"},
	access.CapabilityResourceRead:    {"Read resource", "View the resource and its governed data.", "Resource"},
	access.CapabilityResourceEdit:    {"Edit resource", "Create and update the resource.", "Resource"},
	access.CapabilityResourceManage:  {"Manage resource", "Delete and administer the resource.", "Resource"},
	access.CapabilityResourceShare:   {"Share resource", "Share the resource with other principals.", "Resource"},
	access.CapabilityResourcePublish: {"Publish resource", "Publish the resource to serving.", "Resource"},
}

func describeCapability(capability access.Capability) capabilityDescriptor {
	if descriptor, ok := capabilityDescriptors[capability]; ok {
		return descriptor
	}
	return capabilityDescriptor{label: string(capability), description: "Use this API capability.", category: "Other"}
}
