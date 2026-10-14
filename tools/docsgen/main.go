// Command docsgen renders the documentation site that GitHub Pages serves at
// https://dmdhrumilmistry.github.io/setu/docs/.
//
// Pages are written in Markdown under docs/src (plus docs/DESIGN.md) and
// rendered to static HTML in docs/, together with a search index. The
// output is committed, because Pages deploys the main branch as is.
//
//	cd tools/docsgen
//	go run .           # regenerate docs/*.html and docs/search-index.json
//	go run . -check    # CI: fail if the output is stale, a link is broken,
//	                   # or a CLI flag is missing from the reference
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

const repoURL = "https://github.com/dmdhrumilmistry/setu"

type page struct {
	Src, Out, Title string
}

type section struct {
	Title string
	Pages []page
}

// nav is the site structure, in reading order.
var nav = []section{
	{"Get started", []page{
		{"src/index.md", "index.html", "Overview"},
		{"src/install.md", "install.html", "Install"},
		{"src/quickstart.md", "quickstart.html", "Quick start"},
	}},
	{"Guides", []page{
		{"src/sharing.md", "sharing.html", "Sharing a terminal"},
		{"src/joining.md", "joining.html", "Joining a session"},
		{"src/claude-code.md", "claude-code.html", "Claude Code from your phone"},
		{"src/service.md", "service.html", "Always-on service"},
		{"src/audit.md", "audit.html", "Audit logging"},
	}},
	{"Administration", []page{
		{"src/mdm.md", "mdm.html", "MDM and managed policy"},
		{"src/security.md", "security.html", "Security model"},
	}},
	{"Reference", []page{
		{"src/cli.md", "cli.html", "CLI reference"},
		{"src/troubleshooting.md", "troubleshooting.html", "Troubleshooting and FAQ"},
		{"DESIGN.md", "design.html", "Design and protocol"},
	}},
}

type tocEntry struct {
	Level int
	ID    string
	Text  string
}

type searchEntry struct {
	Page    string `json:"p"`
	Section string `json:"s"`
	URL     string `json:"u"`
	Text    string `json:"t"`
}

type rendered struct {
	page
	HTML        template.HTML
	TOC         []tocEntry
	Description string
	ids         map[string]bool
	links       []string // internal links to validate
	search      []searchEntry
}

func main() {
	root := flag.String("root", "../..", "repository root")
	check := flag.Bool("check", false, "verify the committed output instead of writing it")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	docs := filepath.Join(root, "docs")
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()), // our own content: allows layout HTML
	)
	// Source base name -> output file. Repository files that have a docs
	// counterpart map to it, so DESIGN.md's links keep working on the site.
	outMap := map[string]string{"SECURITY.md": "security.html", "README.md": "index.html"}
	for _, s := range nav {
		for _, p := range s.Pages {
			outMap[filepath.Base(p.Src)] = p.Out
		}
	}

	var pages []*rendered
	for _, s := range nav {
		for _, p := range s.Pages {
			src, err := os.ReadFile(filepath.Join(docs, p.Src))
			if err != nil {
				return err
			}
			r, err := render(md, p, src, outMap)
			if err != nil {
				return fmt.Errorf("%s: %w", p.Src, err)
			}
			pages = append(pages, r)
		}
	}
	if err := checkLinks(pages); err != nil {
		return err
	}

	tpl, err := template.New("page").Parse(pageTemplate)
	if err != nil {
		return err
	}
	outputs := map[string][]byte{}
	var index []searchEntry
	for i, r := range pages {
		var prev, next *rendered
		if i > 0 {
			prev = pages[i-1]
		}
		if i+1 < len(pages) {
			next = pages[i+1]
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, map[string]any{
			"Page": r, "Nav": nav, "Prev": prev, "Next": next, "Repo": repoURL,
			"EditURL": repoURL + "/edit/main/docs/" + r.Src,
		}); err != nil {
			return err
		}
		outputs[r.Out] = buf.Bytes()
		index = append(index, r.search...)
	}
	idx, err := json.Marshal(index)
	if err != nil {
		return err
	}
	outputs["search-index.json"] = append(idx, '\n')

	if !check {
		for name, b := range outputs {
			if err := os.WriteFile(filepath.Join(docs, name), b, 0o644); err != nil {
				return err
			}
		}
		fmt.Printf("wrote %d files to %s\n", len(outputs), docs)
		return nil
	}

	var problems []string
	names := make([]string, 0, len(outputs))
	for n := range outputs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		cur, err := os.ReadFile(filepath.Join(docs, n))
		if err != nil || !bytes.Equal(bytes.ReplaceAll(cur, []byte("\r\n"), []byte("\n")), outputs[n]) {
			problems = append(problems, "stale or missing: docs/"+n)
		}
	}
	existing, _ := filepath.Glob(filepath.Join(docs, "*.html"))
	for _, f := range existing {
		if _, ok := outputs[filepath.Base(f)]; !ok {
			problems = append(problems, "not generated (remove it or add it to nav): docs/"+filepath.Base(f))
		}
	}
	missing, err := undocumentedFlags(root, filepath.Join(docs, "src", "cli.md"))
	if err != nil {
		return err
	}
	problems = append(problems, missing...)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n  ") + "\nrun: cd tools/docsgen && go run .")
	}
	fmt.Println("docs are up to date")
	return nil
}

