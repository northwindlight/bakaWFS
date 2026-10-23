// catalog-probe validates immutable commits against a real Kodo namespace using
// synthetic data only. It deletes only its own randomly generated test prefix.
package main

import (
	"bakaWFS/internal/catalog"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/storage"
)

type loseReplyStore struct {
	catalog.Store
	lost atomic.Bool
}

func (s *loseReplyStore) Create(ctx context.Context, revision uint64, data []byte) error {
	err := s.Store.Create(ctx, revision, data)
	if err == nil && revision > 0 && s.lost.CompareAndSwap(false, true) {
		return errors.New("injected reply loss after successful commit")
	}
	return err
}

func main() {
	bucket := flag.String("bucket", "", "explicit test bucket (only synthetic data is written)")
	credentials := flag.String("credentials-file", "", "existing QINIU_AK/QINIU_SK env file; never printed")
	output := flag.String("output", "", "JSON validation report")
	workers := flag.Int("workers", 4, "independent catalog clients, 2..8")
	yes := flag.Bool("yes", false, "allow isolated cloud writes and cleanup")
	flag.Parse()
	if *bucket == "" || *credentials == "" || *output == "" || !*yes || *workers < 2 || *workers > 8 {
		fmt.Fprintln(os.Stderr, "require --bucket, --credentials-file, --output, --yes and 2..8 workers")
		os.Exit(2)
	}
	if err := run(*bucket, *credentials, *output, *workers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(bucket, credentials, output string, workers int) (runErr error) {
	f, err := os.Open(credentials)
	if err != nil {
		return fmt.Errorf("cannot open credentials file")
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "="); ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	if scanner.Err() != nil || values["QINIU_AK"] == "" || values["QINIU_SK"] == "" {
		return fmt.Errorf("credentials file is incomplete")
	}
	prefix := "__bakawfs_probe__/catalog-" + rand.Text() + "/"
	manager := storage.NewBucketManager(auth.New(values["QINIU_AK"], values["QINIU_SK"]), &storage.Config{UseHTTPS: true})
	report := map[string]any{"independent_clients": workers, "prefix": prefix, "synthetic_data_only": true}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleted := 0
		// List only this run's unique prefix, and validate every key before delete.
		listed, _, err := manager.ListFilesWithContext(cleanupCtx, bucket, storage.ListInputOptionsPrefix(prefix), storage.ListInputOptionsLimit(1000))
		if err == nil {
			for _, item := range listed.Items {
				if strings.HasPrefix(item.Key, prefix) && manager.Delete(bucket, item.Key) == nil {
					deleted++
				} else {
					err = fmt.Errorf("cleanup failed")
				}
			}
		}
		report["cleanup_deleted"] = deleted
		report["cleanup_ok"] = err == nil
		if runErr == nil && deleted != workers+4 {
			err = fmt.Errorf("cleanup count differs from committed revisions")
			report["cleanup_ok"] = false
		}
		if err != nil && runErr == nil {
			runErr = fmt.Errorf("test namespace cleanup incomplete")
		}
		if runErr != nil {
			report["passed"] = false
			report["error"] = runErr.Error()
		} else {
			report["passed"] = true
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if writeErr := os.WriteFile(output, append(data, '\n'), 0600); writeErr != nil && runErr == nil {
			runErr = fmt.Errorf("cannot save report")
		}
		fmt.Println(string(data))
	}()
	newStore := func() (*catalog.KodoStore, error) {
		return catalog.NewKodoStore(values["QINIU_AK"], values["QINIU_SK"], bucket, prefix)
	}
	store, err := newStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := catalog.New(store)
	if err := c.Init(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	failures := make(chan error, workers)
	for index := range workers {
		wg.Go(func() {
			s, err := newStore()
			if err != nil {
				failures <- err
				return
			}
			r, err := catalog.New(s).Apply(ctx, catalog.Operation{Actor: "probe", ID: fmt.Sprintf("client-%d", index), Kind: "mkdir", ParentID: "root", Name: "concurrent"})
			if err == nil && r.Error != "" {
				err = fmt.Errorf("semantic rejection: %s", r.Error)
			}
			if err != nil {
				failures <- err
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	snapshot, err := c.Current(ctx, uint64(workers))
	if err != nil {
		return err
	}
	if len(snapshot.Nodes) != workers+1 || len(snapshot.Receipts) != workers {
		return fmt.Errorf("concurrent update was lost")
	}
	report["concurrent_nodes"] = len(snapshot.Nodes)
	report["concurrent_revision"] = snapshot.Revision
	lossy := &loseReplyStore{Store: store}
	op := catalog.Operation{Actor: "probe", ID: "lost-reply", Kind: "mkdir", ParentID: "root", Name: "deleted-parent"}
	if _, err := catalog.New(lossy).Apply(ctx, op); err == nil {
		return fmt.Errorf("reply-loss injection did not occur")
	}
	parent, err := catalog.New(store).Apply(ctx, op)
	if err != nil {
		return err
	}
	if _, err := catalog.New(store).Apply(ctx, catalog.Operation{Actor: "probe", ID: "delete-parent", Kind: "delete", NodeID: parent.NodeID}); err != nil {
		return err
	}
	lateOp := catalog.Operation{Actor: "probe", ID: "late-upload", Kind: "publish", ParentID: parent.NodeID, Name: "page.jpg", ObjectKey: "synthetic/no-real-blob", Hash: "synthetic", Size: 1}
	late, err := catalog.New(store).Apply(ctx, lateOp)
	if err != nil || late.Error != "parent_not_found" {
		return fmt.Errorf("late upload was not rejected")
	}
	again, err := catalog.New(store).Apply(ctx, lateOp)
	if err != nil || again != late {
		return fmt.Errorf("rejected operation lost its receipt")
	}
	report["lost_reply_recovered"] = true
	report["late_upload_rejected"] = true
	report["semantic_rejection_idempotent"] = true
	final, err := catalog.New(store).Current(ctx, late.Revision)
	if err != nil {
		return err
	}
	if len(final.Nodes) != workers+1 {
		return fmt.Errorf("late upload resurrected the directory")
	}
	report["final_revision"] = final.Revision
	report["final_nodes"] = len(final.Nodes)
	return nil
}
