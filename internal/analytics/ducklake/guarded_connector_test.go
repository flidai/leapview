package ducklake

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

type closeTrackingConnector struct{ closed bool }

func (c *closeTrackingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("not used")
}

func (c *closeTrackingConnector) Driver() driver.Driver { return closeTrackingDriver{} }
func (c *closeTrackingConnector) Close() error {
	c.closed = true
	return nil
}

type closeTrackingDriver struct{}

func (closeTrackingDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not used") }

func TestGuardedConnectorCloseDelegatesToDuckDBConnector(t *testing.T) {
	inner := &closeTrackingConnector{}
	guarded := &guardedConnector{inner: inner}
	if err := guarded.Close(); err != nil {
		t.Fatal(err)
	}
	if !inner.closed {
		t.Fatal("guarded connector did not close underlying connector")
	}
}
