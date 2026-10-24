/* GoPress settings: date/time format previews, permalink tag buttons. */
(function () {
	'use strict';
	// Date and time formats: preview of the chosen or custom format.
	document.querySelectorAll('[data-format-group]').forEach(function (group) {
		var name = group.getAttribute('data-format-group');
		var custom = group.querySelector('[data-format-preview]');
		var out = group.querySelector('[data-preview]');
		var timer;
		var update = function () {
			var checked = group.querySelector('input[name="' + name + '"]:checked');
			var fmt = checked && checked.value !== 'custom' ? checked.value : custom.value;
			if (checked && checked.value !== 'custom') { custom.value = fmt; }
			clearTimeout(timer);
			timer = setTimeout(function () {
				fetch('/admin/api/date-preview?format=' + encodeURIComponent(fmt), { credentials: 'same-origin', headers: { Accept: 'application/json' } })
					.then(function (r) { return r.json(); }).then(function (j) { out.textContent = j.text; });
			}, 200);
		};
		group.addEventListener('change', update);
		custom.addEventListener('input', function () {
			group.querySelector('input[name="' + name + '"][value=custom]').checked = true;
			update();
		});
		update();
	});

	// Permalinks: tag buttons insert into the custom structure.
	var input = document.querySelector('[data-permalink-custom]');
	if (input) {
		document.querySelectorAll('[data-tag]').forEach(function (b) {
			b.addEventListener('click', function () {
				var tag = b.getAttribute('data-tag');
				var v = input.value;
				if (v.indexOf(tag) < 0) {
					var s = input.selectionStart != null ? input.selectionStart : v.length;
					input.value = v.slice(0, s) + (v.slice(0, s).endsWith('/') ? '' : '/') + tag + v.slice(s);
				}
				document.getElementById('custom_selection').checked = true;
				input.focus();
			});
		});
		input.addEventListener('focus', function () { document.getElementById('custom_selection').checked = true; });
		document.querySelectorAll('input[name=permalink_structure]').forEach(function (r) {
			r.addEventListener('change', function () { if (r.value !== 'custom') { input.value = r.value; } });
		});
	}
})();
