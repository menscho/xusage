// Sessions view — sortable, searchable table of per-session usage
// (GET /api/sessions, rows ordered by last activity server-side). Header
// clicks toggle asc/desc sorting; the search box filters client-side across
// session ids and model names; clicking a row applies it as the global
// session filter and jumps to #/series, while the leading chevron expands an
// in-place subtable with the exact input/output/cache-read/cache-creation
// buckets, a pill-tagged model breakdown, and a 7-day trend sparkline lazily
// fetched from /api/daily?session= (cached per querystring). Skeleton while
// loading, empty states, all chart cleanups drained on unmount.
(function () {
  'use strict';

  if (typeof XU === 'undefined' || typeof XU.registerView !== 'function') {
    return; // Partial deploy without app.js — do not throw on load.
  }

  var VIEW = 'sessions';
  var FIELD = 'session';        // XU.state key + query parameter for drill-down
  var FIELD_LABEL = 'Session';
  var ENDPOINT = '/api/sessions';
  var PFX = 'xus';              // scoped CSS prefix
  var KEY_WIDTH = '200px';      // session ids are uuid-like
  var PLACEHOLDER = 'Filter sessions\u2026';
  var NOUN = 'sessions';

  // Semantic token colors — GSAP category hues (SPEC §13.0).
  var SEM = {
    input: '#9d95ff',
    output: '#00bae2',
    cacheRead: '#ff8709',
    cacheCreate: '#fec5fb',
    other: '#7c7c6f'
  };

  // Data-figure type rides the shared mono stack (§13.0), fresh-value fallback.
  var MONO = 'var(--mono,ui-monospace,Consolas,monospace)';

  var CSS = [
    '.' + PFX + '-root .' + PFX + '-eyebrow{font-size:17px;font-weight:400;color:var(--text,#fffce1);letter-spacing:.01em;line-height:1.1;margin:0 0 14px}',
    '.' + PFX + '-root .' + PFX + '-stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:8px;margin-bottom:12px}',
    '.' + PFX + '-root .' + PFX + '-stat{padding:10px 12px;border-radius:8px;background:var(--panel,#191919)}',
    '.' + PFX + '-root .' + PFX + '-stat .' + PFX + '-sval{font-size:18px;font-weight:600;color:var(--text,#fffce1);font-family:' + MONO + ';font-variant-numeric:tabular-nums;line-height:1.1}',
    '.' + PFX + '-root .' + PFX + '-stat .' + PFX + '-slab{font-size:10px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;color:var(--muted,#7c7c6f);margin-top:4px}',
    '.' + PFX + '-root .' + PFX + '-toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px}',
    '.' + PFX + '-root .' + PFX + '-toolbar .input{width:260px}',
    '.' + PFX + '-root .' + PFX + '-grow{flex:1 1 auto}',
    '.' + PFX + '-root .' + PFX + '-count{font-size:12px;color:var(--muted,#7c7c6f);font-family:' + MONO + ';font-variant-numeric:tabular-nums}',
    '.' + PFX + '-root .' + PFX + '-xu-export{appearance:none;background:none;border:1px solid var(--text,#fffce1);border-radius:100px;color:var(--text,#fffce1);padding:5px 14px;font-size:12px;font-weight:600;cursor:pointer;font-family:inherit;line-height:1.2}',
    '.' + PFX + '-root .' + PFX + '-xu-export:hover{opacity:.8}',
    '.' + PFX + '-root .' + PFX + '-xu-export:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:1px}',
    '.' + PFX + '-root .' + PFX + '-chip{display:inline-flex;align-items:center;gap:6px;padding:4px 12px;border:1px solid var(--border,#42433d);border-radius:100px;background:none;color:var(--text,#fffce1);font-size:12px;font-weight:600}',
    '.' + PFX + '-root .' + PFX + '-chip .cv{max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.' + PFX + '-root .' + PFX + '-chip button{appearance:none;border:0;background:none;color:inherit;cursor:pointer;font-size:12px;line-height:1;padding:2px 6px;border-radius:100px}',
    '.' + PFX + '-root .' + PFX + '-chip button:hover{opacity:.8}',
    '.' + PFX + '-root th.num,.' + PFX + '-root td.num{text-align:right;font-variant-numeric:tabular-nums}',
    '.' + PFX + '-root th.sortable{cursor:pointer;-webkit-user-select:none;user-select:none}',
    '.' + PFX + '-root th.sortable:hover{color:var(--text,#fffce1)}',
    '.' + PFX + '-root th.sortable:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:1px}',
    '.' + PFX + '-root .arrow{display:inline-block;min-width:1em;color:var(--accent,#0ae448);font-size:10px;margin-left:2px}',
    '.' + PFX + '-root tr.' + PFX + '-row{cursor:pointer}',
    '.' + PFX + '-root tr.' + PFX + '-row:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:-2px}',
    '.' + PFX + '-root .' + PFX + '-key{max-width:' + KEY_WIDTH + ';overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.' + PFX + '-root .' + PFX + '-expand{width:30px;text-align:center}',
    '.' + PFX + '-root .' + PFX + '-xbtn{appearance:none;background:none;border:0;color:var(--muted,#7c7c6f);cursor:pointer;font-size:11px;line-height:1;padding:3px 6px;border-radius:4px}',
    '.' + PFX + '-root .' + PFX + '-xbtn:hover{color:var(--text,#fffce1);background:var(--panel,#191919)}',
    '.' + PFX + '-root .' + PFX + '-xbtn:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:1px}',
    '.' + PFX + '-root .meter{display:inline-block;width:52px;height:3px;margin-left:8px;vertical-align:middle;border-radius:2px;background:var(--panel,#191919);overflow:hidden}',
    '.' + PFX + '-root .meter i{display:block;height:100%;background:rgba(10,224,72,0.30)}',
    '.' + PFX + '-root tr.' + PFX + '-sub>td{background:var(--panel,#191919);border-top:1px dashed var(--border,#42433d);border-bottom:1px dashed var(--border,#42433d);padding:14px}',
    '.' + PFX + '-root .' + PFX + '-subgrid{display:grid;grid-template-columns:minmax(240px,1.2fr) minmax(220px,1fr) minmax(150px,.7fr);gap:24px}',
    '@media (max-width:900px){.' + PFX + '-root .' + PFX + '-subgrid{grid-template-columns:1fr}}',
    '.' + PFX + '-root .' + PFX + '-slabel{font-size:11px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;color:var(--accent,#0ae448);margin-bottom:6px}',
    '.' + PFX + '-root .' + PFX + '-gauge{display:flex;height:12px;border-radius:6px;overflow:hidden;background:var(--bg,#0e100f);margin-bottom:8px}',
    '.' + PFX + '-root .' + PFX + '-gauge span{display:block;height:100%}',
    '.' + PFX + '-root .' + PFX + '-gleg{display:flex;gap:12px;flex-wrap:wrap;font-size:11px;color:var(--muted,#7c7c6f)}',
    '.' + PFX + '-root .' + PFX + '-gleg i{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:4px}',
    '.' + PFX + '-root .' + PFX + '-gleg b{color:var(--text,#fffce1);font-family:' + MONO + ';font-weight:500;font-variant-numeric:tabular-nums;margin-left:3px}',
    '.' + PFX + '-root .' + PFX + '-kv{display:flex;gap:16px;flex-wrap:wrap;font-size:12px;color:var(--muted,#7c7c6f);margin-top:10px}',
    '.' + PFX + '-root .' + PFX + '-kv b{color:var(--text,#fffce1);font-family:' + MONO + ';font-weight:500;font-variant-numeric:tabular-nums;margin-left:5px}',
    '.' + PFX + '-root .' + PFX + '-models{display:flex;flex-direction:column;gap:5px;font-size:12px}',
    '.' + PFX + '-root .' + PFX + '-model{display:flex;align-items:baseline;gap:12px;min-width:0}',
    '.' + PFX + '-root .' + PFX + '-mname{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:380px}',
    '.' + PFX + '-root .' + PFX + '-mtok{margin-left:auto;color:var(--muted,#7c7c6f);font-family:' + MONO + ';font-variant-numeric:tabular-nums}',
    '.' + PFX + '-root .' + PFX + '-mcost{width:88px;text-align:right;font-family:' + MONO + ';font-variant-numeric:tabular-nums}',
    '.' + PFX + '-root .' + PFX + '-tnote{font-size:11px;color:var(--muted,#7c7c6f);margin-bottom:4px;font-family:' + MONO + ';font-variant-numeric:tabular-nums}',
    '.' + PFX + '-root .badge-warn{color:var(--warn,#f59e0b);border-color:rgba(245,158,11,.45)}'
  ].join('\n');

  // Column definitions: str columns sort ascending on first click, numeric
  // columns descending first.
  var COLUMNS = [
    { id: 'key', label: FIELD_LABEL, str: true },
    { id: 'cost', label: 'Cost', num: true },
    { id: 'tokens', label: 'Tokens', num: true },
    { id: 'messages', label: 'Msgs', num: true },
    { id: 'duration', label: 'Duration', num: true },
    { id: 'last', label: 'Last activity', num: true }
  ];
  var DEFAULT_SORT = { key: 'cost', dir: -1 };

  // Per-mount state; exactly one instance lives at a time (router unmounts first).
  var S = {
    alive: false, tick: 0, typing: null, unsub: null,
    el: null, root: null, input: null, card: null, count: null, chips: null,
    rows: [], qs: null, loading: false, error: '',
    sort: { key: DEFAULT_SORT.key, dir: DEFAULT_SORT.dir },
    filter: '', open: Object.create(null), trends: Object.create(null),
    cleanups: []
  };

  XU.registerView(VIEW, { mount: mount, unmount: unmount });

  // ---------------------------------------------------------------- helpers

  function elIn(parent, tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) { n.className = cls; }
    if (text != null) { n.textContent = text; }
    if (parent) { parent.appendChild(n); }
    return n;
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // Compact token figure for table cells; exact counts live in titles,
  // subtables, and gauge legends (SPEC §13.4).
  function fBig(n) {
    if (XU.fmt && typeof XU.fmt.big === 'function') { return XU.fmt.big(n); }
    return XU.fmt.tokens(n);
  }

  function rowTokens(r) {
    return (r.inputTokens || 0) + (r.outputTokens || 0) +
      (r.cacheCreationTokens || 0) + (r.cacheReadTokens || 0) +
      (r.extraTotalTokens || 0);
  }

  function modelTokens(m) {
    return (m.inputTokens || 0) + (m.outputTokens || 0) +
      (m.cacheCreationTokens || 0) + (m.cacheReadTokens || 0) +
      (m.extraTotalTokens || 0);
  }

  function bucketTitle(r) {
    var parts = ['input ' + XU.fmt.num(r.inputTokens || 0),
                 'output ' + XU.fmt.num(r.outputTokens || 0),
                 'cache read ' + XU.fmt.num(r.cacheReadTokens || 0),
                 'cache creation ' + XU.fmt.num(r.cacheCreationTokens || 0)];
    if (r.extraTotalTokens) { parts.push('reported total ' + XU.fmt.num(r.extraTotalTokens)); }
    return parts.join(' \u00b7 ');
  }

  function injectStyle() {
    if (document.querySelector('style[data-xu-view="' + VIEW + '"]')) { return; }
    var s = document.createElement('style');
    s.setAttribute('data-xu-view', VIEW);
    s.textContent = CSS;
    document.head.appendChild(s);
  }

  function removeStyle() {
    var s = document.querySelector('style[data-xu-view="' + VIEW + '"]');
    if (s && s.parentNode) { s.parentNode.removeChild(s); }
  }

  function drainCleanups() {
    S.cleanups.forEach(function (fn) {
      try { fn(); } catch (e) { /* cleanup must never throw */ }
    });
    S.cleanups = [];
  }

  // Server row -> view row: precomputes totals, duration and search text.
  function normalize(raw) {
    var r = raw || {};
    var first = r.firstActivity ? Date.parse(r.firstActivity) : NaN;
    var last = r.lastActivity ? Date.parse(r.lastActivity) : NaN;
    var dur = (isFinite(first) && isFinite(last) && last >= first) ? (last - first) / 1000 : -1;
    var models = (Array.isArray(r.models) ? r.models : [])
      .map(function (m) { return m || {}; })
      .sort(function (a, b) { return (b.cost || 0) - (a.cost || 0); });
    var key = String(r.key || '');
    return {
      key: key,
      cost: r.cost || 0,
      tokens: rowTokens(r),
      bucketTitle: bucketTitle(r),
      inputTokens: r.inputTokens || 0,
      outputTokens: r.outputTokens || 0,
      cacheReadTokens: r.cacheReadTokens || 0,
      cacheCreationTokens: r.cacheCreationTokens || 0,
      extraTotalTokens: r.extraTotalTokens || 0,
      messages: r.messages || 0,
      sessions: r.sessions || 0,
      models: models,
      firstIso: r.firstActivity || '',
      dur: dur,
      lastMs: isFinite(last) ? last : NaN,
      lastIso: r.lastActivity || '',
      search: (key + ' ' + models.map(function (m) { return m.model || ''; }).join(' ')).toLowerCase()
    };
  }

  function sortValue(r, id) {
    switch (id) {
      case 'key': return r.key.toLowerCase();
      case 'cost': return r.cost;
      case 'tokens': return r.tokens;
      case 'messages': return r.messages;
      case 'duration': return r.dur; // -1 (unknown) sorts as smallest
      case 'last': return isFinite(r.lastMs) ? r.lastMs : -Infinity;
      default: return 0;
    }
  }

  function visibleRows() {
    var q = S.filter.trim().toLowerCase();
    var rowsOut = S.rows.filter(function (r) { return !q || r.search.indexOf(q) !== -1; });
    var id = S.sort.key, dir = S.sort.dir;
    rowsOut.sort(function (a, b) {
      var va = sortValue(a, id), vb = sortValue(b, id);
      var c = (typeof va === 'string')
        ? (va < vb ? -1 : (va > vb ? 1 : 0))
        : (va - vb);
      if (!isFinite(c)) { c = 0; }
      if (c === 0) { c = a.key < b.key ? -1 : (a.key > b.key ? 1 : 0); } // stable tiebreak
      return c * dir;
    });
    return rowsOut;
  }

  // ------------------------------------------------------------ data wiring

  function targetQs() {
    var extra = {};
    extra[FIELD] = (XU.state && XU.state[FIELD]) || '';
    return XU.q(extra) || '';
  }

  function load(qs) {
    S.qs = qs;
    S.trends = Object.create(null); // cached sparklines die with their querystring
    S.loading = true;
    S.error = '';
    var ticket = ++S.tick;
    renderBody(); // skeleton; instant cache paint replaces it if SWR has a payload
    var cacheKey = VIEW + ':' + qs;
    var loadFn = function () { return XU.fetch(ENDPOINT + qs); };
    var renderFn = function (data) {
      if (!S.alive || ticket !== S.tick) { return; }
      S.rows = (Array.isArray(data) ? data : []).map(normalize);
      S.loading = false;
      renderBody();
    };
    var failFn = function (err) {
      if (!S.alive || ticket !== S.tick) { return; }
      S.loading = false;
      S.error = (err && (err.message || err.statusText)) ||
        ('HTTP ' + ((err && err.status) || '?'));
      renderBody();
    };
    if (typeof XU.swr === 'function') {
      XU.swr(cacheKey, loadFn, renderFn);
    } else {
      loadFn().then(renderFn).catch(failFn);
    }
  }

  function onExternalState() {
    if (!S.alive) { return; }
    renderChip();
    var t = targetQs();
    if (t !== S.qs) { load(t); } // range/tz/mode/filter changed -> refetch
  }

  // -------------------------------------------------------------- lifecycle

  function mount(el) {
    injectStyle();
    S.alive = true;
    S.tick++;
    S.rows = [];
    S.qs = null;
    S.loading = false;
    S.error = '';
    S.filter = '';
    S.open = Object.create(null);
    S.trends = Object.create(null);
    S.cleanups = [];
    S.sort = { key: DEFAULT_SORT.key, dir: DEFAULT_SORT.dir };

    el.innerHTML = '';
    S.el = el;
    var rootW = elIn(el, 'div', PFX + '-root');

    elIn(rootW, 'div', PFX + '-eyebrow', '{ Sessions }');

    var bar = elIn(rootW, 'div', PFX + '-toolbar');
    var input = elIn(bar, 'input', 'input');
    input.type = 'search';
    input.placeholder = PLACEHOLDER;
    input.setAttribute('aria-label', PLACEHOLDER);
    input.addEventListener('input', onType);
    S.input = input;
    S.count = elIn(bar, 'span', PFX + '-count');
    elIn(bar, 'span', PFX + '-grow');
    S.chips = elIn(bar, 'span', PFX + '-chips');
    var ex = elIn(bar, 'button', PFX + '-xu-export', 'Export CSV');
    ex.type = 'button';
    ex.title = 'Download the current rows as a CSV file';
    ex.setAttribute('aria-label', 'Export ' + NOUN + ' as CSV');
    ex.addEventListener('click', exportCSV);

    S.stats = elIn(rootW, 'div', PFX + '-stats');
    S.card = elIn(rootW, 'div', 'card');
    S.root = rootW;

    S.unsub = XU.onState(onExternalState);
    load(targetQs());
  }

  function unmount() {
    S.alive = false;
    S.tick++;
    if (S.typing) { clearTimeout(S.typing); S.typing = null; }
    if (typeof S.unsub === 'function') {
      try { S.unsub(); } catch (e) { /* older XU without unsubscribe */ }
    }
    S.unsub = null;
    drainCleanups();
    if (S.el) { S.el.innerHTML = ''; }
    S.el = null; S.root = null; S.input = null; S.card = null; S.count = null; S.chips = null;
    S.stats = null;
    S.rows = [];
    S.trends = Object.create(null);
  }

  // --------------------------------------------------------------- toolbar

  function onType() {
    var v = S.input.value;
    if (S.typing) { clearTimeout(S.typing); }
    S.typing = setTimeout(function () {
      S.typing = null;
      if (!S.alive) { return; }
      if (S.filter !== v) { S.filter = v; renderBody(); }
    }, 120);
  }

  function renderChip() {
    if (!S.chips) { return; }
    S.chips.innerHTML = '';
    var val = (XU.state && XU.state[FIELD]) || '';
    if (!val) { return; }
    var chip = elIn(S.chips, 'span', PFX + '-chip');
    var cv = elIn(chip, 'span', 'cv', FIELD_LABEL + ': ' + val);
    cv.title = val;
    var btn = elIn(chip, 'button', null, '\u2715');
    btn.type = 'button';
    btn.setAttribute('aria-label', 'Clear ' + FIELD_LABEL.toLowerCase() + ' filter');
    btn.addEventListener('click', clearField);
  }

  function clearField() {
    var patch = {};
    patch[FIELD] = '';
    XU.setState(patch);
    renderChip(); // covers XU implementations that do not notify listeners
  }

  // exportCSV builds a row for every currently visible (filtered/sorted)
  // session and downloads it via XU.downloadCSV (client-side Blob).
  function exportCSV() {
    var rows = visibleRows();
    if (!rows.length) {
      if (typeof XU.toast === 'function') { XU.toast('No rows to export.'); }
      return;
    }
    var header = [FIELD_LABEL, 'Cost', 'Tokens', 'Input', 'Output',
      'Cache read', 'Cache created', 'Other', 'Messages', 'Duration', 'Last activity'];
    var out = rows.map(function (r) {
      return [r.key, r.cost.toFixed(4), r.tokens, r.inputTokens, r.outputTokens,
        r.cacheReadTokens, r.cacheCreationTokens, r.extraTotalTokens,
        r.messages, r.dur >= 0 ? Math.round(r.dur) : '', r.lastIso || ''];
    });
    XU.downloadCSV('xusage-' + VIEW + '.csv', header, out);
  }

  // ------------------------------------------------------------- rendering

  // renderStats paints the mini-stats strip above the table: session count,
  // total cost, total tokens, total messages — a one-glance summary tier.
  function renderStats() {
    var host = S.stats;
    if (!host) { return; }
    host.innerHTML = '';
    if (S.loading || S.error || !S.rows.length) { return; }
    var cost = 0, tokens = 0, messages = 0;
    S.rows.forEach(function (r) {
      cost += r.cost; tokens += r.tokens; messages += r.messages;
    });
    var cells = [
      { lab: 'Sessions', val: XU.fmt.num(S.rows.length) },
      { lab: 'Cost', val: XU.fmt.cost(cost) },
      { lab: 'Tokens', val: (XU.fmt.big || XU.fmt.tokens)(tokens) },
      { lab: 'Messages', val: XU.fmt.num(messages) }
    ];
    cells.forEach(function (c) {
      var s = elIn(host, 'div', PFX + '-stat');
      elIn(s, 'div', PFX + '-sval', c.val);
      elIn(s, 'div', PFX + '-slab', c.lab);
    });
  }

  function renderBody() {
    if (!S.card) { return; }
    var card = S.card;
    drainCleanups();
    card.innerHTML = '';
    if (S.loading) {
      if (S.count) { S.count.textContent = ''; }
      var sk = elIn(card, 'div', 'skeleton');
      sk.style.height = '340px';
      return;
    }
    if (S.error) {
      if (S.count) { S.count.textContent = ''; }
      elIn(card, 'div', 'empty', 'Failed to load ' + NOUN + ' \u2014 ' + S.error);
      return;
    }
    var rows = visibleRows();
    renderStats();
    if (S.count) {
      S.count.textContent = rows.length + ' of ' + S.rows.length + ' ' + NOUN;
    }
    if (!rows.length) {
      elIn(card, 'div', 'empty', S.rows.length
        ? 'No ' + NOUN + ' match "' + S.filter.trim() + '".'
        : 'No ' + NOUN + ' found in the selected range.');
      return;
    }
    card.appendChild(buildTable(rows));
  }

  function onSort(col) {
    if (S.sort.key === col.id) {
      S.sort.dir = -S.sort.dir;
    } else {
      S.sort = { key: col.id, dir: col.str ? 1 : -1 };
    }
    renderBody();
  }

  function toggleOpen(key) {
    if (S.open[key]) { delete S.open[key]; } else { S.open[key] = true; }
    renderBody(); // rebuild rehydrates expanded trends from cache instantly
  }

  function buildTable(rows) {
    var maxCost = 0;
    rows.forEach(function (r) { if (r.cost > maxCost) { maxCost = r.cost; } });

    var table = elIn(null, 'table', 'table');
    var thead = elIn(table, 'thead');
    var hr = elIn(thead, 'tr');
    elIn(hr, 'th', PFX + '-expand', '');
    COLUMNS.forEach(function (col) {
      var th = elIn(hr, 'th', 'sortable' + (col.num ? ' num' : ''), col.label);
      th.tabIndex = 0;
      elIn(th, 'span', 'arrow',
        S.sort.key === col.id ? (S.sort.dir < 0 ? '\u2193' : '\u2191') : '');
      if (S.sort.key === col.id) {
        th.setAttribute('aria-sort', S.sort.dir < 0 ? 'descending' : 'ascending');
      }
      th.addEventListener('click', function () { onSort(col); });
      th.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); onSort(col); }
      });
    });

    var tbody = elIn(table, 'tbody');
    var frag = document.createDocumentFragment();
    rows.forEach(function (r) {
      frag.appendChild(mainRow(r, maxCost));
      if (S.open[r.key]) { frag.appendChild(subRow(r)); }
    });
    tbody.appendChild(frag);
    return table;
  }

  function numTd(tr, text, title) {
    var td = elIn(tr, 'td', 'num', text);
    if (title) { td.title = title; }
    return td;
  }

  function mainRow(r, maxCost) {
    var tr = document.createElement('tr');
    tr.className = PFX + '-row';
    tr.tabIndex = 0;

    var xd = elIn(tr, 'td', PFX + '-expand');
    var xb = elIn(xd, 'button', PFX + '-xbtn', S.open[r.key] ? '\u25be' : '\u25b8');
    xb.type = 'button';
    xb.setAttribute('aria-label', (S.open[r.key] ? 'Collapse ' : 'Expand ') +
      FIELD_LABEL.toLowerCase() + ' details');
    xb.addEventListener('click', function (ev) {
      ev.stopPropagation();
      toggleOpen(r.key);
    });

    var k = elIn(tr, 'td', PFX + '-key', r.key);
    k.title = r.key;

    var c = numTd(tr, XU.fmt.cost(r.cost), r.cost.toFixed(4) + ' USD');
    if (maxCost > 0 && r.cost > 0) {
      var m = elIn(c, 'span', 'meter');
      elIn(m, 'i').style.width = Math.round((100 * r.cost) / maxCost) + '%';
    }

    numTd(tr, fBig(r.tokens), r.bucketTitle);
    numTd(tr, XU.fmt.num(r.messages));
    numTd(tr, r.dur >= 0 ? XU.fmt.duration(Math.round(r.dur)) : '\u2014');
    numTd(tr, r.lastIso ? XU.fmt.datetime(r.lastIso) : '\u2014', r.lastIso || null);

    tr.addEventListener('click', function () { drill(r); });
    tr.addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault();
        drill(r);
      }
    });
    return tr;
  }

  // Expanded row: exact token buckets (stacked gauge + legend chips), model
  // breakdown with pill tags, lazily loaded 7-day trend sparkline.
  function subRow(r) {
    var tr = document.createElement('tr');
    tr.className = PFX + '-sub';
    var td = document.createElement('td');
    td.colSpan = COLUMNS.length + 1;
    tr.appendChild(td);

    var grid = elIn(td, 'div', PFX + '-subgrid');

    // column 1: buckets + headline numbers
    var c1 = elIn(grid, 'div');
    elIn(c1, 'div', PFX + '-slabel', 'Token buckets');
    var gauge = elIn(c1, 'div', PFX + '-gauge');
    var leg = elIn(c1, 'div', PFX + '-gleg');
    renderBucketGauge(gauge, leg, r);
    var kv = elIn(c1, 'div', PFX + '-kv');
    kv.innerHTML =
      '<span>Cost<b>' + esc(XU.fmt.cost(r.cost)) + '</b></span>' +
      '<span>Messages<b>' + esc(XU.fmt.num(r.messages)) + '</b></span>' +
      '<span>First<b>' + esc(r.firstIso ? XU.fmt.datetime(r.firstIso) : '\u2014') + '</b></span>';

    // column 2: per-model breakdown
    var c2 = elIn(grid, 'div');
    elIn(c2, 'div', PFX + '-slabel', 'Models');
    var list = elIn(c2, 'div', PFX + '-models');
    if (!r.models.length) {
      elIn(list, 'div', PFX + '-tnote', 'No per-model detail.');
    } else {
      r.models.forEach(function (m) {
        var line = elIn(list, 'div', PFX + '-model');
        var nm = elIn(line, 'span', PFX + '-mname');
        nm.innerHTML = '<span class="pill pill-model">' + esc(m.model || '(unknown)') + '</span>';
        nm.title = m.model || '';
        var tok = elIn(line, 'span', PFX + '-mtok', fBig(modelTokens(m)));
        tok.title = 'exact ' + XU.fmt.num(modelTokens(m)) + ' tokens';
        elIn(line, 'span', PFX + '-mcost', XU.fmt.cost(m.cost || 0));
        if (m.missingPricing) {
          var b = elIn(line, 'span', 'badge badge-warn', 'unpriced');
          b.title = 'No rates known for this model; cost resolves to $0.00.';
        }
      });
    }

    // column 3: lazy 7-day trend
    var c3 = elIn(grid, 'div');
    elIn(c3, 'div', PFX + '-slabel', 'Last 7 days');
    var trend = elIn(c3, 'div', PFX + '-trend');
    hydrateTrend(trend, r);

    return tr;
  }

  // Stacked bucket bar + exact-count legend chips in the fixed semantic colors.
  function renderBucketGauge(gaugeEl, legEl, r) {
    var segs = [
      { name: 'Input', v: r.inputTokens, color: SEM.input },
      { name: 'Output', v: r.outputTokens, color: SEM.output },
      { name: 'Cache read', v: r.cacheReadTokens, color: SEM.cacheRead },
      { name: 'Cache creation', v: r.cacheCreationTokens, color: SEM.cacheCreate },
      { name: 'Other', v: r.extraTotalTokens, color: SEM.other }
    ];
    var total = r.tokens || 0;
    var html = '', leg = '';
    segs.forEach(function (s) {
      if (!(s.v > 0)) { return; }
      html += '<span style="width:' + Math.max((s.v / total) * 100, 0.5).toFixed(2) +
        '%;background:' + s.color + '" title="' + esc(s.name) + ': ' +
        esc(XU.fmt.num(s.v)) + '"></span>';
      leg += '<span><i style="background:' + s.color + '"></i>' + esc(s.name) +
        '<b>' + esc(XU.fmt.num(s.v)) + '</b></span>';
    });
    gaugeEl.innerHTML = html;
    legEl.innerHTML = leg;
  }

  // ---- lazy 7-day trend sparkline (cached per querystring) -----------------

  function setTrendPending(host) {
    host.innerHTML = '';
    var sk = elIn(host, 'div', 'skeleton');
    sk.style.height = '34px';
    sk.style.width = '120px';
  }

  function hydrateTrend(host, r) {
    if (!host) { return; }
    var ck = S.qs + '|' + r.key;
    var entry = S.trends[ck];
    if (entry && entry.state === 'done') { drawTrend(host, entry); return; }
    if (entry && entry.state === 'pending') {
      entry.hosts.push(host);
      setTrendPending(host);
      return;
    }
    if (entry && entry.state === 'error') {
      host.innerHTML = '<span class="' + PFX + '-tnote">failed to load trend</span>';
      return;
    }
    entry = S.trends[ck] = { state: 'pending', hosts: [host], vals: null, dates: null };
    setTrendPending(host);
    var extra = {};
    extra[FIELD] = r.key;
    XU.fetch('/api/daily' + (XU.q(extra) || '')).then(function (drs) {
      if (!S.alive) { return; }
      var tail = (Array.isArray(drs) ? drs : []).slice(-7);
      var vals = [], dates = [];
      tail.forEach(function (d) {
        vals.push(rowTokens(d));
        dates.push(String(d.key || ''));
      });
      entry.state = 'done';
      entry.vals = vals;
      entry.dates = dates;
      entry.hosts.forEach(function (h) {
        drawTrend(h, entry); // detached hosts were cleared with their subtree
      });
      entry.hosts = [];
    }).catch(function () {
      if (!S.alive) { return; }
      entry.state = 'error';
      entry.hosts.forEach(function (h) {
        h.innerHTML = '<span class="' + PFX + '-tnote">failed to load trend</span>';
      });
      entry.hosts = [];
    });
  }

  function drawTrend(host, entry) {
    host.innerHTML = '';
    var total = 0;
    entry.vals.forEach(function (v) { total += v; });
    if (!(total > 0)) {
      elIn(host, 'div', PFX + '-tnote', 'no recent activity');
      return;
    }
    elIn(host, 'div', PFX + '-tnote',
      '\u03a3 ' + XU.fmt.num(Math.round(total)) + ' tokens \u00b7 7d');
    var box = elIn(host, 'div');
    box.style.height = '34px';
    if (typeof XUChart === 'object' && XUChart && typeof XUChart.sparkline === 'function') {
      S.cleanups.push(XUChart.sparkline(box, {
        values: entry.vals,
        color: '#0ae448',
        height: 34
      }));
    } else {
      sparkFallback(box, entry.vals, '#0ae448', 34);
    }
  }

  // Minimal single-path sparkline used when the running charts.js predates
  // the XUChart.sparkline addition.
  function sparkFallback(host, values, color, height) {
    var w = Math.max(host.clientWidth || 0, 100);
    var h = height || 34;
    var min = Math.min.apply(null, values.concat([0]));
    var max = Math.max.apply(null, values.concat([0]));
    if (max <= min) { max = min + 1; }
    var step = (w - 2) / Math.max(values.length - 1, 1);
    var d = '';
    values.forEach(function (v, i) {
      var x = 1 + step * i;
      var y = h - 2 - ((v - min) / (max - min)) * (h - 4);
      d += (i ? 'L' : 'M') + x.toFixed(1) + ',' + y.toFixed(1);
    });
    var NS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('width', w);
    svg.setAttribute('height', h);
    svg.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
    var path = document.createElementNS(NS, 'path');
    path.setAttribute('d', d);
    path.setAttribute('fill', 'none');
    path.setAttribute('stroke', color);
    path.setAttribute('stroke-width', '1.5');
    path.setAttribute('stroke-linejoin', 'round');
    path.setAttribute('stroke-linecap', 'round');
    svg.appendChild(path);
    host.appendChild(svg);
  }

  // Apply this session as the global filter and open the Series view.
  function drill(r) {
    var patch = {};
    patch[FIELD] = r.key;
    XU.setState(patch);
    location.hash = '#/series';
  }
})();
