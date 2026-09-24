package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchIENCCatalogURL(t *testing.T) {
	want := []byte("<IENCCatalog><Cell><name>TEST</name></Cell></IENCCatalog>")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(want)
	}))
	defer ts.Close()

	got, err := fetchIENCCatalogURL(
		context.Background(),
		ts.URL,
		ts.Client(),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("catalogue=%q, want %q", got, want)
	}
}

func TestFetchIENCCatalogURLCancelsStalledBody(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer ts.Close()
	defer close(release)

	_, err := fetchIENCCatalogURL(
		context.Background(),
		ts.URL,
		ts.Client(),
		50*time.Millisecond,
	)
	if !errors.Is(err, errChartDownloadStalled) {
		t.Fatalf("err=%v, want %v", err, errChartDownloadStalled)
	}
}
