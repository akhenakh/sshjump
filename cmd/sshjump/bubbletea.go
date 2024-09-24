package main

import (
	"log/slog"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/davecgh/go-spew/spew"
)

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

	bg := "light"
	if renderer.HasDarkBackground() {
		bg = "dark"
	}

	// get the current targeted port
	currentPort := s.Context().Value(portContextKey).(Port) //nolint:forcetypeassert

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

	case tea.WindowSizeMsg:
		h, v := m.docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
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
}
