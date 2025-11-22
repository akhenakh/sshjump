//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	clusterName   = "sshjump-e2e"
	imageName     = "inair.space/sshjump:e2e"
	namespace     = "sshjump"
	localPort     = "2223" // Port mapped locally via kubectl port-forward
	targetSvcPort = "80"
)

// Detect runtime: prefer podman if installed and docker is missing, or use env override
var containerRuntime = getContainerRuntime()

func getContainerRuntime() string {
	if v := os.Getenv("CONTAINER_RUNTIME"); v != "" {
		return v
	}
	// if podman exists and docker doesn't, default to podman
	_, errDocker := exec.LookPath("docker")
	_, errPodman := exec.LookPath("podman")
	if errDocker != nil && errPodman == nil {
		return "podman"
	}
	return "docker"
}

func TestE2E(t *testing.T) {
	t.Logf("Using container runtime: %s", containerRuntime)

	// Setup Environment
	setupCluster(t)
	defer teardownCluster(t)

	// Build and Load Image
	buildAndLoadImage(t)

	// Generate Keys
	userPrivKey, userPubKey, userPrivKeyPEM := generateSSHKeys(t)
	hostPrivKey, _, _ := generateSSHKeys(t)

	// PRINT THE KEY FOR MANUAL USAGE
	t.Logf("\n--- USER PRIVATE KEY ---\n%s\n------------------------", userPrivKeyPEM)

	// Deploy Resources
	deployKubernetesResources(t, userPubKey, hostPrivKey)

	// Setup Port Forwarding to access the Service from Host
	stopPF := startPortForward(t)
	defer stopPF()

	t.Run("SSH Handshake Success", func(t *testing.T) {
		config := &ssh.ClientConfig{
			User: "testuser",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(userPrivKey),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}

		conn, err := ssh.Dial("tcp", "localhost:"+localPort, config)
		if err != nil {
			t.Fatalf("Failed to dial sshjump: %v", err)
		}
		defer conn.Close()
	})

	t.Run("SSH Handshake Failure (Wrong Key)", func(t *testing.T) {
		wrongKey, _, _ := generateSSHKeys(t)
		config := &ssh.ClientConfig{
			User: "testuser",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(wrongKey),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}

		conn, err := ssh.Dial("tcp", "localhost:"+localPort, config)
		if err == nil {
			conn.Close()
			t.Fatal("Expected authentication failure, but got success")
		}
	})

	t.Run("TCP Forwarding to Nginx Service", func(t *testing.T) {
		config := &ssh.ClientConfig{
			User: "testuser",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(userPrivKey),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}

		client, err := ssh.Dial("tcp", "localhost:"+localPort, config)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		// Attempt to dial through the tunnel to the nginx service in the cluster.
		// The server logic expects "svc.namespace.servicename" for services.
		target := fmt.Sprintf("svc.%s.nginx:%s", namespace, targetSvcPort)

		conn, err := client.Dial("tcp", target)
		if err != nil {
			t.Fatalf("Failed to dial through tunnel to %s: %v", target, err)
		}
		defer conn.Close()

		// Perform a raw HTTP request over the SSH tunnel
		fmt.Fprintf(conn, "GET / HTTP/1.0\r\n\r\n")

		// Read response
		var buf bytes.Buffer
		io.Copy(&buf, conn)

		if !strings.Contains(buf.String(), "Welcome to nginx!") {
			t.Errorf("Expected Nginx welcome message, got: %s", buf.String())
		}
	})
}

func runCmd(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	// Pass through environment for KIND_EXPERIMENTAL_PROVIDER
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Command failed: %s %s\nOutput: %s\nError: %v", name, strings.Join(args, " "), string(out), err)
	}
}

func setupCluster(t *testing.T) {
	t.Log("Creating Kind cluster...")

	// If using podman, we must instruct Kind to use it
	if containerRuntime == "podman" {
		os.Setenv("KIND_EXPERIMENTAL_PROVIDER", "podman")
	}

	// Check if cluster exists first to speed up local dev
	cmd := exec.Command("kind", "get", "clusters")
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), clusterName) {
		t.Log("Cluster already exists")
		return
	}

	runCmd(t, "kind", "create", "cluster", "--name", clusterName)
}

func teardownCluster(t *testing.T) {
	if os.Getenv("SKIP_TEARDOWN") == "true" {
		return
	}
	t.Log("Deleting Kind cluster...")
	runCmd(t, "kind", "delete", "cluster", "--name", clusterName)
}

