package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

)

// Multi-district import (POST /api/import/packs). The chart-library selects SEVERAL
// districts and downloads them together; this endpoint fetches each district's cells
// into its ENC_ROOT subfolder (<data>/<provider>/ENC_ROOT/<district>/) and then bakes
// each touched PROVIDER as ONE archive from its whole ENC_ROOT. Best-available across
// districts is a per-feature decision inside that single archive (the baker's
// finestCsclAt) — no cross-pack peer context, no repacks. This is the primary download
// path; the single-set POST /api/import stays for uploads + legacy.

// importPacksReq is the JSON body: the set of district packs to download + bake.
type importPacksReq struct {
	Packs []importPackSpec `json:"packs"`
}

// importPackSpec is one selected district: its pack key ("<provider>-<district>", e.g.
// "noaa-d5") plus how to fetch its cells — a bulk district zip (zipUrl, optionally
// narrowed to names) or a list of per-cell NOAA zips.
type importPackSpec struct {
	Set     string   `json:"set"`
	ZipURL  string   `json:"zipUrl"`
	Names   []string `json:"names"`
	Updates *bool    `json:"updates"` // nil → apply .001+ (default)
	Cells   []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"cells"`
}

// handleImportPacks validates a multi-district fetch spec and starts a single
// background job that downloads every district then re-bakes each touched provider.
func (s *Server) handleImportPacks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, "POST /api/import/packs")
		return
	}
	var req importPacksReq
	if err := decodeJSONBody(w, r, &req, 1<<20); err != nil {
		writeJSONBodyError(w, err)
		return
	}
	if len(req.Packs) == 0 {
		apiErr(w, http.StatusBadRequest, "no packs")
		return
	}
	for _, p := range req.Packs {
		if !isSetName(p.Set) || districtOf(p.Set) == "" {
			apiErr(w, http.StatusBadRequest, "each pack needs a valid <provider>-<district> key")
			return
		}
		if p.ZipURL == "" && len(p.Cells) == 0 {
			apiErr(w, http.StatusBadRequest, "pack "+p.Set+" needs zipUrl or cells")
			return
		}
		if p.ZipURL != "" && !isChartURL(p.ZipURL) {
			apiErr(w, http.StatusBadRequest, "zipUrl must be a charts.noaa.gov or ienccloud.us URL")
			return
		}
		for _, c := range p.Cells {
			if c.URL != "" && !isChartURL(c.URL) {
				apiErr(w, http.StatusBadRequest, "cell url must be from a known chart provider")
				return
			}
		}
	}

	label := req.Packs[0].Set
	if len(req.Packs) > 1 {
		label = fmt.Sprintf("%d packs", len(req.Packs))
	}
	job, ok := s.startImportJob(label, func(ctx context.Context, jobID string) {
		s.runImportPacks(ctx, jobID, req)
	})
	if !ok {
		apiErr(w, http.StatusServiceUnavailable, "server shutting down")
		return
	}

	w.Header().Set("Content-Type", jsonCT)
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"ok":true,"job":%q,"packs":%d}`, job.ID, len(req.Packs))
}

