/**
 * Photo Slider & Lightbox – front-end behaviour (GoPress port).
 *
 * - Initialises one Swiper per inline gallery (arrows + clickable dots).
 * - Opens a full-viewport lightbox on image click, with a main Swiper, a
 *   synced thumbnail strip, an image counter, keyboard/ESC control, a focus
 *   trap and focus restoration.
 *
 * No build step: relies on the global `Swiper` from the bundled library.
 * The images come from data attributes of the slides (data-full,
 * data-thumb, data-caption); only http(s) and same-site URLs are used.
 */
( function() {
	'use strict';

	if ( typeof window.Swiper === 'undefined' ) {
		return;
	}

	const Swiper = window.Swiper;
	let i18n = {};
	const prefersReducedMotion =
		window.matchMedia && window.matchMedia( '(prefers-reduced-motion: reduce)' ).matches;
	const SPEED = prefersReducedMotion ? 0 : 400;

	/**
	 * Small helper to create an element with attributes.
	 *
	 * @param {string} tag   Tag name.
	 * @param {Object} attrs Attribute map.
	 * @return {HTMLElement} The created element.
	 */
	function make( tag, attrs ) {
		const node = document.createElement( tag );
		if ( attrs ) {
			Object.keys( attrs ).forEach( function( key ) {
				if ( 'class' === key ) {
					node.className = attrs[ key ];
				} else if ( 'text' === key ) {
					node.textContent = attrs[ key ];
				} else {
					node.setAttribute( key, attrs[ key ] );
				}
			} );
		}
		return node;
	}

	/* --------------------------------------------------------------------- *
	 * Lightbox (single shared instance, built lazily)                        *
	 * --------------------------------------------------------------------- */

	let lb = null; // Cached DOM references.
	let mainSwiper = null;
	let thumbsSwiper = null;
	let lastFocused = null;
	let savedOverflow = '';

	function buildLightbox() {
		const overlay = make( 'div', {
			class: 'fslb-lightbox',
			role: 'dialog',
			'aria-modal': 'true',
			'aria-label': i18n.dialogLabel || 'Image gallery',
		} );
		overlay.hidden = true;

		const backdrop = make( 'div', { class: 'fslb-lb-backdrop' } );

		const closeBtn = make( 'button', {
			class: 'fslb-lb-close',
			type: 'button',
			'aria-label': i18n.closeLabel || 'Close',
		} );
		closeBtn.textContent = '\u00d7';

		const counter = make( 'div', { class: 'fslb-lb-counter', 'aria-live': 'polite' } );
		const counterCur = make( 'span', { class: 'fslb-lb-cur', text: '1' } );
		const counterTotal = make( 'span', { class: 'fslb-lb-total', text: '1' } );
		counter.appendChild( counterCur );
		counter.appendChild( document.createTextNode( ' / ' ) );
		counter.appendChild( counterTotal );

		// Main swiper.
		const main = make( 'div', { class: 'fslb-lb-main swiper' } );
		const mainWrap = make( 'div', { class: 'swiper-wrapper' } );
		const prev = make( 'div', { class: 'swiper-button-prev' } );
		const next = make( 'div', { class: 'swiper-button-next' } );
		main.appendChild( mainWrap );
		main.appendChild( prev );
		main.appendChild( next );

		const caption = make( 'div', { class: 'fslb-lb-caption' } );

		// Thumbnail swiper.
		const thumbs = make( 'div', { class: 'fslb-lb-thumbs swiper' } );
		const thumbsWrap = make( 'div', { class: 'swiper-wrapper' } );
		thumbs.appendChild( thumbsWrap );

		const inner = make( 'div', { class: 'fslb-lb-inner' } );
		inner.appendChild( closeBtn );
		inner.appendChild( counter );
		inner.appendChild( main );
		inner.appendChild( caption );
		inner.appendChild( thumbs );

		overlay.appendChild( backdrop );
		overlay.appendChild( inner );
		document.body.appendChild( overlay );

		lb = {
			overlay,
			main,
			mainWrap,
			thumbs,
			thumbsWrap,
			caption,
			counterCur,
			counterTotal,
		};

		// Events.
		closeBtn.addEventListener( 'click', closeLightbox );
		backdrop.addEventListener( 'click', closeLightbox );
		overlay.addEventListener( 'keydown', onKeydown );

		return lb;
	}

	function onKeydown( event ) {
		if ( 'Escape' === event.key ) {
			event.preventDefault();
			closeLightbox();
			return;
		}
		if ( 'Tab' === event.key ) {
			trapFocus( event );
		}
	}

	function trapFocus( event ) {
		const focusable = lb.overlay.querySelectorAll(
			'button, [href], [tabindex]:not([tabindex="-1"])'
		);
		const visible = Array.prototype.filter.call( focusable, function( elm ) {
			return elm.offsetParent !== null;
		} );
		if ( ! visible.length ) {
			return;
		}
		const first = visible[ 0 ];
		const last = visible[ visible.length - 1 ];
		const active = lb.overlay.ownerDocument.activeElement;
		if ( event.shiftKey && active === first ) {
			event.preventDefault();
			last.focus();
		} else if ( ! event.shiftKey && active === last ) {
			event.preventDefault();
			first.focus();
		}
	}

	function updateCaption( items, index, showCaptions ) {
		const item = items[ index ];
		if ( showCaptions && item && item.caption ) {
			lb.caption.textContent = item.caption;
			lb.caption.style.display = '';
		} else {
			lb.caption.textContent = '';
			lb.caption.style.display = 'none';
		}
	}

	function destroySwipers() {
		if ( mainSwiper ) {
			mainSwiper.destroy( true, true );
			mainSwiper = null;
		}
		if ( thumbsSwiper ) {
			thumbsSwiper.destroy( true, true );
			thumbsSwiper = null;
		}
	}

	function openLightbox( items, index, opener, showCaptions ) {
		if ( ! items || ! items.length ) {
			return;
		}
		if ( ! lb ) {
			buildLightbox();
		}
		destroySwipers();

		// Populate slides.
		lb.mainWrap.textContent = '';
		lb.thumbsWrap.textContent = '';

		items.forEach( function( item ) {
			const slide = make( 'div', { class: 'swiper-slide' } );
			const zoom = make( 'div', { class: 'swiper-zoom-container' } );
			const img = make( 'img', { class: 'fslb-lb-image' } );
			img.src = item.full;
			img.alt = item.alt || '';
			zoom.appendChild( img );
			slide.appendChild( zoom );
			lb.mainWrap.appendChild( slide );

			const tSlide = make( 'div', { class: 'swiper-slide' } );
			const tImg = make( 'img', {} );
			tImg.src = item.thumb;
			tImg.alt = '';
			tSlide.appendChild( tImg );
			lb.thumbsWrap.appendChild( tSlide );
		} );

		lb.counterTotal.textContent = String( items.length );

		lastFocused = opener || lb.overlay.ownerDocument.activeElement;

		// Show overlay + lock scroll.
		lb.overlay.hidden = false;
		document.body.classList.add( 'fslb-lb-open' );
		savedOverflow = document.documentElement.style.overflow;
		document.documentElement.style.overflow = 'hidden';

		thumbsSwiper = new Swiper( lb.thumbs, {
			slidesPerView: 'auto',
			spaceBetween: 8,
			freeMode: true,
			watchSlidesProgress: true,
			speed: SPEED,
		} );

		mainSwiper = new Swiper( lb.main, {
			initialSlide: index,
			speed: SPEED,
			spaceBetween: 24,
			zoom: { maxRatio: 3 },
			keyboard: { enabled: true },
			a11y: { enabled: true },
			navigation: {
				nextEl: lb.main.querySelector( '.swiper-button-next' ),
				prevEl: lb.main.querySelector( '.swiper-button-prev' ),
			},
			thumbs: { swiper: thumbsSwiper },
			on: {
				slideChange() {
					lb.counterCur.textContent = String( this.realIndex + 1 );
					updateCaption( items, this.realIndex, showCaptions );
				},
			},
		} );

		lb.counterCur.textContent = String( index + 1 );
		updateCaption( items, index, showCaptions );

		// Move focus into the dialog.
		window.requestAnimationFrame( function() {
			const closeBtn = lb.overlay.querySelector( '.fslb-lb-close' );
			if ( closeBtn ) {
				closeBtn.focus();
			}
		} );
	}

	function closeLightbox() {
		if ( ! lb || lb.overlay.hidden ) {
			return;
		}
		lb.overlay.hidden = true;
		document.body.classList.remove( 'fslb-lb-open' );
		document.documentElement.style.overflow = savedOverflow;
		destroySwipers();
		if ( lastFocused && typeof lastFocused.focus === 'function' ) {
			lastFocused.focus();
		}
		lastFocused = null;
	}

	/* --------------------------------------------------------------------- *
	 * Inline galleries                                                       *
	 * --------------------------------------------------------------------- */

	/**
	 * Accepts an image URL from the page: http(s) or a path on this site.
	 *
	 * @param {string} value URL from a data attribute.
	 * @return {string} The URL, or '' if it is not acceptable.
	 */
	function safeURL( value ) {
		if ( ! value ) {
			return '';
		}
		try {
			const url = new URL( value, window.location.href );
			return 'https:' === url.protocol || 'http:' === url.protocol ? url.href : '';
		} catch ( e ) {
			return '';
		}
	}

	function readItems( root ) {
		const items = [];
		root.querySelectorAll( '.fslb-open' ).forEach( function( btn ) {
			const img = btn.querySelector( 'img' );
			const full = safeURL( btn.getAttribute( 'data-full' ) ) || safeURL( img && img.getAttribute( 'src' ) );
			items.push( {
				full,
				thumb: safeURL( btn.getAttribute( 'data-thumb' ) ) || full,
				alt: img ? img.getAttribute( 'alt' ) || '' : '',
				caption: btn.getAttribute( 'data-caption' ) || '',
			} );
		} );
		return items;
	}

	function initGallery( root ) {
		const sliderEl = root.querySelector( '.fslb-slider' );
		if ( ! sliderEl || sliderEl.dataset.fslbReady ) {
			return;
		}
		sliderEl.dataset.fslbReady = '1';

		const items = readItems( root );
		const showCaptions = '1' === root.getAttribute( 'data-captions' );
		i18n = {
			dialogLabel: root.getAttribute( 'data-label-dialog' ),
			closeLabel: root.getAttribute( 'data-label-close' ),
		};

		// eslint-disable-next-line no-new
		new Swiper( sliderEl, {
			slidesPerView: 1,
			spaceBetween: 0,
			speed: SPEED,
			watchOverflow: true,
			keyboard: { enabled: true, onlyInViewport: true },
			a11y: { enabled: true },
			navigation: {
				nextEl: sliderEl.querySelector( '.fslb-next' ),
				prevEl: sliderEl.querySelector( '.fslb-prev' ),
			},
			pagination: {
				el: sliderEl.querySelector( '.fslb-pagination' ),
				clickable: true,
			},
		} );

		root.querySelectorAll( '.fslb-open' ).forEach( function( btn ) {
			btn.addEventListener( 'click', function() {
				const idx = parseInt( btn.getAttribute( 'data-fslb-index' ), 10 ) || 0;
				openLightbox( items, idx, btn, showCaptions );
			} );
		} );
	}

	function initAll() {
		document.querySelectorAll( '.fslb-gallery' ).forEach( initGallery );
	}

	if ( 'loading' === document.readyState ) {
		document.addEventListener( 'DOMContentLoaded', initAll );
	} else {
		initAll();
	}
}() );