func buildAndLoadImage(t *testing.T) {
	t.Logf("Building image %s with %s...", imageName, containerRuntime)

	cmd := exec.Command(containerRuntime, "build", "-t", imageName, "-f", "../../Dockerfile", "../../")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build image: %v\n%s", err, string(out))
	}

	// Save to Archive
	// This is the compatibility layer. kind load docker-image works poorly with Podman directly.
	// Saving to a tarball and loading the archive is universal.
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "image.tar")

	t.Log("Saving image to archive...")
	var saveCmd *exec.Cmd

	if containerRuntime == "podman" {
		// Podman needs docker-archive format for Kind compatibility
		saveCmd = exec.Command(containerRuntime, "save", "--format=docker-archive", "-o", archivePath, imageName)
	} else {
		saveCmd = exec.Command(containerRuntime, "save", "-o", archivePath, imageName)
	}

	if out, err := saveCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to save image archive: %v\n%s", err, string(out))
	}

	// Load Archive into Kind
	t.Log("Loading archive into Kind...")
	runCmd(t, "kind", "load", "image-archive", archivePath, "--name", clusterName)
}

func generateSSHKeys(t *testing.T) (ssh.Signer, string, string) {
	//  Generate RSA Key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	//  Generate Private Key PEM (This is what you want for ssh -i)
	// We use PKCS#1 format which is the standard "BEGIN RSA PRIVATE KEY"
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	privateKeyPEM := string(privPEM)

	// Create SSH Signer (for the Go client)
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	// Generate Public Key (for authorized_keys)
	pubKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubKeyStr := string(ssh.MarshalAuthorizedKey(pubKey))

	// Returns: Signer, AuthorizedKey format, PrivateKey PEM format
	return signer, strings.TrimSpace(pubKeyStr), privateKeyPEM
}

func deployKubernetesResources(t *testing.T, userPubKey string, hostPrivKey ssh.Signer) {
	t.Log("Deploying Kubernetes resources...")

	// Create Namespace
	kubectlApply(t, `
apiVersion: v1
kind: Namespace
metadata:
  name: `+namespace)

	// Create ConfigMap with permissions and host key
	// Re-doing key gen for Host to get PEM string for the configmap
	rawPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rawPriv)})

	configMap := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: sshjump-config
  namespace: %s
data:
  ssh_host_rsa_key: |
%s
  sshjump.yaml: |
    version: sshjump.inair.space/v1
    permissions:
    - username: "testuser"
      key: "%s"
      namespaces:
      - namespace: "%s"
        services:
        - name: "nginx"
          ports:
            - %s
`, namespace, indent(string(privPEM), 4), userPubKey, namespace, targetSvcPort)

	kubectlApply(t, configMap)

	// RBAC
	kubectlApply(t, fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sshjump
  namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sshjump-role
rules:
- apiGroups: [""]
  resources: ["pods", "services"]
  verbs: ["list", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sshjump-binding
subjects:
- kind: ServiceAccount
  name: sshjump
  namespace: %s
roleRef:
  kind: ClusterRole
  name: sshjump-role
  apiGroup: rbac.authorization.k8s.io
`, namespace, namespace))

	// Deployment (SSHJump)
	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sshjump
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sshjump
  template:
    metadata:
      labels:
        app: sshjump
    spec:
      serviceAccountName: sshjump
      containers:
      - name: sshjump
        image: %s
        imagePullPolicy: Never
        env:
        - name: CONFIG_PATH
          value: /app/config/sshjump.yaml
        - name: PRIVATE_KEY_PATH
          value: /app/config/ssh_host_rsa_key
        ports:
        - containerPort: 2222
        volumeMounts:
        - name: config-vol
          mountPath: /app/config
      volumes:
      - name: config-vol
        configMap:
          name: sshjump-config
`, namespace, imageName))

	// Deployment (Target Nginx)
	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx
  namespace: %s
spec:
  selector:
    app: nginx
  ports:
  - protocol: TCP
    port: 80
    targetPort: 80
`, namespace, namespace))

	// Service (SSHJump)
	kubectlApply(t, fmt.Sprintf(`
apiVersion: v1
kind: Service
metadata:
  name: sshjump
  namespace: %s
spec:
  selector:
    app: sshjump
  ports:
  - protocol: TCP
    port: 2222
    targetPort: 2222
`, namespace))

	// Wait for deployments
	t.Log("Waiting for pods to be ready...")
	runCmd(t, "kubectl", "wait", "--for=condition=available", "--timeout=120s", "deployment/sshjump", "-n", namespace)
	runCmd(t, "kubectl", "wait", "--for=condition=available", "--timeout=60s", "deployment/nginx", "-n", namespace)
}

func kubectlApply(t *testing.T, yamlContent string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yamlContent)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to apply yaml: %s\nError: %v", string(out), err)
	}
}

func startPortForward(t *testing.T) func() {
	t.Log("Starting port-forward...")
	// Using the service directly
	cmd := exec.Command("kubectl", "port-forward", "-n", namespace, "svc/sshjump", localPort+":2222")

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start port-forward: %v", err)
	}

	// Wait for port to be open
	ready := false
	for i := 0; i < 20; i++ {
		conn, err := net.Dial("tcp", "localhost:"+localPort)
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !ready {
		_ = cmd.Process.Kill()
		t.Fatalf("Port forward did not become ready in time")
	}

	return func() {
		_ = cmd.Process.Kill()
	}
}

func indent(s string, n int) string {
	lines := strings.Split(s, "\n")
	pad := strings.Repeat(" ", n)
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}
