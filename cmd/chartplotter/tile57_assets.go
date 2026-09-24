package main

import (
	"fmt"
	"os"
	"path/filepath"

	tile57 "github.com/beetlebugorg/tile57/bindings/go"
)

// emitS101Assets writes the client asset files (colortables/linestyles/sprite/
// patterns) into dir via the native libtile57 asset baker, so the served symbology
// is produced by the SAME engine that renders the tiles. catalogDir "" uses
// libtile57's embedded S-101 catalogue; a path emits from that on-disk
// PortrayalCatalog instead. Returns the files written.
func emitS101Assets(catalogDir, dir string) ([]string, error) {
	a, err := tile57.BakeAssets(catalogDir)
	if err != nil {
		return nil, err
	}

	// Current tile57's MapLibre style expects the dedicated drawn-scale sprite
	// atlas from tile57_bake_sprite_mln. BakeAssets().Sprite* is the historical
	// generic 0.08 atlas; pairing that atlas with the current style inflates ENC
	// symbols by about 0.08/0.028346 = 2.82x.
	spriteJSON, spritePNG, err := tile57.BakeMapLibreSprite(
		catalogDir,
		1,
		tile57.SchemeDay,
	)
	if err != nil {
		return nil, err
	}

	// Restore the SOURCE raster density of the older hosted demo without
	// restoring its oversized on-screen symbols. The old tile57 atlas rasterized
	// at 0.08 catalogue units; current tile57 rasterizes at the actual drawn scale
	// 0.02834627777338028. Their ratio is ~2.82224.
	//
	// Baking the current atlas at this ratio and preserving pixelRatio in MapLibre
	// gives the same logical/physical S-52 symbol size as the current engine, but
	// roughly the same source sampling density/sharpness as the old demo.
	const sharpSpriteRatio = 2.82224003587262
	sharpSpriteJSON, sharpSpritePNG, err := tile57.BakeMapLibreSprite(
		catalogDir,
		sharpSpriteRatio,
		tile57.SchemeDay,
	)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := []struct {
		name string
		data []byte
	}{
		{"colortables.json", a.Colortables},
		{"linestyles.json", a.Linestyles},
		{"sprite.json", spriteJSON},
		{"sprite.png", spritePNG},
		{"sprite-hq.json", sharpSpriteJSON},
		{"sprite-hq.png", sharpSpritePNG},
		{"patterns.json", a.PatternJSON},
		{"patterns.png", a.PatternPNG},
	}
	var written []string
	for _, f := range out {
		if len(f.data) == 0 {
			continue // an empty buffer (e.g. no area patterns) — skip
		}
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return nil, err
		}
		written = append(written, p)
	}
	src := "libtile57 embedded catalogue"
	if catalogDir != "" {
		src = catalogDir
	}
	fmt.Printf("tile57: emitted %d S-101 client asset file(s) from %s\n", len(written), src)
	return written, nil
}