// runImportPacks downloads each selected district's cells into its ENC_ROOT subfolder,
// then bakes each touched provider ONCE from its whole ENC_ROOT. A district whose
// download fails is skipped, not fatal.
func (s *Server) runImportPacks(
	ctx context.Context,
	jobID string,
	req importPacksReq,
) {
	fail := func(err error) {
		log.Printf("import %s (packs): %v", jobID, err)
		s.imports.update(jobID, func(j *importJob) {
			if j.State == "running" {
				j.State = "error"
				j.Err = err.Error()
			}
		})
	}

	providers := map[string]bool{}
	for i, pack := range req.Packs {
		if ctx.Err() != nil {
			return
		}
		s.imports.update(jobID, func(j *importJob) {
			j.Pack, j.PackNum, j.PackTotal = pack.Set, i+1, len(req.Packs)
		})
		count, err := s.fetchPackCells(ctx, jobID, pack)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("import %s: pack %s download: %v", jobID, pack.Set, err)
			continue
		}
		if count == 0 {
			log.Printf("import %s: pack %s: no cells", jobID, pack.Set)
			continue
		}
		providers[providerOf(pack.Set)] = true
	}
	if len(providers) == 0 {
		fail(fmt.Errorf("no cells downloaded for any pack"))
		return
	}
	if ctx.Err() != nil {
		return
	}

	s.bakeMu.Lock()
	defer s.bakeMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	s.imports.update(jobID, func(j *importJob) {
		j.Phase, j.Unit, j.Note, j.Done, j.Total =
			"bake", "cells", "Preparing charts", 0, 0
	})

	names := make([]string, 0, len(providers))
	for provider := range providers {
		names = append(names, provider)
	}
	sort.Strings(names)

	baked := 0
	for _, provider := range names {
		if ctx.Err() != nil {
			return
		}
		if s.bakeProvider(jobID, provider) {
			baked++
		}
	}
	if baked == 0 {
		return
	}
	s.imports.update(jobID, func(j *importJob) {
		j.Pack, j.Band, j.Phase, j.State, j.Note = "", "", "done", "done", ""
		j.PackNum, j.PackTotal = 0, 0
	})
	log.Printf(
		"import %s: baked %d provider(s) from %d district(s)",
		jobID,
		baked,
		len(req.Packs),
	)
}

// fetchPackCells downloads one district directly into its ENC_ROOT storage.
// Bulk ZIPs are downloaded and extracted through disk staging; per-cell fetches
// release each cell's bytes after it is cached instead of retaining the batch in RAM.
func (s *Server) fetchPackCells(
	ctx context.Context,
	jobID string,
	pack importPackSpec,
) (int, error) {
	provider, district := providerOf(pack.Set), districtOf(pack.Set)
	applyUpdates := pack.Updates == nil || *pack.Updates

	if pack.ZipURL != "" {
		name := pack.ZipURL[strings.LastIndexByte(pack.ZipURL, '/')+1:]
		s.imports.update(jobID, func(j *importJob) {
			j.Pack, j.Phase, j.Unit, j.Note, j.Done, j.Total =
				pack.Set, "download", "bytes", "Downloading "+name, 0, 0
		})
		zipPath, err := s.downloadURLToTemp(
			ctx,
			pack.ZipURL,
			provider,
			func(done, total int) {
				s.imports.update(jobID, func(j *importJob) {
					j.Done, j.Total = done, total
				})
			},
		)
		if err != nil {
			return 0, fmt.Errorf("download %s: %w", pack.ZipURL, err)
		}
		defer os.Remove(zipPath)

		s.imports.update(jobID, func(j *importJob) {
			j.Phase, j.Unit, j.Note, j.Done, j.Total =
				"extract", "cells", "Extracting "+name, 0, 0
		})
		stage, err := s.stageExchangeSetZip(
			ctx,
			zipPath,
			provider,
			pack.Names,
			applyUpdates,
		)
		if err != nil {
			return 0, err
		}
		defer os.RemoveAll(stage.dir)
		if len(stage.stems) == 0 {
			return 0, nil
		}
		if err := s.commitStagedExchangeSet(
			ctx,
			provider,
			district,
			stage,
		); err != nil {
			return 0, err
		}
		return len(stage.stems), nil
	}

	dir := s.districtDir(provider, district)
	total := len(pack.Cells)
	s.imports.update(jobID, func(j *importJob) {
		j.Pack, j.Phase, j.Unit, j.Total =
			pack.Set, "download", "cells", total
	})
	count := 0
	for i, cell := range pack.Cells {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		if !isCellName(cell.Name) {
			continue
		}
		if cell.URL != "" && !allowedChartURL(cell.URL) {
			continue
		}
		s.imports.update(jobID, func(j *importJob) {
			j.Note = "Downloading " + cell.Name
			j.Done = i
		})
		_, _, err := loadCellCachedContext(
			ctx,
			chartHTTPClient,
			dir,
			cell.Name,
			cell.URL,
		)
		if err != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			log.Printf("import %s: download %s: %v", jobID, cell.Name, err)
		} else {
			count++
		}
		s.imports.update(jobID, func(j *importJob) { j.Done = i + 1 })
	}
	if count > 0 {
		if err := s.markProviderDirty(provider); err != nil {
			return count, err
		}
	}
	return count, nil
}
