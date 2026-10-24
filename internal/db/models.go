package db

// Post types and statuses (WordPress names).
const (
	TypePost       = "post"
	TypePage       = "page"
	TypeAttachment = "attachment"

	StatusPublish = "publish"
	StatusDraft   = "draft"
	StatusPending = "pending"
	StatusPrivate = "private"
	StatusFuture  = "future"
	StatusTrash   = "trash"
	StatusInherit = "inherit" // attachments

	FormatHTML    = "html"
	FormatWPAutop = "wpautop"
)

// Comment statuses.
const (
	CommentApproved = "approved"
	CommentPending  = "pending"
	CommentSpam     = "spam"
	CommentTrash    = "trash"
)

// Taxonomies.
const (
	TaxCategory = "category"
	TaxTag      = "post_tag"
)

type User struct {
	ID            int64
	Login         string
	Nicename      string
	Email         string
	URL           string
	DisplayName   string
	Description   string
	PassHash      string
	Role          string
	RegisteredGMT Time
}

type Post struct {
	ID              int64
	Type            string
	Status          string
	Slug            string
	ParentID        int64
	AuthorID        int64
	Title           string
	Content         string
	Excerpt         string
	Date            Time // local wall clock
	DateGMT         Time
	Modified        Time // local wall clock
	ModifiedGMT     Time
	CommentStatus   string
	PingStatus      string
	Password        string
	MenuOrder       int
	Template        string
	MimeType        string
	FeaturedMediaID int64
	CommentCount    int
	GUID            string
	ContentFormat   string // "html" or "wpautop"
}

type Term struct {
	ID          int64
	Taxonomy    string
	Slug        string
	Name        string
	Description string
	ParentID    int64
	Count       int
}

// MediaSize is one generated image size of an attachment.
type MediaSize struct {
	File   string `json:"file"` // file name, same directory as Media.File
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mime   string `json:"mime,omitempty"`
}

type Media struct {
	PostID   int64
	File     string // relative to the uploads dir
	Mime     string
	Width    int
	Height   int
	Filesize int64
	Alt      string
	Sizes    map[string]MediaSize
}

type Comment struct {
	ID          int64
	PostID      int64
	ParentID    int64
	Type        string
	Status      string
	Author      string
	AuthorEmail string
	AuthorURL   string
	AuthorIP    string
	Agent       string
	UserID      int64
	Date        Time // local wall clock
	DateGMT     Time
	Content     string
}

type Widget struct {
	ID       string
	Sidebar  string
	Position int
	Type     string
	Settings map[string]any
}
