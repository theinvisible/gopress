// GoPress post editor: a ProseMirror/TipTap editor with a toolbar, a
// floating format menu, slash commands, Markdown shortcuts, an HTML source
// view and a distraction-free mode. It replaces the content <textarea>
// inside [data-gp-editor] and writes the HTML back into it.
import { Editor, Extension } from '@tiptap/core';
import { BubbleMenu } from '@tiptap/extension-bubble-menu';
import Suggestion from '@tiptap/suggestion';
import { extensions } from './schema.js';
import { importHTML, exportHTML, canonical, debugParse } from './content.js';
import { h, icon, dialog, debounce } from './dom.js';
import { pick, imageHTML, captionShortcode, galleryShortcode } from './media.js';
import './editor.css';

const gopress = (window.gopress = window.gopress || {});
const inserts = []; // registered by plugins: {id, label, icon, keywords, onClick(api)}

// ---- Node views ----

function rawView({ node, editor, getPos }) {
	let current = node;
	const code = h('pre', { class: 'gp-raw-code' });
	const preview = h('iframe', { class: 'gp-raw-preview', sandbox: '', title: 'Preview', hidden: true });
	const toggle = h('button', { type: 'button', class: 'button-link', onclick: () => setPreview(preview.hidden) }, 'Preview');
	function setPreview(on) {
		preview.hidden = !on;
		code.hidden = on;
		toggle.textContent = on ? 'Code' : 'Preview';
		if (on) {
			preview.srcdoc = '<!doctype html><meta charset="utf-8"><style>body{font:14px/1.6 system-ui,sans-serif;margin:8px}img{max-width:100%;height:auto}</style>' +
				current.attrs.html;
		}
	}
	const dom = h('div', { class: 'gp-block gp-raw', contenteditable: 'false' },
		h('div', { class: 'gp-block-bar' }, icon('editor-code'), h('span', { class: 'gp-block-label' }, 'HTML'),
			toggle,
			h('button', { type: 'button', class: 'button-link', onclick: () => editRaw() }, 'Edit')),
		code, preview);
	const render = () => { code.textContent = current.attrs.html; if (!preview.hidden) setPreview(true); };
	// Media blocks are easier to recognize rendered.
	if (/<(img|figure|video|iframe)\b/i.test(node.attrs.html)) queueMicrotask(() => setPreview(true));
	async function editRaw() {
		const ta = h('textarea', { name: 'html', rows: 16, class: 'gp-code-input', spellcheck: 'false' }, current.attrs.html);
		const fd = await dialog({ title: 'Edit HTML', body: ta, wide: true });
		if (!fd) return;
		editor.chain().focus().command(({ tr }) => {
			tr.setNodeMarkup(getPos(), null, { html: fd.get('html') });
			return true;
		}).run();
	}
	dom.addEventListener('dblclick', editRaw);
	render();
	return {
		dom,
		update(n) {
			if (n.type !== current.type) return false;
			current = n;
			render();
			return true;
		},
		stopEvent: (e) => e.target.closest && !!e.target.closest('button'),
		ignoreMutation: () => true,
	};
}

