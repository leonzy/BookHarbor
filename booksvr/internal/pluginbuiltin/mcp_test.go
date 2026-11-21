package pluginbuiltin

import (
	"testing"

	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginproto"
	"talebook/booksvr/internal/pluginruntime"
)

func TestMCPManifestContractAndDefaults(t *testing.T) {
	provider := newMCPProvider()
	manifest := provider.Manifest()
	if _, err := pluginproto.ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if provider.InitialEnabled(nil) {
		t.Fatal("MCP must be opt-in")
	}
	config := provider.InitialConfig(nil)
	if config["allow_remote"] != false || config["allow_write"] != false {
		t.Fatal(config)
	}
	caps := pluginiface.CapabilitySet{Interfaces: map[string]bool{"MCPProvider": true}, HasSelfCheck: true}
	registry := pluginruntime.NewPluginRegistry()
	if _, err := registry.Register(provider, caps); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get(mcp.PluginID); err != nil {
		t.Fatal(err)
	}
}

func TestMCPProviderRejectsGenericMethods(t *testing.T) {
	provider := newMCPProvider()
	if _, err := provider.Invoke("execute", nil, nil); err == nil {
		t.Fatal("generic execute accepted")
	}
}
