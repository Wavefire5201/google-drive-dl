package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	primaryColor   = lipgloss.Color("39")  // Blue
	secondaryColor = lipgloss.Color("245") // Gray
	successColor   = lipgloss.Color("42")  // Green
	errorColor     = lipgloss.Color("196") // Red
	warningColor   = lipgloss.Color("214") // Orange

	// Styles
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			MarginBottom(1)

	SelectedStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	NormalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	DimStyle = lipgloss.NewStyle().
			Foreground(secondaryColor)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(errorColor)

	SuccessStyle = lipgloss.NewStyle().
			Foreground(successColor)

	WarningStyle = lipgloss.NewStyle().
			Foreground(warningColor)

	HelpStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			MarginTop(1)

	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(secondaryColor).
			Padding(0, 1)

	ProgressBarFull = lipgloss.NewStyle().
			Foreground(successColor)

	ProgressBarEmpty = lipgloss.NewStyle().
				Foreground(secondaryColor)
)
