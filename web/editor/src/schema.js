// Editor schema: StarterKit plus nodes that keep WordPress content intact
// (raw HTML blocks, shortcodes, the more tag, images with all attributes,
// <pre> as written) and a catch-all attribute map on block nodes.
import { Extension, Mark, Node, mergeAttributes } from '@tiptap/core';
import StarterKit from '@tiptap/starter-kit';
import Link from '@tiptap/extension-link';
import { Table, TableRow, TableCell, TableHeader } from '@tiptap/extension-table';
import { Placeholder } from '@tiptap/extensions';

// Attributes a node type handles itself (not part of htmlAttrs).
const OWN_ATTRS = {
	OL: ['start', 'type'],
	TD: ['colspan', 'rowspan', 'colwidth'],
	TH: ['colspan', 'rowspan', 'colwidth'],
};

// styled keeps the original style text next to the style the browser
// normalizes (#f00 -> rgb(255, 0, 0)); the export writes the original back.
export function styled(attrs) {
	if (!attrs || attrs.style == null) return attrs || {};
	return { ...attrs, 'data-gp-style': attrs.style };
}

export function collectAttrs(el) {
	const own = OWN_ATTRS[el.nodeName] || [];
	const out = {};
	let any = false;
	for (const a of el.attributes) {
		if (own.includes(a.name) || a.name === 'data-gp-plain' || a.name === 'data-gp-style') continue;
		out[a.name] = a.value;
		any = true;
	}
	return any ? out : null;
}

const BLOCK_TYPES = ['paragraph', 'heading', 'blockquote', 'bulletList', 'orderedList', 'listItem',
	'table', 'tableRow', 'tableCell', 'tableHeader', 'horizontalRule', 'pre'];

// Keeps every attribute of block elements (class, id, style, data-*) so
// that WordPress markup survives editing.
const HTMLAttrs = Extension.create({
	name: 'gpHtmlAttrs',
	addGlobalAttributes() {
		return [{
			types: BLOCK_TYPES,
			attributes: {
				htmlAttrs: {
					default: null,
					keepOnSplit: false,
					parseHTML: (el) => collectAttrs(el),
					renderHTML: (attrs) => styled(attrs.htmlAttrs),
				},
			},
		}, {
			// <li>text</li> and <blockquote>text</blockquote> without <p>:
			// remembered so the export writes them back the same way.
			types: ['listItem', 'blockquote'],
			attributes: {
				plain: {
					default: false,
					keepOnSplit: true,
					parseHTML: (el) => !Array.from(el.children).some((c) => c.nodeName === 'P') &&
						!Array.from(el.childNodes).every((c) => c.nodeType === 3 && !c.textContent.trim()),
					renderHTML: (attrs) => (attrs.plain ? { 'data-gp-plain': '1' } : {}),
				},
			},
		}];
	},
});

// A block of HTML the editor cannot represent; kept verbatim.
export const RawHTML = Node.create({
	name: 'rawHTML',
	group: 'block',
	atom: true,
	selectable: true,
	draggable: true,
	addAttributes() {
		return { html: { default: '' } };
	},
	parseHTML() {
		return [{ tag: 'gp-raw', getAttrs: (el) => ({ html: el.getAttribute('data-html') || '' }) }];
	},
	renderHTML({ node }) {
		return ['gp-raw', { 'data-html': node.attrs.html }];
	},
});

// A shortcode on its own line, e.g. [gallery ids="1,2"].
export const Shortcode = Node.create({
	name: 'shortcode',
	group: 'block',
	atom: true,
	selectable: true,
	draggable: true,
	addAttributes() {
		return { text: { default: '' } };
	},
	parseHTML() {
		return [{ tag: 'gp-shortcode', getAttrs: (el) => ({ text: el.getAttribute('data-text') || '' }) }];
	},
	renderHTML({ node }) {
		return ['gp-shortcode', { 'data-text': node.attrs.text }];
	},
});

// <!--more--> (optionally with custom link text).
export const More = Node.create({
	name: 'more',
	group: 'block',
	atom: true,
	selectable: true,
	draggable: true,
	addAttributes() {
		return { text: { default: '' } };
	},
	parseHTML() {
		return [{ tag: 'gp-more', getAttrs: (el) => ({ text: el.getAttribute('data-text') || '' }) }];
	},
	renderHTML({ node }) {
		return ['gp-more', { 'data-text': node.attrs.text }];
	},
	addCommands() {
		return {
			insertMore: () => ({ commands }) => commands.insertContent({ type: 'more' }),
		};
	},
});

