package dbstore

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/keegancsmith/sqlf"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/globals"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/gitserver"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/shared"
	"github.com/sourcegraph/sourcegraph/internal/database/basestore"
	"github.com/sourcegraph/sourcegraph/internal/database/dbtesting"
	"github.com/sourcegraph/sourcegraph/internal/timeutil"
	"github.com/sourcegraph/sourcegraph/schema"
)

func TestGetUploadByID(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)
	ctx := context.Background()

	// Upload does not exist initially
	if _, exists, err := store.GetUploadByID(ctx, 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if exists {
		t.Fatal("unexpected record")
	}

	uploadedAt := time.Unix(1587396557, 0).UTC()
	startedAt := uploadedAt.Add(time.Minute)
	expected := Upload{
		ID:             1,
		Commit:         makeCommit(1),
		Root:           "sub/",
		VisibleAtTip:   true,
		UploadedAt:     uploadedAt,
		State:          "processing",
		FailureMessage: nil,
		StartedAt:      &startedAt,
		FinishedAt:     nil,
		RepositoryID:   123,
		RepositoryName: "n-123",
		Indexer:        "lsif-go",
		NumParts:       1,
		UploadedParts:  []int{},
		Rank:           nil,
	}

	insertUploads(t, db, expected)
	insertVisibleAtTip(t, db, 123, 1)

	if upload, exists, err := store.GetUploadByID(ctx, 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else if diff := cmp.Diff(expected, upload); diff != "" {
		t.Errorf("unexpected upload (-want +got):\n%s", diff)
	}

	t.Run("enforce repository permissions", func(t *testing.T) {
		// Enable permissions user mapping forces checking repository permissions
		// against permissions tables in the database, which should effectively block
		// all access because permissions tables are empty.
		before := globals.PermissionsUserMapping()
		globals.SetPermissionsUserMapping(&schema.PermissionsUserMapping{Enabled: true})
		defer globals.SetPermissionsUserMapping(before)

		_, exists, err := store.GetUploadByID(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("exists: want false but got %v", exists)
		}
	})
}

func TestGetUploadByIDDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	// Upload does not exist initially
	if _, exists, err := store.GetUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if exists {
		t.Fatal("unexpected record")
	}

	uploadedAt := time.Unix(1587396557, 0).UTC()
	startedAt := uploadedAt.Add(time.Minute)
	expected := Upload{
		ID:             1,
		Commit:         makeCommit(1),
		Root:           "sub/",
		VisibleAtTip:   true,
		UploadedAt:     uploadedAt,
		State:          "deleted",
		FailureMessage: nil,
		StartedAt:      &startedAt,
		FinishedAt:     nil,
		RepositoryID:   123,
		RepositoryName: "n-123",
		Indexer:        "lsif-go",
		NumParts:       1,
		UploadedParts:  []int{},
		Rank:           nil,
	}

	insertUploads(t, db, expected)

	// Should still not be queryable
	if _, exists, err := store.GetUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if exists {
		t.Fatal("unexpected record")
	}
}

func TestGetQueuedUploadRank(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(+time.Minute * 6)
	t3 := t1.Add(+time.Minute * 3)
	t4 := t1.Add(+time.Minute * 1)
	t5 := t1.Add(+time.Minute * 4)
	t6 := t1.Add(+time.Minute * 2)
	t7 := t1.Add(+time.Minute * 5)

	insertUploads(t, db,
		Upload{ID: 1, UploadedAt: t1, State: "queued"},
		Upload{ID: 2, UploadedAt: t2, State: "queued"},
		Upload{ID: 3, UploadedAt: t3, State: "queued"},
		Upload{ID: 4, UploadedAt: t4, State: "queued"},
		Upload{ID: 5, UploadedAt: t5, State: "queued"},
		Upload{ID: 6, UploadedAt: t6, State: "processing"},
		Upload{ID: 7, UploadedAt: t1, State: "queued", ProcessAfter: &t7},
	)

	if upload, _, _ := store.GetUploadByID(context.Background(), 1); upload.Rank == nil || *upload.Rank != 1 {
		t.Errorf("unexpected rank. want=%d have=%s", 1, printableRank{upload.Rank})
	}
	if upload, _, _ := store.GetUploadByID(context.Background(), 2); upload.Rank == nil || *upload.Rank != 6 {
		t.Errorf("unexpected rank. want=%d have=%s", 5, printableRank{upload.Rank})
	}
	if upload, _, _ := store.GetUploadByID(context.Background(), 3); upload.Rank == nil || *upload.Rank != 3 {
		t.Errorf("unexpected rank. want=%d have=%s", 3, printableRank{upload.Rank})
	}
	if upload, _, _ := store.GetUploadByID(context.Background(), 4); upload.Rank == nil || *upload.Rank != 2 {
		t.Errorf("unexpected rank. want=%d have=%s", 2, printableRank{upload.Rank})
	}
	if upload, _, _ := store.GetUploadByID(context.Background(), 5); upload.Rank == nil || *upload.Rank != 4 {
		t.Errorf("unexpected rank. want=%d have=%s", 4, printableRank{upload.Rank})
	}

	// Only considers queued uploads to determine rank
	if upload, _, _ := store.GetUploadByID(context.Background(), 6); upload.Rank != nil {
		t.Errorf("unexpected rank. want=%s have=%s", "nil", printableRank{upload.Rank})
	}

	// Process after takes priority over upload time
	if upload, _, _ := store.GetUploadByID(context.Background(), 7); upload.Rank == nil || *upload.Rank != 5 {
		t.Errorf("unexpected rank. want=%d have=%s", 4, printableRank{upload.Rank})
	}
}

