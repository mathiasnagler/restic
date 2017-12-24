package archiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restic/restic/internal/checker"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/repository"
	"github.com/restic/restic/internal/restic"
	restictest "github.com/restic/restic/internal/test"
)

func prepareTempdirRepoSrc(t testing.TB, src TestDir) (tempdir string, repo restic.Repository, cleanup func()) {
	tempdir, removeTempdir := restictest.TempDir(t)
	repo, removeRepository := repository.TestRepository(t)

	TestCreateFiles(t, tempdir, src)

	cleanup = func() {
		removeRepository()
		removeTempdir()
	}

	return tempdir, repo, cleanup
}

func TestNewArchiverSaveFile(t *testing.T) {
	var tests = []TestFile{
		TestFile{Content: ""},
		TestFile{Content: "foo"},
		TestFile{Content: string(restictest.Random(23, 12*1024*1024+1287898))},
		TestFile{Content: string(restictest.Random(65, 45*1024*1024+8273413))},
	}

	for _, testfile := range tests {
		t.Run("", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempdir, repo, cleanup := prepareTempdirRepoSrc(t, TestDir{"file": testfile})
			defer cleanup()

			arch := NewArchiver{
				repo: repo,
				Select: func(string, os.FileInfo) bool {
					return true
				},
			}

			node, err := arch.SaveFile(ctx, filepath.Join(tempdir, "file"))
			if err != nil {
				t.Fatal(err)
			}

			err = repo.Flush(ctx)
			if err != nil {
				t.Fatal(err)
			}

			err = repo.SaveIndex(ctx)
			if err != nil {
				t.Fatal(err)
			}

			TestEnsureFileContent(ctx, t, repo, "file", node, testfile)
		})
	}
}

