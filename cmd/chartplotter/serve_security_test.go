package main

import "testing"

func TestResolveServeAccessPolicy(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		trustedProxies  string
		trustCloudflare bool
		wantMode        string
		wantTrustCF     bool
		wantErr         bool
	}{
		{
			name:     "default is local",
			mode:     "",
			wantMode: accessModeLocal,
		},
		{
			name:     "explicit local",
			mode:     "local",
			wantMode: accessModeLocal,
		},
		{
			name:            "legacy Cloudflare flags auto-select cloudflare",
			trustedProxies:  "172.20.0.0/24",
			trustCloudflare: true,
			wantMode:        accessModeCloudflare,
			wantTrustCF:     true,
		},
		{
			name:           "trusted proxies alone do not auto-enable Cloudflare",
			trustedProxies: "172.20.0.0/24",
			wantErr:        true,
		},
		{
			name:           "local rejects trusted proxies",
			mode:           "local",
			trustedProxies: "172.20.0.0/24",
			wantErr:        true,
		},
		{
			name:            "local rejects Cloudflare trust flag",
			mode:            "local",
			trustCloudflare: true,
			wantErr:         true,
		},
		{
			name:           "cloudflare requires trusted proxies",
			mode:           "cloudflare",
			trustedProxies: "",
			wantErr:        true,
		},
		{
			name:           "cloudflare implies CF header trust",
			mode:           "cloudflare",
			trustedProxies: "172.20.0.0/24",
			wantMode:       accessModeCloudflare,
			wantTrustCF:    true,
		},
		{
			name:            "legacy trust flag remains accepted in cloudflare mode",
			mode:            "cloudflare",
			trustedProxies:  "172.20.0.0/24",
			trustCloudflare: true,
			wantMode:        accessModeCloudflare,
			wantTrustCF:     true,
		},
		{
			name:    "unknown mode",
			mode:    "public",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveServeAccessPolicy(
				tt.mode,
				tt.trustedProxies,
				tt.trustCloudflare,
			)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.mode != tt.wantMode {
				t.Fatalf(
					"mode = %q, want %q",
					got.mode,
					tt.wantMode,
				)
			}

			if got.trustCloudflare != tt.wantTrustCF {
				t.Fatalf(
					"trustCloudflare = %v, want %v",
					got.trustCloudflare,
					tt.wantTrustCF,
				)
			}
		})
	}
}

func TestValidateLocalBindHost(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{host: "127.0.0.1"},
		{host: "localhost"},
		{host: "0.0.0.0"},
		{host: "::"},
		{host: "192.168.210.10"},
		{host: "10.0.0.10"},
		{host: "172.20.0.5"},
		{host: "169.254.10.20"},
		{host: "fc00::10"},
		{host: "fe80::1"},
		{host: "chartplotter"},
		{host: "8.8.8.8", wantErr: true},
		{host: "2001:4860:4860::8888", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			err := validateLocalBindHost(tt.host)

			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
