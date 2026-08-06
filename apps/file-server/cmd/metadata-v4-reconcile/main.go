package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func main() {
	var bucket, prefix string
	var shards int
	var timeout time.Duration
	var yes bool
	flag.StringVar(&bucket, "metadata-gcs-bucket", "", "GCS metadata bucket")
	flag.StringVar(&prefix, "metadata-prefix", "_vfs-link-v4", "v4 metadata prefix")
	flag.IntVar(&shards, "metadata-shard-count", 64, "immutable v4 shard count")
	flag.DurationVar(&timeout, "timeout", time.Hour, "reconciliation timeout")
	flag.BoolVar(&yes, "yes", false, "confirm the maintenance-only stats repair")
	flag.Parse()
	if bucket == "" || prefix != "_vfs-link-v4" || !yes {
		fmt.Fprintln(os.Stderr, "metadata-v4-reconcile: require --metadata-gcs-bucket, --metadata-prefix=_vfs-link-v4, and --yes")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	store, err := db.NewTreeGCSV4(ctx, bucket, prefix, db.TreeV4Options{ShardCount: shards, ReducerInterval: 2 * time.Second, MutationMode: "scoped"})
	if err == nil {
		defer store.Close()
		var result db.TreeDerivedStatsReconcileResult
		result, err = db.ReconcileTreeV4DerivedStats(ctx, store)
		if err == nil {
			fmt.Printf("reconciled records=%d before=%+v after=%+v\n", result.Records, result.Before, result.After)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "metadata-v4-reconcile:", err)
		os.Exit(1)
	}
}
