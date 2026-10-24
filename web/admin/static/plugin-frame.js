// Runs inside a plugin's sandboxed admin page: window.gopress talks to
// the GoPress admin around the frame (see plugin-bridge.js).
(function () {
	'use strict';
	var next = 1;
	var pending = {};
	window.addEventListener('message', function (ev) {
		if (ev.source !== window.parent) return;
		var m = ev.data;
		if (!m || m.gopress !== 1 || !pending[m.id]) return;
		var p = pending[m.id];
		delete pending[m.id];
		if (m.error) p.reject(new Error(m.error)); else p.resolve(m.result);
	});
	function send(msg, wait) {
		msg.gopress = 1;
		msg.id = next++;
		var promise = wait === false ? Promise.resolve() : new Promise(function (resolve, reject) { pending[msg.id] = { resolve: resolve, reject: reject }; });
		window.parent.postMessage(msg, '*');
		return promise;
	}
	window.gopress = {
		// call(method, params) runs the plugin's admin_api handler.
		call: function (method, params) { return send({ type: 'call', method: method, params: params }); },
		media: {
			// pick({multiple, imagesOnly}) resolves with the chosen items.
			pick: function (opts) { opts = opts || {}; return send({ type: 'pick', multiple: !!opts.multiple, imagesOnly: !!opts.imagesOnly }); },
		},
		notice: function (text, kind) { return send({ type: 'notice', text: String(text), kind: kind }); },
		resize: function (height) { return send({ type: 'resize', height: height || document.documentElement.scrollHeight }); },
		navigate: function (page) { return send({ type: 'navigate', page: page }, false); },
		// insertShortcode(tag, attrs, content): editor dialogs only.
		insertShortcode: function (tag, attrs, content) { return send({ type: 'insert', tag: tag, attrs: attrs, content: content }, false); },
		close: function () { return send({ type: 'close' }, false); },
	};
	if (window.ResizeObserver) {
		new ResizeObserver(function () { window.gopress.resize(); }).observe(document.documentElement);
	}
})();
