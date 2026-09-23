package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	const token = "test-secret-token"

	next := http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := WithBearerAuth(next, token)

	tests := []struct {
		name       string
		path       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "valid token",
			path:       "/api/health",
			authHeader: "Bearer " + token,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "missing token",
			path:       "/api/health",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong token",
			path:       "/api/health",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong auth scheme",
			path:       "/api/health",
			authHeader: "Basic " + token,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "healthz is public",
			path:       "/healthz",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "readyz is public",
			path:       "/readyz",
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				tt.path,
				nil,
			)

			if tt.authHeader != "" {
				req.Header.Set(
					"Authorization",
					tt.authHeader,
				)
			}

			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf(
					"expected status %d, got %d",
					tt.wantStatus,
					rec.Code,
				)
			}
		})
	}
}

func TestBearerAuthDisabledWithEmptyToken(
	t *testing.T,
) {
	next := http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := WithBearerAuth(next, "")

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/health",
		nil,
	)

	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNoContent,
			rec.Code,
		)
	}
}

func TestBearerAuthSetsWWWAuthenticate(
	t *testing.T,
) {
	handler := WithBearerAuth(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			w.WriteHeader(http.StatusNoContent)
		}),
		"secret",
	)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/health",
		nil,
	)

	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusUnauthorized,
			rec.Code,
		)
	}

	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal(
			"expected WWW-Authenticate header",
		)
	}
}
