// view_blocks.js — Storage Blocks view (SPEC §9.2, restyled Carbon per §13.0).
//
// Frames raw usage as storage: every burst of activity opens a 5-hour billing
// window ("block"). The view shows:
//   - the current window card: burn-rate arc gauge, progress-to-5h meter,
//     token-balance gauge (semantic colors via XUChart.gauge), projection and
//     a usage-limit-reset warning banner when the server surfaces one;
//   - a horizontal timeline of recent blocks on a shared time axis — gap
//     blocks hatched, active block accented, "now" marked — with exact token
//     buckets in each segment tooltip (§13.4);
//   - a dense table of recent blocks (exact input/output/cache-read/
//     cache-create columns); every row expands an inline subtable with full
//     precision counts and pill-model attribution tags.
//
// Data comes from GET /api/blocks (latest first) through the frozen XU helper.
// The timeline is self-contained SVG; only view-specific styling is injected —
// cards, tables, pills, badges, meters and tooltips come from style.css.
// GSAP (§13.0): green #0ae448 is the brand accent hue; cream #fffce1 ink.
(function () {
  'use strict';

  if (!window.XU || typeof XU.registerView !== 'function') {
    console.warn('[xusage] view_blocks: XU global missing; view not registered');
    return;
  }

  var BLOCK_LIMIT = 72;
  var REFRESH_MS = 30000;
  var BLOCK_HOURS = 5;

  var STYLE_ID = 'xu-view-blocks-style';

  // GSAP semantic token ramp (§13.0): green brand accent, cream + muted steps.
  var SEM = {
    input: '#0ae448',
    output: '#fffce1',
    cacheRead: '#7c7c6f',
    cacheCreate: '#42433d',
    other: '#42433d'
  };

  var CARET_SVG = '<svg viewBox="0 0 10 10" fill="none" aria-hidden="true">' +
    '<path d="M3 2l4 3-4 3" stroke="currentColor" stroke-width="1.4" ' +
    'stroke-linecap="round" stroke-linejoin="round"/></svg>';

  // View-specific styles only; shared components come from style.css.
  var VIEW_CSS = [
    '.xu-blocks{display:flex;flex-direction:column;gap:12px;}',
    '.xu-blocks .xu-eyebrow{font-family:var(--font,"Inter",-apple-system,"Segoe UI",Roboto,sans-serif);font-size:16px;font-weight:400;color:#fffce1;margin-bottom:6px;letter-spacing:.01em}',
    '.xu-blocks .xu-intro{margin-top:1px;}',
    '.xu-blocks .xu-legend-key svg{display:block;width:11px;height:11px;}',
    '.xu-blocks .xu-win-time{color:var(--text,#fffce1);font-variant-numeric:tabular-nums;}',
    '.xu-blocks td .pill{max-width:150px;}',
    '.xu-blocks .xu-tl{display:block;width:100%;}',
    '.xu-blocks .xu-tl text{font-size:10px;fill:var(--faint,#7c7c6f);font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;}',
    '.xu-blocks .xu-tl .xu-seg{cursor:pointer;}',
    '.xu-blocks .xu-tl .xu-seg:hover{filter:brightness(1.25);}',
    '.xu-blocks tr.expandable:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:-1px;}',
    '.xu-gauge{width:100%;max-width:220px;display:block;margin:0 auto;}',
    '.xu-gauge .track{fill:none;stroke:var(--border,#42433d);stroke-width:10;stroke-linecap:round;}',
    '.xu-gauge .value{fill:none;stroke:var(--accent,#0ae448);stroke-width:10;stroke-linecap:round;}',
    '.xu-gauge-big{fill:var(--text,#fffce1);font-size:21px;font-weight:600;font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;}',
    '.xu-gauge-sub{fill:var(--muted,#7c7c6f);font-size:9px;letter-spacing:.08em;text-transform:uppercase;}',
    '.xu-meter-caption{display:flex;justify-content:space-between;font-size:11px;color:var(--faint,#7c7c6f);font-variant-numeric:tabular-nums;margin-top:5px;}'
  ].join('\n');

  var hatchSeq = 0;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) { return; }
    var st = document.createElement('style');
    st.id = STYLE_ID;
    st.textContent = VIEW_CSS;
    document.head.appendChild(st);
  }
  function removeStyle() {
    var el = document.getElementById(STYLE_ID);
    if (el && el.parentNode) { el.parentNode.removeChild(el); }
  }

  // ---------- tiny helpers ----------

  function h(tag, cls) {
    var el = document.createElement(tag);
    if (cls) { el.className = cls; }
    return el;
  }
  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }
  function pad2(n) { return (n < 10 ? '0' : '') + n; }
  function hm(d) { return pad2(d.getHours()) + ':' + pad2(d.getMinutes()); }
  function dayHM(d) { return pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' + hm(d); }
  function parseTs(v) {
    if (!v) { return NaN; }
    return Date.parse(String(v).replace(/(\.\d{3})\d+/, '$1'));
  }
  function toDate(v) {
    var ms = parseTs(v);
    return isNaN(ms) ? null : new Date(ms);
  }
  function big(n) {
    if (XU.fmt && typeof XU.fmt.big === 'function') { return XU.fmt.big(n); }
    return XU.fmt.tokens(n);
  }
  function fNum(n) { return XU.fmt.num(n); }
  function fCost(n) { return XU.fmt.cost(n); }
  function fCostPrecise(n) { return XU.fmt.cost(n, true); }
  function fDur(sec) { return XU.fmt.duration(Math.max(0, sec || 0)); }
  function totalTokens(b) {
    return (b.inputTokens || 0) + (b.outputTokens || 0) +
      (b.cacheCreationTokens || 0) + (b.cacheReadTokens || 0) +
      (b.extraTotalTokens || 0);
  }
  function pillModel(name) {
    if (!name) { return ''; }
    if (typeof XU.pill === 'function') { return XU.pill(null, name); }
    return '<span class="pill pill-model" title="' + esc(name) + '">' + esc(name) + '</span>';
  }
  function modelPills(models, max) {
    var ms = models || [];
    if (!ms.length) { return '<span class="faint">—</span>'; }
    var out = [];
    for (var i = 0; i < ms.length && i < max; i++) { out.push(pillModel(ms[i])); }
    if (ms.length > max) { out.push('<span class="faint small">+' + (ms.length - max) + '</span>'); }
    return '<span class="pill-row">' + out.join('') + '</span>';
  }
  function fmtTick(d, spanMs) {
    if (spanMs >= 8 * 86400e3) { return pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()); }
    return dayHM(d);
  }
  function arcPath(cx, cy, r, frac) {
    frac = Math.min(Math.max(frac || 0, 0), 1);
    if (frac >= 0.9995) { return 'M ' + (cx - r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 0 1 ' + (cx + r) + ' ' + cy; }
    var a = Math.PI * (1 - frac);
    var x = (cx + r * Math.cos(a)).toFixed(2);
    var y = (cy - r * Math.sin(a)).toFixed(2);
    return 'M ' + (cx - r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 0 1 ' + x + ' ' + y;
  }

  var NS = 'http://www.w3.org/2000/svg';
  function svgEl(name, attrs) {
    var n = document.createElementNS(NS, name);
    if (attrs) { for (var k in attrs) { n.setAttribute(k, attrs[k]); } }
    return n;
  }
  function hatchSwatch() {
    return '<svg viewBox="0 0 12 12" aria-hidden="true">' +
      '<rect x="0.5" y="0.5" width="11" height="11" fill="var(--card,#191919)" stroke="var(--border,#42433d)"/>' +
      '<path d="M0 12L12 0M-2 8L8 -2M4 14L14 4" stroke="var(--border,#42433d)" stroke-width="1"/></svg>';
  }

  var view = {
    root: null, body: null, blocks: [], openIds: {}, cleanups: [],
    alive: false, gen: 0, lastW: 0, ro: null, timer: null, onVis: null, offState: null, tip: null, tlWrap: null,

    mount: function (el) {
      this.teardown();
      this.root = h('div', 'xu-blocks');
      ensureStyle();
      this.root.innerHTML =
        '<div class="xu-eyebrow">{ Blocks }</div>' +
        '<div class="row between wrap">' +
        '<div class="col">' +
        '<span class="label">Storage Blocks</span>' +
        '<span class="small faint xu-intro">Five-hour billing windows aggregated ' +
        'from raw entries · hatched stretches are idle gaps between windows</span>' +
        '</div>' +
        '<div class="legend small muted">' +
        '<span class="gauge-key"><span class="sw" style="background:rgba(10,228,72,0.16);' +
        'border:1px solid var(--accent,#0ae448)"></span>Active</span>' +
        '<span class="gauge-key"><span class="sw" style="background:var(--card,#191919);' +
        'border:1px solid var(--border,#42433d)"></span>Closed</span>' +
        '<span class="gauge-key xu-legend-key">' + hatchSwatch() + 'Gap</span>' +
        '</div>' +
        '</div>';
      this.body = h('div', 'col');
      this.body.style.gap = '12px';
      this.root.appendChild(this.body);
      el.appendChild(this.root);
      this.alive = true;
      var self = this;
      var off = XU.onState(function () { if (self.alive) { self.load(); } });
      this.offState = typeof off === 'function' ? off : null;
      this.onVis = function () { if (self.alive && !document.hidden) { self.load(); } };
      document.addEventListener('visibilitychange', this.onVis);
      this.timer = setInterval(function () { if (self.alive && !document.hidden) { self.load(); } }, REFRESH_MS);
      this.load();
    },
    unmount: function () { this.teardown(); },
    teardown: function () {
      this.alive = false; this.gen++; this.disposeCharts();
      if (this.ro) { this.ro.disconnect(); this.ro = null; }
      if (this.timer) { clearInterval(this.timer); this.timer = null; }
      if (this.onVis) { document.removeEventListener('visibilitychange', this.onVis); this.onVis = null; }
      if (this.offState) { this.offState(); this.offState = null; }
      this.hideTip(); removeStyle();
      if (this.root) { this.root.innerHTML = ''; }
      this.root = null; this.body = null; this.tlWrap = null; this.blocks = []; this.openIds = {};
    },
    disposeCharts: function () {
      for (var i = 0; i < this.cleanups.length; i++) { try { this.cleanups[i](); } catch (e) {} }
      this.cleanups = [];
    },
    load: function () {
      var self = this;
      var gen = ++this.gen;
      this.lastW = 0;
      this.body.innerHTML = '';
      var skel = h('section', 'card col');
      skel.style.gap = '10px';
      [88, 64, 180].forEach(function (ht) {
        var s = h('div', 'skeleton'); s.style.height = ht + 'px'; skel.appendChild(s);
      });
      this.body.appendChild(skel);
      var qs = XU.q() || '';
      qs = (qs ? qs + '&' : '?') + 'limit=' + BLOCK_LIMIT;
      XU.fetch('/api/blocks' + qs).then(function (blocks) {
        if (!self.alive || gen !== self.gen) { return; }
        self.blocks = Array.isArray(blocks) ? blocks : [];
        self.render();
      }).catch(function (err) {
        if (!self.alive || gen !== self.gen) { return; }
        var msg = err && err.message ? err.message : (err && err.status ? 'HTTP ' + err.status : 'unknown error');
        self.body.innerHTML = '';
        self.body.appendChild(h('div', 'empty')).textContent = 'Failed to load blocks — ' + msg;
      });
    },
    render: function () {
      this.disposeCharts(); this.hideTip(); this.body.innerHTML = '';
      if (!this.blocks.length) {
        this.body.appendChild(h('div', 'empty')).innerHTML = '<b>No storage blocks</b> in the selected range.';
        return;
      }
      this.tip = document.getElementById('tooltip');
      this.body.appendChild(this.activeCard());
      this.body.appendChild(this.timelineCard());
      this.body.appendChild(this.tableCard());
      this.drawTimeline();
      this.observeTimeline();
    },
    observeTimeline: function () {
      if (this.ro) { this.ro.disconnect(); }
      if (typeof ResizeObserver !== 'function') { return; }
      var self = this;
      this.ro = new ResizeObserver(function () { if (self.alive) { self.drawTimeline(); } });
      this.ro.observe(this.tlWrap);
    },
    findActive: function () {
      for (var i = 0; i < this.blocks.length; i++) { if (this.blocks[i].isActive) { return this.blocks[i]; } }
      return null;
    },
    activeCard: function () {
      var card = h('section', 'card col');
      var b = this.findActive();
      var title = h('div', 'card-head');
      title.innerHTML = '<span class="card-title">Current window</span>';
      var headRight = h('span', 'row small muted');
      if (b) {
        var t = blockTimesOf(b);
        headRight.innerHTML = '<span class="badge accent">ACTIVE</span>' +
          '<span class="tabular">' + esc(dayHM(t.s)) + ' → ' + esc(hm(t.planned)) + ' (' + BLOCK_HOURS + 'h)</span>';
      }
      headRight.style.marginLeft = 'auto';
      title.appendChild(headRight);
      card.appendChild(title);
      if (!b) {
        var lastT = null;
        for (var i = 0; i < this.blocks.length; i++) {
          if (this.blocks[i].isGap) { continue; }
          var bt = blockTimesOf(this.blocks[i]);
          if (bt && bt.e && (!lastT || bt.e > lastT)) { lastT = bt.e; }
        }
        var empty = h('div', 'empty');
        empty.innerHTML = 'No active window right now' +
          (lastT ? ' — last activity ' + esc(dayHM(lastT)) + '.' : '; a new 5h window opens with the next entry.');
        card.appendChild(empty);
        return card;
      }
      var t = blockTimesOf(b);
      var now = new Date();
      if (b.usageLimitReset) { card.appendChild(this.limitBanner(b.usageLimitReset)); }
      var grid = h('div', 'grid cols-2');
      var left = h('div', 'col');
      var burn = b.burnRate || { tokensPerMinute: 0, costPerHour: 0 };
      var frac = (now.getTime() - t.s.getTime()) / (t.planned.getTime() - t.s.getTime());
      frac = isFinite(frac) ? Math.min(Math.max(frac, 0), 1) : 0;
      left.innerHTML =
        '<svg class="xu-gauge" viewBox="0 0 160 92" role="img" aria-label="Window burn rate">' +
        '<path class="track" d="' + arcPath(80, 78, 62, 1) + '"></path>' +
        '<path class="value" d="' + arcPath(80, 78, 62, frac) + '"></path>' +
        '<text class="xu-gauge-big" x="80" y="66" text-anchor="middle">' + esc(big(burn.tokensPerMinute || 0)) + '</text>' +
        '<text class="xu-gauge-sub" x="80" y="80" text-anchor="middle">tok / min</text>' +
        '</svg>' +
        '<div class="meter" aria-hidden="true"><i style="width:' + (frac * 100).toFixed(1) + '%"></i></div>' +
        '<div class="xu-meter-caption"><span>elapsed ' + esc(fDur((now.getTime() - t.s.getTime()) / 1000)) + ' of ' + BLOCK_HOURS +
        'h</span><span>' + Math.round(frac * 100) + '%</span></div>';
      grid.appendChild(left);
      var right = h('div', 'col');
      var balLabel = h('span', 'label'); balLabel.textContent = 'Token balance';
      right.appendChild(balLabel);
      var gaugeHost = h('div'); right.appendChild(gaugeHost);
      var segs = [
        { name: 'Input', value: b.inputTokens || 0, color: SEM.input },
        { name: 'Output', value: b.outputTokens || 0, color: SEM.output },
        { name: 'Cache read', value: b.cacheReadTokens || 0, color: SEM.cacheRead },
        { name: 'Cache creation', value: b.cacheCreationTokens || 0, color: SEM.cacheCreate }
      ];
      if (b.extraTotalTokens) { segs.push({ name: 'Other', value: b.extraTotalTokens, color: SEM.other }); }
      var total = totalTokens(b);
      if (total > 0 && window.XUChart && typeof XUChart.gauge === 'function') {
        var cleanup = XUChart.gauge(gaugeHost, { segments: segs, total: total });
        if (typeof cleanup === 'function') { this.cleanups.push(cleanup); }
      } else { gaugeHost.innerHTML = '<span class="faint small">no tokens yet</span>'; }
      var kvs = h('div', 'col'); kvs.style.marginTop = '10px';
      var rows = kvRow('So far', esc(fCost(b.cost || 0)) + ' · ' + esc(big(total)) + ' tok') +
        kvRow('Messages', esc(fNum(b.messages || 0)));
      if (b.burnRate) { rows += kvRow('Burn cost', esc(fCost(burn.costPerHour || 0)) + ' /hr'); }
      if (b.projection) {
        rows += kvRow('Projected', esc(fCost(b.projection.totalCost || 0)) + ' · ' + esc(big(b.projection.totalTokens || 0)) + ' tok') +
          kvRow('Ends in', esc(fDur((b.projection.remainingMinutes || 0) * 60)));
      }
      kvs.innerHTML = rows; right.appendChild(kvs); grid.appendChild(right);
      card.appendChild(grid);
      if (b.models && b.models.length) {
        var pills = h('div', 'pill-row'); pills.innerHTML = modelPills(b.models, 8); card.appendChild(pills);
      }
      return card;
    },
    limitBanner: function (iso) {
      var r = toDate(iso);
      if (!r) { return null; }
      var diff = r.getTime() - Date.now();
      var rel = diff > 0 ? 'resets in ' + fDur(diff / 1000) : 'reset time has passed';
      var d = h('div', 'banner');
      d.innerHTML = '<b>Usage limit reached</b> — window resets at ' + esc(dayHM(r)) + ' (' + esc(rel) + ')';
      return d;
    },
    timelineCard: function () {
      var card = h('section', 'card');
      var title = h('div', 'card-head');
      title.innerHTML = '<span class="card-title">Timeline</span>' +
        '<span class="faint small">hover a window for exact token buckets</span>';
      card.appendChild(title);
      this.tlWrap = h('div'); card.appendChild(this.tlWrap);
      return card;
    },
    drawTimeline: function () {
      var wrap = this.tlWrap;
      if (!wrap) { return; }
      var width = wrap.clientWidth || 900;
      if (width === this.lastW && wrap.firstChild) { return; }
      this.lastW = width;
      var items = [], t0 = Infinity, t1 = -Infinity, now = Date.now();
      for (var i = 0; i < this.blocks.length; i++) {
        var b = this.blocks[i];
        var t = blockTimesOf(b);
        if (!t) { continue; }
        var end = b.isActive ? Math.max(t.planned.getTime(), t.e.getTime()) : t.e.getTime();
        items.push({ b: b, s: t.s.getTime(), e: t.e.getTime(), planned: t.planned.getTime(), visEnd: end });
        if (t.s.getTime() < t0) { t0 = t.s.getTime(); }
        if (end > t1) { t1 = end; }
      }
      if (!items.length) { wrap.innerHTML = ''; return; }
      var span = t1 - t0;
      if (!(span > 0)) { span = 60000; }
      t0 -= span * 0.02; t1 += span * 0.02; span = t1 - t0;
      var padL = 10, padR = 10, height = 82, trackY = 20, trackH = 20, axisY = 50;
      function x(ms) { return padL + ((ms - t0) / span) * (width - padL - padR); }
      var svg = svgEl('svg', { 'class': 'xu-tl', width: width, height: height, viewBox: '0 0 ' + width + ' ' + height, preserveAspectRatio: 'none' });
      var pid = 'xu-blk-hatch-' + (++hatchSeq);
      var defs = svgEl('defs');
      var pat = svgEl('pattern', { id: pid, width: 6, height: 6, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' });
      var pbg = svgEl('rect', { width: 6, height: 6 }); pbg.style.fill = 'var(--card,#191919)';
      var pln = svgEl('line', { x1: 0, y1: 0, x2: 0, y2: 6 }); pln.style.stroke = 'var(--border,#42433d)'; pln.setAttribute('stroke-width', '1.5');
      pat.appendChild(pbg); pat.appendChild(pln); defs.appendChild(pat); svg.appendChild(defs);
      var TICKS = 5;
      for (var g = 0; g < TICKS; g++) {
        var tm = t0 + (span * g) / (TICKS - 1);
        var gx = x(tm);
        var gl = svgEl('line', { x1: gx, y1: 12, x2: gx, y2: axisY });
        gl.style.stroke = 'var(--border,#42433d)'; gl.style.opacity = '0.4'; svg.appendChild(gl);
        var tx = svgEl('text', { x: gx, y: axisY + 18, 'text-anchor': g === 0 ? 'start' : (g === TICKS - 1 ? 'end' : 'middle') });
        tx.textContent = fmtTick(new Date(tm), span); svg.appendChild(tx);
      }
      var axis = svgEl('line', { x1: x(t0), y1: axisY, x2: x(t1), y2: axisY });
      axis.style.stroke = 'var(--border,#42433d)'; svg.appendChild(axis);
      var self = this;
      for (var s = 0; s < items.length; s++) {
        var it = items[s];
        var rect = svgEl('rect', {
          x: x(it.s), y: trackY, width: Math.max(2, x(it.b.isActive ? it.e : it.visEnd) - x(it.s)),
          height: trackH, rx: 3, 'class': 'xu-seg'
        });
        if (it.b.isGap) {
          rect.style.fill = 'url(#' + pid + ')'; rect.style.stroke = 'var(--border,#42433d)';
        } else if (it.b.isActive) {
          rect.style.fill = 'rgba(10,228,72,0.16)'; rect.style.stroke = 'var(--accent,#0ae448)'; rect.style.strokeWidth = '2';
          if (it.planned > it.e) {
            var pr = svgEl('rect', { x: x(it.e), y: trackY, width: Math.max(1, x(it.planned) - x(it.e)), height: trackH, rx: 3 });
            pr.style.fill = 'none'; pr.style.stroke = 'var(--accent,#0ae448)'; pr.setAttribute('stroke-dasharray', '3 3');
            pr.style.opacity = '0.6'; pr.style.pointerEvents = 'none'; svg.appendChild(pr);
          }
        } else { rect.style.fill = 'var(--card,#191919)'; rect.style.stroke = 'var(--border,#42433d)'; }
        rect.__blk = it.b;
        rect.addEventListener('mousemove', function (evt) { self.showTip(evt.currentTarget.__blk, evt); });
        rect.addEventListener('mouseleave', function () { self.hideTip(); });
        svg.appendChild(rect);
      }
      if (now >= t0 && now <= t1) {
        var nx = x(now);
        var nl = svgEl('line', { x1: nx, y1: 12, x2: nx, y2: axisY });
        nl.style.stroke = 'var(--accent,#0ae448)'; nl.setAttribute('stroke-dasharray', '2 3'); nl.style.opacity = '0.85'; nl.style.pointerEvents = 'none';
        svg.appendChild(nl);
        var nt = svgEl('text', { x: nx, y: 8, 'text-anchor': 'middle' }); nt.textContent = 'now';
        nt.style.fill = 'var(--accent,#0ae448)'; svg.appendChild(nt);
      }
      wrap.innerHTML = ''; wrap.appendChild(svg);
    },
    tipHTML: function (b) {
      var t = blockTimesOf(b);
      if (!t) { return ''; }
      var title;
      if (b.isGap) {
        title = 'Gap · ' + esc(dayHM(t.s)) + ' → ' + esc(dayHM(t.e));
        return '<div class="tt-title">' + title + '</div>' +
          '<div class="tt-row"><span>Idle for</span><span>' + esc(XU.fmt.duration((t.e.getTime() - t.s.getTime()) / 1000)) + '</span></div>';
      }
      title = (b.isActive ? 'Active window' : 'Window') + ' · ' + esc(dayHM(t.s)) + ' → ' +
        (b.isActive ? esc(hm(t.planned)) + ' est' : esc(dayHM(t.e)));
      var html = '<div class="tt-title">' + title + '</div>' +
        bucketRow(SEM.input, 'Input', b.inputTokens) + bucketRow(SEM.output, 'Output', b.outputTokens) +
        bucketRow(SEM.cacheRead, 'Cache read', b.cacheReadTokens) + bucketRow(SEM.cacheCreate, 'Cache creation', b.cacheCreationTokens);
      if (b.extraTotalTokens) { html += bucketRow(SEM.other, 'Other', b.extraTotalTokens); }
      html += '<div class="tt-row"><span>Total</span><span>' + esc(fNum(totalTokens(b))) + '</span></div>' +
        '<div class="tt-row"><span>Cost</span><span>' + esc(fCostPrecise(b.cost || 0)) + '</span></div>';
      if (b.messages) { html += '<div class="tt-row"><span>Messages</span><span>' + esc(fNum(b.messages)) + '</span></div>'; }
      if (b.burnRate) { html += '<div class="tt-row"><span>Burn</span><span>' + esc(big(b.burnRate.tokensPerMinute || 0)) + '/min · ' + esc(fCost(b.burnRate.costPerHour || 0)) + '/hr</span></div>'; }
      return html;
    },
    showTip: function (b, evt) {
      if (!this.tip || !b) { return; }
      this.tip.innerHTML = this.tipHTML(b); this.tip.style.display = 'block';
      var pad = 12, r = this.tip.getBoundingClientRect();
      var nx = evt.clientX + pad, ny = evt.clientY + pad;
      if (nx + r.width > window.innerWidth - 8) { nx = evt.clientX - r.width - pad; }
      if (ny + r.height > window.innerHeight - 8) { ny = evt.clientY - r.height - pad; }
      this.tip.style.left = Math.max(8, nx) + 'px'; this.tip.style.top = Math.max(8, ny) + 'px';
    },
    hideTip: function () { if (this.tip && this.tip.style) { this.tip.style.display = 'none'; } },
    tableCard: function () {
      var card = h('section', 'card flush');
      var title = h('div', 'card-head'); title.style.padding = '12px 16px 0';
      var activeCount = 0;
      for (var i = 0; i < this.blocks.length; i++) { if (this.blocks[i].isActive && !this.blocks[i].isGap) { activeCount++; } }
      title.innerHTML = '<span class="card-title">Recent blocks</span>' +
        '<span class="row small muted tabular" style="margin-left:auto">' +
        '<span class="badge">' + this.blocks.length + '</span>' +
        '<span>windows · ' + activeCount + ' active</span></span>';
      card.appendChild(title);
      var wrapEl = h('div', 'table-wrap'); wrapEl.style.border = 'none';
      wrapEl.appendChild(this.tableEl()); card.appendChild(wrapEl);
      return card;
    },
    tableEl: function () {
      var table = h('table', 'table');
      table.innerHTML = '<thead><tr>' +
        '<th>Window</th><th class="num">Dur</th><th class="num">In</th><th class="num">Out</th>' +
        '<th class="num">Cache rd</th><th class="num">Cache wr</th><th class="num">Total</th>' +
        '<th class="num">Cost</th><th class="num">Burn</th><th>Models</th></tr></thead>';
      var tbody = h('tbody');
      var html = '';
      for (var i = 0; i < this.blocks.length; i++) { html += this.rowHTML(this.blocks[i]); }
      tbody.innerHTML = html; table.appendChild(tbody);
      var self = this;
      table.addEventListener('click', function (evt) { self.onRowActivate(evt); });
      table.addEventListener('keydown', function (evt) { if (evt.key !== 'Enter' && evt.key !== ' ') { return; } self.onRowActivate(evt); });
      return table;
    },
    rowHTML: function (b) {
      var t = blockTimesOf(b);
      if (!t) { return ''; }
      var id = String(b.id != null ? b.id : t.s.getTime());
      var open = !!this.openIds[id];
      var status;
      if (b.isGap) { status = '<span class="badge">GAP</span>'; }
      else if (b.isActive) { status = '<span class="badge accent">ACTIVE</span>'; }
      else { status = '<span class="badge">CLOSED</span>'; }
      var endTxt = b.isActive ? esc(hm(t.planned)) + ' <span class="faint">est</span>' : esc(dayHM(t.e));
      var dur = esc(XU.fmt.duration(Math.max(0, (t.e.getTime() - t.s.getTime()) / 1000)));
      var burn = b.burnRate ? esc(big(b.burnRate.tokensPerMinute || 0)) + '/min' : '<span class="faint">—</span>';
      var row = '<tr class="expandable" tabindex="0" data-blk="' + esc(id) + '">' +
        '<td class="nowrap"><span class="caret' + (open ? ' open' : '') + '">' + CARET_SVG + '</span> ' + status +
        ' <span class="xu-win-time">' + esc(dayHM(t.s)) + ' → ' + endTxt + '</span></td>' +
        '<td class="num">' + dur + '</td>' +
        '<td class="num">' + esc(big(b.inputTokens || 0)) + '</td>' +
        '<td class="num">' + esc(big(b.outputTokens || 0)) + '</td>' +
        '<td class="num">' + esc(big(b.cacheReadTokens || 0)) + '</td>' +
        '<td class="num">' + esc(big(b.cacheCreationTokens || 0)) + '</td>' +
        '<td class="num">' + esc(big(totalTokens(b))) + '</td>' +
        '<td class="num">' + esc(fCost(b.cost || 0)) + '</td>' +
        '<td class="num">' + burn + '</td>' +
        '<td>' + modelPills(b.models, 2) + '</td>' +
        '</tr>';
      row += '<tr class="subtable" data-for="' + esc(id) + '"' + (open ? '' : ' style="display:none"') + '><td colspan="10">' +
        this.subtableHTML(b) + '</td></tr>';
      return row;
    },
    subtableHTML: function (b) {
      var keys = keyWithSw(SEM.input, 'Input', fNum(b.inputTokens || 0)) + keyWithSw(SEM.output, 'Output', fNum(b.outputTokens || 0)) +
        keyWithSw(SEM.cacheRead, 'Cache read', fNum(b.cacheReadTokens || 0)) + keyWithSw(SEM.cacheCreate, 'Cache creation', fNum(b.cacheCreationTokens || 0));
      if (b.extraTotalTokens) { keys += keyWithSw(SEM.other, 'Other (reported total)', fNum(b.extraTotalTokens)); }
      keys += keyPlain('Total tokens', fNum(totalTokens(b))) + keyPlain('Cost', fCostPrecise(b.cost || 0)) + keyPlain('Messages', fNum(b.messages || 0));
      if (b.burnRate) { keys += keyPlain('Burn', big(b.burnRate.tokensPerMinute || 0) + ' tok/min · ' + fCost(b.burnRate.costPerHour || 0) + '/hr'); }
      if (b.projection) {
        keys += keyPlain('Projected', fCost(b.projection.totalCost || 0) + ' · ' + fNum(b.projection.totalTokens || 0) + ' tok') +
          keyPlain('Ends in', fDur((b.projection.remainingMinutes || 0) * 60));
      }
      if (b.usageLimitReset) { keys += keyPlain('Usage limit resets', XU.fmt.datetime(b.usageLimitReset)); }
      var models = '';
      if (b.models && b.models.length) {
        models = '<div class="pill-row" style="margin-top:8px">';
        for (var i = 0; i < b.models.length; i++) { models += pillModel(b.models[i]); }
        models += '</div>';
      }
      return '<div class="subtable"><div class="subtable-grid">' + keys + '</div>' + models + '</div>';
    },
    onRowActivate: function (evt) {
      var target = evt.target;
      if (!target || typeof target.closest !== 'function') { return; }
      var tr = target.closest('tr.expandable');
      if (!tr || !tr.parentElement) { return; }
      if (evt.type === 'keydown') { evt.preventDefault(); }
      var id = tr.getAttribute('data-blk');
      var open = !tr.classList.contains('open');
      tr.classList.toggle('open', open);
      var sub = tr.nextElementSibling;
      if (sub && sub.classList.contains('subtable')) { sub.style.display = open ? '' : 'none'; }
      if (id != null) { this.openIds[id] = open; }
    }
  };

  function blockTimesOf(b) {
    var s = toDate(b.startTime);
    if (!s) { return null; }
    var planned = toDate(b.endTime) || new Date(s.getTime() + BLOCK_HOURS * 3600e3);
    var actual = toDate(b.actualEndTime) || planned;
    return { s: s, e: actual, planned: planned };
  }
  function kvRow(k, v) { return '<div class="subtable-key"><span>' + k + '</span><b>' + v + '</b></div>'; }
  function keyPlain(label, value) { return '<span class="subtable-key">' + esc(label) + ' <b>' + value + '</b></span>'; }
  function keyWithSw(color, label, value) { return '<span class="subtable-key"><span class="sw" style="background:' + color + '"></span>' + esc(label) + ' <b>' + value + '</b></span>'; }
  function bucketRow(color, label, n) { return '<div class="tt-row"><span><span class="sw" style="background:' + color + ';margin-right:6px"></span>' + esc(label) + '</span><span>' + esc(fNum(n || 0)) + '</span></div>'; }

  XU.registerView('blocks', view);
})();
