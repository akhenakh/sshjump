package main

import "github.com/gliderlabs/ssh"

type SSHJumpConfig struct {
	Version     string       `yaml:"version"`     // version for the config file
	Permissions []Permission `yaml:"permissions"` // users and permissions
}

type Application struct {
	Name  string `yaml:"name"`
	Ports []int  `yaml:"ports"`
}

type Namespace struct {
	Namespace    string        `yaml:"namespace"`
	Applications []Application `yaml:"applications"`
}

type Permission struct {
	Username      string        `yaml:"username"`
	AuthorizedKey string        `yaml:"key"`
	Key           ssh.PublicKey `yaml:"-"`
	Namespaces    []Namespace   `yaml:"namespaces"`
}
