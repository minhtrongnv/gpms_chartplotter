package server

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IENC (USACE Inland ENC) provider support. The client never talks to ienccloud.us
// directly — the server fetches + parses the products catalogue and serves it as
// JSON at GET /api/ienc/catalog, and the per-cell s57 zips are downloaded by the
// server-side bake (POST /api/import). One round trip, one trusted origin.

// iencCatalogURL is the USACE Inland ENC products catalogue (U37 = the national
// USACE set, grouped by river).
const iencCatalogURL = "https://ienccloud.us/ienc/products/catalog/IENCU37ProductsCatalog.xml"

const maxIENCCatalogBytes int64 = 16 << 20

// iencXMLCell mirrors a <Cell> in the products catalogue XML (root element name is
// ignored, so this parses any IENC*ProductCatalog).
type iencXMLCell struct {
	Name    string  `xml:"name"`
	River   string  `xml:"river_name"`
	From    string  `xml:"location>from"`
	To      string  `xml:"location>to"`
	Edition string  `xml:"edition"`
	S57     string  `xml:"s57_file>location"`
	North   float64 `xml:"area>north"`
	South   float64 `xml:"area>south"`
	East    float64 `xml:"area>east"`
	West    float64 `xml:"area>west"`
}

type iencXMLCatalog struct {
	Cells []iencXMLCell `xml:"Cell"`
}

// iencCell is the JSON the client consumes (one per IENC cell).
type iencCell struct {
	Name    string     `json:"name"`
	River   string     `json:"river"`
	From    string     `json:"from"`
	To      string     `json:"to"`
	Edition string     `json:"edition"`
	URL     string     `json:"url"`  // s57 zip download URL (server fetches it on bake)
	BBox    [4]float64 `json:"bbox"` // w,s,e,n
}

// iencCache memoises the parsed catalogue for a while (it changes rarely).
type iencCache struct {
	mu   sync.Mutex
	json []byte
	at   time.Time
}

var iencCat iencCache

const iencTTL = time.Hour

// serveIENCCatalog fetches + parses the IENC products catalogue server-side and
// returns it to the client as JSON {"cells":[…]}. Cached for iencTTL.
func (s *Server) serveIENCCatalog(w http.ResponseWriter, r *http.Request) {
	iencCat.mu.Lock()
	defer iencCat.mu.Unlock()
	if iencCat.json == nil || time.Since(iencCat.at) > iencTTL {
		body, err := fetchIENCCatalog(r.Context())
		if err != nil {
			apiErr(w, http.StatusBadGateway, "ienc catalogue: "+err.Error())
			return
		}
		var cat iencXMLCatalog
		if err := xml.Unmarshal(body, &cat); err != nil {
			apiErr(w, http.StatusBadGateway, "ienc catalogue parse: "+err.Error())
			return
		}
		cells := make([]iencCell, 0, len(cat.Cells))
		for _, c := range cat.Cells {
			if c.Name == "" || c.S57 == "" {
				continue
			}
			river := strings.TrimSpace(c.River)
			if river == "" {
				river = "Other"
			}
			cells = append(cells, iencCell{
				Name: c.Name, River: river, From: c.From, To: c.To, Edition: c.Edition,
				URL: strings.TrimSpace(c.S57), BBox: [4]float64{c.West, c.South, c.East, c.North},
			})
		}
		out, err := json.Marshal(map[string]any{"cells": cells})
		if err != nil {
			apiErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		iencCat.json = out
		iencCat.at = time.Now()
	}
	w.Header().Set("Content-Type", jsonCT)
	w.Header().Set("Cache-Control", "max-age=3600")
	_, _ = fmt.Fprintf(w, "%s", iencCat.json)
}


// fetchIENCCatalog fetches the small USACE products XML into memory. Bulk ENC
// downloads were moved to disk-backed streaming, which retired the old
// fetchURLProgress helper; the catalogue still needs a bounded in-memory fetch.
//
// Keep the same no-progress watchdog semantics as ENC downloads so a stalled
// upstream cannot pin an /api/ienc/catalog request forever.
func fetchIENCCatalog(ctx context.Context) ([]byte, error) {
	return fetchIENCCatalogURL(
		ctx,
		iencCatalogURL,
		chartHTTPClient,
		chartDownloadNoProgressTimeout,
	)
}

func fetchIENCCatalogURL(
	ctx context.Context,
	raw string,
	client *http.Client,
	noProgressTimeout time.Duration,
) ([]byte, error) {
	if noProgressTimeout <= 0 {
		noProgressTimeout = chartDownloadNoProgressTimeout
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.ContentLength > maxIENCCatalogBytes {
		return nil, fmt.Errorf(
			"ienc catalogue exceeds %d bytes",
			maxIENCCatalogBytes,
		)
	}

	var out bytes.Buffer
	if resp.ContentLength > 0 {
		out.Grow(int(resp.ContentLength))
	}
	buf := make([]byte, 64<<10)

	stallTimer := time.AfterFunc(noProgressTimeout, func() {
		cancel(fmt.Errorf(
			"%w: no bytes received for %s",
			errChartDownloadStalled,
			noProgressTimeout,
		))
	})
	defer stallTimer.Stop()

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stallTimer.Reset(noProgressTimeout)
			if int64(out.Len()+n) > maxIENCCatalogBytes {
				return nil, fmt.Errorf(
					"ienc catalogue exceeds %d bytes",
					maxIENCCatalogBytes,
				)
			}
			_, _ = out.Write(buf[:n])
		}

		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			return nil, rerr
		}
	}

	return out.Bytes(), nil
}
