package server

import (
	"net/url"
	"testing"

	tile57 "github.com/beetlebugorg/tile57/bindings/go"
)

func TestMarinerFromQuerySoundingsOverride(t *testing.T) {
	on := marinerFromQuery(url.Values{
		"displayOther":  {"0"},
		"showSoundings": {"1"},
	})
	if on.DisplayOther {
		t.Fatal("displayOther should stay false")
	}
	if on.Soundings != tile57.SoundingsShow {
		t.Fatalf("soundings=%v, want SoundingsShow", on.Soundings)
	}

	off := marinerFromQuery(url.Values{
		"displayOther":  {"1"},
		"showSoundings": {"0"},
	})
	if !off.DisplayOther {
		t.Fatal("displayOther should stay true")
	}
	if off.Soundings != tile57.SoundingsHide {
		t.Fatalf("soundings=%v, want SoundingsHide", off.Soundings)
	}

	follow := marinerFromQuery(url.Values{})
	defaults := tile57.MarinerDefaults()
	if follow.Soundings != defaults.Soundings {
		t.Fatalf(
			"omitted soundings=%v, want engine default %v",
			follow.Soundings,
			defaults.Soundings,
		)
	}
}
