package main

import "fmt"

type portItem struct {
	port      Port
	user      string
	itemType  string // "service" or "pod"
}

func (i portItem) Title() string {
	if i.itemType == "service" {
		return i.port.service
	}
	return i.port.pod
}

func (i portItem) Description() string {
	if i.itemType == "service" {
		return fmt.Sprintf("ssh -L local_port:svc.%s.%s:%d %s@jump_host",
			i.port.namespace, i.port.service, i.port.port, i.user)
	}
	return fmt.Sprintf("ssh -L local_port:%s.%s:%d %s@jump_host",
		i.port.namespace, i.port.pod, i.port.port, i.user)
}

func (i portItem) FilterValue() string {
	if i.itemType == "service" {
		return i.port.namespace + "/" + i.port.service
	}
	return i.port.namespace + "/" + i.port.pod
}
