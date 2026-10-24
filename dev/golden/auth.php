<?php
// Generates password hash test vectors with WordPress' own hashing code for
// internal/auth (verification of imported WordPress hashes). Run inside the
// reference WordPress container:
//   make golden
$_SERVER['HTTP_HOST'] = '127.0.0.1:8081';
require '/var/www/html/wp-load.php';
require_once ABSPATH . WPINC . '/class-phpass.php';

$passwords = array( 'password', 'correct horse battery staple', "  spaces around  ", 'Ünïcödé-🔑', str_repeat( 'long', 30 ) );
$phpass    = new PasswordHash( 8, true );
$out       = array();
foreach ( $passwords as $pw ) {
	// wp_hash_password() trims, wp_check_password() does not: the trimmed
	// password is the one that verifies.
	$out[] = array( 'password' => trim( $pw ), 'scheme' => 'wp', 'hash' => wp_hash_password( $pw ) );
	$out[] = array( 'password' => $pw, 'scheme' => 'phpass', 'hash' => $phpass->HashPassword( $pw ) );
	$out[] = array( 'password' => $pw, 'scheme' => 'bcrypt', 'hash' => password_hash( $pw, PASSWORD_BCRYPT ) );
	$out[] = array( 'password' => $pw, 'scheme' => 'md5', 'hash' => md5( $pw ) );
}
// Every vector must verify with WordPress itself.
foreach ( $out as $v ) {
	if ( ! wp_check_password( $v['password'], $v['hash'] ) ) {
		fwrite( STDERR, "vector does not verify: {$v['scheme']}\n" );
		exit( 1 );
	}
}
echo json_encode( $out, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES ), "\n";
