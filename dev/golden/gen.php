<?php
// Generates golden outputs of WordPress formatting functions for the Go
// ports in internal/render. Run inside the reference WordPress container:
//   make golden
// Modes (argv[1]):
//   edge  the generic corpus below (committed: internal/render/testdata/golden.json)
//   site  all published posts/pages of the imported site (local only:
//         local/golden-site.json, contains site content)
$_SERVER['HTTP_HOST'] = '127.0.0.1:8081';
require '/var/www/html/wp-load.php';

$mode  = $argv[1] ?? 'edge';
$cases = array();
if ( 'site' === $mode ) {
	foreach ( get_posts( array( 'post_type' => array( 'post', 'page' ), 'post_status' => 'publish', 'numberposts' => -1, 'orderby' => 'ID', 'order' => 'ASC' ) ) as $p ) {
		$cases[ 'post-' . $p->ID ] = $p->post_content;
		$cases[ 'title-' . $p->ID ] = $p->post_title;
	}
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
	'block-group'     => "<!-- wp:group -->\n<div class=\"wp-block-group\"><!-- wp:paragraph -->\n<p>x</p>\n<!-- /wp:paragraph --></div>\n<!-- /wp:group -->",
	'block-heading'   => "<!-- wp:heading -->\n<h2 class=\"wp-block-heading\">H</h2>\n<!-- /wp:heading -->",
	'block-list-ol'   => "<!-- wp:list {\"ordered\":true} -->\n<ol><!-- wp:list-item -->\n<li>a</li>\n<!-- /wp:list-item --></ol>\n<!-- /wp:list -->",
	'block-p-class'   => "<!-- wp:paragraph {\"className\":\"x\"} -->\n<p class=\"x\">y</p>\n<!-- /wp:paragraph -->",
	'classic-post'    => "Intro with an <a href=\"https://example.org/\">example link</a>.\n\nFeatures include:\n<ul>\n \t<li>Item one</li>\n \t<li>Item two -- with dash</li>\n</ul>\n<span style=\"color: #ff0000;\"><strong>Note (last update 1.2.2021):</strong></span>\n\n<code>echo \"hello\"; # it's code</code>\n<pre>keep  \"this\"\n\nformatting</pre>\nSee the <em>\"docs\"</em>, it's 5'10\" tall... and 10x20.\n\n[gallery ids=\"1,2\"]\n\nThe end.",
	'block-post'      => "<!-- wp:paragraph -->\n<p>Intro &amp; overview</p>\n<!-- /wp:paragraph -->\n\n<!-- wp:image {\"id\":5,\"sizeSlug\":\"large\",\"linkDestination\":\"media\"} -->\n<figure class=\"wp-block-image size-large\"><a href=\"https://example.org/wp-content/uploads/a.png\"><img src=\"https://example.org/wp-content/uploads/a-1024x768.png\" alt=\"\" class=\"wp-image-5\"/></a></figure>\n<!-- /wp:image -->\n\n<!-- wp:list -->\n<ul class=\"wp-block-list\"><!-- wp:list-item -->\n<li>One</li>\n<!-- /wp:list-item -->\n\n<!-- wp:list-item -->\n<li>Two</li>\n<!-- /wp:list-item --></ul>\n<!-- /wp:list -->\n\n<!-- wp:quote -->\n<blockquote class=\"wp-block-quote\"><!-- wp:paragraph -->\n<p><strong>Note:</strong> quoted</p>\n<!-- /wp:paragraph --></blockquote>\n<!-- /wp:quote -->\n\n<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->\n\n<!-- wp:code -->\n<pre class=\"wp-block-code\"><code>a = \"b\"</code></pre>\n<!-- /wp:code -->\n\n<!-- wp:paragraph {\"fontSize\":\"medium\"} -->\n<p class=\"has-medium-font-size\">Big</p>\n<!-- /wp:paragraph -->",
	'block-layout'    => "<!-- wp:group {\"layout\":{\"type\":\"constrained\"}} -->\n<div class=\"wp-block-group\"><!-- wp:columns -->\n<div class=\"wp-block-columns\"><!-- wp:column {\"width\":\"100%\"} -->\n<div class=\"wp-block-column\" style=\"flex-basis:100%\"><!-- wp:paragraph -->\n<p>In a column</p>\n<!-- /wp:paragraph --></div>\n<!-- /wp:column --></div>\n<!-- /wp:columns -->\n\n<!-- wp:gallery {\"columns\":2,\"linkTo\":\"file\",\"sizeSlug\":\"full\"} -->\n<figure class=\"wp-block-gallery has-nested-images columns-2 is-cropped\"><!-- wp:image {\"id\":6,\"sizeSlug\":\"full\",\"linkDestination\":\"media\"} -->\n<figure class=\"wp-block-image size-full\"><a href=\"https://example.org/wp-content/uploads/b.png\"><img src=\"https://example.org/wp-content/uploads/b.png\" alt=\"\" class=\"wp-image-6\"/></a></figure>\n<!-- /wp:image -->\n\n<!-- wp:image {\"id\":7,\"sizeSlug\":\"full\",\"linkDestination\":\"media\"} -->\n<figure class=\"wp-block-image size-full\"><a href=\"https://example.org/wp-content/uploads/c.png\"><img src=\"https://example.org/wp-content/uploads/c.png\" alt=\"\" class=\"wp-image-7\"/></a></figure>\n<!-- /wp:image --></figure>\n<!-- /wp:gallery --></div>\n<!-- /wp:group -->\n\n<!-- wp:image {\"align\":\"left\",\"id\":8,\"linkDestination\":\"custom\"} -->\n<figure class=\"wp-block-image alignleft\"><a href=\"https://example.org/x/\"><img src=\"https://example.org/wp-content/uploads/d-300x200.png\" alt=\"\" class=\"wp-image-8\"/></a></figure>\n<!-- /wp:image -->\n\n<!-- wp:paragraph -->\n<p><br>After image</p>\n<!-- /wp:paragraph -->",
);
foreach ( $edge as $k => $v ) {
	$cases[ 'edge-' . $k ] = $v;
}
if ( 'site' === $mode ) {
	$cases = array_filter( $cases, fn( $k ) => ! str_starts_with( $k, 'edge-' ), ARRAY_FILTER_USE_KEY );
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
		'blocks'    => has_blocks( $in ) ? do_blocks( $in ) : '',
	);
}
echo json_encode( $out, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE ), "\n";
