package becatalog

import "sort"

// TreeNode is a node in the hierarchical directory tree produced by
// [Catalog.BuildTree]. Each TreeNode has a name, full path from root,
// and optional children.
type TreeNode struct {
	Name     string     // base name
	Path     string     // full path from root (e.g. "C:/Users/admin")
	Children []TreeNode // sorted by name
	IsDir    bool       // true if this is a directory node
}

// BuildTree reconstructs the hierarchical directory tree from the flat [Node]
// list in [Catalog.Tree]. Each node's RawIndex identifies its parent: "."
// for the root, or a numeric string for a non-root node (the index of the
// parent in the Tree array).
//
// The returned slice contains one TreeNode per root-level entry (typically
// one per volume). Children are sorted by name.
func (c *Catalog) BuildTree() []TreeNode {
	if len(c.Tree) == 0 {
		return nil
	}

	// Build a parent map: child index -> parent index. Root nodes have
	// parent -1.
	parentOf := make([]int, len(c.Tree))
	for i, n := range c.Tree {
		if n.RawIndex == "." || n.RawIndex == "" {
			parentOf[i] = -1
		} else {
			parentOf[i] = atoi(n.RawIndex)
		}
	}

	// Build child lists: parent index -> child indices.
	childLists := make([][]int, len(c.Tree))
	for i, parent := range parentOf {
		if parent >= 0 {
			childLists[parent] = append(childLists[parent], i)
		}
	}

	var build func(idx int, prefix string) TreeNode
	build = func(idx int, prefix string) TreeNode {
		n := c.Tree[idx]
		isDir := len(childLists[idx]) > 0 || parentOf[idx] == -1
		path := n.Name
		if prefix != "" {
			path = prefix + "/" + n.Name
		}
		var kids []TreeNode
		for _, ci := range childLists[idx] {
			kids = append(kids, build(ci, path))
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
		return TreeNode{
			Name:     n.Name,
			Path:     path,
			Children: kids,
			IsDir:    isDir,
		}
	}

	// Find root nodes (those with parent -1).
	var roots []TreeNode
	for i, parent := range parentOf {
		if parent < 0 {
			roots = append(roots, build(i, ""))
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })
	return roots
}

// atoi parses a decimal string to int, returning 0 on failure.
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