func TestGetUploadsByIDs(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)
	ctx := context.Background()

	insertUploads(t, db,
		Upload{ID: 1},
		Upload{ID: 2},
		Upload{ID: 3},
		Upload{ID: 4},
		Upload{ID: 5},
		Upload{ID: 6},
		Upload{ID: 7},
		Upload{ID: 8},
		Upload{ID: 9},
		Upload{ID: 10},
	)

	t.Run("fetch", func(t *testing.T) {
		indexes, err := store.GetUploadsByIDs(ctx, 2, 4, 6, 8, 12)
		if err != nil {
			t.Fatalf("unexpected error getting indexes for repo: %s", err)
		}

		var ids []int
		for _, index := range indexes {
			ids = append(ids, index.ID)
		}
		sort.Ints(ids)

		if diff := cmp.Diff([]int{2, 4, 6, 8}, ids); diff != "" {
			t.Errorf("unexpected index ids (-want +got):\n%s", diff)
		}
	})

	t.Run("enforce repository permissions", func(t *testing.T) {
		// Enable permissions user mapping forces checking repository permissions
		// against permissions tables in the database, which should effectively block
		// all access because permissions tables are empty.
		before := globals.PermissionsUserMapping()
		globals.SetPermissionsUserMapping(&schema.PermissionsUserMapping{Enabled: true})
		defer globals.SetPermissionsUserMapping(before)

		indexes, err := store.GetUploadsByIDs(ctx, 1, 2, 3, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(indexes) > 0 {
			t.Fatalf("Want no index but got %d indexes", len(indexes))
		}
	})
}

func TestDeleteUploadsStuckUploading(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(time.Minute * 1)
	t3 := t1.Add(time.Minute * 2)
	t4 := t1.Add(time.Minute * 3)
	t5 := t1.Add(time.Minute * 4)

	insertUploads(t, db,
		Upload{ID: 1, Commit: makeCommit(1111), UploadedAt: t1, State: "queued"},    // not uploading
		Upload{ID: 2, Commit: makeCommit(1112), UploadedAt: t2, State: "uploading"}, // deleted
		Upload{ID: 3, Commit: makeCommit(1113), UploadedAt: t3, State: "uploading"}, // deleted
		Upload{ID: 4, Commit: makeCommit(1114), UploadedAt: t4, State: "completed"}, // old, not uploading
		Upload{ID: 5, Commit: makeCommit(1115), UploadedAt: t5, State: "uploading"}, // old
	)

	count, err := store.DeleteUploadsStuckUploading(context.Background(), t1.Add(time.Minute*3))
	if err != nil {
		t.Fatalf("unexpected error deleting uploads stuck uploading: %s", err)
	}
	if count != 2 {
		t.Errorf("unexpected count. want=%d have=%d", 2, count)
	}

	uploads, totalCount, err := store.GetUploads(context.Background(), GetUploadsOptions{Limit: 5})
	if err != nil {
		t.Fatalf("unexpected error getting uploads: %s", err)
	}

	var ids []int
	for _, upload := range uploads {
		ids = append(ids, upload.ID)
	}
	sort.Ints(ids)

	expectedIDs := []int{1, 4, 5}

	if totalCount != len(expectedIDs) {
		t.Errorf("unexpected total count. want=%d have=%d", len(expectedIDs), totalCount)
	}
	if diff := cmp.Diff(expectedIDs, ids); diff != "" {
		t.Errorf("unexpected upload ids (-want +got):\n%s", diff)
	}
}

