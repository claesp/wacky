package wacky

import (
	"path"
	"sort"
	"strings"
)

// Node is a directory or a page in the navigation tree.
type Node struct {
	// Name is the display label: the page title, or the directory name.
	Name string
	// Slug is the URL path of the node, without a leading slash.
	Slug string
	// Page is non-nil for pages and for directories that have an index page.
	Page *Page
	// Children is non-empty for directories.
	Children []*Node
}

// IsDir reports whether the node has children.
func (n *Node) IsDir() bool { return len(n.Children) > 0 }

// URL returns the path the node links to.
func (n *Node) URL() string {
	if n.Slug == "" {
		return "/"
	}
	return "/wacky/" + n.Slug
}

// Find returns the node at slug, or nil. The empty slug returns the root.
func (n *Node) Find(slug string) *Node {
	if n == nil {
		return nil
	}
	if slug == "" {
		return n
	}
	current := n
	for _, part := range strings.Split(slug, "/") {
		next := (*Node)(nil)
		for _, child := range current.Children {
			if path.Base(child.Slug) == part {
				next = child
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

// buildTree groups pages by directory. Directories are listed before pages and
// both are sorted by name, so the same page set always yields the same tree.
func buildTree(pages []*Page) *Node {
	root := &Node{}
	dirs := map[string]*Node{"": root}

	// Directory nodes first, so an index page can attach to its own directory.
	for _, p := range pages {
		ensureDir(dirs, p.Dir)
	}
	for _, p := range pages {
		if p.IsIndex {
			if dir := dirs[p.Dir]; dir != nil && dir.Page == nil {
				dir.Page = p
				if dir != root {
					dir.Name = p.Title
				}
				continue
			}
		}
		parent := dirs[p.Dir]
		if parent == nil {
			parent = root
		}
		parent.Children = append(parent.Children, &Node{
			Name: p.Title,
			Slug: p.Slug,
			Page: p,
		})
	}

	sortNode(root)
	return root
}

// ensureDir creates the node chain for a directory path.
func ensureDir(dirs map[string]*Node, dir string) *Node {
	if node, ok := dirs[dir]; ok {
		return node
	}
	parent := ensureDir(dirs, path.Dir("/" + dir)[1:])
	if dir == "." {
		return parent
	}
	node := &Node{Name: titleFor(path.Base(dir)), Slug: dir}
	parent.Children = append(parent.Children, node)
	dirs[dir] = node
	return node
}

func sortNode(n *Node) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for _, child := range n.Children {
		sortNode(child)
	}
}

// Breadcrumb is one step of the path from the root to a page.
type Breadcrumb struct {
	Name string
	URL  string
}

// Breadcrumbs returns the trail leading to a slug.
func (s *Store) Breadcrumbs(slug string) []Breadcrumb {
	slug = normalizeSlug(slug)
	if slug == "" {
		return nil
	}

	tree := s.current().tree
	trail := make([]Breadcrumb, 0, strings.Count(slug, "/")+1)
	parts := strings.Split(slug, "/")
	for i := range parts {
		partial := strings.Join(parts[:i+1], "/")
		name := titleFor(parts[i])
		if node := tree.Find(partial); node != nil && node.Name != "" {
			name = node.Name
		}
		trail = append(trail, Breadcrumb{Name: name, URL: "/wacky/" + partial})
	}
	return trail
}
