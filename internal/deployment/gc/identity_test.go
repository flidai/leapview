package gc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/google/uuid"
)

const testUUIDv7 = "018f0e4e-6f2a-7abc-8def-0123456789ab"

func TestCollectorNewIDRequiresCanonicalUUIDv7(t *testing.T) {
	c := Collector{Config: Config{IDGenerator: func() (string, error) { return testUUIDv7, nil }}}
	got, err := c.newID()
	if err != nil {
		t.Fatal(err)
	}
	if got != testUUIDv7 {
		t.Fatalf("id=%q want %q", got, testUUIDv7)
	}
	parsed, err := uuid.Parse(got)
	if err != nil || parsed.Version() != uuid.Version(7) || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("id=%q is not an RFC 4122 UUIDv7", got)
	}

	for _, invalid := range []string{
		"",
		" " + testUUIDv7,
		"018f0e4e-6f2a-7abc-8def-0123456789AC",
		"018f0e4e-6f2a-6abc-8def-0123456789ab",
	} {
		c.Config.IDGenerator = func() (string, error) { return invalid, nil }
		if _, err := c.newID(); err == nil {
			t.Errorf("newID(%q) succeeded", invalid)
		}
	}
}

func TestCollectorIdentityGeneratorErrorFailsClosed(t *testing.T) {
	sentinel := errors.New("entropy unavailable")
	control := &trackingControl{fakeControl: fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}}
	lease := &trackingDeletionLease{}
	c := Collector{
		Control:   control,
		Store:     &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("orphan"), CreatedAt: time.Now().Add(-time.Hour)}}},
		Inspector: fakeInspector{},
		Config: Config{
			PhysicalPoolID: "pool",
			HolderID:       "holder",
			DeletionLease:  lease,
			LeaseOwnerID:   "owner",
			RequireLease:   true,
			IDGenerator: func() (string, error) {
				return "", sentinel
			},
		},
	}
	if _, err := c.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want generator error", err)
	}
	if len(control.requests) != 0 || control.releaseCount != 0 || len(lease.acquired) != 0 || len(lease.released) != 0 {
		t.Fatalf("identity failure caused side effects: control=%#v releases=%d lease=%#v/%#v", control.requests, control.releaseCount, lease.acquired, lease.released)
	}
}

func TestCollectorRejectsOpaqueLeaseAndCycleIDs(t *testing.T) {
	const uuidv4 = "550e8400-e29b-41d4-a716-446655440000"
	for _, test := range []struct {
		name    string
		leaseID string
		cycleID string
	}{
		{name: "lease UUIDv4", leaseID: uuidv4, cycleID: testUUIDv7},
		{name: "cycle UUIDv4", leaseID: testUUIDv7, cycleID: uuidv4},
		{name: "lease uppercase", leaseID: strings.ToUpper(testUUIDv7), cycleID: testUUIDv7},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &trackingControl{fakeControl: fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}}
			c := Collector{
				Control:   control,
				Store:     &fakeStore{objects: map[string]Object{}},
				Inspector: fakeInspector{},
				Config: Config{
					PhysicalPoolID: "pool",
					HolderID:       "holder",
					LeaseID:        test.leaseID,
					CycleID:        test.cycleID,
					Now:            func() time.Time { return time.Now().UTC() },
				},
			}
			if _, err := c.Run(context.Background()); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err=%v, want invalid config", err)
			}
			if len(control.requests) != 0 || control.releaseCount != 0 {
				t.Fatalf("invalid identity caused fence side effects: requests=%d releases=%d", len(control.requests), control.releaseCount)
			}
		})
	}
}

