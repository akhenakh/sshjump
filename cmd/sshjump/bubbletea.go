package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/davecgh/go-spew/spew"
)

// Enum for UI state management
type state int

const (
	stateLoading state = iota
	stateList
	stateSelected
)

type model struct {
	state    state
	list     list.Model
	spinner  spinner.Model
	user     string
	server   *Server
	err      error
	selected *portItem

	// Styles
	renderer *lipgloss.Renderer
	docStyle lipgloss.Style
	cmdStyle lipgloss.Style

	width  int
	height int
	dump   bool
	logger *slog.Logger
}

// Define custom messages
type portsLoadedMsg []list.Item
type errMsg error

func (srv *Server) teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	userConnections.WithLabelValues(s.User()).Inc()
	pty, _, _ := s.Pty()

	renderer := bubbletea.MakeRenderer(s)

	// Initialize spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = renderer.NewStyle().Foreground(lipgloss.Color("205"))

	// Initialize list (empty initially)
	l := list.New([]list.Item{}, list.NewDefaultDelegate(), pty.Window.Width, pty.Window.Height-4)
	l.Title = "Available Connections"
	l.Styles.Title = renderer.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	m := model{
		state:    stateLoading,
		user:     s.User(),
		server:   srv,
		list:     l,
		spinner:  sp,
		logger:   srv.logger,
		renderer: renderer,
		docStyle: renderer.NewStyle().Margin(1, 2),
		cmdStyle: renderer.NewStyle().
			Foreground(lipgloss.Color("#04B575")).
			Background(lipgloss.Color("#252525")).
			Padding(1, 2).
			MarginTop(1),
		width:  pty.Window.Width,
		height: pty.Window.Height,
	}

	return &m, []tea.ProgramOption{tea.WithAltScreen()}
}

// Command to load data asynchronously
func fetchPorts(user string, srv *Server) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ports, err := srv.KubernetesPortsForUser(ctx, user)
		if err != nil {
			return errMsg(err)
		}

		items := []list.Item{}
		for _, p := range ports {
			itemType := "pod"
			if p.service != "" {
				itemType = "service"
			}
			items = append(items, portItem{
				port:     p,
				user:     user,
				itemType: itemType,
			})
		}
		return portsLoadedMsg(items)
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		fetchPorts(m.user, m.server),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.dump {
		m.logger.Debug("update", "msg", spew.Sdump(msg))
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.state == stateSelected {
				m.state = stateList
				m.selected = nil
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			if m.state == stateList {
				i, ok := m.list.SelectedItem().(portItem)
				if ok {
					m.selected = &i
					m.state = stateSelected
				}
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h, v := m.docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)

	case portsLoadedMsg:
		m.list.SetItems(msg)
		m.state = stateList
		// Stop spinner
		return m, nil

	case errMsg:
		m.err = msg
		return m, tea.Quit
	}

	// Model Logic based on State
	switch m.state {
	case stateLoading:
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case stateList:
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\nPress q to quit.", m.err)
	}

	switch m.state {
	case stateLoading:
		return m.docStyle.Render(fmt.Sprintf("%s Loading Kubernetes resources...", m.spinner.View()))

	case stateSelected:
		if m.selected == nil {
			return ""
		}

		header := m.renderer.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			Render(fmt.Sprintf("Forwarding to %s", m.selected.Title()))

		desc := fmt.Sprintf("To access this %s, run the following command in a new terminal:", m.selected.itemType)

		// Construct the command string
		cmdStr := m.selected.Description()

		content := fmt.Sprintf("%s\n\n%s\n%s\n\nPress esc to back.", header, desc, m.cmdStyle.Render(cmdStr))

		return m.docStyle.Render(content)

	default:
		return m.docStyle.Render(m.list.View())
	}
}

// StructuredMiddlewareWithLogger implementation (same as before)
func StructuredMiddlewareWithLogger(logger *slog.Logger) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			ct := time.Now()
			logger.Info(
				"connect",
				"user", sess.User(),
				"remote-addr", sess.RemoteAddr().String(),
				"client-version", sess.Context().ClientVersion(),
			)
			next(sess)
			logger.Info(
				"disconnect",
				"user", sess.User(),
				"remote-addr", sess.RemoteAddr().String(),
				"duration", time.Since(ct),
			)
		}
	}
}
