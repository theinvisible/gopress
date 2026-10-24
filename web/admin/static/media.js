/* GoPress media library: grid and list view, filters, infinite loading,
   drag-and-drop uploads with progress, attachment details with inline
   saving, bulk deletion. */
(function () {
	'use strict';
	var root = document.querySelector('.gp-media');
	if (!root) { return; }
	var gp = window.gopress;
	var csrf = document.body.getAttribute('data-csrf');
	var grid = root.querySelector('.gp-media-grid');
	var list = root.querySelector('.gp-media-list');
	var listBody = list.querySelector('tbody');
	var empty = root.querySelector('.gp-media-empty');
	var more = root.querySelector('.gp-media-more');
	var queue = root.querySelector('.gp-upload-queue');
	var input = root.querySelector('[data-file-input]');
	var overlay = root.querySelector('.gp-drop-overlay');
	var dialog = root.querySelector('.gp-media-details');
	var state = { page: 0, pages: 1, loading: false, view: 'grid', selecting: false, selected: {}, items: {} };
	var filters = {};
	try { state.view = localStorage.getItem('gp-media-view') || 'grid'; } catch (e) { /* no storage */ }

	var icon = function (mime) {
		if (/^audio\//.test(mime)) { return 'media-audio'; }
		if (/^video\//.test(mime)) { return 'media-video'; }
		if (/zip|gzip|tar|7z|bzip|xz|rpm|debian/.test(mime)) { return 'media-archive'; }
		if (/pdf/.test(mime)) { return 'pdf'; }
		return 'media-document';
	};
	var el = function (tag, attrs, children) {
		var e = document.createElement(tag);
		Object.keys(attrs || {}).forEach(function (k) {
			if (k === 'text') { e.textContent = attrs[k]; } else { e.setAttribute(k, attrs[k]); }
		});
		(children || []).forEach(function (c) { if (c) { e.appendChild(c); } });
		return e;
	};
	var size = function (n) {
		if (n >= 1048576) { return (n / 1048576).toFixed(1) + ' MB'; }
		return Math.max(1, Math.round(n / 1024)) + ' KB';
	};

	function tile(it) {
		var inner = it.thumb ? el('img', { src: it.thumb, alt: it.alt || '', loading: 'lazy', decoding: 'async' })
			: el('span', { 'class': 'gp-tile-doc' }, [el('span', { 'class': 'dashicons dashicons-' + icon(it.mime), 'aria-hidden': 'true' }), el('span', { text: it.filename })]);
		var b = el('button', { type: 'button', 'class': 'gp-media-tile', role: 'listitem', 'data-id': it.id, 'aria-label': it.title || it.filename, 'aria-pressed': state.selected[it.id] ? 'true' : 'false' },
			[inner, el('span', { 'class': 'gp-check', 'aria-hidden': 'true' }, [el('span', { 'class': 'dashicons dashicons-yes' })])]);
		return b;
	}
	function row(it) {
		var thumb = it.thumb ? el('img', { src: it.thumb, alt: '', loading: 'lazy' }) : el('span', { 'class': 'dashicons dashicons-' + icon(it.mime), 'aria-hidden': 'true' });
		var title = el('td', { 'class': 'column-title' }, [el('div', { 'class': 'gp-file-cell' }, [thumb, el('div', {}, [
			el('strong', {}, [el('button', { type: 'button', 'class': 'button-link', 'data-id': it.id, text: it.title || it.filename })]),
			el('p', { text: it.filename })])])]);
		var parent = it.parent_url ? el('a', { href: it.parent_url, text: it.parent }) : document.createTextNode(it.parent || '(Unattached)');
		var cb = el('input', { type: 'checkbox', 'data-check': it.id });
		cb.checked = !!state.selected[it.id];
		return el('tr', { 'data-row': it.id }, [el('th', { 'class': 'check-column', scope: 'row' }, [cb]), title, el('td', { text: it.author }), el('td', {}, [parent]), el('td', { text: it.date })]);
	}
	function render(items, reset) {
		if (reset) { grid.textContent = ''; listBody.textContent = ''; }
		items.forEach(function (it) {
			state.items[it.id] = it;
			grid.appendChild(tile(it));
			listBody.appendChild(row(it));
		});
		empty.hidden = Object.keys(state.items).length > 0;
	}
	function setView(v) {
		state.view = v;
		grid.hidden = v !== 'grid';
		list.hidden = v !== 'list';
		root.querySelectorAll('[data-view]').forEach(function (b) { b.setAttribute('aria-pressed', b.getAttribute('data-view') === v ? 'true' : 'false'); });
		try { localStorage.setItem('gp-media-view', v); } catch (e) { /* no storage */ }
	}
	function load(reset) {
		if (state.loading) { return; }
		if (reset) { state.page = 0; state.pages = 1; state.items = {}; }
		if (state.page >= state.pages) { more.hidden = true; return; }
		state.loading = true;
		var q = new URLSearchParams(filters);
		q.set('page', String(state.page + 1));
		fetch('/admin/api/media?' + q.toString(), { credentials: 'same-origin', headers: { Accept: 'application/json' } })
			.then(function (r) { return r.json(); })
			.then(function (data) {
				state.page++;
				state.pages = data.pages;
				render(data.items, reset);
				more.hidden = state.page >= state.pages;
			})
			.finally(function () { state.loading = false; });
	}
	new IntersectionObserver(function (entries) {
		if (entries[0].isIntersecting) { load(false); }
	}, { rootMargin: '400px' }).observe(more);

	// Filters.
	var timer;
	root.querySelectorAll('[data-filter]').forEach(function (f) {
		var update = function () {
			var k = f.getAttribute('data-filter');
			var v = f.type === 'checkbox' ? (f.checked ? '1' : '') : f.value.trim();
			if (v) { filters[k] = v; } else { delete filters[k]; }
			clearTimeout(timer);
			timer = setTimeout(function () { load(true); }, f.type === 'search' ? 300 : 0);
		};
		f.addEventListener(f.type === 'search' ? 'input' : 'change', update);
	});
	root.querySelectorAll('[data-view]').forEach(function (b) {
		b.addEventListener('click', function () { setView(b.getAttribute('data-view')); });
	});

	// Selection mode and bulk deletion.
	var selBtn = root.querySelector('[data-select-mode]');
	var delBtn = root.querySelector('[data-delete-selected]');
	var updateSel = function () {
		var n = Object.keys(state.selected).length;
		delBtn.hidden = !state.selecting || n === 0;
		delBtn.textContent = 'Delete permanently (' + n + ')';
	};
	selBtn.addEventListener('click', function () {
		state.selecting = !state.selecting;
		selBtn.setAttribute('aria-pressed', String(state.selecting));
		selBtn.textContent = state.selecting ? 'Cancel' : 'Bulk select';
		root.classList.toggle('gp-selecting', state.selecting);
		if (!state.selecting) {
			state.selected = {};
			grid.querySelectorAll('[aria-pressed=true]').forEach(function (t) { t.setAttribute('aria-pressed', 'false'); });
			listBody.querySelectorAll('[data-check]').forEach(function (c) { c.checked = false; });
		}
		updateSel();
	});
	var toggle = function (id, on) {
		if (on) { state.selected[id] = true; } else { delete state.selected[id]; }
		var t = grid.querySelector('[data-id="' + id + '"]');
		if (t) { t.setAttribute('aria-pressed', on ? 'true' : 'false'); }
		var c = listBody.querySelector('[data-check="' + id + '"]');
		if (c) { c.checked = on; }
		updateSel();
	};
	listBody.addEventListener('change', function (ev) {
		var c = ev.target.closest('[data-check]');
		if (c) {
			if (!state.selecting) { selBtn.click(); }
			toggle(c.getAttribute('data-check'), c.checked);
		}
	});
	delBtn.addEventListener('click', function () {
		var ids = Object.keys(state.selected);
		if (!ids.length || !confirm('Delete ' + ids.length + ' file(s) permanently? Posts that show them will have broken images.')) { return; }
		Promise.all(ids.map(function (id) { return post('/admin/api/media/' + id + '/delete', {}); }))
			.then(function () { state.selected = {}; selBtn.click(); load(true); })
			.catch(function (err) { alert(err.message); load(true); });
	});

	function post(url, fields) {
		var body = new URLSearchParams(fields);
		return fetch(url, { method: 'POST', body: body, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf, Accept: 'application/json' } })
			.then(function (r) { return r.json().then(function (j) { if (!r.ok) { throw new Error(j.error || 'Request failed.'); } return j; }); });
	}

	// Details dialog.
	var current = null;
	var status = dialog.querySelector('.gp-details-status');
	function openDetails(id) {
		var it = state.items[id];
		if (!it) { return; }
		current = it;
		var prev = dialog.querySelector('.gp-details-preview');
		prev.textContent = '';
		if (/^image\//.test(it.mime)) {
			var big = (it.sizes.large || it.sizes.full);
			prev.appendChild(el('img', { src: big.url, alt: it.alt || '' }));
		} else if (/^video\//.test(it.mime)) {
			prev.appendChild(el('video', { src: it.url, controls: '', preload: 'metadata' }));
		} else if (/^audio\//.test(it.mime)) {
			prev.appendChild(el('audio', { src: it.url, controls: '', preload: 'metadata' }));
		} else {
			prev.appendChild(el('span', { 'class': 'dashicons dashicons-' + icon(it.mime), 'aria-hidden': 'true' }));
		}
		var meta = dialog.querySelector('.gp-details-meta');
		meta.textContent = '';
		var add = function (k, v) { if (v) { meta.appendChild(el('dt', { text: k })); meta.appendChild(el('dd', { text: v })); } };
		add('File', it.filename);
		add('Type', it.mime);
		add('Size', size(it.filesize));
		if (it.width) { add('Dimensions', it.width + ' × ' + it.height + ' px'); }
		add('Uploaded', it.date);
		add('By', it.author);
		add('Attached to', it.parent || '(Unattached)');
		dialog.querySelectorAll('[data-field]').forEach(function (f) {
			f.value = it[f.getAttribute('data-field')] || '';
			f.disabled = !it.can_edit;
		});
		var isImage = /^image\//.test(it.mime);
		dialog.querySelector('[data-field=alt]').closest('label').hidden = !isImage;
		dialog.querySelector('.gp-alt-hint').hidden = !isImage;
		dialog.querySelector('[data-url]').value = it.url;
		dialog.querySelector('[data-view-file]').href = it.url;
		dialog.querySelector('[data-delete]').hidden = !it.can_delete;
		status.textContent = '';
		if (!dialog.open) { dialog.showModal(); }
	}
	dialog.querySelectorAll('[data-field]').forEach(function (f) {
		f.addEventListener('change', function () {
			if (!current) { return; }
			var fields = {};
			fields[f.getAttribute('data-field')] = f.value;
			status.textContent = 'Saving…';
			post('/admin/api/media/' + current.id, fields).then(function (it) {
				state.items[it.id] = it;
				current = it;
				status.textContent = 'Saved.';
				var t = grid.querySelector('[data-id="' + it.id + '"]');
				if (t) { t.setAttribute('aria-label', it.title || it.filename); }
			}).catch(function (err) { status.textContent = err.message; });
		});
	});
	dialog.querySelector('[data-copy-url]').addEventListener('click', function (ev) {
		if (navigator.clipboard) { navigator.clipboard.writeText(current.url).then(function () { ev.target.textContent = 'Copied'; }); }
	});
	dialog.querySelector('[data-delete]').addEventListener('click', function () {
		if (!current || !confirm('Delete “' + (current.title || current.filename) + '” permanently? Posts that show it will have a broken image.')) { return; }
		post('/admin/api/media/' + current.id + '/delete', {}).then(function () {
			dialog.close();
			load(true);
		}).catch(function (err) { status.textContent = err.message; });
	});
	dialog.addEventListener('keydown', function (ev) {
		if (!current || ev.target.matches('input, textarea')) { return; }
		var ids = Object.keys(state.items);
		var i = ids.indexOf(String(current.id));
		if (ev.key === 'ArrowRight' && i < ids.length - 1) { openDetails(ids[i + 1]); }
		if (ev.key === 'ArrowLeft' && i > 0) { openDetails(ids[i - 1]); }
	});

	var onItemClick = function (ev) {
		var b = ev.target.closest('[data-id]');
		if (!b) { return; }
		var id = b.getAttribute('data-id');
		if (state.selecting) { toggle(id, !state.selected[id]); return; }
		openDetails(id);
	};
	grid.addEventListener('click', onItemClick);
	listBody.addEventListener('click', function (ev) { if (!ev.target.closest('[data-check]')) { onItemClick(ev); } });

	// Uploads: button, drag and drop, paste.
	function uploadFiles(files) {
		if (!files || !files.length) { return; }
		gp.uploadQueue(files, {}, {
			start: function (file) {
				var li = el('li', {}, [el('span', { text: file.name }), el('progress', { max: '1', value: '0' })]);
				queue.prepend(li);
				return li;
			},
			progress: function (li, f) { li.querySelector('progress').value = f; },
			done: function (li, item) {
				li.classList.add('done');
				li.querySelector('progress').value = 1;
				setTimeout(function () { li.remove(); }, 1500);
				state.items[item.id] = item;
				grid.prepend(tile(item));
				listBody.prepend(row(item));
				empty.hidden = true;
			},
			failed: function (li, err) {
				li.classList.add('failed');
				li.appendChild(el('span', { 'class': 'gp-upload-error', text: err.message }));
			}
		});
	}
	root.querySelector('[data-upload]').addEventListener('click', function () { input.click(); });
	input.addEventListener('change', function () { uploadFiles(input.files); input.value = ''; });
	var depth = 0;
	var hasFiles = function (ev) { return ev.dataTransfer && Array.prototype.indexOf.call(ev.dataTransfer.types, 'Files') >= 0; };
	document.addEventListener('dragenter', function (ev) { if (hasFiles(ev)) { depth++; overlay.hidden = false; } });
	document.addEventListener('dragleave', function (ev) { if (hasFiles(ev) && --depth <= 0) { depth = 0; overlay.hidden = true; } });
	document.addEventListener('dragover', function (ev) { if (hasFiles(ev)) { ev.preventDefault(); } });
	document.addEventListener('drop', function (ev) {
		if (!hasFiles(ev)) { return; }
		ev.preventDefault();
		depth = 0;
		overlay.hidden = true;
		uploadFiles(ev.dataTransfer.files);
	});
	document.addEventListener('paste', function (ev) {
		if (ev.target.closest('input, textarea') || !ev.clipboardData) { return; }
		var files = Array.prototype.filter.call(ev.clipboardData.files || [], function (f) { return /^image\//.test(f.type); });
		if (files.length) { ev.preventDefault(); uploadFiles(files); }
	});

	setView(state.view);
	load(true);
})();
