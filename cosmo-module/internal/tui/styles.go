package tui

import "charm.land/lipgloss/v2"

// Shared styles for the app shell. Page packages keep their own local styles;
// these cover the chrome (header, tab bar, footer).
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13"))

	groupStyle = lipgloss.NewStyle().
			Faint(true)

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("13")).
			Padding(0, 1)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("7")).
				Padding(0, 1)

	tabBarStyle = lipgloss.NewStyle().
			Padding(0, 0, 1, 0)

	footerErrStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))
)
