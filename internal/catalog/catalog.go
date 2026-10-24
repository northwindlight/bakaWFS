// Package catalog implements immutable directory snapshots. Snapshot N can only
// be created after N-1 exists, using a store's atomic create-if-absent primitive.
// No mutable head pointer, process lock, or database participates in a commit.
package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound = errors.New("snapshot not found")
	ErrExists   = errors.New("snapshot already exists")
	ErrCapacity = errors.New("catalog capacity reached")
)

// Store must atomically publish complete bytes, reject different bytes already
// occupying the same revision, and distinguish missing data from other errors.
// Revisions cannot be overwritten or removed. Compaction requires a later,
// explicitly designed protocol; deleting old revisions breaks this contract.
type Store interface {
	Read(context.Context, uint64) ([]byte, error)
	Create(context.Context, uint64, []byte) error
}

type Node struct {
	ID        string `json:"id"`
	Parent    string `json:"parent,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	ObjectKey string `json:"object_key,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Hash      string `json:"hash,omitempty"` // Provider object hash, not image/xxhash.
}

type Result struct {
	Revision uint64 `json:"revision"`
	NodeID   string `json:"node_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Receipt struct {
	Fingerprint string `json:"fingerprint"`
	RequestHash string `json:"request_hash,omitempty"`
	Result      Result `json:"result"`
}

type Snapshot struct {
	Format   int                `json:"format"`
	Revision uint64             `json:"revision"`
	Nodes    map[string]Node    `json:"nodes"`
	Receipts map[string]Receipt `json:"receipts"`
	Jobs     map[string]Job     `json:"jobs,omitempty"`
}

type Job struct {
	ID          string `json:"id"`
	Actor       string `json:"actor"`
	Kind        string `json:"kind"`
	ParentID    string `json:"parent_id"`
	Name        string `json:"name"`
	ObjectKey   string `json:"object_key"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	RequestHash string `json:"request_hash"`
	UploadID    string `json:"upload_id,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	StageKey    string `json:"stage_key,omitempty"`
	State       string `json:"state"`
	Created     int64  `json:"created"`
	Expires     int64  `json:"expires"`
	NodeID      string `json:"node_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Operation struct {
	ID          string `json:"id"`
	RequestHash string `json:"request_hash,omitempty"`
	Actor       string `json:"actor"`
	Kind        string `json:"kind"` // mkdir, publish, move, copy, delete
	NodeID      string `json:"node_id,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
	Name        string `json:"name,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Job         *Job   `json:"job,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	UploadID    string `json:"upload_id,omitempty"`
}

type Catalog struct {
	store Store
	floor atomic.Uint64 // Hint only; the store is the authority.
}

func New(store Store) *Catalog { return &Catalog{store: store} }

// Seed atomically publishes a complete imported library as revision zero.
func (c *Catalog) Seed(ctx context.Context, nodes map[string]Node) error {
	initial := Snapshot{Format: 1, Nodes: nodes, Receipts: map[string]Receipt{}}
	if err := initial.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(initial)
	if err != nil {
		return err
	}
	if err := c.store.Create(ctx, 0, data); errors.Is(err, ErrExists) {
		actual, readErr := c.store.Read(ctx, 0)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(actual, data) {
			return fmt.Errorf("existing catalog seed differs; refusing to replace library")
		}
		return nil
	} else {
		return err
	}
}

func (s *Snapshot) Validate() error {
	root, ok := s.Nodes["root"]
	if !ok || root.ID != "root" || root.Parent != "" || root.Kind != "dir" {
		return fmt.Errorf("invalid catalog root")
	}
	names := map[string]bool{}
	for id, n := range s.Nodes {
		if n.ID != id || (n.Kind != "dir" && n.Kind != "file") {
			return fmt.Errorf("invalid catalog node")
		}
		if id == "root" {
			continue
		}
		parent, ok := s.Nodes[n.Parent]
		if !ok || parent.Kind != "dir" {
			return fmt.Errorf("missing parent in imported catalog")
		}
		if !utf8.ValidString(n.Name) || n.Name == "" || n.Name == "." || n.Name == ".." || strings.ContainsAny(n.Name, "/\\\x00") {
			return fmt.Errorf("invalid imported filename")
		}
		key := n.Parent + "\x00" + n.Name
		if names[key] {
			return fmt.Errorf("duplicate imported name")
		}
		names[key] = true
		if n.Kind == "file" && (n.ObjectKey == "" || n.Hash == "" || n.Size < 0) {
			return fmt.Errorf("invalid imported object")
		}
		visited := map[string]bool{}
		for p := id; p != "root"; p = s.Nodes[p].Parent {
			if p == "" || visited[p] {
				return fmt.Errorf("cycle in imported catalog")
			}
			visited[p] = true
		}
	}
	return nil
}

