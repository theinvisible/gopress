/* GoPress comment moderation: approve, spam, trash, reply and quick edit
   without reloading (comment list and dashboard). */
(function () {
	'use strict';

	function updateCounts(counts) {
		if (!counts) { return; }
		var pending = counts.pending || 0;
		document.querySelectorAll('.awaiting-mod .pending-count, #wp-admin-bar-comments .pending-count').forEach(function (el) {
			el.textContent = pending;
		});
		document.querySelectorAll('.awaiting-mod, #wp-admin-bar-comments .ab-label').forEach(function (el) {
			el.className = el.className.replace(/count-\d+/, 'count-' + pending);
		});
		var map = { all: (counts.approved || 0) + pending, pending: pending, approved: counts.approved || 0, spam: counts.spam || 0, trash: counts.trash || 0 };
		Object.keys(map).forEach(function (k) {
			document.querySelectorAll('.gp-count-' + k).forEach(function (el) { el.textContent = map[k]; });
		});
	}

	function act(id, action, data) {
		return gp.post('/admin/api/comments/' + id + '/' + action, data || {});
	}

	function rowOf(el) {
		return el.closest('tr[data-comment-id], .gp-comment');
	}

	document.addEventListener('click', function (ev) {
		var b = ev.target.closest('[data-moderate]');
		if (!b) { return; }
		var action = b.getAttribute('data-moderate');
		var box = b.closest('[data-comment-actions]');
		var id = box ? box.getAttribute('data-id') : '';
		// Only real moderation buttons: a numeric comment id and a known action.
		if (!/^[0-9]+$/.test(id) || ['approve', 'unapprove', 'spam', 'unspam', 'trash', 'untrash', 'delete'].indexOf(action) < 0) { return; }
		if (b.hasAttribute('data-confirm') && !confirm(b.getAttribute('data-confirm'))) { return; }
		var row = rowOf(b);
		if (row) { row.classList.add('gp-saving'); }
		act(id, action).then(function (res) {
			updateCounts(res.counts);
			if (!row) { return; }
			row.classList.remove('gp-saving');
			var listView = new URLSearchParams(location.search).get('comment_status') || '';
			var stays = (action === 'approve' || action === 'unapprove') && listView === '';
			if (stays || row.classList.contains('gp-comment')) {
				// Flip the row between pending and approved in place.
				var approved = action === 'approve';
				row.classList.toggle('unapproved', !approved && action === 'unapprove');
				if (action === 'spam' || action === 'trash' || action === 'delete') { row.remove(); return; }
				b.textContent = approved ? 'Unapprove' : 'Approve';
				b.setAttribute('data-moderate', approved ? 'unapprove' : 'approve');
				row.setAttribute('data-status', approved ? 'approved' : 'pending');
				var badge = row.querySelector('.gp-badge-warn');
				if (badge && approved) { badge.remove(); }
				return;
			}
			row.style.transition = 'opacity .2s';
			row.style.opacity = '0';
			setTimeout(function () { row.remove(); }, 200);
		}).catch(function (err) {
			if (row) { row.classList.remove('gp-saving'); }
			alert(err.message);
		});
	});

	function inlineForm(tplId, row, fill, submit) {
		var tpl = document.getElementById(tplId);
		var open = row.nextElementSibling;
		if (open && open.classList.contains('gp-inline-edit')) { open.remove(); }
		var tr = tpl.content.firstElementChild.cloneNode(true);
		var form = tr.querySelector('form');
		if (fill) { fill(form); }
		row.after(tr);
		form.querySelector('textarea, input').focus();
		var close = function () { tr.remove(); };
		tr.querySelector('.cancel').addEventListener('click', close);
		tr.addEventListener('keydown', function (e) { if (e.key === 'Escape') { close(); } });
		form.addEventListener('submit', function (e) {
			e.preventDefault();
			var spinner = form.querySelector('.spinner');
			spinner.classList.add('is-active');
			submit(new FormData(form)).then(function (res) {
				updateCounts(res.counts);
				close();
			}).catch(function (err) {
				spinner.classList.remove('is-active');
				form.querySelector('.gp-error').textContent = err.message;
			});
		});
	}

	document.addEventListener('click', function (ev) {
		var reply = ev.target.closest('[data-comment-reply]');
		var edit = ev.target.closest('[data-comment-edit]');
		if (!reply && !edit) { return; }
		var row = rowOf(ev.target);
		var id = row.getAttribute('data-comment-id');
		if (reply) {
			inlineForm('gp-comment-reply', row, null, function (fd) {
				return act(id, 'reply', fd).then(function (res) {
					row.classList.remove('unapproved');
					var note = document.createElement('div');
					note.className = 'notice notice-success inline';
					note.innerHTML = '<p></p>';
					note.firstChild.textContent = 'Reply published.';
					row.querySelector('.column-comment').append(note);
					return res;
				});
			});
		} else {
			var data = JSON.parse(edit.getAttribute('data-edit'));
			inlineForm('gp-comment-edit', row, function (form) {
				form.elements.author.value = data.author;
				form.elements.email.value = data.email;
				form.elements.url.value = data.url;
				form.elements.content.value = data.content;
			}, function (fd) {
				return act(id, 'quick-edit', fd).then(function (res) {
					var text = row.querySelector('.gp-comment-text');
					// res.html is sanitized server side (comment allowlist).
					if (text && res.html !== undefined) { text.innerHTML = res.html; }
					data = { author: fd.get('author'), email: fd.get('email'), url: fd.get('url'), content: fd.get('content') };
					edit.setAttribute('data-edit', JSON.stringify(data));
					return res;
				});
			});
		}
	});
})();
