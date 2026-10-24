/* GoPress uploads: gopress.upload(file, {post, onProgress}) sends one file
   to /admin/api/media and resolves with the media item. XMLHttpRequest is
   used for the upload progress. gopress.uploadQueue runs several uploads
   with limited parallelism. */
(function () {
	'use strict';
	var gp = window.gopress = window.gopress || {};
	var csrf = document.body.getAttribute('data-csrf');

	gp.upload = function (file, opts) {
		opts = opts || {};
		return new Promise(function (resolve, reject) {
			var fd = new FormData();
			if (opts.post) { fd.append('post', String(opts.post)); }
			fd.append('file', file, file.name || 'pasted-image.png');
			var xhr = new XMLHttpRequest();
			xhr.open('POST', '/admin/api/media');
			xhr.setRequestHeader('X-CSRF-Token', csrf);
			xhr.setRequestHeader('Accept', 'application/json');
			xhr.responseType = 'json';
			xhr.upload.onprogress = function (e) {
				if (e.lengthComputable && opts.onProgress) { opts.onProgress(e.loaded / e.total); }
			};
			xhr.onload = function () {
				var body = xhr.response || {};
				if (xhr.status === 201) { resolve(body); } else { reject(new Error(body.error || 'The upload failed (' + xhr.status + ').')); }
			};
			xhr.onerror = function () { reject(new Error('The upload failed (network error).')); };
			xhr.send(fd);
		});
	};

	// uploadQueue uploads files, at most `parallel` at a time. hooks:
	// start(file) → handle passed to progress(handle, fraction),
	// done(handle, item) and failed(handle, error).
	gp.uploadQueue = function (files, opts, hooks) {
		var list = Array.prototype.slice.call(files);
		var parallel = 3, running = 0, i = 0;
		return new Promise(function (resolveAll) {
			var results = [];
			var next = function () {
				if (i >= list.length && running === 0) { resolveAll(results); return; }
				while (running < parallel && i < list.length) {
					(function (file) {
						running++;
						var h = hooks.start ? hooks.start(file) : null;
						gp.upload(file, { post: opts.post, onProgress: function (f) { if (hooks.progress) { hooks.progress(h, f); } } })
							.then(function (item) { results.push(item); if (hooks.done) { hooks.done(h, item); } })
							.catch(function (err) { if (hooks.failed) { hooks.failed(h, err); } })
							.then(function () { running--; next(); });
					})(list[i++]);
				}
			};
			next();
		});
	};
})();