func (c *Catalog) Init(ctx context.Context) error {
	initial := Snapshot{Format: 1, Nodes: map[string]Node{"root": {ID: "root", Kind: "dir"}}, Receipts: map[string]Receipt{}}
	data, _ := json.Marshal(initial)
	err := c.store.Create(ctx, 0, data)
	if errors.Is(err, ErrExists) {
		_, err = c.read(ctx, 0)
	}
	return err
}

func (c *Catalog) read(ctx context.Context, revision uint64) (*Snapshot, error) {
	data, err := c.store.Read(ctx, revision)
	if err != nil {
		return nil, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid snapshot JSON at revision %d", revision)
	}
	if snapshot.Format != 1 || snapshot.Revision != revision || snapshot.Nodes == nil || snapshot.Receipts == nil || snapshot.Nodes["root"].Kind != "dir" {
		return nil, fmt.Errorf("invalid snapshot at revision %d", revision)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("invalid catalog graph at revision %d", revision)
	}
	return &snapshot, nil
}

func (c *Catalog) remember(revision uint64) {
	for old := c.floor.Load(); revision > old; old = c.floor.Load() {
		if c.floor.CompareAndSwap(old, revision) {
			break
		}
	}
}

// Current finds the highest contiguous revision by exact object reads, never
// bucket listing or a cached head. minRevision prevents a client reading behind
// a revision it has already observed. Provider visibility/availability still
// bounds freshness; a missing required revision fails instead of falling back.
func (c *Catalog) Current(ctx context.Context, minRevision uint64) (*Snapshot, error) {
	probe := c.read
	if index, ok := c.store.(interface {
		Exists(context.Context, uint64) (bool, error)
	}); ok {
		probe = func(ctx context.Context, revision uint64) (*Snapshot, error) {
			exists, e := index.Exists(ctx, revision)
			if e == nil && !exists {
				return nil, ErrNotFound
			}
			return nil, e
		}
	}
	low := max(c.floor.Load(), minRevision)
	latest, err := probe(ctx, low)
	if err != nil {
		return nil, err
	}
	step := uint64(1)
	var high uint64
	for {
		if low > math.MaxUint64-step {
			return nil, fmt.Errorf("revision space exhausted")
		}
		high = low + step
		next, err := probe(ctx, high)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		low, latest = high, next
		if step <= math.MaxUint64/2 {
			step *= 2
		}
	}
	for high-low > 1 {
		middle := low + (high-low)/2
		next, err := probe(ctx, middle)
		if errors.Is(err, ErrNotFound) {
			high = middle
			continue
		}
		if err != nil {
			return nil, err
		}
		low, latest = middle, next
	}
	if latest == nil {
		latest, err = c.read(ctx, low)
		if err != nil {
			return nil, err
		}
	}
	c.remember(low)
	return latest, nil
}

