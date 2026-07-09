// fakedaemon is a minimal go/rpc server used only by
// go/tui/conn_reconnect_test.go to drive Connect()'s reconnect state machine
// against a real OS process: killing this process (not just closing a
// listener in-process) closes its socket connections the way an actual
// daemon crash or restart would, which an in-process fake server cannot
// reproduce (rpc.Server.Close only stops accepting new connections).
//
// Protocol: session.create returns {"session_id": "sess_1"};
// session.events.stream subscribes the caller and then replays, as
// notifications, one JSON object per line read from the file named by the
// CARINA_FAKEDAEMON_EVENTS env var (each line a JSON notification params
// object with a "type" field), polling the file for new lines so a test can
// append events after the subscription is live.
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	sock := os.Getenv("CARINA_FAKEDAEMON_SOCKET")
	if sock == "" {
		log.Fatal("fakedaemon: CARINA_FAKEDAEMON_SOCKET is required")
	}
	eventsPath := os.Getenv("CARINA_FAKEDAEMON_EVENTS")

	s := rpc.NewServer()
	s.Register("session.create", func(_ json.RawMessage) (any, error) {
		return map[string]any{"session_id": "sess_1"}, nil
	})
	s.RegisterStream("session.events.stream", func(_ json.RawMessage, sub *rpc.Subscription) error {
		if eventsPath == "" {
			return nil
		}
		go tailEvents(eventsPath, sub)
		return nil
	})

	if err := s.ListenUnix(sock); err != nil {
		log.Fatalf("fakedaemon: %v", err)
	}
}

// tailEvents polls eventsPath for new lines and publishes each as an "event"
// notification, so the test can append lines after the client subscribed.
func tailEvents(path string, sub *rpc.Subscription) {
	var offset int64
	for {
		time.Sleep(20 * time.Millisecond)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, 0); err != nil {
			f.Close()
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			offset += int64(len(line)) + 1
			if len(line) == 0 {
				continue
			}
			var params json.RawMessage = append([]byte(nil), line...)
			if err := sub.Notify("event", params); err != nil {
				f.Close()
				return
			}
		}
		f.Close()
	}
}
