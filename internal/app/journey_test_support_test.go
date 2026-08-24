package app

import (
	"context"

	connectionadmin "github.com/flidai/leapview/internal/analytics/connectionadmin"
)

// journeyConnectionAdministrationStub carries the deliberately inert methods
// shared by the credentialed and authorization journey fixtures. Individual
// journeys override only the operations whose behavior they assert.
type journeyConnectionAdministrationStub struct{}

func (journeyConnectionAdministrationStub) List(context.Context, string, connectionadmin.BindingScope, connectionadmin.TargetID) ([]connectionadmin.TargetBinding, error) {
	return nil, nil
}
func (journeyConnectionAdministrationStub) PlanConfigurationChange(context.Context, string, connectionadmin.BindingKey, connectionadmin.TargetBindingConfiguration) (connectionadmin.BindingChangePlan, error) {
	return connectionadmin.BindingChangePlan{}, nil
}
func (journeyConnectionAdministrationStub) UpdateConfiguration(context.Context, connectionadmin.UpdateConfigurationRequest) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
func (journeyConnectionAdministrationStub) Test(context.Context, string, connectionadmin.BindingKey) (connectionadmin.BindingHealthStatus, error) {
	return connectionadmin.BindingHealthStatus{}, nil
}
func (journeyConnectionAdministrationStub) RefreshNow(context.Context, string, connectionadmin.BindingKey) (connectionadmin.BindingHealthStatus, error) {
	return connectionadmin.BindingHealthStatus{}, nil
}
func (journeyConnectionAdministrationStub) Enable(context.Context, string, connectionadmin.BindingKey) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
func (journeyConnectionAdministrationStub) Disable(context.Context, string, connectionadmin.BindingKey) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