func TestGetUploads(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)
	ctx := context.Background()

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(-time.Minute * 1)
	t3 := t1.Add(-time.Minute * 2)
	t4 := t1.Add(-time.Minute * 3)
	t5 := t1.Add(-time.Minute * 4)
	t6 := t1.Add(-time.Minute * 5)
	t7 := t1.Add(-time.Minute * 6)
	t8 := t1.Add(-time.Minute * 7)
	t9 := t1.Add(-time.Minute * 8)
	t10 := t1.Add(-time.Minute * 9)
	failureMessage := "unlucky 333"

	insertUploads(t, db,
		Upload{ID: 1, Commit: makeCommit(3331), UploadedAt: t1, Root: "sub1/", State: "queued"},
		Upload{ID: 2, UploadedAt: t2, State: "errored", FailureMessage: &failureMessage, Indexer: "lsif-tsc"},
		Upload{ID: 3, Commit: makeCommit(3333), UploadedAt: t3, Root: "sub2/", State: "queued"},
		Upload{ID: 4, UploadedAt: t4, State: "queued", RepositoryID: 51, RepositoryName: "foo bar x"},
		Upload{ID: 5, Commit: makeCommit(3333), UploadedAt: t5, Root: "sub1/", State: "processing", Indexer: "lsif-tsc"},
		Upload{ID: 6, UploadedAt: t6, Root: "sub2/", State: "processing", RepositoryID: 52, RepositoryName: "foo bar y"},
		Upload{ID: 7, UploadedAt: t7, Root: "sub1/", Indexer: "lsif-tsc"},
		Upload{ID: 8, UploadedAt: t8, Indexer: "lsif-tsc"},
		Upload{ID: 9, UploadedAt: t9, State: "queued"},
		Upload{ID: 10, UploadedAt: t10, Root: "sub1/", Indexer: "lsif-tsc"},

		// Deleted duplicates
		Upload{ID: 11, Commit: makeCommit(3331), UploadedAt: t1, Root: "sub1/", State: "deleted"},
		Upload{ID: 12, UploadedAt: t2, State: "deleted", FailureMessage: &failureMessage, Indexer: "lsif-tsc"},
		Upload{ID: 13, Commit: makeCommit(3333), UploadedAt: t3, Root: "sub2/", State: "deleted"},
	)
	insertVisibleAtTip(t, db, 50, 2, 5, 7, 8)

	// upload 10 depends on uploads 7 and 8
	insertPackages(t, store, []shared.Package{
		{DumpID: 7, Scheme: "npm", Name: "foo", Version: "0.1.0"},
		{DumpID: 8, Scheme: "npm", Name: "bar", Version: "1.2.3"},
	})
	insertPackageReferences(t, store, []shared.PackageReference{
		{Package: shared.Package{DumpID: 10, Scheme: "npm", Name: "foo", Version: "0.1.0"}},
		{Package: shared.Package{DumpID: 10, Scheme: "npm", Name: "bar", Version: "1.2.3"}},
	})

	testCases := []struct {
		repositoryID   int
		state          string
		term           string
		visibleAtTip   bool
		dependencyOf   int
		dependentOf    int
		uploadedBefore *time.Time
		uploadedAfter  *time.Time
		oldestFirst    bool
		expectedIDs    []int
	}{
		{expectedIDs: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{oldestFirst: true, expectedIDs: []int{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}},
		{repositoryID: 50, expectedIDs: []int{1, 2, 3, 5, 7, 8, 9, 10}},
		{state: "completed", expectedIDs: []int{7, 8, 10}},
		{term: "sub", expectedIDs: []int{1, 3, 5, 6, 7, 10}}, // searches root
		{term: "003", expectedIDs: []int{1, 3, 5}},           // searches commits
		{term: "333", expectedIDs: []int{1, 2, 3, 5}},        // searches commits and failure message
		{term: "tsc", expectedIDs: []int{2, 5, 7, 8, 10}},    // searches indexer
		{term: "QuEuEd", expectedIDs: []int{1, 3, 4, 9}},     // searches text status
		{term: "bAr", expectedIDs: []int{4, 6}},              // search repo names
		{visibleAtTip: true, expectedIDs: []int{2, 5, 7, 8}},
		{dependencyOf: 10, expectedIDs: []int{7, 8}},
		{dependentOf: 7, expectedIDs: []int{10}},
		{uploadedBefore: &t5, expectedIDs: []int{6, 7, 8, 9, 10}},
		{uploadedAfter: &t4, expectedIDs: []int{1, 2, 3}},
	}

	for _, testCase := range testCases {
		for lo := 0; lo < len(testCase.expectedIDs); lo++ {
			hi := lo + 3
			if hi > len(testCase.expectedIDs) {
				hi = len(testCase.expectedIDs)
			}

			name := fmt.Sprintf(
				"repositoryID=%d state=%s term=%s visibleAtTip=%v dependencyOf=%d dependentOf=%d offset=%d",
				testCase.repositoryID,
				testCase.state,
				testCase.term,
				testCase.visibleAtTip,
				testCase.dependencyOf,
				testCase.dependentOf,
				lo,
			)

			t.Run(name, func(t *testing.T) {
				uploads, totalCount, err := store.GetUploads(ctx, GetUploadsOptions{
					RepositoryID:   testCase.repositoryID,
					State:          testCase.state,
					Term:           testCase.term,
					VisibleAtTip:   testCase.visibleAtTip,
					DependencyOf:   testCase.dependencyOf,
					DependentOf:    testCase.dependentOf,
					UploadedBefore: testCase.uploadedBefore,
					UploadedAfter:  testCase.uploadedAfter,
					OldestFirst:    testCase.oldestFirst,
					Limit:          3,
					Offset:         lo,
				})
				if err != nil {
					t.Fatalf("unexpected error getting uploads for repo: %s", err)
				}
				if totalCount != len(testCase.expectedIDs) {
					t.Errorf("unexpected total count. want=%d have=%d", len(testCase.expectedIDs), totalCount)
				}

				var ids []int
				for _, upload := range uploads {
					ids = append(ids, upload.ID)
				}

				if diff := cmp.Diff(testCase.expectedIDs[lo:hi], ids); diff != "" {
					t.Errorf("unexpected upload ids at offset %d (-want +got):\n%s", lo, diff)
				}
			})
		}
	}

	t.Run("enforce repository permissions", func(t *testing.T) {
		// Enable permissions user mapping forces checking repository permissions
		// against permissions tables in the database, which should effectively block
		// all access because permissions tables are empty.
		before := globals.PermissionsUserMapping()
		globals.SetPermissionsUserMapping(&schema.PermissionsUserMapping{Enabled: true})
		defer globals.SetPermissionsUserMapping(before)

		uploads, totalCount, err := store.GetUploads(ctx,
			GetUploadsOptions{
				Limit: 1,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(uploads) > 0 || totalCount > 0 {
			t.Fatalf("Want no upload but got %d uploads with totalCount %d", len(uploads), totalCount)
		}
	})
}

func TestInsertUploadUploading(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertRepo(t, db, 50, "")

	id, err := store.InsertUpload(context.Background(), Upload{
		Commit:       makeCommit(1),
		Root:         "sub/",
		State:        "uploading",
		RepositoryID: 50,
		Indexer:      "lsif-go",
		NumParts:     3,
	})
	if err != nil {
		t.Fatalf("unexpected error enqueueing upload: %s", err)
	}

	expected := Upload{
		ID:             id,
		Commit:         makeCommit(1),
		Root:           "sub/",
		VisibleAtTip:   false,
		UploadedAt:     time.Time{},
		State:          "uploading",
		FailureMessage: nil,
		StartedAt:      nil,
		FinishedAt:     nil,
		RepositoryID:   50,
		RepositoryName: "n-50",
		Indexer:        "lsif-go",
		NumParts:       3,
		UploadedParts:  []int{},
	}

	if upload, exists, err := store.GetUploadByID(context.Background(), id); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else {
		// Update auto-generated timestamp
		expected.UploadedAt = upload.UploadedAt

		if diff := cmp.Diff(expected, upload); diff != "" {
			t.Errorf("unexpected upload (-want +got):\n%s", diff)
		}
	}
}

func TestInsertUploadQueued(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertRepo(t, db, 50, "")

	id, err := store.InsertUpload(context.Background(), Upload{
		Commit:        makeCommit(1),
		Root:          "sub/",
		State:         "queued",
		RepositoryID:  50,
		Indexer:       "lsif-go",
		NumParts:      1,
		UploadedParts: []int{0},
	})
	if err != nil {
		t.Fatalf("unexpected error enqueueing upload: %s", err)
	}

	rank := 1
	expected := Upload{
		ID:             id,
		Commit:         makeCommit(1),
		Root:           "sub/",
		VisibleAtTip:   false,
		UploadedAt:     time.Time{},
		State:          "queued",
		FailureMessage: nil,
		StartedAt:      nil,
		FinishedAt:     nil,
		RepositoryID:   50,
		RepositoryName: "n-50",
		Indexer:        "lsif-go",
		NumParts:       1,
		UploadedParts:  []int{0},
		Rank:           &rank,
	}

	if upload, exists, err := store.GetUploadByID(context.Background(), id); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else {
		// Update auto-generated timestamp
		expected.UploadedAt = upload.UploadedAt

		if diff := cmp.Diff(expected, upload); diff != "" {
			t.Errorf("unexpected upload (-want +got):\n%s", diff)
		}
	}
}

func TestInsertUploadWithAssociatedIndexID(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertRepo(t, db, 50, "")

	associatedIndexIDArg := 42
	id, err := store.InsertUpload(context.Background(), Upload{
		Commit:            makeCommit(1),
		Root:              "sub/",
		State:             "queued",
		RepositoryID:      50,
		Indexer:           "lsif-go",
		NumParts:          1,
		UploadedParts:     []int{0},
		AssociatedIndexID: &associatedIndexIDArg,
	})
	if err != nil {
		t.Fatalf("unexpected error enqueueing upload: %s", err)
	}

	rank := 1
	associatedIndexIDResult := 42
	expected := Upload{
		ID:                id,
		Commit:            makeCommit(1),
		Root:              "sub/",
		VisibleAtTip:      false,
		UploadedAt:        time.Time{},
		State:             "queued",
		FailureMessage:    nil,
		StartedAt:         nil,
		FinishedAt:        nil,
		RepositoryID:      50,
		RepositoryName:    "n-50",
		Indexer:           "lsif-go",
		NumParts:          1,
		UploadedParts:     []int{0},
		Rank:              &rank,
		AssociatedIndexID: &associatedIndexIDResult,
	}

	if upload, exists, err := store.GetUploadByID(context.Background(), id); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else {
		// Update auto-generated timestamp
		expected.UploadedAt = upload.UploadedAt

		if diff := cmp.Diff(expected, upload); diff != "" {
			t.Errorf("unexpected upload (-want +got):\n%s", diff)
		}
	}
}

func TestMarkQueued(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db, Upload{ID: 1, State: "uploading"})

	uploadSize := int64(300)
	if err := store.MarkQueued(context.Background(), 1, &uploadSize); err != nil {
		t.Fatalf("unexpected error marking upload as queued: %s", err)
	}

	if upload, exists, err := store.GetUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else if upload.State != "queued" {
		t.Errorf("unexpected state. want=%q have=%q", "queued", upload.State)
	} else if upload.UploadSize == nil || *upload.UploadSize != 300 {
		if upload.UploadSize == nil {
			t.Errorf("unexpected upload size. want=%v have=%v", 300, upload.UploadSize)
		} else {
			t.Errorf("unexpected upload size. want=%v have=%v", 300, *upload.UploadSize)
		}
	}
}

func TestMarkFailed(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db, Upload{ID: 1, State: "uploading"})

	failureReason := "didn't like it"
	if err := store.MarkFailed(context.Background(), 1, failureReason); err != nil {
		t.Fatalf("unexpected error marking upload as failed: %s", err)
	}

	if upload, exists, err := store.GetUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else if upload.State != "failed" {
		t.Errorf("unexpected state. want=%q have=%q", "failed", upload.State)
	} else if upload.NumFailures != 1 {
		t.Errorf("unexpected num failures. want=%v have=%v", 1, upload.NumFailures)
	} else if upload.FailureMessage == nil || *upload.FailureMessage != failureReason {
		if upload.FailureMessage == nil {
			t.Errorf("unexpected failure message. want='%s' have='%v'", failureReason, upload.FailureMessage)
		} else {
			t.Errorf("unexpected failure message. want='%s' have='%v'", failureReason, *upload.FailureMessage)
		}
	}
}

