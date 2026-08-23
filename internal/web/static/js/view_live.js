// view_live.js — Live Stream view (SPEC §9.2, redesigned per §13).
//
// Realtime monitor polling GET /api/live every 5 seconds:
//   - today's cost/token/message counters with ▲▼ delta chips per poll;
//   - a latest-entry attribution strip: pill-cli + pill-model tags for the
//     newest entry (latest{provider,model,at}) with a relative age;
//   - the active 5-hour window as a burn-rate arc gauge with elapsed meter,
//     projection and a usage-limit-reset warning banner;
//   - a ticker of the ten most recent sessions (GET /api/sessions?limit=10).
//
// Polling pauses while the tab is hidden (visibilitychange), handles being
// mounted while hidden, and is fully torn down on unmount: interval, listener,
// and the view-scoped <style> tag. The streaming dot mirrors poll health:
// green while polls succeed, amber while paused, red after a failure.
// Styling reuses the design-system classes (.card, .kpi, .grid.kpis, .table,
// .pill, .delta.chip, .banner, .dot, .empty, .skeleton); only view-specific
// bits (latest strip, kv rows, arc gauge, dot states) are scoped under
// .xu-live in an injected style tag.
(function () {
  'use strict';

  if (!window.XU || typeof XU.registerView !== 'function') {
    console.warn('[xusage] view_live: XU global missing; view not registered');
    return;
  }

  var POLL_MS = 5000;
  var STYLE_ID = 'xu-view-live-style';

  // GSAP semantic token ramp (§13.0): green brand accent, cream + muted steps.
  var SEM = {
    input: '#0ae448',
    output: '#fffce1',
    cacheRead: '#7c7c6f',
    cacheCreate: '#42433d'
  };

  // ------------------------------------------------------------------ state

  var root = null;        // mounted container element
  var timer = null;       // poll interval handle
  var busy = false;       // a poll request is in flight
  var prevToday = null;   // previous poll's today{} for delta computation
  var refs = {};          // cached [data-ref] element lookups

  // ------------------------------------------------------------- utilities

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // RFC3339Nano → ms; trim fractional seconds to ms precision for Date.parse.
  function parseTs(iso) {
    if (!iso) { return NaN; }
    return Date.parse(String(iso).replace(/(\.\d{3})\d+/, '$1'));
  }

  function clamp(v, lo, hi) { return Math.min(Math.max(v, lo), hi); }

  function pad2(n) { return (n < 10 ? '0' : '') + n; }

  function fmtClock(iso) {
    var ms = parseTs(iso);
    if (isNaN(ms)) { return '—'; }
    var d = new Date(ms);
    return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }

  // Compact relative age ("42s", "7m", "3h", "2d") against a reference time.
  function fmtAgo(iso, refMs) {
    var ms = parseTs(iso);
    if (isNaN(ms) || isNaN(refMs)) { return '—'; }
    var s = Math.floor((refMs - ms) / 1000);
    if (s < 0) { return 'now'; }
    if (s < 60) { return s + 's'; }
    if (s < 3600) { return Math.floor(s / 60) + 'm'; }
    if (s < 86400) { return Math.floor(s / 3600) + 'h'; }
    return Math.floor(s / 86400) + 'd';
  }

  // Minutes → "3h 24m" / "42m" / "0m".
  function fmtDurMin(mins) {
    mins = Math.max(0, Math.round(mins || 0));
    var hCount = Math.floor(mins / 60);
    var m = mins % 60;
    if (hCount <= 0) { return m + 'm'; }
    return hCount + 'h ' + pad2(m) + 'm';
  }

  function big(n) {
    if (XU.fmt && typeof XU.fmt.big === 'function') { return XU.fmt.big(n); }
    return XU.fmt.tokens(n);
  }

  function totalTokens(o) {
    if (!o) { return 0; }
    return (o.inputTokens || 0) + (o.outputTokens || 0) +
      (o.cacheCreationTokens || 0) + (o.cacheReadTokens || 0) +
      (o.extraTotalTokens || 0);
  }

  // Attribution pills: prefers the app-core XU.pill(cli, model) helper; the
  // fallback emits the exact same style.css pill markup.
  function pillsHTML(cli, model) {
    if (typeof XU.pill === 'function') { return XU.pill(cli, model); }
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

  function kv(label, value) {
    return '<div class="xu-kv"><span class="k">' + label +
      '</span><span class="v">' + value + '</span></div>';
  }

  // Signed delta chip vs the previous poll; empty when no baseline or no change.
  function deltaChip(cur, prev, fmtAbs) {
    if (prev == null) { return ''; }
    var d = cur - prev;
    if (!isFinite(d) || d === 0) { return ''; }
    var cls = d > 0 ? 'up' : 'down';
    var arrow = d > 0 ? '▲' : '▼';
    return '<span class="delta chip ' + cls + '" title="since last poll">' +
      arrow + ' ' + fmtAbs(Math.abs(d)) + '</span>';
  }

  // ------------------------------------------------------------------- css

  // View-specific styles only; the design-system classes come from style.css.
  // Palette vars carry literal fallbacks for robustness.
  var VIEW_CSS = [
    '.xu-live{display:flex;flex-direction:column;gap:12px;}',
    '.xu-live .xu-eyebrow{font-family:var(--font,"Inter",-apple-system,"Segoe UI",Roboto,sans-serif);font-size:16px;font-weight:400;color:#fffce1;margin-bottom:6px;letter-spacing:.01em}',
    // latest-entry attribution strip
    '.xu-latest{display:flex;align-items:center;gap:10px;padding:7px 12px;' +
      'border:1px solid var(--border,#42433d);border-radius:8px;background:var(--card,#191919);}',
    '.xu-latest .label{flex:0 0 auto;}',
    '.xu-latest .xu-latest-meta{margin-left:auto;font-size:12px;color:var(--faint,#7c7c6f);font-variant-numeric:tabular-nums;white-space:nowrap;}',
    // streaming status dot states (base .dot comes from style.css)
    '.xu-live .dot{width:8px;height:8px;border-radius:50%;flex:none;}',
    '.xu-live .dot.dot-live{background:var(--ok,#0ae448);' +
      'animation:xu-live-blink 1.5s infinite alternate;}',
    '.xu-live .dot.dot-paused{background:var(--warn,#7c7c6f);}',
    '.xu-live .dot.dot-err{background:var(--down,#42433d);}',
    '@keyframes xu-live-blink{0%{opacity:.4;}100%{opacity:1;}}',
    // poll-health line: green mono while streaming, muted otherwise
    '.xu-status{font-family:var(--mono,monospace);font-weight:700;' +
      'font-variant-numeric:tabular-nums;color:var(--faint,#7c7c6f);}',
    '.xu-status.is-healthy{color:var(--ok,#0ae448);}',
    // key-value rows (not part of the shared system)
    '.xu-kv{display:flex;justify-content:space-between;gap:12px;padding:7px 0;border-bottom:1px solid var(--border,#42433d);font-size:13px;}',
    '.xu-kv:last-of-type{border-bottom:none;}',
    '.xu-kv .k{color:var(--muted,#7c7c6f);}',
    '.xu-kv .v{font-variant-numeric:tabular-nums;text-align:right;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;}',
    // arc gauge geometry
    '.xu-gauge{width:100%;max-width:210px;display:block;margin:2px auto 6px;}',
    '.xu-gauge .track{fill:none;stroke:var(--border,#42433d);stroke-width:10;stroke-linecap:round;}',
    '.xu-gauge .value{fill:none;stroke:var(--accent,#0ae448);stroke-width:10;stroke-linecap:round;}',
    '.xu-gauge-big{fill:var(--text,#fffce1);font-size:20px;font-weight:600;font-family:var(--mono,monospace);font-variant-numeric:tabular-nums;}',
    '.xu-gauge-sub{fill:var(--muted,#7c7c6f);font-size:9px;letter-spacing:.08em;text-transform:uppercase;}',
    '.xu-meter-caption{display:flex;justify-content:space-between;font-size:11px;color:var(--faint,#7c7c6f);font-variant-numeric:tabular-nums;margin:4px 0 10px;}',
    // ticker
    '.xu-sess-id{max-width:130px;overflow:hidden;text-overflow:ellipsis;}',
    // skeleton lines
    '.xu-sk-line{height:12px;margin:10px 0;}'
  ].join('');

  function injectStyle() {
    if (document.getElementById(STYLE_ID)) { return; }
    var el = document.createElement('style');
    el.setAttribute('id', STYLE_ID);
    el.textContent = VIEW_CSS;
    document.head.appendChild(el);
  }

  function removeStyle() {
    var el = document.getElementById(STYLE_ID);
    if (el && el.parentNode) { el.parentNode.removeChild(el); }
  }

  // -------------------------------------------------------------- skeleton

  function skLine() { return '<div class="skeleton xu-sk-line"></div>'; }

  function kpiSkeleton(label, figRef, subRef) {
    return '<div class="card kpi">' +
      '<div class="kpi-label">' + label + '</div>' +
      '<div class="kpi-value" data-ref="' + figRef + '">' +
      '<span class="skeleton" style="display:inline-block;width:110px;height:24px"></span></div>' +
      '<div class="kpi-sub" data-ref="' + subRef + '"></div>' +
      '</div>';
  }

  var SKELETON_HTML =
    '<div class="xu-live">' +
    '<div class="xu-eyebrow">{ Live }</div>' +
    '<div class="row">' +
    '<span class="dot" data-ref="dot"></span>' +
    '<span class="label">Live Stream</span>' +
    '<span class="xu-status faint small" data-ref="status">connecting…</span>' +
    '<span class="right faint small tabular" data-ref="stamp"></span>' +
    '</div>' +
    '<div class="xu-latest" data-ref="latest" hidden></div>' +
    '<div class="grid kpis">' +
    kpiSkeleton('Cost Today', 'costFig', 'costSub') +
    kpiSkeleton('Tokens Today', 'tokFig', 'tokSub') +
    kpiSkeleton('Messages Today', 'msgFig', 'msgSub') +
    '</div>' +
    '<div class="grid cols-2">' +
    '<section class="card"><div class="card-title"><span>Active Window</span></div>' +
    '<div data-ref="blockBody">' + skLine() + skLine() + skLine() + '</div></section>' +
    '<section class="card"><div class="card-title"><span>Recent Sessions</span>' +
    '<span class="faint small" data-ref="sessNote"></span></div>' +
    '<div data-ref="sessBody">' + skLine() + skLine() + skLine() + '</div></section>' +
    '</div>' +
    '</div>';

  // -------------------------------------------------------------- rendering

  function setDot(mode) {
    if (refs.dot) {
      refs.dot.className = 'dot' + (mode ? ' dot-' + mode : '');
    }
  }

  // Update one KPI figure in place; flash it softly when the value changed.
  function setFigure(figRef, subRef, text, chip) {
    var fig = refs[figRef];
    var sub = refs[subRef];
    if (!fig) { return; }
    if (fig.textContent !== text) {
      fig.textContent = text;
      if (fig.animate) {
        fig.animate([{ opacity: 0.35 }, { opacity: 1 }],
          { duration: 300, easing: 'ease-out' });
      }
    }
    if (sub) { sub.innerHTML = chip || ''; }
  }

  // Semicircular arc path for fraction frac (0..1), left → right over the top.
  function arcPath(cx, cy, r, frac) {
    frac = clamp(frac || 0, 0, 1);
    if (frac >= 0.9995) {
      return 'M ' + (cx - r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 0 1 ' + (cx + r) + ' ' + cy;
    }
    var a = Math.PI * (1 - frac);
    var x = (cx + r * Math.cos(a)).toFixed(2);
    var y = (cy - r * Math.sin(a)).toFixed(2);
    return 'M ' + (cx - r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 0 1 ' + x + ' ' + y;
  }

  // Latest-entry attribution strip (§13.5 latest{provider,model,at}).
  function latestHTML(latest, refMs) {
    if (!latest || (!latest.provider && !latest.model)) {
      refs.latest.hidden = true;
      return;
    }
    refs.latest.hidden = false;
    refs.latest.innerHTML =
      '<span class="label">Latest</span>' +
      '<span class="pill-row">' + pillsHTML(latest.provider, latest.model) + '</span>' +
      '<span class="xu-latest-meta">' + esc(fmtAgo(latest.at, refMs)) +
      (isNaN(parseTs(latest.at)) ? '' : ' · ' + esc(XU.fmt.datetime(latest.at))) +
      '</span>';
  }

  function blockHTML(b, refMs) {
    if (!b) {
      return '<div class="empty">No active window — a new 5h block opens ' +
        'with the next entry.</div>';
    }
    var startMs = parseTs(b.startTime);
    var endMs = parseTs(b.endTime);
    var dur = isNaN(startMs) || isNaN(endMs) ? 0 : Math.max(endMs - startMs, 1);
    var frac = dur ? clamp((refMs - startMs) / dur, 0, 1) : 0;
    var burn = b.burnRate || { tokensPerMinute: 0, costPerHour: 0 };
    var proj = b.projection;

    var rows =
      kv('Burn rate', esc(big(burn.tokensPerMinute || 0)) + ' tok/min') +
      kv('Burn cost', esc(XU.fmt.cost(burn.costPerHour || 0)) + ' /hr') +
      kv('Window tokens', esc(XU.fmt.tokens(totalTokens(b)))) +
      kv('Window cost', esc(XU.fmt.cost(b.cost || 0))) +
      kv('Messages', esc(XU.fmt.num(b.messages || 0)));
    if (proj) {
      rows +=
        kv('Projected tokens', esc(XU.fmt.tokens(proj.totalTokens || 0))) +
        kv('Projected cost', esc(XU.fmt.cost(proj.totalCost || 0))) +
        kv('Ends in', esc(fmtDurMin(proj.remainingMinutes)));
    }

    var warn = '';
    if (b.usageLimitReset) {
      warn = '<div class="banner" style="margin-top:12px">Usage limit reached — resets ' +
        esc(XU.fmt.datetime(b.usageLimitReset)) + '</div>';
    }

    var models = '';
    if (b.models && b.models.length) {
      var names = b.models.slice(0, 4);
      models = '<div class="pill-row" style="margin-top:10px">' +
        names.map(function (m) { return pillsHTML(null, m); }).join('') +
        (b.models.length > names.length
          ? '<span class="faint small">+' + (b.models.length - names.length) + '</span>'
          : '') +
        '</div>';
    }

    return '<svg class="xu-gauge" viewBox="0 0 140 82" role="img" ' +
      'aria-label="Active window burn gauge">' +
      '<path class="track" d="' + arcPath(70, 70, 56, 1) + '"></path>' +
      '<path class="value" d="' + arcPath(70, 70, 56, frac) + '"></path>' +
      '<text class="xu-gauge-big" x="70" y="56" text-anchor="middle">' +
      esc(big(burn.tokensPerMinute || 0)) + '</text>' +
      '<text class="xu-gauge-sub" x="70" y="70" text-anchor="middle">tok / min</text>' +
      '</svg>' +
      '<div class="meter" aria-hidden="true"><i style="width:' +
      (frac * 100).toFixed(1) + '%"></i></div>' +
      '<div class="xu-meter-caption"><span>elapsed ' +
      esc(fmtDurMin((frac * dur) / 60000)) + ' of 5h</span><span>' +
      Math.round(frac * 100) + '%</span></div>' +
      rows + models + warn;
  }

  function sessionsHTML(rows, refMs) {
    if (rows === null) { return null; } // ticker fetch failed: keep last good
    if (!rows || !rows.length) {
      return '<div class="empty">No sessions recorded yet.</div>';
    }
    var body = rows.map(function (r) {
      var id = r.key || '(no session)';
      var short = id.length > 14 ? id.slice(0, 14) + '…' : id;
      return '<tr title="' + esc(id) + '">' +
        '<td class="xu-sess-id">' + esc(short) + '</td>' +
        '<td class="num">' + esc(XU.fmt.num(r.messages || 0)) + '</td>' +
        '<td class="num">' + esc(XU.fmt.tokens(totalTokens(r))) + '</td>' +
        '<td class="num">' + esc(XU.fmt.cost(r.cost || 0)) + '</td>' +
        '<td class="num muted">' + esc(fmtAgo(r.lastActivity, refMs)) + '</td>' +
        '</tr>';
    }).join('');
    return '<table class="table mini"><thead><tr>' +
      '<th>Session</th><th class="num">Msgs</th><th class="num">Tokens</th>' +
      '<th class="num">Cost</th><th class="num">Last</th>' +
      '</tr></thead><tbody>' + body + '</tbody></table>';
  }

  function render(data, sessions) {
    if (!root) { return; }
    var refMs = parseTs(data.updatedAt);
    if (isNaN(refMs)) { refMs = Date.now(); }

    var today = data.today || { cost: 0, tokens: 0, messages: 0 };
    setFigure('costFig', 'costSub', XU.fmt.cost(today.cost || 0),
      deltaChip(today.cost || 0, prevToday && prevToday.cost, function (v) { return XU.fmt.cost(v); }));
    setFigure('tokFig', 'tokSub', XU.fmt.tokens(today.tokens || 0),
      deltaChip(today.tokens || 0, prevToday && prevToday.tokens, function (v) { return XU.fmt.tokens(v); }));
    setFigure('msgFig', 'msgSub', XU.fmt.num(today.messages || 0),
      deltaChip(today.messages || 0, prevToday && prevToday.messages, function (v) { return XU.fmt.num(v); }));

    if (refs.latest) { latestHTML(data.latest, refMs); }
    if (refs.blockBody) { refs.blockBody.innerHTML = blockHTML(data.activeBlock, refMs); }
    if (refs.sessBody) {
      var html = sessionsHTML(sessions, refMs);
      if (html !== null) { // null = fetch failed; leave the last good table
        refs.sessBody.innerHTML = html;
        if (refs.sessNote && sessions && sessions.length) {
          refs.sessNote.textContent = 'last ' + sessions.length;
        }
      }
    }

    prevToday = { cost: today.cost || 0, tokens: today.tokens || 0, messages: today.messages || 0 };
  }

  // ----------------------------------------------------------------- polling

  function setStatus(text) {
    if (refs.status) {
      refs.status.textContent = text;
      // Healthy polls read like the reference's "● Polling daemon healthy"
      // line: emerald mono 700. Everything else stays muted.
      refs.status.classList.toggle('is-healthy', /^streaming/.test(text));
    }
  }

  function showError(err) {
    setDot('err');
    var msg = err && (err.message || (typeof err === 'string' ? err : ''));
    if (!msg) { msg = 'request failed'; }
    if (err && err.status) { msg = err.status + ': ' + msg; }
    setStatus(msg.length > 64 ? msg.slice(0, 64) + '…' : msg);
  }

  function poll() {
    if (!root || busy) { return; }
    busy = true;
    // /api/live drives the whole view; the ticker is best-effort — when it
    // fails we keep the last good table instead of dropping the fresh live
    // payload (render treats a null sessions result as "leave as-is").
    var qs = XU.q() || '';
    Promise.all([
      XU.fetch('/api/live'),
      // XU.q() serializes only filter state, so limit rides on separately;
      // the ticker is best-effort — failure keeps the last good table.
      XU.fetch('/api/sessions' + (qs ? qs + '&' : '?') + 'limit=10')
        .catch(function () { return null; }),
    ]).then(function (results) {
      if (!root) { return; } // unmounted while in flight
      render(results[0], results[1]);
      if (!document.hidden) {
        setDot('live');
        setStatus('streaming · ' + POLL_MS / 1000 + 's poll');
      }
      if (refs.stamp) { refs.stamp.textContent = 'updated ' + fmtClock(results[0].updatedAt); }
      busy = false;
    }, function (err) {
      busy = false;
      if (root) { showError(err); } // keep last good data on transient failures
    });
  }

  function startTimer() {
    stopTimer();
    timer = setInterval(poll, POLL_MS);
  }

  function stopTimer() {
    if (timer) { clearInterval(timer); timer = null; }
  }

  function onVisibility() {
    if (!root) { return; }
    if (document.hidden) {
      stopTimer();
      setDot('paused');
      setStatus('paused — tab hidden');
    } else {
      poll();
      startTimer();
    }
  }

  // ------------------------------------------------------------ registration

  XU.registerView('live', {
    mount: function (el) {
      // Defensive teardown in case of a remount without unmount.
      stopTimer();
      document.removeEventListener('visibilitychange', onVisibility);

      root = el;
      prevToday = null;
      injectStyle();
      el.innerHTML = SKELETON_HTML;

      refs = {};
      var nodes = el.querySelectorAll('[data-ref]');
      for (var i = 0; i < nodes.length; i++) {
        refs[nodes[i].getAttribute('data-ref')] = nodes[i];
      }

      document.addEventListener('visibilitychange', onVisibility);
      if (document.hidden) {
        // Mounted while hidden: stay paused; onVisibility starts the loop.
        setDot('paused');
        setStatus('paused — tab hidden');
      } else {
        poll();
        startTimer();
      }
    },

    unmount: function () {
      stopTimer();
      document.removeEventListener('visibilitychange', onVisibility);
      busy = false;
      removeStyle();
      root = null;
      refs = {};
      prevToday = null;
    },
  });
})();
