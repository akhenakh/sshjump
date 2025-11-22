package main

import "fmt"

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
	// i.port.addr is already "ip:port"
	return fmt.Sprintf("Remote: %s", i.port.addr)
}

func (i portItem) FilterValue() string {
	if i.itemType == "service" {
		return i.port.namespace + "/" + i.port.service
	}
	return i.port.namespace + "/" + i.port.pod
}
