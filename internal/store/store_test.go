package store

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

func mustCreate(t *testing.T, s *Store, path string, kind Kind, ephemeral bool) Metadata {
	t.Helper()
	m, err := s.Create(path, kind, ephemeral)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	return m
}

func mustSet(t *testing.T, s *Store, path string, data []byte, ifGen int64) Metadata {
	t.Helper()
	m, err := s.SetContents(path, data, ifGen)
	if err != nil {
		t.Fatalf("SetContents(%q): %v", path, err)
	}
	return m
}

func TestRootExists(t *testing.T) {
	s := New()
	m, err := s.Stat("/")
	if err != nil {
		t.Fatalf("Stat(/): %v", err)
	}
	if m.Kind != Dir {
		t.Errorf("root Kind = %v, want Dir", m.Kind)
	}
}

func TestCreateAndStat(t *testing.T) {
	s := New()
	d := mustCreate(t, s, "/a", Dir, false)
	f := mustCreate(t, s, "/a/x", File, true)

	if d.Kind != Dir || d.Ephemeral {
		t.Errorf("/a = %+v, want permanent Dir", d)
	}
	if f.Kind != File || !f.Ephemeral {
		t.Errorf("/a/x = %+v, want ephemeral File", f)
	}
	if d.InstanceNum <= 0 {
		t.Errorf("/a InstanceNum = %d, want > 0", d.InstanceNum)
	}
	if f.InstanceNum <= d.InstanceNum {
		t.Errorf("/a/x InstanceNum = %d, want > %d (created later)", f.InstanceNum, d.InstanceNum)
	}

	for path, want := range map[string]Metadata{"/a": d, "/a/x": f} {
		got, err := s.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		if got != want {
			t.Errorf("Stat(%q) = %+v, want %+v (what Create returned)", path, got, want)
		}
	}
}

func TestInvalidPaths(t *testing.T) {
	s := New()
	for _, p := range []string{"", "a", "a/b", "/a/", "//", "/a//b"} {
		if _, err := s.Create(p, File, false); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Create(%q) err = %v, want ErrInvalidPath", p, err)
		}
		if _, err := s.Stat(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Stat(%q) err = %v, want ErrInvalidPath", p, err)
		}
	}
}

func TestCreateErrors(t *testing.T) {
	s := New()
	mustCreate(t, s, "/a", Dir, false)
	mustCreate(t, s, "/a/f", File, false)

	tests := []struct {
		path string
		want error
	}{
		{"/", ErrExists},
		{"/a", ErrExists},
		{"/a/f", ErrExists},
		{"/missing/f", ErrNotFound}, // no implicit parents
		{"/a/f/g", ErrNotDir},       // parent is a file
	}
	for _, tc := range tests {
		if _, err := s.Create(tc.path, File, false); !errors.Is(err, tc.want) {
			t.Errorf("Create(%q) err = %v, want %v", tc.path, err, tc.want)
		}
	}
}

