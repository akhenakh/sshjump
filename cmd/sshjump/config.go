package main

import "github.com/gliderlabs/ssh"

// SSHJumpConfig represents the configuration for the SSH jump server.
type SSHJumpConfig struct {
	Version     string       `yaml:"version"`     // Version specifies the version for the config file.
	Permissions []Permission `yaml:"permissions"` // Permissions is a list of users and their permissions.
}

// Pod represents a Kubernetes pod (multiple containers).
type Pod struct {
	Name  string  `yaml:"name"`  // Name matching containers
	Ports []int32 `yaml:"ports"` // Ports is a list of ports allowed.
}

type Service struct {
	Name  string  `yaml:"name"`  // Name matching service
	Ports []int32 `yaml:"ports"` // Ports is a list of ports allowed.
}

// Namespace represents a group of containers within a specific namespace.
type Namespace struct {
	Namespace string    `yaml:"namespace"` // Namespace is the name of the namespace.
	Pods      []Pod     `yaml:"pods"`      // Pods is a list of Pods allowed in the namespace.
	Services  []Service `yaml:"services"`
}

// Permission represents the access permissions for a single user.
type Permission struct {
	Username      string        `yaml:"username"` // Username is the SSH username for the user.
	AuthorizedKey string        `yaml:"key"`      // AuthorizedKey is the authorized key for the user.
	Key           ssh.PublicKey `yaml:"-"`
	Namespaces    []Namespace   `yaml:"namespaces"` // Namespaces is a list of namespaces the user has access to.
	AllowAll      bool          `yaml:"allowAll"`   // allow this user to connect to every detected ports
}
