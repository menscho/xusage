// Share view — generates a shareable PNG from template_share.png composited
// with live range data on a <canvas> (1536x1024), then offers Download (.png)
// and Copy-to-clipboard. Pure client-side; matches the dashboard's dark design
// and outlined-pill buttons.
//
// Data: GET /api/daily?range=... returns rows with totalTokens, cost,
// sessions, messages, key (YYYY-MM-DD). We derive:
//   Total tokens   = sum(totalTokens)
//   Agent runs     = sum(sessions)            ("processed across N agent runs")
//   Avg / run      = total / runs
//   Cost           = sum(cost)
//   Date range     = first..last day key
//   Chart          = per-day totalTokens bars inside the template's chart box
//
// Placement coordinates come verbatim from the share spec; the canvas uses a
// top-left (0,0) origin with y increasing downward.

(function () {
  'use strict';

  if (!window.XU || typeof window.XU.registerView !== 'function') {
    return;
  }

  var fmt = window.XU.fmt;
  var qs = window.XU.q;

  var TEMPLATE = '/share/template_share.png';
  var W = 1536, H = 1024;

  // Dark palette (matches the rest of the dashboard).
  var COLORS = {
    canvas: '#0a0a0a',
    card: '#111111',
    elevated: '#161616',
    border: '#1e1e1e',
    text: '#e0e0e0',
    muted: '#808080',
    accent: '#4a9eff',
    accent2: '#6bb3ff',
    purple: '#A78BFA'
  };

  var RANGES = ['1d', '3d', '7d', '30d', '90d', 'all'];

  // ---------------------------------------------------------------------------
  // Scoped styles
  // ---------------------------------------------------------------------------

  var STYLE_ID = 'xu-share-css';

  function ensureStyles() {
    if (document.getElementById(STYLE_ID)) { return; }
    var s = document.createElement('style');
    s.id = STYLE_ID;
    s.textContent = [
      '.sh-wrap{max-width:980px;margin:0 auto;}',
      '.sh-head{margin-bottom:10px;}',
      '.sh-eyebrow{font-family:var(--font);font-size:16px;font-weight:400;color:' + COLORS.text + ';margin-bottom:6px;}',
      '.sh-title{font-size:34px;font-weight:600;line-height:1.05;letter-spacing:-0.02em;color:' + COLORS.text + ';margin:0;}',
      '.sh-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin:6px 0 14px;}',
      '.sh-seg{display:inline-flex;align-items:stretch;height:30px;padding:3px;gap:2px;border:1px solid ' + COLORS.border + ';border-radius:8px;background:' + COLORS.canvas + ';}',
      '.sh-seg button{appearance:none;border:0;background:transparent;color:' + COLORS.muted + ';font:600 12px var(--font);padding:0 11px;border-radius:5px;cursor:pointer;white-space:nowrap;}',
      '.sh-seg button:hover{color:' + COLORS.text + ';}',
      '.sh-seg button.active{color:' + COLORS.canvas + ';background:' + COLORS.accent + ';}',
      '.sh-canvas-box{display:flex;justify-content:center;}',
      '.sh-canvas{display:block;max-width:100%;max-height:calc(100vh - 370px);width:auto;height:auto;border-radius:4px;}',
      '.sh-actions{display:flex;gap:12px;margin-top:14px;flex-wrap:wrap;}',
      '.sh-act{display:inline-flex;align-items:center;justify-content:center;width:42px;height:42px;border:1px solid ' + COLORS.text + ';border-radius:100px;background:transparent;color:' + COLORS.text + ';cursor:pointer;transition:opacity .12s;}',
      '.sh-act:hover{opacity:.82;}',
      '.sh-act:active{transform:translateY(.5px);}',
      '.sh-act svg{width:18px;height:18px;display:block;}',
      '.sh-empty{padding:48px 16px;text-align:center;color:' + COLORS.muted + ';font-size:14px;border:1px dashed ' + COLORS.border + ';border-radius:8px;background:' + COLORS.card + ';}',
      '.sh-skel{background:' + COLORS.elevated + ';border-radius:4px;animation:xu-ov-shim 1.4s ease-in-out infinite;}',
      '@keyframes xu-ov-shim{0%{opacity:0.4;}50%{opacity:0.7;}100%{opacity:0.4;}}'
    ].join('');
    document.head.appendChild(s);
  }

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function big(n) {
    if (fmt && typeof fmt.big === 'function') { return fmt.big(n); }
    return String(n);
  }

  // Row total: the /api/daily Row JSON exposes only the individual token
  // buckets (no precomputed total), so sum them here.
  function rowTotal(r) {
    return (Number(r.inputTokens) || 0) + (Number(r.outputTokens) || 0) +
      (Number(r.cacheCreationTokens) || 0) + (Number(r.cacheReadTokens) || 0) +
      (Number(r.extraTotalTokens) || 0);
  }
  function num(n) {
    if (fmt && typeof fmt.num === 'function') { return fmt.num(n); }
    return String(n);
  }
  function cost(n) {
    if (fmt && typeof fmt.cost === 'function') { return fmt.cost(n); }
    return '$' + n;
  }

  // Compact "142.8K" style for the average-per-run metric.
  function fmtK(n) {
    n = Number(n) || 0;
    if (n >= 1e9) { return (n / 1e9).toFixed(2).replace(/\.?0+$/, '') + 'B'; }
    if (n >= 1e6) { return (n / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M'; }
    if (n >= 1e3) { return (n / 1e3).toFixed(1).replace(/\.?0+$/, '') + 'K'; }
    return String(Math.round(n));
  }

  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  // "2026-08-22" -> "Aug 22"
  function fmtDay(key) {
    if (!key || key.length < 10) { return key || ''; }
    var m = parseInt(key.slice(5, 7), 10);
    var d = parseInt(key.slice(8, 10), 10);
    return MONTHS[m - 1] + ' ' + d;
  }

  function dateRangeLabel(rows) {
    if (!rows || !rows.length) { return ''; }
    var first = rows[0].key, last = rows[rows.length - 1].key;
    var a = fmtDay(first), b = fmtDay(last);
    return a === b ? a : (a + ' – ' + b);
  }

  // Decrement font size from start until text fits maxWidth (keeps it bold).
  function fitFont(ctx, text, maxWidth, start, min) {
    var size = start;
    do {
      ctx.font = '700 ' + size + 'px "Inter", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';
      if (ctx.measureText(text).width <= maxWidth) { return size; }
      size -= 4;
    } while (size >= min);
    return min;
  }

  function drawValue(ctx, text, x, y, w, h, color, startSize, minSize, align, cx, cy) {
    if (cx == null) { cx = x + w / 2; }
    if (cy == null) { cy = y + h / 2; }
    ctx.save();
    ctx.fillStyle = color;
    ctx.textAlign = align || 'center';
    ctx.textBaseline = 'middle';
    fitFont(ctx, text, w - 16, startSize, minSize);
    ctx.fillText(text, align === 'left' ? x : cx, cy);
    ctx.restore();
  }

  // ---------------------------------------------------------------------------
  // Chart drawing inside the template's chart box.
  // ---------------------------------------------------------------------------

  function drawChart(ctx, rows) {
    var box = { x: 90, y: 440, w: 700, h: 480 };
    var baseline = box.y + box.h; // 920
    var top = box.y;

    var vals = rows.map(function (r) { return rowTotal(r); });
    var max = Math.max.apply(null, vals.concat([1]));
    var n = vals.length;
    if (!n) { return; }

    var slot = box.w / n;
    var bw = Math.max(3, Math.min(46, slot * 0.62));

    var grad = ctx.createLinearGradient(0, top, 0, baseline);
    grad.addColorStop(0, COLORS.accent2);
    grad.addColorStop(1, COLORS.accent);

    for (var i = 0; i < n; i++) {
      var v = vals[i];
      var bh = v > 0 ? Math.max(2, (v / max) * (box.h - 12)) : 0;
      var x = box.x + i * slot + (slot - bw) / 2;
      var y = baseline - bh;
      ctx.fillStyle = grad;
      if (typeof ctx.roundRect === 'function') {
        ctx.beginPath();
        ctx.roundRect(x, y, bw, bh, Math.min(4, bw / 2));
        ctx.fill();
      } else {
        ctx.fillRect(x, y, bw, bh);
      }
    }
  }

  // ---------------------------------------------------------------------------
  // Compose the share image onto a canvas.
  // ---------------------------------------------------------------------------

  function compose(canvas, img, rows) {
    canvas.width = W;
    canvas.height = H;
    var ctx = canvas.getContext('2d');
    ctx.clearRect(0, 0, W, H);
    ctx.drawImage(img, 0, 0, W, H);

    var total = 0, runs = 0, costSum = 0;
    rows.forEach(function (r) {
      total += rowTotal(r);
      runs += Number(r.sessions) || 0;
      costSum += Number(r.cost) || 0;
    });
    var avg = runs > 0 ? total / runs : 0;

    // 1. Total tokens
    drawValue(ctx, big(total), 190, 145, 450, 155, '#F5F5F5', 130, 70, 'left');
    // 2. Agent run count (between template phrases)
    // Run count: fixed center anchor (420, 319), vertically centered with the
    // "processed across / agent runs" text. Expands equally left/right from
    // center; shrinks font (not width) if it would exceed ~85px.
    drawValue(ctx, num(runs), 375, 301, 101, 36, COLORS.purple, 30, 20, 'center', 420, 324);
    // 3. Chart
    drawChart(ctx, rows);
    // 4. Avg tokens / run
    drawValue(ctx, fmtK(avg), 1070, 125, 390, 75, '#F5F5F5', 78, 40, 'left');
    // 5. Cost
    drawValue(ctx, cost(costSum), 1070, 380, 390, 75, '#F5F5F5', 78, 40, 'left');
    // 6. Runs
    drawValue(ctx, num(runs), 1070, 625, 390, 75, '#F5F5F5', 78, 40, 'left');
    // 7. Date range
    drawValue(ctx, dateRangeLabel(rows), 1070, 855, 390, 55, '#F5F5F5', 52, 28, 'left');
  }

  // ---------------------------------------------------------------------------
  // View state
  // ---------------------------------------------------------------------------

  var view = { gen: 0, offState: null, range: 'all', img: null, rows: [] };

  function fetchDaily(range) {
    var extra = { range: range };
    var qstr = qs(extra);
    return XU.fetch('/api/daily' + qstr);
  }

  function loadImage() {
    return new Promise(function (resolve, reject) {
      var img = new Image();
      img.crossOrigin = 'anonymous';
      img.onload = function () { resolve(img); };
      img.onerror = function () { reject(new Error('template load failed')); };
      img.src = TEMPLATE;
    });
  }

  // ---------------------------------------------------------------------------
  // Rendering
  // ---------------------------------------------------------------------------

  function renderToolbar(root) {
    var tb = root.querySelector('[data-toolbar]');
    tb.innerHTML = '';
    var seg = document.createElement('div');
    seg.className = 'sh-seg';
    RANGES.forEach(function (r) {
      var b = document.createElement('button');
      b.type = 'button';
      b.textContent = r === 'all' ? 'All' : r;
      b.setAttribute('data-range', r);
      if (r === view.range) { b.classList.add('active'); }
      seg.appendChild(b);
    });
    tb.appendChild(seg);
  }

  function renderLoading(root) {
    var box = root.querySelector('[data-canvas]');
    box.innerHTML = '<div style="padding:24px">' +
      '<div class="sh-skel" style="width:100%;height:480px"></div></div>';
  }

  function renderEmpty(root, msg) {
    var box = root.querySelector('[data-canvas]');
    box.innerHTML = '<div class="sh-empty">' + (msg || 'No usage data for this range.') + '</div>';
  }

  function paint(root) {
    var canvasBox = root.querySelector('[data-canvas]');
    if (!view.img) { renderEmpty(root, 'Failed to load share template.'); return; }
    if (!view.rows || !view.rows.length) { renderEmpty(root); return; }

    canvasBox.innerHTML = '';
    var canvas = document.createElement('canvas');
    canvas.className = 'sh-canvas';
    canvasBox.appendChild(canvas);

    var draw = function () { compose(canvas, view.img, view.rows); };
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(draw);
    } else {
      draw();
    }
  }

  function load(root, gen) {
    if (!view.img) {
      loadImage().then(function (img) {
        view.img = img;
        if (gen === view.gen) { fetchData(root, gen); }
      }).catch(function () {
        if (gen === view.gen) { renderEmpty(root, 'Failed to load share template.'); }
      });
    } else {
      fetchData(root, gen);
    }
  }

  function fetchData(root, gen) {
    fetchDaily(view.range).then(function (rows) {
      if (gen !== view.gen) { return; }
      view.rows = Array.isArray(rows) ? rows : [];
      paint(root);
    }).catch(function () {
      if (gen !== view.gen) { return; }
      renderEmpty(root, 'Failed to load usage data.');
    });
  }

  // ---------------------------------------------------------------------------
  // Download / copy
  // ---------------------------------------------------------------------------

  function canvasToBlob(canvas, cb) {
    if (canvas.toBlob) {
      canvas.toBlob(cb, 'image/png');
    } else {
      // Very old fallback
      var data = canvas.toDataURL('image/png');
      var bin = atob(data.split(',')[1]);
      var arr = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) { arr[i] = bin.charCodeAt(i); }
      cb(new Blob([arr], { type: 'image/png' }));
    }
  }

  function download(canvas) {
    canvasToBlob(canvas, function (blob) {
      if (!blob) { return; }
      var a = document.createElement('a');
      a.download = 'xusage-share-' + view.range + '.png';
      a.href = URL.createObjectURL(blob);
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(a.href); }, 500);
      if (XU.toast) { XU.toast('Share image downloaded'); }
    });
  }

  function copy(canvas) {
    if (!window.ClipboardItem || !navigator.clipboard || !navigator.clipboard.write) {
      if (XU.toast) { XU.toast('Clipboard image copy not supported here'); }
      return;
    }
    canvasToBlob(canvas, function (blob) {
      if (!blob) { return; }
      navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })])
        .then(function () { if (XU.toast) { XU.toast('Image copied to clipboard'); } })
        .catch(function () { if (XU.toast) { XU.toast('Copy failed'); } });
    });
  }

  // ---------------------------------------------------------------------------
  // Mount / unmount
  // ---------------------------------------------------------------------------

  function mount(el) {
    ensureStyles();
    view.gen++;
    var gen = view.gen;
    view.range = XU.state.range || 'all';

    el.innerHTML = '';
    var wrap = document.createElement('div');
    wrap.className = 'sh-wrap';

    var head = document.createElement('div');
    head.className = 'sh-head';
    head.innerHTML = '<div class="sh-eyebrow">{ Share }</div>' +
      '<h1 class="sh-title">Share your usage</h1>';
    wrap.appendChild(head);

    var tb = document.createElement('div');
    tb.className = 'sh-toolbar';
    tb.setAttribute('data-toolbar', '');
    wrap.appendChild(tb);

    var canvasBox = document.createElement('div');
    canvasBox.className = 'sh-canvas-box';
    canvasBox.setAttribute('data-canvas', '');
    wrap.appendChild(canvasBox);

    var actions = document.createElement('div');
    actions.className = 'sh-actions';

    var dl = document.createElement('button');
    dl.type = 'button';
    dl.className = 'sh-act';
    dl.setAttribute('data-act', 'download');
    dl.title = 'Download PNG';
    dl.setAttribute('aria-label', 'Download PNG');
    dl.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M12 3v12"/><path d="M7 11l5 5 5-5"/><path d="M5 21h14"/></svg>';

    var cp = document.createElement('button');
    cp.type = 'button';
    cp.className = 'sh-act';
    cp.setAttribute('data-act', 'copy');
    cp.title = 'Copy to clipboard';
    cp.setAttribute('aria-label', 'Copy to clipboard');
    cp.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>';

    actions.appendChild(dl);
    actions.appendChild(cp);
    wrap.appendChild(actions);

    el.appendChild(wrap);

    renderToolbar(wrap);
    renderLoading(wrap);

    // State subscription: range changes from the global controls strip too.
    if (typeof XU.onState === 'function') {
      if (typeof view.offState === 'function') { view.offState(); }
      view.offState = XU.onState(function () {
        if (gen !== view.gen) { return; }
        view.range = XU.state.range || 'all';
        renderToolbar(wrap);
        load(wrap, gen);
      });
    }

    wrap.addEventListener('click', function (ev) {
      if (gen !== view.gen) { return; }
      var rb = ev.target.closest ? ev.target.closest('[data-range]') : null;
      if (rb) {
        view.range = rb.getAttribute('data-range');
        XU.setState({ range: view.range, since: '', until: '' });
        return;
      }
      var act = ev.target.closest ? ev.target.closest('[data-act]') : null;
      if (act) {
        var canvas = wrap.querySelector('canvas.sh-canvas');
        if (!canvas) { return; }
        if (act.getAttribute('data-act') === 'download') { download(canvas); }
        else { copy(canvas); }
      }
    });

    load(wrap, gen);
  }

  function unmount() {
    view.gen++;
    view.rows = [];
    if (typeof view.offState === 'function') { view.offState(); view.offState = null; }
  }

  window.XU.registerView('share', { mount: mount, unmount: unmount });
})();
