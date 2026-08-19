package workload

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Classes: []Class{"interactive", "batch"},
		Policies: map[Class]Policy{
			"interactive": {ReservedRunning: 1, MaximumRunning: 2, MaximumQueued: 3, MaximumMemoryBytes: 100, QueueTimeout: time.Second},
			"batch":       {MaximumRunning: 2, MaximumQueued: 3, MaximumMemoryBytes: 100},
		},
		MaximumRunning:                 3,
		MaximumQueued:                  5,
		MaximumMemoryBytes:             200,
		MaximumRunningPerPrincipal:     2,
		MaximumQueuedPerPrincipal:      3,
		MaximumMemoryBytesPerPrincipal: 150,
		MaximumRunningPerGroup:         2,
		MaximumQueuedPerGroup:          3,
		MaximumMemoryBytesPerGroup:     150,
	}
}

func TestConfigRequiresExplicitOrderedClassesAndPolicies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		code   ConfigErrorCode
	}{
		{"empty classes", func(c *Config) { c.Classes = nil }, ConfigClassesRequired},
		{"duplicate class", func(c *Config) { c.Classes = []Class{"interactive", "interactive"} }, ConfigDuplicateClass},
		{"padded class", func(c *Config) { c.Classes = []Class{" interactive", "batch"} }, ConfigInvalidClass},
		{"control class", func(c *Config) { c.Classes = []Class{"interactive\n", "batch"} }, ConfigInvalidClass},
		{"missing policy", func(c *Config) { delete(c.Policies, "batch") }, ConfigMissingPolicy},
		{"undeclared policy", func(c *Config) { c.Policies["other"] = Policy{} }, ConfigUndeclaredPolicy},
		{"reservation exceeds class", func(c *Config) { c.Policies["batch"] = Policy{ReservedRunning: 2, MaximumRunning: 1} }, ConfigReservationExceedsClass},
		{"reservation exceeds instance", func(c *Config) {
			c.Policies["interactive"] = Policy{ReservedRunning: 2, MaximumRunning: 2}
			c.Policies["batch"] = Policy{ReservedRunning: 2, MaximumRunning: 2}
		}, ConfigReservationsExceedInstance},
		{"class running exceeds instance", func(c *Config) { c.Policies["batch"] = Policy{MaximumRunning: 4} }, ConfigClassRunningExceedsInstance},
		{"class queue exceeds instance", func(c *Config) { c.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 6} }, ConfigClassQueueExceedsInstance},
		{"class memory exceeds instance", func(c *Config) { c.Policies["batch"] = Policy{MaximumRunning: 1, MaximumMemoryBytes: 201} }, ConfigClassMemoryExceedsInstance},
		{"principal running exceeds instance", func(c *Config) { c.MaximumRunningPerPrincipal = 4 }, ConfigPrincipalRunningExceedsInstance},
		{"principal queue exceeds instance", func(c *Config) { c.MaximumQueuedPerPrincipal = 6 }, ConfigPrincipalQueueExceedsInstance},
		{"group running exceeds instance", func(c *Config) { c.MaximumRunningPerGroup = 4 }, ConfigGroupRunningExceedsInstance},
		{"group queue exceeds instance", func(c *Config) { c.MaximumQueuedPerGroup = 6 }, ConfigGroupQueueExceedsInstance},
		{"principal memory exceeds instance", func(c *Config) { c.MaximumMemoryBytesPerPrincipal = 201 }, ConfigPrincipalMemoryExceedsInstance},
		{"group memory exceeds instance", func(c *Config) { c.MaximumMemoryBytesPerGroup = 201 }, ConfigGroupMemoryExceedsInstance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := validConfig()
			tc.mutate(&config)
			err := config.Validate()
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code != tc.code {
				t.Fatalf("Validate() error = %v, code = %v, want %v", err, configErr, tc.code)
			}
		})
	}
}

