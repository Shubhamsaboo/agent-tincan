// Theme toggle. Loaded in <head> so a saved choice applies before first paint.
(function () {
  var root = document.documentElement;
  var KEY = 'tincan-theme';

  function saved() {
    try { return localStorage.getItem(KEY); } catch (e) { return null; }
  }
  function current() {
    var t = root.getAttribute('data-theme');
    if (t) return t;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  function label(theme) {
    var btn = document.getElementById('theme-toggle');
    if (btn) {
      var text = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
      btn.setAttribute('aria-label', text);
      btn.setAttribute('title', text);
    }
  }
  function apply(theme) {
    root.setAttribute('data-theme', theme);
    label(theme);
  }

  var s = saved();
  if (s === 'light' || s === 'dark') root.setAttribute('data-theme', s);

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('theme-toggle');
    if (!btn) return;
    label(current());
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var onChange = function () { if (!root.getAttribute('data-theme')) label(current()); };
      if (mq.addEventListener) mq.addEventListener('change', onChange);
    }
    btn.addEventListener('click', function () {
      var next = current() === 'dark' ? 'light' : 'dark';
      apply(next);
      try { localStorage.setItem(KEY, next); } catch (e) {}
    });
  });
})();

// GitHub star count. Shows the count when the API answers; otherwise the
// badge stays as it is, with no count and no error text.
(function () {
  var API = 'https://api.github.com/repos/mvanhorn/agent-tincan';

  function formatCount(n) {
    if (n < 1000) return String(n);
    if (n < 1000000) {
      var k = Math.round(n / 100) / 10;
      if (k < 1000) return (k % 1 === 0 ? k.toFixed(0) : k.toFixed(1)) + 'k';
    }
    var m = Math.round(n / 100000) / 10;
    return (m % 1 === 0 ? m.toFixed(0) : m.toFixed(1)) + 'm';
  }

  function show(count) {
    var text = formatCount(count);
    var slots = document.querySelectorAll('[data-gh-stars]');
    for (var i = 0; i < slots.length; i++) {
      var num = slots[i].querySelector('[data-gh-count]');
      if (num) num.textContent = text;
      slots[i].hidden = false;
    }
  }

  function load() {
    if (!document.querySelector('[data-gh-stars]') || typeof fetch !== 'function') return;
    fetch(API, { credentials: 'omit' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (data && typeof data.stargazers_count === 'number' && data.stargazers_count >= 0) {
          show(data.stargazers_count);
        }
      })
      .catch(function () {});
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', load);
  } else {
    load();
  }
})();
