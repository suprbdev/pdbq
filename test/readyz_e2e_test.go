package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

func TestReadyzPingsDatabase(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.RLS.Enabled = false
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != `{"status":"ok"}` {
		t.Errorf("readyz = %d %s", res.StatusCode, body)
	}
}
