
# SSHJump

A Kubernetes port forwarder using SSH with a nice TUI.

## Features

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

## Configuration

```yaml
version: sshjump.inair.space/v1

permissions:
- username: "akh"
  authorizedKey:
  - "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  namespaces:
  - namespace: "default"
    containers:
    - name: "nginx"
      ports:
        - 8080
    services:
    - name: "redis"
      ports:
        - 6379
```
