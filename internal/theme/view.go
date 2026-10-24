package theme

import "html/template"

// View is the data passed to every page template. HTML fragments are
// prepared by the site package; the templates arrange them in the theme's
// markup.
type View struct {
	Lang            string
	Charset         string
	Title           string // document <title>
	SiteName        string
	SiteDescription string
	HomeURL         string
	IsFront         bool // aria-current on the site title
	BodyClass       string
	Head            template.HTML // feed links, canonical, styles, inline CSS
	Menu            template.HTML // primary navigation
	Sidebar         template.HTML // empty: no sidebar rendered
	FooterScripts   template.HTML
	Credit          template.HTML // footer site-info content
	SearchForm      template.HTML

	// Loops (index, archive, search).
	Posts        []*Post
	ArchiveTitle template.HTML // archive header h1 content
	ArchiveMeta  template.HTML // term description
	AuthorInfo   template.HTML
	NavAbove     template.HTML
	NavBelow     template.HTML
	SearchQuery  string

	// Singular (single, page, attachment).
	Post      *Post
	SingleNav template.HTML
	Comments  template.HTML
}

// Post is one entry in a loop or the singular post.
type Post struct {
	ID           int64
	Class        string
	Title        template.HTML
	URL          string
	Featured     bool // sticky on the first home page
	Thumbnail    template.HTML
	CommentsLink template.HTML // empty when comments are closed
	Content      template.HTML
	Summary      template.HTML // excerpt (search results)
	Meta         template.HTML // entry meta (posts)

	// Attachment pages.
	AttachmentMeta template.HTML
	ImageNav       template.HTML
	Attachment     template.HTML
	Caption        template.HTML
}
