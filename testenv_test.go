package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func makeTestDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return openDB(filepath.Join(t.TempDir(), "roost.db"))
}

func newTestSrvFromHandler(t *testing.T, h http.Handler) *testSrv {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &testSrv{url: ts.URL}
}
