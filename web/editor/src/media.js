// Media picker: browse the media library, pick one or more files and
// choose how to insert them. Used by "Add Media", the slash menu, the
// featured image box and plugins (window.gopress.media.pick).
import { h, icon, escapeAttr, escapeHTML, debounce } from './dom.js';

const SIZE_LABELS = { thumbnail: 'Thumbnail', medium: 'Medium', medium_large: 'Medium Large', large: 'Large',
	'post-thumbnail': 'Post Thumbnail', full: 'Full Size' };

// sizeOptions lists the sizes of an item, smallest first (full last).
function sizeOptions(it) {
	return Object.keys(it.sizes).filter((s) => s !== 'full').sort((a, b) => it.sizes[a].width - it.sizes[b].width).concat(['full'])
		.map((s) => [s, (SIZE_LABELS[s] || s) + ' – ' + it.sizes[s].width + ' × ' + it.sizes[s].height]);
}

// currentPost returns the id of the edited post, saving a new post as a
// draft first (uploads are attached to it). 0 if there is none.
export async function currentPost() {
	const cp = window.gopress && window.gopress.currentPost;
	if (!cp) return 0;
	try { return Number(await cp.ensure()) || 0; } catch (e) { return 0; }
}

// saveDetails stores changed alt texts and captions of picked items.
function saveDetails(items) {
	const csrf = document.body.getAttribute('data-csrf');
	for (const it of items) {
		if (!it.can_edit || !it._orig || (it.alt === it._orig.alt && it.caption === it._orig.caption)) continue;
		const body = new URLSearchParams({ alt: it.alt || '', caption: it.caption || '' });
		fetch('/admin/api/media/' + it.id, { method: 'POST', body, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf, Accept: 'application/json' } });
	}
}

