package main

import (
	"fmt"
	"strings"
)

type portItem struct {
	port     Port
	user     string
	itemType string // "service" or "pod"
}

func (i portItem) Title() string {
	if i.itemType == "service" {
		return fmt.Sprintf("Service: %s (%s)", i.port.service, i.port.namespace)
	}
	return fmt.Sprintf("Pod: %s (%s)", i.port.pod, i.port.namespace)
}

func (i portItem) Description() string {
	// We return the command string here
	localPort := i.port.port
	if localPort < 1024 {
		localPort = 8080 // suggest safe local port if remote is privileged
	}

	target := ""
	if i.itemType == "service" {
		target = fmt.Sprintf("svc.%s.%s", i.port.namespace, i.port.service)
	} else {
		target = fmt.Sprintf("%s.%s", i.port.namespace, i.port.pod)
	}

	// Assuming sshjump hostname is 'jump-host' placeholder, user updates manually or we can inject it
	return fmt.Sprintf("ssh -L %d:%s:%d %s@<jump-host> -p 2222",
		localPort, target, i.port.port, i.user)
}

func (i portItem) FilterValue() string {
	var sb strings.Builder
	sb.WriteString(i.port.namespace)
	sb.WriteString("/")
	if i.itemType == "service" {
		sb.WriteString(i.port.service)
	} else {
		sb.WriteString(i.port.pod)
	}
	return sb.String()
}
