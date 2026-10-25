// Gallery management: the page talks to GoPress only through
// window.gopress (call, media.pick, notice). Texts are set with
// textContent; images only get src attributes.
(function () {
	'use strict';
	var g = window.gopress;
	var $ = function (id) { return document.getElementById(id); };
	var current = null; // the gallery being edited
	var images = [];    // [{id, url, title}]

	function el(tag, props, text) {
		var e = document.createElement(tag);
		Object.keys(props || {}).forEach(function (k) {
			if (k === 'class') e.className = props[k]; else e.setAttribute(k, props[k]);
		});
		if (text !== undefined) e.textContent = text;
		return e;
	}
	function shortcode(id) { return '[fotogalerie id="' + id + '"]'; }
	function fail(err) { g.notice(err.message || String(err), 'error'); }

	// ---- List ----
	function showList() {
		$('edit-view').hidden = true;
		$('list-view').hidden = false;
		return g.call('list').then(function (list) {
			var rows = $('rows');
			rows.textContent = '';
			if (!list.length) {
				var tr = el('tr');
				tr.appendChild(el('td', { colspan: '4' }, 'No galleries yet.'));
				rows.appendChild(tr);
			}
			list.forEach(function (gal) {
				var tr = el('tr');
				tr.appendChild(el('td', {}, gal.title));
				tr.appendChild(el('td', {}, String(gal.count)));
				var sc = el('input', { type: 'text', readonly: '', class: 'code', 'aria-label': 'Shortcode of ' + gal.title });
				sc.value = shortcode(gal.id);
				sc.addEventListener('focus', function () { sc.select(); });
				var td = el('td');
				td.appendChild(sc);
				tr.appendChild(td);
				var act = el('td', { class: 'row-actions' });
				if (gal.editable) {
					var edit = el('button', { type: 'button', class: 'link' }, 'Edit');
					edit.addEventListener('click', function () { openGallery(gal.id); });
					var del = el('button', { type: 'button', class: 'link danger' }, 'Delete');
					del.addEventListener('click', function () {
						if (!confirm('Delete the gallery “' + gal.title + '”? Posts that show it will show nothing in its place.')) return;
						g.call('delete', { id: gal.id }).then(function () {
							g.notice('Gallery deleted.', 'success');
							showList();
						}).catch(fail);
					});
					act.append(edit, ' ', del);
				}
				tr.appendChild(act);
				rows.appendChild(tr);
			});
		}).catch(fail);
	}

	// ---- Edit ----
	function openGallery(id) {
		g.call('get', { id: id }).then(function (r) {
			edit(r.gallery, r.thumbs);
		}).catch(fail);
	}

	function edit(gal, thumbs) {
		current = gal;
		images = thumbs.slice();
		$('title').value = gal ? gal.title : '';
		$('captions').checked = gal ? gal.captions : true;
		$('shortcode').textContent = gal ? 'Shortcode: ' + shortcode(gal.id) : '';
		$('list-view').hidden = true;
		$('edit-view').hidden = false;
		renderImages();
		$('title').focus();
	}

	function move(from, to) {
		if (to < 0 || to >= images.length || from === to) return;
		var it = images.splice(from, 1)[0];
		images.splice(to, 0, it);
		renderImages();
	}

	var dragFrom = -1;
	function renderImages() {
		var ul = $('images');
		ul.textContent = '';
		$('no-images').hidden = images.length > 0;
		images.forEach(function (it, i) {
			var li = el('li', { draggable: 'true' });
			var img = el('img', { alt: it.title || '' });
			img.src = it.url;
			var bar = el('div', { class: 'bar' });
			var left = el('button', { type: 'button', 'aria-label': 'Move left' }, '←');
			var right = el('button', { type: 'button', 'aria-label': 'Move right' }, '→');
			var remove = el('button', { type: 'button', class: 'danger', 'aria-label': 'Remove image' }, '×');
			left.disabled = i === 0;
			right.disabled = i === images.length - 1;
			left.addEventListener('click', function () { move(i, i - 1); ul.children[Math.max(i - 1, 0)].querySelector('button').focus(); });
			right.addEventListener('click', function () { move(i, i + 1); });
			remove.addEventListener('click', function () { images.splice(i, 1); renderImages(); });
			bar.append(left, right, remove);
			li.append(img, bar);
			li.addEventListener('dragstart', function (ev) { dragFrom = i; ev.dataTransfer.effectAllowed = 'move'; li.classList.add('dragging'); });
			li.addEventListener('dragend', function () { li.classList.remove('dragging'); });
			li.addEventListener('dragover', function (ev) { ev.preventDefault(); ev.dataTransfer.dropEffect = 'move'; });
			li.addEventListener('drop', function (ev) { ev.preventDefault(); if (dragFrom >= 0) move(dragFrom, i); dragFrom = -1; });
			ul.appendChild(li);
		});
	}

	$('pick').addEventListener('click', function () {
		g.media.pick({ multiple: true, imagesOnly: true }).then(function (items) {
			var have = {};
			images.forEach(function (it) { have[it.id] = true; });
			items.forEach(function (it) {
				if (have[it.id]) return;
				var t = it.sizes && (it.sizes.thumbnail || it.sizes.medium);
				images.push({ id: it.id, url: (t && t.url) || it.url, title: it.title || '' });
			});
			renderImages();
		}).catch(fail);
	});
	$('clear').addEventListener('click', function () {
		if (images.length && confirm('Remove all images from this gallery?')) { images = []; renderImages(); }
	});
	$('back').addEventListener('click', showList);
	$('add').addEventListener('click', function () { edit(null, []); });
	$('edit').addEventListener('submit', function (ev) {
		ev.preventDefault();
		var btn = $('save');
		btn.disabled = true;
		g.call('save', {
			id: current ? current.id : 0,
			title: $('title').value,
			captions: $('captions').checked,
			images: images.map(function (it) { return it.id; })
		}).then(function (gal) {
			current = gal;
			$('shortcode').textContent = 'Shortcode: ' + shortcode(gal.id);
			g.notice('Gallery saved.', 'success');
		}).catch(fail).then(function () { btn.disabled = false; });
	});

	showList();
})();
