package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/internal/stageowner"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

type stageCandidateRelease struct {
	tenant                 string
	database               *sql.DB
	rawManifest, signature []byte
	bundle                 string
	candidate              guardrelease.PreviewCandidateProvenance
}

var stageCandidate struct {
	sync.RWMutex
	source *stageCandidateRelease
}

// This reports configuration only, never installation or execution authority.
func StageCandidateReleaseConfigured() bool {
	stageCandidate.RLock()
	defer stageCandidate.RUnlock()
	return stageCandidate.source != nil
}

func ApplicationAdoptionReleaseForStack(ctx context.Context, tenant, stack string, read bool) (*agentpb.StackKitReleasePin, error) {
	return applicationAdoptionPin(func() (*stackkitrelease.Release, error) {
		if read {
			return CachedTargetStackKitReleaseForStack(ctx, tenant, stack)
		}
		return TargetStackKitReleaseForStack(ctx, tenant, stack)
	})
}

func ApplicationConnectorReleaseForStack(ctx context.Context, tenant, stack string, read bool) (*agentpb.StackKitReleasePin, error) {
	var release *stackkitrelease.Release
	var err error
	if read {
		release, err = CachedTargetStackKitReleaseForStack(ctx, tenant, stack)
	} else {
		release, err = TargetStackKitReleaseForStack(ctx, tenant, stack)
	}
	return applicationConnectorReleasePin(release, err)
}

// PrepareStageCandidateRelease is a boot-only adapter after actual Stage DB
// attestation and the existing immutable Owner assignment. Paths only locate
// signed bytes. Neither environment values nor downloaded provenance creates
// Stage custody, selects a tenant or activates background work.
func PrepareStageCandidateRelease(binding *db.StagingRuntimeBinding, database *sql.DB, tenant string) error {
	if !binding.Verified() || database == nil || !stageowner.StageRuntimePrepared() || strings.TrimSpace(tenant) == "" {
		return db.ErrStagingRuntimeBinding
	}
	dir := strings.TrimSpace(os.Getenv("TECHSTACK_GUARD_RELEASE_DIR"))
	bundle := strings.TrimSpace(os.Getenv(stackKitReleaseBundleEnv))
	if dir == "" || bundle == "" {
		return nil
	}
	read := func(name string, limit int64) ([]byte, error) {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
			return nil, fmt.Errorf("Stage Guard evidence is not bounded")
		}
		raw := make([]byte, info.Size())
		_, err = file.ReadAt(raw, 0)
		return raw, err
	}
	raw, err := read("release-manifest.json", guardrelease.MaxManifestBytes)
	if err != nil {
		return err
	}
	sig, err := read("release-manifest.sigstore.json", guardrelease.MaxBundleBytes)
	if err != nil {
		return err
	}
	verifier, err := guardrelease.NewVerifier(guardrelease.Options{})
	if err != nil {
		return err
	}
	manifest, err := verifier.VerifyManifest(raw, sig)
	if err != nil {
		return err
	}
	if manifest.StackKits == nil || manifest.StackKits.Candidate == nil {
		return nil
	}
	source := &stageCandidateRelease{tenant: tenant, database: database, rawManifest: raw, signature: sig, bundle: bundle, candidate: *manifest.StackKits.Candidate}
	stageCandidate.Lock()
	defer stageCandidate.Unlock()
	if stageCandidate.source != nil {
		return fmt.Errorf("Stage candidate source is already prepared")
	}
	stageCandidate.source = source
	return nil
}

// StageCandidateForStack resolves the CURRENT canonical DB row in the verified
// Stage pool. The same actual owner/tenant policy gates requests and jobs; a
// stack locator cannot create an assignment or an execution grant.
func StageCandidateForStack(ctx context.Context, tenant, stackID string) (*stackkitrelease.StageCandidateResolver, bool, error) {
	source, configured, err := stageCandidateSourceForStack(ctx, tenant, stackID)
	if err != nil || !configured {
		return nil, configured, err
	}
	resolver, err := source.resolver(stackID)
	return resolver, true, err
}

func (source *stageCandidateRelease) resolver(stackID string) (*stackkitrelease.StageCandidateResolver, error) {
	return stackkitrelease.NewStageCandidateResolver(source.rawManifest, source.signature, source.bundle, stackkitrelease.StageCandidateAssignment{TenantID: source.tenant, FoundationID: stackID, Candidate: source.candidate})
}

