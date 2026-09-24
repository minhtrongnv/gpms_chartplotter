package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxAPIJSONBody int64 = 64 << 10 // 64 KiB

var errRequestBodyTooLarge = errors.New(
	"request body too large",
)

func decodeJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	dst any,
	maxBytes int64,
) error {
	if r.Body == nil {
		return errors.New("request body required")
	}

	r.Body = http.MaxBytesReader(
		w,
		r.Body,
		maxBytes,
	)

	decoder := json.NewDecoder(r.Body)

	if err := decoder.Decode(dst); err != nil {
		return normalizeJSONDecodeError(
			err,
			maxBytes,
		)
	}

	// A valid request must contain exactly one JSON value.
	//
	// This second Decode also forces the decoder to read to EOF,
	// which is important because MaxBytesReader can then detect
	// oversized trailing data.
	var extra any

	err := decoder.Decode(&extra)

	switch {
	case errors.Is(err, io.EOF):
		return nil

	case err == nil:
		return errors.New(
			"request body must contain exactly one JSON value",
		)

	default:
		return normalizeJSONDecodeError(
			err,
			maxBytes,
		)
	}
}

func normalizeJSONDecodeError(
	err error,
	maxBytes int64,
) error {
	var maxErr *http.MaxBytesError

	if errors.As(err, &maxErr) {
		return fmt.Errorf(
			"%w: limit is %d bytes",
			errRequestBodyTooLarge,
			maxBytes,
		)
	}

	if errors.Is(err, io.EOF) {
		return errors.New("request body required")
	}

	return fmt.Errorf(
		"bad JSON: %w",
		err,
	)
}

func writeJSONBodyError(
	w http.ResponseWriter,
	err error,
) {
	if errors.Is(
		err,
		errRequestBodyTooLarge,
	) {
		apiErr(
			w,
			http.StatusRequestEntityTooLarge,
			err.Error(),
		)

		return
	}

	apiErr(
		w,
		http.StatusBadRequest,
		err.Error(),
	)
}
