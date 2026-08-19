// Package module owns process composition for instance-local workload admission.
package module

import (
	"context"
	"errors"
	"sync"

	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/jobs"
)

type Config struct {
	Policy   workload.Config
	Observer workload.Observer
}

type Admitter = workload.Admitter
type Stats = workload.Stats
type Observer = workload.Observer
type Request = workload.Request

const (
	controlMemoryEstimate     = int64(16 << 20)
	maintenanceMemoryEstimate = int64(128 << 20)
)

func JobAdmitter(admitter Admitter) jobs.Admitter {
	if admitter == nil {
		return nil
	}
	return jobs.AdmitterFunc(func(ctx context.Context, request jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
		return admitter.Acquire(ctx, workload.Request{
			Class: workload.Class(request.Class), PrincipalID: request.PrincipalID,
			GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation,
			EstimatedMemoryBytes: request.EstimatedMemoryBytes,
		})
	})
}

const (
	BackgroundClass  = workload.Background
	RefreshClass     = workload.Refresh
	ControlClass     = workload.Control
	MaintenanceClass = workload.Maintenance
)

func DefaultConfig() workload.Config {
	return workload.DefaultConfig()
}

func MaintenanceRequest(operation string) Request {
	return Request{Class: MaintenanceClass, PrincipalID: jobplatform.SystemPrincipalID, Operation: operation, EstimatedMemoryBytes: maintenanceMemoryEstimate}
}

func ControlRequest(operation string) Request {
	return Request{Class: ControlClass, PrincipalID: jobplatform.SystemPrincipalID, Operation: operation, EstimatedMemoryBytes: controlMemoryEstimate}
}

type Module struct {
	controller *workload.Controller
	stop       sync.Once
}

func Build(_ context.Context, config Config) (*Module, error) {
	options := []workload.Option{}
	if config.Observer != nil {
		options = append(options, workload.WithObserver(config.Observer))
	}
	controller, err := workload.New(config.Policy, options...)
	if err != nil {
		return nil, err
	}
	return &Module{controller: controller}, nil
}

func (m *Module) Acquire(ctx context.Context, request workload.Request) (workload.Lease, error) {
	if m == nil || m.controller == nil {
		return nil, errors.New("workload admission is unavailable")
	}
	return m.controller.Acquire(ctx, request)
}

func (m *Module) Stats() workload.Stats {
	if m == nil || m.controller == nil {
		return workload.Stats{}
	}
	return m.controller.Stats()
}

func (m *Module) SetObserver(observer workload.Observer) {
	if m != nil && m.controller != nil {
		m.controller.SetObserver(observer)
	}
}

func (m *Module) Close() {
	if m != nil && m.controller != nil {
		m.stop.Do(m.controller.Close)
	}
}
