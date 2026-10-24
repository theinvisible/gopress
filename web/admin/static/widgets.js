/* GoPress widgets: drag and drop between areas, keyboard moves, saving
   forms without reloading. */
(function () {
	'use strict';
	var root = document.querySelector('.gp-widgets');
	if (!root) { return; }
	var csrf = document.body.getAttribute('data-csrf');
	var status = root.querySelector('.gp-widgets-status');
	var lists = function () { return Array.prototype.slice.call(root.querySelectorAll('.gp-widget-list')); };

	function saveOrder() {
		var layout = {};
		lists().forEach(function (ul) {
			layout[ul.getAttribute('data-area')] = Array.prototype.map.call(ul.children, function (li) { return li.getAttribute('data-id'); });
			ul.closest('.inside').querySelector('.gp-widget-empty').classList.toggle('is-empty', ul.children.length === 0);
		});
		status.textContent = 'Saving…';
		fetch('/admin/widgets/order', { method: 'POST', body: JSON.stringify(layout), credentials: 'same-origin',
			headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/json', Accept: 'application/json' } })
			.then(function (r) { status.textContent = r.ok ? 'Order saved.' : 'Saving the order failed. Reload the page.'; })
			.catch(function () { status.textContent = 'Saving the order failed. Reload the page.'; });
	}

	// Drag and drop.
	var dragged = null;
	root.addEventListener('dragstart', function (ev) {
		var li = ev.target.closest('.gp-widget');
		if (!li) { return; }
		dragged = li;
		li.classList.add('is-dragging');
		ev.dataTransfer.effectAllowed = 'move';
		ev.dataTransfer.setData('text/plain', li.getAttribute('data-id'));
	});
	root.addEventListener('dragend', function () {
		if (dragged) { dragged.classList.remove('is-dragging'); dragged = null; saveOrder(); }
		root.querySelectorAll('.is-over').forEach(function (e) { e.classList.remove('is-over'); });
	});
	root.addEventListener('dragover', function (ev) {
		if (!dragged) { return; }
		var area = ev.target.closest('.gp-widget-area');
		if (!area) { return; }
		ev.preventDefault();
		var ul = area.querySelector('.gp-widget-list');
		root.querySelectorAll('.is-over').forEach(function (e) { if (e !== area) { e.classList.remove('is-over'); } });
		area.classList.add('is-over');
		var after = Array.prototype.find.call(ul.children, function (li) {
			if (li === dragged) { return false; }
			var r = li.getBoundingClientRect();
			return ev.clientY < r.top + r.height / 2;
		});
		if (after) { ul.insertBefore(dragged, after); } else { ul.appendChild(dragged); }
	});
	root.addEventListener('drop', function (ev) { if (dragged) { ev.preventDefault(); } });

	// Keyboard: up/down and "move to".
	root.addEventListener('click', function (ev) {
		var b = ev.target.closest('[data-move]');
		if (!b) { return; }
		ev.preventDefault();
		var li = b.closest('.gp-widget');
		if (b.getAttribute('data-move') === 'up' && li.previousElementSibling) { li.parentNode.insertBefore(li, li.previousElementSibling); }
		else if (b.getAttribute('data-move') === 'down' && li.nextElementSibling) { li.parentNode.insertBefore(li.nextElementSibling, li); }
		else { return; }
		b.focus();
		saveOrder();
	});
	root.addEventListener('change', function (ev) {
		var sel = ev.target.closest('[data-move-to]');
		if (!sel) { return; }
		var li = sel.closest('.gp-widget');
		root.querySelector('.gp-widget-list[data-area="' + sel.value + '"]').appendChild(li);
		saveOrder();
		li.scrollIntoView({ block: 'nearest' });
	});

	// Save forms with fetch.
	root.addEventListener('submit', function (ev) {
		var form = ev.target.closest('.gp-widget-form');
		if (!form || (ev.submitter && ev.submitter.getAttribute('formaction'))) { return; }
		ev.preventDefault();
		var btn = form.querySelector('.button-primary');
		btn.disabled = true;
		fetch(form.action, { method: 'POST', body: new FormData(form), credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf, Accept: 'application/json' } })
			.then(function (r) { return r.json().then(function (j) { if (!r.ok) { throw new Error(j.error || 'Saving failed.'); } return j; }); })
			.then(function (j) {
				var li = form.closest('.gp-widget');
				var t = li.querySelector('.gp-widget-title');
				if (j.title) {
					if (!t) { t = document.createElement('span'); t.className = 'gp-widget-title'; li.querySelector('summary strong').after(t); }
					t.textContent = ': ' + j.title;
				} else if (t) { t.remove(); }
				status.textContent = 'Widget saved.';
			})
			.catch(function (err) { status.textContent = err.message; })
			.finally(function () { btn.disabled = false; });
	});
})();
