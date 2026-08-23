// Overview view — redesigned dark-gray palette day-by-day browser.
//
//   Hero: all-time token volume at display scale under a section heading,
//   plus sessions · days · messages sub-line.
//
//   Day browser: left/right arrow navigation through days with data in the
//   selected range. Each day shows a header ("Saturday - 22.08.2026"),
//   total tokens for the day, and a per-model breakdown table sorted by
//   total tokens (descending): model name, total, input, cached input, output.
//
//   Data: /api/summary (hero) + /api/models (all-time breakdown) + /api/daily (range-filtered rows). The global
//   range selector in the controls strip drives XU.state.range; this view
//   re-fetches on state change via XU.onState.

(function () {
  'use strict';

  if (!window.XU || typeof window.XU.registerView !== 'function') {
    return;
  }

  var fmt = window.XU.fmt;
  var q = window.XU.q;

  // ---------------------------------------------------------------------------
  // Palette (darker gray, cool blue accent)
  // ---------------------------------------------------------------------------

  var COLORS = {
    canvas: '#0a0a0a',
    card: '#111111',
    elevated: '#161616',
    border: '#1e1e1e',
    text: '#e0e0e0',
    muted: '#808080',
    accent: '#4a9eff',
    success: '#22c55e',
    dim: '#555555'
  };

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function qs(extra) {
    var s = q(extra || {});
    if (!s) { return ''; }
    return s.charAt(0) === '?' ? s : '?' + s;
  }

  function big(n) {
    if (fmt && typeof fmt.big === 'function') { return fmt.big(n); }
    return fmt.tokens(n);
  }

  // Total tokens for a daily row.
  function rowTotal(r) {
    return (r.inputTokens || 0) + (r.outputTokens || 0) +
      (r.cacheCreationTokens || 0) + (r.cacheReadTokens || 0) +
      (r.extraTotalTokens || 0);
  }

  // Total tokens for a model breakdown entry.
  function modelTotal(m) {
    return (m.inputTokens || 0) + (m.outputTokens || 0) +
      (m.cacheCreationTokens || 0) + (m.cacheReadTokens || 0) +
      (m.extraTotalTokens || 0);
  }

  // Format "2026-08-22" → "Saturday - 22.08.2026"
  function formatDayHeader(key) {
    if (!key || key.length < 10) { return key || ''; }
    var days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday',
                'Thursday', 'Friday', 'Saturday'];
    var parts = key.split('-');
    var d = new Date(
      parseInt(parts[0], 10),
      parseInt(parts[1], 10) - 1,
      parseInt(parts[2], 10)
    );
    var dayName = days[d.getDay()];
    var dd = parts[2];
    var mm = parts[1];
    var yyyy = parts[0];
    return dayName + ' - ' + dd + '.' + mm + '.' + yyyy;
  }

  // ---------------------------------------------------------------------------
  // Scoped styles (injected once)
  // ---------------------------------------------------------------------------

  var STYLE_ID = 'xu-ov-redesign-css';

  function ensureStyles() {
    if (document.getElementById(STYLE_ID)) { return; }
    var s = document.createElement('style');
    s.id = STYLE_ID;
    s.textContent = [
      // Hero section
      '.ov-hero{padding:32px 0 24px;border-bottom:1px solid ' + COLORS.border + ';margin-bottom:24px;}',
      '.ov-hero-label{font-size:12px;font-family:var(--mono,monospace);',
      'text-transform:uppercase;letter-spacing:0.08em;color:' + COLORS.muted + ';margin-bottom:8px;}',
      '.ov-hero-num{font-size:56px;font-weight:600;line-height:1;color:' + COLORS.text + ';',
      'font-variant-numeric:tabular-nums;letter-spacing:-0.03em;',
      'font-family:var(--mono,monospace);}',
      '.ov-hero-num .ov-unit{color:' + COLORS.accent + ';}',
      '.ov-hero-meta{margin-top:10px;font-size:13px;color:' + COLORS.muted + ';',
      'font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;}',

      // Day browser navigation
      '.ov-day-nav{display:flex;align-items:center;gap:12px;margin-bottom:20px;}',
      '.ov-day-arrows{display:flex;gap:4px;}',
      '.ov-day-arrow{width:36px;height:36px;display:flex;align-items:center;justify-content:center;',
      'border:1px solid ' + COLORS.border + ';border-radius:6px;background:' + COLORS.card + ';',
      'color:' + COLORS.text + ';cursor:pointer;font-size:16px;',
      'transition:background 0.15s,border-color 0.15s;}',
      '.ov-day-arrow:hover{background:' + COLORS.elevated + ';border-color:' + COLORS.accent + ';}',
      '.ov-day-arrow:disabled{opacity:0.3;cursor:default;border-color:' + COLORS.border + ';}',
      '.ov-day-arrow:disabled:hover{background:' + COLORS.card + ';}',
      '.ov-day-info{display:flex;align-items:baseline;gap:16px;flex:1;min-width:0;}',
      '.ov-day-title{font-size:18px;font-weight:600;color:' + COLORS.text + ';white-space:nowrap;}',
      '.ov-day-tokens{font-size:13px;color:' + COLORS.accent + ';',
      'font-family:var(--mono,monospace);white-space:nowrap;font-variant-numeric:tabular-nums;}',

      // Range pills (inline in the overview header area)
      '.ov-range{display:flex;gap:4px;margin-left:auto;flex-shrink:0;}',
      '.ov-range-btn{height:28px;padding:0 12px;border:1px solid ' + COLORS.border + ';',
      'border-radius:6px;background:' + COLORS.card + ';color:' + COLORS.muted + ';',
      'font-size:12px;font-weight:500;cursor:pointer;',
      'font-family:var(--font,system-ui,sans-serif);',
      'transition:background 0.15s,color 0.15s,border-color 0.15s;}',
      '.ov-range-btn:hover{color:' + COLORS.text + ';border-color:' + COLORS.dim + ';}',
      '.ov-range-btn.active{background:' + COLORS.accent + ';color:#fff;',
      'border-color:' + COLORS.accent + ';}',

      // Model breakdown table
      '.ov-models{background:' + COLORS.card + ';border:1px solid ' + COLORS.border + ';',
      'border-radius:8px;overflow:hidden;}',
      '.ov-models-head{display:grid;grid-template-columns:1fr 100px 100px 100px 100px;',
      'gap:0;padding:10px 16px;border-bottom:1px solid ' + COLORS.border + ';',
      'font-size:11px;font-family:var(--mono,monospace);text-transform:uppercase;',
      'letter-spacing:0.06em;color:' + COLORS.muted + ';font-weight:500;}',
      '.ov-models-head span{text-align:right;}',
      '.ov-models-head span:first-child{text-align:left;}',
      '.ov-model-row{display:grid;grid-template-columns:1fr 100px 100px 100px 100px;',
      'gap:0;padding:10px 16px;border-bottom:1px solid ' + COLORS.border + ';',
      'font-size:13px;font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;',
      'align-items:center;transition:background 0.1s;}',
      '.ov-model-row:last-child{border-bottom:none;}',
      '.ov-model-row:hover{background:' + COLORS.elevated + ';}',
      '.ov-model-name{color:' + COLORS.text + ';overflow:hidden;',
      'text-overflow:ellipsis;white-space:nowrap;padding-right:12px;font-weight:500;}',
      '.ov-model-val{color:' + COLORS.text + ';text-align:right;}',
      '.ov-model-val.muted{color:' + COLORS.muted + ';}',

      // Empty state
      '.ov-empty{padding:48px 16px;text-align:center;color:' + COLORS.muted + ';',
      'font-size:14px;border:1px dashed ' + COLORS.border + ';border-radius:8px;',
      'background:' + COLORS.card + ';}',
      '.ov-empty-day{padding:40px 16px;text-align:center;color:' + COLORS.muted + ';font-size:14px;}',

      // Skeleton
      '.ov-skel{background:' + COLORS.elevated + ';border-radius:4px;',
      'animation:xu-ov-shim 1.4s ease-in-out infinite;}',
      '@keyframes xu-ov-shim{0%{opacity:0.4;}50%{opacity:0.7;}100%{opacity:0.4;}}',

      // Date count indicator
      '.ov-day-count{font-size:12px;color:' + COLORS.muted + ';',
      'font-family:var(--mono,monospace);white-space:nowrap;}',

      // All-time model breakdown
      '.ov-alltime-section{margin-top:24px}',
      '.ov-alltime-label{font-size:12px;font-family:var(--mono,monospace);text-transform:uppercase;letter-spacing:0.08em;color:' + COLORS.muted + ';margin-bottom:12px}',
      '.ov-alltime{background:' + COLORS.card + ';border:1px solid ' + COLORS.border + ';border-radius:8px;overflow:hidden}',
      '.ov-alltime-head{display:grid;grid-template-columns:1fr 100px 100px 100px 100px;gap:0;padding:10px 16px;border-bottom:1px solid ' + COLORS.border + ';font-size:11px;font-family:var(--mono,monospace);text-transform:uppercase;letter-spacing:0.06em;color:' + COLORS.muted + ';font-weight:500}',
      '.ov-alltime-head span{text-align:right}',
      '.ov-alltime-head span:first-child{text-align:left}',
      '.ov-alltime-row{display:grid;grid-template-columns:1fr 100px 100px 100px 100px;gap:0;padding:10px 16px;border-bottom:1px solid ' + COLORS.border + ';font-size:13px;font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;align-items:center;transition:background 0.1s}',
      '.ov-alltime-row:last-child{border-bottom:none}',
      '.ov-alltime-row:hover{background:' + COLORS.elevated + '}',
      '.ov-alltime-name{color:' + COLORS.text + ';overflow:hidden;text-overflow:ellipsis;white-space:nowrap;padding-right:12px;font-weight:500}',
      '.ov-alltime-val{color:' + COLORS.text + ';text-align:right}',
      '.ov-alltime-val.muted{color:' + COLORS.muted + '}'
    ].join('');
    document.head.appendChild(s);
  }

  // ---------------------------------------------------------------------------
  // View state
  // ---------------------------------------------------------------------------

  var view = {
    gen: 0,
    offState: null,
    dayIndex: -1,   // current position in the daily rows array
    dailyRows: [],  // cached array of daily rows from /api/daily
    range: 'all'    // current range for the inline selector
  };

  // Range options for the inline range pills
  var RANGES = ['1d', '3d', '7d', '30d', '90d', 'all'];

  // ---------------------------------------------------------------------------
  // DOM builders
  // ---------------------------------------------------------------------------

  function heroSection(summary) {
    var hero = document.createElement('div');
    hero.className = 'ov-hero';

    var label = document.createElement('div');
    label.className = 'ov-hero-label';
    label.textContent = 'All-Time Tokens';
    hero.appendChild(label);

    var numWrap = document.createElement('div');
    numWrap.setAttribute('data-ov', 'hero-num');

    var raw = big(summary.totalTokens || 0);
    var m = String(raw).match(/^(-?[\d,.]+)\s*([A-Za-z]*)$/);
    if (m && m[2]) {
      var num = document.createElement('span');
      num.className = 'hm-num';
      num.textContent = m[1];
      numWrap.appendChild(num);
      var unit = document.createElement('span');
      unit.className = 'ov-unit';
      unit.textContent = m[2];
      numWrap.appendChild(unit);
    } else {
      numWrap.textContent = raw;
    }
    numWrap.className = 'ov-hero-num';
    hero.appendChild(numWrap);

    var meta = document.createElement('div');
    meta.className = 'ov-hero-meta';
    meta.setAttribute('data-ov', 'hero-meta');
    meta.textContent = fmt.num(summary.sessions || 0) + ' sessions \u00b7 ' +
      fmt.num(summary.days || 0) + ' days \u00b7 ' +
      fmt.num(summary.messages || 0) + ' messages';
    hero.appendChild(meta);

    return hero;
  }

  // Day-by-day browser navigation bar
  function dayBrowser() {
    var nav = document.createElement('div');
    nav.className = 'ov-day-nav';

    var arrows = document.createElement('div');
    arrows.className = 'ov-day-arrows';

    var prevBtn = document.createElement('button');
    prevBtn.type = 'button';
    prevBtn.className = 'ov-day-arrow';
    prevBtn.setAttribute('data-ov', 'prev');
    prevBtn.textContent = '\u2190';
    prevBtn.title = 'Previous day';
    arrows.appendChild(prevBtn);

    var nextBtn = document.createElement('button');
    nextBtn.type = 'button';
    nextBtn.className = 'ov-day-arrow';
    nextBtn.setAttribute('data-ov', 'next');
    nextBtn.textContent = '\u2192';
    nextBtn.title = 'Next day';
    arrows.appendChild(nextBtn);

    nav.appendChild(arrows);

    var info = document.createElement('div');
    info.className = 'ov-day-info';

    var title = document.createElement('span');
    title.className = 'ov-day-title';
    title.setAttribute('data-ov', 'day-title');
    info.appendChild(title);

    var tokens = document.createElement('span');
    tokens.className = 'ov-day-tokens';
    tokens.setAttribute('data-ov', 'day-tokens');
    info.appendChild(tokens);

    var count = document.createElement('span');
    count.className = 'ov-day-count';
    count.setAttribute('data-ov', 'day-count');
    info.appendChild(count);

    nav.appendChild(info);

    return nav;
  }

  // Per-model breakdown table for a single day
  function modelsTable(models) {
    var wrap = document.createElement('div');
    wrap.className = 'ov-models';

    // Header row
    var head = document.createElement('div');
    head.className = 'ov-models-head';
    head.appendChild(document.createTextNode('Model'));
    ['Total', 'Input', 'Cached', 'Output'].forEach(function (label) {
      var span = document.createElement('span');
      span.textContent = label;
      head.appendChild(span);
    });
    wrap.appendChild(head);

    if (!models || !models.length) {
      var empty = document.createElement('div');
      empty.className = 'ov-empty-day';
      empty.textContent = 'No model usage for this day';
      wrap.appendChild(empty);
      return wrap;
    }

    // Sort by total tokens descending
    var sorted = models.slice().sort(function (a, b) {
      return modelTotal(b) - modelTotal(a);
    });

    sorted.forEach(function (m) {
      var row = document.createElement('div');
      row.className = 'ov-model-row';

      var name = document.createElement('div');
      name.className = 'ov-model-name';
      name.textContent = m.model || 'unknown';
      name.title = m.model || 'unknown';
      row.appendChild(name);

      var total = document.createElement('div');
      total.className = 'ov-model-val';
      total.textContent = big(modelTotal(m));
      row.appendChild(total);

      var input = document.createElement('div');
      input.className = 'ov-model-val muted';
      input.textContent = big(m.inputTokens || 0);
      row.appendChild(input);

      var cached = document.createElement('div');
      cached.className = 'ov-model-val muted';
      cached.textContent = big(m.cacheReadTokens || 0);
      row.appendChild(cached);

      var output = document.createElement('div');
      output.className = 'ov-model-val muted';
      output.textContent = big(m.outputTokens || 0);
      row.appendChild(output);

      wrap.appendChild(row);
    });

    return wrap;
  }

  // All-time model breakdown table (fetched from /api/models)
  function allTimeModelTable(models) {
    var wrap = document.createElement('div');
    wrap.className = 'ov-alltime';

    // Header row
    var head = document.createElement('div');
    head.className = 'ov-alltime-head';
    head.appendChild(document.createTextNode('Model'));
    ['Total', 'Input', 'Cached', 'Output'].forEach(function (label) {
      var span = document.createElement('span');
      span.textContent = label;
      head.appendChild(span);
    });
    wrap.appendChild(head);

    if (!models || !models.length) {
      var empty = document.createElement('div');
      empty.className = 'ov-empty-day';
      empty.textContent = 'No model usage data';
      wrap.appendChild(empty);
      return wrap;
    }

    // Sort by total tokens descending
    var sorted = models.slice().sort(function (a, b) {
      return modelTotal(b) - modelTotal(a);
    });

    sorted.forEach(function (m) {
      var row = document.createElement('div');
      row.className = 'ov-alltime-row';

      var name = document.createElement('div');
      name.className = 'ov-alltime-name';
      name.textContent = m.key || 'unknown';
      name.title = m.key || 'unknown';
      row.appendChild(name);

      var total = document.createElement('div');
      total.className = 'ov-alltime-val';
      total.textContent = big(modelTotal(m));
      row.appendChild(total);

      var input = document.createElement('div');
      input.className = 'ov-alltime-val muted';
      input.textContent = big(m.inputTokens || 0);
      row.appendChild(input);

      var cached = document.createElement('div');
      cached.className = 'ov-alltime-val muted';
      cached.textContent = big(m.cacheReadTokens || 0);
      row.appendChild(cached);

      var output = document.createElement('div');
      output.className = 'ov-alltime-val muted';
      output.textContent = big(m.outputTokens || 0);
      row.appendChild(output);

      wrap.appendChild(row);
    });

    return wrap;
  }

  // Skeleton loader
  function renderSkeleton(root) {
    root.innerHTML = '';
    var hero = document.createElement('div');
    hero.className = 'ov-hero';
    var label = document.createElement('div');
    label.className = 'ov-hero-label';
    label.textContent = 'All-Time Tokens';
    hero.appendChild(label);
    var num = document.createElement('div');
    num.className = 'ov-hero-num';
    var sk = document.createElement('div');
    sk.className = 'ov-skel';
    sk.style.cssText = 'width:35%;height:50px;';
    num.appendChild(sk);
    hero.appendChild(num);
    var meta = document.createElement('div');
    meta.className = 'ov-hero-meta';
    meta.textContent = 'sessions \u00b7 days \u00b7 messages';
    hero.appendChild(meta);
    root.appendChild(hero);

    var browser = dayBrowser();
    root.appendChild(browser);

    var skTable = document.createElement('div');
    skTable.className = 'ov-models';
    for (var i = 0; i < 5; i++) {
      var skRow = document.createElement('div');
      skRow.className = 'ov-model-row';
      skRow.style.opacity = '0.5';
      var skName = document.createElement('div');
      skName.className = 'ov-model-name';
      skName.innerHTML = '<div class="ov-skel" style="width:60%;height:14px;"></div>';
      skRow.appendChild(skName);
      for (var c = 0; c < 4; c++) {
        var skCell = document.createElement('div');
        skCell.className = 'ov-model-val';
        skCell.innerHTML = '<div class="ov-skel" style="width:50%;height:14px;margin-left:auto;"></div>';
        skRow.appendChild(skCell);
      }
      skTable.appendChild(skRow);
    }
    root.appendChild(skTable);
  }

  function renderFatal(root, message) {
    root.innerHTML = '';
    var d = document.createElement('div');
    d.className = 'ov-empty';
    d.textContent = message;
    root.appendChild(d);
  }

  // ---------------------------------------------------------------------------
  // Paint the day view (called whenever dayIndex or data changes)
  // ---------------------------------------------------------------------------

  function paintDay(root) {
    var rows = view.dailyRows;
    var idx = view.dayIndex;

    // Update arrow states
    var prevBtn = root.querySelector('[data-ov=prev]');
    var nextBtn = root.querySelector('[data-ov=next]');
    if (prevBtn) { prevBtn.disabled = idx <= 0; }
    if (nextBtn) { nextBtn.disabled = idx >= rows.length - 1; }

    var titleEl = root.querySelector('[data-ov=day-title]');
    var tokensEl = root.querySelector('[data-ov=day-tokens]');
    var countEl = root.querySelector('[data-ov=day-count]');

    if (!rows.length) {
      if (titleEl) { titleEl.textContent = 'No data'; }
      if (tokensEl) { tokensEl.textContent = ''; }
      if (countEl) { countEl.textContent = ''; }
      // Remove old models table, show empty
      var oldModels = root.querySelector('.ov-models');
      if (oldModels) {
        var empty = document.createElement('div');
        empty.className = 'ov-empty';
        empty.textContent = 'No usage data for the selected range';
        oldModels.parentNode.replaceChild(empty, oldModels);
      }
      return;
    }

    var row = rows[idx];
    if (titleEl) { titleEl.textContent = formatDayHeader(row.key); }
    if (tokensEl) { tokensEl.textContent = big(rowTotal(row)) + ' tokens'; }
    if (countEl) { countEl.textContent = (idx + 1) + ' / ' + rows.length; }

    // Replace models table
    var oldTable = root.querySelector('.ov-models');
    var table = modelsTable(row.models);
    if (oldTable) {
      oldTable.parentNode.replaceChild(table, oldTable);
    } else {
      root.appendChild(table);
    }
  }

  // ---------------------------------------------------------------------------
  // Data loading
  // ---------------------------------------------------------------------------

  function fetchAll() {
    return Promise.allSettled([
      XU.fetch('/api/summary' + qs()),   // 0
      XU.fetch('/api/daily' + qs()),     // 1
      XU.fetch('/api/models')            // 2
    ]);
  }

  function settled(list, i) {
    return list && list[i] && list[i].status === 'fulfilled' ? list[i].value : null;
  }

  function paint(root, data) {
    var summary = settled(data, 0);
    var daily = settled(data, 1) || [];

    if (!summary) {
      renderFatal(root, 'Failed to load overview data \u2014 is the xusage server running?');
      return;
    }

    root.innerHTML = '';

    // Hero
    root.appendChild(heroSection(summary));

    // All-time model breakdown
    var models = settled(data, 2);
    if (models) {
      var alltimeSection = document.createElement('div');
      alltimeSection.className = 'ov-alltime-section';
      var alltimeLabel = document.createElement('div');
      alltimeLabel.className = 'ov-alltime-label';
      alltimeLabel.textContent = 'All-Time Models';
      alltimeSection.appendChild(alltimeLabel);
      alltimeSection.appendChild(allTimeModelTable(models));
      root.appendChild(alltimeSection);
    }

    // Inline range selector (mirrors controls strip but embedded in the view)
    var rangeBar = document.createElement('div');
    rangeBar.style.cssText = 'display:flex;align-items:center;gap:8px;margin-top:24px;margin-bottom:20px;';
    var rangeWrap = document.createElement('div');
    rangeWrap.className = 'ov-range';
    RANGES.forEach(function (r) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'ov-range-btn' + (r === view.range ? ' active' : '');
      btn.setAttribute('data-ov-range', r);
      btn.textContent = r === 'all' ? 'All' : r;
      rangeWrap.appendChild(btn);
    });
    rangeBar.appendChild(rangeWrap);
    root.appendChild(rangeBar);

    // Day browser navigation
    root.appendChild(dayBrowser());

    // Empty state if no daily data
    if (!daily.length) {
      var empty = document.createElement('div');
      empty.className = 'ov-empty';
      empty.textContent = 'No usage data for the selected range';
      root.appendChild(empty);
      view.dailyRows = [];
      view.dayIndex = -1;
      return;
    }

    // Store daily rows and default to the most recent day
    view.dailyRows = daily;
    view.dayIndex = daily.length - 1;

    // Render the models table placeholder (paintDay will fill it)
    var tableWrap = document.createElement('div');
    tableWrap.className = 'ov-models';
    root.appendChild(tableWrap);

    // Now paint the current day
    paintDay(root);
  }

  // ---------------------------------------------------------------------------
  // Mount / unmount
  // ---------------------------------------------------------------------------

  function load(root, gen) {
    if (typeof XU.swr === 'function') {
      XU.swr('overview:' + XU.q(), fetchAll, function (data, stale) {
        if (gen !== view.gen) { return; }
        paint(root, data);
      });
    } else {
      fetchAll().then(function (data) {
        if (gen !== view.gen) { return; }
        paint(root, data);
      }).catch(function () {
        if (gen !== view.gen) { return; }
        renderFatal(root, 'Failed to load overview data');
      });
    }
  }

  function mount(el) {
    ensureStyles();
    view.gen++;
    var gen = view.gen;
    view.range = XU.state.range || 'all';
    renderSkeleton(el);

    // State change subscription
    if (typeof XU.onState === 'function') {
      try {
        if (typeof view.offState === 'function') { view.offState(); }
        view.offState = XU.onState(function () {
          if (gen !== view.gen) { return; }
          view.range = XU.state.range || 'all';
          load(el, gen);
        });
      } catch (e) { /* state notifications are best-effort */ }
    }

    // Click delegation for day arrows and range buttons
    el.addEventListener('click', function (ev) {
      if (gen !== view.gen) { return; }

      // Day arrow navigation
      var arrow = ev.target.closest ? ev.target.closest('[data-ov=prev],[data-ov=next]') : null;
      if (arrow) {
        var dir = arrow.getAttribute('data-ov') === 'prev' ? -1 : 1;
        var newIdx = view.dayIndex + dir;
        if (newIdx >= 0 && newIdx < view.dailyRows.length) {
          view.dayIndex = newIdx;
          paintDay(el);
        }
        return;
      }

      // Inline range selector
      var rangeBtn = ev.target.closest ? ev.target.closest('[data-ov-range]') : null;
      if (rangeBtn) {
        var r = rangeBtn.getAttribute('data-ov-range');
        XU.setState({ range: r, since: '', until: '' });
        return;
      }
    });

    load(el, gen);
  }

  function unmount() {
    view.gen++;
    view.dailyRows = [];
    view.dayIndex = -1;
    if (typeof view.offState === 'function') { view.offState(); view.offState = null; }
  }

  window.XU.registerView('overview', {
    mount: mount,
    unmount: unmount
  });
})();
