package types

import (
	"io"
	"strings"
	"time"

	"github.com/sourcegraph/go-diff/diff"

	"github.com/sourcegraph/sourcegraph/internal/api"
	batcheslib "github.com/sourcegraph/sourcegraph/lib/batches"
)

func NewChangesetSpecFromRaw(rawSpec string) (*ChangesetSpec, error) {
	c := &ChangesetSpec{}

	var err error
	c.Spec, err = batcheslib.ParseChangesetSpec([]byte(rawSpec))
	if err != nil {
		return nil, err
	}

	return c, c.computeDiffStat()
}

type ChangesetSpec struct {
	ID     int64
	RandID string

	Spec *batcheslib.ChangesetSpec

	DiffStatAdded   int32
	DiffStatChanged int32
	DiffStatDeleted int32

	BatchSpecID int64
	RepoID      api.RepoID
	UserID      int32

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Clone returns a clone of a ChangesetSpec.
func (cs *ChangesetSpec) Clone() *ChangesetSpec {
	cc := *cs
	return &cc
}

// computeDiffStat parses the Diff of the ChangesetSpecDescription and sets the
// diff stat fields that can be retrieved with DiffStat().
// If the Diff is invalid or parsing failed, an error is returned.
func (cs *ChangesetSpec) computeDiffStat() error {
	if cs.Spec.IsImportingExisting() {
		return nil
	}

	d, err := cs.Spec.Diff()
	if err != nil {
		return err
	}

	stats := diff.Stat{}
	reader := diff.NewMultiFileDiffReader(strings.NewReader(d))
	for {
		fileDiff, err := reader.ReadFile()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		stat := fileDiff.Stat()
		stats.Added += stat.Added
		stats.Deleted += stat.Deleted
		stats.Changed += stat.Changed
	}

	cs.DiffStatAdded = stats.Added
	cs.DiffStatDeleted = stats.Deleted
	cs.DiffStatChanged = stats.Changed

	return nil
}

// DiffStat returns a *diff.Stat.
func (cs *ChangesetSpec) DiffStat() diff.Stat {
	return diff.Stat{
		Added:   cs.DiffStatAdded,
		Deleted: cs.DiffStatDeleted,
		Changed: cs.DiffStatChanged,
	}
}

// ChangesetSpecTTL specifies the TTL of ChangesetSpecs that haven't been
// attached to a BatchSpec.
// It's lower than BatchSpecTTL because ChangesetSpecs should be attached to
// a BatchSpec immediately after having been created, whereas a BatchSpec
// might take a while to be complete and might also go through a lengthy review
// phase.
const ChangesetSpecTTL = 2 * 24 * time.Hour

// ExpiresAt returns the time when the ChangesetSpec will be deleted if not
// attached to a BatchSpec.
func (cs *ChangesetSpec) ExpiresAt() time.Time {
	return cs.CreatedAt.Add(ChangesetSpecTTL)
}

// ChangesetSpecs is a slice of *ChangesetSpecs.
type ChangesetSpecs []*ChangesetSpec

// IDs returns the unique RepoIDs of all changeset specs in the slice.
func (cs ChangesetSpecs) RepoIDs() []api.RepoID {
	repoIDMap := make(map[api.RepoID]struct{})
	for _, c := range cs {
		repoIDMap[c.RepoID] = struct{}{}
	}
	repoIDs := make([]api.RepoID, 0)
	for id := range repoIDMap {
		repoIDs = append(repoIDs, id)
	}
	return repoIDs
}
