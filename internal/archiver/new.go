package archiver

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/restic/chunker"
	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
)

// SelectFunc returns true for all items that should be included (files and
// dirs). If false is returned, files are ignored and dirs are not even walked.
type SelectFunc func(item string, fi os.FileInfo) bool

// NewArchiver saves a directory structure to the repo.
type NewArchiver struct {
	repo   restic.Repository
	Select SelectFunc
}

// Valid returns an error if anything is missing.
func (arch *NewArchiver) Valid() error {
	if arch.repo == nil {
		return errors.New("repo is not set")
	}

	if arch.Select == nil {
		return errors.New("Select is not set")

	}

	return nil
}

// SaveFile chunks a file and saves it to the repository.
func (arch *NewArchiver) SaveFile(ctx context.Context, filename string) (*restic.Node, error) {
	debug.Log("%v", filename)
	// f, err := fs.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	f, err := fs.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	chnker := chunker.New(f, arch.repo.Config().ChunkerPolynomial)

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, errors.Wrap(err, "Stat")
	}

	node, err := restic.NodeFromFileInfo(f.Name(), fi)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	if node.Type != "file" {
		_ = f.Close()
		return nil, errors.Errorf("node type %q is wrong", node.Type)
	}

	node.Content = []restic.ID{}
	buf := make([]byte, chunker.MinSize)
	for {
		chunk, err := chnker.Next(buf)
		if errors.Cause(err) == io.EOF {
			break
		}
		if err != nil {
			_ = f.Close()
			return nil, err
		}

		// test if the context has ben cancelled, return the error
		if ctx.Err() != nil {
			_ = f.Close()
			return nil, ctx.Err()
		}

		id, err := arch.repo.SaveBlob(ctx, restic.DataBlob, chunk.Data, restic.ID{})
		if err != nil {
			_ = f.Close()
			return nil, err
		}

		// test if the context has ben cancelled, return the error
		if ctx.Err() != nil {
			_ = f.Close()
			return nil, ctx.Err()
		}

		node.Content = append(node.Content, id)
		buf = chunk.Data
	}

	err = f.Close()
	if err != nil {
		return nil, err
	}

	return node, nil
}

func (arch *NewArchiver) saveTree(ctx context.Context, prefix string, fi os.FileInfo, dir string) (*restic.Tree, error) {
	debug.Log("%v %v", prefix, dir)

	f, err := fs.Open(dir)
	if err != nil {
		return nil, errors.Wrap(err, "Open")
	}

	entries, err := f.Readdir(-1)
	if err != nil {
		return nil, errors.Wrap(err, "Readdir")
	}

	err = f.Close()
	if err != nil {
		return nil, errors.Wrap(err, "Close")
	}

	tree := restic.NewTree()
	for _, fi := range entries {
		pathname := filepath.Join(dir, fi.Name())

		abspathname, err := filepath.Abs(pathname)
		if err != nil {
			return nil, err
		}

		if !arch.Select(abspathname, fi) {
			debug.Log("% is excluded", pathname)
			continue
		}

		var node *restic.Node
		switch {
		case fs.IsRegularFile(fi):
			node, err = arch.SaveFile(ctx, pathname)
		case fi.Mode().IsDir():
			node, err = arch.SaveDir(ctx, path.Join(prefix, fi.Name()), fi, pathname)
		default:
			node, err = restic.NodeFromFileInfo(pathname, fi)
		}

		if err != nil {
			return nil, err
		}

		err = tree.Insert(node)
		if err != nil {
			return nil, err
		}
	}

	return tree, nil
}

// SaveDir reads a directory and saves it to the repo.
func (arch *NewArchiver) SaveDir(ctx context.Context, prefix string, fi os.FileInfo, dir string) (*restic.Node, error) {
	debug.Log("%v %v", prefix, dir)

	treeNode, err := restic.NodeFromFileInfo(dir, fi)
	if err != nil {
		return nil, err
	}

	tree, err := arch.saveTree(ctx, prefix, fi, dir)
	if err != nil {
		return nil, err
	}

	id, err := arch.repo.SaveTree(ctx, tree)
	if err != nil {
		return nil, err
	}

	treeNode.Subtree = &id
	return treeNode, nil
}

// SnapshotOptions bundle attributes for a new snapshot.
type SnapshotOptions struct {
	Hostname string
	Time     time.Time
	Tags     []string
	Parent   restic.ID
	Targets  []string
}

