package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubernetesPortsForUser return a list of Kubernetes services/containers the provided user is allowed to reach.
func (srv *Server) KubernetesPortsForUser(ctx context.Context, user string) (Ports, error) {
	// 1. Check permissions first before hitting API to fail fast
	srv.mu.RLock()
	userPerms, exists := srv.permissions[user]
	srv.mu.RUnlock()

	if !exists {
		return []Port{}, nil
	}

	var kports []Port

	// Optimization: If the user restricts by namespace, we could use FieldSelectors in ListOptions
	// to only fetch specific namespaces, but List(AllNamespaces) is usually more efficient
	// than N calls for N namespaces unless N is very small and cluster is very large.
	// Sticking to ListAll for simplicity in this bastion context.

	// list all pods in all namespaces
	pods, err := srv.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("can't fetch pods list %w", err)
	}

	for _, pod := range pods.Items {
		// Skip pods that aren't running
		if pod.Status.Phase != "Running" {
			continue
		}
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				kports = append(kports, Port{
					namespace: pod.Namespace,
					pod:       pod.Name,
					container: container.Name,
					port:      port.ContainerPort,
					addr:      pod.Status.PodIP, // Use PodIP usually, not HostIP, unless using HostNetwork
				})
			}
		}
	}

	// Get the list of services in all namespaces
	services, err := srv.clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("can't fetch services list %w", err)
	}

	for _, service := range services.Items {
		var addr string
		if len(service.Spec.ClusterIPs) > 0 {
			addr = service.Spec.ClusterIPs[0]
		}

		// Skip headless services or those without IP for now (simplified)
		if addr == "None" || addr == "" {
			continue
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

	return Allowed(kports, userPerms), nil
}
