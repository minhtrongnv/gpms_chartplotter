package server

import "testing"

func TestParseOSMPathBounds(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{path: "0/0/0.png", ok: true},
		{path: "8/255/255.png", ok: true},
		{path: "8/256/20.png", ok: false},
		{path: "8/20/256.png", ok: false},
		{path: "8/-1/20.png", ok: false},
		{path: "23/0/0.png", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			_, _, _, ok := parseOSMPath(tt.path)
			if ok != tt.ok {
				t.Fatalf("parseOSMPath(%q) ok=%v, want %v", tt.path, ok, tt.ok)
			}
		})
	}
}
