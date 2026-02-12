package main

import (
	"sync"

	"github.com/charmbracelet/ssh"
)

const (
	resolverKey   = "target_resolver"
	statusChanKey = "status_channel"
)

// TargetResolver acts as a thread-safe store for the user's selection.
type TargetResolver struct {
	mu           sync.RWMutex
	target       string
	TOTPVerified bool          // TOTPVerified is set to true after successful TOTP verification
	Resolved     chan struct{} // Closed when a target is selected
}

// GetTargetResolver returns the resolver state object for this session.
func GetTargetResolver(ctx ssh.Context) *TargetResolver {
	if val := ctx.Value(resolverKey); val != nil {
		return val.(*TargetResolver)
	}
	tr := &TargetResolver{
		Resolved: make(chan struct{}),
	}
	ctx.SetValue(resolverKey, tr)
	return tr
}

// GetStatusChannel returns the channel used for Static notification (Forwarder -> TUI).
func GetStatusChannel(ctx ssh.Context) chan string {
	if val := ctx.Value(statusChanKey); val != nil {
		return val.(chan string)
	}
	// Buffered channel to ensure the forwarder doesn't block if the TUI is busy/not ready
	ch := make(chan string, 10)
	ctx.SetValue(statusChanKey, ch)
	return ch
}
