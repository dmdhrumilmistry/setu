package host

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":            "report.pdf",
		"../../etc/passwd":      "passwd",
		`..\..\Windows\win.ini`: "win.ini",
		"/abs/path/file.txt":    "file.txt",
		"..":                    "upload",
		"":                      "upload",
		".bashrc":               "bashrc",
		"CON.txt":               "_CON.txt",
		"nul":                   "_nul",
		"a<b>c:d\"e|f?g*h.txt":  "a_b_c_d_e_f_g_h.txt",
		"bell\x07\nname.txt":    "bellname.txt",
		"evil" + string(rune(0x202e)) + "gnp.exe": "evilgnp.exe", // right-to-left override
		"  spaced .txt ":                 "spaced .txt",
		"photo \U0001F44B.jpg":           "photo \U0001F44B.jpg",
		strings.Repeat("é", 150) + ".go": strings.Repeat("é", 98) + ".go", // 200-byte cap, whole runes, extension kept
	} {
		if got := SanitizeFileName(in); got != want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateUniqueNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	var paths []string
	for range 3 {
		f, path, err := createUnique(dir, "notes.txt")
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(path)
		f.Close()
		paths = append(paths, filepath.Base(path))
	}
	if strings.Join(paths, "|") != "notes.txt|notes (1).txt|notes (2).txt" {
		t.Fatalf("names %v", paths)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); !strings.HasSuffix(string(b), "notes.txt") {
		t.Fatalf("first file was overwritten: %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "notes.txt"))
		dst, _ := os.Stat(dir)
		if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
			t.Fatalf("modes file %v dir %v, want 0600 / 0700", st.Mode().Perm(), dst.Mode().Perm())
		}
	}
}
