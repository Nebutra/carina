package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestProtocolEventCatalogMatchesSchemaEnum(t *testing.T) {
	root := repoRootFromHere(t)
	var catalog struct {
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "events", "events.json"), &catalog)

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "schemas", "event.schema.json"), &schema)
	enum := map[string]bool{}
	for _, name := range schema.Properties["type"].Enum {
		enum[name] = true
	}
	for _, typ := range catalog.Types {
		if !enum[typ.Name] {
			t.Fatalf("event %q exists in events.json but not event.schema.json enum", typ.Name)
		}
	}
}

type protocolMethod struct {
	Method            string          `json:"method"`
	Scope             rpc.Scope       `json:"scope"`
	Remote            bool            `json:"remote"`
	Stream            bool            `json:"stream,omitempty"`
	DynamicScope      bool            `json:"dynamic_scope,omitempty"`
	ControlPlaneWrite bool            `json:"control_plane_write,omitempty"`
	Conditional       string          `json:"conditional,omitempty"`
	Params            json.RawMessage `json:"params"`
	Result            json.RawMessage `json:"result"`
}

func TestProtocolMethodRegistryMatchesDaemonBidirectionally(t *testing.T) {
	root := repoRootFromHere(t)
	var methods struct {
		APIs map[string][]protocolMethod `json:"apis"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "jsonrpc", "methods.json"), &methods)

	catalog := make(map[string]protocolMethod)
	for group, records := range methods.APIs {
		for _, record := range records {
			if record.Method == "" || record.Scope == "" || len(record.Params) == 0 || len(record.Result) == 0 {
				t.Fatalf("protocol group %s contains incomplete record: %+v", group, record)
			}
			if !rpc.ValidScope(record.Scope) {
				t.Fatalf("protocol method %s has invalid scope %q", record.Method, record.Scope)
			}
			if _, duplicate := catalog[record.Method]; duplicate {
				t.Fatalf("protocol method %s is registered more than once", record.Method)
			}
			catalog[record.Method] = record
		}
	}

	base := &Daemon{server: rpc.NewServer()}
	base.registerMethods()
	baseMethods := descriptorMap(base.server.MethodDescriptors())
	if _, ok := baseMethods["gateway.token.issue"]; ok {
		t.Fatal("gateway.token.issue must not register without a signing key")
	}

	withConditional := &Daemon{server: rpc.NewServer(), gatewayTokens: &rpc.GatewayTokenIssuer{}}
	withConditional.registerMethods()
	actual := descriptorMap(withConditional.server.MethodDescriptors())

	var missing, fictional, mismatched []string
	for name, desc := range actual {
		record, ok := catalog[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		if desc.Scope != record.Scope || desc.Remote != record.Remote || desc.Stream != record.Stream ||
			desc.DynamicScope != record.DynamicScope || desc.ControlPlaneWrite != record.ControlPlaneWrite || !desc.Advertise {
			mismatched = append(mismatched, name)
		}
	}
	for name := range catalog {
		if _, ok := actual[name]; !ok {
			fictional = append(fictional, name)
		}
	}
	for _, names := range [][]string{missing, fictional, mismatched} {
		sort.Strings(names)
	}
	if len(missing) > 0 || len(fictional) > 0 || len(mismatched) > 0 {
		t.Fatalf("protocol registry drift: missing=%v fictional=%v descriptor_mismatch=%v", missing, fictional, mismatched)
	}
	for name, record := range catalog {
		_, registeredByDefault := baseMethods[name]
		if record.Conditional == "" && !registeredByDefault {
			t.Fatalf("unconditional protocol method %s is not registered by default", name)
		}
		if record.Conditional != "" && registeredByDefault {
			t.Fatalf("conditional protocol method %s unexpectedly registers by default", name)
		}
	}
	conditional := catalog["gateway.token.issue"]
	if conditional.Conditional != "gateway_token_signing_key_file" {
		t.Fatalf("gateway.token.issue conditional marker = %q", conditional.Conditional)
	}
}

func descriptorMap(descriptors []rpc.MethodDescriptor) map[string]rpc.MethodDescriptor {
	out := make(map[string]rpc.MethodDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		out[descriptor.Method] = descriptor
	}
	return out
}

func readProtocolJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
