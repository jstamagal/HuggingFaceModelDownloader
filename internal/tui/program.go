// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

const (
	colorModeAuto   = "auto"
	colorModeAlways = "always"
	colorModeNever  = "never"
)

var colorMode = colorModeAuto

// SetColorMode controls whether interactive programs honor terminal color
// detection, force color, or render without color.
func SetColorMode(mode string) {
	colorMode = strings.ToLower(strings.TrimSpace(mode))
}

// programOptions returns options shared by every Bubble Tea program. Auto
// deliberately leaves detection to Bubble Tea, including its support for the
// NO_COLOR standard. Always is the explicit escape hatch for environments
// (often remote shells) that inject NO_COLOR even though the terminal supports
// ANSI colors.
func programOptions() []tea.ProgramOption {
	profile, forced := configuredColorProfile(os.Environ())
	if !forced {
		return nil
	}
	return []tea.ProgramOption{tea.WithColorProfile(profile)}
}

func configuredColorProfile(environ []string) (colorprofile.Profile, bool) {
	switch colorMode {
	case colorModeAlways:
		clean := make([]string, 0, len(environ)+1)
		for _, entry := range environ {
			if strings.HasPrefix(entry, "NO_COLOR=") ||
				strings.HasPrefix(entry, "CLICOLOR_FORCE=") {
				continue
			}
			clean = append(clean, entry)
		}
		clean = append(clean, "CLICOLOR_FORCE=1")
		profile := colorprofile.Env(clean)
		if profile < colorprofile.ANSI {
			profile = colorprofile.ANSI
		}
		return profile, true
	case colorModeNever:
		return colorprofile.ASCII, true
	default:
		return colorprofile.Unknown, false
	}
}
