package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type memoryStore struct {
	mu        sync.Mutex
	data      map[uint64][]byte
	loseReply bool
	readError error
}

func newMemoryStore() *memoryStore { return &memoryStore{data: map[uint64][]byte{}} }
func (s *memoryStore) Read(ctx context.Context, revision uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readError != nil {
		return nil, s.readError
	}
	data, ok := s.data[revision]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), data...), nil
}
func (s *memoryStore) Create(ctx context.Context, revision uint64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[revision]; exists {
		return ErrExists
	}
	s.data[revision] = append([]byte(nil), data...)
	if s.loseReply {
		s.loseReply = false
		return errors.New("connection lost after commit")
	}
	return nil
}

func setup(t *testing.T) (*Catalog, *memoryStore) {
	t.Helper()
	store := newMemoryStore()
	c := New(store)
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, store
}
func apply(t *testing.T, c *Catalog, op Operation) Result {
	t.Helper()
	if op.Actor == "" {
		op.Actor = "admin"
	}
	r, err := c.Apply(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConcurrentInstancesNoLostUpdates(t *testing.T) {
	_, store := setup(t)
	const count = 32
	var wg sync.WaitGroup
	for index := range count {
		wg.Go(func() {
			c := New(store)
			r := apply(t, c, Operation{ID: fmt.Sprint(index), Kind: "mkdir", ParentID: "root", Name: "同名目录"})
			if r.Error != "" {
				t.Error(r)
			}
		})
	}
	wg.Wait()
	snapshot, err := New(store).Current(context.Background(), count)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != count+1 || snapshot.Revision != count || len(snapshot.Receipts) != count {
		t.Fatalf("lost update: %+v", snapshot)
	}
	used := map[string]bool{}
	for _, node := range snapshot.Children("root") {
		if used[node.Name] {
			t.Fatal("duplicate name")
		}
		used[node.Name] = true
	}
}

func TestLostReplyAndBoundIdempotence(t *testing.T) {
	c, store := setup(t)
	store.loseReply = true
	op := Operation{ID: "once", Actor: "admin", Kind: "mkdir", ParentID: "root", Name: "漫画"}
	if _, err := c.Apply(context.Background(), op); err == nil {
		t.Fatal("must report lost response")
	}
	result, err := New(store).Apply(context.Background(), op)
	if err != nil || result.Revision != 1 {
		t.Fatal(result, err)
	}
	repeated, err := New(store).Apply(context.Background(), op)
	if err != nil || repeated != result {
		t.Fatal(repeated, err)
	}
	op.Name = "不同参数"
	if _, err := New(store).Apply(context.Background(), op); err == nil {
		t.Fatal("reused ID changed arguments")
	}
	snapshot, err := New(store).Current(context.Background(), 1)
	if err != nil || len(snapshot.Nodes) != 2 {
		t.Fatal(snapshot, err)
	}
}

func TestDeleteRejectsLateUploadEvenAfterSameNameRecreated(t *testing.T) {
	c, _ := setup(t)
	old := apply(t, c, Operation{ID: "mkdir-old", Kind: "mkdir", ParentID: "root", Name: "目录"})
	apply(t, c, Operation{ID: "delete", Kind: "delete", NodeID: old.NodeID})
	newDir := apply(t, c, Operation{ID: "mkdir-new", Kind: "mkdir", ParentID: "root", Name: "目录"})
	if newDir.NodeID == old.NodeID {
		t.Fatal("directory incarnation reused")
	}
	op := Operation{ID: "late-upload", Actor: "admin", Kind: "publish", ParentID: old.NodeID, Name: "1.jpg", ObjectKey: "immutable/blob1", Size: 9, Hash: "qiniu-etag"}
	late := apply(t, c, op)
	if late.Error != "parent_not_found" {
		t.Fatal(late)
	}
	if again := apply(t, c, op); again != late {
		t.Fatal("semantic failure was not idempotent")
	}
	snapshot, _ := c.Current(context.Background(), late.Revision)
	if len(snapshot.Children(newDir.NodeID)) != 0 {
		t.Fatal("late upload resurrected or entered new directory")
	}
}

func TestUploadFollowsRenamedParentByID(t *testing.T) {
	c, _ := setup(t)
	dir := apply(t, c, Operation{ID: "mkdir", Kind: "mkdir", ParentID: "root", Name: "旧名"})
	apply(t, c, Operation{ID: "rename", Kind: "move", NodeID: dir.NodeID, ParentID: "root", Name: "新名"})
	file := apply(t, c, Operation{ID: "upload", Kind: "publish", ParentID: dir.NodeID, Name: "页 #100%.jpg", ObjectKey: "objects/unique", Size: 1, Hash: "hash"})
	if file.Path != "新名/页 #100%.jpg" {
		t.Fatal(file)
	}
}

func TestSubtreeCopyDeleteAndCycle(t *testing.T) {
	c, _ := setup(t)
	dir := apply(t, c, Operation{ID: "dir", Kind: "mkdir", ParentID: "root", Name: "原作"})
	sub := apply(t, c, Operation{ID: "sub", Kind: "mkdir", ParentID: dir.NodeID, Name: "第一话"})
	file := apply(t, c, Operation{ID: "file", Kind: "publish", ParentID: sub.NodeID, Name: "1.jpg", ObjectKey: "immutable/image", Size: 10, Hash: "etag"})
	cycle := apply(t, c, Operation{ID: "cycle", Kind: "move", NodeID: dir.NodeID, ParentID: sub.NodeID, Name: "loop"})
	if cycle.Error != "directory_cycle" {
		t.Fatal(cycle)
	}
	copy := apply(t, c, Operation{ID: "copy", Kind: "copy", NodeID: dir.NodeID, ParentID: "root", Name: "原作"})
	if copy.Path != "原作(1)" {
		t.Fatal(copy)
	}
	deleted := apply(t, c, Operation{ID: "delete", Kind: "delete", NodeID: dir.NodeID})
	snapshot, err := c.Current(context.Background(), deleted.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Nodes[file.NodeID]; exists {
		t.Fatal("source subtree remains")
	}
	if len(snapshot.Nodes) != 4 {
		t.Fatal("copy subtree lost nodes")
	}
	for _, node := range snapshot.Nodes {
		if node.Kind == "file" && (node.ObjectKey != "immutable/image" || node.Size != 10 || node.Hash != "etag") {
			t.Fatal(node)
		}
	}
	// Old snapshots remain complete; physical blobs are not reclaimed by delete.
	old, err := c.read(context.Background(), file.Revision)
	if err != nil || len(old.Nodes) != 4 {
		t.Fatal(old, err)
	}
}

func TestReadErrorsNeverInitializeOrOverwrite(t *testing.T) {
	c, store := setup(t)
	store.readError = errors.New("network unavailable")
	if _, err := c.Current(context.Background(), 0); err == nil {
		t.Fatal("network error treated as empty catalog")
	}
	if _, err := c.Apply(context.Background(), Operation{ID: "x", Actor: "admin", Kind: "mkdir", ParentID: "root", Name: "x"}); err == nil {
		t.Fatal("network error accepted")
	}
	if len(store.data) != 1 {
		t.Fatal("unexpected write")
	}
	store.readError = nil
	if _, err := c.Current(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Fatal("must not fall behind required revision", err)
	}
	store.data[0] = []byte(`{"format":999}`)
	if err := c.Init(context.Background()); err == nil {
		t.Fatal("invalid existing seed overwritten")
	}
}

func TestInvalidAndRootActions(t *testing.T) {
	c, _ := setup(t)
	for index, op := range []Operation{
		{Kind: "delete", NodeID: "root"}, {Kind: "move", NodeID: "root", ParentID: "root", Name: "x"},
		{Kind: "mkdir", ParentID: "root", Name: "../escape"}, {Kind: "mkdir", ParentID: "root", Name: "."},
		{Kind: "publish", ParentID: "root", Name: "x", ObjectKey: "blob", Size: -1, Hash: "etag"},
		{Kind: "mkdir", ParentID: "missing", Name: "x"}, {Kind: "other"},
	} {
		op.ID = fmt.Sprint(index)
		r := apply(t, c, op)
		if r.Error == "" {
			t.Fatal(op, r)
		}
	}
	snapshot, _ := c.Current(context.Background(), 0)
	if len(snapshot.Nodes) != 1 {
		t.Fatal("invalid request mutated tree")
	}
	data, _ := json.Marshal(snapshot)
	if len(data) == 0 {
		t.Fatal("snapshot cannot be serialized")
	}
}

func TestSeedRejectsIncompleteAndDifferentImports(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store)
	nodes := map[string]Node{"root": {ID: "root", Kind: "dir"}, "f": {ID: "f", Parent: "root", Name: "原档 #%.jpg", Kind: "file", ObjectKey: "objects/hash", Hash: "etag", Size: 123}}
	if e := c.Seed(ctx, nodes); e != nil {
		t.Fatal(e)
	}
	if e := c.Seed(ctx, nodes); e != nil {
		t.Fatal(e)
	}
	nodes["f"] = Node{ID: "f", Parent: "missing", Name: "image", Kind: "file", ObjectKey: "object", Hash: "etag"}
	if e := New(newMemoryStore()).Seed(ctx, nodes); e == nil {
		t.Fatal("accepted missing parent")
	}
	delete(nodes, "f")
	if e := c.Seed(ctx, nodes); e == nil {
		t.Fatal("overwrote seed")
	}
}
func TestLateJobCannotRecreateDeletedOrReplacedParent(t *testing.T) {
	c, _ := setup(t)
	p := apply(t, c, Operation{ID: "parent", Kind: "mkdir", ParentID: "root", Name: "book"})
	job := Job{ID: "late", Actor: "admin", Kind: "upload", ParentID: p.NodeID, Name: "1.jpg", ObjectKey: "objects/late", Size: 10, Hash: "expected"}
	start := apply(t, c, Operation{ID: "start", Kind: "job_start", Job: &job})
	if start.Error != "" {
		t.Fatal(start)
	}
	apply(t, c, Operation{ID: "delete", Kind: "delete", NodeID: p.NodeID})
	replacement := apply(t, c, Operation{ID: "replacement", Kind: "mkdir", ParentID: "root", Name: "book"})
	result := apply(t, c, Operation{ID: "finish", Kind: "job_finish", JobID: job.ID, ObjectKey: job.ObjectKey, Size: 10, Hash: "expected"})
	if result.Error != "parent_not_found" {
		t.Fatal(result)
	}
	snapshot, e := c.Current(context.Background(), 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Children(replacement.NodeID)) != 0 || snapshot.Jobs[job.ID].State != "failed" {
		t.Fatal("late publish recreated parent")
	}
}
func TestCancelAndFinishHaveOneWinner(t *testing.T) {
	for range 30 {
		c, store := setup(t)
		job := Job{ID: "race", Actor: "admin", Kind: "upload", ParentID: "root", Name: "file", ObjectKey: "objects/race", Size: 0, Hash: "empty"}
		apply(t, c, Operation{ID: "start", Kind: "job_start", Job: &job})
		var wg sync.WaitGroup
		wg.Go(func() { apply(t, New(store), Operation{ID: "cancel", Kind: "job_cancel", JobID: job.ID}) })
		wg.Go(func() {
			apply(t, New(store), Operation{ID: "finish", Kind: "job_finish", JobID: job.ID, ObjectKey: job.ObjectKey, Hash: "empty"})
		})
		wg.Wait()
		s, e := c.Current(context.Background(), 0)
		if e != nil {
			t.Fatal(e)
		}
		state := s.Jobs[job.ID].State
		if (state == "published" && len(s.Nodes) != 2) || (state == "cancelled" && len(s.Nodes) != 1) || (state != "cancelled" && state != "published") {
			t.Fatalf("inconsistent winner: %s %d", state, len(s.Nodes))
		}
	}
}
func TestEmptyUploadSizeAndActorBound(t *testing.T) {
	c, _ := setup(t)
	job := Job{ID: "empty", Actor: "admin", Kind: "upload", ParentID: "root", Name: "empty", ObjectKey: "objects/empty", Hash: "hash"}
	apply(t, c, Operation{ID: "start", Kind: "job_start", Job: &job})
	r := apply(t, c, Operation{ID: "stolen", Actor: "other", Kind: "job_cancel", JobID: job.ID})
	if r.Error != "job_not_found" {
		t.Fatal(r)
	}
	r = apply(t, c, Operation{ID: "bad-size", Kind: "job_finish", JobID: job.ID, ObjectKey: job.ObjectKey, Hash: "hash", Size: 1})
	if r.Error != "object_mismatch" {
		t.Fatal(r)
	}
}