func TestConfigRejectsZeroOrNegativeLimitsAndDurations(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"zero maximum running":     func(c *Config) { c.MaximumRunning = 0 },
		"negative maximum running": func(c *Config) { c.MaximumRunning = -1 },
		"negative queue":           func(c *Config) { c.MaximumQueued = -1 },
		"negative memory":          func(c *Config) { c.MaximumMemoryBytes = -1 },
		"negative duration":        func(c *Config) { c.Policies["batch"] = Policy{MaximumRunning: 1, QueueTimeout: -time.Second} },
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestConfigReservationSumCannotOverflow(t *testing.T) {
	config := Config{
		Classes:        []Class{"first", "second"},
		Policies:       map[Class]Policy{"first": {ReservedRunning: math.MaxInt, MaximumRunning: math.MaxInt}, "second": {ReservedRunning: math.MaxInt, MaximumRunning: math.MaxInt}},
		MaximumRunning: math.MaxInt,
	}
	if err := config.Validate(); !IsConfigCode(err, ConfigReservationsExceedInstance) {
		t.Fatalf("reservation overflow error = %v, want reservations exceed instance", err)
	}
}

func TestConfigZeroSemanticsRemainExplicit(t *testing.T) {
	config := Config{
		Classes:        []Class{"disabled", "unlimited"},
		Policies:       map[Class]Policy{"disabled": {}, "unlimited": {MaximumRunning: 2}},
		MaximumRunning: 2,
		// Zero queue and memory limits are valid explicit policy values.
		MaximumQueued:      0,
		MaximumMemoryBytes: 0,
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error for explicit zero semantics = %v", err)
	}
	controller, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := controller.Config(); got.MaximumQueued != 0 || got.MaximumMemoryBytes != 0 || got.Policies["disabled"].MaximumRunning != 0 {
		t.Fatalf("zero policy was changed: %+v", got)
	}
}

func TestNewDefensivelyCopiesConfigurationAndStats(t *testing.T) {
	config := validConfig()
	controller, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	config.Classes[0] = "changed"
	config.Policies["interactive"] = Policy{}
	config.Classes = append(config.Classes, "late")

	snapshot := controller.Stats()
	wantClasses := []Class{"interactive", "batch"}
	if !reflect.DeepEqual(snapshot.ClassOrder, wantClasses) {
		t.Fatalf("Stats().ClassOrder = %v, want %v", snapshot.ClassOrder, wantClasses)
	}
	if snapshot.Classes["interactive"].Policy.MaximumRunning != 2 {
		t.Fatalf("controller policy changed after caller mutation: %+v", snapshot.Classes["interactive"].Policy)
	}
	snapshot.ClassOrder[0] = "mutated"
	snapshot.Classes["interactive"] = ClassStats{}
	if got := controller.Stats().ClassOrder[0]; got != "interactive" {
		t.Fatalf("snapshot mutation changed controller: %q", got)
	}
}

func TestCanonicalizeIdentityAndRequest(t *testing.T) {
	groups := []string{"z", "a", "z"}
	identity, err := (Identity{PrincipalID: "principal", GroupIDs: groups}).Canonicalize()
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	if want := []string{"a", "z"}; !reflect.DeepEqual(identity.GroupIDs, want) {
		t.Fatalf("canonical groups = %v, want %v", identity.GroupIDs, want)
	}
	groups[0] = "changed"
	if identity.GroupIDs[1] != "z" {
		t.Fatal("canonical identity aliases caller groups")
	}
	request, err := (Request{Class: "batch", PrincipalID: "principal", GroupIDs: []string{"g2", "g1", "g2"}, Operation: "query.run", EstimatedMemoryBytes: 1}).Canonicalize()
	if err != nil {
		t.Fatalf("request Canonicalize() error = %v", err)
	}
	if want := []string{"g1", "g2"}; !reflect.DeepEqual(request.GroupIDs, want) {
		t.Fatalf("request groups = %v, want %v", request.GroupIDs, want)
	}
}

func TestRequestValidationIsTyped(t *testing.T) {
	cases := []struct {
		name    string
		request Request
		reason  RejectionReason
	}{
		{"principal", Request{Class: "batch", Operation: "query", EstimatedMemoryBytes: 1}, InvalidPrincipal},
		{"class", Request{Class: " batch", PrincipalID: "p", Operation: "query", EstimatedMemoryBytes: 1}, InvalidClass},
		{"group", Request{Class: "batch", PrincipalID: "p", GroupIDs: []string{" g"}, Operation: "query", EstimatedMemoryBytes: 1}, InvalidGroup},
		{"operation", Request{Class: "batch", PrincipalID: "p", Operation: "query\n", EstimatedMemoryBytes: 1}, InvalidOperation},
		{"memory", Request{Class: "batch", PrincipalID: "p", Operation: "query"}, InvalidMemory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.request.Canonicalize()
			if !IsReason(err, tc.reason) {
				t.Fatalf("error = %v, reason = %v, want %v", err, mustReason(err), tc.reason)
			}
		})
	}
}

