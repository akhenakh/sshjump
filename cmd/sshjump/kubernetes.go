package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Port struct {
	namespace string
	pod       string
	container string
	service   string
	port      int32
	addr      string // the real addr to connect to
}

type Ports []Port

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

	return srv.allowed(kports, user), nil
}

func (ps Ports) MatchingService(name, namespace string, port int32) (string, bool) {
	for _, ps := range ps {
		if ps.namespace == namespace && ps.service == name && ps.port == port {
			return fmt.Sprintf("%s:%d", ps.addr, ps.port), true
		}
	}

	return "", false
}

func (ps Ports) MatchingPod(name, namespace string, port int32) (string, bool) {
	for _, ps := range ps {
		if ps.namespace == namespace && ps.pod == name && ps.port == port {
			return fmt.Sprintf("%s:%d", ps.addr, ps.port), true
		}
	}

	return "", false
}

// allowed filter list of ports using user permissions.
func (srv *Server) allowed(ports Ports, user string) Ports {
	userPerms, exists := srv.permissions[user]
	if !exists {
		// If the user doesn't exist in the permissions map, return an empty slice
		return []Port{}
	}

	// full access
	if userPerms.AllowAll {
		return ports
	}

	var allowed []Port

	for _, port := range ports {
		for _, userNs := range userPerms.Namespaces {
			if userNs.Namespace == port.namespace {
				// Namespace matches
				if len(userNs.Pods) == 0 {
					// full access to the namespace
					allowed = append(allowed, port)

					continue
				}

				// check for pods & services
				// check against user perms
				for _, uPod := range userNs.Pods {
					for _, up := range uPod.Ports {
						if addr, ok := ports.MatchingPod(uPod.Name, userNs.Namespace, up); ok {
							port.addr = addr
							allowed = append(allowed, port)

							continue
						}
					}
				}
				for _, uService := range userNs.Services {
					for _, up := range uService.Ports {
						if addr, ok := ports.MatchingService(uService.Name, userNs.Namespace, up); ok {
							port.addr = addr
							allowed = append(allowed, port)

							continue
						}
					}
				}
			}
		}
	}

	return allowed
}
