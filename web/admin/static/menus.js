/* GoPress menus: add items from the sources, drag and drop with nesting,
   keyboard moves, item settings; the structure is sent as JSON on save. */
(function () {
	'use strict';
	var form = document.getElementById('update-nav-menu');
	if (!form) { return; }
	var list = document.getElementById('menu-to-edit');
	var tpl = document.getElementById('gp-menu-item-tpl');
	var empty = form.querySelector('.gp-menu-empty');
	var kindLabels = { page: 'Page', post: 'Post', category: 'Category', post_tag: 'Tag', custom: 'Custom Link' };
	var maxDepth = 10;
	var dirty = false;

	var items = function () { return Array.prototype.slice.call(list.children); };
	var depthOf = function (li) { return parseInt(li.getAttribute('data-depth'), 10) || 0; };
	function setDepth(li, d) {
		li.className = li.className.replace(/\bdepth-\d+/, '') + ' depth-' + d;
		li.className = li.className.replace(/\s+/g, ' ').trim();
		li.setAttribute('data-depth', String(d));
	}
	// subtree returns li and its sub items (the following deeper items).
	function subtree(li) {
		var all = items(), i = all.indexOf(li), d = depthOf(li), out = [li];
		for (var j = i + 1; j < all.length && depthOf(all[j]) > d; j++) { out.push(all[j]); }
		return out;
	}
	// normalize keeps depths valid: each item at most one deeper than the
	// previous one, the first at 0.
	function normalize() {
		var prev = -1;
		items().forEach(function (li) {
			var d = Math.min(depthOf(li), prev + 1, maxDepth);
			setDepth(li, Math.max(d, 0));
			prev = depthOf(li);
		});
		empty.hidden = list.children.length > 0;
	}
	function changed() { dirty = true; normalize(); }

	function addItem(data) {
		var li = tpl.content.firstElementChild.cloneNode(true);
		li.setAttribute('data-id', '0');
		li.setAttribute('data-kind', data.kind);
		li.setAttribute('data-object', data.object || '0');
		li.querySelector('.gp-menu-item-title').textContent = data.label;
		li.querySelector('.gp-menu-item-type').textContent = kindLabels[data.kind];
		var title = li.querySelector('[data-field=title]');
		if (data.kind === 'custom') {
			title.value = data.label;
			li.querySelector('.gp-field-url').hidden = false;
			li.querySelector('[data-field=url]').value = data.url;
		} else {
			title.placeholder = data.label;
		}
		setDepth(li, 0);
		list.appendChild(li);
		changed();
	}

	// Sources.
	document.querySelectorAll('.gp-menu-source').forEach(function (src) {
		var kind = src.getAttribute('data-kind');
		var add = src.querySelector('[data-add-items]');
		if (add) {
			add.addEventListener('click', function () {
				src.querySelectorAll('input[type=checkbox]:checked').forEach(function (cb) {
					addItem({ kind: kind, object: cb.value, label: cb.getAttribute('data-title') });
					cb.checked = false;
				});
			});
		}
		var search = src.querySelector('[data-menu-search]');
		if (search) {
			var timer;
			search.addEventListener('input', function () {
				clearTimeout(timer);
				timer = setTimeout(function () {
					fetch('/admin/api/menu-search?kind=' + kind + '&s=' + encodeURIComponent(search.value.trim()),
						{ credentials: 'same-origin', headers: { Accept: 'application/json' } })
						.then(function (r) { return r.json(); })
						.then(function (rows) {
							var ul = src.querySelector('.gp-menu-choices');
							ul.textContent = '';
							rows.forEach(function (row) {
								var li = document.createElement('li');
								var label = document.createElement('label');
								var cb = document.createElement('input');
								cb.type = 'checkbox';
								cb.value = row.id;
								cb.setAttribute('data-title', row.title);
								label.append(cb, ' ' + row.title);
								li.appendChild(label);
								ul.appendChild(li);
							});
						});
				}, 250);
			});
		}
		var customAdd = src.querySelector('[data-add-custom]');
		if (customAdd) {
			customAdd.addEventListener('click', function () {
				var url = src.querySelector('[data-custom-url]');
				var label = src.querySelector('[data-custom-label]');
				if (!url.value.trim() || !label.value.trim()) { (url.value.trim() ? label : url).focus(); return; }
				addItem({ kind: 'custom', url: url.value.trim(), label: label.value.trim() });
				url.value = '';
				label.value = '';
			});
		}
	});

	// Item settings, removal, keyboard moves.
	list.addEventListener('click', function (ev) {
		var li = ev.target.closest('.gp-menu-item');
		if (!li) { return; }
		if (ev.target.closest('[data-toggle-item]')) {
			var s = li.querySelector('.gp-menu-item-settings');
			s.hidden = !s.hidden;
			ev.target.closest('[data-toggle-item]').setAttribute('aria-expanded', String(!s.hidden));
			return;
		}
		if (ev.target.closest('[data-remove-item]')) {
			var sub = subtree(li);
			sub.slice(1).forEach(function (c) { setDepth(c, depthOf(c) - 1); });
			li.remove();
			changed();
			return;
		}
		var mv = ev.target.closest('[data-move]');
		if (!mv) { return; }
		var group = subtree(li), all = items(), i = all.indexOf(li);
		switch (mv.getAttribute('data-move')) {
		case 'up':
			var prev = all[i - 1];
			if (!prev) { return; }
			while (prev && depthOf(prev) > depthOf(li)) { prev = prev.previousElementSibling; }
			if (prev) { group.forEach(function (g) { list.insertBefore(g, prev); }); }
			break;
		case 'down':
			var after = all[i + group.length];
			if (!after) { return; }
			var next = subtree(after);
			var anchor = next[next.length - 1].nextElementSibling;
			group.forEach(function (g) { list.insertBefore(g, anchor); });
			break;
		case 'in':
			group.forEach(function (g) { setDepth(g, depthOf(g) + 1); });
			break;
		case 'out':
			if (depthOf(li) === 0) { return; }
			group.forEach(function (g) { setDepth(g, depthOf(g) - 1); });
			break;
		}
		changed();
		mv.focus();
	});
	list.addEventListener('input', function (ev) {
		dirty = true;
		if (ev.target.matches('[data-field=title]')) {
			var li = ev.target.closest('.gp-menu-item');
			li.querySelector('.gp-menu-item-title').textContent = ev.target.value || ev.target.placeholder;
		}
	});

	// Drag and drop: vertical position, horizontal offset sets the depth.
	var dragged = null, startX = 0, startDepth = 0, group = [];
	list.addEventListener('dragstart', function (ev) {
		var li = ev.target.closest('.gp-menu-item');
		if (!li || ev.target.closest('input, textarea')) { return; }
		dragged = li;
		group = subtree(li);
		startX = ev.clientX;
		startDepth = depthOf(li);
		group.forEach(function (g) { g.classList.add('is-dragging'); });
		ev.dataTransfer.effectAllowed = 'move';
		ev.dataTransfer.setData('text/plain', '');
	});
	list.addEventListener('dragover', function (ev) {
		if (!dragged) { return; }
		ev.preventDefault();
		var target = ev.target.closest('.gp-menu-item');
		if (target && group.indexOf(target) < 0) {
			var r = target.getBoundingClientRect();
			var before = ev.clientY < r.top + r.height / 2;
			var ref = before ? target : subtree(target).pop().nextElementSibling;
			group.forEach(function (g) { list.insertBefore(g, ref); });
		}
		var delta = Math.round((ev.clientX - startX) / 30);
		var d = Math.max(0, startDepth + delta);
		var shift = d - depthOf(dragged);
		if (shift) { group.forEach(function (g) { setDepth(g, depthOf(g) + shift); }); }
	});
	list.addEventListener('dragend', function () {
		if (!dragged) { return; }
		group.forEach(function (g) { g.classList.remove('is-dragging'); });
		dragged = null;
		changed();
	});
	list.addEventListener('drop', function (ev) { ev.preventDefault(); });

	// Save: serialize the structure.
	form.addEventListener('submit', function (ev) {
		if (ev.submitter && ev.submitter.getAttribute('formaction')) { dirty = false; return; }
		normalize();
		var data = items().map(function (li) {
			var f = function (k) { var el = li.querySelector('[data-field=' + k + ']'); return el ? el.value : ''; };
			return {
				id: parseInt(li.getAttribute('data-id'), 10) || 0,
				kind: li.getAttribute('data-kind'),
				object_id: parseInt(li.getAttribute('data-object'), 10) || 0,
				title: f('title'), url: f('url'), attr_title: f('attr_title'), classes: f('classes'),
				target: li.querySelector('[data-field=target]').checked ? '_blank' : '',
				depth: depthOf(li)
			};
		});
		form.elements.items.value = JSON.stringify(data);
		dirty = false;
	});
	window.addEventListener('beforeunload', function (ev) { if (dirty) { ev.preventDefault(); ev.returnValue = ''; } });
	normalize();
})();
