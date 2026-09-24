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


func TestMarinerFromQueryDetailLevels(t *testing.T) {
	base := marinerFromQuery(url.Values{
		"displayBase":     {"1"},
		"displayStandard": {"0"},
		"displayOther":    {"0"},
	})
	if !base.DisplayBase || base.DisplayStandard || base.DisplayOther {
		t.Fatalf("base detail flags = base:%v standard:%v other:%v",
			base.DisplayBase, base.DisplayStandard, base.DisplayOther)
	}

	standard := marinerFromQuery(url.Values{
		"displayBase":     {"1"},
		"displayStandard": {"1"},
		"displayOther":    {"0"},
	})
	if !standard.DisplayBase || !standard.DisplayStandard || standard.DisplayOther {
		t.Fatalf("standard detail flags = base:%v standard:%v other:%v",
			standard.DisplayBase, standard.DisplayStandard, standard.DisplayOther)
	}

	other := marinerFromQuery(url.Values{
		"displayBase":     {"1"},
		"displayStandard": {"1"},
		"displayOther":    {"1"},
	})
	if !other.DisplayBase || !other.DisplayStandard || !other.DisplayOther {
		t.Fatalf("other detail flags = base:%v standard:%v other:%v",
			other.DisplayBase, other.DisplayStandard, other.DisplayOther)
	}
}
