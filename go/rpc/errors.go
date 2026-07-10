package rpc

import "errors"

// ErrDaemonUnreachable is the sentinel a caller can errors.Is() against to
// detect a dial failure, instead of string-matching the "(is the daemon
// running? ...)" suffix Dial's error message already carries (P1.5(b)).
// client.go's Dial wraps this sentinel alongside the underlying
// net.DialTimeout error via a multi-%w fmt.Errorf, so both the sentinel and
// the human-readable hint survive in the returned error's chain.
var ErrDaemonUnreachable = errors.New("daemon unreachable")

// ErrSocketInUse is the sentinel ListenUnix returns when another live
// process already holds the cross-process advisory lock for the socket
// path (P1.8 startup discipline): two `carina` invocations racing to
// auto-start carina-daemon on a fresh machine must not both bind the same
// socket path — the second ListenUnix must fail loudly instead of
// os.Remove-ing the first instance's live socket out from under it.
var ErrSocketInUse = errors.New("rpc: socket already in use by another daemon instance")

// ErrCallTimeout is the sentinel Client.Call returns when a request's
// response does not arrive within the client's call timeout (P1.8/P1.6): a
// diagnostic tool like `carina doctor` must fail fast and observably if the
// daemon accepts the connection but its handler (or the kernel child
// process behind it) is wedged, rather than hang forever with no exit code
// and no partial report.
var ErrCallTimeout = errors.New("rpc: call timed out waiting for a response")
