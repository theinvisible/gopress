// Editor dialog: choose a gallery; the editor inserts the shortcode.
(function () {
	'use strict';
	var g = window.gopress;
	var list = document.getElementById('list');
	var insert = document.getElementById('insert');
	g.call('list').then(function (galleries) {
		list.textContent = '';
		if (!galleries.length) {
			var p = document.createElement('p');
			p.textContent = 'There are no galleries yet.';
			list.appendChild(p);
			return;
		}
		galleries.forEach(function (gal, i) {
			var label = document.createElement('label');
			var radio = document.createElement('input');
			radio.type = 'radio';
			radio.name = 'gallery';
			radio.value = String(gal.id);
			radio.addEventListener('change', function () { insert.disabled = false; });
			label.append(radio, ' ' + gal.title + ' (' + gal.count + (gal.count === 1 ? ' image)' : ' images)'));
			list.appendChild(label);
			if (i === 0) { radio.checked = true; insert.disabled = false; }
		});
	}).catch(function (err) { g.notice(err.message, 'error'); });
	document.getElementById('pick').addEventListener('submit', function (ev) {
		ev.preventDefault();
		var chosen = document.querySelector('input[name=gallery]:checked');
		if (!chosen) return;
		var attrs = { id: chosen.value };
		var c = document.getElementById('captions').value;
		if (c) attrs.captions = c;
		g.insertShortcode('fotogalerie', attrs);
	});
	document.getElementById('cancel').addEventListener('click', function () { g.close(); });
})();
