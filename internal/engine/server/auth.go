package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

type bearerAuth struct {
	tokenHash [sha256.Size]byte
}

func newBearerAuth(token string) *bearerAuth {
	return &bearerAuth{
		tokenHash: sha256.Sum256([]byte(token)),
	}
}

// WithBearerAuth protects the Chartplotter HTTP surface.
//
// /healthz and /readyz deliberately remain public for
// Docker/Kubernetes/load-balancer probes.
func WithBearerAuth(
	next http.Handler,
	token string,
) http.Handler {
	token = strings.TrimSpace(token)

	// Empty token means auth disabled.
	// This is intended for localhost development only.
	if token == "" {
		return next
	}

	auth := newBearerAuth(token)

	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if isPublicProbe(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		presented, ok := bearerToken(
			r.Header.Get("Authorization"),
		)

		if !ok || !auth.valid(presented) {
			w.Header().Set(
				"WWW-Authenticate",
				`Bearer realm="GPMS Chartplotter"`,
			)

			http.Error(
				w,
				"unauthorized",
				http.StatusUnauthorized,
			)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func isPublicProbe(path string) bool {
	return path == "/healthz" ||
		path == "/readyz"
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok {
		return "", false
	}

	if !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	token = strings.TrimSpace(token)

	if token == "" {
		return "", false
	}

	return token, true
}

func (a *bearerAuth) valid(token string) bool {
	hash := sha256.Sum256([]byte(token))

	return subtle.ConstantTimeCompare(
		a.tokenHash[:],
		hash[:],
	) == 1
}
