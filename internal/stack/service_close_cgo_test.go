//go:build cgo

package stack

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

type closeTestConnector struct {
	dsn                              string
	rollbackEntered, rollbackRelease chan struct{}
	closeEntered, closeRelease       chan struct{}
	poolClosed                       chan struct{}
}

func (c *closeTestConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }
func (c *closeTestConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}

	return &closeTestConn{SQLiteConn: conn.(*sqlite3.SQLiteConn), gate: c}, nil
}
func (c *closeTestConnector) Close() error { close(c.poolClosed); return nil }

type closeTestConn struct {
	*sqlite3.SQLiteConn
	gate *closeTestConnector
}

func (c *closeTestConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.SQLiteConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}

	return closeTestTx{Tx: tx, gate: c.gate}, nil
}

func (c *closeTestConn) Close() error {
	close(c.gate.closeEntered)
	<-c.gate.closeRelease
	return c.SQLiteConn.Close()
}

type closeTestTx struct {
	driver.Tx
	gate *closeTestConnector
}

func (tx closeTestTx) Rollback() error {
	close(tx.gate.rollbackEntered)
	<-tx.gate.rollbackRelease
	return tx.Tx.Rollback()
}

func TestServiceCloseWaitsForRollbackAndDriverClose(t *testing.T) {
	dir := privateTestDir(t)
	s, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close(ctx) })
	s.receiptCancel()
	<-s.receiptDone
	// Acquire the single connection after the expiry loop before replacing the
	// pool with an instrumented driver; no production driver globals are changed.
	conn, err := s.Store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err = s.Store.db.Close(); err != nil {
		t.Fatal(err)
	}

	gate := &closeTestConnector{dsn: fileURI(filepath.Join(dir, "state.db")) + "?_journal_mode=WAL&_synchronous=FULL", rollbackEntered: make(chan struct{}), rollbackRelease: make(chan struct{}), closeEntered: make(chan struct{}), closeRelease: make(chan struct{}), poolClosed: make(chan struct{})}
	var releaseRollback, releaseClose sync.Once
	t.Cleanup(func() {
		releaseRollback.Do(func() { close(gate.rollbackRelease) })
		releaseClose.Do(func() { close(gate.closeRelease) })
	})
	s.Store.db = sql.OpenDB(gate)
	s.Store.db.SetMaxOpenConns(1)
	execTest(t, s.Store, "CREATE TABLE shutdown_wal (id INTEGER)")
	execTest(t, s.Store, "INSERT INTO shutdown_wal VALUES (1)")
	txctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tx, err := s.Store.db.BeginTx(txctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	wait := func(ch <-chan struct{}) {
		t.Helper()

		select {
		case <-ch:
		case <-waitCtx.Done():
			t.Fatal("shutdown boundary not reached")
		}
	}
	cancel()
	wait(gate.rollbackEntered)

	if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("expected automatic rollback to own the transaction", err)
	}

	deadline, cancelClose := context.WithCancel(ctx)
	cancelClose()

	closeErr := s.Close(deadline)
	if closeErr == nil {
		t.Error("Close reported success with automatic rollback still blocked")
	} else {
		requireCode(t, closeErr, "storage_unavailable")
	}

	wait(gate.poolClosed)
	assertOwned := func() {
		t.Helper()

		if lock, err := lockState(dir); err == nil {
			lock.close()
			t.Error("state lock released before driver finished")
		} else {
			requireCode(t, err, "conflict")
		}

		if discovery, err := ReadDiscovery(dir); err != nil || discovery.InstanceID != s.Discovery.InstanceID {
			t.Error("discovery removed before driver finished", err)
		}
	}
	assertOwned()
	releaseRollback.Do(func() { close(gate.rollbackRelease) })
	wait(gate.closeEntered)
	assertOwned()
	releaseClose.Do(func() { close(gate.closeRelease) })

	if err = s.Close(waitCtx); err != closeErr {
		t.Error("repeated Close changed its result", err)
	}

	if got := s.Store.db.Stats().OpenConnections; got != 0 {
		t.Errorf("Close returned with %d open connections", got)
	}

	if _, err = os.Stat(filepath.Join(dir, "service.json")); !os.IsNotExist(err) {
		t.Error("completed shutdown retained discovery", err)
	}

	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal("completed shutdown retained state lock", err)
	}
	defer restarted.Close(ctx)

	if restarted.Discovery.DatabaseID != s.Discovery.DatabaseID {
		t.Fatal("restart changed database identity")
	}

	if err = s.Close(ctx); err != closeErr {
		t.Error("old Close changed its result", err)
	}

	if discovery, err := ReadDiscovery(dir); err != nil || discovery.InstanceID != restarted.Discovery.InstanceID {
		t.Fatal("old Close removed new discovery", err)
	}
}
