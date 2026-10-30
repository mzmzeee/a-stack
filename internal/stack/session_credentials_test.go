package stack

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteCredentialDiscoveryHonorsDeadline(t *testing.T) {
	root := privateTestDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			<-done
			conn.Close()
		}
	}()
	defer func() { close(done); listener.Close() }()

	remoteFile, err := remotePath("stalled")
	if err != nil {
		t.Fatal(err)
	}

	if err = MkdirPrivate(filepath.Dir(remoteFile)); err != nil {
		t.Fatal(err)
	}

	if err = writePrivate(remoteFile, RemoteProfile{Name: "stalled", URL: "https://" + listener.Addr().String(), DatabaseID: opaqueID()}, false); err != nil {
		t.Fatal(err)
	}

	credential := filepath.Join(root, "agent.json")
	if err = writePrivate(credential, Credential{Kind: AgentCapability, Token: opaqueID(), Remote: "stalled"}, false); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()

	_, err = NewClientContext(deadline, filepath.Join(root, "no-local-fallback"), credential, AgentCapability)
	if err == nil {
		t.Fatal("stalled TLS discovery succeeded")
	}

	if time.Since(start) > time.Second {
		t.Fatal("remote discovery exceeded the hook deadline")
	}

	if _, err = os.Stat(filepath.Join(root, "no-local-fallback")); !os.IsNotExist(err) {
		t.Fatal("discovery touched local fallback state")
	}
}
