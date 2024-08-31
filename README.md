
# SSHJump

A Kubernetes port forwarder using SSH with a nice TUI.

## Features

- [ ] Allow access to a namespace
- [ ] Allow access to a pod
- [ ] Jumphost ssh
- [ ] TUI
- [ ] OTP
- [ ] logs
- [ ] allow deny metrics
	
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
