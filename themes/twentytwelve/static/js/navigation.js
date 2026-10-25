/**
 * Handles toggling the navigation menu for small screens and
 * accessibility for submenu items.
 */
( function() {
	var nav = document.getElementById( 'site-navigation' ), button, menu;
	if ( ! nav ) {
		return;
	}

	button = nav.getElementsByTagName( 'button' )[0];
	menu   = nav.getElementsByTagName( 'ul' )[0];
	if ( ! button ) {
		return;
	}

	// Hide button if menu is missing or empty.
	if ( ! menu || ! menu.childNodes.length ) {
		button.style.display = 'none';
		return;
	}

	// Assign an ID for the default page list if no menu is set as Primary.
	if ( ! menu.id ) {
		menu.id = 'twentytwelve-page-list-menu';
	}

	button.setAttribute( 'aria-controls', menu.id );
	button.setAttribute( 'aria-expanded', 'false' );

	button.onclick = function() {
		if ( -1 === menu.className.indexOf( 'nav-menu' ) ) {
			menu.className = 'nav-menu';
		}

		if ( -1 !== button.className.indexOf( 'toggled-on' ) ) {
			button.setAttribute( 'aria-expanded', 'false' );
			button.className = button.className.replace( ' toggled-on', '' );
			menu.className = menu.className.replace( ' toggled-on', '' );
		} else {
			button.setAttribute( 'aria-expanded', 'true' );
			button.className += ' toggled-on';
			menu.className += ' toggled-on';
		}
	};
} )();

// Better focus for hidden submenu items for accessibility. (GoPress: the
// original uses jQuery, which GoPress does not load; same behavior in
// plain JavaScript.)
( function() {
	var nav = document.querySelector( '.main-navigation' );
	if ( ! nav ) {
		return;
	}
	var parentLinks = '.menu-item-has-children > a, .page_item_has_children > a';

	function toggleFocus() {
		for ( var el = this.parentElement; el; el = el.parentElement ) {
			if ( el.classList.contains( 'menu-item' ) || el.classList.contains( 'page_item' ) ) {
				el.classList.toggle( 'focus' );
			}
		}
	}
	var links = nav.getElementsByTagName( 'a' );
	for ( var i = 0; i < links.length; i++ ) {
		links[ i ].addEventListener( 'focus', toggleFocus );
		links[ i ].addEventListener( 'blur', toggleFocus );
	}

	if ( 'ontouchstart' in window ) {
		document.body.addEventListener( 'touchstart', function( e ) {
			var link = e.target.closest && e.target.closest( parentLinks );
			if ( ! link ) {
				return;
			}
			var el = link.parentElement;
			if ( ! el.classList.contains( 'focus' ) ) {
				e.preventDefault();
				el.classList.toggle( 'focus' );
				for ( var sib = el.parentElement.firstElementChild; sib; sib = sib.nextElementSibling ) {
					if ( sib !== el ) {
						sib.classList.remove( 'focus' );
					}
				}
			}
		}, { passive: false } );
	}
} )();
