package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"crud-api/internal/store"
)

func setup(t *testing.T) (*API, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	return New(database, slog.New(slog.NewTextHandler(io.Discard, nil))), database
}

func call(server http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func TestCRUD(t *testing.T) {
	server, _ := setup(t)
	response := call(server, "POST", "/api/v1/items", `{"name":" First ","description":"sample"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	var created store.Item
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/items/%d", created.ID)
	if created.Name != "First" || response.Header().Get("Location") != path {
		t.Fatalf("unexpected create response: %+v, %v", created, response.Header())
	}
	for _, test := range []struct {
		method, path, body, contains string
		status                       int
	}{
		{"GET", path, "", `"name":"First"`, 200},
		{"GET", "/api/v1/items?limit=1&offset=0", "", `"items":[{`, 200},
		{"GET", "/api/v1/items?offset=1", "", `"items":[]`, 200},
		{"PUT", path, `{"name":"Updated","completed":true}`, `"completed":true`, 200},
		{"GET", path, "", `"name":"Updated"`, 200},
		{"DELETE", path, "", "", 204},
		{"GET", path, "", `"error":"item not found"`, 404},
		{"PUT", path, `{"name":"Missing"}`, `"error":"item not found"`, 404},
		{"DELETE", path, "", `"error":"item not found"`, 404},
		{"GET", "/api/v1/items", "", `"items":[]`, 200},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := call(server, test.method, test.path, test.body)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("got %d %s; expected %d containing %q", response.Code, response.Body, test.status, test.contains)
			}
			if test.status == 204 && response.Body.Len() != 0 {
				t.Fatal("204 must have no body")
			}
		})
	}
}

func TestValidation(t *testing.T) {
	server, _ := setup(t)
	for _, test := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"empty name", "POST", "/api/v1/items", `{"name":" "}`, 400},
		{"long name", "POST", "/api/v1/items", `{"name":"` + strings.Repeat("a", 201) + `"}`, 400},
		{"long description", "POST", "/api/v1/items", `{"name":"ok","description":"` + strings.Repeat("a", 10001) + `"}`, 400},
		{"unknown field", "POST", "/api/v1/items", `{"name":"ok","extra":1}`, 400},
		{"bad bool", "POST", "/api/v1/items", `{"name":"ok","completed":"true"}`, 400},
		{"invalid JSON", "POST", "/api/v1/items", `{`, 400},
		{"empty body", "POST", "/api/v1/items", "", 400},
		{"null body", "POST", "/api/v1/items", `null`, 400},
		{"array body", "POST", "/api/v1/items", `[]`, 400},
		{"multiple objects", "POST", "/api/v1/items", `{"name":"ok"} {}`, 400},
		{"trailing garbage", "POST", "/api/v1/items", `{"name":"ok"} !`, 400},
		{"large body", "POST", "/api/v1/items", `{"name":"` + strings.Repeat("a", 65536) + `"}`, 413},
		{"zero id", "GET", "/api/v1/items/0", "", 400},
		{"negative id", "DELETE", "/api/v1/items/-1", "", 400},
		{"invalid id", "PUT", "/api/v1/items/nope", `{"name":"ok"}`, 400},
		{"overflow id", "GET", "/api/v1/items/99999999999999999999", "", 400},
		{"zero limit", "GET", "/api/v1/items?limit=0", "", 400},
		{"large limit", "GET", "/api/v1/items?limit=101", "", 400},
		{"negative offset", "GET", "/api/v1/items?offset=-1", "", 400},
		{"bad limit", "GET", "/api/v1/items?limit=nope", "", 400},
		{"empty limit", "GET", "/api/v1/items?limit=", "", 400},
		{"repeated limit", "GET", "/api/v1/items?limit=1&limit=2", "", 400},
		{"method", "PATCH", "/api/v1/items/1", `{}`, 405},
		{"unknown route", "GET", "/missing", "", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(server, test.method, test.path, test.body)
			if response.Code != test.status {
				t.Fatalf("got %d %s, want %d", response.Code, response.Body, test.status)
			}
		})
	}
	request := httptest.NewRequest("POST", "/api/v1/items", strings.NewReader(`{"name":"ok"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type: %d", response.Code)
	}
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	server, database := setup(t)
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if response := call(server, "GET", path, ""); response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
	}
	call(server, "GET", "/api/v1/items/12345", "")
	call(server, "GET", "/random-path-12345", "")
	metrics := call(server, "GET", "/metrics", "").Body.String()
	for _, expected := range []string{"http_requests_total", "http_request_duration_seconds", "go_goroutines", `route="GET /api/v1/items/{id}"`, `status="404"`, `route="unmatched"`} {
		if !strings.Contains(metrics, expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	if strings.Contains(metrics, "12345") {
		t.Fatal("metrics must not expose raw resource IDs or unknown paths")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/healthz", 200}, {"/readyz", 503}, {"/api/v1/items", 500},
	} {
		if response := call(server, "GET", test.path, ""); response.Code != test.status {
			t.Errorf("closed database %s: got %d, want %d", test.path, response.Code, test.status)
		}
	}
}