func TestAddUploadPart(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db, Upload{ID: 1, State: "uploading"})

	for _, part := range []int{1, 5, 2, 3, 2, 2, 1, 6} {
		if err := store.AddUploadPart(context.Background(), 1, part); err != nil {
			t.Fatalf("unexpected error adding upload part: %s", err)
		}
	}
	if upload, exists, err := store.GetUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	} else if !exists {
		t.Fatal("expected record to exist")
	} else {
		sort.Ints(upload.UploadedParts)
		if diff := cmp.Diff([]int{1, 2, 3, 5, 6}, upload.UploadedParts); diff != "" {
			t.Errorf("unexpected upload parts (-want +got):\n%s", diff)
		}
	}
}

func TestDeleteUploadByID(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 1, RepositoryID: 50},
	)

	if found, err := store.DeleteUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error deleting upload: %s", err)
	} else if !found {
		t.Fatalf("expected record to exist")
	}

	// Ensure record was deleted
	if states, err := getUploadStates(db, 1); err != nil {
		t.Fatalf("unexpected error getting states: %s", err)
	} else if diff := cmp.Diff(map[int]string{1: "deleting"}, states); diff != "" {
		t.Errorf("unexpected dump (-want +got):\n%s", diff)
	}

	repositoryIDs, err := store.DirtyRepositories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing dirty repositories: %s", err)
	}

	var keys []int
	for repositoryID := range repositoryIDs {
		keys = append(keys, repositoryID)
	}
	sort.Ints(keys)

	if len(keys) != 1 || keys[0] != 50 {
		t.Errorf("expected repository to be marked dirty")
	}
}