// Apply persists successes and semantic rejections, binding an operation ID to
// its actor and exact arguments. An ambiguous write error is returned to the
// caller: retrying the SAME operation recovers its receipt after a lost reply.
func (c *Catalog) Apply(ctx context.Context, op Operation) (Result, error) {
	if op.ID == "" || op.Actor == "" {
		return Result{}, fmt.Errorf("operation ID and actor are required")
	}
	encoded, _ := json.Marshal(op)
	fingerprint := sha256.Sum256(encoded)
	key := op.Actor + "\x00" + op.ID
	for attempt := 0; attempt < 128; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		snapshot, err := c.Current(ctx, 0)
		if err != nil {
			return Result{}, err
		}
		if receipt, exists := snapshot.Receipts[key]; exists {
			if receipt.Fingerprint != hex.EncodeToString(fingerprint[:]) {
				return Result{}, fmt.Errorf("operation ID reused with different arguments")
			}
			return receipt.Result, nil
		}
		if snapshot.Revision == math.MaxUint64 {
			return Result{}, fmt.Errorf("revision space exhausted")
		}
		revision := snapshot.Revision + 1
		result := mutate(snapshot, op)
		result.Revision = revision
		snapshot.Revision = revision
		snapshot.Receipts[key] = Receipt{Fingerprint: hex.EncodeToString(fingerprint[:]), RequestHash: op.RequestHash, Result: result}
		data, err := json.Marshal(snapshot)
		if err != nil {
			return Result{}, err
		}
		if len(data) > MaxSnapshotBytes {
			return Result{}, ErrCapacity
		}
		err = c.store.Create(ctx, revision, data)
		if errors.Is(err, ErrExists) {
			// Reading the contested slot avoids repeatedly trusting an out-of-date
			// highest-revision search during provider visibility delays.
			if _, readErr := c.read(ctx, revision); readErr != nil {
				return Result{}, readErr
			}
			c.remember(revision)
			continue
		}
		if err != nil {
			return Result{}, err
		}
		c.remember(revision)
		return result, nil
	}
	return Result{}, fmt.Errorf("too much concurrent catalog contention; retry the same operation")
}

func nodeID(op Operation, source string) string {
	digest := sha256.Sum256([]byte(op.Actor + "\x00" + op.ID + "\x00" + source))
	return hex.EncodeToString(digest[:16])
}

func mutate(s *Snapshot, op Operation) Result {
	if strings.HasPrefix(op.Kind, "job_") {
		return mutateJob(s, op)
	}
	bad := func(code string) Result { return Result{Error: code} }
	if op.Kind != "mkdir" && op.Kind != "publish" && op.Kind != "move" && op.Kind != "copy" && op.Kind != "delete" {
		return bad("unknown_operation")
	}
	if op.Kind != "delete" {
		if !utf8.ValidString(op.Name) || op.Name == "" || op.Name == "." || op.Name == ".." || strings.ContainsAny(op.Name, "/\\\x00") {
			return bad("invalid_name")
		}
		parent, exists := s.Nodes[op.ParentID]
		if !exists || parent.Kind != "dir" {
			return bad("parent_not_found")
		}
	}
	var source Node
	if op.Kind == "move" || op.Kind == "copy" || op.Kind == "delete" {
		var exists bool
		source, exists = s.Nodes[op.NodeID]
		if !exists {
			return bad("node_not_found")
		}
		if source.ID == "root" {
			return bad("root_operation_forbidden")
		}
		if op.Kind != "delete" {
			for parent := op.ParentID; parent != ""; parent = s.Nodes[parent].Parent {
				if parent == source.ID {
					return bad("directory_cycle")
				}
			}
		}
	}
	if op.Kind == "publish" && (op.ObjectKey == "" || op.Hash == "" || op.Size < 0) {
		return bad("invalid_object")
	}
	if op.Kind == "delete" {
		for id := range descendants(s, source.ID) {
			delete(s.Nodes, id)
		}
		return Result{NodeID: source.ID}
	}
	name := resolveName(s, op.ParentID, op.Name, op.NodeID, op.Kind == "move")
	var id string
	switch op.Kind {
	case "mkdir", "publish":
		id = nodeID(op, "new")
		node := Node{ID: id, Parent: op.ParentID, Name: name, Kind: "dir"}
		if op.Kind == "publish" {
			node.Kind, node.ObjectKey, node.Size, node.Hash = "file", op.ObjectKey, op.Size, op.Hash
		}
		s.Nodes[id] = node
	case "move":
		id = source.ID
		source.Parent, source.Name = op.ParentID, name
		s.Nodes[id] = source
	case "copy":
		tree := descendants(s, source.ID)
		for original, node := range tree {
			node.ID = nodeID(op, original)
			if original == source.ID {
				node.Parent, node.Name = op.ParentID, name
			} else {
				node.Parent = nodeID(op, node.Parent)
			}
			s.Nodes[node.ID] = node
		}
		id = nodeID(op, source.ID)
	}
	return Result{NodeID: id, Path: s.Path(id)}
}