func TestNewArchiverSaveDir(t *testing.T) {
	const targetNodeName = "targetdir"

	var tests = []struct {
		src    TestDir
		chdir  string
		target string
		want   TestDir
	}{
		{
			src: TestDir{
				"targetfile": TestFile{Content: string(restictest.Random(888, 2*1024*1024+5000))},
			},
			target: ".",
			want: TestDir{
				"targetdir": TestDir{
					"targetfile": TestFile{Content: string(restictest.Random(888, 2*1024*1024+5000))},
				},
			},
		},
		{
			src: TestDir{
				"targetdir": TestDir{
					"foo":        TestFile{Content: "foo"},
					"emptyfile":  TestFile{Content: ""},
					"bar":        TestFile{Content: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"},
					"largefile":  TestFile{Content: string(restictest.Random(888, 2*1024*1024+5000))},
					"largerfile": TestFile{Content: string(restictest.Random(234, 5*1024*1024+5000))},
				},
			},
			target: "targetdir",
		},
		{
			src: TestDir{
				"foo":       TestFile{Content: "foo"},
				"emptyfile": TestFile{Content: ""},
				"bar":       TestFile{Content: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"},
			},
			target: ".",
			want: TestDir{
				"targetdir": TestDir{
					"foo":       TestFile{Content: "foo"},
					"emptyfile": TestFile{Content: ""},
					"bar":       TestFile{Content: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"},
				},
			},
		},
		{
			src: TestDir{
				"foo": TestDir{
					"subdir": TestDir{
						"x": TestFile{Content: "xxx"},
						"y": TestFile{Content: "yyyyyyyyyyyyyyyy"},
						"z": TestFile{Content: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
					},
					"file": TestFile{Content: "just a test"},
				},
			},
			chdir:  "foo/subdir",
			target: "../../",
			want: TestDir{
				"targetdir": TestDir{
					"foo": TestDir{
						"subdir": TestDir{
							"x": TestFile{Content: "xxx"},
							"y": TestFile{Content: "yyyyyyyyyyyyyyyy"},
							"z": TestFile{Content: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
						},
						"file": TestFile{Content: "just a test"},
					},
				},
			},
		},
		{
			src: TestDir{
				"foo": TestDir{
					"file":  TestFile{Content: "just a test"},
					"file2": TestFile{Content: "again"},
				},
			},
			target: "./foo",
			want: TestDir{
				"targetdir": TestDir{
					"file":  TestFile{Content: "just a test"},
					"file2": TestFile{Content: "again"},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempdir, repo, cleanup := prepareTempdirRepoSrc(t, test.src)
			defer cleanup()

			arch := NewArchiver{
				repo: repo,
				Select: func(string, os.FileInfo) bool {
					return true
				},
			}

			chdir := tempdir
			if test.chdir != "" {
				chdir = filepath.Join(chdir, test.chdir)
			}

			back := fs.TestChdir(t, chdir)
			defer back()

			fi, err := fs.Lstat(test.target)
			if err != nil {
				t.Fatal(err)
			}

			node, err := arch.SaveDir(ctx, "/", fi, test.target)
			if err != nil {
				t.Fatal(err)
			}

			node.Name = targetNodeName
			tree := &restic.Tree{Nodes: []*restic.Node{node}}
			treeID, err := repo.SaveTree(ctx, tree)
			if err != nil {
				t.Fatal(err)
			}

			err = repo.Flush(ctx)
			if err != nil {
				t.Fatal(err)
			}

			err = repo.SaveIndex(ctx)
			if err != nil {
				t.Fatal(err)
			}

			want := test.want
			if want == nil {
				want = test.src
			}
			TestEnsureTree(ctx, t, "/", repo, treeID, want)
		})
	}
}

func TestNewArchiverSnapshot(t *testing.T) {
	var tests = []struct {
		name    string
		src     TestDir
		want    TestDir
		chdir   string
		targets []string
	}{
		{
			name: "single-file",
			src: TestDir{
				"foo": TestFile{Content: "foo"},
			},
			targets: []string{"foo"},
		},
		{
			name: "file-current-dir",
			src: TestDir{
				"foo": TestFile{Content: "foo"},
			},
			targets: []string{"./foo"},
		},
		{
			name: "dir",
			src: TestDir{
				"target": TestDir{
					"foo": TestFile{Content: "foo"},
				},
			},
			targets: []string{"target"},
		},
		{
			name: "dir-current-dir",
			src: TestDir{
				"target": TestDir{
					"foo": TestFile{Content: "foo"},
				},
			},
			targets: []string{"./target"},
		},
		{
			name: "content-dir-current-dir",
			src: TestDir{
				"target": TestDir{
					"foo": TestFile{Content: "foo"},
				},
			},
			targets: []string{"./target/."},
		},
		{
			name: "current-dir",
			src: TestDir{
				"target": TestDir{
					"foo": TestFile{Content: "foo"},
				},
			},
			targets: []string{"."},
		},
		{
			name: "subdir",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo in subsubdir"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			targets: []string{"subdir"},
			want: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo in subsubdir"},
					},
				},
			},
		},
		{
			name: "subsubdir",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo in subsubdir"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			targets: []string{"subdir/subsubdir"},
			want: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo in subsubdir"},
					},
				},
			},
		},
		{
			name: "parent-dir",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
				},
				"other": TestFile{Content: "another file"},
			},
			chdir:   "subdir",
			targets: []string{".."},
		},
		{
			name: "parent-parent-dir",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
					"subsubdir": TestDir{
						"empty": TestFile{Content: ""},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			chdir:   "subdir/subsubdir",
			targets: []string{"../.."},
		},
		{
			name: "parent-parent-dir-slash",
			src: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			chdir:   "subdir/subsubdir",
			targets: []string{"../../"},
			want: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
		},
		{
			name: "parent-subdir",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
				},
				"other": TestFile{Content: "another file"},
			},
			chdir:   "subdir",
			targets: []string{"../subdir"},
			want: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo"},
				},
			},
		},
		{
			name: "parent-parent-dir-subdir",
			src: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			chdir:   "subdir/subsubdir",
			targets: []string{"../../subdir/subsubdir"},
			want: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
				},
			},
		},
		{
			name: "included-multiple1",
			src: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
					"other": TestFile{Content: "another file"},
				},
			},
			targets: []string{"subdir", "subdir/subsubdir"},
		},
		{
			name: "included-multiple2",
			src: TestDir{
				"subdir": TestDir{
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo"},
					},
					"other": TestFile{Content: "another file"},
				},
			},
			targets: []string{"subdir/subsubdir", "subdir"},
		},
		{
			name: "collision",
			src: TestDir{
				"subdir": TestDir{
					"foo": TestFile{Content: "foo in subdir"},
					"subsubdir": TestDir{
						"foo": TestFile{Content: "foo in subsubdir"},
					},
				},
				"foo": TestFile{Content: "another file"},
			},
			chdir:   "subdir",
			targets: []string{".", "../foo"},
			want: TestDir{

				"foo": TestFile{Content: "foo in subdir"},
				"subsubdir": TestDir{
					"foo": TestFile{Content: "foo in subsubdir"},
				},
				"foo-1": TestFile{Content: "another file"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempdir, repo, cleanup := prepareTempdirRepoSrc(t, test.src)
			defer cleanup()

			arch := NewArchiver{
				repo: repo,
				Select: func(string, os.FileInfo) bool {
					return true
				},
			}

			chdir := tempdir
			if test.chdir != "" {
				chdir = filepath.Join(chdir, filepath.FromSlash(test.chdir))
			}

			back := fs.TestChdir(t, chdir)
			defer back()

			var targets []string
			for _, target := range test.targets {
				targets = append(targets, os.ExpandEnv(target))
			}

			t.Logf("targets: %v", targets)
			_, snapshotID, err := arch.Snapshot(ctx, targets)
			if err != nil {
				t.Fatal(err)
			}

			t.Logf("saved as %v", snapshotID.Str())

			want := test.want
			if want == nil {
				want = test.src
			}
			TestEnsureSnapshot(t, repo, snapshotID, want)

			checker.TestCheckRepo(t, repo)
		})
	}
}

