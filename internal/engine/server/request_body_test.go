package server

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodyValid(t *testing.T) {
	var body struct {
		Name string `json:"name"`
	}

	req := httptest.NewRequest(
		"POST",
		"/",
		strings.NewReader(`{"name":"GPMS"}`),
	)

	rec := httptest.NewRecorder()

	err := decodeJSONBody(
		rec,
		req,
		&body,
		1024,
	)

	if err != nil {
		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}

	if body.Name != "GPMS" {
		t.Fatalf(
			"expected GPMS, got %q",
			body.Name,
		)
	}
}

func TestDecodeJSONBodyRejectsOversized(
	t *testing.T,
) {
	var body map[string]any

	req := httptest.NewRequest(
		"POST",
		"/",
		strings.NewReader(
			`{"value":"abcdefghijklmnopqrstuvwxyz"}`,
		),
	)

	rec := httptest.NewRecorder()

	err := decodeJSONBody(
		rec,
		req,
		&body,
		16,
	)

	if !errors.Is(
		err,
		errRequestBodyTooLarge,
	) {
		t.Fatalf(
			"expected oversized error, got %v",
			err,
		)
	}
}

func TestDecodeJSONBodyRejectsMultipleValues(
	t *testing.T,
) {
	var body map[string]any

	req := httptest.NewRequest(
		"POST",
		"/",
		strings.NewReader(
			`{"a":1} {"b":2}`,
		),
	)

	rec := httptest.NewRecorder()

	err := decodeJSONBody(
		rec,
		req,
		&body,
		1024,
	)

	if err == nil {
		t.Fatal(
			"expected multiple JSON values to fail",
		)
	}
}

func TestDecodeJSONBodyRejectsTrailingGarbage(
	t *testing.T,
) {
	var body map[string]any

	req := httptest.NewRequest(
		"POST",
		"/",
		strings.NewReader(
			`{"a":1} garbage`,
		),
	)

	rec := httptest.NewRecorder()

	err := decodeJSONBody(
		rec,
		req,
		&body,
		1024,
	)

	if err == nil {
		t.Fatal(
			"expected trailing garbage to fail",
		)
	}
}

func TestDecodeJSONBodyRejectsEmpty(
	t *testing.T,
) {
	var body map[string]any

	req := httptest.NewRequest(
		"POST",
		"/",
		strings.NewReader(""),
	)

	rec := httptest.NewRecorder()

	err := decodeJSONBody(
		rec,
		req,
		&body,
		1024,
	)

	if err == nil {
		t.Fatal(
			"expected empty body to fail",
		)
	}
}
