// Bridge between the admin and plugin pages in sandboxed iframes. The
// iframe has an opaque origin (no cookies, no access to this page); it
// talks to the admin only through these messages, which are checked to
// come from the plugin's own frame. Server calls carry the admin's CSRF
// token; HTML from the plugin is never inserted here.
(function () {
	'use strict';
	var gp = (window.gopress = window.gopress || {});
	var MAX = 1 << 20; // bytes of one message

	function size(o) {
		try { return JSON.stringify(o).length; } catch (e) { return MAX + 1; }
	}

	function notice(text, kind) {
		var wrap = document.querySelector('.wrap');
		if (!wrap || typeof text !== 'string') return;
		var div = document.createElement('div');
		div.className = 'notice is-dismissible notice-' + (['success', 'error', 'warning', 'info'].indexOf(kind) >= 0 ? kind : 'info');
		var p = document.createElement('p');
		p.textContent = text.slice(0, 500);
		div.appendChild(p);
		var h1 = wrap.querySelector('h1');
		wrap.insertBefore(div, h1 ? h1.nextSibling : wrap.firstChild);
		setTimeout(function () { div.remove(); }, 8000);
	}

	// connect wires an iframe: api is the call endpoint, csrf the token,
	// handlers.insert/close are used by editor dialogs.
	function connect(frame, api, csrf, handlers) {
		handlers = handlers || {};
		function reply(id, result, error) {
			// The frame's origin is opaque ("null"), so the target cannot be
			// narrower than "*"; the reply only goes to that window.
			frame.contentWindow.postMessage({ gopress: 1, id: id, result: result, error: error }, '*');
		}
		function onMessage(ev) {
			if (ev.source !== frame.contentWindow || ev.origin !== 'null') return;
			var m = ev.data;
			if (!m || m.gopress !== 1 || typeof m.type !== 'string' || size(m) > MAX) return;
			switch (m.type) {
			case 'call':
				if (typeof m.method !== 'string' || m.method.length > 100) return reply(m.id, null, 'invalid method');
				fetch(api, {
					method: 'POST', credentials: 'same-origin',
					headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, Accept: 'application/json' },
					body: JSON.stringify({ method: m.method, params: m.params === undefined ? null : m.params }),
				}).then(function (r) { return r.json(); }).then(function (r) {
					reply(m.id, r.result, r.error);
				}).catch(function () { reply(m.id, null, 'request failed'); });
				break;
			case 'pick':
				if (!gp.media || !gp.media.pick) return reply(m.id, null, 'media picker not available');
				gp.media.pick({ multiple: !!m.multiple, mode: 'select', title: 'Select Media', button: 'Select', imagesOnly: !!m.imagesOnly }).then(function (res) {
					var items = res ? res.items.map(function (it) {
						return { id: it.id, url: it.url, title: it.title, alt: it.alt, caption: it.caption, mime: it.mime,
							width: it.width, height: it.height, sizes: it.sizes };
					}) : [];
					reply(m.id, items);
				});
				break;
			case 'notice':
				notice(m.text, m.kind);
				reply(m.id, true);
				break;
			case 'resize':
				var hgt = Number(m.height);
				if (hgt > 0) frame.style.height = Math.min(Math.max(hgt, 100), 10000) + 'px';
				reply(m.id, true);
				break;
			case 'navigate':
				if (handlers.navigate && typeof m.page === 'string' && /^[a-z][a-z0-9_-]{0,39}$/.test(m.page)) {
					var base = location.pathname.replace(/\/[^/]+$/, '/');
					location.href = base + m.page;
				}
				break;
			case 'insert':
				if (handlers.insert && typeof m.tag === 'string') handlers.insert(m.tag, m.attrs || {}, typeof m.content === 'string' ? m.content : '');
				break;
			case 'close':
				if (handlers.close) handlers.close();
				break;
			}
		}
		window.addEventListener('message', onMessage);
		return function () { window.removeEventListener('message', onMessage); };
	}

	gp.pluginBridge = { connect: connect };

	// Admin pages may switch to another page of the plugin; editor dialogs
	// may not leave the editor.
	document.querySelectorAll('iframe[data-plugin-frame]').forEach(function (f) {
		connect(f, f.getAttribute('data-api'), f.getAttribute('data-csrf'), { navigate: true });
	});
})();
