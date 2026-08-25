// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"image/color"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// Color palette and component styles. Charm v2 makes light/dark selection
// explicit, so SetTheme rebuilds these values before a TUI starts.
var (
	ColorPrimary, ColorSecondary, ColorSuccess    color.Color
	ColorWarning, ColorError, ColorMuted          color.Color
	ColorHighlight, ColorBorder, ColorBorderFocus color.Color
	colorBarBg, colorBarFg, colorSubtleBg         color.Color
	colorSubtleFg, colorSelectedFg                color.Color
	themeIsDark                                   = true
	themeForced                                   bool
)

// SetTheme overrides background detection: "light", "dark", or "auto"
// (keep lipgloss/termenv detection). Call before any TUI starts.
func SetTheme(theme string) {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "light":
		themeForced = true
		setStyles(false)
	case "dark":
		themeForced = true
		setStyles(true)
	default:
		themeForced = false
		// OSC 11 background-color replies are frequently swallowed by SSH
		// relays and terminal multiplexers. COLORFGBG, when present, gives us a
		// reliable initial theme; Bubble Tea can still refine it after startup.
		if isDark, ok := colorFGBGTheme(os.Getenv("COLORFGBG")); ok {
			setStyles(isDark)
		} else {
			setStyles(true)
		}
	}
}

// colorFGBGTheme interprets the final palette index as the terminal's
// background. Common values include "15;0" (dark) and "0;15" (light).
func colorFGBGTheme(value string) (isDark, ok bool) {
	parts := strings.Split(value, ";")
	if len(parts) == 0 {
		return false, false
	}
	background, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil || background < 0 || background > 15 {
		return false, false
	}

	// ANSI black through cyan, plus bright black, are conventionally dark.
	return background <= 6 || background == 8, true
}

// setBackgroundTheme applies a Bubble Tea background-color response unless a
// user explicitly selected --theme light or --theme dark.
func setBackgroundTheme(isDark bool) {
	if !themeForced && themeIsDark != isDark {
		setStyles(isDark)
	}
}

// Selector and browser styles.
var (
	TitleStyle, SubtitleStyle, HeaderInfoStyle                         lipgloss.Style
	ItemStyle, SelectedItemStyle, CursorStyle                          lipgloss.Style
	CheckboxChecked, CheckboxUnchecked                                 lipgloss.Style
	StarFilled, StarEmpty, RecommendedBadge                            lipgloss.Style
	SizeLabelStyle, RAMLabelStyle, DescriptionStyle                    lipgloss.Style
	CategoryStyle, FooterStyle, FooterKeyStyle, FooterDescStyle        lipgloss.Style
	CommandBoxStyle, CommandLabelStyle, CommandTextStyle               lipgloss.Style
	SummaryStyle, SummaryLabelStyle, SummaryValueStyle                 lipgloss.Style
	BoxStyle, StatusBarStyle, HelpStyle, HelpKeyStyle                  lipgloss.Style
	ErrorStyle, SuccessStyle                                           lipgloss.Style
	SearchScreenStyle, SearchBackgroundStyle                           lipgloss.Style
	SearchTopBarStyle, SearchBottomBarStyle, SearchHeavySeparatorStyle lipgloss.Style
	SearchPanelStyle, SearchFocusedPanelStyle, SearchPanelTitleStyle   lipgloss.Style
	SearchSelectedStyle, SearchResultIDStyle, SearchMutedStyle         lipgloss.Style
	SearchAccentStyle, SearchInputPromptStyle, SearchInputTextStyle    lipgloss.Style
	SearchFilterKeyStyle, SearchFilterValueStyle                       lipgloss.Style
	SearchDetailLabelStyle, SearchDetailValueStyle, SearchHelpKeyStyle lipgloss.Style
	SearchErrorStyle                                                   lipgloss.Style
)

func init() { setStyles(true) }

