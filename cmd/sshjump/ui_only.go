package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/client-go/kubernetes"
)

func startUIOnly(clientset *kubernetes.Clientset) {
	slogFile, err := os.OpenFile("slog_app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		log.Fatal("Error opening slog file:", err)
	}
	defer slogFile.Close()

	logger := slog.New(slog.NewJSONHandler(slogFile, nil))

	p := tea.NewProgram(initialUIOnlyModel(logger), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
	}
}

func initialUIOnlyModel(logger *slog.Logger) *model {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Available Connections"

	return &model{
		logger:   logger,
		user:     "bob",
		dump:     true,
		docStyle: lipgloss.NewStyle().Margin(1, 2),
		list:     l,
	}
}
