package workload

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Class is an opaque, host-defined workload class identifier.  The package
// deliberately does not publish a set of product classes.
type Class string

// Policy bounds one configured class. A zero memory limit means the class adds
// no memory limit. A zero running maximum disables execution for the class. A
// zero queue maximum disables queuing. A zero timeout disables that deadline.
type Policy struct {
	ReservedRunning    int
	MaximumRunning     int
	MaximumQueued      int
	MaximumMemoryBytes int64
	QueueTimeout       time.Duration
	ExecutionTimeout   time.Duration
}

// Config is the complete admission policy supplied by a host.  Classes are
// ordered and are never inferred from map iteration.  Policies must contain
// exactly one entry for every class and no entry for an undeclared class.
//
// Config has no default value: an all-zero value is invalid.  Callers may
// mutate a Config after New returns; the controller retains a defensive copy.
type Config struct {
	Classes  []Class
	Policies map[Class]Policy

	MaximumRunning                 int
	MaximumQueued                  int
	MaximumMemoryBytes             int64
	MaximumRunningPerPrincipal     int
	MaximumQueuedPerPrincipal      int
	MaximumMemoryBytesPerPrincipal int64
	MaximumRunningPerGroup         int
	MaximumQueuedPerGroup          int
	MaximumMemoryBytesPerGroup     int64
}

