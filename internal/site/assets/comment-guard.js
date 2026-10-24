/*
 * GoPress comment form helper:
 * - pre-fills name, email and website from the commenter cookies (pages are
 *   cached, so the server does not personalize the form);
 * - fetches a fresh anti-spam token when the visitor starts using the form,
 *   so the minimum fill time also works on cached pages. Without JavaScript
 *   the token embedded in the page is used.
 */
( function () {
	var form = document.getElementById( 'commentform' );
	if ( ! form || ! window.fetch ) {
		return;
	}
	var fields = { comment_author_email_: 'email', comment_author_url_: 'url', comment_author_: 'author' };
	document.cookie.split( ';' ).forEach( function ( part ) {
		var eq = part.indexOf( '=' );
		var name = part.slice( 0, eq ).trim();
		var value;
		try {
			value = decodeURIComponent( part.slice( eq + 1 ).replace( /\+/g, ' ' ) );
		} catch ( e ) {
			return;
		}
		for ( var prefix in fields ) {
			if ( name.indexOf( prefix ) === 0 ) {
				var input = form.querySelector( '#' + fields[ prefix ] );
				if ( input && ! input.value ) {
					input.value = value;
				}
				break;
			}
		}
	} );

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
