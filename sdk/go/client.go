// Package sdk is the Go SDK for the Carina Agent Runtime.
// It re-exports the JSON-RPC client used by carina-cli so external tools and
// CI integrations depend on sdk/go rather than on runtime internals.
package sdk

import (
	"os"
	"path/filepath"

	"github.com/Nebutra/carina/go/rpc"
)

// Client is a JSON-RPC 2.0 client for the carina-daemon unix socket.
type Client = rpc.Client

// DefaultSocketPath returns ~/.carina/daemon.sock.
func DefaultSocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".carina", "daemon.sock"), nil
}

// Dial connects to the daemon at the default socket path.
func Dial() (*Client, error) {
	socket, err := DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	return rpc.Dial(socket)
}

// DialPath connects to the daemon at an explicit socket path.
func DialPath(socketPath string) (*Client, error) {
	return rpc.Dial(socketPath)
}
