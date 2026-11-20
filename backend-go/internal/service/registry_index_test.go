package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRegistryIndexContainsDigestVerifiesManifestGraph(t *testing.T) {
	platformManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	platformDigest := testManifestDigest(platformManifest)
	platformIndex := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q}]}`, platformDigest)
	indexDigest := testManifestDigest(platformIndex)

	t.Run("direct platform manifest descriptor", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path != "/v2/orders/api/manifests/"+indexDigest {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "" {
				t.Errorf("credentials must not be sent to a plaintext registry")
			}
			w.Header().Set("Docker-Content-Digest", indexDigest)
			_, _ = w.Write([]byte(platformIndex))
		}))
		defer server.Close()

		cfg := config.Config{RegistryHost: server.URL, RegistryUser: "robot", RegistryPassword: "secret"}
		found, err := registryIndexContainsDigest(context.Background(), cfg, registryImageRef(server, indexDigest), platformDigest)
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if requests.Load() != 1 {
			t.Fatalf("manifest requests=%d, want 1", requests.Load())
		}
	})

	t.Run("nested index and unrelated digest", func(t *testing.T) {
		nestedIndex := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q}]}`, platformDigest)
		nestedDigest := testManifestDigest(nestedIndex)
		rootIndex := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":%q}]}`, nestedDigest)
		rootDigest := testManifestDigest(rootIndex)
		manifests := map[string]string{rootDigest: rootIndex, nestedDigest: nestedIndex}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			body, ok := manifests[digest]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()

		cfg := config.Config{RegistryHost: server.URL}
		found, err := registryIndexContainsDigest(context.Background(), cfg, registryImageRef(server, rootDigest), platformDigest)
		if err != nil || !found {
			t.Fatalf("nested index: found=%v err=%v", found, err)
		}
		found, err = registryIndexContainsDigest(context.Background(), cfg, registryImageRef(server, rootDigest), observationDigest)
		if err != nil || found {
			t.Fatalf("unrelated digest: found=%v err=%v", found, err)
		}
	})

	t.Run("tampered manifest body is rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Docker-Content-Digest", indexDigest)
			_, _ = w.Write([]byte(platformIndex + " "))
		}))
		defer server.Close()

		found, err := registryIndexContainsDigest(context.Background(), config.Config{RegistryHost: server.URL}, registryImageRef(server, indexDigest), platformDigest)
		if err == nil || found || !strings.Contains(err.Error(), "正文摘要") {
			t.Fatalf("found=%v err=%v, want body-digest rejection", found, err)
		}
	})

	t.Run("registry mismatch is not queried", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
		defer server.Close()

		found, err := registryIndexContainsDigest(context.Background(), config.Config{RegistryHost: "other.example"}, registryImageRef(server, indexDigest), platformDigest)
		if err == nil || found || requests.Load() != 0 {
			t.Fatalf("found=%v err=%v requests=%d", found, err, requests.Load())
		}
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		var targetRequests atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
		defer target.Close()
		registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(http.StatusFound)
		}))
		defer registry.Close()

		found, err := registryIndexContainsDigest(context.Background(), config.Config{RegistryHost: registry.URL, RegistryUser: "robot", RegistryPassword: "secret"}, registryImageRef(registry, indexDigest), platformDigest)
		if err == nil || found || targetRequests.Load() != 0 {
			t.Fatalf("found=%v err=%v redirected requests=%d", found, err, targetRequests.Load())
		}
	})
}

func TestObservePodImageDigestVerifiesConfiguredRegistryIndexChild(t *testing.T) {
	platformManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	platformDigest := testManifestDigest(platformManifest)
	indexBody := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q}]}`, platformDigest)
	indexDigest := testManifestDigest(indexBody)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Docker-Content-Digest", indexDigest)
		_, _ = w.Write([]byte(indexBody))
	}))
	defer server.Close()

	runtime := newPodImageObservationRuntime(platformDigest)
	expectedImage := registryImageRef(server, indexDigest)
	for i := range runtime.replicaSets.Items {
		if runtime.replicaSets.Items[i].GetName() == "rs-new" {
			_ = unstructured.SetNestedSlice(runtime.replicaSets.Items[i].Object, []any{map[string]any{"name": "api", "image": expectedImage}}, "spec", "template", "spec", "containers")
		}
	}
	status, detail := observePodImageDigestWithRegistry(context.Background(), runtime, "prod", "orders", "api", expectedImage, config.Config{RegistryHost: server.URL})
	if status != "verified" || !strings.Contains(detail, "index 描述符链核验") {
		t.Fatalf("observation status=%q detail=%q", status, detail)
	}
	if requests.Load() != 1 {
		t.Fatalf("same platform digest should be resolved once, requests=%d", requests.Load())
	}
}

func testManifestDigest(body string) string {
	hash := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func registryImageRef(server *httptest.Server, digest string) string {
	return strings.TrimPrefix(server.URL, "http://") + "/orders/api@" + digest
}
