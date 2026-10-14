// Applies the saved theme before the page paints (loaded synchronously in
// <head>). Without a saved choice the system preference applies.
(function () {
  try {
    var t = localStorage.getItem('setu-docs-theme');
    if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  } catch (e) { /* storage blocked: follow the system */ }
})();