func TestNewArchiverSnapshotSelect(t *testing.T) {
	var tests = []struct {
		name  string
		src   TestDir
		want  TestDir
		selFn SelectFunc
	}{
		{
			name: "exclude-txt-files",
			src: TestDir{
				"work": TestDir{
					"foo":     TestFile{Content: "foo"},
					"foo.txt": TestFile{Content: "foo text file"},
					"subdir": TestDir{
						"other":   TestFile{Content: "other in subdir"},
						"bar.txt": TestFile{Content: "bar.txt in subdir"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			want: TestDir{
				"work": TestDir{
					"foo": TestFile{Content: "foo"},
					"subdir": TestDir{
						"other": TestFile{Content: "other in subdir"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			selFn: func(item string, fi os.FileInfo) bool {
				if filepath.Ext(item) == ".txt" {
					return false
				}
				return true
			},
		},
		{
			name: "exclude-dir",
			src: TestDir{
				"work": TestDir{
					"foo":     TestFile{Content: "foo"},
					"foo.txt": TestFile{Content: "foo text file"},
					"subdir": TestDir{
						"other":   TestFile{Content: "other in subdir"},
						"bar.txt": TestFile{Content: "bar.txt in subdir"},
					},
				},
				"other": TestFile{Content: "another file"},
			},
			want: TestDir{
				"work": TestDir{
					"foo":     TestFile{Content: "foo"},
					"foo.txt": TestFile{Content: "foo text file"},
				},
				"other": TestFile{Content: "another file"},
			},
			selFn: func(item string, fi os.FileInfo) bool {
				if filepath.Base(item) == "subdir" {
					return false
				}
				return true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempdir, repo, cleanup := prepareTempdirRepoSrc(t, test.src)
			defer cleanup()

			arch := NewArchiver{
				repo:   repo,
				Select: test.selFn,
			}

			back := fs.TestChdir(t, tempdir)
			defer back()

			targets := []string{"."}
			_, snapshotID, err := arch.Snapshot(ctx, targets)
			if err != nil {
				t.Fatal(err)
			}

			t.Logf("saved as %v", snapshotID.Str())

			want := test.want
			if want == nil {
				want = test.src
			}
			TestEnsureSnapshot(t, repo, snapshotID, want)

			checker.TestCheckRepo(t, repo)
		})
	}
}
