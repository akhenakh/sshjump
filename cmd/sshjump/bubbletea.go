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

type state int

const (
	stateLoading state = iota
	stateList
	stateConnected
	stateError
)

type model struct {
	state    state
	list     list.Model
	spinner  spinner.Model
	user     string
	server   *Server
	err      error
	selected *portItem

	// For static forwards
	connectedTarget string

	resolver   *TargetResolver
	statusChan chan string

	renderer    *lipgloss.Renderer
	docStyle    lipgloss.Style
	statusStyle lipgloss.Style

	width  int
	height int
	dump   bool
	logger *slog.Logger
}

type portsLoadedMsg []list.Item
type errMsg error
type connectionEstablishedMsg string

func (srv *Server) teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	userConnections.WithLabelValues(s.User()).Inc()
	pty, _, _ := s.Pty()

	renderer := bubbletea.MakeRenderer(s)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = renderer.NewStyle().Foreground(lipgloss.Color("205"))

	l := list.New([]list.Item{}, list.NewDefaultDelegate(), pty.Window.Width, pty.Window.Height-4)
	l.Title = "Select a target to Connect"
	l.Styles.Title = renderer.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	resolver := GetTargetResolver(s.Context())
	statusChan := GetStatusChannel(s.Context())

	m := model{
		state:      stateLoading,
		user:       s.User(),
		server:     srv,
		list:       l,
		spinner:    sp,
		logger:     srv.logger,
		renderer:   renderer,
		resolver:   resolver,
		statusChan: statusChan,
		docStyle:   renderer.NewStyle().Margin(1, 2),
		statusStyle: renderer.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#04B575")).
			Bold(true).
			Padding(1, 2),
		width:  pty.Window.Width,
		height: pty.Window.Height,
	}

	return &m, []tea.ProgramOption{tea.WithAltScreen()}
}

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

// Command to wait for a static connection event from the server
func waitForStatus(ch chan string) tea.Cmd {
	return func() tea.Msg {
		target := <-ch
		return connectionEstablishedMsg(target)
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		fetchPorts(m.user, m.server),
		waitForStatus(m.statusChan),
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
		case "enter":
			if m.state == stateList {
				i, ok := m.list.SelectedItem().(portItem)
				if ok {
					m.selected = &i
					m.state = stateConnected

					// FIX: i.port.addr is already "IP:PORT", so we do NOT append port again.
					realAddr := i.port.addr

					// Store the target safely
					m.resolver.mu.Lock()
					m.resolver.target = realAddr
					m.resolver.mu.Unlock()

					// Signal readiness to all waiting connections
					select {
					case <-m.resolver.Resolved:
						// Already closed
					default:
						close(m.resolver.Resolved)
					}
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
		if m.state == stateLoading {
			m.state = stateList
		}
		return m, nil

	case connectionEstablishedMsg:
		m.connectedTarget = string(msg)
		m.state = stateConnected
		return m, waitForStatus(m.statusChan)

	case errMsg:
		m.err = msg
		m.state = stateError
		return m, nil
	}

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
	if m.state == stateError {
		return m.docStyle.Render(fmt.Sprintf("Error: %v\nPress q to quit.", m.err))
	}

	switch m.state {
	case stateLoading:
		return m.docStyle.Render(fmt.Sprintf("%s Loading Kubernetes resources...", m.spinner.View()))

	case stateConnected:
		var title, details string

		if m.connectedTarget != "" {
			title = fmt.Sprintf("✔ Active Tunnel: %s", m.connectedTarget)
			details = "Traffic is active on your static forward.\n\n(Multiple forwards may be active)"
		} else if m.selected != nil {
			title = fmt.Sprintf("✔ Connected to %s", m.selected.Title())
			// FIX: Just display addr, it already contains the port
			details = fmt.Sprintf("Tunnel target set to: %s", m.selected.port.addr)
		} else {
			title = "✔ Connected"
		}

		content := fmt.Sprintf(`
%s

You can now connect to your local forwarded port.
Press 'q' or Ctrl+C to disconnect.
`, details)

		return m.docStyle.Render(
			lipgloss.JoinVertical(lipgloss.Left,
				m.renderer.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render(title),
				content,
			),
		)

	default:
		return m.docStyle.Render(m.list.View())
	}
}

func StructuredMiddlewareWithLogger(logger *slog.Logger) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			ct := time.Now()
			logger.Info("connect", "user", sess.User(), "remote", sess.RemoteAddr().String())
			next(sess)
			logger.Info("disconnect", "user", sess.User(), "duration", time.Since(ct))
		}
	}
}
