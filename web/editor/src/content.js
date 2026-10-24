// Loading HTML into the editor and writing it back.
//
// Loading never loses content: every top-level piece of the stored HTML is
// parsed into the editor schema and serialized again; if the result is not
// equivalent to the original, the piece is kept as a raw HTML block instead.
import { DOMParser as PMDOMParser, DOMSerializer, Fragment } from '@tiptap/pm/model';

const SHORTCODE_LINE = /^\[(\[?)([\w-]+)(?![\w-])[^\]]*?\](?:[\s\S]*?\[\/\2\])?\]?$/;
const BLOCK_TAGS = new Set(['ADDRESS', 'ARTICLE', 'ASIDE', 'BLOCKQUOTE', 'DD', 'DIV', 'DL', 'DT', 'FIELDSET', 'FIGCAPTION',
	'FIGURE', 'FOOTER', 'FORM', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'HEADER', 'HR', 'LI', 'MAIN', 'NAV', 'OL', 'P', 'PRE',
	'SECTION', 'TABLE', 'TBODY', 'THEAD', 'TFOOT', 'TR', 'TD', 'TH', 'UL', 'CAPTION', 'DETAILS', 'SUMMARY']);

// Inert parsing: <template> content does not load images or run scripts.
function fragmentOf(html) {
	const t = document.createElement('template');
	t.innerHTML = html;
	return t.content;
}