// pick({multiple, title, button, mode}) resolves with
// {items: [...], settings: {align, size, link, columns, caption}} or null.
// mode "insert" shows the display settings; "select" only selects.
export function pick({ url, multiple = false, title = 'Add Media', button = 'Insert', mode = 'insert', imagesOnly = false } = {}) {
	return new Promise((resolve) => {
		let page = 1;
		let pages = 1;
		let query = '';
		const selected = new Map();
		let focused = null;

		const grid = h('ul', { class: 'gp-media-grid', role: 'listbox', 'aria-multiselectable': multiple ? 'true' : 'false' });
		const more = h('button', { type: 'button', class: 'button gp-media-more', hidden: true, onclick: () => load(page + 1) }, 'Load more');
		const status = h('p', { class: 'gp-media-status', 'aria-live': 'polite' });
		const search = h('input', { type: 'search', class: 'gp-media-search', placeholder: 'Search media…', 'aria-label': 'Search media' });
		const fileInput = h('input', { type: 'file', multiple: true, hidden: true });
		const uploadBtn = h('button', { type: 'button', class: 'button gp-media-upload' }, icon('upload'), ' Upload files');
		uploadBtn.addEventListener('click', () => fileInput.click());
		fileInput.addEventListener('change', () => { uploadFiles(fileInput.files); fileInput.value = ''; });
		const side = h('aside', { class: 'gp-media-side' });
		const insertBtn = h('button', { type: 'button', class: 'button button-primary', disabled: true }, button);
		const dlg = h('dialog', { class: 'gp-dialog gp-media-dialog' },
			h('div', { class: 'gp-media-frame' },
				h('header', {}, h('h2', { text: title }), uploadBtn, fileInput, search,
					h('button', { type: 'button', class: 'gp-dialog-close', 'aria-label': 'Close', onclick: () => close(null) }, icon('no-alt'))),
				h('p', { class: 'gp-media-drop-hint' }, 'Drop files here to upload them.'),
				h('div', { class: 'gp-media-main' }, h('div', { class: 'gp-media-browser' }, grid, status, more), side),
				h('footer', {}, h('span', { class: 'gp-media-count' }), insertBtn)));

		function close(v) {
			dlg.close();
			dlg.remove();
			resolve(v);
		}
		dlg.addEventListener('cancel', (e) => { e.preventDefault(); close(null); });

		function load(p) {
			const qs = new URLSearchParams({ page: p, s: query });
			if (imagesOnly) qs.set('images', '1');
			status.textContent = 'Loading…';
			fetch(url + '?' + qs, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
				.then((r) => r.json())
				.then((data) => {
					if (p === 1) grid.replaceChildren();
					page = p;
					pages = data.pages || 1;
					for (const it of data.items) grid.append(tile(it));
					more.hidden = page >= pages;
					status.textContent = data.total ? '' : 'No media found.';
				})
				.catch(() => { status.textContent = 'The media library could not be loaded.'; });
		}

		// Uploads from the picker: button and drag and drop onto the dialog.
		async function uploadFiles(files) {
			const gp = window.gopress;
			if (!files || !files.length || !gp.uploadQueue) return;
			const post = await currentPost();
			let done = 0;
			const total = files.length;
			status.textContent = 'Uploading 1 of ' + total + '…';
			await gp.uploadQueue(files, { post }, {
				done: (_, it) => {
					done++;
					status.textContent = done < total ? 'Uploading ' + (done + 1) + ' of ' + total + '…' : '';
					if (imagesOnly && !it.mime.startsWith('image/')) return;
					const li = tile(it);
					grid.prepend(li);
					if (multiple || done === 1) li.click();
				},
				failed: (_, err) => { done++; status.textContent = err.message; },
			});
		}
		dlg.addEventListener('dragover', (e) => { if (e.dataTransfer && [...e.dataTransfer.types].includes('Files')) { e.preventDefault(); dlg.classList.add('is-dropping'); } });
		dlg.addEventListener('dragleave', (e) => { if (e.target === dlg) dlg.classList.remove('is-dropping'); });
		dlg.addEventListener('drop', (e) => {
			if (!e.dataTransfer || !e.dataTransfer.files.length) return;
			e.preventDefault();
			dlg.classList.remove('is-dropping');
			uploadFiles(e.dataTransfer.files);
		});

		function tile(it) {
			const isImage = it.mime.startsWith('image/');
			if (!it._orig) it._orig = { alt: it.alt || '', caption: it.caption || '' };
			const li = h('li', { role: 'option', tabindex: '0', 'aria-selected': 'false', 'aria-label': it.title || it.url },
				isImage ? h('img', { src: it.thumb, alt: '', loading: 'lazy', width: 150, height: 150 })
					: h('div', { class: 'gp-media-file' }, icon('media-default'), h('span', { text: it.url.split('/').pop() })),
				h('span', { class: 'gp-media-check' }, icon('yes')));
			li._item = it;
			const toggle = () => {
				if (!multiple) {
					grid.querySelectorAll('[aria-selected=true]').forEach((x) => x.setAttribute('aria-selected', 'false'));
					selected.clear();
				}
				if (selected.has(it.id) && multiple) {
					selected.delete(it.id);
					li.setAttribute('aria-selected', 'false');
				} else {
					selected.set(it.id, it);
					li.setAttribute('aria-selected', 'true');
				}
				focused = selected.has(it.id) ? it : [...selected.values()].pop() || null;
				renderSide();
			};
			li.addEventListener('click', toggle);
			li.addEventListener('dblclick', () => { if (!multiple) { toggle(); finish(); } });
			li.addEventListener('keydown', (e) => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); toggle(); } });
			if (selected.has(it.id)) li.setAttribute('aria-selected', 'true');
			return li;
		}

		const settings = { align: 'none', size: 'large', link: 'none', columns: '3', gallerySize: 'thumbnail', galleryLink: 'file' };
		function renderSide() {
			const n = selected.size;
			insertBtn.disabled = n === 0;
			dlg.querySelector('.gp-media-count').textContent = n ? n + ' selected' : '';
			if (multiple && n > 1) insertBtn.textContent = mode === 'insert' ? 'Insert gallery' : button;
			else insertBtn.textContent = button;
			side.replaceChildren();
			if (!focused) {
				side.append(h('p', { class: 'description' }, multiple ? 'Select one image, or several for a gallery.' : 'Select a file.'));
				return;
			}
			const it = focused;
			side.append(h('h3', { text: 'Attachment Details' }),
				it.mime.startsWith('image/') ? h('img', { src: it.sizes.medium ? it.sizes.medium.url : it.thumb, alt: '', class: 'gp-media-preview' }) : null,
				h('p', { class: 'gp-media-meta' }, h('strong', { text: it.url.split('/').pop() }), h('br'),
					it.width ? it.width + ' × ' + it.height + ' px' : it.mime));
			if (mode !== 'insert') return;
			const field = (label, input) => h('label', { class: 'gp-field' }, h('span', { text: label }), input);
			if (n <= 1) {
				const alt = h('input', { type: 'text', value: it.alt || '' });
				alt.addEventListener('input', () => { it.alt = alt.value; });
				const cap = h('textarea', { rows: 2 }, it.caption || '');
				cap.addEventListener('input', () => { it.caption = cap.value; });
				side.append(field('Alternative Text', alt), field('Caption', cap), h('h3', { text: 'Display Settings' }));
				const sel = (key, opts) => {
					const s = h('select', {}, ...opts.map(([v, l]) => h('option', { value: v, selected: settings[key] === v }, l)));
					s.addEventListener('change', () => { settings[key] = s.value; });
					return s;
				};
				if (it.mime.startsWith('image/')) {
					side.append(field('Alignment', sel('align', [['left', 'Left'], ['center', 'Center'], ['right', 'Right'], ['none', 'None']])),
						field('Link To', sel('link', [['none', 'None'], ['file', 'Media File'], ['post', 'Attachment Page']])),
						field('Size', sel('size', sizeOptions(it))));
					if (!it.sizes[settings.size]) settings.size = 'full';
				}
			} else {
				const sel = (key, opts) => {
					const s = h('select', {}, ...opts.map(([v, l]) => h('option', { value: v, selected: settings[key] === v }, l)));
					s.addEventListener('change', () => { settings[key] = s.value; });
					return s;
				};
				side.append(h('h3', { text: 'Gallery Settings' }),
					field('Link To', sel('galleryLink', [['file', 'Media File'], ['post', 'Attachment Page'], ['none', 'None']])),
					field('Columns', sel('columns', ['1', '2', '3', '4', '5', '6', '7', '8', '9'].map((c) => [c, c]))),
					field('Size', sel('gallerySize', [['thumbnail', 'Thumbnail'], ['medium', 'Medium'], ['large', 'Large'], ['full', 'Full Size']])));
			}
		}

		function finish() {
			if (!selected.size) return;
			saveDetails([...selected.values()]);
			close({ items: [...selected.values()], settings });
		}
		insertBtn.addEventListener('click', finish);
		search.addEventListener('input', debounce(() => { query = search.value.trim(); load(1); }, 250));

		document.body.append(dlg);
		dlg.showModal();
		renderSide();
		load(1);
		search.focus();
	});
}

