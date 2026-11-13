package control

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zebrunner/mcloud-dummy/internal/android"
	"github.com/zebrunner/mcloud-dummy/internal/config"
)

func testServer(t *testing.T) (*httptest.Server, *android.Device) {
	t.Helper()
	cfg, err := config.Parse([]byte("devices:\n  - platform: android\n    serial: test-\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := android.New(cfg.Devices[0], slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer((&Server{Host: "127.0.0.1", Android: []*android.Device{d}}).Handler())
	t.Cleanup(srv.Close)
	return srv, d
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestInputRotateIsValidated(t *testing.T) {
	srv, d := testServer(t)
	url := srv.URL + "/api/devices/test-001/"
	for _, body := range []string{`{"rotate":45}`, `{"rotate":-90}`, `{"rotate":360}`} {
		if code, out := post(t, url+"input", body); code != http.StatusBadRequest || !strings.Contains(out, "rotation must be") {
			t.Errorf("input %s: %d %q", body, code, out)
		}
		if code, _ := post(t, url+"state", strings.Replace(body, "rotate", "rotation", 1)); code != http.StatusBadRequest {
			t.Errorf("state %s: %d", body, code)
		}
	}
	if d.App.Rotation() != 0 {
		t.Fatalf("an invalid rotation is not applied: %d", d.App.Rotation())
	}
	if code, out := post(t, url+"input", `{"rotate":90}`); code != http.StatusOK || d.App.Rotation() != 90 {
		t.Fatalf("rotate 90: %d %q, rotation %d", code, out, d.App.Rotation())
	}
}