// Save saves a target (file or directory) to the repo.
func (arch *NewArchiver) Save(ctx context.Context, prefix, target string) (node *restic.Node, err error) {
	debug.Log("%v target %q", prefix, target)
	fi, err := fs.Lstat(target)
	if err != nil {
		return nil, err
	}

	abstarget, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}

	if !arch.Select(abstarget, fi) {
		debug.Log("%v is excluded", target)
		return nil, nil
	}

	switch {
	case fs.IsRegularFile(fi):
		node, err = arch.SaveFile(ctx, target)
	case fi.IsDir():
		node, err = arch.SaveDir(ctx, prefix, fi, target)
	default:
		node, err = restic.NodeFromFileInfo(target, fi)
	}

	return node, err
}

func (arch *NewArchiver) saveArchiveTree(ctx context.Context, prefix string, atree *ArchiveTree) (*restic.Tree, error) {
	debug.Log("%v (%v nodes)", prefix, len(atree.Nodes))

	tree := restic.NewTree()

	for name, subatree := range atree.Nodes {
		debug.Log("%v save node %v", prefix, name)

		// this is a leaf node
		if subatree.Path != "" {
			node, err := arch.Save(ctx, path.Join(prefix, name), subatree.Path)
			if err != nil {
				return nil, err
			}

			if node == nil {
				debug.Log("%v excluded: %v", prefix, name)
				continue
			}

			node.Name = name

			err = tree.Insert(node)
			if err != nil {
				return nil, err
			}

			continue
		}

		// not a leaf node, archive subtree
		subtree, err := arch.saveArchiveTree(ctx, path.Join(prefix, name), &subatree)
		if err != nil {
			return nil, err
		}

		id, err := arch.repo.SaveTree(ctx, subtree)
		if err != nil {
			return nil, err
		}

		if subatree.FileInfoPath == "" {
			return nil, errors.Errorf("FileInfoPath for %v/%v is empty", prefix, name)
		}

		debug.Log("%v, saved subtree %v as %v", prefix, subtree, id.Str())

		fi, err := fs.Lstat(subatree.FileInfoPath)
		if err != nil {
			return nil, err
		}

		debug.Log("%v, dir node data loaded from %v", prefix, subatree.FileInfoPath)

		node, err := restic.NodeFromFileInfo(subatree.FileInfoPath, fi)
		if err != nil {
			return nil, err
		}

		node.Name = name
		node.Subtree = &id

		err = tree.Insert(node)
		if err != nil {
			return nil, err
		}
	}

	return tree, nil
}

func readdirnames(dir string) ([]string, error) {
	f, err := fs.Open(dir)
	if err != nil {
		return nil, err
	}

	entries, err := f.Readdirnames(-1)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	err = f.Close()
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// resolveRelativeTargets replaces targets that only contain relative
// directories ("." or "../../") to the contents of the directory.
func resolveRelativeTargets(targets []string) ([]string, error) {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		pc := pathComponents(target, false)
		if len(pc) > 0 {
			result = append(result, target)
			continue
		}

		debug.Log("replacing %q with readdir(%q)", target, target)
		entries, err := readdirnames(target)
		if err != nil {
			return nil, err
		}

		for _, name := range entries {
			result = append(result, filepath.Join(target, name))
		}
	}

	return result, nil
}

// Snapshot saves several targets and returns a snapshot.
func (arch *NewArchiver) Snapshot(ctx context.Context, targets []string) (*restic.Snapshot, restic.ID, error) {
	err := arch.Valid()
	if err != nil {
		return nil, restic.ID{}, err
	}

	for i, target := range targets {
		targets[i] = filepath.Clean(target)
	}

	debug.Log("targets before resolving: %v", targets)

	targets, err = resolveRelativeTargets(targets)
	if err != nil {
		return nil, restic.ID{}, err
	}

	debug.Log("targets after resolving: %v", targets)

	atree, err := NewArchiveTree(targets)
	if err != nil {
		return nil, restic.ID{}, err
	}

	tree, err := arch.saveArchiveTree(ctx, "/", atree)
	if err != nil {
		return nil, restic.ID{}, err
	}

	id, err := arch.repo.SaveTree(ctx, tree)
	if err != nil {
		return nil, restic.ID{}, err
	}

	err = arch.repo.Flush(ctx)
	if err != nil {
		return nil, restic.ID{}, err
	}

	err = arch.repo.SaveIndex(ctx)
	if err != nil {
		return nil, restic.ID{}, err
	}

	sn, err := restic.NewSnapshot(targets, nil, "", time.Now())
	sn.Tree = &id

	id, err = arch.repo.SaveJSONUnpacked(ctx, restic.SnapshotFile, sn)
	if err != nil {
		return nil, restic.ID{}, err
	}

	return sn, id, nil
}