func TestCollectorIdentityFailureAfterFenceReleasesLeases(t *testing.T) {
	sentinel := errors.New("cycle identity unavailable")
	ids := []string{testUUIDv7, "018f0e4e-6f2a-7abd-8def-0123456789ab", "018f0e4e-6f2a-7abe-8def-0123456789ab"}
	called := 0
	control := &trackingControl{fakeControl: fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}}
	lease := &trackingDeletionLease{}
	c := Collector{
		Control:   control,
		Store:     &fakeStore{objects: map[string]Object{}},
		Inspector: fakeInspector{},
		Config: Config{
			PhysicalPoolID: "pool",
			HolderID:       "holder",
			DeletionLease:  lease,
			LeaseOwnerID:   "owner",
			RequireLease:   true,
			IDGenerator: func() (string, error) {
				if called < len(ids) {
					id := ids[called]
					called++
					return id, nil
				}
				return "", sentinel
			},
		},
	}
	if _, err := c.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want generator error", err)
	}
	if len(control.requests) != 1 || control.releaseCount != 1 {
		t.Fatalf("fence cleanup=%d requests=%d", control.releaseCount, len(control.requests))
	}
	if len(lease.acquired) != 1 || len(lease.released) != 1 {
		t.Fatalf("deletion lease cleanup=%d/%d", len(lease.acquired), len(lease.released))
	}
	if control.requests[0].ID != ids[0] || !strings.HasSuffix(control.requests[0].HolderID, "/"+ids[2]) || lease.acquired[0] != "owner/"+ids[1] {
		t.Fatalf("generated identities lease=%q owner=%q holder=%q", control.requests[0].ID, lease.acquired[0], control.requests[0].HolderID)
	}
}

func TestCollectorDeleteIdentityFailureReleasesFence(t *testing.T) {
	sentinel := errors.New("delete identity unavailable")
	now := time.Now().UTC()
	control := &trackingControl{fakeControl: fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}}
	c := Collector{
		Control:   control,
		Store:     &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("orphan"), CreatedAt: now.Add(-time.Hour)}}},
		Inspector: fakeInspector{},
		Config: Config{
			PhysicalPoolID: "pool",
			HolderID:       "holder",
			LeaseID:        testUUIDv7,
			CycleID:        "018f0e4e-6f2a-7abd-8def-0123456789ab",
			Now:            func() time.Time { return now },
			OrphanGrace:    time.Minute,
			IDGenerator: func() (string, error) {
				return "", sentinel
			},
		},
	}
	result, err := c.Run(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want generator error", err)
	}
	if result.Cycle.ID == "" || len(control.requests) != 1 || control.releaseCount != 1 {
		t.Fatalf("fence cleanup=%d requests=%d result=%#v", control.releaseCount, len(control.requests), result)
	}
	if len(control.intents) != 0 || len(c.Store.(*fakeStore).deleted) != 0 {
		t.Fatalf("delete started after identity failure: intents=%#v deleted=%v", control.intents, c.Store.(*fakeStore).deleted)
	}
}

type trackingControl struct {
	fakeControl
	requests     []deployment.GCFenceRequest
	releaseCount int
}

func (f *trackingControl) AcquireGCFence(ctx context.Context, request deployment.GCFenceRequest) (deployment.GCFence, error) {
	f.requests = append(f.requests, request)
	return f.fakeControl.AcquireGCFence(ctx, request)
}

func (f *trackingControl) ReleaseGCFence(ctx context.Context, fence deployment.GCFence, now time.Time) error {
	f.releaseCount++
	return f.fakeControl.ReleaseGCFence(ctx, fence, now)
}

type trackingDeletionLease struct {
	acquired []string
	released []string
}

func (l *trackingDeletionLease) AcquireNamespaceDeletionLease(_ context.Context, ownerID string, _ time.Duration) (string, error) {
	l.acquired = append(l.acquired, ownerID)
	return "token", nil
}

func (l *trackingDeletionLease) VerifyNamespaceDeletionLease(context.Context, string, string) error {
	return nil
}

func (l *trackingDeletionLease) ReleaseNamespaceDeletionLease(_ context.Context, ownerID, token string) error {
	l.released = append(l.released, ownerID+"/"+token)
	return nil
}
