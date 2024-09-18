package main

import (
	"reflect"
	"testing"
)

func TestAllowed(t *testing.T) {
	nginxPerm := Permission{
		Namespaces: []Namespace{{
			Namespace: "default",
			Pods: []Pod{{
				Name:  "nginx",
				Ports: []int32{8080},
			}},
			Services: nil,
		}},
		AllowAll: false,
	}

	allowAllPerm := Permission{
		AllowAll: true,
	}

	namespacePerm := Permission{
		Namespaces: []Namespace{{
			Namespace: "default",
		}},
		AllowAll: false,
	}

	tests := []struct {
		name      string
		ports     Ports
		userPerms Permission
		want      Ports
	}{
		{
			"simple path",
			Ports{Port{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10",
			}},
			nginxPerm,
			[]Port{{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10:8080",
			}},
		},

		{
			"simple path empty not matching namespace",
			Ports{Port{
				namespace: "notdefault",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10",
			}},
			nginxPerm,
			nil,
		},

		{
			"simple path empty not matching pod",
			Ports{Port{
				namespace: "default",
				pod:       "notnginx",
				port:      8080,
				addr:      "10.16.0.10",
			}},
			nginxPerm,
			nil,
		},

		{
			"simple path empty not matching port",
			Ports{Port{
				namespace: "default",
				pod:       "nginx",
				port:      8081,
				addr:      "10.16.0.10",
			}},
			nginxPerm,
			nil,
		},

		{
			"simple path allow all",
			Ports{Port{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10",
			}},
			allowAllPerm,
			[]Port{{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10:8080",
			}},
		},

		{
			"full access to namespace",
			Ports{Port{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10",
			}},
			namespacePerm,
			[]Port{{
				namespace: "default",
				pod:       "nginx",
				port:      8080,
				addr:      "10.16.0.10:8080",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allowed(tt.ports, tt.userPerms); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Allowed() = %v, want %v", got, tt.want)
			}
		})
	}
}
