/* GoPress list tables: select all, bulk action guard, quick edit and
   confirmations. */
(function () {
	'use strict';

	// Select all checkboxes (header and footer).
	document.querySelectorAll('.cb-select-all').forEach(function (all) {
		all.addEventListener('change', function () {
			var table = all.closest('table');
			table.querySelectorAll('tbody .check-column input[type=checkbox]').forEach(function (cb) { cb.checked = all.checked; });
			table.querySelectorAll('.cb-select-all').forEach(function (o) { o.checked = all.checked; });
		});
	});

	// Bulk actions need an action and a selection.
	document.querySelectorAll('[data-bulk-apply]').forEach(function (btn) {
		btn.addEventListener('click', function (ev) {
			var form = btn.form;
			var action = form.querySelector('select[name=action]').value;
			var any = form.querySelector('tbody .check-column input:checked');
			if (!action || !any) { ev.preventDefault(); return; }
			if (action === 'delete' && !confirm('Delete the selected items permanently?')) { ev.preventDefault(); }
		});
	});

	// Confirmation on single buttons.
	document.addEventListener('click', function (ev) {
		var b = ev.target.closest('[data-confirm-click]');
		if (b && !confirm(b.getAttribute('data-confirm-click'))) { ev.preventDefault(); }
	});

	// Quick edit.
	var tpl = document.getElementById('gp-quick-edit');
	if (!tpl) { return; }
	document.addEventListener('click', function (ev) {
		var btn = ev.target.closest('.editinline');
		if (!btn) { return; }
		var row = btn.closest('tr');
		var data = JSON.parse(row.getAttribute('data-quick-edit'));
		var edit = tpl.content.firstElementChild.cloneNode(true);
		var form = edit.querySelector('form');
		function set(name, v) {
			var el = form.elements[name];
			if (!el) { return; }
			if (el.type === 'checkbox') { el.checked = !!v; } else { el.value = v == null ? '' : v; }
		}
		set('title', data.title);
		set('slug', data.slug);
		set('date', data.date);
		set('password', data.password);
		set('status', data.status === 'future' ? 'publish' : data.status);
		set('comments', data.comments);
		set('pings', data.pings);
		set('sticky', data.sticky);
		set('tags', data.tags);
		set('parent', String(data.parent || 0));
		set('menu_order', data.menu_order);
		set('template', data.template || '');
		form.querySelectorAll('input[name="categories[]"]').forEach(function (cb) {
			cb.checked = (data.categories || []).indexOf(parseInt(cb.value, 10)) >= 0;
		});
		row.hidden = true;
		row.after(edit);
		form.elements.title.focus();

		function close() { edit.remove(); row.hidden = false; btn.focus(); }
		edit.querySelector('.cancel').addEventListener('click', close);
		edit.addEventListener('keydown', function (e) { if (e.key === 'Escape') { close(); } });
		form.addEventListener('submit', function (e) {
			e.preventDefault();
			var spinner = form.querySelector('.spinner');
			spinner.classList.add('is-active');
			edit.classList.add('gp-saving');
			gp.post(form.getAttribute('data-base') + data.id + '/quick-edit', new FormData(form)).then(function () {
				location.reload();
			}).catch(function (err) {
				spinner.classList.remove('is-active');
				edit.classList.remove('gp-saving');
				form.querySelector('.gp-error').textContent = err.message;
			});
		});
	});
})();
