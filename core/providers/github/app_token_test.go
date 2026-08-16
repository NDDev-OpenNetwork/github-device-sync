package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppTokenSourceMintsVerifiesAndCachesInstallationToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/app/installations/123/access_tokens" ||
			request.Header.Get("X-GitHub-Api-Version") != APIVersion ||
			request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("request=%s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		jwt := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		assertAppJWT(t, jwt, &privateKey.PublicKey, "456", now)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-GitHub-Request-Id", "request-1")
		_, _ = fmt.Fprintf(
			writer, `{"token":"ghs_fixture_token","expires_at":%q,`+
				`"permissions":{"metadata":"read"},"repository_selection":"all"}`,
			now.Add(time.Hour).Format(time.RFC3339),
		)
	}))
	defer server.Close()
	store := &appTokenSecretStore{value: privatePEM}
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewAppTokenSource(AppTokenConfig{
		AppID: "456", InstallationIDs: map[string]string{"installation:personal": "123"},
		PrivateKeyReference: "inventory-app-key", Secrets: store,
		BaseURL: server.URL + "/", HTTPClient: httpClient, Now: func() time.Time { return now },
		AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Token(context.Background(), "installation:personal")
	if err != nil || first.Value != "ghs_fixture_token" || !first.ExpiresAt.Equal(now.Add(time.Hour)) ||
		first.Permissions["metadata"] != "read" || first.RepositorySelection != "all" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := source.Token(context.Background(), "installation:personal")
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if requests.Load() != 1 || store.calls.Load() != 1 {
		t.Fatalf("requests=%d secrets=%d", requests.Load(), store.calls.Load())
	}
	if _, err := source.Token(context.Background(), "installation:unknown"); err == nil {
		t.Fatal("unknown installation was accepted")
	}
}

func TestAppTokenSourceDoesNotExposeSecretProviderError(t *testing.T) {
	source, err := NewAppTokenSource(AppTokenConfig{
		AppID: "456", InstallationIDs: map[string]string{"installation:personal": "123"},
		PrivateKeyReference: "inventory-app-key",
		Secrets:             &appTokenSecretStore{err: fmt.Errorf("sensitive path and token")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "installation:personal")
	if err == nil || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "token") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestAppTokenSourceDoesNotSerializeDifferentInstallations(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/app/installations/123/access_tokens" {
			close(firstEntered)
			<-firstRelease
		}
		_, _ = fmt.Fprintf(
			writer, `{"token":"ghs_fixture_%s","expires_at":%q,`+
				`"permissions":{"metadata":"read"},"repository_selection":"all"}`,
			request.URL.Path, now.Add(time.Hour).Format(time.RFC3339),
		)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewAppTokenSource(AppTokenConfig{
		AppID: "456", InstallationIDs: map[string]string{
			"installation:first": "123", "installation:second": "124",
		},
		PrivateKeyReference: "inventory-app-key",
		Secrets:             &appTokenSecretStore{value: privatePEM},
		BaseURL:             server.URL + "/", HTTPClient: httpClient, Now: func() time.Time { return now },
		AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() {
		_, tokenErr := source.Token(context.Background(), "installation:first")
		firstResult <- tokenErr
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first installation request did not start")
	}
	secondResult := make(chan error, 1)
	go func() {
		_, tokenErr := source.Token(context.Background(), "installation:second")
		secondResult <- tokenErr
	}()
	select {
	case tokenErr := <-secondResult:
		if tokenErr != nil {
			t.Fatalf("second installation token: %v", tokenErr)
		}
	case <-time.After(time.Second):
		t.Fatal("second installation was serialized behind the first")
	}
	close(firstRelease)
	if tokenErr := <-firstResult; tokenErr != nil {
		t.Fatalf("first installation token: %v", tokenErr)
	}
}

func TestAppTokenSourceReturnsDefensivePermissionCopies(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(
			writer, `{"token":"ghs_fixture","expires_at":%q,`+
				`"permissions":{"metadata":"read"},"repository_selection":"all"}`,
			now.Add(time.Hour).Format(time.RFC3339),
		)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewAppTokenSource(AppTokenConfig{
		AppID: "456", InstallationIDs: map[string]string{"installation:personal": "123"},
		PrivateKeyReference: "inventory-app-key",
		Secrets: &appTokenSecretStore{value: pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
		})},
		BaseURL: server.URL + "/", HTTPClient: httpClient,
		Now: func() time.Time { return now }, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Token(context.Background(), "installation:personal")
	if err != nil {
		t.Fatal(err)
	}
	first.Permissions["metadata"] = "write"
	second, err := source.Token(context.Background(), "installation:personal")
	if err != nil {
		t.Fatal(err)
	}
	if second.Permissions["metadata"] != "read" {
		t.Fatalf("cached permission evidence was mutated: %#v", second.Permissions)
	}
}

type appTokenSecretStore struct {
	value []byte
	err   error
	calls atomic.Int32
}

func (store *appTokenSecretStore) Get(context.Context, string) ([]byte, error) {
	store.calls.Add(1)
	return append([]byte(nil), store.value...), store.err
}

func assertAppJWT(
	t *testing.T,
	value string,
	publicKey *rsa.PublicKey,
	appID string,
	now time.Time,
) {
	t.Helper()
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts=%d", len(parts))
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != appID || claims.IssuedAt != now.Add(-time.Minute).Unix() ||
		claims.Expires != now.Add(9*time.Minute).Unix() {
		t.Fatalf("claims=%+v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
}
