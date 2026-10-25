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
		if (snap === lastSaved || submitting || uploading()) { return Promise.resolve(); }
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

	// Uploads are attached to the post: a new post is saved as a draft
	// first.
	window.gopress = window.gopress || {};
	window.gopress.currentPost = {
		ensure: function () {
			if (postId !== '0') { return Promise.resolve(postId); }
			lastSaved = null;
			return autosave().then(function () { return postId; });
		}
	};

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

	// ---- Live preview ----
	// A side panel with the preview of the newest autosave, reloaded a
	// moment after each change (same as the Preview button, without a new
	// window).
	var panel = document.getElementById('gp-live-preview');
	var liveBtn = document.getElementById('post-live-preview');
	if (panel && liveBtn) {
		var frame = panel.querySelector('iframe');
		var state = panel.querySelector('.gp-live-preview-state');
		var shown = null;
		var changedAt = 0;
		var busy = false;
		var refresh = function () {
			if (panel.hidden || busy) { return; }
			busy = true;
			state.textContent = 'Updating…';
			var y = 0;
			try { y = frame.contentWindow.scrollY; } catch (e) {}
			lastSaved = null;
			autosave().then(function () {
				shown = snapshot();
				var url = form.getAttribute('data-preview');
				frame.onload = function () {
					try { frame.contentWindow.scrollTo(0, y); } catch (e) {}
					state.textContent = '';
					busy = false;
				};
				frame.src = url + (url.indexOf('?') >= 0 ? '&' : '?') + 'gp_live_preview=1&t=' + Date.now();
			}).catch(function () { busy = false; state.textContent = 'Update failed'; });
		};
		var setOpen = function (open) {
			panel.hidden = !open;
			document.body.classList.toggle('gp-live-preview-open', open);
			liveBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
			if (open) { refresh(); } else { frame.src = 'about:blank'; }
		};
		liveBtn.addEventListener('click', function () { setOpen(panel.hidden); });
		panel.querySelector('.gp-live-preview-close').addEventListener('click', function () { setOpen(false); liveBtn.focus(); });
		setInterval(function () {
			if (panel.hidden) { return; }
			var snap = snapshot();
			if (snap === shown) { changedAt = 0; return; }
			if (!changedAt) { changedAt = Date.now(); return; }
			if (Date.now() - changedAt >= 1500) { changedAt = 0; refresh(); }
		}, 500);
	}

	// ---- Others editing the same post ----
	// The editor tells the server every 30 s that the post is open; live
	// updates (live.js) report others opening it and newer saves.
	var me = document.body.getAttribute('data-user');
	var presenceBox = document.querySelector('.gp-presence');
	var staleBox = document.querySelector('.gp-stale');
	var showEditors = function (list) {
		if (!presenceBox) { return; }
		var others = (list || []).filter(function (e) { return String(e.id) !== me; });
		presenceBox.hidden = others.length === 0;
		presenceBox.querySelector('p').textContent = others.length === 0 ? '' :
			others.map(function (e) { return e.name; }).join(', ') + (others.length === 1 ? ' is' : ' are') +
			' editing this ' + (postType === 'page' ? 'page' : 'post') + ' too. Changes saved by one of you can overwrite the other\'s.';
	};
	var beat = function (editing) {
		if (postId === '0' || !document.body.getAttribute('data-live')) { return; }
		var fd = new FormData();
		fd.append('editing', editing ? '1' : '0');
		return fetch('/admin/api/posts/' + postId + '/presence', {
			method: 'POST', body: fd, credentials: 'same-origin', keepalive: !editing,
			headers: { 'X-CSRF-Token': gp.csrf, 'Accept': 'application/json' }
		}).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) {
			if (j && editing) { showEditors(j.editors); }
		}).catch(function () {});
	};
	if (document.body.getAttribute('data-live')) {
		beat(true);
		setInterval(function () { if (!document.hidden) { beat(true); } }, 30000);
		document.addEventListener('visibilitychange', function () { if (!document.hidden) { beat(true); } });
		window.addEventListener('pagehide', function () { beat(false); });
		document.addEventListener('gp-live', function (ev) {
			var d = ev.detail.data;
			if (String(d.post) !== postId) { return; }
			if (ev.detail.type === 'post.presence') {
				showEditors(d.editors);
			} else if (ev.detail.type === 'post.saved' && staleBox) {
				staleBox.hidden = false;
				staleBox.querySelector('span').textContent = d.by + ' saved a newer version of this ' +
					(postType === 'page' ? 'page' : 'post') + '. Saving now would overwrite their changes.';
			}
		});
		if (staleBox) {
			staleBox.querySelector('.gp-stale-reload').addEventListener('click', function () {
				submitting = true; // the user chose to drop their changes
				location.reload();
			});
		}
	}

	// ---- Leaving with unsaved changes ----
	window.addEventListener('beforeunload', function (ev) {
		if (!submitting && isDirty()) { ev.preventDefault(); ev.returnValue = ''; }
	});
	var uploading = function () {
		var ge = window.gopress && window.gopress.editor;
		return !!(ge && ge.pendingUploads && ge.pendingUploads() > 0);
	};
	form.addEventListener('submit', function (ev) {
		if (uploading()) {
			ev.preventDefault();
			alert('Please wait until the uploads are finished.');
			return;
		}
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
