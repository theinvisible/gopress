// Waves once when a greeting scrolls into view.
document.querySelectorAll('.hello-greeting').forEach(function (el) {
	if (!window.IntersectionObserver) return el.classList.add('hello-wave');
	new IntersectionObserver(function (entries, obs) {
		if (entries[0].isIntersecting) { el.classList.add('hello-wave'); obs.disconnect(); }
	}).observe(el);
});
