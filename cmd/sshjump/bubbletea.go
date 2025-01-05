package main

import (

	"context"


	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/davecgh/go-spew/spew"
)

var docStyle = lipgloss.NewStyle().Margin(1, 2)

type model struct {
	list     list.Model
	ready    bool
	user     string
	quitting bool
	bg       string
	docStyle lipgloss.Style
}

// You can wire any Bubble Tea model up to the middleware with a function that
// handles the incoming ssh.Session. Here we just grab the terminal info and
// pass it to the new model. You can also return tea.ProgramOptions (such as
// tea.WithAltScreen) on a session by session basis.
func (srv *Server) teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	userConnections.WithLabelValues(s.User()).Inc()

	// This should never fail, as we are using the activeterm middleware.
	pty, _, _ := s.Pty()

	// When running a Bubble Tea app over SSH, you shouldn't use the default
	// lipgloss.NewStyle function.
	// That function will use the color profile from the os.Stdin, which is the
	// server, not the client.
	// We provide a MakeRenderer function in the bubbletea middleware package,
	// so you can easily get the correct renderer for the current session, and
	// use it to create the styles.
	// The recommended way to use these styles is to then pass them down to
	// your Bubble Tea model.
	renderer := bubbletea.MakeRenderer(s)
	docStyle := renderer.NewStyle().Margin(1, 2)
	// txtStyle := renderer.NewStyle().Foreground(lipgloss.Color("10"))
	// quitStyle := renderer.NewStyle().Foreground(lipgloss.Color("8"))


	bg := "light"
	if renderer.HasDarkBackground() {
		bg = "dark"
	}


	// Get available ports
	ports, err := srv.KubernetesPortsForUser(context.Background(), s.User())
	if err != nil {
		srv.logger.Error("failed to get ports for user", "error", err)
		ports = Ports{}
	}

	// Convert ports to list items
	items := []list.Item{}
	for _, p := range ports {
		if p.service != "" {
			items = append(items, portItem{
				port:     p,
				user:     s.User(),
				itemType: "service",
			})
		} else {
			items = append(items, portItem{
				port:     p,
				user:     s.User(),
				itemType: "pod",
			})
		}
	}

	// Create new list
	l := list.New(items, list.NewDefaultDelegate(), pty.Window.Width, pty.Window.Height-4)
	l.Title = "Available Ports"
	l.SetShowHelp(true)
	l.Styles.Title = renderer.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	m := model{
		term:        pty.Term,
		profile:     renderer.ColorProfile().Name(),
		width:       pty.Window.Width,
		height:      pty.Window.Height,
		bg:          bg,
		docStyle:    docStyle,
		user:        s.User(),
		logger:      srv.logger,
		currentPort: currentPort,
		list:        list.New(nil, list.NewDefaultDelegate(), 0, 0),
	}
	m.list.Title = "Available Connections"

	return &m, []tea.ProgramOption{tea.WithAltScreen()}
}

<<<<<<< HEAD
// Just a generic tea.Model to demo terminal information of ssh.
type model struct {
	term           string
	profile        string
	width          int
	height         int
	bg             string
	user           string
	logger         *slog.Logger
	currentPort    Port
	availablePorts Ports
	docStyle       lipgloss.Style
	list           list.Model
	dump           bool
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.dump {
		m.logger.Debug("update", "msg", spew.Sdump(msg))
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *model) View() string {
	return m.docStyle.Render(m.list.View())
}

// StructuredMiddlewareWithLogger provides basic connection logging in a structured form.
// Connects are logged with the remote address, invoked command, TERM setting,
// window dimensions, client version, and if the auth was public key based.
// Disconnect will log the remote address and connection duration.
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

func (m model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}
	if !m.ready {
		return "\n  Initializing..."
	}
	return docStyle.Render(m.list.View())

}