func TestDeleteUploadByIDNotCompleted(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 1, RepositoryID: 50, State: "uploading"},
	)

	if found, err := store.DeleteUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error deleting upload: %s", err)
	} else if !found {
		t.Fatalf("expected record to exist")
	}

	// Ensure record was deleted
	if states, err := getUploadStates(db, 1); err != nil {
		t.Fatalf("unexpected error getting states: %s", err)
	} else if diff := cmp.Diff(map[int]string{1: "deleted"}, states); diff != "" {
		t.Errorf("unexpected dump (-want +got):\n%s", diff)
	}

	repositoryIDs, err := store.DirtyRepositories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing dirty repositories: %s", err)
	}

	var keys []int
	for repositoryID := range repositoryIDs {
		keys = append(keys, repositoryID)
	}
	sort.Ints(keys)

	if len(keys) != 1 || keys[0] != 50 {
		t.Errorf("expected repository to be marked dirty")
	}
}

func TestDeleteUploadByIDMissingRow(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	if found, err := store.DeleteUploadByID(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error deleting upload: %s", err)
	} else if found {
		t.Fatalf("unexpected record")
	}
}

func TestDeleteUploadsWithoutRepository(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	var uploads []Upload
	for i := 0; i < 25; i++ {
		for j := 0; j < 10+i; j++ {
			uploads = append(uploads, Upload{ID: len(uploads) + 1, RepositoryID: 50 + i})
		}
	}
	insertUploads(t, db, uploads...)

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(-DeletedRepositoryGracePeriod + time.Minute)
	t3 := t1.Add(-DeletedRepositoryGracePeriod - time.Minute)

	deletions := map[int]time.Time{
		52: t2, 54: t2, 56: t2, // deleted too recently
		61: t3, 63: t3, 65: t3, // deleted
	}

	for repositoryID, deletedAt := range deletions {
		query := sqlf.Sprintf(`UPDATE repo SET deleted_at=%s WHERE id=%s`, deletedAt, repositoryID)

		if _, err := db.Query(query.Query(sqlf.PostgresBindVar), query.Args()...); err != nil {
			t.Fatalf("Failed to update repository: %s", err)
		}
	}

	deletedCounts, err := store.DeleteUploadsWithoutRepository(context.Background(), t1)
	if err != nil {
		t.Fatalf("unexpected error deleting uploads: %s", err)
	}

	expected := map[int]int{
		61: 21,
		63: 23,
		65: 25,
	}
	if diff := cmp.Diff(expected, deletedCounts); diff != "" {
		t.Errorf("unexpected deletedCounts (-want +got):\n%s", diff)
	}

	var uploadIDs []int
	for i := range uploads {
		uploadIDs = append(uploadIDs, i+1)
	}

	// Ensure records were deleted
	if states, err := getUploadStates(db, uploadIDs...); err != nil {
		t.Fatalf("unexpected error getting states: %s", err)
	} else {
		deletedStates := 0
		for _, state := range states {
			if state == "deleted" {
				deletedStates++
			}
		}

		expected := 0
		for _, deletedCount := range deletedCounts {
			expected += deletedCount
		}

		if deletedStates != expected {
			t.Errorf("unexpected number of deleted records. want=%d have=%d", expected, deletedStates)
		}
	}
}

func TestHardDeleteUploadByID(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 51, State: "completed"},
		Upload{ID: 52, State: "completed"},
		Upload{ID: 53, State: "completed"},
		Upload{ID: 54, State: "completed"},
	)
	insertPackages(t, store, []shared.Package{
		{DumpID: 52, Scheme: "test", Name: "p1", Version: "1.2.3"},
		{DumpID: 53, Scheme: "test", Name: "p2", Version: "1.2.3"},
	})
	insertPackageReferences(t, store, []shared.PackageReference{
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p2", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 54, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 54, Scheme: "test", Name: "p2", Version: "1.2.3"}},
	})

	if err := store.UpdateNumReferences(context.Background(), []int{51, 52, 53, 54}); err != nil {
		t.Fatalf("unexpected error updating num references: %s", err)
	}

	if err := store.HardDeleteUploadByID(context.Background(), 51); err != nil {
		t.Fatalf("unexpected error deleting upload: %s", err)
	}

	numReferencesByID, err := scanIntPairs(store.Query(context.Background(), sqlf.Sprintf(`SELECT id, num_references FROM lsif_uploads`)))
	if err != nil {
		t.Fatalf("unexpected error querying num_references: %s", err)
	}

	expectedNumReferencesByID := map[int]int{
		// 51 was deleted
		52: 1,
		53: 1,
		54: 0,
	}
	if diff := cmp.Diff(expectedNumReferencesByID, numReferencesByID); diff != "" {
		t.Errorf("unexpected reference count (-want +got):\n%s", diff)
	}
}

