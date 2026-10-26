package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

func maintenanceTestPanel(t *testing.T, importDir, authDir string) *Panel {
	t.Helper()
	if err := os.MkdirAll(importDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return New(Config{APIKey: "test-key", ImportDir: importDir, AuthDir: authDir, Pool: pool.New("")})
}

func credentialRequest(p *Panel, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestCredsImportUpsertsAndRejectsTraversal(t *testing.T) {
	dir, authDir := t.TempDir(), t.TempDir()
	fixture := `{"account":{"uid":"uid-1","nickname":"sample"},"auth":{"accessToken":"a1","refreshToken":"r1","domain":"www.workbuddy.ai","expiresAt":1821905025,"realm":"global"}}`
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-1.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	p := maintenanceTestPanel(t, dir, authDir)
	body := []byte(`{"names":["workbuddy-1.json","../config.json"]}`)
	rec := credentialRequest(p, http.MethodPost, "/panel/api/creds/import", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		Imported int `json:"imported"`
		Results  []struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || len(result.Results) != 2 || !result.Results[0].OK || result.Results[1].Error == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	got := p.cfg.Pool.AuthByUID("uid-1")
	if got == nil || got.Realm() != "global" || got.AccessToken != "a1" {
		t.Fatalf("credential not loaded: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(authDir, "workbuddy-uid-1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCredsExportWritesNoTokenToResponse(t *testing.T) {
	dir, authDir := t.TempDir(), t.TempDir()
	p := maintenanceTestPanel(t, dir, authDir)
	a := &auth.Auth{AccessToken: "sensitive-access-token", RefreshToken: "sensitive-refresh-token", UID: "uid-2", Nickname: "sample", Domain: "www.codebuddy.cn"}
	p.cfg.Pool.Add(a)
	rec := credentialRequest(p, http.MethodPost, "/panel/api/creds/export", []byte(`{"all":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(a.AccessToken)) || bytes.Contains(rec.Body.Bytes(), []byte(a.RefreshToken)) {
		t.Fatal("response leaked full tokens")
	}
	file := filepath.Join(dir, "workbuddy-uid-2.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := auth.ParseImported(raw)
	if err != nil || parsed.AccessToken != a.AccessToken || parsed.RefreshToken != a.RefreshToken {
		t.Fatalf("export not reusable: %v", err)
	}
}

func TestRestartUnavailableReturnsNotImplemented(t *testing.T) {
	p := New(Config{APIKey: "test-key", RestartScript: filepath.Join(t.TempDir(), "missing.sh")})
	rec := credentialRequest(p, http.MethodPost, "/panel/api/restart", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