func TestNewFileIsEmpty(t *testing.T) {
	s := New()
	mustCreate(t, s, "/f", File, false)
	data, _, err := s.GetContents("/f")
	if err != nil {
		t.Fatalf("GetContents: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("new file contents = %q, want empty", data)
	}
}

func TestSetContentsUnconditional(t *testing.T) {
	s := New()
	created := mustCreate(t, s, "/f", File, false)

	m1 := mustSet(t, s, "/f", []byte("hello"), 0)
	if m1.ContentGen <= created.ContentGen {
		t.Errorf("ContentGen after write = %d, want > %d", m1.ContentGen, created.ContentGen)
	}
	if m1.InstanceNum != created.InstanceNum {
		t.Errorf("write changed InstanceNum: %d -> %d", created.InstanceNum, m1.InstanceNum)
	}

	data, got, err := s.GetContents("/f")
	if err != nil {
		t.Fatalf("GetContents: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("contents = %q, want %q", data, "hello")
	}
	if got != m1 {
		t.Errorf("GetContents meta = %+v, want %+v", got, m1)
	}

	m2 := mustSet(t, s, "/f", []byte("world"), 0)
	if m2.ContentGen <= m1.ContentGen {
		t.Errorf("second write ContentGen = %d, want > %d", m2.ContentGen, m1.ContentGen)
	}
}

func TestChecksum(t *testing.T) {
	s := New()
	mustCreate(t, s, "/f", File, false)
	mustCreate(t, s, "/g", File, false)

	a := mustSet(t, s, "/f", []byte("same"), 0)
	b := mustSet(t, s, "/g", []byte("same"), 0)
	if a.Checksum != b.Checksum {
		t.Errorf("equal contents gave different checksums: %x vs %x", a.Checksum, b.Checksum)
	}
	c := mustSet(t, s, "/g", []byte("different"), 0)
	if c.Checksum == a.Checksum {
		t.Errorf("different contents gave the same checksum %x", c.Checksum)
	}
}

func TestSetContentsCompareAndSwap(t *testing.T) {
	s := New()
	mustCreate(t, s, "/f", File, false)

	v1 := mustSet(t, s, "/f", []byte("v1"), 0)
	v2 := mustSet(t, s, "/f", []byte("v2"), v1.ContentGen) // matching generation: succeeds

	// A writer still holding v1's generation lost the race.
	if _, err := s.SetContents("/f", []byte("stale"), v1.ContentGen); !errors.Is(err, ErrGenMismatch) {
		t.Fatalf("stale SetContents err = %v, want ErrGenMismatch", err)
	}

	data, m, err := s.GetContents("/f")
	if err != nil {
		t.Fatalf("GetContents: %v", err)
	}
	if string(data) != "v2" || m != v2 {
		t.Errorf("after failed write got %q %+v, want %q %+v", data, m, "v2", v2)
	}
}

func TestContentsErrors(t *testing.T) {
	s := New()
	mustCreate(t, s, "/d", Dir, false)

	if _, _, err := s.GetContents("/d"); !errors.Is(err, ErrIsDir) {
		t.Errorf("GetContents(dir) err = %v, want ErrIsDir", err)
	}
	if _, err := s.SetContents("/d", []byte("x"), 0); !errors.Is(err, ErrIsDir) {
		t.Errorf("SetContents(dir) err = %v, want ErrIsDir", err)
	}
	if _, _, err := s.GetContents("/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetContents(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.SetContents("/nope", []byte("x"), 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetContents(missing) err = %v, want ErrNotFound", err)
	}
}

// The store must not share byte slices with callers.
func TestContentsAreCopied(t *testing.T) {
	s := New()
	mustCreate(t, s, "/f", File, false)

	in := []byte("abc")
	mustSet(t, s, "/f", in, 0)
	in[0] = 'X'

	out, _, _ := s.GetContents("/f")
	if !bytes.Equal(out, []byte("abc")) {
		t.Fatalf("caller's slice leaked into store: got %q", out)
	}
	out[0] = 'Y'

	again, _, _ := s.GetContents("/f")
	if !bytes.Equal(again, []byte("abc")) {
		t.Errorf("returned slice aliases store: got %q", again)
	}
}

func TestReadDir(t *testing.T) {
	s := New()
	mustCreate(t, s, "/d", Dir, false)
	mustCreate(t, s, "/d/b", File, false)
	mustCreate(t, s, "/d/a", File, false)
	mustCreate(t, s, "/d/c", Dir, false)
	mustCreate(t, s, "/d/c/deep", File, false)
	mustCreate(t, s, "/e", Dir, false)

	tests := []struct {
		path string
		want []string
	}{
		{"/", []string{"d", "e"}},
		{"/d", []string{"a", "b", "c"}}, // sorted, names only, not recursive
		{"/e", []string{}},
	}
	for _, tc := range tests {
		got, err := s.ReadDir(tc.path)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", tc.path, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ReadDir(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	if _, err := s.ReadDir("/d/a"); !errors.Is(err, ErrNotDir) {
		t.Errorf("ReadDir(file) err = %v, want ErrNotDir", err)
	}
	if _, err := s.ReadDir("/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadDir(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := New()
	mustCreate(t, s, "/d", Dir, false)
	mustCreate(t, s, "/d/f", File, false)

	if err := s.Delete("/d"); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("Delete(non-empty dir) err = %v, want ErrNotEmpty", err)
	}
	if err := s.Delete("/d/f"); err != nil {
		t.Fatalf("Delete(/d/f): %v", err)
	}
	if _, err := s.Stat("/d/f"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat after delete err = %v, want ErrNotFound", err)
	}
	if err := s.Delete("/d"); err != nil {
		t.Fatalf("Delete(empty /d): %v", err)
	}
	if err := s.Delete("/d"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
	if err := s.Delete("/"); err == nil {
		t.Error("Delete(/) succeeded, want an error")
	}
	if names, _ := s.ReadDir("/"); len(names) != 0 {
		t.Errorf("root still lists %q", names)
	}
}

// The Q1 scenario. Encodes the global-counter decision: a recreated node's
// numbers are strictly greater than anything the deleted node ever had, so a
// client holding the old generation can never mistake it for current.
func TestRecreatedNodeNeverReusesNumbers(t *testing.T) {
	s := New()
	old := mustCreate(t, s, "/x", File, false)
	var cached Metadata
	for i := range 3 {
		cached = mustSet(t, s, "/x", fmt.Appendf(nil, "old %d", i), 0)
	}

	if err := s.Delete("/x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	fresh := mustCreate(t, s, "/x", File, false)
	if fresh.InstanceNum <= old.InstanceNum {
		t.Errorf("recreated InstanceNum = %d, want > %d", fresh.InstanceNum, old.InstanceNum)
	}
	var now Metadata
	for i := range 3 {
		now = mustSet(t, s, "/x", fmt.Appendf(nil, "new %d", i), 0)
	}
	if now.ContentGen <= cached.ContentGen {
		t.Errorf("recreated ContentGen = %d, want > %d: a client caching the old file would accept stale data",
			now.ContentGen, cached.ContentGen)
	}
}

// Every create and write takes its own number from the global counter, so all
// of them must be distinct even when goroutines race. Run with -race too.
func TestConcurrentUse(t *testing.T) {
	s := New()
	const n = 100
	created := make([]Metadata, n)
	written := make([]Metadata, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := fmt.Sprintf("/f%d", i)
			if created[i], errs[i] = s.Create(p, File, false); errs[i] != nil {
				return
			}
			written[i], errs[i] = s.SetContents(p, []byte(p), 0)
			s.ReadDir("/") // concurrent reader
		}()
	}
	wg.Wait()

	seen := make(map[int64]string)
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		for what, v := range map[string]int64{
			"InstanceNum": created[i].InstanceNum,
			"ContentGen":  written[i].ContentGen,
		} {
			label := fmt.Sprintf("/f%d %s", i, what)
			if prev, dup := seen[v]; dup {
				t.Errorf("counter value %d handed out twice: %s and %s", v, prev, label)
			}
			seen[v] = label
		}
	}
	if names, _ := s.ReadDir("/"); len(names) != n {
		t.Errorf("root has %d entries, want %d", len(names), n)
	}
}
