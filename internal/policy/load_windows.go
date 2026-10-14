package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// RegistryKey is where Group Policy, Intune (ADMX ingestion) and other MDMs
// put the policy. HKLM\SOFTWARE\Policies is writable only by administrators.
const RegistryKey = `SOFTWARE\Policies\setu`

func filePaths() []string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return []string{filepath.Join(pd, "setu", "policy.json")}
}

// loadNative reads HKLM\SOFTWARE\Policies\setu. Scalars are values of that
// key (REG_DWORD for booleans and numbers, REG_SZ for text and durations).
// Lists are REG_MULTI_SZ values or a subkey whose values are the items
// (what ADMX <list> elements write); maps are a subkey of name = value.
func loadNative() (*Policy, error) {
	return loadKey(registry.LOCAL_MACHINE, RegistryKey, `HKLM\`+RegistryKey)
}

// loadKey reads a policy from root\path; src names it in messages.
func loadKey(root registry.Key, path, src string) (*Policy, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("managed policy %s: %w", src, err)
	}
	defer k.Close()

	raw := map[string]any{}
	t := reflect.TypeOf(Policy{})
	known := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		known[strings.ToLower(name)] = true
		v, ok, err := readField(k, name, f.Type)
		if err != nil {
			return nil, fmt.Errorf("managed policy %s\\%s: %w", src, name, err)
		}
		if ok {
			raw[name] = v
		}
	}
	// Unknown values or subkeys are errors, as in the JSON form.
	names, _ := k.ReadValueNames(0)
	subs, _ := k.ReadSubKeyNames(0)
	for _, n := range append(names, subs...) {
		if n != "" && !isGPMarker(n) && !known[strings.ToLower(n)] {
			return nil, fmt.Errorf("managed policy %s: unknown setting %q", src, n)
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return Parse(b, src)
}

func readField(k registry.Key, name string, t reflect.Type) (any, bool, error) {
	switch t.Kind() {
	case reflect.Slice:
		if vals, _, err := k.GetStringsValue(name); err == nil {
			return vals, true, nil
		}
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			return nil, false, nil
		}
		defer sub.Close()
		m, err := readStringValues(sub)
		if err != nil {
			return nil, false, err
		}
		keys := make([]string, 0, len(m))
		for n := range m {
			keys = append(keys, n)
		}
		sortItemNames(keys) // ADMX lists are written as "1", "2", ... "10"
		var out []string
		for _, n := range keys {
			out = append(out, m[n])
		}
		return out, true, nil
	case reflect.Map:
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			return nil, false, nil
		}
		defer sub.Close()
		m, err := readStringValues(sub)
		return m, err == nil, err
	}
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return nil, false, nil // not set
	}
	switch typ {
	case registry.DWORD, registry.QWORD:
		n, _, err := k.GetIntegerValue(name)
		if err != nil {
			return nil, false, err
		}
		if t.Kind() == reflect.Bool {
			return n != 0, true, nil
		}
		return n, true, nil
	case registry.SZ, registry.EXPAND_SZ:
		s, _, err := k.GetStringValue(name)
		if err != nil {
			return nil, false, err
		}
		if t.Kind() == reflect.Bool {
			return s == "1" || strings.EqualFold(s, "true"), true, nil
		}
		return s, true, nil
	}
	return nil, false, fmt.Errorf("unsupported registry type %d", typ)
}

func readStringValues(k registry.Key) (map[string]string, error) {
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, n := range names {
		if isGPMarker(n) {
			continue
		}
		s, _, err := k.GetStringValue(n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		m[n] = s
	}
	return m, nil
}

// checkAdminOwned refuses a policy file whose owner is not an administrator
// account: a user must not be able to plant or edit the policy.
func checkAdminOwned(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	for _, w := range []windows.WELL_KNOWN_SID_TYPE{windows.WinBuiltinAdministratorsSid, windows.WinLocalSystemSid} {
		if owner.IsWellKnown(w) {
			return nil
		}
	}
	if s := owner.String(); s == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" { // TrustedInstaller
		return nil
	}
	return fmt.Errorf("owned by %s, not Administrators or SYSTEM", owner.String())
}

// isGPMarker reports Group Policy bookkeeping values ("**del.Name",
// "**delvals.", "**DeleteValues", ...), which are not settings.
func isGPMarker(name string) bool { return strings.HasPrefix(name, "**") }

// sortItemNames orders list item names numerically when they are numbers
// (so "10" follows "9"), and by name otherwise.
func sortItemNames(names []string) {
	sort.Slice(names, func(i, j int) bool {
		a, aerr := strconv.Atoi(names[i])
		b, berr := strconv.Atoi(names[j])
		if aerr == nil && berr == nil {
			return a < b
		}
		return names[i] < names[j]
	})
}
