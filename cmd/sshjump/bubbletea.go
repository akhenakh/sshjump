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

	// Channel to signal the SSH forwarder
	targetChan chan string

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

	// Get the communication channel from the context
	targetChan := GetTargetChannel(s.Context())

	m := model{
		state:      stateLoading,
		user:       s.User(),
		server:     srv,
		list:       l,
		spinner:    sp,
		logger:     srv.logger,
		renderer:   renderer,
		targetChan: targetChan,
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
			// If we are connected, we shouldn't close the app with 'q',
			// as it kills the forwarder. Force Ctrl+C or keep it running.
			// For now, let's allow quit, which kills the tunnel.
			return m, tea.Quit
		case "enter":
			if m.state == stateList {
				i, ok := m.list.SelectedItem().(portItem)
				if ok {
					m.selected = &i
					m.state = stateConnected

					// Send the real address to the SSH Forwarder handler
					// This unblocks the DirectTCPIPHandler
					realAddr := fmt.Sprintf("%s:%d", i.port.addr, i.port.port)

					// Send in a goroutine to avoid blocking UI if channel is full (unlikely)
					go func() {
						m.targetChan <- realAddr
					}()
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
		return m, nil

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
		if m.selected == nil {
			return ""
		}

		title := fmt.Sprintf("✔ Connected to %s", m.selected.Title())

		// We cannot know the user's local port (e.g., -L 8082:...), so we provide a generic message.
		content := fmt.Sprintf(`
Tunnel target set to:
%s

You can now connect to your local forwarded port.
(The connection will hang until you initiate traffic locally)

Press 'q' or Ctrl+C to disconnect.
`, m.statusStyle.Render(fmt.Sprintf("%s:%d", m.selected.port.addr, m.selected.port.port)))

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
