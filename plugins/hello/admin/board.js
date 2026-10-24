// The board talks to GoPress only through window.gopress (the bridge):
// call() reaches the plugin's admin API, media.pick() opens the picker.
(function () {
	var g = window.gopress;
	function show(s) {
		document.getElementById('user').textContent = s.user;
		document.getElementById('hits').textContent = s.hits;
	}
	function load() { return g.call('stats').then(show); }
	document.getElementById('reset').onclick = function () {
		g.call('reset').then(load).then(function () { g.notice('Counter reset.', 'success'); });
	};
	document.getElementById('pick').onclick = function () {
		g.media.pick({ multiple: true, imagesOnly: true }).then(function (items) {
			var list = document.getElementById('pinned');
			list.textContent = '';
			items.forEach(function (it) {
				var li = document.createElement('li');
				var img = document.createElement('img');
				img.src = (it.sizes && it.sizes.thumbnail && it.sizes.thumbnail.url) || it.url;
				img.alt = it.alt || it.title || '';
				li.appendChild(img);
				list.appendChild(li);
			});
			return g.call('pin', items.map(function (it) { return it.id; }));
		});
	};
	load();
})();