func setStyles(isDark bool) {
	themeIsDark = isDark
	pick := lipgloss.LightDark(isDark)
	ColorPrimary = pick(lipgloss.Color("31"), lipgloss.Color("86"))
	ColorSecondary = pick(lipgloss.Color("55"), lipgloss.Color("99"))
	ColorSuccess = pick(lipgloss.Color("28"), lipgloss.Color("82"))
	ColorWarning = pick(lipgloss.Color("130"), lipgloss.Color("214"))
	ColorError = pick(lipgloss.Color("124"), lipgloss.Color("196"))
	ColorMuted = pick(lipgloss.Color("244"), lipgloss.Color("245"))
	ColorHighlight = pick(lipgloss.Color("94"), lipgloss.Color("229"))
	ColorBorder = pick(lipgloss.Color("250"), lipgloss.Color("238"))
	ColorBorderFocus = pick(lipgloss.Color("31"), lipgloss.Color("86"))
	colorBarBg = lipgloss.Color("24")
	colorBarFg = lipgloss.Color("231")
	colorSubtleBg = pick(lipgloss.Color("253"), lipgloss.Color("236"))
	colorSubtleFg = pick(lipgloss.Color("238"), lipgloss.Color("250"))
	colorSelectedFg = pick(lipgloss.Color("231"), lipgloss.Color("16"))

	TitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary).
		MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	HeaderInfoStyle = lipgloss.NewStyle().Foreground(ColorSecondary)
	ItemStyle = lipgloss.NewStyle().PaddingLeft(2)
	SelectedItemStyle = lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(ColorSuccess)
	CursorStyle = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true)
	CheckboxChecked = lipgloss.NewStyle().Foreground(ColorSuccess).SetString("[x]")
	CheckboxUnchecked = lipgloss.NewStyle().Foreground(ColorMuted).SetString("[ ]")
	StarFilled = lipgloss.NewStyle().Foreground(ColorWarning).SetString("★")
	StarEmpty = lipgloss.NewStyle().Foreground(ColorMuted).SetString("☆")
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

	CategoryStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		MarginTop(1).
		MarginBottom(0)

	FooterStyle = lipgloss.NewStyle().
		Foreground(ColorMuted).
		MarginTop(1)

	FooterKeyStyle = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true)

	FooterDescStyle = lipgloss.NewStyle().
		Foreground(ColorMuted)

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

	SummaryStyle = lipgloss.NewStyle().
		MarginTop(1).
		Padding(0, 1)

	SummaryLabelStyle = lipgloss.NewStyle().
		Foreground(ColorMuted)

	SummaryValueStyle = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true)

	BoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 2)

	StatusBarStyle = lipgloss.NewStyle().
		Foreground(colorSubtleFg).
		Background(colorSubtleBg).
		Padding(0, 1)

	HelpStyle = lipgloss.NewStyle().
		Foreground(ColorMuted)

	HelpKeyStyle = lipgloss.NewStyle().
		Foreground(ColorPrimary)

	ErrorStyle = lipgloss.NewStyle().
		Foreground(ColorError).
		Bold(true)

	SuccessStyle = lipgloss.NewStyle().
		Foreground(ColorSuccess).
		Bold(true)

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
	SearchPanelStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)
	SearchFocusedPanelStyle = SearchPanelStyle.BorderForeground(ColorBorderFocus)
	SearchPanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchSelectedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorSelectedFg).
		Background(ColorPrimary)
	SearchResultIDStyle = lipgloss.NewStyle()
	SearchMutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	SearchAccentStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	SearchInputPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchInputTextStyle = lipgloss.NewStyle().Foreground(ColorHighlight)
	SearchFilterKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	SearchFilterValueStyle = lipgloss.NewStyle().Foreground(ColorPrimary)
	SearchDetailLabelStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	SearchDetailValueStyle = lipgloss.NewStyle()
	SearchHelpKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	SearchErrorStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorError)
}

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
