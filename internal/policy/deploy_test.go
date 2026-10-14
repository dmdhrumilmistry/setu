package policy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The files in deploy/mdm are what administrators push to devices; keep
// them parseable, consistent with each other and with the Policy schema.

const deployDir = "../../deploy/mdm"

func readDeploy(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func policyKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Policy{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

func examplePolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := Parse(readDeploy(t, "policy.example.json"), "example")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// plistValue converts one decoded plist element to a JSON-compatible value.
func plistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "true", "false":
		return start.Name.Local == "true", dec.Skip()
	case "string", "integer", "real":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		if start.Name.Local == "string" {
			return s, nil
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	case "array":
		var out []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch el := tok.(type) {
			case xml.StartElement:
				v, err := plistValue(dec, el)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			case xml.EndElement:
				return out, nil
			}
		}
	case "dict":
		out := map[string]any{}
		key := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch el := tok.(type) {
			case xml.StartElement:
				if el.Name.Local == "key" {
					if err := dec.DecodeElement(&key, &el); err != nil {
						return nil, err
					}
					continue
				}
				v, err := plistValue(dec, el)
				if err != nil {
					return nil, err
				}
				out[key] = v
			case xml.EndElement:
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("unsupported plist element %s", start.Name.Local)
}

func TestDeployExampleJSON(t *testing.T) {
	examplePolicy(t)
}

func TestDeployMobileconfig(t *testing.T) {
	dec := xml.NewDecoder(bytes.NewReader(readDeploy(t, "macos/setu.mobileconfig")))
	dec.Strict = true
	var root any
	for root == nil {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("mobileconfig: %v", err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "dict" {
			if root, err = plistValue(dec, el); err != nil {
				t.Fatal(err)
			}
		}
	}
	outer := root.(map[string]any)
	if outer["PayloadType"] != "Configuration" || outer["PayloadScope"] != "System" {
		t.Errorf("profile must be a device-scoped Configuration: %v %v", outer["PayloadType"], outer["PayloadScope"])
	}
	payload := outer["PayloadContent"].([]any)[0].(map[string]any)
	if payload["PayloadType"] != Domain {
		t.Fatalf("payload type %v, want %s", payload["PayloadType"], Domain)
	}
	b, _ := json.Marshal(payload)
	p, err := parseProfilePrefs(b, "mobileconfig")
	if err != nil {
		t.Fatal(err)
	}
	if p.Hash() != examplePolicy(t).Hash() {
		t.Error("macos/setu.mobileconfig differs from policy.example.json")
	}
}

func TestDeployADMX(t *testing.T) {
	admx := readDeploy(t, "windows/setu.admx")
	adml := readDeploy(t, "windows/en-US/setu.adml")
	for name, doc := range map[string][]byte{"admx": admx, "adml": adml} {
		dec := xml.NewDecoder(bytes.NewReader(doc))
		dec.Strict = true
		for {
			if _, err := dec.Token(); err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("%s is not well-formed: %v", name, err)
			}
		}
	}
	keys := policyKeys()
	// Every registry value or list subkey the template writes is a setting.
	for _, m := range regexp.MustCompile(`valueName="([^"]+)"`).FindAllStringSubmatch(string(admx), -1) {
		if !keys[m[1]] {
			t.Errorf("ADMX writes unknown value %q", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`key="Software\\Policies\\setu\\([^"]+)"`).FindAllStringSubmatch(string(admx), -1) {
		if !keys[m[1]] {
			t.Errorf("ADMX writes unknown list %q", m[1])
		}
	}
	if regexp.MustCompile(`key="(?i:software\\policies\\microsoft)`).Match(admx) {
		t.Error("ingested ADMX may not write under Software\\Policies\\Microsoft")
	}
	// Every string, presentation and element the ADMX references exists.
	defined := func(re string) map[string]bool {
		out := map[string]bool{}
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(adml), -1) {
			out[m[1]] = true
		}
		return out
	}
	strs, pres := defined(`<string id="([^"]+)"`), defined(`<presentation id="([^"]+)"`)
	for _, m := range regexp.MustCompile(`\$\((string|presentation)\.([^)]+)\)`).FindAllStringSubmatch(string(admx), -1) {
		if (m[1] == "string" && !strs[m[2]]) || (m[1] == "presentation" && !pres[m[2]]) {
			t.Errorf("ADML lacks %s.%s", m[1], m[2])
		}
	}
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`<(?:text|decimal|boolean|list|enum) id="([^"]+)"`).FindAllStringSubmatch(string(admx), -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`refId="([^"]+)"`).FindAllStringSubmatch(string(adml), -1) {
		if !ids[m[1]] {
			t.Errorf("ADML presentation refers to unknown element %q", m[1])
		}
	}
}

func TestDeployReg(t *testing.T) {
	keys := policyKeys()
	sc := bufio.NewScanner(bytes.NewReader(readDeploy(t, "windows/setu-policy.reg")))
	section := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "["):
			section = strings.Trim(line, "[]")
			const base = `HKEY_LOCAL_MACHINE\SOFTWARE\Policies\setu`
			if !strings.HasPrefix(section, base) {
				t.Errorf("reg file writes outside %s: %s", base, section)
			}
			if sub := strings.TrimPrefix(strings.TrimPrefix(section, base), `\`); sub != "" && !keys[sub] {
				t.Errorf("reg file has unknown list %q", sub)
			}
		case strings.HasPrefix(line, `"`) && !strings.Contains(section, `setu\`):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, `"`), `"`)
			if !keys[name] {
				t.Errorf("reg file sets unknown value %q", name)
			}
		}
	}
}

func TestDeployVaanarSena(t *testing.T) {
	y := string(readDeploy(t, "vaanarsena/setu.yaml"))
	// The embedded ADMX must be the shipped one.
	const marker = "          data: |\n"
	i := strings.Index(y, marker)
	if i < 0 {
		t.Fatal("no embedded ADMX")
	}
	var body []string
	for _, line := range strings.Split(y[i+len(marker):], "\n") {
		if line != "" && !strings.HasPrefix(line, "            ") {
			break
		}
		body = append(body, strings.TrimPrefix(line, "            "))
	}
	got := strings.TrimRight(strings.Join(body, "\n"), "\n")
	want := strings.TrimRight(string(readDeploy(t, "windows/setu.admx")), "\n")
	if got != want {
		t.Error("vaanarsena/setu.yaml embeds a different ADMX than windows/setu.admx; regenerate it")
	}
	// Apple payload keys and Windows OMA-URI policies must exist.
	keys := policyKeys()
	for _, m := range regexp.MustCompile(`(?m)^            ([A-Za-z]+):`).FindAllStringSubmatch(y, -1) {
		if m[1] != "PayloadType" && !keys[m[1]] {
			t.Errorf("Apple payload sets unknown key %q", m[1])
		}
	}
	admx := string(readDeploy(t, "windows/setu.admx"))
	for _, m := range regexp.MustCompile(`setu~Policy~Setu/([A-Za-z]+)`).FindAllStringSubmatch(y, -1) {
		if !strings.Contains(admx, `<policy name="`+m[1]+`"`) {
			t.Errorf("OMA-URI targets unknown ADMX policy %q", m[1])
		}
	}
	for _, line := range strings.Split(y, "\n") {
		if strings.Contains(line, "data:") && strings.Contains(line, "&#xF000;") {
			t.Errorf("VaanarSena XML-escapes data: use the literal U+F000 separator, not &#xF000;: %s", line)
		}
	}
	if !strings.Contains(y, "1\\uF000claude\\uF000") {
		t.Error("AllowedCommands list should use the \\uF000 separator")
	}
}
