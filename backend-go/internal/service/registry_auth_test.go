package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
)

func TestConfiguredBearerRegistryResolvesTagsAndIndexChildren(t *testing.T) {
	childManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	childDigest := testManifestDigest(childManifest)
	indexBody := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q}]}`, childDigest)
	rootDigest := testManifestDigest(indexBody)

	var tokenRequests atomic.Int32
	var registryRequests atomic.Int32
	authServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "robot" || password != "secret" {
			t.Errorf("allowlisted HTTPS realm did not receive configured credentials")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.URL.Query().Get("scope"); got != "repository:orders/api:pull" {
			t.Errorf("token scope=%q, want repository:orders/api:pull", got)
		}
		if got := r.URL.Query().Get("service"); got != "registry-service" {
			t.Errorf("token service=%q", got)
		}
		_, _ = w.Write([]byte(`{"token":"pull-token"}`))
	}))
	defer authServer.Close()

	registryServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryRequests.Add(1)
		if r.Header.Get("Authorization") != "Bearer pull-token" {
			realm := authServer.URL + "/token"
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope=%q`, realm, "registry-service", "repository:orders/api:pull,push"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v2/orders/api/manifests/v1" && r.URL.Path != "/v2/orders/api/manifests/"+rootDigest {
			t.Errorf("unexpected registry path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", rootDigest)
		_, _ = w.Write([]byte(indexBody))
	}))
	defer registryServer.Close()
	client := trustedRegistryTestClient(t, registryServer, authServer)
	registryHost := strings.TrimPrefix(registryServer.URL, "https://")
	authHost := strings.TrimPrefix(authServer.URL, "https://")
	cfg := config.Config{
		RegistryHost: registryHost, RegistryUser: "robot", RegistryPassword: "secret",
		RegistryAuthHosts: []string{authHost},
	}

	digest, err := resolveRegistryDigestWithClient(context.Background(), cfg, registryHost+"/orders/api:v1", client)
	if err != nil || NormalizeDigest(digest) != rootDigest {
		t.Fatalf("tag digest=%q err=%v", digest, err)
	}
	found, err := registryIndexContainsDigestWithClient(context.Background(), cfg, registryHost+"/orders/api@"+rootDigest, childDigest, client)
	if err != nil || !found {
		t.Fatalf("index child found=%v err=%v", found, err)
	}
	if tokenRequests.Load() != 2 || registryRequests.Load() != 4 {
		t.Fatalf("token requests=%d registry requests=%d", tokenRequests.Load(), registryRequests.Load())
	}
}

func TestBearerRealmMustBeSameHostOrExplicitlyAllowlisted(t *testing.T) {
	var tokenRequests atomic.Int32
	authServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests.Add(1)
		_, _ = w.Write([]byte(`{"token":"unexpected"}`))
	}))
	defer authServer.Close()
	registryServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q`, authServer.URL+"/token", "registry-service"))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer registryServer.Close()
	client := trustedRegistryTestClient(t, registryServer, authServer)
	registryHost := strings.TrimPrefix(registryServer.URL, "https://")
	cfg := config.Config{RegistryHost: registryHost, RegistryUser: "robot", RegistryPassword: "secret"}
	if _, err := resolveRegistryDigestWithClient(context.Background(), cfg, registryHost+"/orders/api:v1", client); err == nil || !strings.Contains(err.Error(), "未加入 REGISTRY_AUTH_HOSTS") {
		t.Fatalf("unlisted auth realm err=%v", err)
	}
	if tokenRequests.Load() != 0 {
		t.Fatalf("unlisted realm received %d requests", tokenRequests.Load())
	}
}

func TestBearerRealmRedirectIsNotFollowed(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	authServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer authServer.Close()
	registryServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q`, authServer.URL+"/token", "registry-service"))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer registryServer.Close()
	client := trustedRegistryTestClient(t, registryServer, authServer, target)
	registryHost := strings.TrimPrefix(registryServer.URL, "https://")
	authHost := strings.TrimPrefix(authServer.URL, "https://")
	cfg := config.Config{RegistryHost: registryHost, RegistryUser: "robot", RegistryPassword: "secret", RegistryAuthHosts: []string{authHost}}
	if _, err := resolveRegistryDigestWithClient(context.Background(), cfg, registryHost+"/orders/api:v1", client); err == nil {
		t.Fatal("redirecting token realm must fail")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect destination received %d requests", targetRequests.Load())
	}
}

func trustedRegistryTestClient(t *testing.T, servers ...*httptest.Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	for _, server := range servers {
		roots.AddCert(server.Certificate())
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := registryHTTPClient(5 * time.Second)
	client.Transport = transport
	t.Cleanup(func() { transport.CloseIdleConnections() })
	return client
}
