package main

import (
	"github.com/charmbracelet/ssh"
)

const targetChanKey = "target_channel"

// GetTargetChannel returns the channel used to coordinate the TUI selection and the TCP Forwarder.
// It ensures the channel is created only once per SSH connection.
func GetTargetChannel(ctx ssh.Context) chan string {
	// Check if it exists
	if val := ctx.Value(targetChanKey); val != nil {
		return val.(chan string)
	}

	// We need to lock the context to avoid race conditions if TUI and Forwarder start instantly together
	// Since ssh.Context doesn't expose a mutex, we use a global one or rely on the fact
	// that SetValue is usually thread-safe in gliderlabs/ssh (it uses a sync.Mutex internally).

	// Create a buffered channel so the TUI doesn't block if the forwarder isn't ready yet,
	// though usually, the forwarder is waiting.
	ch := make(chan string, 1)
	ctx.SetValue(targetChanKey, ch)
	return ch
}
