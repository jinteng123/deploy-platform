package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetObjectMetadataRequestsOnlyPartialMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/namespaces/orders/secrets/registry-auth"; got != want {
			t.Errorf("request path=%q, want %q", got, want)
		}
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "as=PartialObjectMetadata") || strings.Contains(accept, ",application/json") {
			t.Errorf("Accept permits a full-object fallback: %q", accept)
		}
		w.Header().Set("Content-Type", "application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1")
		_, _ = w.Write([]byte(`{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"namespace":"orders","name":"registry-auth","uid":"secret-uid-1","resourceVersion":"27"}}`))
	}))
	defer server.Close()

	client, err := NewFromToken(server.URL, "test-token", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.metadataHTTP.CloseIdleConnections()
	def, err := LookupResource("", "v1", "secrets")
	if err != nil {
		t.Fatal(err)
	}
	object, err := client.GetObjectMetadata(context.Background(), def, "orders", "registry-auth")
	if err != nil {
		t.Fatalf("GetObjectMetadata: %v", err)
	}
	if string(object.UID) != "secret-uid-1" || object.ResourceVersion != "27" {
		t.Fatalf("metadata=%+v", object.ObjectMeta)
	}
}

func TestGetObjectMetadataRejectsFullObjectFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Secret","data":{"password":"must-not-be-decoded"}}`))
	}))
	defer server.Close()
	client, err := NewFromToken(server.URL, "test-token", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.metadataHTTP.CloseIdleConnections()
	def, err := LookupResource("", "v1", "secrets")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetObjectMetadata(context.Background(), def, "orders", "registry-auth"); err == nil {
		t.Fatal("full-object fallback was accepted as PartialObjectMetadata")
	}
}