function shortcodeView({ node, editor, getPos }) {
	let current = node;
	const label = h('code', { class: 'gp-sc-text' });
	const name = h('span', { class: 'gp-block-label' });
	const dom = h('div', { class: 'gp-block gp-sc', contenteditable: 'false' },
		h('div', { class: 'gp-block-bar' }, icon('shortcode'), name,
			h('button', { type: 'button', class: 'button-link', onclick: () => edit() }, 'Edit')),
		label);
	const render = () => {
		const t = current.attrs.text;
		const m = t.match(/^\[\[?([\w-]+)/);
		name.textContent = m ? m[1] : 'Shortcode';
		label.textContent = t.length > 400 ? t.slice(0, 400) + '…' : t;
		const custom = m && gopress.editor && gopress.editor.shortcodeViews[m[1]];
		if (custom) custom(dom, t);
	};
	async function edit() {
		const ta = h('textarea', { name: 'text', rows: 6, class: 'gp-code-input', spellcheck: 'false' }, current.attrs.text);
		const fd = await dialog({ title: 'Edit Shortcode', body: ta, wide: true });
		if (!fd) return;
		const text = fd.get('text').trim();
		editor.chain().focus().command(({ tr }) => {
			if (text) tr.setNodeMarkup(getPos(), null, { text });
			else tr.delete(getPos(), getPos() + current.nodeSize);
			return true;
		}).run();
	}
	dom.addEventListener('dblclick', edit);
	render();
	return {
		dom,
		update(n) {
			if (n.type !== current.type) return false;
			current = n;
			render();
			return true;
		},
		stopEvent: (e) => e.target.closest && !!e.target.closest('button'),
		ignoreMutation: () => true,
	};
}

function moreView({ node }) {
	return {
		dom: h('div', { class: 'gp-more', contenteditable: 'false', title: 'Content before this line is the excerpt on archive pages' },
			h('span', {}, node.attrs.text ? 'MORE: ' + node.attrs.text : 'MORE')),
		ignoreMutation: () => true,
	};
}

function imageView({ node, editor, getPos }) {
	let current = node;
	const img = h('img', {});
	const dom = h('span', { class: 'gp-image', contenteditable: 'false' }, img);
	const render = () => {
		const a = current.attrs.attrs || {};
		for (const k of Array.from(img.attributes)) img.removeAttribute(k.name);
		for (const [k, v] of Object.entries(a)) {
			if (/^on/i.test(k)) continue;
			try { img.setAttribute(k, v); } catch (e) { /* invalid attribute name */ }
		}
		img.setAttribute('draggable', 'false');
	};
	dom.addEventListener('dblclick', () => editImage(editor, getPos(), current));
	render();
	return {
		dom,
		update(n) {
			if (n.type !== current.type) return false;
			current = n;
			render();
			return true;
		},
		selectNode() { dom.classList.add('is-selected'); },
		deselectNode() { dom.classList.remove('is-selected'); },
		ignoreMutation: () => true,
	};
}

async function editImage(editor, pos, node) {
	const a = { ...(node.attrs.attrs || {}) };
	const cls = (a.class || '').split(/\s+/).filter(Boolean);
	const align = (cls.find((c) => /^align(left|right|center|none)$/.test(c)) || 'alignnone').slice(5);
	const body = h('div', { class: 'gp-form-grid' },
		h('label', {}, h('span', { text: 'Alternative text' }), h('input', { type: 'text', name: 'alt', value: a.alt || '' })),
		h('label', {}, h('span', { text: 'Alignment' }), h('select', { name: 'align' },
			...['none', 'left', 'center', 'right'].map((v) => h('option', { value: v, selected: v === align }, v[0].toUpperCase() + v.slice(1))))),
		h('label', {}, h('span', { text: 'Title' }), h('input', { type: 'text', name: 'title', value: a.title || '' })));
	const fd = await dialog({ title: 'Image', body, extraButtons: [h('button', { type: 'button', class: 'button button-link-delete', onclick: (e) => {
		e.target.closest('dialog').gpClose('delete');
	} }, 'Remove image')] });
	if (!fd) return;
	if (fd === 'delete') {
		editor.chain().focus().deleteRange({ from: pos, to: pos + node.nodeSize }).run();
		return;
	}
	a.alt = fd.get('alt');
	if (fd.get('title')) a.title = fd.get('title'); else delete a.title;
	a.class = cls.filter((c) => !/^align/.test(c)).concat('align' + fd.get('align')).join(' ');
	editor.chain().focus().command(({ tr }) => { tr.setNodeMarkup(pos, null, { attrs: a }); return true; }).run();
}

// ---- Commands shared by toolbar, slash menu and shortcuts ----

function setAlign(editor, align) {
	const { state } = editor;
	const { from, to } = state.selection;
	editor.chain().focus().command(({ tr }) => {
		state.doc.nodesBetween(from, to, (node, pos) => {
			if (!['paragraph', 'heading'].includes(node.type.name)) return;
			const attrs = { ...(node.attrs.htmlAttrs || {}) };
			const style = (attrs.style || '').split(';').map((s) => s.trim()).filter((s) => s && !/^text-align\s*:/.test(s));
			if (align) style.push('text-align: ' + align);
			if (style.length) attrs.style = style.join('; ') + ';'; else delete attrs.style;
			tr.setNodeMarkup(pos, null, { ...node.attrs, htmlAttrs: Object.keys(attrs).length ? attrs : null });
		});
		return true;
	}).run();
}

function currentAlign(editor) {
	const { $from } = editor.state.selection;
	for (let d = $from.depth; d > 0; d--) {
		const n = $from.node(d);
		if (n.type.name === 'paragraph' || n.type.name === 'heading') {
			const m = ((n.attrs.htmlAttrs || {}).style || '').match(/text-align\s*:\s*(\w+)/);
			return m ? m[1] : null;
		}
	}
	return null;
}

async function editLink(editor) {
	const prev = editor.getAttributes('link');
	const { from, to, empty } = editor.state.selection;
	const body = h('div', { class: 'gp-form-grid' },
		h('label', {}, h('span', { text: 'URL' }), h('input', { type: 'text', name: 'href', value: prev.href || '', placeholder: 'https://', required: true })),
		empty && !prev.href ? h('label', {}, h('span', { text: 'Link text' }), h('input', { type: 'text', name: 'text' })) : null,
		h('label', { class: 'gp-check' }, h('input', { type: 'checkbox', name: 'blank', checked: prev.target === '_blank' }), 'Open in a new tab'),
		h('label', { class: 'gp-check' }, h('input', { type: 'checkbox', name: 'nofollow', checked: /nofollow/.test(prev.rel || '') }), 'Mark as nofollow'));
	const extra = prev.href ? [h('button', { type: 'button', class: 'button button-link-delete', onclick: (e) => e.target.closest('dialog').gpClose('unlink') }, 'Remove link')] : [];
	const fd = await dialog({ title: prev.href ? 'Edit Link' : 'Insert Link', body, submit: prev.href ? 'Update' : 'Add Link', extraButtons: extra });
	if (!fd) { editor.commands.focus(); return; }
	if (fd === 'unlink') { editor.chain().focus().extendMarkRange('link').unsetLink().run(); return; }
	let href = fd.get('href').trim();
	if (!href) return;
	if (!/^(https?:|mailto:|tel:|\/|#|\?)/i.test(href)) {
		href = /^[\w.-]+@[\w.-]+\.\w+$/.test(href) ? 'mailto:' + href : 'https://' + href;
	}
	const rel = [];
	if (fd.get('blank')) rel.push('noopener');
	if (fd.get('nofollow')) rel.push('nofollow');
	const attrs = { href, target: fd.get('blank') ? '_blank' : null, rel: rel.length ? rel.join(' ') : null };
	const text = fd.get('text');
	if (empty && !prev.href) {
		editor.chain().focus().insertContent({ type: 'text', text: text || href, marks: [{ type: 'link', attrs }] }).run();
	} else {
		editor.chain().focus().setTextSelection({ from, to }).extendMarkRange('link').setLink(attrs).run();
	}
}

async function insertMedia(api, { gallery = false } = {}) {
	const res = await pick({ url: api.config.media, multiple: true, title: gallery ? 'Create Gallery' : 'Add Media', imagesOnly: gallery });
	if (!res) { api.editor.commands.focus(); return; }
	const { items, settings } = res;
	const ed = api.editor.chain().focus();
	if (items.length > 1 || gallery) {
		ed.insertContent({ type: 'shortcode', attrs: { text: galleryShortcode(items, settings) } }).run();
		return;
	}
	const it = items[0];
	if (!it.mime.startsWith('image/')) {
		ed.insertContent('<a href="' + it.url.replace(/"/g, '&quot;') + '">' + (it.title || it.url.split('/').pop()).replace(/</g, '&lt;') + '</a>').run();
		return;
	}
	const html = imageHTML(it, settings);
	if (it.caption && it.caption.trim()) {
		ed.insertContent({ type: 'shortcode', attrs: { text: captionShortcode(it, html, settings) } }).run();
	} else {
		ed.insertContent(html).run();
	}
}

async function insertHTMLBlock(editor) {
	const ta = h('textarea', { name: 'html', rows: 12, class: 'gp-code-input', spellcheck: 'false', placeholder: '<div>…</div>' });
	const fd = await dialog({ title: 'Insert HTML', body: ta, wide: true, submit: 'Insert' });
	if (!fd || !fd.get('html').trim()) return;
	editor.chain().focus().insertContent({ type: 'rawHTML', attrs: { html: fd.get('html').trim() } }).run();
}

async function insertShortcode(editor) {
	const ta = h('input', { type: 'text', name: 'text', class: 'gp-code-input', placeholder: '[shortcode attr="value"]', required: true });
	const fd = await dialog({ title: 'Insert Shortcode', body: ta, submit: 'Insert' });
	if (!fd || !fd.get('text').trim()) return;
	editor.chain().focus().insertContent({ type: 'shortcode', attrs: { text: fd.get('text').trim() } }).run();
}

// ---- Slash commands ----

function slashItems(api) {
	const e = api.editor;
	const base = [
		{ id: 'p', label: 'Paragraph', icon: 'editor-paragraph', keys: 'text p', run: () => e.chain().focus().setParagraph().run() },
		{ id: 'h2', label: 'Heading 2', icon: 'heading', keys: 'h2 title heading', run: () => e.chain().focus().setHeading({ level: 2 }).run() },
		{ id: 'h3', label: 'Heading 3', icon: 'heading', keys: 'h3 heading subtitle', run: () => e.chain().focus().setHeading({ level: 3 }).run() },
		{ id: 'h4', label: 'Heading 4', icon: 'heading', keys: 'h4 heading', run: () => e.chain().focus().setHeading({ level: 4 }).run() },
		{ id: 'ul', label: 'Bulleted list', icon: 'editor-ul', keys: 'list ul bullet', run: () => e.chain().focus().toggleBulletList().run() },
		{ id: 'ol', label: 'Numbered list', icon: 'editor-ol', keys: 'list ol ordered number', run: () => e.chain().focus().toggleOrderedList().run() },
		{ id: 'quote', label: 'Quote', icon: 'editor-quote', keys: 'quote blockquote citation', run: () => e.chain().focus().toggleBlockquote().run() },
		{ id: 'pre', label: 'Code / Preformatted', icon: 'editor-code', keys: 'code pre preformatted', run: () => e.chain().focus().togglePre().run() },
		{ id: 'image', label: 'Image or file', icon: 'format-image', keys: 'image picture photo media file', run: () => insertMedia(api) },
		{ id: 'gallery', label: 'Gallery', icon: 'format-gallery', keys: 'gallery images photos', run: () => insertMedia(api, { gallery: true }) },
		{ id: 'table', label: 'Table', icon: 'editor-table', keys: 'table grid', run: () => e.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: false }).run() },
		{ id: 'hr', label: 'Separator', icon: 'minus', keys: 'hr separator divider line', run: () => e.chain().focus().setHorizontalRule().run() },
		{ id: 'more', label: 'Read more', icon: 'editor-insertmore', keys: 'more excerpt break', run: () => e.chain().focus().insertMore().run() },
		{ id: 'html', label: 'Custom HTML', icon: 'html', keys: 'html code embed iframe', run: () => insertHTMLBlock(e) },
		{ id: 'shortcode', label: 'Shortcode', icon: 'shortcode', keys: 'shortcode plugin', run: () => insertShortcode(e) },
	];
	return base.concat(inserts.map((i) => ({ id: i.id, label: i.label, icon: i.icon || 'admin-plugins', keys: (i.keywords || '') + ' ' + i.label.toLowerCase(),
		run: () => i.onClick(api) })));
}

function slashExtension(api) {
	return Extension.create({
		name: 'gpSlash',
		addProseMirrorPlugins() {
			return [Suggestion({
				editor: this.editor,
				char: '/',
				allowSpaces: false,
				startOfLine: false,
				allow: ({ state, range }) => {
					const $from = state.doc.resolve(range.from);
					if ($from.parent.type.name !== 'paragraph') return false;
					const before = $from.parent.textBetween(0, $from.parentOffset, '\0', '\0');
					return before.trim() === '';
				},
				items: ({ query }) => {
					const q = query.toLowerCase();
					return slashItems(api).filter((it) => !q || it.label.toLowerCase().includes(q) || it.keys.includes(q)).slice(0, 12);
				},
				command: ({ editor, range, props }) => {
					editor.chain().focus().deleteRange(range).run();
					props.run();
				},
				render: () => {
					let menu, items = [], index = 0, command;
					const draw = () => {
						menu.replaceChildren(...(items.length ? items.map((it, i) => h('button', {
							type: 'button', class: 'gp-slash-item' + (i === index ? ' is-active' : ''), role: 'option',
							'aria-selected': i === index ? 'true' : 'false',
							onmousedown: (ev) => { ev.preventDefault(); command(it); },
						}, icon(it.icon), h('span', { text: it.label }))) : [h('div', { class: 'gp-slash-empty', text: 'No matching block' })]));
					};
					const place = (props) => {
						const r = props.clientRect && props.clientRect();
						if (!r) return;
						const top = r.bottom + window.scrollY + 6;
						menu.style.left = Math.min(r.left + window.scrollX, window.scrollX + document.documentElement.clientWidth - 280) + 'px';
						menu.style.top = top + 'px';
					};
					return {
						onStart(props) {
							menu = h('div', { class: 'gp-slash', role: 'listbox', 'aria-label': 'Insert block' });
							document.body.append(menu);
							items = props.items;
							index = 0;
							command = props.command;
							draw();
							place(props);
						},
						onUpdate(props) {
							items = props.items;
							index = 0;
							command = props.command;
							draw();
							place(props);
						},
						onKeyDown({ event }) {
							if (event.key === 'ArrowDown') { index = (index + 1) % Math.max(items.length, 1); draw(); return true; }
							if (event.key === 'ArrowUp') { index = (index - 1 + items.length) % Math.max(items.length, 1); draw(); return true; }
							if (event.key === 'Enter' || event.key === 'Tab') { if (items[index]) command(items[index]); return true; }
							if (event.key === 'Escape') { menu.remove(); return true; }
							return false;
						},
						onExit() { if (menu) menu.remove(); },
					};
				},
			})];
		},
	});
}

// ---- Toolbar ----

function toolbar(api) {
	const e = api.editor;
	const btn = (iconName, label, run, isActive, shortcut) => {
		const b = h('button', { type: 'button', class: 'gp-tb', title: label + (shortcut ? ' (' + shortcut + ')' : ''), 'aria-label': label,
			onmousedown: (ev) => ev.preventDefault(), onclick: run }, icon(iconName));
		b._active = isActive;
		return b;
	};
	const mod = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl+';
	const format = h('select', { class: 'gp-tb-format', 'aria-label': 'Paragraph format' },
		h('option', { value: 'p' }, 'Paragraph'),
		h('option', { value: 'h2' }, 'Heading 2'), h('option', { value: 'h3' }, 'Heading 3'),
		h('option', { value: 'h4' }, 'Heading 4'), h('option', { value: 'h5' }, 'Heading 5'), h('option', { value: 'h6' }, 'Heading 6'),
		h('option', { value: 'pre' }, 'Preformatted'));
	format.addEventListener('change', () => {
		const v = format.value;
		const c = e.chain().focus();
		if (v === 'p') c.setParagraph().run();
		else if (v === 'pre') c.togglePre().run();
		else c.setHeading({ level: Number(v[1]) }).run();
	});
	const buttons = [
		btn('editor-bold', 'Bold', () => e.chain().focus().toggleBold().run(), () => e.isActive('bold'), mod + 'B'),
		btn('editor-italic', 'Italic', () => e.chain().focus().toggleItalic().run(), () => e.isActive('italic'), mod + 'I'),
		btn('editor-strikethrough', 'Strikethrough', () => e.chain().focus().toggleStrike().run(), () => e.isActive('strike'), mod + 'Shift+S'),
		btn('editor-code', 'Inline code', () => e.chain().focus().toggleCode().run(), () => e.isActive('code'), mod + 'E'),
		'|',
		btn('editor-ul', 'Bulleted list', () => e.chain().focus().toggleBulletList().run(), () => e.isActive('bulletList'), mod + 'Shift+8'),
		btn('editor-ol', 'Numbered list', () => e.chain().focus().toggleOrderedList().run(), () => e.isActive('orderedList'), mod + 'Shift+7'),
		btn('editor-quote', 'Quote', () => e.chain().focus().toggleBlockquote().run(), () => e.isActive('blockquote'), mod + 'Shift+B'),
		btn('minus', 'Separator', () => e.chain().focus().setHorizontalRule().run()),
		'|',
		btn('editor-alignleft', 'Align left', () => setAlign(e, null), () => !currentAlign(e) || currentAlign(e) === 'left'),
		btn('editor-aligncenter', 'Align center', () => setAlign(e, 'center'), () => currentAlign(e) === 'center'),
		btn('editor-alignright', 'Align right', () => setAlign(e, 'right'), () => currentAlign(e) === 'right'),
		'|',
		btn('admin-links', 'Link', () => editLink(e), () => e.isActive('link'), mod + 'K'),
		btn('editor-unlink', 'Remove link', () => e.chain().focus().extendMarkRange('link').unsetLink().run()),
		btn('editor-table', 'Table', () => e.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: false }).run()),
		btn('editor-insertmore', 'Read more', () => e.chain().focus().insertMore().run()),
		'|',
		btn('undo', 'Undo', () => e.chain().focus().undo().run(), null, mod + 'Z'),
		btn('redo', 'Redo', () => e.chain().focus().redo().run(), null, mod + 'Shift+Z'),
	];
	const tableTools = h('div', { class: 'gp-tb-table', hidden: true },
		h('span', { class: 'gp-tb-label' }, 'Table:'),
		btn('table-row-before', 'Add row above', () => e.chain().focus().addRowBefore().run()),
		btn('table-row-after', 'Add row below', () => e.chain().focus().addRowAfter().run()),
		btn('table-row-delete', 'Delete row', () => e.chain().focus().deleteRow().run()),
		btn('table-col-before', 'Add column before', () => e.chain().focus().addColumnBefore().run()),
		btn('table-col-after', 'Add column after', () => e.chain().focus().addColumnAfter().run()),
		btn('table-col-delete', 'Delete column', () => e.chain().focus().deleteColumn().run()),
		btn('trash', 'Delete table', () => e.chain().focus().deleteTable().run()));
	const bar = h('div', { class: 'gp-toolbar', role: 'toolbar', 'aria-label': 'Formatting' }, format,
		...buttons.map((b) => (b === '|' ? h('span', { class: 'gp-tb-sep', 'aria-hidden': 'true' }) : b)), tableTools);
	const update = () => {
		for (const b of buttons) {
			if (b === '|' || !b._active) continue;
			const on = b._active();
			b.classList.toggle('is-active', on);
			b.setAttribute('aria-pressed', on ? 'true' : 'false');
		}
		let f = 'p';
		for (let l = 2; l <= 6; l++) if (e.isActive('heading', { level: l })) f = 'h' + l;
		if (e.isActive('pre')) f = 'pre';
		format.value = f;
		tableTools.hidden = !e.isActive('table');
	};
	return { bar, update };
}

function bubbleMenu(api) {
	const e = api.editor;
	const b = (name, label, run, active) => {
		const el = h('button', { type: 'button', class: 'gp-bm', 'aria-label': label, title: label, onmousedown: (ev) => ev.preventDefault(), onclick: run }, icon(name));
		el._active = active;
		return el;
	};
	const items = [
		b('editor-bold', 'Bold', () => e.chain().focus().toggleBold().run(), () => e.isActive('bold')),
		b('editor-italic', 'Italic', () => e.chain().focus().toggleItalic().run(), () => e.isActive('italic')),
		b('editor-strikethrough', 'Strikethrough', () => e.chain().focus().toggleStrike().run(), () => e.isActive('strike')),
		b('editor-code', 'Code', () => e.chain().focus().toggleCode().run(), () => e.isActive('code')),
		b('admin-links', 'Link', () => editLink(e), () => e.isActive('link')),
		h('span', { class: 'gp-tb-sep' }),
		b('heading', 'Heading', () => e.chain().focus().toggleHeading({ level: 2 }).run(), () => e.isActive('heading', { level: 2 })),
		b('editor-quote', 'Quote', () => e.chain().focus().toggleBlockquote().run(), () => e.isActive('blockquote')),
	];
	const el = h('div', { class: 'gp-bubble', role: 'toolbar', 'aria-label': 'Text formatting' }, ...items);
	el.gpUpdate = () => items.forEach((i) => i._active && i.classList.toggle('is-active', i._active()));
	return el;
}

// ---- Mount ----

function mount(root) {
	const textarea = root.querySelector('textarea');
	const config = JSON.parse(root.getAttribute('data-gp-editor') || '{}');
	const api = { config, editor: null, inserts };
	const body = h('div', { class: 'gp-editor-body' });
	const source = h('textarea', { class: 'gp-source', spellcheck: 'false', hidden: true, 'aria-label': 'HTML source' });
	const words = h('span', { class: 'gp-wordcount' });
	const savedState = h('span', { class: 'gp-savestate', 'aria-live': 'polite' });
	const bubble = bubbleMenu(api);

	const editor = new Editor({
		element: body,
		extensions: [
			...extensions({ placeholder: config.placeholder }),
			slashExtension(api),
			BubbleMenu.configure({
				element: bubble,
				shouldShow: ({ editor: ed, state, from, to }) => from !== to && ed.isEditable && !ed.isActive('image') &&
					!['rawHTML', 'shortcode', 'more'].includes(state.selection.node && state.selection.node.type.name),
			}),
		],
		editorProps: {
			attributes: { class: 'gp-content entry-content', 'aria-label': 'Content', role: 'textbox', 'aria-multiline': 'true' },
			// Office/Docs pastes: drop their inline styles and classes.
			transformPastedHTML: (html) => html.replace(/ (style|class|lang|dir)="[^"]*"/gi, '').replace(/<\/?(span|font|o:p)[^>]*>/gi, ''),
		},
		content: '',
	});
	api.editor = editor;
	editor.view.dom.setAttribute('spellcheck', 'true');
	editor.view.setProps({
		nodeViews: {
			rawHTML: (node, view, getPos) => rawView({ node, editor, getPos }),
			shortcode: (node, view, getPos) => shortcodeView({ node, editor, getPos }),
			more: (node) => moreView({ node }),
			image: (node, view, getPos) => imageView({ node, editor, getPos }),
		},
	});

	const setHTML = (html) => {
		const doc = importHTML(editor.schema, html || '');
		editor.commands.setContent(doc.toJSON(), { emitUpdate: false });
	};
	const getHTML = () => (source.hidden ? exportHTML(editor.schema, editor.state.doc) : source.value);
	setHTML(textarea.value);
	const initial = getHTML();

	const tb = toolbar(api);
	let mode = 'visual';
	const tabVisual = h('button', { type: 'button', class: 'gp-tab is-active', 'aria-pressed': 'true' }, 'Visual');
	const tabHTML = h('button', { type: 'button', class: 'gp-tab', 'aria-pressed': 'false' }, 'HTML');
	const setMode = (m) => {
		if (m === mode) return;
		if (m === 'html') {
			source.value = exportHTML(editor.schema, editor.state.doc);
			source.hidden = false;
			body.hidden = true;
			tb.bar.classList.add('is-disabled');
			autosize();
			source.focus();
		} else {
			setHTML(source.value);
			source.hidden = true;
			body.hidden = false;
			tb.bar.classList.remove('is-disabled');
			editor.commands.focus();
		}
		mode = m;
		tabVisual.classList.toggle('is-active', m === 'visual');
		tabHTML.classList.toggle('is-active', m === 'html');
		tabVisual.setAttribute('aria-pressed', m === 'visual' ? 'true' : 'false');
		tabHTML.setAttribute('aria-pressed', m === 'html' ? 'true' : 'false');
		try { localStorage.setItem('gp-editor-mode', m); } catch (e) { /* private mode */ }
	};
	tabVisual.addEventListener('click', () => setMode('visual'));
	tabHTML.addEventListener('click', () => setMode('html'));
	const autosize = () => { source.style.height = 'auto'; source.style.height = Math.max(400, source.scrollHeight + 4) + 'px'; };
	source.addEventListener('input', () => { autosize(); changed(); });

	const focusBtn = h('button', { type: 'button', class: 'gp-tab gp-focus-toggle', title: 'Distraction-free writing (Ctrl+Shift+F)', 'aria-pressed': 'false' }, icon('editor-expand'));
	const toggleFocus = () => {
		const on = document.body.classList.toggle('gp-focus-mode');
		focusBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
		if (!source.hidden) source.focus(); else editor.commands.focus();
	};
	focusBtn.addEventListener('click', toggleFocus);
	const helpBtn = h('button', { type: 'button', class: 'gp-tab', title: 'Keyboard shortcuts', onclick: () => showHelp() }, icon('editor-help'));
	const mediaBtn = h('button', { type: 'button', class: 'button gp-add-media', onclick: () => insertMedia(api) }, icon('admin-media'), ' Add Media');

	const header = h('div', { class: 'gp-editor-header' }, mediaBtn, ...inserts.filter((i) => i.button).map((i) =>
		h('button', { type: 'button', class: 'button', onclick: () => i.onClick(api) }, i.icon ? icon(i.icon) : null, ' ' + i.label)),
	h('div', { class: 'gp-tabs' }, tabVisual, tabHTML, focusBtn, helpBtn));
	const status = h('div', { class: 'gp-statusbar' }, words, savedState);
	textarea.hidden = true;
	root.classList.add('gp-editor');
	root.append(header, tb.bar, body, source, status);

	// Change tracking.
	const listeners = [];
	let dirty = false;
	const countWords = debounce(() => {
		const text = editor.state.doc.textBetween(0, editor.state.doc.content.size, ' ', ' ');
		const n = (text.match(/[\p{L}\p{N}]+(?:['’-][\p{L}\p{N}]+)*/gu) || []).length;
		words.textContent = 'Word count: ' + n;
	}, 300);
	function changed() {
		dirty = true;
		listeners.forEach((fn) => fn());
	}
	editor.on('update', () => { changed(); countWords(); });
	editor.on('selectionUpdate', () => { tb.update(); bubble.gpUpdate(); });
	editor.on('transaction', () => { tb.update(); bubble.gpUpdate(); });
	tb.update();
	countWords();

	// Keyboard shortcuts on the whole editor.
	root.addEventListener('keydown', (ev) => {
		const m = ev.ctrlKey || ev.metaKey;
		if (m && ev.key.toLowerCase() === 'k' && mode === 'visual') { ev.preventDefault(); editLink(editor); }
		if (m && ev.shiftKey && ev.key.toLowerCase() === 'f') { ev.preventDefault(); toggleFocus(); }
		if (ev.key === 'Escape' && document.body.classList.contains('gp-focus-mode')) toggleFocus();
	});

	// The form gets the HTML on submit.
	const form = root.closest('form');
	if (form) form.addEventListener('submit', () => { textarea.value = getHTML(); }, true);

	try { if (localStorage.getItem('gp-editor-mode') === 'html') setMode('html'); } catch (e) { /* private mode */ }

	const instance = {
		getHTML,
		setHTML: (html) => { if (source.hidden) setHTML(html); else { source.value = html; autosize(); } changed(); },
		isDirty: () => dirty || getHTML() !== initial,
		markSaved: (text) => { dirty = false; savedState.textContent = text || ''; },
		onChange: (fn) => listeners.push(fn),
		insertHTML: (html) => editor.chain().focus().insertContent(html).run(),
		insertShortcode: (text) => editor.chain().focus().insertContent({ type: 'shortcode', attrs: { text } }).run(),
		focus: () => editor.commands.focus(),
		tiptap: editor,
	};
	gopress.editor.instance = instance;
	root.dispatchEvent(new CustomEvent('gp-editor-ready', { bubbles: true, detail: instance }));
	return instance;
}

function showHelp() {
	const mod = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl';
	const rows = [
		['/', 'Insert a block (heading, image, gallery, table, …)'],
		['## + space', 'Heading 2 (### for heading 3, …)'],
		['- or * + space', 'Bulleted list'], ['1. + space', 'Numbered list'], ['> + space', 'Quote'],
		['**text**', 'Bold'], ['*text*', 'Italic'], ['`code`', 'Inline code'], ['---', 'Separator'],
		[mod + '+B / I / U', 'Bold / italic / underline'], [mod + '+K', 'Link'], [mod + '+Z / ' + mod + '+Shift+Z', 'Undo / redo'],
		[mod + '+S', 'Save'], [mod + '+Shift+F', 'Distraction-free writing'], ['Shift+Enter', 'Line break'],
	];
	dialog({ title: 'Keyboard Shortcuts', submit: 'Close', body: h('table', { class: 'gp-help' },
		...rows.map(([k, d]) => h('tr', {}, h('th', {}, h('kbd', { text: k })), h('td', { text: d })))) });
}

gopress.editor = Object.assign(gopress.editor || {}, {
	shortcodeViews: {},
	// registerInsert({id, label, icon, keywords, button, onClick(api)}):
	// adds an entry to the slash menu (and a button next to "Add Media"
	// if button is true). api.editor is the TipTap editor; use
	// gopress.editor.instance.insertShortcode(text) to insert content.
	registerInsert(item) { inserts.push(item); },
	// registerShortcodeView(name, fn(dom, text)): customizes the block of a
	// shortcode in the visual editor (e.g. a gallery preview).
	registerShortcodeView(name, fn) { gopress.editor.shortcodeViews[name] = fn; },
	mount,
	// For the round-trip test (web/editor/test/roundtrip.mjs).
	_test: { importHTML, exportHTML, canonical, debugParse },
});
gopress.media = Object.assign(gopress.media || {}, {
	pick: (opts) => pick({ url: '/admin/api/media', ...opts }),
});

function init() {
	document.querySelectorAll('[data-gp-editor]').forEach(mount);
}
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
