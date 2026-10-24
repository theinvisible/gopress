/* GoPress customizer: saves the form as a draft while editing and
   reloads the preview frame, which shows the site with the draft. */
(function () {
	'use strict';
	var form = document.getElementById('customize-form');
	if (!form) { return; }
	var csrf = document.body.getAttribute('data-csrf');
	var frame = document.querySelector('.gp-customize-preview iframe');
	var status = form.querySelector('.gp-customize-status');
	var timer, seq = 0;

	function saveDraft() {
		var n = ++seq;
		status.textContent = 'Updating the preview…';
		fetch(form.getAttribute('data-draft'), { method: 'POST', body: new FormData(form), credentials: 'same-origin',
			headers: { 'X-CSRF-Token': csrf, Accept: 'application/json' } })
			.then(function (r) { return r.json().then(function (j) { if (!r.ok) { throw new Error(j.error || 'The preview could not be updated.'); } }); })
			.then(function () {
				if (n !== seq) { return; }
				status.textContent = 'Preview updated. Visitors see the changes after “Publish”.';
				try { frame.contentWindow.location.reload(); } catch (e) { frame.src = frame.src; }
			})
			.catch(function (err) { status.textContent = err.message; });
	}
	var changed = function () { clearTimeout(timer); timer = setTimeout(saveDraft, 400); };
	form.addEventListener('input', changed);
	form.addEventListener('change', changed);

	// Images from the media library.
	form.querySelectorAll('[data-image-field]').forEach(function (f) {
		var input = f.querySelector('input[type=hidden]');
		var img = f.querySelector('img');
		var clear = f.querySelector('[data-clear]');
		f.querySelector('[data-pick]').addEventListener('click', function () {
			if (!window.gopress || !window.gopress.media) { return; }
			window.gopress.media.pick({ title: f.querySelector('.gp-field-label').textContent, button: 'Choose', mode: 'select', imagesOnly: true }).then(function (res) {
				if (!res || !res.items.length) { return; }
				var it = res.items[0];
				input.value = it.id;
				img.src = (it.sizes.medium || it.sizes.full).url;
				img.hidden = false;
				clear.hidden = false;
				var extra = f.parentNode.querySelector('input[name=background_image_url]');
				if (extra && input.name === 'background_image_id') { extra.remove(); }
				changed();
			});
		});
		clear.addEventListener('click', function () {
			input.value = '';
			img.hidden = true;
			img.removeAttribute('src');
			clear.hidden = true;
			var extra = f.parentNode.querySelector('input[name=background_image_url]');
			if (extra && input.name === 'background_image_id') { extra.remove(); }
			changed();
		});
	});

	// Preview sizes.
	document.querySelectorAll('[data-device]').forEach(function (b) {
		b.addEventListener('click', function () {
			document.querySelectorAll('[data-device]').forEach(function (x) { x.setAttribute('aria-pressed', x === b ? 'true' : 'false'); });
			frame.className = 'is-' + b.getAttribute('data-device');
		});
	});
})();
