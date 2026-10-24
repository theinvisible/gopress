/*
 * GoPress comment form helper: fetches a fresh anti-spam token when the
 * visitor starts using the form, so the minimum fill time also works on
 * cached pages. Without JavaScript the token embedded in the page is used.
 */
( function () {
	var form = document.getElementById( 'commentform' );
	if ( ! form || ! window.fetch ) {
		return;
	}
	var done = false;
	form.addEventListener( 'focusin', function () {
		if ( done ) {
			return;
		}
		done = true;
		var post = form.querySelector( 'input[name="comment_post_ID"]' );
		var token = form.querySelector( 'input[name="gp_token"]' );
		if ( ! post || ! token ) {
			return;
		}
		fetch( form.getAttribute( 'action' ) + '?gp_token=1&post=' + encodeURIComponent( post.value ), {
			credentials: 'same-origin',
			cache: 'no-store'
		} ).then( function ( r ) {
			return r.ok ? r.json() : null;
		} ).then( function ( data ) {
			if ( data && typeof data.token === 'string' ) {
				token.value = data.token;
			}
		} ).catch( function () {} );
	} );
} )();
