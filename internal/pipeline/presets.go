package pipeline

import (
	"fmt"
	"sort"
)

// Preset trades speed for detail. Later phases add stages to standard/full.
type Preset string

const (
	PresetFast     Preset = "fast"
	PresetStandard Preset = "standard"
	PresetFull     Preset = "full"
)

type presetDef struct {
	maxSide         int
	geometryModel   string
	resolutionLevel int
}

var presets = map[Preset]presetDef{
	PresetFast:     {maxSide: 1024, geometryModel: "moge2-vits-normal", resolutionLevel: 5},
	PresetStandard: {maxSide: 2048, geometryModel: "moge2-vitl-normal", resolutionLevel: 9},
	PresetFull:     {maxSide: 3072, geometryModel: "moge2-vitl-normal", resolutionLevel: 9},
}

// ParsePreset validates a preset name; empty means standard.
func ParsePreset(s string) (Preset, error) {
	if s == "" {
		return PresetStandard, nil
	}
	if _, ok := presets[Preset(s)]; !ok {
		return "", fmt.Errorf("%w: unknown preset %q (want one of %v)", ErrInvalidInput, s, PresetNames())
	}
	return Preset(s), nil
}

// PresetNames lists valid presets in a stable order.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for p := range presets {
		names = append(names, string(p))
	}
	sort.Strings(names)
	return names
}