func stageCandidateSourceForStack(ctx context.Context, tenant, stackID string) (*stageCandidateRelease, bool, error) {
	stageCandidate.RLock()
	source := stageCandidate.source
	stageCandidate.RUnlock()
	if source == nil {
		return nil, false, nil
	}
	if tenant != source.tenant || !stageowner.StageOwnerActive() || !releasechannel.OwnsTenant(tenant) || stackID == "" {
		return nil, true, stageowner.ErrStageOwnerNotAdmitted
	}
	stack, err := controlplane.NewPostgresStore(source.database).GetStack(ctx, tenant, stackID)
	if err != nil {
		return nil, true, err
	}
	if stack == nil || stack.ID != stackID || stack.TenantID != tenant || stack.DeletedAt != nil || !releasechannel.OwnsSubject(stack.OwnerSubjectID) {
		return nil, true, stageowner.ErrStageOwnerNotAdmitted
	}
	return source, true, nil
}

// Read projections still check live owner/Stack scope each time. Only artifact
// parsing uses the existing bounded read TTL; execution always admits afresh.
func CachedTargetStackKitReleaseForStack(ctx context.Context, tenant, stackID string) (*stackkitrelease.Release, error) {
	source, configured, err := stageCandidateSourceForStack(ctx, tenant, stackID)
	if err != nil {
		return nil, err
	}
	if !configured {
		return CachedTargetStackKitRelease()
	}
	key := "candidate|" + tenant + "|" + stackID + "|" + source.candidate.IndexSHA256 + "|" + releaseFileIdentity()
	return cachedTargetStackKitRelease(key, func() (*stackkitrelease.Release, error) {
		resolver, err := source.resolver(stackID)
		if err != nil {
			return nil, err
		}
		return resolver.Resolve()
	})
}

func TargetStackKitReleaseForStack(ctx context.Context, tenant, stackID string) (*stackkitrelease.Release, error) {
	resolver, configured, err := StageCandidateForStack(ctx, tenant, stackID)
	if err != nil {
		return nil, err
	}
	if configured {
		return resolver.Resolve()
	}
	return configuredTargetStackKitRelease()
}

func TargetStackKitReleaseForWorker(ctx context.Context, tenant, workerID string) (*stackkitrelease.Release, error) {
	stageCandidate.RLock()
	source := stageCandidate.source
	stageCandidate.RUnlock()
	if source == nil {
		return configuredTargetStackKitRelease()
	}
	if tenant != source.tenant {
		return nil, stageowner.ErrStageOwnerNotAdmitted
	}
	worker, err := controlplane.NewPostgresStore(source.database).GetWorker(ctx, tenant, workerID)
	if err != nil {
		return nil, err
	}
	if worker == nil || worker.TenantID != tenant || !releasechannel.OwnsSubject(worker.OwnerSubjectID) {
		return nil, stageowner.ErrStageOwnerNotAdmitted
	}
	return TargetStackKitReleaseForStack(ctx, tenant, worker.StackID)
}

func PinnedStackKitReleaseForStack(ctx context.Context, tenant, stackID string) (*stackkitrelease.Release, error) {
	resolver, configured, err := StageCandidateForStack(ctx, tenant, stackID)
	if err != nil {
		return nil, err
	}
	if !configured {
		return configuredPinnedStackKitRelease()
	}
	pin := strings.TrimSpace(os.Getenv(stackKitReleasePinEnv))
	if !filepath.IsAbs(pin) {
		return nil, fmt.Errorf("candidate controller needs the existing absolute runtime pin locator")
	}
	return resolver.ResolveController(filepath.Dir(pin))
}

func UseCaseCatalogForStack(ctx context.Context, tenant, stackID string) (stackkitrelease.UseCaseCatalog, error) {
	resolver, configured, err := StageCandidateForStack(ctx, tenant, stackID)
	if err != nil {
		return stackkitrelease.UseCaseCatalog{}, err
	}
	if !configured {
		return stackkitrelease.ResolveUseCaseCatalog()
	}
	release, err := resolver.Resolve()
	if err != nil {
		return stackkitrelease.UseCaseCatalog{}, err
	}
	return release.CandidateUseCaseCatalog()
}
