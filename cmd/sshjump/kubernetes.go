package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubernetesPortsForUser return a list of Kubernetes services/containers the provided user is allowed to reach.
func (srv *Server) KubernetesPortsForUser(ctx context.Context, user string) (Ports, error) {
	var kports []Port

	// list all pods in all namespaces
	pods, err := srv.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("can't fetch pods list %w", err)
	}

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				kports = append(kports, Port{
					namespace: pod.Namespace,
					pod:       pod.Name,
					container: container.Name,
					port:      port.ContainerPort,
					addr:      pod.Status.HostIP,
				})
			}
		}
	}

	// Get the list of services in all namespaces
	services, err := srv.clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("can't fetch services list %w", err)
	}

	// Iterate through the services get their names and ports
	for _, service := range services.Items {
		var addr string
		if len(service.Spec.ClusterIPs) > 0 {
			addr = service.Spec.ClusterIPs[0]
		}

		for _, port := range service.Spec.Ports {
			kports = append(kports, Port{
				namespace: service.Namespace,
				service:   service.Name,
				port:      port.Port,
				addr:      addr,
			})
		}
	}

	userPerms, exists := srv.permissions[user]
	if !exists {
		// If the user doesn't exist in the permissions map, return an empty slice
		return []Port{}, nil
	}

	return Allowed(kports, userPerms), nil
}