func TestSelectRepositoriesForIndexScan(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	now := timeutil.Now()
	insertRepo(t, db, 50, "r0")
	insertRepo(t, db, 51, "r1")
	insertRepo(t, db, 52, "r2")
	insertRepo(t, db, 53, "r3")

	// Make visible to repo culling query
	addToSearchContext(t, db, 50)
	addToSearchContext(t, db, 51)
	addToSearchContext(t, db, 52)
	addToSearchContext(t, db, 53)

	// Can return nulls
	if repositories, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 2, now); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int{50, 51}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 20 minutes later, first two repositories are still on cooldown
	if repositories, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 100, now.Add(time.Minute*20)); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int{52, 53}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 30 minutes later, all repositories are still on cooldown
	if repositories, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 100, now.Add(time.Minute*30)); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int(nil), repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 90 minutes later, all repositories are visible
	if repositories, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 100, now.Add(time.Minute*90)); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int{50, 51, 52, 53}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// Make new invisible repository
	insertRepo(t, db, 54, "r4")

	// 95 minutes later, new repository is not yet visible
	if repositoryIDs, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 100, now.Add(time.Minute*95)); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int(nil), repositoryIDs); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// Make new repository visible
	addToSearchContext(t, db, 54)

	// 100 minutes later, only new repository is visible
	if repositoryIDs, err := store.selectRepositoriesForIndexScan(context.Background(), time.Hour, 100, now.Add(time.Minute*100)); err != nil {
		t.Fatalf("unexpected error fetching repositories for index scan: %s", err)
	} else if diff := cmp.Diff([]int{54}, repositoryIDs); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}
}

func TestSelectRepositoriesForRetentionScan(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 1, RepositoryID: 50, State: "completed"},
		Upload{ID: 2, RepositoryID: 51, State: "completed"},
		Upload{ID: 3, RepositoryID: 52, State: "completed"},
		Upload{ID: 4, RepositoryID: 53, State: "completed"},
		Upload{ID: 5, RepositoryID: 54, State: "errored"},
		Upload{ID: 6, RepositoryID: 54, State: "deleted"},
	)

	now := timeutil.Now()

	for _, repositoryID := range []int{50, 51, 52, 53, 54} {
		// Only call this to insert a record into the lsif_dirty_repositories table
		if err := store.MarkRepositoryAsDirty(context.Background(), repositoryID); err != nil {
			t.Fatalf("unexpected error marking repository as dirty`: %s", err)
		}

		// Only call this to update the updated_at field in the lsif_dirty_repositories table
		if err := store.CalculateVisibleUploads(context.Background(), repositoryID, gitserver.ParseCommitGraph(nil), nil, time.Hour, time.Hour, 1, now); err != nil {
			t.Fatalf("unexpected error updating commit graph: %s", err)
		}
	}

	// Can return nulls
	if repositories, err := store.selectRepositoriesForRetentionScan(context.Background(), time.Hour, 2, now); err != nil {
		t.Fatalf("unexpected error fetching repositories for retention scan: %s", err)
	} else if diff := cmp.Diff([]int{50, 51}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 20 minutes later, first two repositories are still on cooldown
	if repositories, err := store.selectRepositoriesForRetentionScan(context.Background(), time.Hour, 100, now.Add(time.Minute*20)); err != nil {
		t.Fatalf("unexpected error fetching repositories for retention scan: %s", err)
	} else if diff := cmp.Diff([]int{52, 53}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 30 minutes later, all repositories are still on cooldown
	if repositories, err := store.selectRepositoriesForRetentionScan(context.Background(), time.Hour, 100, now.Add(time.Minute*30)); err != nil {
		t.Fatalf("unexpected error fetching repositories for retention scan: %s", err)
	} else if diff := cmp.Diff([]int(nil), repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// 90 minutes later, all repositories are visible
	if repositories, err := store.selectRepositoriesForRetentionScan(context.Background(), time.Hour, 100, now.Add(time.Minute*90)); err != nil {
		t.Fatalf("unexpected error fetching repositories for retention scan: %s", err)
	} else if diff := cmp.Diff([]int{50, 51, 52, 53}, repositories); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}

	// Make repository 5 newly visible
	if _, err := db.Exec(`UPDATE lsif_uploads SET state = 'completed' WHERE id = 5`); err != nil {
		t.Fatalf("unexpected error updating upload: %s", err)
	}

	// 95 minutes later, only new repository is visible
	if repositoryIDs, err := store.selectRepositoriesForRetentionScan(context.Background(), time.Hour, 100, now.Add(time.Minute*95)); err != nil {
		t.Fatalf("unexpected error fetching repositories for retention scan: %s", err)
	} else if diff := cmp.Diff([]int{54}, repositoryIDs); diff != "" {
		t.Fatalf("unexpected repository list (-want +got):\n%s", diff)
	}
}

func TestUpdateUploadRetention(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 1, State: "completed"},
		Upload{ID: 2, State: "completed"},
		Upload{ID: 3, State: "completed"},
		Upload{ID: 4, State: "completed"},
		Upload{ID: 5, State: "completed"},
	)

	now := timeutil.Now()

	if err := store.updateUploadRetention(context.Background(), []int{}, []int{2, 3, 4}, now); err != nil {
		t.Fatalf("unexpected error marking uploads as expired: %s", err)
	}

	count, _, err := basestore.ScanFirstInt(db.Query(`SELECT COUNT(*) FROM lsif_uploads WHERE expired`))
	if err != nil {
		t.Fatalf("unexpected error counting uploads: %s", err)
	}

	if count != 3 {
		t.Fatalf("unexpected count. want=%d have=%d", 3, count)
	}
}

