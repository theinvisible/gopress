/* GoPress editor screen: autosave, preview, publish box, tags, categories,
   featured image and keyboard shortcuts. The content editor itself is
   web/editor (window.gopress.editor). */
(function () {
	'use strict';
	var form = document.getElementById('post');
	if (!form) { return; }
	var postId = form.getAttribute('data-post-id') || '0';
	var postType = form.getAttribute('data-post-type');
	var base = form.getAttribute('data-base');
	var editor = null;
	var submitting = false;
	var dirtyMeta = false;
	var lastSaved = null;

	function contentHTML() {
		return editor ? editor.getHTML() : form.elements.content.value;
	}
	function snapshot() {
		return form.elements.post_title.value + '\u0000' + contentHTML() + '\u0000' + (form.elements.excerpt ? form.elements.excerpt.value : '');
	}

	document.addEventListener('gp-editor-ready', function (ev) {
		editor = ev.detail;
		lastSaved = snapshot();
	});
	form.addEventListener('input', function (ev) { if (ev.target.name !== 'content') { dirtyMeta = true; } });
	form.addEventListener('change', function () { dirtyMeta = true; });

	function isDirty() {
		return dirtyMeta || (lastSaved !== null && snapshot() !== lastSaved);
	}

	// ---- Autosave ----
	function autosave() {
		var snap = snapshot();
		if (snap === lastSaved || submitting) { return Promise.resolve(); }
		var fd = new FormData();
		fd.append('post_id', postId);
		fd.append('post_type', postType);
		fd.append('title', form.elements.post_title.value);
		fd.append('content', contentHTML());
		fd.append('excerpt', form.elements.excerpt ? form.elements.excerpt.value : '');
		return gp.post('/admin/api/autosave', fd).then(function (res) {
			lastSaved = snap;
			if (postId === '0' && res.post_id) {
				// The first autosave created the draft: continue editing it.
				postId = String(res.post_id);
				form.setAttribute('data-post-id', postId);
				form.action = base + postId + '/edit';
				form.setAttribute('data-preview', res.preview_url);
				if (history.replaceState) { history.replaceState(null, '', res.edit_url); }
			}
			var t = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
			if (editor) { editor.markSaved((res.saved === 'draft' ? 'Draft saved at ' : 'Autosaved at ') + t); }
		}).catch(function (err) {
			if (editor) { editor.markSaved('Autosave failed: ' + err.message); }
		});
	}
	setInterval(autosave, 60000);

	// ---- Preview ----
	var previewBtn = document.getElementById('post-preview');
	if (previewBtn) {
		previewBtn.addEventListener('click', function () {
			// Open the window synchronously (popup blockers), then save.
			var win = window.open('about:blank', 'gp-preview-' + postId);
			lastSaved = null;
			autosave().then(function () {
				var url = form.getAttribute('data-preview');
				if (win) { win.location = url + (url.indexOf('?') >= 0 ? '&' : '?') + 't=' + Date.now(); }
			});
		});
	}

	// ---- Leaving with unsaved changes ----
	window.addEventListener('beforeunload', function (ev) {
		if (!submitting && isDirty()) { ev.preventDefault(); ev.returnValue = ''; }
	});
	form.addEventListener('submit', function () {
		submitting = true;
		var spinner = form.querySelector('#publishing-action .spinner');
		if (spinner) { spinner.classList.add('is-active'); }
	});

	// Ctrl/Cmd+S saves (draft or update).
	document.addEventListener('keydown', function (ev) {
		if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 's') {
			ev.preventDefault();
			var btn = document.getElementById('save-post') || document.getElementById('publish');
			if (btn) { btn.click(); }
		}
	});

	// ---- Publish box ----
	var pw = form.querySelector('.gp-password-field');
	form.querySelectorAll('input[name=visibility]').forEach(function (r) {
		r.addEventListener('change', function () {
			if (pw) { pw.hidden = form.elements.visibility.value !== 'password'; }
		});
	});
	var date = form.elements.post_date;
	var publish = document.getElementById('publish');
	function updatePublishLabel() {
		if (!publish || publish.name !== 'publish' || !date) { return; }
		var future = date.value && new Date(date.value) > new Date();
		publish.value = future ? 'Schedule' : 'Publish';
	}
	if (date) { date.addEventListener('input', updatePublishLabel); updatePublishLabel(); }

	// Slug input grows with its content.
	var slug = form.elements.post_name;
	if (slug) {
		var fit = function () { slug.size = Math.max(12, (slug.value || slug.placeholder).length + 1); };
		slug.addEventListener('input', fit);
		fit();
	}

	// ---- Restore autosave ----
	var restore = document.querySelector('[data-restore-autosave]');
	if (restore) {
		restore.addEventListener('click', function () {
			var data = JSON.parse(restore.getAttribute('data-restore-autosave'));
			form.elements.post_title.value = data.title;
			if (form.elements.excerpt) { form.elements.excerpt.value = data.excerpt; }
			if (editor) { editor.setHTML(data.content); } else { form.elements.content.value = data.content; }
			dirtyMeta = true;
			restore.closest('.notice').remove();
		});
	}

	// ---- Tags ----
	var tagsBox = form.querySelector('.gp-tags');
	if (tagsBox) {
		var hidden = tagsBox.querySelector('input[name=tags]');
		var list = tagsBox.querySelector('.gp-tag-list');
		var input = tagsBox.querySelector('.gp-tag-input');
		var datalist = tagsBox.querySelector('datalist');
		var tags = hidden.value ? hidden.value.split(',').map(function (t) { return t.trim(); }).filter(Boolean) : [];
		var render = function () {
			hidden.value = tags.join(', ');
			list.replaceChildren.apply(list, tags.map(function (t, i) {
				var li = document.createElement('li');
				var span = document.createElement('span');
				span.textContent = t;
				var x = document.createElement('button');
				x.type = 'button';
				x.className = 'gp-tag-remove';
				x.setAttribute('aria-label', 'Remove tag: ' + t);
				x.textContent = '×';
				x.addEventListener('click', function () { tags.splice(i, 1); dirtyMeta = true; render(); });
				li.append(span, x);
				return li;
			}));
		};
		var add = function () {
			input.value.split(',').map(function (t) { return t.trim(); }).filter(Boolean).forEach(function (t) {
				if (!tags.some(function (x) { return x.toLowerCase() === t.toLowerCase(); })) { tags.push(t); }
			});
			input.value = '';
			dirtyMeta = true;
			render();
		};
		input.addEventListener('keydown', function (ev) {
			if (ev.key === 'Enter' || ev.key === ',') { ev.preventDefault(); add(); }
		});
		tagsBox.querySelector('.gp-tag-button').addEventListener('click', add);
		var timer;
		input.addEventListener('input', function () {
			clearTimeout(timer);
			timer = setTimeout(function () {
				gp.get(tagsBox.getAttribute('data-tags-url') + '?q=' + encodeURIComponent(input.value.trim())).then(function (names) {
					datalist.replaceChildren.apply(datalist, names.map(function (n) { var o = document.createElement('option'); o.value = n; return o; }));
				}).catch(function () {});
			}, 200);
		});
		render();
	}

	// ---- New category ----
	var addCat = document.getElementById('category-add-submit');
	if (addCat) {
		addCat.addEventListener('click', function () {
			var name = document.getElementById('newcategory');
			var parent = document.getElementById('newcategory_parent');
			if (!name.value.trim()) { name.focus(); return; }
			gp.post('/admin/api/categories', { name: name.value.trim(), parent: parent.value }).then(function (t) {
				var ul = document.getElementById('categorychecklist');
				var existing = ul.querySelector('input[value="' + t.id + '"]');
				if (!existing) {
					var li = document.createElement('li');
					var label = document.createElement('label');
					label.className = 'selectit';
					var cb = document.createElement('input');
					cb.type = 'checkbox';
					cb.name = 'post_category[]';
					cb.value = t.id;
					label.append(cb, ' ' + t.name);
					li.append(label);
					ul.prepend(li);
					existing = cb;
				}
				existing.checked = true;
				name.value = '';
				dirtyMeta = true;
			}).catch(function (err) { alert(err.message); });
		});
	}

	// ---- Featured image ----
	var featured = form.querySelector('.gp-featured');
	if (featured) {
		var field = featured.querySelector('input[name=featured_media]');
		var setBtn = featured.querySelector('.gp-featured-set');
		var removeBtn = featured.querySelector('.gp-featured-remove');
		setBtn.addEventListener('click', function () {
			if (!window.gopress || !gopress.media) { return; }
			gopress.media.pick({ title: 'Featured image', button: 'Set featured image', mode: 'select', imagesOnly: true }).then(function (res) {
				if (!res) { return; }
				var it = res.items[0];
				field.value = it.id;
				var img = document.createElement('img');
				img.src = (it.sizes.medium || it.sizes.full).url;
				img.alt = '';
				setBtn.replaceChildren(img);
				removeBtn.parentNode.hidden = false;
				dirtyMeta = true;
			});
		});
		removeBtn.addEventListener('click', function () {
			field.value = '';
			setBtn.textContent = 'Set featured image';
			removeBtn.parentNode.hidden = true;
			dirtyMeta = true;
		});
	}
})();