func mutateJob(s *Snapshot, op Operation) Result {
	bad := func(code string) Result { return Result{Error: code} }
	if s.Jobs == nil {
		s.Jobs = map[string]Job{}
	}
	if op.Kind == "job_start" {
		if op.Job == nil || op.Job.ID == "" || op.Job.Actor != op.Actor || op.Job.ObjectKey == "" || op.Job.Size < 0 {
			return bad("invalid_job")
		}
		if _, ok := s.Jobs[op.Job.ID]; ok {
			return bad("job_already_exists")
		}
		parent, ok := s.Nodes[op.Job.ParentID]
		if !ok || parent.Kind != "dir" {
			return bad("parent_not_found")
		}
		job := *op.Job
		job.State = "pending"
		job.Created = time.Now().Unix()
		if job.Expires == 0 {
			job.Expires = job.Created + 86400
		}
		s.Jobs[job.ID] = job
		return Result{NodeID: job.ID}
	}
	job, ok := s.Jobs[op.JobID]
	if !ok || job.Actor != op.Actor {
		return bad("job_not_found")
	}
	switch op.Kind {
	case "job_upload_id":
		if job.State != "pending" {
			return bad("job_not_pending")
		}
		if job.UploadID != "" {
			return bad("upload_already_initialized")
		}
		if op.UploadID == "" {
			return bad("invalid_upload_id")
		}
		job.UploadID = op.UploadID
	case "job_cancel":
		if job.State == "published" {
			return bad("already_committed")
		}
		job.State = "cancelled"
	case "job_fail":
		if job.State == "published" {
			return bad("already_committed")
		}
		if job.State != "cancelled" {
			job.State = "failed"
			job.Error = "transfer_failed"
		}
	case "job_finish":
		if job.State != "pending" {
			return bad("job_not_pending")
		}
		if time.Now().Unix() > job.Expires {
			job.State = "expired"
			job.Error = "job_expired"
			s.Jobs[job.ID] = job
			return bad(job.Error)
		}
		if op.ObjectKey != job.ObjectKey || op.Size < 0 || ((job.Kind == "upload" || job.Size > 0) && job.Size != op.Size) || (job.Hash != "" && job.Hash != op.Hash) {
			return bad("object_mismatch")
		}
		result := mutate(s, Operation{ID: op.ID, Actor: op.Actor, Kind: "publish", ParentID: job.ParentID, Name: job.Name, ObjectKey: op.ObjectKey, Size: op.Size, Hash: op.Hash})
		if result.Error != "" {
			job.State = "failed"
			job.Error = result.Error
			s.Jobs[job.ID] = job
			return result
		}
		job.State = "published"
		job.NodeID = result.NodeID
		job.Size = op.Size
		job.Hash = op.Hash
		s.Jobs[job.ID] = job
		return result
	default:
		return bad("unknown_job_operation")
	}
	s.Jobs[job.ID] = job
	return Result{NodeID: job.ID}
}

func descendants(s *Snapshot, root string) map[string]Node {
	children := make(map[string][]Node)
	for _, node := range s.Nodes {
		children[node.Parent] = append(children[node.Parent], node)
	}
	found := make(map[string]Node)
	queue := []Node{s.Nodes[root]}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		found[node.ID] = node
		queue = append(queue, children[node.ID]...)
	}
	return found
}

func resolveName(s *Snapshot, parent, name, source string, moving bool) string {
	used := make(map[string]bool)
	for id, node := range s.Nodes {
		if node.Parent == parent && (!moving || id != source) {
			used[node.Name] = true
		}
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 1; used[name]; index++ {
		name = fmt.Sprintf("%s(%d)%s", base, index, ext)
	}
	return name
}

func (s *Snapshot) Path(id string) string {
	var names []string
	for id != "root" && id != "" {
		node, exists := s.Nodes[id]
		if !exists {
			return ""
		}
		names = append(names, node.Name)
		id = node.Parent
	}
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, "/")
}

func (s *Snapshot) Children(parent string) []Node {
	nodes := []Node{}
	for _, node := range s.Nodes {
		if node.Parent == parent {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}
