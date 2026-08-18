package ducklake

import (
	"context"
	"database/sql/driver"
	"io"
)

// guardedConnector applies the shared-pool SQL capability guard at the
// database/sql driver boundary. This covers Environment.Exec, resource
// sessions, and transaction callbacks alike: all statement execution flows
// through the wrapped driver connection before DuckDB sees the SQL.
type guardedConnector struct {
	inner driver.Connector
	guard func(string) error
}

func (c *guardedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &guardedConn{inner: conn, guard: c.guard}, nil
}

func (c *guardedConnector) Driver() driver.Driver { return c.inner.Driver() }

func (c *guardedConnector) Close() error {
	if closer, ok := c.inner.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type guardedConn struct {
	inner driver.Conn
	guard func(string) error
}

// arrowDriverConn returns the native DuckDB connection only after applying
// the same statement guard used by database/sql execution. Native Arrow
// requires duckdb-go's concrete driver connection and therefore cannot use
// the guarded wrapper directly; callers must pass the exact SQL they will
// execute so unwrapping cannot bypass shared-pool admission rules.
func (c *guardedConn) arrowDriverConn(statement string) (driver.Conn, error) {
	if err := c.check(statement); err != nil {
		return nil, err
	}
	return c.inner, nil
}

func (c *guardedConn) Prepare(query string) (driver.Stmt, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &guardedStmt{inner: stmt}, nil
}

func (c *guardedConn) Close() error              { return c.inner.Close() }
func (c *guardedConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *guardedConn) check(query string) error {
	if c.guard == nil {
		return nil
	}
	return c.guard(query)
}

func (c *guardedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	if preparer, ok := c.inner.(driver.ConnPrepareContext); ok {
		stmt, err := preparer.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &guardedStmt{inner: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *guardedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginTx, ok := c.inner.(driver.ConnBeginTx); ok {
		return beginTx.BeginTx(ctx, opts)
	}
	return nil, driver.ErrSkip
}

func (c *guardedConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	if execer, ok := c.inner.(driver.Execer); ok {
		return execer.Exec(query, args)
	}
	return nil, driver.ErrSkip
}

func (c *guardedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *guardedConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	if queryer, ok := c.inner.(driver.Queryer); ok {
		return queryer.Query(query, args)
	}
	return nil, driver.ErrSkip
}

func (c *guardedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.check(query); err != nil {
		return nil, err
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *guardedConn) Ping(ctx context.Context) error {
	if pinger, ok := c.inner.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *guardedConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.inner.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *guardedConn) IsValid() bool {
	if validator, ok := c.inner.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *guardedConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.inner.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

type guardedStmt struct{ inner driver.Stmt }

func (s *guardedStmt) Close() error                                    { return s.inner.Close() }
func (s *guardedStmt) NumInput() int                                   { return s.inner.NumInput() }
func (s *guardedStmt) Exec(args []driver.Value) (driver.Result, error) { return s.inner.Exec(args) }
func (s *guardedStmt) Query(args []driver.Value) (driver.Rows, error)  { return s.inner.Query(args) }

func (s *guardedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := s.inner.(driver.StmtExecContext); ok {
		return execer.ExecContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

func (s *guardedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := s.inner.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, args)
	}
	return nil, driver.ErrSkip
}
