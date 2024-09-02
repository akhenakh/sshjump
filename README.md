
# SSHJump

A Kubernetes port forwarder using SSH and a nice TUI.

SSHJump uses SSH public key authentication to validate users and permissions.

## Usage

Use SSH local forward to forward any port from the cluster:

```sh
ssh -L8080:nginx:8080 -p 2222 k8s.cluster.domain.tld
```
If you are authorized sshjump will connect your localhost port 8080 to the first running container named `nginx`.

### Target Selection

You can chose between services and containers using the prefixes `srv` or `container`, if you avoir prefix, it defaults to container.

Will forward to the `nginx` Kubernetes service.
```sh
ssh -L8080:srv.nginx:8080 -p 2222 k8s.cluster.domain.tld
```

Will forward to the first container named `nginx` Kubernetes service.
```sh
ssh -L8080:container.nginx:8080 -p 2222 k8s.cluster.domain.tld
```

You can specify the namespace by prefixing the forward address with the namespace.
```sh
ssh -L8080:myproject.srv.nginx:8080 -p 2222 k8s.cluster.domain.tld
```

## Installation

SSHJump is mainly intended to run from inside a Kubernetes cluster but can be used running outside, simply pointing it to a kube config.

If `KUBE_CONFIG_PATH` env variable is set to a `﻿.kube/config` SSHJump will use it to connect the Kubernetes API.

## Configuration

Example configuration to allow the user `bob` to access `nginx` and `redis` in the `projecta` namespace.
```yaml
version: sshjump.inair.space/v1

permissions:
- username: "bob"
  authorizedKey: "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  namespaces:
  - namespace: "projecta"
    containers:
    - name: "nginx"
      ports:
        - 8080
    services:
    - name: "redis"
      ports:
        - 6379
```

By default SSHJump will deny access to any namespaces if not explicetly mentioned in the `namespaces` list, to let a user access to everything (like in a dev env) use `allowAll: true`

```yaml
version: sshjump.inair.space/v1

permissions:
- username: "bob"
  authorizedKey: "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  allowAll: true
```

## Features


## TODO

- [ ] restrict access to a namespace
- [ ] restrict access to a pod
- [ ] Jumphost ssh
- [ ] TUI
- [ ] OTP
- [ ] logs
- [ ] allow/deny metrics
- [ ] reload config on changes
- [ ] config map example
- [ ] kubernetes example
- [ ] helm example	

