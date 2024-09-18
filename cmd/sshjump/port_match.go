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

// Allowed filter list of ports using user permissions.
func Allowed(ports Ports, userPerms Permission) Ports {
	var allowed []Port

	for _, port := range ports {
		fullAccess := userPerms.AllowAll
		if fullAccess {
			for _, p := range ports {
				p.addr = fmt.Sprintf("%s:%d", p.addr, p.port)
				allowed = append(allowed, p)
			}

			return allowed
		}

		for _, userNs := range userPerms.Namespaces {
			if userNs.Namespace == port.namespace {
				// check if user got the full access to the namespace no restriction
				if len(userNs.Pods) == 0 {
					port.addr = fmt.Sprintf("%s:%d", port.addr, port.port)
					allowed = append(allowed, port)

					continue
				}

				// check for pods & services
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
						if addr, ok := ports.MatchingService(uService.Name, userNs.Namespace, up); ok || fullAccess {
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
