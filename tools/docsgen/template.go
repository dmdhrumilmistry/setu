package main

// pageTemplate is the layout of every docs page. Everything is same-origin
// (the web client lives next door and handles secrets), so the CSP allows
// only this site's own scripts, styles and search index.
const pageTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'">
<meta name="referrer" content="no-referrer">
<title>{{.Page.Title}} - setu docs</title>
<meta name="description" content="{{.Page.Description}}">
<meta name="color-scheme" content="light dark">
<link rel="icon" href="assets/logo.svg" type="image/svg+xml">
<link rel="stylesheet" href="assets/style.css">
<script src="assets/theme.js"></script>
<script src="assets/docs.js" defer></script>
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="topbar">
  <button class="icon-btn nav-toggle" id="nav-toggle" aria-controls="sidebar" aria-expanded="false" aria-label="Open navigation">
    <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M3 6h18M3 12h18M3 18h18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
  </button>
  <a class="brand" href="index.html"><img src="assets/logo.svg" alt="" width="26" height="26"><span>setu</span><small>docs</small></a>
  <div class="search" role="search">
    <svg class="search-icon" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><circle cx="11" cy="11" r="7" fill="none" stroke="currentColor" stroke-width="2"/><path d="M20 20l-3.5-3.5" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
    <input id="search" type="search" placeholder="Search the docs" aria-label="Search the docs" autocomplete="off" spellcheck="false">
    <kbd class="search-key" aria-hidden="true">/</kbd>
    <ul id="search-results" class="search-results" role="listbox" aria-label="Search results" hidden></ul>
  </div>
  <nav class="toplinks" aria-label="Site">
    <a href="../">Web client</a>
    <a href="{{.Repo}}">GitHub</a>
  </nav>
  <button class="icon-btn" id="theme-toggle" aria-label="Toggle dark mode" title="Toggle dark mode">
    <svg class="sun" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><circle cx="12" cy="12" r="4.5" fill="none" stroke="currentColor" stroke-width="2"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
    <svg class="moon" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/></svg>
  </button>
</header>
<div class="layout">
  <nav id="sidebar" class="sidebar" aria-label="Documentation">
    {{range .Nav}}<div class="nav-section">
      <p class="nav-title">{{.Title}}</p>
      <ul>{{range .Pages}}
        <li><a href="{{.Out}}"{{if eq .Out $.Page.Out}} aria-current="page"{{end}}>{{.Title}}</a></li>{{end}}
      </ul>
    </div>
    {{end}}
  </nav>
  <div class="scrim" id="scrim" hidden></div>
  <main id="main" tabindex="-1">
    <article class="content">
{{.Page.HTML}}
    </article>
    <nav class="pager" aria-label="Previous and next page">
      {{with .Prev}}<a class="prev" href="{{.Out}}"><span>Previous</span>{{.Title}}</a>{{else}}<span></span>{{end}}
      {{with .Next}}<a class="next" href="{{.Out}}"><span>Next</span>{{.Title}}</a>{{end}}
    </nav>
    <footer class="footer">
      <a href="{{.EditURL}}">Edit this page on GitHub</a>
      <span>setu is open source. Peer-to-peer terminal sharing with no server of your own.</span>
    </footer>
  </main>
  {{if .Page.TOC}}<aside class="toc" aria-label="On this page">
    <p class="toc-title">On this page</p>
    <ul>{{range .Page.TOC}}
      <li class="l{{.Level}}"><a href="#{{.ID}}">{{.Text}}</a></li>{{end}}
    </ul>
  </aside>{{end}}
</div>
</body>
</html>
`
