package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/beetlebugorg/chartplotter/internal/engine/server"
)

// serveCmd hosts the web frontend (embedded static assets) plus the server-side
// S-101 baking + tile-serving API: chart imports are parsed and baked into tiles
// in the backend (the browser only renders pre-baked tiles), alongside the
// /api/cell NOAA-download proxy.
type serveCmd struct {
	Host   string `default:"127.0.0.1" help:"Bind host."`
	Port   int    `default:"8080" help:"Bind port."`
	Assets string `type:"existingdir" help:"Serve static assets from this directory instead of the built-in embedded bundle (for development)."`
	Cache  string `help:"Cache dir for REGENERABLE baked .pmtiles tile sets (default: XDG cache)."`
	Data   string `help:"Data dir for SOURCE ENC (district zips, raw cells) — safe, not auto-deleted (default: XDG data)."`

	ClearCache bool `name:"clear-cache" help:"On startup, delete the cached baked archives for a clean slate (source ENC is kept)."`

	TrustedProxies string `name:"trusted-proxies" help:"Comma-separated trusted reverse-proxy CIDRs. Proxy headers are ignored unless RemoteAddr is inside one of these CIDRs."`

	TrustCloudflare bool `name:"trust-cloudflare" help:"Trust CF-Connecting-IP from configured trusted proxies."`

	S101 string `name:"s101" type:"existingdir" help:"Override the embedded catalogue with an external S-101 PortrayalCatalog directory (for iterating on rules). Every chart baked by the server (chart library imports) uses this catalogue's symbology, and the matching client assets are served. Requires --s101-fc."`

	S101FC string `name:"s101-fc" type:"existingfile" help:"S-101 FeatureCatalogue.xml path (with --s101)."`
}

func (c serveCmd) Run() error {
	// Portrayal is S-101. Emit the client assets
	// (colortables/linestyles/sprite/patterns) via libtile57's
	// asset baker and serve them as a fallback.
	//
	// An explicit --s101 PortrayalCatalog dir overrides
	// libtile57's embedded catalogue.
	catalogDir := c.S101

	assetDir, err := os.MkdirTemp(
		"",
		"cp-s101-assets-",
	)
	if err != nil {
		return err
	}

	defer os.RemoveAll(assetDir)

	if _, err := emitS101Assets(
		catalogDir,
		assetDir,
	); err != nil {
		return fmt.Errorf(
			"emit S-101 assets: %w",
			err,
		)
	}

	// The emitted assets are a FALLBACK, not a replacement:
	//
	// 1. explicit --assets dir
	// 2. generated S-101 assets
	// 3. embedded bundle
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
			return fmt.Errorf(
				"clear cache: %w",
				err,
			)
		}

		appLog.Printf(
			"Cleared %d cached file(s) from %s\n",
			n,
			cacheDir,
		)
	}

	// Loopback bind → enforce the Host-header DNS-rebind
	// check on /api.
	//
	// Any other bind means the operator explicitly opted
	// into network exposure.
	allowRemote := !(c.Host == "127.0.0.1" ||
		c.Host == "localhost" ||
		c.Host == "::1")

	accessToken := strings.TrimSpace(
		os.Getenv("CHARTPLOTTER_ACCESS_TOKEN"),
	)

	if allowRemote && accessToken == "" {
		return fmt.Errorf(
			"CHARTPLOTTER_ACCESS_TOKEN is required when binding to a non-loopback host",
		)
	}

	// CF-Connecting-IP is only meaningful when the immediate
	// peer itself is an explicitly trusted proxy.
	//
	// Requiring --trusted-proxies together with
	// --trust-cloudflare prevents accidentally trusting a
	// caller-supplied CF-Connecting-IP header.
	if c.TrustCloudflare &&
		strings.TrimSpace(c.TrustedProxies) == "" {

		return fmt.Errorf(
			"--trust-cloudflare requires --trusted-proxies",
		)
	}

	clientIPResolver, err := server.NewClientIPResolver(
		c.TrustedProxies,
		c.TrustCloudflare,
	)
	if err != nil {
		return fmt.Errorf(
			"configure trusted proxies: %w",
			err,
		)
	}

	srv := server.New(
		c.Assets,
		cacheDir,
		dataDir,
		allowRemote,
		engineCommit,
	)

	// Install the trusted-proxy-aware client resolver before
	// the HTTP server begins accepting requests.
	//
	// The rate limiter uses this resolver to ensure clients
	// behind an approved reverse proxy receive independent
	// rate-limit buckets.
	srv.SetClientIPResolver(
		clientIPResolver,
	)

	if strings.TrimSpace(c.TrustedProxies) != "" {
		if c.TrustCloudflare {
			appLog.Println(
				"Trusted proxy client-IP resolution enabled (Cloudflare headers allowed)",
			)
		} else {
			appLog.Println(
				"Trusted proxy client-IP resolution enabled",
			)
		}
	}

	var handler http.Handler = srv

	if accessToken != "" {
		handler = server.WithBearerAuth(
			handler,
			accessToken,
		)

		appLog.Println(
			"Bearer authentication enabled",
		)
	} else {
		appLog.Println(
			"Bearer authentication disabled (loopback development mode)",
		)
	}

	defer func() {
		if err := srv.Close(); err != nil {
			appLog.Printf(
				"Server cleanup failed: %v",
				err,
			)
		}
	}()

	// Emitted S-101 assets are searched after --assets and
	// before the embedded asset bundle.
	srv.SetAssetFallback(
		s101AssetDir,
	)

	srv.Version = version

	// Loud warning if any served pack predates this binary.
	srv.ReportStaleCache()

	addr := net.JoinHostPort(
		c.Host,
		fmt.Sprintf("%d", c.Port),
	)

	remoteNote := ""

	if allowRemote {
		remoteNote = ", remote OK"
	}

	assetsDesc := "embedded"

	if c.Assets != "" {
		assetsDesc = c.Assets
	}

	appLog.Printf(
		"http://%s/  (assets=%s, cache=%s, data=%s%s)\n",
		addr,
		assetsDesc,
		cacheDir,
		dataDir,
		remoteNote,
	)

	appCtx, appCancel := context.WithCancel(
		context.Background(),
	)

	defer appCancel()

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,

		BaseContext: func(
			net.Listener,
		) context.Context {
			return appCtx
		},
	}

	signalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()

	errCh := make(
		chan error,
		1,
	)

	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil &&
			!errors.Is(
				err,
				http.ErrServerClosed,
			) {

			return fmt.Errorf(
				"HTTP server: %w",
				err,
			)
		}

		return nil

	case <-signalCtx.Done():
		srv.SetReady(false)

		appLog.Println(
			"Shutdown signal received",
		)

		appCancel()
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)

	defer cancel()

	if err := httpServer.Shutdown(
		shutdownCtx,
	); err != nil {

		_ = httpServer.Close()

		return fmt.Errorf(
			"Graceful HTTP shutdown: %w",
			err,
		)
	}

	if err := <-errCh; err != nil &&
		!errors.Is(
			err,
			http.ErrServerClosed,
		) {

		return fmt.Errorf(
			"HTTP server after shutdown: %w",
			err,
		)
	}

	appLog.Println(
		"HTTP server stopped cleanly",
	)

	return nil
}
