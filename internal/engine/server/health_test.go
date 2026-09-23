package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(
		http.MethodGet,
		"/healthz",
		nil,
	)

	rec := httptest.NewRecorder()

	s.serveHealthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusOK,
			rec.Code,
		)
	}

	if rec.Body.String() != "ok\n" {
		t.Fatalf(
			"expected body %q, got %q",
			"ok\n",
			rec.Body.String(),
		)
	}
}

func TestReadyzReady(t *testing.T) {
	s := &Server{}
	s.ready.Store(true)

	req := httptest.NewRequest(
		http.MethodGet,
		"/readyz",
		nil,
	)

	rec := httptest.NewRecorder()

	s.serveReadyz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusOK,
			rec.Code,
		)
	}

	if rec.Body.String() != "ready\n" {
		t.Fatalf(
			"expected body %q, got %q",
			"ready\n",
			rec.Body.String(),
		)
	}
}

func TestReadyzNotReady(t *testing.T) {
	s := &Server{}
	s.ready.Store(false)

	req := httptest.NewRequest(
		http.MethodGet,
		"/readyz",
		nil,
	)

	rec := httptest.NewRecorder()

	s.serveReadyz(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusServiceUnavailable,
			rec.Code,
		)
	}

	if rec.Body.String() != "not ready\n" {
		t.Fatalf(
			"expected body %q, got %q",
			"not ready\n",
			rec.Body.String(),
		)
	}
}

func TestHealthzRejectsPost(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(
		http.MethodPost,
		"/healthz",
		nil,
	)

	rec := httptest.NewRecorder()

	s.serveHealthz(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusMethodNotAllowed,
			rec.Code,
		)
	}
}
