package database

import (
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/metric"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrumentedSQLDBRecordsQueriesAndPoolStats(t *testing.T) {
	db, err := newSQLiteDB(filepath.Join(t.TempDir(), "metrics.db"), DefaultPool())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	queries := metric.DBQueriesTotal.WithLabelValues("select", "unknown")
	before := testutil.ToFloat64(queries)
	var value int
	if err := db.db.QueryRowContext(t.Context(), "SELECT 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", value)
	}
	if got := testutil.ToFloat64(queries); got != before+1 {
		t.Errorf("select query metric = %v, want %v", got, before+1)
	}
	if testutil.ToFloat64(metric.DBConnectionsOpen) < 1 {
		t.Error("database connection pool metric should report an open connection")
	}
	queryErrors := metric.DBErrors.WithLabelValues("select", "other")
	errorBefore := testutil.ToFloat64(queryErrors)
	if err := db.db.QueryRowContext(t.Context(), "SELECT 1 FROM no_such_table").Scan(&value); err == nil {
		t.Fatal("query against a missing table unexpectedly succeeded")
	}
	if got := testutil.ToFloat64(queryErrors); got != errorBefore+1 {
		t.Errorf("database error metric = %v, want %v", got, errorBefore+1)
	}
}
