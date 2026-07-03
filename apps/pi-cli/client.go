package main

import (
	"io"
	"os"

	"github.com/TsekaLuk/pi-os/go/rpc"
)

type rpcClient = rpc.Client

func dialDaemon() (*rpcClient, error) {
	socket, err := defaultSocketPath()
	if err != nil {
		return nil, err
	}
	return rpc.Dial(socket)
}

func readAllStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
