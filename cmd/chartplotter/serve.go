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
	Host       string `default:"127.0.0.1" help:"Bind host."`
	Port       int    `default:"8080" help:"Bind port."`
	AccessMode string `name:"access-mode" help:"Access mode: local for offline ship/LAN use; cloudflare for Internet access through a trusted Cloudflare Tunnel. Defaults to local; legacy Cloudflare proxy flags auto-select cloudflare."`
	Assets     string `type:"existingdir" help:"Serve static assets from this directory instead of the built-in embedded bundle (for development)."`
	Cache  string `help:"Cache dir for REGENERABLE baked .pmtiles tile sets (default: XDG cache)."`
	Data   string `help:"Data dir for SOURCE ENC (district zips, raw cells) — safe, not auto-deleted (default: XDG data)."`

	ClearCache bool `name:"clear-cache" help:"On startup, delete the cached baked archives for a clean slate (source ENC is kept)."`

	TrustedProxies string `name:"trusted-proxies" help:"Comma-separated trusted reverse-proxy CIDRs. Proxy headers are ignored unless RemoteAddr is inside one of these CIDRs."`

	TrustCloudflare bool `name:"trust-cloudflare" help:"Trust CF-Connecting-IP from configured trusted proxies. Implied by --access-mode=cloudflare; retained for compatibility."`

	S101 string `name:"s101" type:"existingdir" help:"Override the embedded catalogue with an external S-101 PortrayalCatalog directory (for iterating on rules). Every chart baked by the server (chart library imports) uses this catalogue's symbology, and the matching client assets are served. Requires --s101-fc."`

	S101FC string `name:"s101-fc" type:"existingfile" help:"S-101 FeatureCatalogue.xml path (with --s101)."`
}


const (
	accessModeLocal      = "local"
	accessModeCloudflare = "cloudflare"
)

type serveAccessPolicy struct {
	mode            string
	trustCloudflare bool
}

func resolveServeAccessPolicy(
	mode string,
	trustedProxies string,
	trustCloudflareFlag bool,
) (serveAccessPolicy, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	proxyConfigured := strings.TrimSpace(trustedProxies) != ""

	// Preserve the 3.3.3 CLI while making the two deployment modes explicit:
	// no mode + no proxy flags means offline/local; the old trusted-proxy +
	// --trust-cloudflare form is treated as Cloudflare Tunnel mode.
	if mode == "" {
		if trustCloudflareFlag {
			mode = accessModeCloudflare
		} else {
			mode = accessModeLocal
		}
	}

	switch mode {
	case accessModeLocal:
		if proxyConfigured {
			return serveAccessPolicy{}, fmt.Errorf(
				"--trusted-proxies is only valid with --access-mode=cloudflare",
			)
		}
		if trustCloudflareFlag {
			return serveAccessPolicy{}, fmt.Errorf(
				"--trust-cloudflare is only valid with --access-mode=cloudflare",
			)
		}

		return serveAccessPolicy{
			mode: accessModeLocal,
		}, nil

	case accessModeCloudflare:
		if !proxyConfigured {
			return serveAccessPolicy{}, fmt.Errorf(
				"--access-mode=cloudflare requires --trusted-proxies",
			)
		}

		return serveAccessPolicy{
			mode:            accessModeCloudflare,
			trustCloudflare: true,
		}, nil

	default:
		return serveAccessPolicy{}, fmt.Errorf(
			"invalid --access-mode %q (want local or cloudflare)",
			mode,
		)
	}
}

// validateLocalBindHost catches an accidental direct bind to a concrete public
// IP while running in the offline/LAN mode. Wildcard binds remain valid because
// Docker and ship deployments commonly listen on 0.0.0.0/:: inside an isolated
// host/network namespace. Non-IP hostnames are left to the OS resolver.
func validateLocalBindHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}

	if ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() {

		return nil
	}

	return fmt.Errorf(
		"--access-mode=local cannot bind public IP %q; use a private/LAN address or --access-mode=cloudflare",
		host,
	)
}

func (c serveCmd) Run() error {
	accessPolicy, err := resolveServeAccessPolicy(
		c.AccessMode,
		c.TrustedProxies,
		c.TrustCloudflare,
	)
	if err != nil {
		return err
	}

	if accessPolicy.mode == accessModeLocal {
		if err := validateLocalBindHost(c.Host); err != nil {
			return err
		}
	}

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

	clientIPResolver, err := server.NewClientIPResolver(
		c.TrustedProxies,
		accessPolicy.trustCloudflare,
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

	switch accessPolicy.mode {
	case accessModeCloudflare:
		appLog.Println(
			"Access mode: CLOUDFLARE TUNNEL (Internet)",
		)
		appLog.Println(
			"Trusted proxy client-IP resolution enabled (Cloudflare headers allowed)",
		)
	case accessModeLocal:
		appLog.Println(
			"Access mode: LOCAL/OFFLINE (ship LAN; no Cloudflare dependency)",
		)
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
	} else if accessPolicy.mode == accessModeCloudflare {
		appLog.Println(
			"Bearer authentication disabled (Cloudflare Tunnel mode; Cloudflare Access may be enabled upstream)",
		)
	} else {
		appLog.Println(
			"Bearer authentication disabled (local/offline ship mode)",
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
		MaxHeaderBytes:    64 << 10,

		// ReadTimeout and WriteTimeout intentionally stay at zero. ENC/plugin
		// uploads can be large, while SSE and Range responses are long-lived.
		// Individual request bodies are size-capped by their handlers instead.
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
