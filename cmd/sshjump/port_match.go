package main

import "fmt"

type Port struct {
	namespace string
	pod       string
	container string
	service   string
	port      int32
	addr      string // the real addr to connect to
}

type Ports []Port

func (ps Ports) MatchingService(name, namespace string, port int32) (string, bool) {
	for _, p := range ps {
		if p.namespace == namespace && p.service == name && p.port == port {
			// The address is already formatted by Allowed()
			return p.addr, true
		}
	}
	return "", false
}

func (ps Ports) MatchingPod(name, namespace string, port int32) (string, bool) {
	for _, p := range ps {
		if p.namespace == namespace && p.pod == name && p.port == port {
			// The address is already formatted by Allowed()
			return p.addr, true
		}
	}
	return "", false
}

// Allowed filter list of ports using user permissions.
// It also ensures the Port.addr field is formatted as "host:port" for connectability.
func Allowed(ports Ports, userPerms Permission) Ports {
	// Optimization: Pre-calculate strings or use logic inside loop
	if userPerms.AllowAll {
		// We must return a new slice where addresses are formatted
		allowed := make([]Port, len(ports))
		for i, p := range ports {
			p.addr = fmt.Sprintf("%s:%d", p.addr, p.port)
			allowed[i] = p
		}
		return allowed
	}

	// Pre-process permissions into a lookup map for O(1) access
	// Map Key: "namespace" -> Permission Config for that namespace
	nsPerms := make(map[string]Namespace)
	for _, ns := range userPerms.Namespaces {
		nsPerms[ns.Namespace] = ns
	}

	var allowed []Port

	for _, port := range ports {
		perm, exists := nsPerms[port.namespace]
		if !exists {
			continue
		}

		// If namespace exists in permissions but has no specific pod/service restrictions,
		// it implies full access to that namespace.
		if len(perm.Pods) == 0 && len(perm.Services) == 0 {
			port.addr = fmt.Sprintf("%s:%d", port.addr, port.port)
			allowed = append(allowed, port)
			continue
		}

		// Check specific Pods
		if port.pod != "" {
			for _, p := range perm.Pods {
				if p.Name == port.pod {
					for _, allowedPort := range p.Ports {
						if allowedPort == port.port {
							port.addr = fmt.Sprintf("%s:%d", port.addr, port.port)
							allowed = append(allowed, port)
							goto NextPort // Break out of nested loops for this port
						}
					}
				}
			}
		}

		// Check specific Services
		if port.service != "" {
			for _, s := range perm.Services {
				if s.Name == port.service {
					for _, allowedPort := range s.Ports {
						if allowedPort == port.port {
							port.addr = fmt.Sprintf("%s:%d", port.addr, port.port)
							allowed = append(allowed, port)
							goto NextPort
						}
					}
				}
			}
		}

	NextPort:
	}

	return allowed
}
