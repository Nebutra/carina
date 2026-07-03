package main

import (
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
