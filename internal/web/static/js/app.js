// xusage app core: XU global (fetch/state/format/instant-load), hash router,
// topbar wiring, clock, and the /api/pulse poller (SPEC §9.1 + §13).
//
// The shell (index.html) is static chrome — this file never rebuilds the nav;
// it fills the mount points (brand tag, clock, provider chip, updated stamp)
// and toggles .active on .nav a[data-view]. Views register themselves via
// XU.registerView(name, {mount, unmount}).
//
// §13 additions over the frozen §9.1 surface: fmt.big, XU.pill, XU.pulse /
// XU.onPulse, a vendor key in XU.state (emitted by XU.q), the GSAP instant-
// load layer (XU.cacheGet/cacheSet/swr/prefetch), the #clock ticker, and the
// controls-strip binding (range/mode/tz segs, provider chip, export, rescan).

(function () {
  'use strict';

  // ---- state ---------------------------------------------------------------

  var DEFAULT_STATE = {
    range: '30d',   // 1d | 3d | 7d | 30d | 90d | all
    since: '',      // YYYY-MM-DD, used when range == 'custom'
    until: '',
    tz: 'local',    // local | utc
    mode: 'auto',   // auto | display | calculate
    project: '',
    session: '',
    model: '',
    provider: '',
    vendor: ''
  };

  function loadState() {
    try {
      var raw = localStorage.getItem('xu.state');
      if (raw) { return JSON.parse(raw); }
    } catch (e) { /* private mode etc. */ }
    return {};
  }

  var state = Object.assign({}, DEFAULT_STATE, loadState());

  var stateListeners = [];
  var listenerGen = 0;

  function setState(patch) {
    Object.assign(state, patch);
    try { localStorage.setItem('xu.state', JSON.stringify(state)); } catch (e) {}
    renderProviderChip();
    syncControls();
    for (var i = 0; i < stateListeners.length; ) {
      var sub = stateListeners[i];
      if (sub.gen < listenerGen) {
        stateListeners.splice(i, 1);
        continue;
      }
      i++;
      try { sub.fn(); } catch (e) { /* a broken view must not stop the rest */ }
    }
    if (patch.provider !== undefined) { updateProvChip(); }
  }

  function onState(fn) {
    var sub = { fn: fn, gen: listenerGen };
    stateListeners.push(sub);
    return function () {
      var i = stateListeners.indexOf(sub);
      if (i >= 0) { stateListeners.splice(i, 1); }
    };
  }

  // ---- query helper ---------------------------------------------------------

  function q(extra) {
    var eff = Object.assign({}, state, extra || {});
    var params = [];
    var since = eff.since, until = eff.until;
    if (!since && !until && eff.range !== 'all') {
      var r = rangeBounds(eff.range);
      if (r) { since = r.since; until = r.until; }
    }
    if (since) { params.push('since=' + encodeURIComponent(since)); }
    if (until) { params.push('until=' + encodeURIComponent(until)); }
    if (eff.tz === 'utc') { params.push('tz=UTC'); }
    if (eff.mode && eff.mode !== 'auto') { params.push('mode=' + encodeURIComponent(eff.mode)); }
    ['project', 'session', 'model', 'provider', 'vendor'].forEach(function (k) {
      if (eff[k]) { params.push(k + '=' + encodeURIComponent(eff[k])); }
    });
    return params.length ? '?' + params.join('&') : '';
  }

  // rangeBounds maps a named range to concrete since/until dates (local time).
  // GSAP ranges: 1d | 3d | 7d | 30d | 90d | all.
  function rangeBounds(range) {
    function pad(x) { return String(x).padStart(2, '0'); }
    function fmt(d) {
      return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
    }
    var now = new Date();
    switch (range) {
      case '1d': { var t = new Date(now); return { since: fmt(t), until: fmt(t) }; }
      case '3d': { var a = new Date(now); a.setDate(a.getDate() - 2); return { since: fmt(a), until: '' }; }
      case '7d': { var b = new Date(now); b.setDate(b.getDate() - 6); return { since: fmt(b), until: '' }; }
      case '30d': { var c = new Date(now); c.setDate(c.getDate() - 29); return { since: fmt(c), until: '' }; }
      case '90d': { var d = new Date(now); d.setDate(d.getDate() - 89); return { since: fmt(d), until: '' }; }
      case 'ytd': return { since: now.getFullYear() + '-01-01', until: '' };
      default: return null; // all / custom-without-dates
    }
  }

  // ---- instant-load layer (SPEC §13.0.1) -------------------------------------
  // Stale-while-revalidate localStorage cache. cacheKey = viewName + ':' + q().

  var CACHE_TTL_MS = 5 * 60 * 1000; // 5 min
  var CACHE_MAX_BYTES = 1024 * 1000; // 1 MB cap
  var CACHE_PREFIX = 'xu.swr:';

  function cacheGet(key) {
    try {
      var raw = localStorage.getItem(CACHE_PREFIX + key);
      if (!raw) { return null; }
      var entry = JSON.parse(raw);
      if (!entry || !entry.t || typeof entry.t !== 'number') { return null; }
      if (Date.now() - entry.t > CACHE_TTL_MS) { return null; }
      return entry.d;
    } catch (e) { return null; }
  }

  function cacheSet(key, data) {
    try {
      var payload = JSON.stringify({ t: Date.now(), d: data });
      if (payload.length > CACHE_MAX_BYTES) { return false; } // too big: skip
      localStorage.setItem(CACHE_PREFIX + key, payload);
      return true;
    } catch (e) { return false; } // private mode / quota
  }

  // swr(cacheKey, loadFn, renderFn): paint cached synchronously, then refetch.
  //   (a) var c = cacheGet(key); if (c) renderFn(c, true)  -> INSTANT paint
  //   (b) loadFn() resolves -> if no cache or JSON changed, cacheSet + renderFn(fresh, false)
  function swr(cacheKey, loadFn, renderFn) {
    var cached = cacheGet(cacheKey);
    if (cached) {
      try { renderFn(cached, true); } catch (e) { /* view render is best-effort */ }
    }
    var fresh = null;
    return Promise.resolve()
      .then(function () { return loadFn(); })
      .then(function (data) {
        fresh = data;
        if (!cached) {
          cacheSet(cacheKey, data);
          try { renderFn(data, false); } catch (e) {}
          return;
        }
        // Only repaint when the payload actually changed (avoids flicker).
        var changed = false;
        try { changed = JSON.stringify(cached) !== JSON.stringify(data); } catch (e) {}
        if (changed) {
          cacheSet(cacheKey, data);
          try { renderFn(data, false); } catch (e) {}
        }
      })
      .then(function () { return fresh; });
  }

  // prefetch fires a set of GETs in parallel to warm connections at boot.
  function prefetch(urls) {
    if (!Array.isArray(urls)) { return Promise.resolve([]); }
    return Promise.all(urls.map(function (url) {
      return fetchJSON(url).catch(function () { return null; });
    }));
  }

  // ---- fetch -----------------------------------------------------------------

  function fetchJSON(path) {
    return fetch(path, { headers: { Accept: 'application/json' } }).then(function (res) {
      if (!res.ok) {
        return res.text().then(function (body) {
          var msg = body;
          try { msg = JSON.parse(body).error || body; } catch (e) {}
          throw { status: res.status, message: String(msg || res.statusText) };
        });
      }
      return res.json();
    }, function (err) {
      throw { status: 0, message: err && err.message ? err.message : 'network error' };
    });
  }

  function postJSON(path) {
    return fetch(path, { method: 'POST' }).then(function (res) {
      if (!res.ok) { throw { status: res.status, message: res.statusText }; }
      return res.json();
    });
  }

  // ---- formatting ---------------------------------------------------------------

  var fmt = {};

  fmt.num = function (n) {
    if (n == null || isNaN(n)) { return '0'; }
    return Number(n).toLocaleString('en-US');
  };

  fmt.tokens = function (n) {
    n = Number(n) || 0;
    var abs = Math.abs(n);
    if (abs >= 1e12) { return trim1(n / 1e12) + 'T'; }
    if (abs >= 1e9) { return trim1(n / 1e9) + 'B'; }
    if (abs >= 1e6) { return trim1(n / 1e6) + 'M'; }
    if (abs >= 1e3) { return trim1(n / 1e3) + 'k'; }
    return String(Math.round(n));
  };

  function trim1(x) {
    var r = Math.round(x * 10) / 10;
    if (Math.abs(r) >= 100 || r === Math.floor(r)) { return String(Math.round(r)); }
    return String(r);
  }

  // fmt.big — compact volume figures (SPEC §13.3):
  // 987 → "987", 1234567 → "1.23M", 1834000000 → "1.83B".
  fmt.big = function (n) {
    n = Number(n) || 0;
    var sign = n < 0 ? '-' : '';
    var abs = Math.abs(n);
    if (abs < 1e6) { return sign + String(Math.round(abs)); }
    var x, suffix;
    if (abs >= 1e12) { x = abs / 1e12; suffix = 'T'; }
    else if (abs >= 1e9) { x = abs / 1e9; suffix = 'B'; }
    else { x = abs / 1e6; suffix = 'M'; }
    return sign + x.toFixed(2).replace(/\.?0+$/, '') + suffix;
  };

  fmt.cost = function (n, precise) {
    n = Number(n) || 0;
    var abs = Math.abs(n);
    if (precise || abs < 10) { return '$' + n.toFixed(abs >= 100 ? 0 : 2); }
    if (abs >= 1000) { return '$' + fmt.tokens(Math.round(n)); }
    return '$' + n.toFixed(0);
  };

  fmt.pct = function (n) {
    n = Number(n) || 0;
    return (n >= 10 ? Math.round(n) : n.toFixed(1)) + '%';
  };

  fmt.date = function (iso) {
    if (!iso || iso.length < 10) { return iso || ''; }
    var y = iso.slice(0, 4), m = iso.slice(5, 7), d = iso.slice(8, 10);
    var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
                  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
    return months[Number(m) - 1] + ' ' + d + ', ' + y;
  };

  fmt.datetime = function (iso) {
    if (!iso) { return ''; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return String(iso); }
    var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
                  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
    var pad = function (x) { return String(x).padStart(2, '0'); };
    return months[d.getMonth()] + ' ' + d.getDate() + ' ' +
      pad(d.getHours()) + ':' + pad(d.getMinutes());
  };

  fmt.duration = function (sec) {
    sec = Number(sec) || 0;
    if (sec < 60) { return Math.round(sec) + 's'; }
    var m = Math.floor(sec / 60);
    var h = Math.floor(m / 60);
    m = m % 60;
    if (h === 0) { return m + 'm'; }
    var d = Math.floor(h / 24);
    h = h % 24;
    if (d === 0) { return h + 'h ' + m + 'm'; }
    return d + 'd ' + h + 'h';
  };

  // GSAP categorical palette (§13.0): brand green first, then the taxonomy
  // hues (never reused across categories): orange, pink, lilac, blue.
  var CATEGORICAL = ['#0ae448', '#abff84', '#ff8709', '#fec5fb', '#9d95ff', '#00bae2',
                     '#7c7c6f', '#42433d'];

  var colors = {
    categorical: CATEGORICAL.slice(),
    series: function (i) { return CATEGORICAL[i % CATEGORICAL.length]; },
    accent: '#0ae448',
    muted: '#7c7c6f'
  };

  // colorFor assigns a stable categorical color per label across views.
  var colorMap = {};
  var nextColor = 0;
  function colorFor(label) {
    if (!(label in colorMap)) { colorMap[label] = colors.series(nextColor++); }
    return colorMap[label];
  }

  // ---- DOM helpers (shared, not part of the frozen API) ------------------------

  function el(tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) { node.className = cls; }
    if (text != null) { node.textContent = text; }
    return node;
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function skeletonRows(container, rows, cols) {
    container.innerHTML = '';
    var wrap = el('div');
    for (var i = 0; i < rows; i++) {
      var line = el('div', 'row');
      line.style.marginBottom = '8px';
      for (var c = 0; c < cols; c++) {
        var sk = el('div', 'skeleton');
        sk.style.height = '14px';
        sk.style.width = (60 + ((i * 7 + c * 13) % 25)) + '%';
        sk.style.flex = c === 0 ? '2' : '1';
        line.appendChild(sk);
      }
      wrap.appendChild(line);
    }
    container.appendChild(wrap);
  }

  function csvEscape(v) {
    var s = String(v == null ? '' : v);
    if (/[",\n]/.test(s)) { s = '"' + s.replace(/"/g, '""') + '"'; }
    return s;
  }

  function downloadCSV(filename, header, rows) {
    var lines = [header.map(csvEscape).join(',')];
    rows.forEach(function (r) { lines.push(r.map(csvEscape).join(',')); });
    var blob = new Blob([lines.join('\r\n')], { type: 'text/csv;charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 500);
  }

  // ---- pill attribution (§13.4) -------------------------------------------------

  function pill(cli, model) {
    var out = '';
    if (cli) {
      out += '<span class="pill pill-cli" title="' + esc(cli) + '">' +
        '<i>' + esc(String(cli).charAt(0)) + '</i>' + esc(cli) + '</span>';
    }
    if (model) {
      out += '<span class="pill pill-model" title="' + esc(model) + '">' +
        esc(model) + '</span>';
    }
    return out;
  }

  // ---- toast plumbing -----------------------------------------------------------

  var toastTimer = null;
  function toast(msg) {
    var t = document.getElementById('toast');
    if (!t) { return; }
    t.textContent = msg;
    t.classList.add('show');
    if (toastTimer) { clearTimeout(toastTimer); }
    toastTimer = setTimeout(function () { t.classList.remove('show'); }, 2600);
  }

  // ---- router & views -------------------------------------------------------------

  var VIEW_LABELS = {
    overview: 'Overview', leaderboard: 'Leaderboard', models: 'Models',
    clis: 'CLIs', providers: 'Providers', projects: 'Projects',
    sessions: 'Sessions', series: 'Series', blocks: 'Blocks', live: 'Live', share: 'Share'
  };

  var VIEWS = ['overview', 'leaderboard', 'models', 'clis', 'providers',
               'projects', 'sessions', 'series', 'blocks', 'live', 'share'];

  var views = {};
  var mounted = null;
  var currentView = 'overview';

  function registerView(name, def) { views[name] = def; }

  function parseHash() {
    var h = location.hash.replace(/^#\/?/, '');
    var parts = h.split('?');
    return parts[0].split('/')[0] || 'overview';
  }

  function route() {
    listenerGen++;
    var name = parseHash();
    var view = views[name];
    var content = document.getElementById('view');

    if (mounted && mounted.unmount) {
      try { mounted.unmount(); } catch (e) { /* teardown is best-effort */ }
    }
    mounted = null;

    markActive(name);
    currentView = name;
    if (!content) { return; }

    if (!view) {
      content.innerHTML = '';
      content.appendChild(el('div', 'empty',
        'Unknown view "' + name + '". Use the top bar navigation.'));
      document.title = 'xusage';
      return;
    }

    document.title = 'xusage — ' + (VIEW_LABELS[name] || name);
    content.innerHTML = '';
    // entrance animation
    content.classList.remove('anim');
    void content.offsetWidth;
    content.classList.add('anim');
    view.mount(content);
    mounted = view;
  }

  function markActive(name) {
    var links = document.querySelectorAll('.nav a');
    for (var i = 0; i < links.length; i++) {
      links[i].classList.toggle('active',
        links[i].getAttribute('data-view') === name);
    }
  }

  // show(view) — nav entry point used by nav clicks + XU.show(name).
  function show(name) {
    if (location.hash === '#/' + name) {
      route(); // already there: force remount
    } else {
      location.hash = '#/' + name;
    }
  }

  // ---- nav click wiring ----------------------------------------------------------

  function initNav() {
    var nav = document.getElementById('nav');
    if (!nav) { return; }
    nav.addEventListener('click', function (ev) {
      var a = ev.target && ev.target.closest ? ev.target.closest('a[data-view]') : null;
      if (!a) { return; }
      ev.preventDefault();
      var v = a.getAttribute('data-view');
      show(v);
    });
  }

  // ---- controls strip (§13.2) ---------------------------------------------------

  function initControls() {
    var rangeGroup = document.getElementById('seg-range');
    var modeGroup = document.getElementById('seg-mode');
    var tzGroup = document.getElementById('seg-tz');

    if (rangeGroup) {
      rangeGroup.addEventListener('click', function (ev) {
        var btn = ev.target && ev.target.closest
          ? ev.target.closest('button[data-range]') : null;
        if (!btn) { return; }
        var r = btn.getAttribute('data-range');
        setState({ range: r, since: '', until: '' });
      });
    }
    if (modeGroup) {
      modeGroup.addEventListener('click', function (ev) {
        var btn = ev.target && ev.target.closest
          ? ev.target.closest('button[data-mode]') : null;
        if (!btn) { return; }
        setState({ mode: btn.getAttribute('data-mode') });
      });
    }
    if (tzGroup) {
      tzGroup.addEventListener('click', function (ev) {
        var btn = ev.target && ev.target.closest
          ? ev.target.closest('button[data-tz]') : null;
        if (!btn) { return; }
        setState({ tz: btn.getAttribute('data-tz') });
      });
    }

    var provChip = document.getElementById('prov-chip');
    if (provChip) {
      provChip.querySelector('.prov-chip-x').addEventListener('click', function () {
        setState({ provider: '' });
      });
    }

    var exportBtn = document.getElementById('export');
    if (exportBtn) {
      exportBtn.addEventListener('click', function () {
        // Views name their export hook exportCsv / exportCSV / export — try each.
        var fn = mounted && (mounted.exportCsv || mounted.exportCSV || mounted.export);
        if (typeof fn === 'function') {
          try { fn.call(mounted); } catch (e) { toast('export failed'); }
        } else {
          toast('this view has no export');
        }
      });
    }

    var rescan = document.getElementById('rescan');
    if (rescan) {
      rescan.addEventListener('click', function () {
        rescan.disabled = true;
        postJSON('/api/refresh').then(function (meta) {
          renderFoot(meta);
        }).catch(function () {}).then(function () {
          rescan.disabled = false;
          pulse();
          route();
        });
      });
    }

    updateProvChip();
  }

  function updateProvChip() {
    var chip = document.getElementById('prov-chip');
    if (!chip) { return; }
    var val = state.provider;
    if (!val) { chip.hidden = true; return; }
    chip.hidden = false;
    chip.querySelector('i').textContent = String(val).charAt(0);
    chip.querySelector('.prov-chip-val').textContent = val;
  }

  // syncControls reflects XU.state onto the static controls.
  function syncControls() {
    var groups = [
      { id: 'seg-range', attr: 'data-range', key: 'range' },
      { id: 'seg-mode', attr: 'data-mode', key: 'mode' },
      { id: 'seg-tz', attr: 'data-tz', key: 'tz' }
    ];
    groups.forEach(function (g) {
      var box = document.getElementById(g.id);
      if (!box) { return; }
      var btns = box.querySelectorAll('button[' + g.attr + ']');
      for (var i = 0; i < btns.length; i++) {
        btns[i].classList.toggle('active', btns[i].getAttribute(g.attr) === state[g.key]);
      }
    });
  }

  function renderProviderChip() { /* no-op: provider chip is in controls */ }

  function renderFoot(meta) {
    var tag = document.getElementById('brand-tag');
    if (tag) { tag.textContent = 'v' + ((meta && meta.version) || 'dev'); }
    var detected = ((meta && meta.providers) || []).filter(function (p) { return p.detected; });
    var sources = document.getElementById('foot-sources');
    if (sources) {
      sources.textContent = detected.length
        ? detected.length + ' source' + (detected.length > 1 ? 's' : '')
        : 'no sources';
    }
  }

  // ---- clock --------------------------------------------------------------------

  var clockTimer = null;
  function pad2(n) { return String(n).padStart(2, '0'); }

  function startClock() {
    function tick() {
      var now = new Date();
      var txt = pad2(now.getHours()) + ':' + pad2(now.getMinutes()) + ':' + pad2(now.getSeconds());
      var clock = document.getElementById('clock');
      if (clock) { clock.textContent = txt; }
      var foot = document.getElementById('foot-clock');
      if (foot) { foot.textContent = txt; }
    }
    tick();
    if (clockTimer) { clearInterval(clockTimer); }
    clockTimer = setInterval(tick, 1000);

    // Local timezone label (IANA).
    var tzl = document.getElementById('tzl');
    if (tzl) {
      try { tzl.textContent = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local'; }
      catch (e) { tzl.textContent = 'local'; }
    }
  }

  // ---- pulse: /api/pulse poller + fan-out (§13.1/§13.2) ------------------------------

  var pulseListeners = [];
  var pulseLast = null;
  var pulseLastAt = 0;
  var pulseBusy = false;
  var navSparkSig = '';
  var navSparkCleanups = [];

  function onPulse(fn) {
    var sub = { fn: fn };
    pulseListeners.push(sub);
    if (pulseLast) {
      try { fn(pulseLast); } catch (e) { /* listener errors are isolated */ }
    }
    return function () {
      var i = pulseListeners.indexOf(sub);
      if (i >= 0) { pulseListeners.splice(i, 1); }
    };
  }

  function pulse() {
    if (pulseBusy) { return Promise.resolve(pulseLast); }
    pulseBusy = true;
    return fetchJSON('/api/pulse').then(function (payload) {
      pulseBusy = false;
      pulseLast = payload || {};
      pulseLastAt = Date.now();
      renderPulseChrome(pulseLast);
      for (var i = 0; i < pulseListeners.length; i++) {
        try { pulseListeners[i].fn(pulseLast); } catch (e) { /* isolated */ }
      }
      return pulseLast;
    }, function () {
      pulseBusy = false;
      var banner = document.getElementById('updated');
      if (banner) { banner.textContent = 'pulse offline'; }
      return null;
    });
  }

  function renderPulseChrome(p) {
    var banner = document.getElementById('updated');
    if (banner) {
      var d = new Date((p && p.generatedAt) ? p.generatedAt : new Date());
      var txt = 'updated ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
      if (p && p.latest && p.latest.provider) { txt += ' \u00b7 ' + p.latest.provider; }
      banner.textContent = txt;
    }
    renderNavSparks(p);
  }

  function renderNavSparks(p) {
    var hosts = document.querySelectorAll('.nav-spark');
    if (!hosts.length) { return; }
    var values = [];
    (p && p.last7 || []).forEach(function (pt) { values.push(Number(pt && pt.tokens) || 0); });
    var sig = values.join(',');
    if (sig === navSparkSig) { return; }
    navSparkSig = sig;
    while (navSparkCleanups.length) {
      try { navSparkCleanups.pop()(); } catch (e) { /* already detached */ }
    }
    for (var i = 0; i < hosts.length; i++) {
      hosts[i].innerHTML = '';
      if (window.XUChart && typeof XUChart.sparkline === 'function') {
        var clean = XUChart.sparkline(hosts[i], {
          values: values, color: colors.accent, width: 54, height: 12
        });
        if (typeof clean === 'function') { navSparkCleanups.push(clean); }
      }
    }
  }

  // ---- boot ----------------------------------------------------------------------------

  function boot() {
    initNav();
    initControls();
    syncControls();
    startClock();
    window.addEventListener('hashchange', route);
    route();

    // One meta fetch for the version footer; the rescan button refreshes it.
    fetchJSON('/api/meta').then(renderFoot).catch(function () { /* offline */ });

    // Prefetch common endpoints in parallel to warm connections (§13.0.1).
    prefetch([
      '/api/meta', '/api/summary', '/api/pulse',
      '/api/models' + q() + (q() ? '&' : '?') + 'trend=1',
      '/api/providers', '/api/blocks' + q()
    ]);

    document.addEventListener('visibilitychange', function () {
      if (!document.hidden && mounted) { route(); }
    });
  }

  // ---- export ----------------------------------------------------------------------------

  window.XU = {
    version: '2.0.0',
    fetch: fetchJSON,
    post: postJSON,
    q: q,
    state: state,
    setState: setState,
    onState: onState,
    fmt: fmt,
    colors: colors,
    colorFor: colorFor,
    pill: pill,
    pulse: pulse,
    onPulse: onPulse,
    registerView: registerView,
    views: views,
    viewList: VIEWS,
    currentView: function () { return currentView; },
    navigate: function (hash) { location.hash = hash; },
    show: show,
    toast: toast,
    el: el,
    esc: esc,
    skeletonRows: skeletonRows,
    downloadCSV: downloadCSV,
    // instant-load layer (§13.0.1)
    cacheGet: cacheGet,
    cacheSet: cacheSet,
    swr: swr,
    prefetch: prefetch,
    // range helpers
    rangeBounds: rangeBounds
  };

  // All scripts use `defer`, so they run in document order: app.js boots before
  // the view scripts register themselves. readyState is already 'interactive'
  // here, so waiting for 'loading'->DOMContentLoaded never fires — boot() would
  // run route() with zero views registered ("Unknown view"). Wait for `load`,
  // which only fires after every defer script (all views) has executed.
  if (document.readyState === 'complete') {
    boot();
  } else {
    window.addEventListener('load', boot);
  }
})();