func render(md goldmark.Markdown, p page, src []byte, outMap map[string]string) (*rendered, error) {
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	doc := md.Parser().Parse(text.NewReader(src))
	r := &rendered{page: p, ids: map[string]bool{}}

	var (
		cur     = searchEntry{Page: p.Title, URL: p.Out}
		curText strings.Builder
		flush   = func() {
			if t := strings.TrimSpace(curText.String()); t != "" || cur.Section != "" {
				e := cur
				e.Text = truncate(collapse(t), 400)
				r.search = append(r.search, e)
			}
			curText.Reset()
		}
		firstPara = true
	)
	var linkErr error
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			id := attr(n, "id")
			r.ids[id] = true
			title := plain(n, src)
			if n.Level == 1 {
				return ast.WalkSkipChildren, nil
			}
			if n.Level <= 3 {
				r.TOC = append(r.TOC, tocEntry{Level: n.Level, ID: id, Text: title})
			}
			if n.Level == 2 || n.Level == 3 {
				flush()
				cur = searchEntry{Page: p.Title, Section: title, URL: p.Out + "#" + id}
			}
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph:
			t := plain(n, src)
			if firstPara && n.Parent() == doc {
				r.Description = truncate(collapse(t), 160)
				firstPara = false
			}
		case *ast.Link:
			dest := string(n.Destination)
			if fixed, internal, err := rewriteLink(dest, outMap); err != nil {
				linkErr = errors.Join(linkErr, err)
			} else {
				n.Destination = []byte(fixed)
				if internal {
					r.links = append(r.links, fixed)
				}
			}
		case *ast.Text:
			curText.Write(n.Segment.Value(src))
			curText.WriteByte(' ')
		case *ast.CodeBlock, *ast.FencedCodeBlock:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				curText.Write(seg.Value(src))
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	if linkErr != nil {
		return nil, linkErr
	}
	flush()
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return nil, err
	}
	r.HTML = template.HTML(buf.String())
	return r, nil
}

var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

// rewriteLink maps foo.md#x to foo.html#x. External links pass through.
func rewriteLink(dest string, outMap map[string]string) (string, bool, error) {
	if dest == "" || schemeRE.MatchString(dest) || strings.HasPrefix(dest, "//") {
		return dest, false, nil
	}
	if strings.HasPrefix(dest, "#") {
		return dest, true, nil
	}
	path, frag, _ := strings.Cut(dest, "#")
	if strings.HasSuffix(path, ".md") {
		out, ok := outMap[filepath.Base(path)]
		if !ok {
			return "", false, fmt.Errorf("link to %s: not a docs page (link to GitHub instead)", dest)
		}
		path = out
	}
	if frag != "" {
		return path + "#" + frag, true, nil
	}
	return path, true, nil
}

func checkLinks(pages []*rendered) error {
	byOut := map[string]*rendered{}
	for _, p := range pages {
		byOut[p.Out] = p
	}
	var errs []error
	for _, p := range pages {
		for _, l := range p.links {
			path, frag, _ := strings.Cut(l, "#")
			target := p
			if path != "" {
				t, ok := byOut[path]
				if !ok {
					if strings.HasPrefix(path, "../") || strings.HasPrefix(path, "assets/") {
						continue // the web client, or an asset
					}
					errs = append(errs, fmt.Errorf("%s: broken link %s", p.Src, l))
					continue
				}
				target = t
			}
			if frag != "" && !target.ids[frag] {
				errs = append(errs, fmt.Errorf("%s: link %s: no heading #%s in %s", p.Src, l, frag, target.Src))
			}
		}
	}
	return errors.Join(errs...)
}

// undocumentedFlags runs the real CLI's help and reports flags that
// docs/src/cli.md never mentions.
func undocumentedFlags(root, cliDoc string) ([]string, error) {
	doc, err := os.ReadFile(cliDoc)
	if err != nil {
		return nil, err
	}
	internal := map[string]bool{"session-dir": true, "no-console": true}
	flagRE := regexp.MustCompile(`(?m)^\s+-([a-z][a-z0-9-]*)`)
	var missing []string
	for _, args := range [][]string{
		{"share"}, {"join"}, {"service", "add"}, {"service", "rotate"},
		{"service", "audit"}, {"service", "install"}, {"service", "run"},
	} {
		cmd := exec.Command("go", append(append([]string{"run", "./cmd/setu"}, args...), "-h")...)
		cmd.Dir = root
		out, _ := cmd.CombinedOutput() // -h exits non-zero
		found := flagRE.FindAllStringSubmatch(string(out), -1)
		if len(found) == 0 {
			return nil, fmt.Errorf("setu %s -h printed no flags:\n%s", strings.Join(args, " "), out)
		}
		for _, m := range found {
			if internal[m[1]] {
				continue
			}
			if !regexp.MustCompile(`--?` + regexp.QuoteMeta(m[1]) + `\b`).Match(doc) {
				missing = append(missing, fmt.Sprintf("cli.md does not document `setu %s --%s`", strings.Join(args, " "), m[1]))
			}
		}
	}
	return missing, nil
}

func attr(n ast.Node, name string) string {
	if v, ok := n.AttributeString(name); ok {
		if b, ok := v.([]byte); ok {
			return string(b)
		}
	}
	return ""
}

// plain returns the text content of a node.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
			if c.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(c.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if i := strings.LastIndexByte(s[:n], ' '); i > n/2 {
		return s[:i] + " ..."
	}
	return s[:n] + "..."
}