func TestIdentifierBoundsAreValidated(t *testing.T) {
	long := strings.Repeat("x", MaxIdentifierLength+1)
	if err := (Config{Classes: []Class{Class(long)}, Policies: map[Class]Policy{Class(long): {MaximumRunning: 1}}, MaximumRunning: 1}).Validate(); !IsConfigCode(err, ConfigInvalidClass) {
		t.Fatalf("long class error = %v, want invalid class", err)
	}
	if _, err := (Identity{PrincipalID: long}).Canonicalize(); !IsReason(err, InvalidPrincipal) {
		t.Fatalf("long principal error = %v, want invalid principal", err)
	}
	if _, err := (Identity{PrincipalID: "p", GroupIDs: []string{long}}).Canonicalize(); !IsReason(err, InvalidGroup) {
		t.Fatalf("long group error = %v, want invalid group", err)
	}
	if _, err := (Request{Class: Class(long), PrincipalID: "p", Operation: "query", EstimatedMemoryBytes: 1}).Canonicalize(); !IsReason(err, InvalidClass) {
		t.Fatalf("long request class error = %v, want invalid class", err)
	}
}

func TestAcquireCopiesRequestBeforeReturningFailure(t *testing.T) {
	controller, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	groups := []string{"z", "a"}
	_, err = controller.Acquire(context.Background(), Request{Class: "batch", PrincipalID: "p", GroupIDs: groups, Operation: "query", EstimatedMemoryBytes: 1})
	groups[0] = "changed"
	var rejection *Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("Acquire() error = %v, want typed rejection", err)
	}
	if !reflect.DeepEqual(rejection.GroupIDs, []string{"a", "z"}) {
		t.Fatalf("rejection groups = %v, want canonical copy", rejection.GroupIDs)
	}
}

func TestCurrentReturnsDefensiveAdmissionMetadata(t *testing.T) {
	request := Request{Class: "batch", PrincipalID: "p", GroupIDs: []string{"g"}, Operation: "query", EstimatedMemoryBytes: 1}
	ctx := context.WithValue(context.Background(), admissionContextKey{}, &activeAdmission{request: request})
	current, ok := Current(ctx)
	if !ok || !reflect.DeepEqual(current, request) {
		t.Fatalf("Current() = (%+v, %v), want (%+v, true)", current, ok, request)
	}
	current.GroupIDs[0] = "changed"
	again, _ := Current(ctx)
	if again.GroupIDs[0] != "g" {
		t.Fatal("Current() exposes mutable admission metadata")
	}
	if _, ok := Current(context.Background()); ok {
		t.Fatal("ordinary context unexpectedly has a current admission")
	}
}

func TestControllerFailsClosedAndCloseIsIdempotent(t *testing.T) {
	controller, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Acquire(context.Background(), Request{Class: "batch", PrincipalID: "p", Operation: "query", EstimatedMemoryBytes: 1}); !IsReason(err, AdmissionUnavailable) {
		t.Fatalf("Acquire() error = %v, want admission unavailable", err)
	}
	controller.Close()
	controller.Close()
	if _, err := controller.Acquire(context.Background(), Request{Class: "batch", PrincipalID: "p", Operation: "query", EstimatedMemoryBytes: 1}); !IsReason(err, ControllerShutdown) {
		t.Fatalf("closed Acquire() error = %v, want controller shutdown", err)
	}
}

func mustReason(err error) RejectionReason { reason, _ := ReasonOf(err); return reason }

func IsConfigCode(err error, code ConfigErrorCode) bool {
	var configErr *ConfigError
	return errors.As(err, &configErr) && configErr.Code == code
}
