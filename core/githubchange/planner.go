package githubchange

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

type ContentInput struct {
	Path          string
	Message       string
	ExpectedState string
	ExpectedSHA   string
	Content       []byte
	FinalStatus   string
}

type BuildInput struct {
	RepositoryID string
	Scope        Scope
	Branch       BranchChange
	Title        string
	Body         string
	Files        []ContentInput
}

type BuildResult struct {
	Scope Scope
	Files []PlannedFile
	Steps []operations.Step
}

func Build(input BuildInput) (BuildResult, error) {
	if input.RepositoryID == "" || len(input.Files) == 0 || len(input.Files) > maxChangeFiles {
		return BuildResult{}, errors.New("GitHub change build input is incomplete")
	}
	files := append([]ContentInput(nil), input.Files...)
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	planned := make([]PlannedFile, len(files))
	for index, file := range files {
		if index > 0 && files[index-1].Path == file.Path {
			return BuildResult{}, errors.New("GitHub change build input contains duplicate files")
		}
		planned[index] = PlannedFile{
			Path: file.Path, Status: file.FinalStatus, ContentDigest: DigestContent(file.Content),
		}
	}
	digest, err := ChangeSetDigest(input.Scope, planned)
	if err != nil {
		return BuildResult{}, err
	}
	input.Scope.ChangeSetDigest = digest
	steps := make([]operations.Step, 0, len(files)+2)
	steps = append(steps, operationStep(
		input.RepositoryID, "ensure-change-branch", BranchAction,
		OperationParameters{Scope: input.Scope, Branch: &input.Branch},
	))
	for index, file := range files {
		steps = append(steps, operationStep(
			input.RepositoryID, fmt.Sprintf("put-change-file-%03d", index+1), ContentAction,
			OperationParameters{Scope: input.Scope, Content: &ContentChange{
				Path: file.Path, Message: file.Message, ExpectedState: file.ExpectedState,
				ExpectedSHA: file.ExpectedSHA, FinalStatus: file.FinalStatus,
				ContentBase64: base64.StdEncoding.EncodeToString(file.Content),
				ContentDigest: planned[index].ContentDigest,
			}},
		))
	}
	steps = append(steps, operationStep(
		input.RepositoryID, "open-change-draft-pr", DraftPRAction,
		OperationParameters{Scope: input.Scope, PullRequest: &PullRequestChange{
			Title: input.Title, Body: input.Body, Files: planned,
		}},
	))
	result := BuildResult{Scope: input.Scope, Files: planned, Steps: steps}
	if err := ValidatePlan(operations.Plan{Steps: steps}); err != nil {
		return BuildResult{}, err
	}
	return result, nil
}

func operationStep(
	repositoryID string,
	stepID string,
	action string,
	parameters OperationParameters,
) operations.Step {
	return operations.Step{
		StepID: stepID, RepositoryID: repositoryID, Action: action,
		RequiresApproval: true, Compensation: operations.Compensation{Mode: "manual"},
		Parameters: Parameters(parameters),
	}
}
