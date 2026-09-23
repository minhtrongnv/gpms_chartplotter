package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/beetlebugorg/chartplotter/internal/engine/server"
)

// serveCmd hosts the web frontend (embedded static assets) plus the server-side
// S-101 baking + tile-serving API: chart imports are parsed and baked into tiles
// in the backend (the browser only renders pre-baked tiles), alongside the
// /api/cell NOAA-download proxy.
type serveCmd struct {
	Host       string `default:"127.0.0.1" help:"Bind host."`
	Port       int    `default:"8080" help:"Bind port."`
	Assets     string `type:"existingdir" help:"Serve static assets from this directory instead of the built-in embedded bundle (for development)."`
	Cache      string `help:"Cache dir for REGENERABLE baked .pmtiles tile sets (default: XDG cache)."`
	Data       string `help:"Data dir for SOURCE ENC (district zips, raw cells) — safe, not auto-deleted (default: XDG data)."`
	ClearCache bool   `name:"clear-cache" help:"On startup, delete the cached baked archives for a clean slate (source ENC is kept)."`
	S101       string `name:"s101" type:"existingdir" help:"Override the embedded catalogue with an external S-101 PortrayalCatalog directory (for iterating on rules). Every chart baked by the server (chart library imports) uses this catalogue's symbology, and the matching client assets are served. Requires --s101-fc."`
	S101FC     string `name:"s101-fc" type:"existingfile" help:"S-101 FeatureCatalogue.xml path (with --s101)."`
}

func (c serveCmd) Run() error {
	// Portrayal is S-101. Emit the client assets (colortables/linestyles/sprite/
	// patterns) via libtile57's asset baker and serve them as a fallback: an explicit
	// --s101 PortrayalCatalog dir overrides libtile57's embedded catalogue.
	catalogDir := c.S101 // "" = libtile57's embedded catalogue
	assetDir, err := os.MkdirTemp("", "cp-s101-assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(assetDir)
	if _, err := emitS101Assets(catalogDir, assetDir); err != nil {
		return fmt.Errorf("emit S-101 assets: %w", err)
	}
	// The emitted assets are a FALLBACK, not a replacement: an explicit --assets dir
	// stays primary (a prebaked widget bundle serves its own index.html /
	// charts-index.json / .pmtiles), this temp dir fills in the generated S-101 files
	// it lacks, and the embedded bundle backs the rest. Registered on the Server below.
	s101AssetDir := assetDir
	if catalogDir != "" {
		appLog.Printf(
			"portrayal: S-101 (catalogue=%s)",
			catalogDir,
		)
	} else {
		appLog.Println(
			"portrayal: S-101 (libtile57 embedded catalogue)",
		)
	}

	cacheDir := c.Cache
	if cacheDir == "" {
		cacheDir = server.DefaultCacheDir()
	}
	dataDir := c.Data
	if dataDir == "" {
		dataDir = server.DefaultDataDir()
	}

	if c.ClearCache {
		n, err := server.ClearCache(cacheDir)
		if err != nil {
			return fmt.Errorf("clear cache: %w", err)
		}
		appLog.Printf("Cleared %d cached file(s) from %s\n", n, cacheDir)
	}

	// Loopback bind → enforce the Host-header DNS-rebind check on /api. Any
	// other bind means the operator opted into network exposure.
	allowRemote := !(c.Host == "127.0.0.1" || c.Host == "localhost" || c.Host == "::1")
	srv := server.New(c.Assets, cacheDir, dataDir, allowRemote, engineCommit)

	defer func() {
		if err := srv.Close(); err != nil {
			appLog.Printf("Server cleanup failed: %v", err)
		}
	}()

	srv.SetAssetFallback(s101AssetDir) // emitted S-101 assets, searched after --assets, before embedded
	srv.Version = version
	srv.ReportStaleCache() // loud warning if any served pack predates this binary

	addr := net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))
	remoteNote := ""
	if allowRemote {
		remoteNote = ", remote OK"
	}
	assetsDesc := "embedded"
	if c.Assets != "" {
		assetsDesc = c.Assets
	}
	appLog.Printf("http://%s/  (assets=%s, cache=%s, data=%s%s)\n", addr, assetsDesc, cacheDir, dataDir, remoteNote)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,

		BaseContext: func(net.Listener) context.Context {
			return appCtx
		},
	}

	signalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	errCh := make(chan error, 1)

	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server: %w", err)
		}

		return nil

	case <-signalCtx.Done():
		srv.SetReady(false)

		appLog.Println("Shutdown signal received")

		appCancel()
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()

		return fmt.Errorf(
			"Graceful HTTP shutdown: %w",
			err,
		)
	}

	if err := <-errCh; err != nil &&
		!errors.Is(err, http.ErrServerClosed) {

		return fmt.Errorf(
			"HTTP server after shutdown: %w",
			err,
		)
	}

	appLog.Println("HTTP server stopped cleanly")

	return nil
}
