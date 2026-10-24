/* GoPress terms screen: quick edit of name and slug. */
(function () {
	'use strict';
	var tpl = document.getElementById('gp-quick-term');
	var body = document.getElementById('the-list');
	if (!tpl || !body) { return; }
	var csrf = document.body.getAttribute('data-csrf');
	var base = body.getAttribute('data-base');

	body.addEventListener('click', function (ev) {
		var btn = ev.target.closest('[data-quick-term]');
		if (!btn) { return; }
		var row = btn.closest('tr');
		var edit = tpl.content.firstElementChild.cloneNode(true);
		var form = edit.querySelector('form');
		form.elements['tag-name'].value = row.getAttribute('data-name');
		form.elements.slug.value = row.getAttribute('data-slug');
		row.hidden = true;
		row.after(edit);
		form.elements['tag-name'].focus();
		var close = function () { edit.remove(); row.hidden = false; btn.focus(); };
		form.querySelector('.cancel').addEventListener('click', close);
		form.addEventListener('keydown', function (e) { if (e.key === 'Escape') { close(); } });
		form.addEventListener('submit', function (e) {
			e.preventDefault();
			var data = new URLSearchParams();
			data.set('tag-name', form.elements['tag-name'].value);
			data.set('slug', form.elements.slug.value);
			data.set('quick', '1');
			var id = row.id.replace('tag-', '');
			fetch(base + id + '/edit', { method: 'POST', body: data, credentials: 'same-origin',
				headers: { 'X-CSRF-Token': csrf, 'Accept': 'application/json' } })
				.then(function (r) { return r.json().then(function (j) { return { ok: r.ok, j: j }; }); })
				.then(function (res) {
					if (!res.ok) {
						var err = form.querySelector('.gp-quick-error');
						err.textContent = res.j.error || 'Saving failed.';
						err.hidden = false;
						return;
					}
					row.setAttribute('data-name', res.j.name);
					row.setAttribute('data-slug', res.j.slug);
					row.querySelector('.row-title').textContent = res.j.name;
					row.querySelector('.column-slug').textContent = res.j.slug;
					close();
				});
		});
	});
})();
