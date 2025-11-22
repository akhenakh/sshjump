package main

import (
	"github.com/charmbracelet/ssh"
)

const (
	targetChanKey = "target_channel"
	statusChanKey = "status_channel"
)

// GetTargetChannel returns the channel used for Dynamic selection (TUI -> Forwarder).
func GetTargetChannel(ctx ssh.Context) chan string {
	if val := ctx.Value(targetChanKey); val != nil {
		return val.(chan string)
	}
	ch := make(chan string, 1)
	ctx.SetValue(targetChanKey, ch)
	return ch
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
