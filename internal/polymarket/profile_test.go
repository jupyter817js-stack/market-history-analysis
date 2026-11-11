package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", os.ErrNotExist
		}
		wd = parent
	}
}

func newTestClient(t *testing.T, gammaURL, dataURL string) *Client {
	t.Helper()
	return NewClient(ClientOptions{
		GammaBaseURL: gammaURL,
		DataBaseURL:  dataURL,
		HTTPTimeout:  5 * time.Second,
		GammaRPS:     1000,
		DataRPS:      1000,
		RetryMax:     1,
	})
}

func TestGetPublicProfile_ResolvesProxyWallet(t *testing.T) {
	body := loadFixture(t, "public_profile_success.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/public-profile") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("address"); got == "" {
			t.Errorf("address param missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetPublicProfile(context.Background(), "0xinput")
	if err != nil {
		t.Fatalf("GetPublicProfile: %v", err)
	}
	if got := ProxyWalletFromProfile(res.Parsed); got != "0xabc1234567890abcdef1234567890abcdef12345" {
		t.Errorf("unexpected proxyWallet: %s", got)
	}
	if got := DisplayNameFromProfile(res.Parsed); got != "RN1" {
		t.Errorf("unexpected name: %s", got)
	}
	if got := CreatedAtSourceFromProfile(res.Parsed); got != "2024-12-01T05:31:56Z" {
		t.Errorf("unexpected createdAt: %s", got)
	}
}
