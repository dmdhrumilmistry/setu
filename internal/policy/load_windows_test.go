package policy

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// TestRegistryLayout writes the layout Group Policy / ADMX ingestion
// produces to a scratch key under HKCU and reads it back.
func TestRegistryLayout(t *testing.T) {
	const path = `Software\setu-policy-test`
	_ = registry.DeleteKey(registry.CURRENT_USER, path+`\AllowedCommands`)
	_ = registry.DeleteKey(registry.CURRENT_USER, path+`\ResourceAttributes`)
	_ = registry.DeleteKey(registry.CURRENT_USER, path)
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, path+`\AllowedCommands`)
		_ = registry.DeleteKey(registry.CURRENT_USER, path+`\ResourceAttributes`)
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
	})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(k.SetDWordValue("RequirePassword", 1))
	must(k.SetDWordValue("MaxClients", 2))
	must(k.SetStringValue("MaxKeyValidity", "168h"))
	must(k.SetStringValue("AuditEndpoint", "https://otel.corp.example"))
	must(k.SetStringsValue("AllowedRelays", []string{"wss://a.example", "wss://b.example"}))
	list, _, err := registry.CreateKey(k, "AllowedCommands", registry.ALL_ACCESS)
	must(err)
	for i := 1; i <= 11; i++ { // 11 items: "10" must sort after "9"
		must(list.SetStringValue(strconv.Itoa(i), fmt.Sprintf("cmd%d", i)))
	}
	must(list.SetStringValue("**delvals.", " ")) // Group Policy marker
	list.Close()
	attrs, _, err := registry.CreateKey(k, "ResourceAttributes", registry.ALL_ACCESS)
	must(err)
	must(attrs.SetStringValue("device.asset_tag", "LAP-0042"))
	must(k.SetStringValue("**del.AuditCAFile", " ")) // Group Policy marker
	attrs.Close()

	p, err := loadKey(registry.CURRENT_USER, path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !p.RequirePassword || p.MaxClients != 2 || p.MaxKeyValidity.D() != 168*time.Hour || p.AuditEndpoint != "https://otel.corp.example" {
		t.Errorf("scalars %+v", p)
	}
	if len(p.AllowedRelays) != 2 || len(p.AllowedCommands) != 11 || p.AllowedCommands[0] != "cmd1" || p.AllowedCommands[10] != "cmd11" {
		t.Errorf("lists relays=%v commands=%v", p.AllowedRelays, p.AllowedCommands)
	}
	if p.ResourceAttributes["device.asset_tag"] != "LAP-0042" {
		t.Errorf("map %v", p.ResourceAttributes)
	}

	must(k.SetDWordValue("DisableShre", 1)) // typo: must fail closed
	if _, err := loadKey(registry.CURRENT_USER, path, "test"); err == nil {
		t.Error("unknown registry value accepted")
	}
}
