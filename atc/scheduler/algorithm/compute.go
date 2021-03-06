package algorithm

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/tracing"
)

type Resolver interface {
	Resolve(context.Context) (map[string]*versionCandidate, db.ResolutionFailure, error)
	InputConfigs() InputConfigs
}

func New(versionsDB db.VersionsDB) *Algorithm {
	return &Algorithm{
		versionsDB: versionsDB,
	}
}

type Algorithm struct {
	versionsDB db.VersionsDB
}

func (a *Algorithm) Compute(
	ctx context.Context,
	job db.Job,
	inputs []atc.JobInput,
	resources db.Resources,
	relatedJobs NameToIDMap,
) (db.InputMapping, bool, bool, error) {
	ctx, span := tracing.StartSpan(ctx, "Algorithm.Compute", tracing.Attrs{
		"pipeline": job.PipelineName(),
		"job":      job.Name(),
	})
	defer span.End()

	resolvers, err := constructResolvers(a.versionsDB, job, inputs, resources, relatedJobs)
	if err != nil {
		return nil, false, false, fmt.Errorf("construct resolvers: %w", err)
	}

	inputMapper, err := newInputMapper(ctx, a.versionsDB, job.ID())
	if err != nil {
		return nil, false, false, fmt.Errorf("setting up input mapper: %w", err)
	}

	return a.computeResolvers(ctx, resolvers, inputMapper)
}

func (a *Algorithm) computeResolvers(
	ctx context.Context,
	resolvers []Resolver,
	inputMapper inputMapper,
) (db.InputMapping, bool, bool, error) {
	finalHasNext := false
	finalResolved := true
	finalMapping := db.InputMapping{}

	for _, resolver := range resolvers {
		versionCandidates, resolveErr, err := resolver.Resolve(ctx)
		if err != nil {
			return nil, false, false, fmt.Errorf("resolve: %w", err)
		}

		// determines if the algorithm successfully resolved all inputs depending
		// on if all resolvers did not return a resolve error
		finalResolved = finalResolved && (resolveErr == "")

		// converts the version candidates into an object that is recognizable by
		// other components. also computes the first occurrence for all satisfiable
		// inputs
		finalMapping = inputMapper.candidatesToInputMapping(finalMapping, resolver.InputConfigs(), versionCandidates, resolveErr)

		// if any one of the resolvers has a version candidate that has an unused
		// next every version, the algorithm should return true for being able to
		// be run again
		finalHasNext = finalHasNext || a.finalizeHasNext(versionCandidates)
	}

	return finalMapping, finalResolved, finalHasNext, nil
}

func (a *Algorithm) finalizeHasNext(versionCandidates map[string]*versionCandidate) bool {
	hasNextCombined := false
	for _, candidate := range versionCandidates {
		hasNextCombined = hasNextCombined || candidate.HasNextEveryVersion
	}

	return hasNextCombined
}
