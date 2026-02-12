package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/davecgh/go-spew/spew"
)

type state int

const (
	stateTOTP state = iota
	stateLoading
	stateList
	stateConnected
	stateError
)

type model struct {
	state     state
	list      list.Model
	spinner   spinner.Model
	totpInput textinput.Model
	user      string
	server    *Server
	err       error
	selected  *portItem

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
type totpValidatedMsg bool

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

	// Initialize TOTP input
	ti := textinput.New()
	ti.Placeholder = "Enter 6-digit TOTP code"
	ti.CharLimit = 6
	ti.Width = 20
	ti.EchoMode = textinput.EchoPassword
	ti.Focus()

	resolver := GetTargetResolver(s.Context())
	statusChan := GetStatusChannel(s.Context())

	// Determine initial state based on TOTP requirement
	perms := srv.PermsForUser(s.User())
	initialState := stateLoading
	if NeedsTOTPVerification(s.Context(), perms) {
		initialState = stateTOTP
	}

	m := model{
		state:      initialState,
		user:       s.User(),
		server:     srv,
		list:       l,
		spinner:    sp,
		totpInput:  ti,
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
	// If TOTP is required, just blink the cursor
	if m.state == stateTOTP {
		return textinput.Blink
	}

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
			if m.state == stateTOTP {
				// Validate TOTP code
				code := m.totpInput.Value()
				perms := m.server.PermsForUser(m.user)
				if ValidateTOTP(perms.TOTPSecret, code) {
					return m, func() tea.Msg {
						return totpValidatedMsg(true)
					}
				} else {
					m.totpInput.SetValue("")
					m.err = fmt.Errorf("invalid TOTP code")
					return m, nil
				}
			}
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

	case totpValidatedMsg:
		if bool(msg) {
			// Set TOTP as verified in resolver
			m.resolver.mu.Lock()
			m.resolver.TOTPVerified = true
			m.resolver.mu.Unlock()
			m.err = nil
			m.state = stateLoading
			return m, tea.Batch(
				m.spinner.Tick,
				fetchPorts(m.user, m.server),
				waitForStatus(m.statusChan),
			)
		}

	case errMsg:
		m.err = msg
		m.state = stateError
		return m, nil
	}

	switch m.state {
	case stateTOTP:
		m.totpInput, cmd = m.totpInput.Update(msg)
		cmds = append(cmds, cmd)
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
	case stateTOTP:
		title := m.renderer.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			Render("🔐 TOTP 2FA Required")

		instructions := "Enter your 6-digit TOTP code to continue."
		if m.err != nil {
			instructions = m.renderer.NewStyle().
				Foreground(lipgloss.Color("#FF0000")).
				Render("❌ Invalid TOTP code. Please try again.")
		}

		return m.docStyle.Render(
			lipgloss.JoinVertical(lipgloss.Left,
				title,
				"",
				instructions,
				"",
				m.totpInput.View(),
				"",
				m.renderer.NewStyle().Foreground(lipgloss.Color("241")).Render("Press Enter to submit, Ctrl+C to quit"),
			),
		)

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
