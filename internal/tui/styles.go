// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color palette.
//
// Every color is a lipgloss.AdaptiveColor with a variant for light and dark
// terminal backgrounds. lipgloss detects the background (termenv OSC 11 /
// COLORFGBG) at first render; SetTheme provides a manual override for
// terminals where detection fails. No style ever paints a full-screen
// background — the terminal's own background shows through, which is the only
// scheme that stays readable in both black-on-white and white-on-black
// setups.
var (
	ColorPrimary   = lipgloss.AdaptiveColor{Light: "31", Dark: "86"}   // teal / cyan
	ColorSecondary = lipgloss.AdaptiveColor{Light: "55", Dark: "99"}   // purple
	ColorSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "82"}   // green
	ColorWarning   = lipgloss.AdaptiveColor{Light: "130", Dark: "214"} // orange
	ColorError     = lipgloss.AdaptiveColor{Light: "124", Dark: "196"} // red
	ColorMuted     = lipgloss.AdaptiveColor{Light: "244", Dark: "245"} // gray
	ColorHighlight = lipgloss.AdaptiveColor{Light: "94", Dark: "229"}  // yellow/gold

	ColorBorder      = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	ColorBorderFocus = lipgloss.AdaptiveColor{Light: "31", Dark: "86"}

	// Explicit foreground/background pairs (safe on any terminal because both
	// sides are specified).
	colorBarBg      = lipgloss.AdaptiveColor{Light: "24", Dark: "24"} // deep blue
	colorBarFg      = lipgloss.AdaptiveColor{Light: "231", Dark: "231"}
	colorSubtleBg   = lipgloss.AdaptiveColor{Light: "253", Dark: "236"}
	colorSubtleFg   = lipgloss.AdaptiveColor{Light: "238", Dark: "250"}
	colorSelectedFg = lipgloss.AdaptiveColor{Light: "231", Dark: "16"}
)

// SetTheme overrides background detection: "light", "dark", or "auto"
// (keep lipgloss/termenv detection). Call before any TUI starts.
func SetTheme(theme string) {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
}

// Selector styles
var (
	// Header styles
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	HeaderInfoStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	// Item styles
	ItemStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	SelectedItemStyle = lipgloss.NewStyle().
				PaddingLeft(2).
				Foreground(ColorSuccess)

	CursorStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	// Checkbox styles
	CheckboxChecked   = lipgloss.NewStyle().Foreground(ColorSuccess).SetString("[x]")
	CheckboxUnchecked = lipgloss.NewStyle().Foreground(ColorMuted).SetString("[ ]")

	// Quality stars
	StarFilled = lipgloss.NewStyle().Foreground(ColorWarning).SetString("★")
	StarEmpty  = lipgloss.NewStyle().Foreground(ColorMuted).SetString("☆")

	// Labels
	RecommendedBadge = lipgloss.NewStyle().
				Foreground(colorSelectedFg).
				Background(ColorSuccess).
				Padding(0, 1).
				SetString("recommended")

	SizeLabelStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Width(10).
			Align(lipgloss.Right)

	RAMLabelStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Width(12)

	DescriptionStyle = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Italic(true)

	// Category header
	CategoryStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSecondary).
			MarginTop(1).
			MarginBottom(0)

	// Footer styles
	FooterStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).
			MarginTop(1)

	FooterKeyStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	FooterDescStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	// Command box
	CommandBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1).
			MarginTop(1)

	CommandLabelStyle = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Bold(true)

	CommandTextStyle = lipgloss.NewStyle().
				Foreground(ColorHighlight)

	// Summary styles
	SummaryStyle = lipgloss.NewStyle().
			MarginTop(1).
			Padding(0, 1)

	SummaryLabelStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)

	SummaryValueStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	// Border box for main content
	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(1, 2)

	// Status bar
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(colorSubtleFg).
			Background(colorSubtleBg).
			Padding(0, 1)

	// Help keys
	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	HelpKeyStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	// Error style
	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	// Success style
	SuccessStyle = lipgloss.NewStyle().
			Foreground(ColorSuccess).
			Bold(true)

	// Search browser styles. Bars use explicit fg+bg pairs; panels and text
	// inherit the terminal background and use adaptive foregrounds.
	SearchScreenStyle = lipgloss.NewStyle()

	SearchBackgroundStyle = lipgloss.NewStyle()

	SearchTopBarStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorBarFg).
				Background(colorBarBg)

	SearchBottomBarStyle = lipgloss.NewStyle().
				Foreground(colorSubtleFg).
				Background(colorSubtleBg)

	SearchHeavySeparatorStyle = lipgloss.NewStyle().Foreground(ColorBorder)
	SearchPanelStyle          = lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(ColorBorder).
					Padding(0, 1)
	SearchFocusedPanelStyle = SearchPanelStyle.BorderForeground(ColorBorderFocus)
	SearchPanelTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchSelectedStyle     = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSelectedFg).
				Background(ColorPrimary)
	SearchResultIDStyle    = lipgloss.NewStyle()
	SearchMutedStyle       = lipgloss.NewStyle().Foreground(ColorMuted)
	SearchAccentStyle      = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	SearchInputPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchInputTextStyle   = lipgloss.NewStyle().Foreground(ColorHighlight)
	SearchFilterKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchFilterValueStyle = lipgloss.NewStyle().Foreground(ColorPrimary)
	SearchDetailLabelStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	SearchDetailValueStyle = lipgloss.NewStyle()
	SearchHelpKeyStyle     = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	SearchErrorStyle       = lipgloss.NewStyle().Bold(true).Foreground(ColorError)
)

// RenderStars renders quality stars (filled and empty).
func RenderStars(quality int) string {
	if quality <= 0 {
		return ""
	}
	if quality > 5 {
		quality = 5
	}

	var s string
	for i := 0; i < quality; i++ {
		s += StarFilled.String()
	}
	for i := quality; i < 5; i++ {
		s += StarEmpty.String()
	}
	return s
}

// RenderCheckbox renders a checkbox based on checked state.
func RenderCheckbox(checked bool) string {
	if checked {
		return CheckboxChecked.String()
	}
	return CheckboxUnchecked.String()
}

// FormatCategoryTitle formats a category key into a display title.
func FormatCategoryTitle(category string) string {
	switch category {
	case "quantization":
		return "Quantizations"
	case "variant":
		return "Precision Variants"
	case "component":
		return "Components"
	case "split":
		return "Dataset Splits"
	case "format":
		return "Weight Format"
	case "precision":
		return "Precision"
	case "vision_encoder":
		return "Vision Encoder (mmproj)"
	default:
		return "Options"
	}
}
