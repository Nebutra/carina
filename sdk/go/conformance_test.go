package sdk

import (
	"os"
	"testing"
)

// TestRealDaemonConformance is opt-in so SDK releases can run the same
// read-only smoke contract against a packaged daemon.
func TestRealDaemonConformance(t *testing.T) {
	socket := os.Getenv("CARINA_CONFORMANCE_SOCKET")
	if socket == "" {
		t.Skip("CARINA_CONFORMANCE_SOCKET is not set")
	}
	c, err := DialPath(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := c.Initialize("carina-sdk-go", "0.6.3")
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion == "" {
		t.Fatal("daemon omitted protocol version")
	}
	if _, err = c.Doctor(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListAgents(""); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListWorkers(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListWorkflows(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListExtensions(); err != nil {
		t.Fatal(err)
	}
}
