// Plugin settings pages: media fields (one image or a sortable list).
(function () {
	'use strict';
	var gp = window.gopress || {};
	document.querySelectorAll('[data-media-field]').forEach(function (box) {
		var input = box.querySelector('input[type=hidden]');
		var list = box.querySelector('[data-thumbs]');
		var multiple = box.getAttribute('data-multiple') === '1';
		function ids() { return input.value.split(',').filter(Boolean); }
		function render(items) {
			list.textContent = '';
			items.forEach(function (it, i) {
				var li = document.createElement('li');
				li.setAttribute('data-id', it.id);
				var img = document.createElement('img');
				img.src = it.thumb || it.url;
				img.alt = it.title || '';
				li.appendChild(img);
				if (multiple) {
					var up = document.createElement('button');
					up.type = 'button'; up.className = 'button-link'; up.textContent = '←'; up.title = 'Move left';
					up.disabled = i === 0;
					up.onclick = function () { move(i, -1); };
					var down = document.createElement('button');
					down.type = 'button'; down.className = 'button-link'; down.textContent = '→'; down.title = 'Move right';
					down.disabled = i === items.length - 1;
					down.onclick = function () { move(i, 1); };
					var rm = document.createElement('button');
					rm.type = 'button'; rm.className = 'button-link button-link-delete'; rm.textContent = '×'; rm.title = 'Remove';
					rm.onclick = function () { var v = ids(); v.splice(i, 1); input.value = v.join(','); load(); };
					li.append(up, down, rm);
				}
				list.appendChild(li);
			});
		}
		function move(i, d) {
			var v = ids();
			var t = v[i]; v[i] = v[i + d]; v[i + d] = t;
			input.value = v.join(',');
			load();
		}
		var cache = {};
		function load() {
			var want = ids();
			var missing = want.filter(function (id) { return !cache[id]; });
			var done = function () { render(want.map(function (id) { return cache[id] || { id: id, url: '' }; })); };
			if (!missing.length) return done();
			Promise.all(missing.map(function (id) {
				return fetch('/admin/api/media/' + id, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
					.then(function (r) { return r.ok ? r.json() : null; }).then(function (it) { if (it) cache[id] = it; });
			})).then(done);
		}
		box.querySelector('[data-pick]').addEventListener('click', function () {
			if (!gp.media || !gp.media.pick) return;
			gp.media.pick({ multiple: multiple, mode: 'select', title: 'Select Media', button: 'Select', imagesOnly: true }).then(function (res) {
				if (!res) return;
				res.items.forEach(function (it) { cache[it.id] = it; });
				var picked = res.items.map(function (it) { return String(it.id); });
				input.value = (multiple ? ids().concat(picked.filter(function (id) { return ids().indexOf(id) < 0; })) : picked.slice(0, 1)).join(',');
				load();
			});
		});
		box.querySelector('[data-clear]').addEventListener('click', function () { input.value = ''; load(); });
		load();
	});
	document.querySelectorAll('[data-check-all]').forEach(function (all) {
		all.addEventListener('change', function () {
			all.closest('table').querySelectorAll('tbody input[type=checkbox]').forEach(function (c) { c.checked = all.checked; });
		});
	});
})();
// "Upload Plugin" reveals the upload form.
document.querySelectorAll('[data-toggle]').forEach(function (b) {
	b.addEventListener('click', function () {
		var el = document.querySelector(b.getAttribute('data-toggle'));
		if (el) { el.hidden = !el.hidden; if (!el.hidden) { var f = el.querySelector('input[type=file]'); if (f) f.focus(); } }
	});
});

// Plugins screen: progress of a plugin's WordPress import (live.js).
(function () {
	'use strict';
	document.addEventListener('gp-live', function (ev) {
		if (ev.detail.type !== 'import.progress') return;
		var d = ev.detail.data;
		var row = Array.prototype.find.call(document.querySelectorAll('tr[data-plugin]'), function (tr) {
			return tr.getAttribute('data-plugin') === d.plugin;
		});
		if (!row) return;
		var box = row.querySelector('.gp-import-progress');
		if (!box) {
			box = document.createElement('p');
			box.className = 'gp-import-progress';
			var bar = document.createElement('progress');
			bar.max = 100;
			var label = document.createElement('span');
			box.append(bar, ' ', label);
			row.querySelector('.column-description').appendChild(box);
		}
		box.querySelector('progress').value = d.percent;
		box.querySelector('span').textContent = (d.text ? d.text + ' ' : '') + '(' + d.percent + ' %)';
		if (d.percent >= 100) setTimeout(function () { box.remove(); }, 5000);
	});
})();
