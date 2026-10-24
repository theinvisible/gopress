// Tiny DOM helpers (no framework).

// h('button', {class: 'x', onclick: fn}, 'label', child, …)
export function h(tag, attrs, ...children) {
	const el = document.createElement(tag);
	for (const [k, v] of Object.entries(attrs || {})) {
		if (v == null || v === false) continue;
		if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
		else if (k === 'class') el.className = v;
		else if (k === 'dataset') Object.assign(el.dataset, v);
		else if (k === 'text') el.textContent = v;
		else if (v === true) el.setAttribute(k, '');
		else el.setAttribute(k, v);
	}
	for (const c of children.flat()) {
		if (c == null || c === false) continue;
		el.append(c instanceof Node ? c : document.createTextNode(String(c)));
	}
	return el;
}

export function icon(name) {
	return h('span', { class: 'dashicons dashicons-' + name, 'aria-hidden': 'true' });
}

// dialog opens a modal <dialog> with a form; resolves with the form data
// (or null when cancelled).
export function dialog({ title, body, submit = 'Save', wide = false, extraButtons = [] }) {
	return new Promise((resolve) => {
		const form = h('form', { method: 'dialog', class: 'gp-dialog-form' },
			h('header', {}, h('h2', { text: title }),
				h('button', { type: 'button', class: 'gp-dialog-close', 'aria-label': 'Close', onclick: () => close(null) }, icon('no-alt'))),
			h('div', { class: 'gp-dialog-body' }, body),
			h('footer', {}, ...extraButtons,
				h('button', { type: 'button', class: 'button', onclick: () => close(null) }, 'Cancel'),
				h('button', { type: 'submit', class: 'button button-primary' }, submit)));
		const dlg = h('dialog', { class: 'gp-dialog' + (wide ? ' gp-dialog-wide' : '') }, form);
		let done = false;
		function close(v) {
			if (done) return;
			done = true;
			dlg.close();
			dlg.remove();
			resolve(v);
		}
		form.addEventListener('submit', (e) => {
			e.preventDefault();
			close(new FormData(form));
		});
		dlg.addEventListener('cancel', (e) => { e.preventDefault(); close(null); });
		dlg.addEventListener('click', (e) => { if (e.target === dlg) close(null); });
		document.body.append(dlg);
		dlg.showModal();
		const first = form.querySelector('input:not([type=hidden]), textarea, select');
		if (first) first.focus();
		dlg.gpClose = close;
	});
}

// escapeAttr escapes a value for a double-quoted shortcode attribute.
export function escapeAttr(s) {
	return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

export function escapeHTML(s) {
	return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

export function debounce(fn, ms) {
	let t;
	return (...a) => {
		clearTimeout(t);
		t = setTimeout(() => fn(...a), ms);
	};
}