// Splits text between blocks into shortcode lines; null if it is not only
// shortcodes.
function shortcodeLines(text) {
	const lines = text.split(/\n+/).map((l) => l.trim()).filter(Boolean);
	if (!lines.length) return [];
	// A multi-line enclosing shortcode ([caption]…[/caption]) stays together.
	const joined = text.trim();
	if (SHORTCODE_LINE.test(joined) && !/\n\s*\n/.test(joined)) {
		const name = joined.match(/^\[\[?([\w-]+)/)[1];
		const closes = joined.includes('[/' + name + ']');
		if (closes || lines.length === 1) return [joined];
	}
	return lines.every((l) => SHORTCODE_LINE.test(l)) ? lines : null;
}

export function importHTML(schema, html) {
	const parser = PMDOMParser.fromSchema(schema);
	const nodes = [];
	const raw = (h) => nodes.push(schema.nodes.rawHTML.create({ html: h }));
	let pending = []; // consecutive inline nodes/text at the top level

	const flushPending = () => {
		if (!pending.length) return;
		const html = pending.map(outer).join('');
		pending = [];
		const text = html.trim();
		if (!text) return;
		const sc = !/[<>]/.test(text.replace(/\[caption[\s\S]*?\[\/caption\]/g, '')) ? shortcodeLines(text) : null;
		if (sc) {
			sc.forEach((t) => nodes.push(schema.nodes.shortcode.create({ text: t })));
		} else {
			// Text or inline markup outside of any block renders as is
			// (no wpautop for stored HTML), so keep it as is.
			raw(text);
		}
	};

	for (const n of Array.from(fragmentOf(html).childNodes)) {
		if (n.nodeType === Node.COMMENT_NODE) {
			flushPending();
			const m = n.data.match(/^more(?:\s(.*))?$/);
			if (m) nodes.push(schema.nodes.more.create({ text: (m[1] || '').trim() }));
			else raw('<!--' + n.data + '-->');
			continue;
		}
		if (n.nodeType === Node.ELEMENT_NODE && BLOCK_TAGS.has(n.nodeName)) {
			flushPending();
			const parsed = tryParse(schema, parser, n);
			if (parsed) nodes.push(...parsed);
			else raw(n.outerHTML);
			continue;
		}
		pending.push(n);
	}
	flushPending();
	if (!nodes.length) nodes.push(schema.nodes.paragraph.create());
	return schema.topNodeType.create(null, nodes);
}

function outer(n) {
	if (n.nodeType === Node.TEXT_NODE) return escapeText(n.data);
	if (n.nodeType === Node.COMMENT_NODE) return '<!--' + n.data + '-->';
	return n.outerHTML;
}

function escapeText(s) {
	return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/ /g, '&nbsp;');
}

// tryParse returns the editor nodes for el if they serialize back to
// equivalent HTML, else null.
function tryParse(schema, parser, el) {
	const wrap = document.createElement('div');
	wrap.appendChild(el.cloneNode(true));
	let doc;
	try {
		doc = parser.parse(wrap);
	} catch (e) {
		return null;
	}
	const nodes = [];
	doc.content.forEach((c) => nodes.push(c));
	if (nodes.some((c) => c.type.name === 'rawHTML' || c.type.name === 'shortcode' || c.type.name === 'more')) return null;
	const back = exportFragment(schema, Fragment.fromArray(nodes));
	return canonical(el.outerHTML) === canonical(back) ? nodes : null;
}

export function exportHTML(schema, doc) {
	return exportFragment(schema, doc.content);
}

function exportFragment(schema, fragment) {
	const div = document.createElement('div');
	div.appendChild(DOMSerializer.fromSchema(schema).serializeFragment(fragment, { document }));
	postProcess(div);
	const parts = [];
	for (const n of Array.from(div.childNodes)) {
		if (n.nodeType === Node.ELEMENT_NODE && n.nodeName === 'GP-RAW-OUT') {
			parts.push(n.getAttribute('data-html'));
		} else if (n.nodeType === Node.ELEMENT_NODE) {
			parts.push(serializeElement(n));
		} else if (n.nodeType === Node.COMMENT_NODE) {
			parts.push('<!--' + n.data + '-->');
		} else if (n.nodeType === Node.TEXT_NODE && n.data.trim()) {
			parts.push(n.data);
		}
	}
	return parts.join('\n\n');
}

// postProcess turns the editor's placeholder elements into the stored
// markup and unwraps the <p> of plain list items and quotes.
function postProcess(root) {
	root.querySelectorAll('[data-gp-style]').forEach((el) => {
		el.setAttribute('style', el.getAttribute('data-gp-style'));
		el.removeAttribute('data-gp-style');
	});
	// Items and quotes that had bare text: the editor wrapped that text in
	// <p>; unwrap those again.
	root.querySelectorAll('[data-gp-plain]').forEach((el) => {
		el.removeAttribute('data-gp-plain');
		for (const k of Array.from(el.childNodes)) {
			if (k.nodeName === 'P' && !k.attributes.length) k.replaceWith(...Array.from(k.childNodes));
		}
	});
	root.querySelectorAll('gp-shortcode').forEach((el) => {
		el.replaceWith(document.createTextNode(el.getAttribute('data-text')));
	});
	root.querySelectorAll('gp-more').forEach((el) => {
		const t = el.getAttribute('data-text');
		el.replaceWith(document.createComment(t ? 'more ' + t : 'more'));
	});
	root.querySelectorAll('gp-raw').forEach((el) => {
		// Top-level raw blocks are emitted verbatim by exportFragment;
		// nested ones (inside a quote) are inlined as markup.
		if (el.parentNode === root) {
			const out = document.createElement('gp-raw-out');
			out.setAttribute('data-html', el.getAttribute('data-html'));
			el.replaceWith(out);
		} else {
			el.replaceWith(fragmentOf(el.getAttribute('data-html')));
		}
	});
}

// serializeElement writes an element as HTML, with list items and table
// rows on their own lines for a readable source view.
function serializeElement(el) {
	el.querySelectorAll('ul, ol, table, thead, tbody, tfoot, tr').forEach(prettyChildren);
	if (/^(UL|OL|TABLE|THEAD|TBODY|TFOOT|TR)$/.test(el.nodeName)) prettyChildren(el);
	return el.outerHTML;
}

function prettyChildren(el) {
	for (const c of Array.from(el.children)) {
		el.insertBefore(document.createTextNode('\n'), c);
	}
	if (el.children.length) el.appendChild(document.createTextNode('\n'));
}

// ---- Equivalence of HTML (for the load check) ----

const ALIAS = { B: 'STRONG', I: 'EM', DEL: 'S', STRIKE: 'S' };

const MARK_TAGS = new Set(['STRONG', 'B', 'EM', 'I', 'S', 'DEL', 'STRIKE', 'U', 'CODE', 'A', 'SPAN', 'MARK', 'SUP', 'SUB',
	'SMALL', 'ABBR', 'KBD', 'CITE', 'Q', 'INS', 'VAR', 'SAMP', 'DFN', 'TIME', 'BDI', 'FONT']);

// canonical returns a normalized form of HTML for the load check: inline
// formatting is compared as runs of text with their set of formats (so
// <strong><a>x</a></strong> equals <a><strong>x</strong></a>), attributes
// are sorted, and whitespace is collapsed except in <pre>.
export function canonical(html) {
	const tokens = [];
	walk(fragmentOf(html), tokens, false, []);
	// Merge adjacent text runs with the same formats.
	const merged = [];
	for (const t of tokens) {
		// Formatting on whitespace alone is invisible (and dropped by the
		// editor).
		if (t.text !== undefined && !t.pre && !t.text.replace(/&nbsp;/g, 'x').trim()) t.marks = '';
		const last = merged[merged.length - 1];
		if (t.text !== undefined && last && last.text !== undefined && last.marks === t.marks) last.text += t.text;
		else merged.push({ ...t });
	}
	return merged.map((t) => {
		if (t.text === undefined) return t.tag;
		const text = t.pre ? t.text : t.text.replace(/\s+/g, ' ');
		return t.marks ? '{' + t.marks + '}' + text : text;
	}).join('').replace(/\s+/g, ' ').replace(/ ?(<\/?([a-z0-9-]+)(?: [^>]*)?>) ?/g, (m, tag, name) => {
		// Whitespace around block tags is insignificant.
		name = name.toUpperCase();
		return BLOCK_TAGS.has(name) || name === 'BR' ? tag : m;
	}).replace(/\{[^}]*\} (?=<)/g, ' ').trim();
}

function attrString(n) {
	return Array.from(n.attributes).map((a) => {
		let v = a.value.trim();
		if (a.name === 'class') v = v.split(/\s+/).filter(Boolean).join(' ');
		if (a.name === 'style') v = v.split(';').map((d) => d.trim().replace(/\s*:\s*/, ': ')).filter(Boolean).join('; ');
		return a.name + '="' + v + '"';
	}).filter((a) => a !== 'class=""' && a !== 'style=""').sort().join(' ');
}

function walk(node, out, inPre, marks) {
	const markKey = marks.slice().sort().join('|');
	for (const n of Array.from(node.childNodes)) {
		if (n.nodeType === Node.TEXT_NODE) {
			const t = n.data.replace(/\u00a0/g, '&nbsp;');
			out.push({ text: inPre ? t : escapeText(t), marks: markKey, pre: inPre });
		} else if (n.nodeType === Node.COMMENT_NODE) {
			out.push({ tag: '<!--' + n.data + '-->' });
		} else if (n.nodeType === Node.ELEMENT_NODE) {
			const name = (ALIAS[n.nodeName] || n.nodeName).toLowerCase();
			const attrs = attrString(n);
			if (MARK_TAGS.has(n.nodeName) && !inPre) {
				walk(n, out, inPre, marks.concat(name + (attrs ? ' ' + attrs : '')));
				continue;
			}
			const prefix = BLOCK_TAGS.has(n.nodeName) || !markKey ? '' : '{' + markKey + '}';
			out.push({ tag: prefix + '<' + name + (attrs ? ' ' + attrs : '') + '>' });
			walk(n, out, inPre || n.nodeName === 'PRE', BLOCK_TAGS.has(n.nodeName) ? [] : marks);
			out.push({ tag: '</' + name + '>' });
		}
	}
}


// debugParse shows why a block is kept as HTML (round-trip test).
export function debugParse(schema, html) {
	const el = fragmentOf(html).firstElementChild;
	const wrap = document.createElement('div');
	wrap.appendChild(el.cloneNode(true));
	const doc = PMDOMParser.fromSchema(schema).parse(wrap);
	const back = exportFragment(schema, doc.content);
	return { orig: canonical(html), back: canonical(back), html: back };
}
