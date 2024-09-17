
# SSHJump

WORK IN PROGRESS, NOT READY FOR PRODUCTION

A Kubernetes (first but not only) port forwarder using SSH and a nice TUI.

SSHJump uses SSH public key authentication to validate users and permissions.

![SSH Jump kangaroo logo](img/sshjump512.png?raw=true "SSH Jump logo")

## Why?
- You don't want to give Kubernetes API access to your users for the sole purpose of TCP forward, provisioning a new user with SSHjump is basically adding a user and a key.
- Your Kubernetes cluster may not have its API exposed publically, as a good security measure, attack surface is lowered by just exposing SSHJump access.


## Usage

Use regular SSH local forward to forward any ports from the cluster, providing the namespace and services/pods in the address:

```sh
ssh -L8080:argocd.argocd-server:8080 -p 2222 myk8s.cluster.domain.tld
```
If you are authorized sshjump will connect your localhost port 8080 to the first running pod named `nginx` the namespace `nginx`.

### Target Selection

You can choose between services and pods using the prefixes `srv` or `pod`, if you don't set a prefix, it defaults to pods.

Will forward to the `nginx` Kubernetes service.
```sh
ssh -L8080:srv.nginx.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```

Will forward to the first pod named `nginx` Kubernetes service.
```sh
ssh -L8080:nginx.nginx:8080 -p 2222 myk8s.cluster.domain.tld
```

You can specify the namespace by prefixing the forward address with the namespace.
```sh
ssh -L8080:srv.mynamespace.nginx:8080 -p 2222 myk8s.cluster.domain.tld
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

Finally you need to open a TCP port to SSHJump (This example use the Gateway API and Envoy):

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
## Tailscale

It's possible to join your tailnet by providing a ts auth key.

Pass the key in a file (from secret or configmaps) using the env variable `TS_AUTHKEY_PATH`.

## Features




## Image Build

This repo is using [`ko`](https://ko.build):
```sh
KO_DOCKER_REPO=ghcr.io/akhenakh/sshjump ko build --platform=linux/amd64,linux/arm64  --bare ./cmd/sshjump
```

There is a `Dockerfile` to be used with Docker & Podman too.

## Community

#sshjump on [Libera Network](https://libera.chat)

## TODO

- [ ] restrict access to a namespace
- [ ] restrict access to a pod
- [ ] Jumphost ssh
- [ ] TUI
- [ ] OTP
- [X] logs
- [X] user tunnel connection metric
- [ ] allow/deny metrics
- [ ] reload config on changes
- [ ] config map example
- [X] kubernetes example
- [ ] helm example
- [X] tailscale
- [ ] network policies