func TestUpdateNumReferences(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 50, State: "completed"},
		Upload{ID: 51, State: "completed"},
		Upload{ID: 52, State: "completed"},
		Upload{ID: 53, State: "completed"},
		Upload{ID: 54, State: "completed"},
		Upload{ID: 55, State: "completed"},
		Upload{ID: 56, State: "completed"},
	)
	insertPackages(t, store, []shared.Package{
		{DumpID: 53, Scheme: "test", Name: "p1", Version: "1.2.3"},
		{DumpID: 54, Scheme: "test", Name: "p2", Version: "1.2.3"},
		{DumpID: 55, Scheme: "test", Name: "p3", Version: "1.2.3"},
		{DumpID: 56, Scheme: "test", Name: "p4", Version: "1.2.3"},
	})
	insertPackageReferences(t, store, []shared.PackageReference{
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p2", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p3", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p4", Version: "1.2.3"}},

		{Package: shared.Package{DumpID: 53, Scheme: "test", Name: "p4", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 54, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 55, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 56, Scheme: "test", Name: "p1", Version: "1.2.3"}},
	})

	if err := store.UpdateNumReferences(context.Background(), []int{50, 51, 52, 53, 54, 55, 56}); err != nil {
		t.Fatalf("unexpected error updating num references: %s", err)
	}

	numReferencesByID, err := scanIntPairs(store.Query(context.Background(), sqlf.Sprintf(`SELECT id, num_references FROM lsif_uploads`)))
	if err != nil {
		t.Fatalf("unexpected error querying num_references: %s", err)
	}

	expectedNumReferencesByID := map[int]int{
		50: 0,
		51: 0,
		52: 0,
		53: 5, // referenced by 51, 52, 54, 55, 56
		54: 1, // referenced by 52
		55: 1, // referenced by 51
		56: 2, // referenced by 52, 53
	}
	if diff := cmp.Diff(expectedNumReferencesByID, numReferencesByID); diff != "" {
		t.Errorf("unexpected reference count (-want +got):\n%s", diff)
	}
}

func TestUpdateDependencyNumReferences(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 50, State: "completed"}, // removed
		Upload{ID: 51, State: "completed"}, // removed
		Upload{ID: 52, State: "completed"}, // removed
		Upload{ID: 53, State: "completed"},
		Upload{ID: 54, State: "completed"},
		Upload{ID: 55, State: "completed"},
		Upload{ID: 56, State: "completed"},
	)
	insertPackages(t, store, []shared.Package{
		{DumpID: 53, Scheme: "test", Name: "p1", Version: "1.2.3"},
		{DumpID: 54, Scheme: "test", Name: "p2", Version: "1.2.3"},
		{DumpID: 55, Scheme: "test", Name: "p3", Version: "1.2.3"},
		{DumpID: 56, Scheme: "test", Name: "p4", Version: "1.2.3"},
	})
	insertPackageReferences(t, store, []shared.PackageReference{
		// References removed
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p2", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p3", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p4", Version: "1.2.3"}},

		// Remaining references
		{Package: shared.Package{DumpID: 53, Scheme: "test", Name: "p4", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 54, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 55, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 56, Scheme: "test", Name: "p1", Version: "1.2.3"}},
	})

	// Set correct initial counts
	if err := store.UpdateNumReferences(context.Background(), []int{50, 51, 52, 53, 54, 55, 56}); err != nil {
		t.Fatalf("unexpected error updating num references: %s", err)
	}

	// Remove ref counts from uploads 50, 51, and 52
	if err := store.UpdateDependencyNumReferences(context.Background(), []int{50, 51, 52}, true); err != nil {
		t.Fatalf("unexpected error updating num references: %s", err)
	}

	numReferencesByID, err := scanIntPairs(store.Query(context.Background(), sqlf.Sprintf(`SELECT id, num_references FROM lsif_uploads`)))
	if err != nil {
		t.Fatalf("unexpected error querying num_references: %s", err)
	}

	expectedNumReferencesByID := map[int]int{
		50: 0,
		51: 0,
		52: 0,
		53: 3, // referenced by 54, 55, 56 (reference from 51, 52 removed)
		54: 0, // referenced by nothing    (reference from 52 removed)
		55: 0, // referenced by nothing    (reference from 51 removed)
		56: 1, // referenced by 53         (reference from 52 removed)
	}
	if diff := cmp.Diff(expectedNumReferencesByID, numReferencesByID); diff != "" {
		t.Errorf("unexpected reference count (-want +got):\n%s", diff)
	}
}