// Validate checks the host-supplied policy without allocating controller
// state.  Validation errors are *ConfigError values and can be inspected with
// errors.As or errors.Is.
func (c Config) Validate() error {
	if c.MaximumRunning <= 0 {
		return configError(ConfigInvalidMaximumRunning, "maximum_running", "instance maximum running must be positive")
	}
	if err := validateNonNegativeConfigLimits(c); err != nil {
		return err
	}
	if c.MaximumRunningPerPrincipal > c.MaximumRunning {
		return configError(ConfigPrincipalRunningExceedsInstance, "maximum_running_per_principal", "principal running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerPrincipal > c.MaximumQueued {
		return configError(ConfigPrincipalQueueExceedsInstance, "maximum_queued_per_principal", "principal queue limit exceeds instance maximum")
	}
	if c.MaximumRunningPerGroup > c.MaximumRunning {
		return configError(ConfigGroupRunningExceedsInstance, "maximum_running_per_group", "group running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerGroup > c.MaximumQueued {
		return configError(ConfigGroupQueueExceedsInstance, "maximum_queued_per_group", "group queue limit exceeds instance maximum")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerPrincipal > c.MaximumMemoryBytes {
		return configError(ConfigPrincipalMemoryExceedsInstance, "maximum_memory_bytes_per_principal", "principal memory limit exceeds instance memory limit")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerGroup > c.MaximumMemoryBytes {
		return configError(ConfigGroupMemoryExceedsInstance, "maximum_memory_bytes_per_group", "group memory limit exceeds instance memory limit")
	}
	if len(c.Classes) == 0 {
		return configError(ConfigClassesRequired, "classes", "at least one class is required")
	}

	seen := make(map[Class]struct{}, len(c.Classes))
	reservations := 0
	for _, class := range c.Classes {
		if err := validateIdentifier(string(class), "class"); err != nil {
			return configError(ConfigInvalidClass, "classes", err.Error())
		}
		if _, ok := seen[class]; ok {
			return configErrorClass(ConfigDuplicateClass, "classes", class, "class is declared more than once")
		}
		seen[class] = struct{}{}
		policy, ok := c.Policies[class]
		if !ok {
			return configErrorClass(ConfigMissingPolicy, "policies", class, "every declared class requires one policy")
		}
		if err := validatePolicy(class, policy, c.MaximumRunning, c.MaximumQueued, c.MaximumMemoryBytes); err != nil {
			return err
		}
		if policy.ReservedRunning > c.MaximumRunning-reservations {
			return configError(ConfigReservationsExceedInstance, "policies", "class reservations exceed instance maximum running")
		}
		reservations += policy.ReservedRunning
	}
	for class := range c.Policies {
		if _, ok := seen[class]; !ok {
			return configErrorClass(ConfigUndeclaredPolicy, "policies", class, "policy is supplied for an undeclared class")
		}
	}
	return nil
}

func validateNonNegativeConfigLimits(c Config) error {
	limits := []struct {
		name string
		v    int64
	}{
		{"maximum_running", int64(c.MaximumRunning)},
		{"maximum_queued", int64(c.MaximumQueued)},
		{"maximum_memory_bytes", c.MaximumMemoryBytes},
		{"maximum_running_per_principal", int64(c.MaximumRunningPerPrincipal)},
		{"maximum_queued_per_principal", int64(c.MaximumQueuedPerPrincipal)},
		{"maximum_memory_bytes_per_principal", c.MaximumMemoryBytesPerPrincipal},
		{"maximum_running_per_group", int64(c.MaximumRunningPerGroup)},
		{"maximum_queued_per_group", int64(c.MaximumQueuedPerGroup)},
		{"maximum_memory_bytes_per_group", c.MaximumMemoryBytesPerGroup},
	}
	for _, limit := range limits {
		if limit.v < 0 {
			return configError(ConfigNegativeLimit, limit.name, "limits must not be negative")
		}
	}
	return nil
}

func validatePolicy(class Class, p Policy, maxRunning, maxQueued int, maxMemory int64) error {
	if p.ReservedRunning < 0 || p.MaximumRunning < 0 || p.MaximumQueued < 0 || p.MaximumMemoryBytes < 0 || p.QueueTimeout < 0 || p.ExecutionTimeout < 0 {
		return configErrorClass(ConfigNegativePolicy, "policies", class, "policy limits and durations must not be negative")
	}
	if p.ReservedRunning > p.MaximumRunning {
		return configErrorClass(ConfigReservationExceedsClass, "reserved_running", class, "class reservation exceeds class maximum running")
	}
	if p.MaximumRunning > maxRunning {
		return configErrorClass(ConfigClassRunningExceedsInstance, "maximum_running", class, "class running limit exceeds instance maximum")
	}
	if p.MaximumQueued > maxQueued {
		return configErrorClass(ConfigClassQueueExceedsInstance, "maximum_queued", class, "class queue limit exceeds instance maximum")
	}
	if maxMemory > 0 && p.MaximumMemoryBytes > 0 && p.MaximumMemoryBytes > maxMemory {
		return configErrorClass(ConfigClassMemoryExceedsInstance, "maximum_memory_bytes", class, "class memory limit exceeds instance memory limit")
	}
	return nil
}

// Clone returns a deep copy of the configuration.
func (c Config) Clone() Config {
	c.Classes = append([]Class(nil), c.Classes...)
	policies := c.Policies
	c.Policies = make(map[Class]Policy, len(policies))
	for class, policy := range policies {
		c.Policies[class] = policy
	}
	return c
}

// Identity identifies the principal and groups associated with a request.
// Identifiers are opaque: Canonicalize only validates, deduplicates, sorts,
// and copies groups; it does not trim or case-fold any value.
type Identity struct {
	PrincipalID string
	GroupIDs    []string
}

// MaxIdentifierLength bounds class, principal, and group identifiers. The
// values remain opaque; the bound only prevents unbounded admission metadata.
const MaxIdentifierLength = 256

// MaxOperationLength bounds operation labels carried by observations.
const MaxOperationLength = 96

// Canonicalize validates and defensively copies an identity.  Group IDs are
// sorted and duplicate IDs are removed.
func (i Identity) Canonicalize() (Identity, error) {
	if err := validateIdentifier(i.PrincipalID, "principal"); err != nil {
		return Identity{}, &Rejection{Reason: InvalidPrincipal, PrincipalID: i.PrincipalID, cause: err}
	}
	groups, err := canonicalGroups(i.GroupIDs)
	if err != nil {
		return Identity{}, &Rejection{Reason: InvalidGroup, PrincipalID: i.PrincipalID, cause: err}
	}
	return Identity{PrincipalID: i.PrincipalID, GroupIDs: groups}, nil
}

// Clone returns a defensive copy of the identity.
func (i Identity) Clone() Identity {
	i.GroupIDs = append([]string(nil), i.GroupIDs...)
	return i
}

// Request is the actor, operation, class, and resource estimate submitted for
// admission.  EstimatedMemoryBytes must be positive.
type Request struct {
	Class                Class
	PrincipalID          string
	GroupIDs             []string
	Operation            string
	EstimatedMemoryBytes int64
}

// Clone returns a defensive copy of the request.
func (r Request) Clone() Request {
	r.GroupIDs = append([]string(nil), r.GroupIDs...)
	return r
}

// Canonicalize validates and copies a request.  Class membership and memory
// bounds that depend on a Config are checked by Controller.Acquire when the
// scheduler implementation is enabled.
func (r Request) Canonicalize() (Request, error) {
	identity, err := (Identity{PrincipalID: r.PrincipalID, GroupIDs: r.GroupIDs}).Canonicalize()
	if err != nil {
		return Request{}, err
	}
	if err := validateIdentifier(string(r.Class), "class"); err != nil {
		return Request{}, &Rejection{Reason: InvalidClass, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, cause: err}
	}
	if err := validateOperation(r.Operation); err != nil {
		return Request{}, &Rejection{Reason: InvalidOperation, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, Operation: r.Operation, cause: err}
	}
	if r.EstimatedMemoryBytes <= 0 {
		return Request{}, &Rejection{Reason: InvalidMemory, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, Operation: r.Operation}
	}
	r.PrincipalID, r.GroupIDs = identity.PrincipalID, identity.GroupIDs
	return r, nil
}

func validateOperation(operation string) error {
	if operation == "" {
		return errors.New("operation must not be empty")
	}
	if operation != strings.TrimSpace(operation) {
		return errors.New("operation must not be whitespace padded")
	}
	if strings.IndexFunc(operation, unicode.IsControl) >= 0 {
		return errors.New("operation must not contain control characters")
	}
	if len(operation) > MaxOperationLength {
		return fmt.Errorf("operation exceeds %d bytes", MaxOperationLength)
	}
	return nil
}

func validateIdentifier(value, kind string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not be whitespace padded", kind)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", kind)
	}
	if len(value) > MaxIdentifierLength {
		return fmt.Errorf("%s exceeds %d bytes", kind, MaxIdentifierLength)
	}
	return nil
}

func canonicalGroups(groups []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if err := validateIdentifier(group, "group"); err != nil {
			return nil, err
		}
		seen[group] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for group := range seen {
		result = append(result, group)
	}
	sort.Strings(result)
	return result, nil
}
