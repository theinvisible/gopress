/* GoPress admin: menu, notices, password toggles, confirmations and a
   fetch helper with the CSRF token. No inline handlers (strict CSP). */
(function () {
	'use strict';
	var body = document.body;
	body.classList.remove('no-js');
	body.classList.add('js');

	var csrf = body.getAttribute('data-csrf') || '';

	// gp.post(url, data) -> Promise<json>; data is a FormData or object.
	window.gp = window.gp || {};
	window.gp.csrf = csrf;
	window.gp.post = function (url, data) {
		var fd = data instanceof FormData ? data : new FormData();
		if (!(data instanceof FormData) && data) {
			Object.keys(data).forEach(function (k) {
				var v = data[k];
				if (Array.isArray(v)) { v.forEach(function (x) { fd.append(k, x); }); } else if (v !== undefined && v !== null) { fd.append(k, v); }
			});
		}
		return fetch(url, {
			method: 'POST', body: fd, credentials: 'same-origin',
			headers: { 'X-CSRF-Token': csrf, 'Accept': 'application/json' }
		}).then(function (r) {
			return r.json().catch(function () { return {}; }).then(function (j) {
				if (!r.ok) { var e = new Error(j.error || ('Request failed (' + r.status + ')')); e.status = r.status; e.data = j; throw e; }
				return j;
			});
		});
	};
	window.gp.get = function (url) {
		return fetch(url, { credentials: 'same-origin', headers: { 'Accept': 'application/json' } }).then(function (r) {
			return r.json().then(function (j) { if (!r.ok) { throw new Error(j.error || 'Request failed'); } return j; });
		});
	};

	// Collapse menu (remembered per browser).
	var collapse = document.getElementById('collapse-button');
	try { if (localStorage.getItem('gp-folded') === '1') { body.classList.add('folded'); } } catch (e) {}
	if (collapse) {
		collapse.addEventListener('click', function () {
			var folded = body.classList.toggle('folded');
			collapse.setAttribute('aria-expanded', folded ? 'false' : 'true');
			try { localStorage.setItem('gp-folded', folded ? '1' : '0'); } catch (e) {}
		});
	}

	// Responsive menu toggle in the admin bar.
	var toggle = document.querySelector('#wp-admin-bar-menu-toggle a');
	var wrap = document.getElementById('wpwrap');
	if (toggle && wrap) {
		toggle.addEventListener('click', function (ev) {
			ev.preventDefault();
			var open = wrap.classList.toggle('wp-responsive-open');
			toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
		});
	}

	// Admin bar dropdowns: mouse (with a short delay before closing, so
	// the pointer can travel into the menu), keyboard focus and touch (the
	// first tap opens the menu instead of following the link).
	document.querySelectorAll('#wpadminbar .menupop > .ab-item').forEach(function (a) {
		var li = a.parentNode, timer;
		var open = function () {
			clearTimeout(timer);
			document.querySelectorAll('#wpadminbar .menupop.hover').forEach(function (o) { if (o !== li) { o.classList.remove('hover'); } });
			li.classList.add('hover');
		};
		li.addEventListener('mouseenter', open);
		li.addEventListener('mouseleave', function () { timer = setTimeout(function () { li.classList.remove('hover'); }, 300); });
		a.addEventListener('focus', open);
		li.addEventListener('focusout', function (ev) {
			if (!li.contains(ev.relatedTarget)) { li.classList.remove('hover'); }
		});
		a.addEventListener('click', function (ev) {
			if (window.matchMedia('(hover: none)').matches && !li.classList.contains('menu-opened')) {
				ev.preventDefault();
				li.classList.add('menu-opened');
				open();
			}
		});
	});
	document.addEventListener('click', function (ev) {
		if (!ev.target.closest('#wpadminbar .menupop')) {
			document.querySelectorAll('#wpadminbar .menupop.hover').forEach(function (o) { o.classList.remove('hover', 'menu-opened'); });
		}
	});

	// Dismissible notices.
	document.addEventListener('click', function (ev) {
		var btn = ev.target.closest('.notice-dismiss');
		if (btn) { var n = btn.closest('.notice'); if (n) { n.remove(); } }
	});
	// Drop ?msg= from the address so a reload does not repeat the notice.
	if (/[?&]msg=/.test(location.search) && history.replaceState) {
		var u = new URL(location.href);
		u.searchParams.delete('msg');
		history.replaceState(null, '', u.pathname + (u.search ? u.search : '') + u.hash);
	}

	// Password visibility toggles.
	document.querySelectorAll('[data-toggle-password]').forEach(function (b) {
		b.addEventListener('click', function () {
			var input = document.getElementById(b.getAttribute('data-toggle-password'));
			if (!input) { return; }
			var show = input.type === 'password';
			input.type = show ? 'text' : 'password';
			b.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
			var icon = b.querySelector('.dashicons');
			if (icon) { icon.className = 'dashicons ' + (show ? 'dashicons-hidden' : 'dashicons-visibility'); }
		});
	});

	// Confirmation for destructive actions: <form data-confirm="…"> or a
	// submit button with data-confirm.
	document.addEventListener('submit', function (ev) {
		var f = ev.target;
		var b = ev.submitter;
		var msg = (b && b.getAttribute('data-confirm')) || (f.getAttribute && f.getAttribute('data-confirm'));
		if (msg && !window.confirm(msg)) { ev.preventDefault(); }
	}, true);

	// Recovery codes: copy and print.
	var copy = document.querySelector('[data-copy-codes]');
	if (copy) {
		copy.addEventListener('click', function () {
			var codes = Array.prototype.map.call(document.querySelectorAll('.gp-codes code'), function (c) { return c.textContent; }).join('\n');
			navigator.clipboard.writeText(codes).then(function () { copy.textContent = 'Copied'; });
		});
	}
	// One-time links and codes: copy buttons and select-on-focus fields.
	document.addEventListener('click', function (ev) {
		var b = ev.target.closest('[data-copy]');
		if (!b || !navigator.clipboard) { return; }
		navigator.clipboard.writeText(b.getAttribute('data-copy')).then(function () { b.textContent = 'Copied'; });
	});
	document.querySelectorAll('[data-select-all]').forEach(function (el) {
		el.addEventListener('focus', function () { el.select(); });
	});
	var print = document.querySelector('[data-print]');
	if (print) { print.addEventListener('click', function () { window.print(); }); }

	// Postbox toggles (dashboard, editor side boxes).
	document.addEventListener('click', function (ev) {
		var h = ev.target.closest('.postbox .handlediv, .postbox .hndle');
		if (!h || ev.target.closest('a, input, button:not(.handlediv)')) { return; }
		var box = h.closest('.postbox');
		var closed = box.classList.toggle('closed');
		var btn = box.querySelector('.handlediv');
		if (btn) { btn.setAttribute('aria-expanded', closed ? 'false' : 'true'); }
	});
})();
