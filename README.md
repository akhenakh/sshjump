
# SSHJump

WORK IN PROGRESS, NOT READY FOR PRODUCTION

A Kubernetes (first but not only) port forwarder using SSH and a nice TUI.

SSHJump uses SSH public key authentication to validate users and permissions.

![SSH Jump kangaroo logo](img/sshjump512.png?raw=true "SSH Jump logo")

## Why?
- You don't want to give Kubernetes API access to your users for the sole purpose of TCP forward, provisioning a new user with SSHjump is basically adding an SSH key to a YAML file.
- Your Kubernetes cluster may not have its API exposed publically, as a good security measure, attack surface is lowered by just exposing SSHJump access.


## Usage

Use regular SSH local forward to forward any ports from the cluster, providing the namespace and services/pods in the address:

```sh
ssh -L8080:nginx.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```
If you are authorized sshjump will connect your localhost port 8080 to the first running pod named `nginx` the namespace `nginx`.

## Target Selection

### Dynamic with UI
You can use the dynamic host selector:
```sh
ssh -L8080:sshjump:1 -p 2222 myk8s.cluster.domain.tld
```
It will display a UI in the terminal for you to select the target.
Note that the port after the `:` is not important, since it will be set dynamically in the TUI window.

![sshjump ui](/img/term1.png)
![sshjump ui](/img/term2.png)

### Static Target
You can target specific services or pods using the  `svc` or `pod` prefixes, if you don't set a prefix, it defaults to pods.

Will forward to the `nginx` Kubernetes service.
```sh
ssh -L8080:svc.mynamespace.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```

Will forward to the first pod named `nginx` Kubernetes service.
```sh
ssh -L8080:mynamespace.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```

You can specify the namespace by prefixing the forward address with the namespace.
```sh
ssh -L8080:svc.mynamespace.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```

## Installation

SSHJump requires read access to the Kubernetes API to list services and pods.

```sh
kubectl create ns sshjump
kubectl apply -f deployment/sshjump-serviceaccount.yaml
```

A config file with the users SSH keys and host key is passed to SSHJump using a configmap, edit the file to add your users then apply it to Kubernetes.

To generate your host key:
```sh
ssh-keygen -t rsa -f ssh_host_rsa_key -N ""
```

```sh
kubectl apply -f deployment/sshjump-configmap.yaml
```

Deploy the app.

```sh
kubectl apply -f deployment/sshjump-deployment.yaml
```

Finally, you need to open a TCP port to SSHJump (This example use the Gateway API and Envoy):

```sh
kubectl apply -f deployment/sshjump-tcp.yaml
```

### Outside Kubernetes

SSHJump is intended to run from inside a Kubernetes cluster but can be used running outside, simply pointing it to a kube config.

If `KUBE_CONFIG_PATH` env variable is set to a `﻿.kube/config` SSHJump will use it to connect the Kubernetes API.

This is mainly for development purpose and testing.

## Config file

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
        - 8888
    services:
    - name: "redis"
      ports:
        - 6379
```

By default SSHJump will deny access to any namespaces if not explicitly mentioned in the `namespaces` list, to let a user access to everything in any namespaces (like in a dev env) use `allowAll: true`

```yaml
version: sshjump.inair.space/v1

permissions:
- username: "bob"
  authorizedKey: "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  allowAll: true
```

To open access to a full namespace, just list the namespace without pod name.
```yaml
version: sshjump.inair.space/v1

permissions:
- username: "bob"
  authorizedKey: "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  namespaces:
  - namespace: "projecta"
```

## TOTP Two-Factor Authentication (2FA)

SSHJump supports TOTP (Time-based One-Time Password) for two-factor authentication, adding an extra layer of security beyond SSH public key authentication.

### Generating a TOTP Secret

To enable TOTP for a user, first generate a secret:

```sh
./sshjump generate-totp
```

This will output a base32-encoded secret that should be added to your configuration file. The user will need to add this secret to their authenticator app (Google Authenticator, Authy, etc.).

### Configuring TOTP

Add the `totpSecret` field to the user's permission entry in your configuration:

```yaml
version: sshjump.inair.space/v1

