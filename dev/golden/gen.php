<?php
// Generates golden outputs of WordPress formatting functions for the Go
// ports in internal/render. Run inside the reference WordPress container:
//   make golden
// Inputs: all published posts/pages plus the edge cases below.
$_SERVER['HTTP_HOST'] = '127.0.0.1:8081';
require '/var/www/html/wp-load.php';

$cases = array();
foreach ( get_posts( array( 'post_type' => array( 'post', 'page' ), 'post_status' => 'publish', 'numberposts' => -1, 'orderby' => 'ID', 'order' => 'ASC' ) ) as $p ) {
	$cases[ 'post-' . $p->ID ] = $p->post_content;
	$cases[ 'title-' . $p->ID ] = $p->post_title;
}
$edge = array(
	'empty'           => '',
	'single-line'     => 'Hello world',
	'two-paras'       => "First\n\nSecond",
	'line-break'      => "Line one\nLine two",
	'br-br'           => 'a<br><br>b',
	'pre'             => "<pre>keep\n\nthis</pre>\nafter",
	'list'            => "<ul>\n<li>a</li>\n<li>b</li>\n</ul>\ntext",
	'blockquote'      => "<blockquote>quoted\n\nmore</blockquote>",
	'shortcode-alone' => "text\n\n[gallery ids=\"1,2\"]\n\nmore",
	'shortcode-encl'  => "[caption id=\"x\"]<img src=\"a.png\" /> cap[/caption]",
	'script'          => "<script>\nvar a = 1;\n\nvar b;\n</script>",
	'spaces-nl'       => "a  \n  b\n\n\nc",
	'quotes'          => "\"Hello\" and 'single' it's -- dash --- em... 10x20 (c) 'tis",
	'quotes-html'     => '<a href="x">"link"</a> "after" <code>"raw"</code>',
	'primes'          => "He's 6'2\" tall, the '90s, 'quoted'",
	'dashes'          => 'a - b -- c --- d',
	'smilies'         => 'Nice :) and ;-) and :D',
	'comment-tag'     => "<!-- note -->\ntext",
	'nbsp'            => "x&nbsp;\n\ny",
	'option'          => "<select>\n<option>a</option>\n<option>b</option>\n</select>",
	'figcaption'      => "<figure><img src=\"a.png\">\n<figcaption>cap</figcaption>\n</figure>",
	'div'             => "<div>text</div>\n\nmore",
	'nested-shortcode'=> "[paypal-donation]",
	'wordpress'       => 'I use Wordpress daily.',
);
foreach ( $edge as $k => $v ) {
	$cases[ 'edge-' . $k ] = $v;
}

$tags = array( 'caption' => true, 'gallery' => true, 'paypal-donation' => true, 'fotogalerie' => true );
$out  = array();
foreach ( $cases as $name => $in ) {
	$out[] = array(
		'name'      => $name,
		'in'        => $in,
		'autop'     => wpautop( $in ),
		'unautop'   => shortcode_unautop( wpautop( $in ) ),
		'texturize' => wptexturize( $in, true ),
		'smilies'   => convert_smilies( $in ),
		'dangit'    => capital_P_dangit( $in ),
		'excerpt'   => wp_trim_words( excerpt_remove_blocks( strip_shortcodes( $in ) ), 55, ' [&hellip;]' ),
	);
}
echo json_encode( $out, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE ), "\n";
