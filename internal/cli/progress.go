package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func runProgress(parent context.Context, input io.Reader, output io.Writer, label string, operation func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := &progressModel{ctx: ctx, cancel: cancel, operation: operation, label: label}
	// Do not give Tea the operation context: it would quit on cancellation
	// before the application returns its actual error. Wait for completion.
	_, err := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler()).Run()
	if err != nil {
		return err
	}
	return model.err
}

type (
	operationDone struct{ err error }
	progressTick  time.Time
)

func tickProgress() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return progressTick(t) })
}

type progressModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	operation func(context.Context) error
	label     string
	frame     int
	done      bool
	err       error
}

func (m *progressModel) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return operationDone{m.operation(m.ctx)} }, tickProgress())
}

func (m *progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case operationDone:
		m.done, m.err = true, msg.err
		return m, tea.Quit
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancel()
		}
	case progressTick:
		m.frame++
		return m, tickProgress()
	}
	return m, nil
}

func (m *progressModel) View() string {
	if m.done {
		return ""
	}
	if m.ctx.Err() != nil {
		return "Canceling " + m.label + "...\n"
	}
	const frames = "|/-\\"
	return string(frames[m.frame%len(frames)]) + " " + m.label + "... (ctrl+c to cancel)\n"
}
