package auth

import "github.com/theinvisible/gopress/internal/db"

// Capabilities (WordPress names).
const (
	CapRead                = "read"
	CapEditPosts           = "edit_posts"
	CapEditOthersPosts     = "edit_others_posts"
	CapEditPublishedPosts  = "edit_published_posts"
	CapPublishPosts        = "publish_posts"
	CapDeletePosts         = "delete_posts"
	CapDeleteOthersPosts   = "delete_others_posts"
	CapDeletePublished     = "delete_published_posts"
	CapEditPrivatePosts    = "edit_private_posts"
	CapReadPrivatePosts    = "read_private_posts"
	CapEditPages           = "edit_pages"
	CapEditOthersPages     = "edit_others_pages"
	CapEditPublishedPages  = "edit_published_pages"
	CapPublishPages        = "publish_pages"
	CapDeletePages         = "delete_pages"
	CapDeleteOthersPages   = "delete_others_pages"
	CapDeletePublishedPage = "delete_published_pages"
	CapEditPrivatePages    = "edit_private_pages"
	CapReadPrivatePages    = "read_private_pages"
	CapModerateComments    = "moderate_comments"
	CapManageCategories    = "manage_categories"
	CapUploadFiles         = "upload_files"
	CapUnfilteredHTML      = "unfiltered_html"
	CapManageOptions       = "manage_options"
	CapListUsers           = "list_users"
	CapEditUsers           = "edit_users"
	CapEditThemeOptions    = "edit_theme_options"
	CapActivatePlugins     = "activate_plugins"
	CapCreateUsers         = "create_users"
	CapDeleteUsers         = "delete_users"
	CapPromoteUsers        = "promote_users"
	CapRemoveUsers         = "remove_users"
)

// Roles (WordPress names).
const (
	RoleAdministrator = "administrator"
	RoleEditor        = "editor"
	RoleAuthor        = "author"
	RoleContributor   = "contributor"
	RoleSubscriber    = "subscriber"
)

func set(caps ...string) map[string]bool {
	m := make(map[string]bool, len(caps))
	for _, c := range caps {
		m[c] = true
	}
	return m
}

var (
	contributorCaps = []string{CapRead, CapEditPosts, CapDeletePosts}
	authorCaps      = append(contributorCaps[:len(contributorCaps):len(contributorCaps)],
		CapEditPublishedPosts, CapPublishPosts, CapDeletePublished, CapUploadFiles)
	editorCaps = append(authorCaps[:len(authorCaps):len(authorCaps)],
		CapEditOthersPosts, CapDeleteOthersPosts, CapEditPrivatePosts, CapReadPrivatePosts,
		CapEditPages, CapEditOthersPages, CapEditPublishedPages, CapPublishPages, CapDeletePages,
		CapDeleteOthersPages, CapDeletePublishedPage, CapEditPrivatePages, CapReadPrivatePages,
		CapModerateComments, CapManageCategories, CapUnfilteredHTML)
	adminCaps = append(editorCaps[:len(editorCaps):len(editorCaps)],
		CapManageOptions, CapListUsers, CapEditUsers, CapEditThemeOptions, CapActivatePlugins,
		CapCreateUsers, CapDeleteUsers, CapPromoteUsers, CapRemoveUsers)

	roleCaps = map[string]map[string]bool{
		RoleAdministrator: set(adminCaps...),
		RoleEditor:        set(editorCaps...),
		RoleAuthor:        set(authorCaps...),
		RoleContributor:   set(contributorCaps...),
		RoleSubscriber:    set(CapRead),
	}
)

// Roles lists the roles from most to least privileged with their labels.
var Roles = []struct{ Name, Label string }{
	{RoleAdministrator, "Administrator"}, {RoleEditor, "Editor"}, {RoleAuthor, "Author"},
	{RoleContributor, "Contributor"}, {RoleSubscriber, "Subscriber"},
}

// ValidRole reports whether role is a known role.
func ValidRole(role string) bool { _, ok := roleCaps[role]; return ok }

// RoleLabel returns the display name of a role.
func RoleLabel(role string) string {
	for _, r := range Roles {
		if r.Name == role {
			return r.Label
		}
	}
	return role
}

// Can reports whether user has a capability through its role (limited
// to u.CapLimit when set, for requests made with an access token).
func Can(u *db.User, capability string) bool {
	if u == nil {
		return false
	}
	return roleCaps[u.Role][capability] && (u.CapLimit == nil || u.CapLimit[capability])
}

// postCaps maps a post type to its capability names.
type postCaps struct {
	edit, editOthers, editPublished, editPrivate, publish, delete, deleteOthers, deletePublished string
}

func capsFor(typ string) postCaps {
	if typ == db.TypePage {
		return postCaps{CapEditPages, CapEditOthersPages, CapEditPublishedPages, CapEditPrivatePages,
			CapPublishPages, CapDeletePages, CapDeleteOthersPages, CapDeletePublishedPage}
	}
	return postCaps{CapEditPosts, CapEditOthersPosts, CapEditPublishedPosts, CapEditPrivatePosts,
		CapPublishPosts, CapDeletePosts, CapDeleteOthersPosts, CapDeletePublished}
}

// CanEditType reports whether user may edit (its own) posts of a type.
func CanEditType(u *db.User, typ string) bool { return Can(u, capsFor(typ).edit) }

// CanPublishType reports whether user may publish posts of a type.
func CanPublishType(u *db.User, typ string) bool { return Can(u, capsFor(typ).publish) }

// CanEditPost reports whether user may edit p (map_meta_cap 'edit_post').
func CanEditPost(u *db.User, p *db.Post) bool {
	c := capsFor(p.Type)
	if !Can(u, c.edit) {
		return false
	}
	if p.AuthorID != u.ID && !Can(u, c.editOthers) {
		return false
	}
	switch p.Status {
	case db.StatusPublish, db.StatusFuture:
		return Can(u, c.editPublished)
	case db.StatusPrivate:
		return p.AuthorID == u.ID || Can(u, c.editPrivate)
	}
	return true
}

// CanDeletePost reports whether user may trash or delete p.
func CanDeletePost(u *db.User, p *db.Post) bool {
	c := capsFor(p.Type)
	if !Can(u, c.delete) {
		return false
	}
	if p.AuthorID != u.ID && !Can(u, c.deleteOthers) {
		return false
	}
	if p.Status == db.StatusPublish || p.Status == db.StatusFuture {
		return Can(u, c.deletePublished)
	}
	return true
}
