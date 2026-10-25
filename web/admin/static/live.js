/* GoPress live updates: one Server-Sent Events stream per admin page
   (/admin/events). Shows notices as toasts, keeps the moderation badges
   current and passes every event on as a "gp-live" DOM event
   ({type, data}) for the page's own scripts (editor, plugins screen,
   plugin pages). Texts are always set with textContent. */
(function () {
	'use strict';
	var body = document.body;
	var url = body.getAttribute('data-live');
	if (!url || !window.EventSource) { return; }
	var topics = body.getAttribute('data-live-topics') || '';
	var types = ['notice', 'comment.pending', 'plugin.paused', 'import.progress', 'post.presence', 'post.saved',
		'plugin.event', 'resync', 'logout'];

	// ---- Toasts ----
	var region = document.createElement('div');
	region.className = 'gp-toasts';
	region.setAttribute('role', 'status');
	region.setAttribute('aria-live', 'polite');
	body.appendChild(region);

	function toast(text, kind, link) {
		var t = document.createElement('div');
		t.className = 'gp-toast gp-toast-' + (kind || 'info');
		var p = document.createElement('p');
		p.textContent = text;
		t.appendChild(p);
		if (link) {
			var a = document.createElement('a');
			a.href = link.href;
			a.textContent = link.text;
			t.appendChild(a);
		}
		var x = document.createElement('button');
		x.type = 'button';
		x.className = 'gp-toast-close';
		x.setAttribute('aria-label', 'Dismiss');
		x.textContent = '×';
		x.addEventListener('click', function () { t.remove(); });
		t.appendChild(x);
		region.appendChild(t);
		while (region.children.length > 4) { region.firstChild.remove(); }
		if (kind !== 'error') { setTimeout(function () { t.remove(); }, 10000); }
	}
	window.gp = window.gp || {};
	window.gp.toast = toast;

	// ---- Moderation badges ----
	function setPending(n) {
		n = Math.max(0, parseInt(n, 10) || 0);
		document.querySelectorAll('.pending-count').forEach(function (el) {
			el.textContent = String(n);
			var badge = el.classList.contains('awaiting-mod') ? el : el.closest('.awaiting-mod');
			if (badge) {
				badge.className = badge.className.replace(/\bcount-\d+\b/, '') + ' count-' + n;
			}
		});
		document.querySelectorAll('.awaiting-mod .screen-reader-text, #wp-admin-bar-comments .screen-reader-text').forEach(function (el) {
			el.textContent = n + (n === 1 ? ' Comment' : ' Comments') + ' in moderation';
		});
	}

	var handlers = {
		'notice': function (d) { toast(d.source ? d.source + ': ' + d.text : d.text, d.kind); },
		'comment.pending': function (d) {
			setPending(d.pending);
			if (d.author) {
				toast('New comment by ' + d.author + ' awaits moderation.', 'info',
					{ href: '/admin/comments/?comment_status=pending', text: 'Moderate' });
			}
		},
		'plugin.paused': function (d) {
			toast('The plugin ' + d.title + ' was paused after repeated errors: ' + d.error, 'error',
				{ href: '/admin/plugins/', text: 'Plugins' });
		},
		'logout': function () {
			stop();
			toast('Your session has ended. Please log in again.', 'warning', { href: '/login', text: 'Log in' });
		}
	};

	// ---- Stream ----
	var es = null;
	var backoff = 5000;
	var stopped = false;
	function stop() { stopped = true; if (es) { es.close(); } }
	function connect() {
		if (stopped) { return; }
		es = new EventSource(url + (topics ? '?topics=' + encodeURIComponent(topics) : ''));
		types.forEach(function (type) {
			es.addEventListener(type, function (ev) {
				var data = {};
				try { data = JSON.parse(ev.data); } catch (e) { return; }
				backoff = 5000;
				if (handlers[type]) { handlers[type](data); }
				document.dispatchEvent(new CustomEvent('gp-live', { detail: { type: type, data: data } }));
			});
		});
		es.addEventListener('open', function () { backoff = 5000; });
		es.addEventListener('error', function () {
			// The browser retries by itself unless the server refused the
			// stream (closed): then back off, up to five minutes.
			if (es.readyState === EventSource.CLOSED && !stopped) {
				setTimeout(connect, backoff);
				backoff = Math.min(backoff * 2, 300000);
			}
		});
	}
	window.addEventListener('pagehide', stop);
	window.addEventListener('pageshow', function (ev) {
		if (ev.persisted) { stopped = false; connect(); } // back from the page cache
	});
	connect();
})();
