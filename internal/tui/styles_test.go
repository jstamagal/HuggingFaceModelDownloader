// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestSetThemeAutoUsesColorFGBGLightBackground(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "client 123 server 22")
	t.Setenv("COLORFGBG", "0;15")
	t.Cleanup(func() { SetTheme("dark") })

	SetTheme("auto")
	if themeIsDark {
		t.Fatal("auto theme ignored COLORFGBG light background")
	}
}

func TestSetThemeAutoUsesColorFGBGDarkBackground(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "client 123 server 22")
	t.Setenv("COLORFGBG", "15;0")
	t.Cleanup(func() { SetTheme("dark") })

	SetTheme("auto")
	if !themeIsDark {
		t.Fatal("auto theme ignored COLORFGBG dark background")
	}
}

func TestColorModeAlwaysOverridesNoColor(t *testing.T) {
	SetColorMode("always")
	t.Cleanup(func() { SetColorMode("auto") })

	profile, forced := configuredColorProfile([]string{
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"SSH_TTY=/dev/pts/11",
	})
	if !forced {
		t.Fatal("always mode did not force a color profile")
	}
	if profile != colorprofile.ANSI256 {
		t.Fatalf("profile = %s, want ANSI256", profile)
	}
}

func TestColorModeAutoLeavesDetectionToBubbleTea(t *testing.T) {
	SetColorMode("auto")
	t.Cleanup(func() { SetColorMode("auto") })

	profile, forced := configuredColorProfile([]string{
		"TERM=xterm-256color",
		"NO_COLOR=1",
	})
	if forced {
		t.Fatalf("auto mode unexpectedly forced profile %s", profile)
	}
}

func TestColorModeNeverUsesASCII(t *testing.T) {
	SetColorMode("never")
	t.Cleanup(func() { SetColorMode("auto") })

	profile, forced := configuredColorProfile([]string{"TERM=xterm-256color"})
	if !forced || profile != colorprofile.ASCII {
		t.Fatalf("configuredColorProfile() = (%s, %t), want (Ascii, true)", profile, forced)
	}
}
