/* GoPress profile and security pages. */
(function () {
	'use strict';
	// Rename passkey: ask for the new name.
	document.querySelectorAll('form[data-rename]').forEach(function (f) {
		f.addEventListener('submit', function (ev) {
			var name = window.prompt('New name for this passkey:', f.elements.name.value);
			if (name === null || !name.trim()) { ev.preventDefault(); return; }
			f.elements.name.value = name.trim().slice(0, 60);
		});
	});
	// The new password fields must match before submitting.
	var p1 = document.getElementById('pass1'), p2 = document.getElementById('pass2');
	if (p1 && p2) {
		var check = function () { p2.setCustomValidity(p2.value && p1.value !== p2.value ? 'The passwords do not match.' : ''); };
		p1.addEventListener('input', check);
		p2.addEventListener('input', check);
	}
})();
