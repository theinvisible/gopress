-- Reference counts of the WordPress source; target values for import-wp.
SELECT 'posts' AS what, post_type, post_status, COUNT(*) AS n FROM wp_posts
  WHERE post_type NOT IN ('revision') GROUP BY post_type, post_status
UNION ALL SELECT 'comments', comment_type, comment_approved, COUNT(*) FROM wp_comments GROUP BY comment_type, comment_approved
UNION ALL SELECT 'terms', taxonomy, '', COUNT(*) FROM wp_term_taxonomy GROUP BY taxonomy
UNION ALL SELECT 'users', '', '', COUNT(*) FROM wp_users
UNION ALL SELECT 'postmeta', '', '', COUNT(*) FROM wp_postmeta
UNION ALL SELECT 'options', '', '', COUNT(*) FROM wp_options
ORDER BY 1, 2, 3;
