// setu docs: theme toggle, mobile navigation, copy buttons, heading anchors,
// "on this page" highlighting and client-side search. No dependencies.
(function () {
  'use strict';
  var root = document.documentElement;

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) { /* ignore */ }
  }

  // Theme toggle: flips between light and dark, remembering the choice.
  var themeBtn = document.getElementById('theme-toggle');
  if (themeBtn) {
    themeBtn.addEventListener('click', function () {
      var dark = root.dataset.theme === 'dark' ||
        (!root.dataset.theme && window.matchMedia('(prefers-color-scheme: dark)').matches);
      root.dataset.theme = dark ? 'light' : 'dark';
      store('setu-docs-theme', root.dataset.theme);
    });
  }

  // Mobile navigation drawer.
  var navBtn = document.getElementById('nav-toggle');
  var scrim = document.getElementById('scrim');
  function setNav(open) {
    document.body.classList.toggle('nav-open', open);
    if (navBtn) navBtn.setAttribute('aria-expanded', String(open));
    if (scrim) scrim.hidden = !open;
  }
  if (navBtn) navBtn.addEventListener('click', function () { setNav(!document.body.classList.contains('nav-open')); });
  if (scrim) scrim.addEventListener('click', function () { setNav(false); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') setNav(false); });

  // Copy buttons on code blocks.
  document.querySelectorAll('.content pre').forEach(function (pre) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy';
    btn.textContent = 'Copy';
    btn.setAttribute('aria-label', 'Copy code to clipboard');
    btn.addEventListener('click', function () {
      var code = pre.querySelector('code') || pre;
      var text = code.innerText.replace(/\n$/, '');
      var done = function () {
        btn.textContent = 'Copied';
        btn.classList.add('done');
        setTimeout(function () { btn.textContent = 'Copy'; btn.classList.remove('done'); }, 1600);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done, function () {});
      }
    });
    pre.appendChild(btn);
  });

  // Anchor links on headings.
  document.querySelectorAll('.content h2[id], .content h3[id], .content h4[id]').forEach(function (h) {
    var a = document.createElement('a');
    a.className = 'anchor';
    a.href = '#' + h.id;
    a.textContent = '#';
    a.setAttribute('aria-label', 'Link to this section');
    h.appendChild(a);
  });

  // External links open safely in a new tab.
  document.querySelectorAll('.content a[href^="http"]').forEach(function (a) {
    if (a.host !== location.host) {
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
    }
  });

  // Highlight the current section in "On this page".
  var tocLinks = Array.prototype.slice.call(document.querySelectorAll('.toc a'));
  if (tocLinks.length && 'IntersectionObserver' in window) {
    var byId = {};
    tocLinks.forEach(function (a) { byId[decodeURIComponent(a.hash.slice(1))] = a; });
    var visible = {};
    var obs = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { visible[e.target.id] = e.isIntersecting; });
      var heads = document.querySelectorAll('.content h2[id], .content h3[id]');
      var current = null;
      for (var i = 0; i < heads.length; i++) {
        if (visible[heads[i].id]) { current = heads[i].id; break; }
        if (heads[i].getBoundingClientRect().top < 120) current = heads[i].id;
      }
      tocLinks.forEach(function (a) { a.classList.remove('active'); });
      if (current && byId[current]) byId[current].classList.add('active');
    }, { rootMargin: '-60px 0px -60% 0px' });
    document.querySelectorAll('.content h2[id], .content h3[id]').forEach(function (h) { obs.observe(h); });
  }

  // Search: loads search-index.json on first use and ranks matches.
  var input = document.getElementById('search');
  var list = document.getElementById('search-results');
  if (!input || !list) return;
  var index = null;
  var loading = null;
  var selected = -1;

  function load() {
    if (index || loading) return loading;
    loading = fetch('search-index.json', { credentials: 'same-origin' })
      .then(function (r) { return r.json(); })
      .then(function (data) { index = data; return data; })
      .catch(function () { index = []; return index; });
    return loading;
  }

  function escapeHTML(s) {
    return s.replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function highlight(s, terms) {
    var out = escapeHTML(s);
    terms.forEach(function (t) {
      var re = new RegExp('(' + t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'ig');
      out = out.replace(re, '<mark>$1</mark>');
    });
    return out;
  }

  function snippet(text, terms) {
    var lower = text.toLowerCase();
    var at = -1;
    for (var i = 0; i < terms.length && at < 0; i++) at = lower.indexOf(terms[i]);
    var start = Math.max(0, at - 50);
    if (start > 0) { // begin at a word, not mid-word
      var sp = text.indexOf(' ', start);
      if (sp >= 0 && sp < at) start = sp + 1;
    }
    var end = Math.min(text.length, start + 150);
    if (end < text.length) { var e2 = text.lastIndexOf(' ', end); if (e2 > at) end = e2; }
    return (start > 0 ? '... ' : '') + text.slice(start, end) + (end < text.length ? ' ...' : '');
  }

  // Ranking: every term must match somewhere. Section headings weigh most,
  // the whole query as a phrase earns a bonus, page titles count a little
  // (so a page named "Claude Code" does not win every search for "code").
  function search(q) {
    var phrase = q.toLowerCase().trim().replace(/\s+/g, ' ');
    var terms = phrase.split(' ').filter(Boolean);
    if (!terms.length || !index) return [];
    var results = [];
    index.forEach(function (e) {
      var title = (e.s || e.p).toLowerCase();
      var page = e.p.toLowerCase();
      var text = (e.t || '').toLowerCase();
      var score = 0;
      for (var i = 0; i < terms.length; i++) {
        var t = terms[i];
        var s = 0;
        if (title.indexOf(t) >= 0) s += 10;
        if (page.indexOf(t) >= 0) s += 1;
        if (text.indexOf(t) >= 0) s += 1 + Math.min(3, text.split(t).length - 1) * 0.5;
        if (!s) return;
        score += s;
      }
      if (terms.length > 1) {
        if (title.indexOf(phrase) >= 0) score += 25;
        else if (text.indexOf(phrase) >= 0) score += 6;
      }
      if (!e.s && title.indexOf(phrase) === 0) score += 5; // page itself
      results.push({ e: e, score: score });
    });
    results.sort(function (a, b) { return b.score - a.score; });
    return results.slice(0, 10).map(function (r) { return r.e; });
  }

  function render() {
    var q = input.value.trim();
    selected = -1;
    if (!q) { list.hidden = true; list.innerHTML = ''; return; }
    var terms = q.toLowerCase().split(/\s+/).filter(Boolean);
    var hits = search(q);
    if (!hits.length) {
      list.innerHTML = '<li class="empty">No results for "' + escapeHTML(q) + '"</li>';
    } else {
      list.innerHTML = hits.map(function (e, i) {
        var title = e.s ? highlight(e.s, terms) + ' <span class="r-page">- ' + escapeHTML(e.p) + '</span>' : highlight(e.p, terms);
        return '<li role="option" id="sr-' + i + '"><a href="' + escapeHTML(e.u) + '"><span class="r-title">' + title +
          '</span><span class="r-text">' + highlight(snippet(e.t || '', terms), terms) + '</span></a></li>';
      }).join('');
    }
    list.hidden = false;
  }

  function move(delta) {
    var items = list.querySelectorAll('li[role="option"]');
    if (!items.length) return;
    if (selected >= 0) items[selected].removeAttribute('aria-selected');
    selected = (selected + delta + items.length) % items.length;
    items[selected].setAttribute('aria-selected', 'true');
    items[selected].scrollIntoView({ block: 'nearest' });
  }

  input.addEventListener('focus', load);
  input.addEventListener('input', function () { load().then(render); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
    else if (e.key === 'Enter') {
      var items = list.querySelectorAll('li[role="option"] a');
      var target = items[selected >= 0 ? selected : 0];
      if (target) { e.preventDefault(); location.href = target.getAttribute('href'); list.hidden = true; }
    } else if (e.key === 'Escape') { list.hidden = true; input.blur(); }
  });
  document.addEventListener('click', function (e) {
    if (!list.contains(e.target) && e.target !== input) list.hidden = true;
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === '/' && document.activeElement !== input && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) {
      e.preventDefault();
      input.focus();
    }
  });
})();