// Inline image keeping all its attributes (class with alignment, size and
// wp-image-N, width, height, srcset, …).
export const Image = Node.create({
	name: 'image',
	group: 'inline',
	inline: true,
	atom: true,
	draggable: true,
	addAttributes() {
		return { attrs: { default: {} } };
	},
	parseHTML() {
		return [{ tag: 'img[src]', getAttrs: (el) => ({ attrs: collectAttrs(el) || {} }) }];
	},
	renderHTML({ node }) {
		return ['img', styled(node.attrs.attrs)];
	},
});

// <pre> as written (with or without an inner <code>).
export const Pre = Node.create({
	name: 'pre',
	group: 'block',
	content: 'text*',
	marks: '',
	code: true,
	defining: true,
	addAttributes() {
		return {
			codeChild: {
				default: null,
				parseHTML: (el) => {
					const kids = Array.from(el.childNodes).filter((n) => !(n.nodeType === 3 && n.textContent === ''));
					if (kids.length === 1 && kids[0].nodeName === 'CODE') return collectAttrs(kids[0]) || {};
					return null;
				},
				renderHTML: () => ({}),
			},
		};
	},
	parseHTML() {
		return [{
			tag: 'pre',
			preserveWhitespace: 'full',
			contentElement: (el) => {
				const kids = Array.from(el.childNodes);
				return kids.length === 1 && kids[0].nodeName === 'CODE' ? kids[0] : el;
			},
		}];
	},
	renderHTML({ node, HTMLAttributes }) {
		if (node.attrs.codeChild) return ['pre', HTMLAttributes, ['code', node.attrs.codeChild, 0]];
		return ['pre', HTMLAttributes, 0];
	},
	addCommands() {
		return {
			togglePre: () => ({ commands }) => commands.toggleNode('pre', 'paragraph'),
		};
	},
	addKeyboardShortcuts() {
		return {
			'Mod-Alt-c': () => this.editor.commands.togglePre(),
			// Tab inserts a tab inside <pre>.
			Tab: () => (this.editor.isActive('pre') ? this.editor.commands.insertContent('\t') : false),
		};
	},
	addInputRules() {
		return [];
	},
});

// Inline elements without an editor button (colored text spans, <mark>,
// <sup>, …) are kept with all their attributes.
function inlineMark(tag) {
	return Mark.create({
		name: 'html_' + tag,
		addAttributes() {
			return { attrs: { default: {} } };
		},
		parseHTML() {
			return [{ tag, getAttrs: (el) => ({ attrs: collectAttrs(el) || {} }) }];
		},
		renderHTML({ mark }) {
			return [tag, styled(mark.attrs.attrs), 0];
		},
	});
}
export const INLINE_MARK_TAGS = ['span', 'mark', 'sup', 'sub', 'small', 'abbr', 'kbd', 'cite', 'q', 'ins', 'var', 'samp', 'dfn', 'time', 'bdi', 'font'];

// Tables without the colgroup/inline width TipTap adds by default.
const PlainTable = Table.extend({
	renderHTML({ HTMLAttributes }) {
		return ['table', HTMLAttributes, ['tbody', 0]];
	},
});

// Links keep their attributes; no target/rel are added by default.
const linkAttrs = {
	target: { default: null },
	rel: { default: null },
	class: { default: null },
	title: { default: null },
	id: { default: null },
	hreflang: { default: null },
	download: { default: null },
	lang: { default: null },
	'data-type': { default: null },
	'data-id': { default: null },
	style: { default: null, renderHTML: (a) => styled(a.style == null ? {} : { style: a.style }) },
};

export function extensions({ placeholder } = {}) {
	return [
		StarterKit.configure({ codeBlock: false, trailingNode: false, link: false }),
		Link.extend({
			// Links nest inside bold/italic (<strong><a>…</a></strong>), as in
			// classic WordPress content.
			priority: 50,
			addAttributes() {
				return { ...this.parent(), ...linkAttrs };
			},
		}).configure({
			openOnClick: false,
			autolink: true,
			linkOnPaste: true,
			defaultProtocol: 'https',
			HTMLAttributes: { target: null, rel: null, class: null },
		}),
		HTMLAttrs,
		RawHTML,
		Shortcode,
		More,
		Image,
		Pre,
		PlainTable.configure({ resizable: false }),
		TableRow,
		TableHeader,
		TableCell,
		...INLINE_MARK_TAGS.map(inlineMark),
		Placeholder.configure({ placeholder: placeholder || 'Start writing, or type / for blocks…' }),
	];
}

export { mergeAttributes };