// imageHTML builds the markup WordPress' media modal inserts for one image.
export function imageHTML(it, { align = 'none', size = 'large', link = 'none' } = {}) {
	const s = it.sizes[size] || it.sizes.full;
	const sizeName = it.sizes[size] ? size : 'full';
	let img = '<img class="align' + align + ' size-' + sizeName + ' wp-image-' + it.id + '" src="' + escapeAttr(s.url) +
		'" alt="' + escapeAttr(it.alt || '') + '" width="' + s.width + '" height="' + s.height + '" />';
	if (link === 'file') img = '<a href="' + escapeAttr(it.url) + '">' + img + '</a>';
	if (link === 'post') img = '<a href="' + escapeAttr(it.page) + '">' + img + '</a>';
	return img;
}

// captionShortcode wraps image HTML into [caption].
export function captionShortcode(it, html, { align = 'none', size = 'large' } = {}) {
	const s = it.sizes[size] || it.sizes.full;
	return '[caption id="attachment_' + it.id + '" align="align' + align + '" width="' + s.width + '"]' + html + ' ' +
		escapeHTML(it.caption.trim()) + '[/caption]';
}

export function galleryShortcode(items, { columns = '3', gallerySize = 'thumbnail', galleryLink = 'file' } = {}) {
	let attrs = '';
	if (galleryLink !== 'post') attrs += ' link="' + galleryLink + '"';
	if (columns !== '3') attrs += ' columns="' + columns + '"';
	if (gallerySize !== 'thumbnail') attrs += ' size="' + gallerySize + '"';
	return '[gallery' + attrs + ' ids="' + items.map((i) => i.id).join(',') + '"]';
}
