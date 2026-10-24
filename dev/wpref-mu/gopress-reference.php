<?php
/**
 * Reference WordPress only: show the classic editor screens, which the
 * GoPress admin mirrors, instead of the block editor.
 */
add_filter( 'use_block_editor_for_post', '__return_false' );
add_filter( 'use_widgets_block_editor', '__return_false' );