permissions:
- username: "bob"
  authorizedKey: "ssh-ed25519 AAAAAasasasasas bob@sponge.net"
  totpSecret: "JBSWY3DPEHPK3PXP"
  namespaces:
  - namespace: "projecta"
    containers:
    - name: "nginx"
      ports:
        - 8080
```

### User Setup

After the administrator adds the TOTP secret to the config, the user should:

1. **Add the secret to their authenticator app:**
   - Manually enter the secret provided by the administrator
   - Or scan a QR code generated from the secret

2. **Connect via SSH:**
   ```sh
   ssh -L8080:svc.projecta.nginx:8080 -p 2222 sshjump.example.com
   ```

3. **Enter the TOTP code:**
   - If the user has TOTP configured, they will be prompted to enter their 6-digit code
   - For interactive TUI mode: a prompt will appear before showing the resource list
   - For direct port-forwarding: the user must first open an interactive session to verify TOTP

**Important:** Once TOTP is verified in a session, port-forwarding works for the duration of that session. If the session disconnects, TOTP must be re-verified.

### Interactive TOTP Verification

If using direct port-forwarding (not the TUI), users must first open an interactive session:

```sh
# First, verify TOTP in an interactive session
ssh -p 2222 sshjump.example.com
# Enter TOTP code when prompted
# Keep this session open

# Then, in another terminal, use port-forwarding
ssh -L8080:svc.projecta.nginx:8080 -p 2222 sshjump.example.com
```

## Tailscale

It's possible to join your tailnet by providing a ts auth key.

Pass the key in a file (from secret or configmaps) using the env variable `TS_AUTHKEY_PATH`.

## Features

- **SSH Public Key Authentication** - Secure authentication using standard SSH keys
- **TOTP Two-Factor Authentication** - Optional 2FA using time-based one-time passwords
- **Dynamic Target Selection** - Interactive TUI for selecting Kubernetes resources
- **Static Port Forwarding** - Direct forwarding to specific pods and services
- **Kubernetes Integration** - Automatic discovery of pods and services
- **Namespace Restrictions** - Fine-grained access control per namespace
- **Config Hot Reload** - Configuration updates without restart
- **Prometheus Metrics** - Connection and tunnel metrics
- **Tailscale Support** - Join your tailnet for secure access


## End to End Testing

```sh  
CONTAINER_RUNTIME=podman go test -tags e2e -v -timeout 5m ./test/e2e/
```
On Linux, with podman you may have to create the cluster manually.
```sh
systemd-run --scope --user -p "Delegate=yes" kind create cluster -n sshjump-e2e
```

Add env `SKIP_TEARDOWN=true` to debug the kind in case of errors.

To reach SSHJump, the private key of the user will be displayed on screen:  
```sh
k -n sshjump port-forward sshjump-55f55c569c-sf8t9 2222:2222
ssh -i test.key -p 2222 -v testuser@localhost 
kind delete  cluster -n sshjump-e2e  
```

## Image Build

This repo is using [`ko`](https://ko.build):
```sh
KO_DOCKER_REPO=ghcr.io/akhenakh/sshjump ko build --platform=linux/amd64,linux/arm64  --bare ./cmd/sshjump
```

There is a `Dockerfile` to be used with Docker & Podman too.

## Community

#sshjump on [Libera Network](https://libera.chat)

## TODO

- [X] restrict access to a namespace
- [X] restrict access to a pod
- [ ] Jumphost ssh
- [X] TUI
- [X] OTP
- [X] logs
- [X] user tunnel connection metric
- [X] allow/deny metrics
- [X] reload config on changes
- [ ] config map example
- [X] kubernetes example
- [X] helm template
- [X] tailscale
- [ ] network policies
- [ ] add a sshsession id for tracking in logs