func TestSoftDeleteExpiredUploads(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	insertUploads(t, db,
		Upload{ID: 50, State: "completed"},
		Upload{ID: 51, State: "completed"},
		Upload{ID: 52, State: "completed"},
		Upload{ID: 53, State: "completed"}, // referenced by 51, 52, 54, 55, 56
		Upload{ID: 54, State: "completed"}, // referenced by 52
		Upload{ID: 55, State: "completed"}, // referenced by 51
		Upload{ID: 56, State: "completed"}, // referenced by 52, 53
	)
	insertPackages(t, store, []shared.Package{
		{DumpID: 53, Scheme: "test", Name: "p1", Version: "1.2.3"},
		{DumpID: 54, Scheme: "test", Name: "p2", Version: "1.2.3"},
		{DumpID: 55, Scheme: "test", Name: "p3", Version: "1.2.3"},
		{DumpID: 56, Scheme: "test", Name: "p4", Version: "1.2.3"},
	})
	insertPackageReferences(t, store, []shared.PackageReference{
		// References removed
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p2", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 51, Scheme: "test", Name: "p3", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 52, Scheme: "test", Name: "p4", Version: "1.2.3"}},

		// Remaining references
		{Package: shared.Package{DumpID: 53, Scheme: "test", Name: "p4", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 54, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 55, Scheme: "test", Name: "p1", Version: "1.2.3"}},
		{Package: shared.Package{DumpID: 56, Scheme: "test", Name: "p1", Version: "1.2.3"}},
	})

	if err := store.UpdateUploadRetention(context.Background(), []int{}, []int{51, 52, 53, 54}); err != nil {
		t.Fatalf("unexpected error marking uploads as expired: %s", err)
	}

	if err := store.UpdateNumReferences(context.Background(), []int{50, 51, 52, 53, 54, 55, 56}); err != nil {
		t.Fatalf("unexpected error updating num references: %s", err)
	}

	if count, err := store.SoftDeleteExpiredUploads(context.Background()); err != nil {
		t.Fatalf("unexpected error soft deleting uploads: %s", err)
	} else if count != 2 {
		t.Fatalf("unexpected number of uploads deleted: want=%d have=%d", 2, count)
	}

	// Ensure records were deleted
	expectedStates := map[int]string{
		50: "completed",
		51: "deleting",
		52: "deleting",
		53: "completed",
		54: "completed",
		55: "completed",
		56: "completed",
	}
	if states, err := getUploadStates(db, 50, 51, 52, 53, 54, 55, 56); err != nil {
		t.Fatalf("unexpected error getting states: %s", err)
	} else if diff := cmp.Diff(expectedStates, states); diff != "" {
		t.Errorf("unexpected upload states (-want +got):\n%s", diff)
	}

	// Ensure repository was marked as dirty
	repositoryIDs, err := store.DirtyRepositories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing dirty repositories: %s", err)
	}

	var keys []int
	for repositoryID := range repositoryIDs {
		keys = append(keys, repositoryID)
	}
	sort.Ints(keys)

	if len(keys) != 1 || keys[0] != 50 {
		t.Errorf("expected repository to be marked dirty")
	}
}

func TestGetOldestCommitDate(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(time.Minute * 4)
	t4 := t1.Add(time.Minute * 6)

	insertUploads(t, db,
		Upload{ID: 1, State: "completed"},
		Upload{ID: 2, State: "completed"},
		Upload{ID: 3, State: "completed"},
		Upload{ID: 4, State: "errored"},
		Upload{ID: 5, State: "completed"},
		Upload{ID: 6, State: "completed", RepositoryID: 51},
		Upload{ID: 7, State: "completed", RepositoryID: 51},
		Upload{ID: 8, State: "completed", RepositoryID: 51},
	)

	if _, err := db.Exec("UPDATE lsif_uploads SET committed_at = '-infinity' WHERE id = 3"); err != nil {
		t.Fatalf("unexpected error updating commit date %s", err)
	}

	for uploadID, commitDate := range map[int]time.Time{
		1: t3,
		2: t4,
		4: t1,
		6: t2,
	} {
		if err := store.UpdateCommitedAt(context.Background(), uploadID, commitDate); err != nil {
			t.Fatalf("unexpected error updating commit date %s", err)
		}
	}

	if commitDate, ok, err := store.GetOldestCommitDate(context.Background(), 50); err != nil {
		t.Fatalf("unexpected error getting oldest commit date: %s", err)
	} else if !ok {
		t.Fatalf("expected commit date for repository")
	} else if !commitDate.Equal(t3) {
		t.Fatalf("unexpected commit date. want=%s have=%s", t3, commitDate)
	}

	if commitDate, ok, err := store.GetOldestCommitDate(context.Background(), 51); err != nil {
		t.Fatalf("unexpected error getting oldest commit date: %s", err)
	} else if !ok {
		t.Fatalf("expected commit date for repository")
	} else if !commitDate.Equal(t2) {
		t.Fatalf("unexpected commit date. want=%s have=%s", t2, commitDate)
	}

	if _, ok, err := store.GetOldestCommitDate(context.Background(), 52); err != nil {
		t.Fatalf("unexpected error getting oldest commit date: %s", err)
	} else if ok {
		t.Fatalf("unexpected commit date for repository")
	}
}

func TestUpdateCommitedAt(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	db := dbtesting.GetDB(t)
	store := testStore(db)

	t1 := time.Unix(1587396557, 0).UTC()
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(time.Minute * 4)
	t4 := t1.Add(time.Minute * 6)

	insertUploads(t, db,
		Upload{ID: 1, State: "completed"},
		Upload{ID: 2, State: "completed"},
		Upload{ID: 3, State: "completed"},
		Upload{ID: 4, State: "completed"},
		Upload{ID: 5, State: "completed"},
		Upload{ID: 6, State: "completed"},
		Upload{ID: 7, State: "completed"},
		Upload{ID: 8, State: "completed"},
	)

	for uploadID, commitDate := range map[int]time.Time{
		1: t3,
		2: t4,
		4: t1,
		6: t2,
	} {
		if err := store.UpdateCommitedAt(context.Background(), uploadID, commitDate); err != nil {
			t.Fatalf("unexpected error updating commit date %s", err)
		}
	}

	commitDates, err := basestore.ScanTimes(db.Query("SELECT committed_at FROM lsif_uploads WHERE id IN (1, 2, 4, 6) ORDER BY id"))
	if err != nil {
		t.Fatalf("unexpected error querying commit dates: %s", err)
	}
	if diff := cmp.Diff([]time.Time{t3, t4, t1, t2}, commitDates); diff != "" {
		t.Errorf("unexpected commit dates(-want +got):\n%s", diff)
	}
}
