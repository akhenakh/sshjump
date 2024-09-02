package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type containerPort struct {
	namespace string
	pod       string
	container string
	port      int32
}

// PortsForUser return a list of services the provided user is allowed to reach
func (srv *Server) PortsForUser(ctx context.Context, user string) ([]containerPort, error) {
	// list all pods in all namespaces
	pods, err := srv.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("can't fetch pods list %w", err)
	}

	var cps []containerPort

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				cps = append(cps, containerPort{
					namespace: pod.Namespace,
					pod:       pod.Name,
					container: container.Name,
					port:      port.ContainerPort,
				})
			}
		}
	}

	return cps, nil
}
