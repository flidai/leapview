package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/google/uuid"
)

// MaxDeliveryCollectionPageSize is the hard bound for native delivery
// discovery. Collection callers must never turn a project-scoped incident
// read into an unbounded history scan.
const MaxDeliveryCollectionPageSize int32 = 200

type DeliveryPublicationPage struct {
	Items      []DeliveryPublication
	NextCursor *string
}

type DeliveryGenerationPage struct {
	Items      []DeliveryGeneration
	NextCursor *string
}

type DeliveryPlanPage struct {
	Items      []DeliveryPlan
	NextCursor *string
}

type DeliveryBuildAttemptPage struct {
	Items      []DeliveryBuildAttempt
	NextCursor *string
}

type DeliveryCandidatePage struct {
	Items      []DeliveryCandidate
	NextCursor *string
}

type ApprovalRequestPage struct {
	Items      []ApprovalRequest
	NextCursor *string
}

type deliveryCursor struct {
	ID string `json:"id"`
}

func deliveryPageArgs(limit int32, token string) (int32, string, error) {
	if limit < 1 || limit > MaxDeliveryCollectionPageSize {
		return 0, "", fmt.Errorf("%w: delivery page limit is outside bound", ErrInvalid)
	}
	if len(token) > 2048 {
		return 0, "", fmt.Errorf("%w: delivery page token is too long", ErrInvalid)
	}
	if strings.TrimSpace(token) == "" {
		return limit + 1, "", nil
	}
	if !strings.HasPrefix(token, "k1.") {
		return 0, "", fmt.Errorf("%w: invalid delivery page token", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "k1."))
	if err != nil {
		return 0, "", fmt.Errorf("%w: invalid delivery page token", ErrInvalid)
	}
	var cursor deliveryCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ID == "" {
		return 0, "", fmt.Errorf("%w: invalid delivery page token", ErrInvalid)
	}
	id, err := uuid.Parse(cursor.ID)
	if err != nil {
		return 0, "", fmt.Errorf("%w: invalid delivery page token", ErrInvalid)
	}
	return limit + 1, id.String(), nil
}

func deliveryNextCursor(id string) *string {
	payload, _ := json.Marshal(deliveryCursor{ID: id})
	token := "k1." + base64.RawURLEncoding.EncodeToString(payload)
	return &token
}

func deliveryPageIDs(limit int32, token string, fetch func(int32, string) ([]string, error)) ([]string, *string, error) {
	pageLimit, afterID, err := deliveryPageArgs(limit, token)
	if err != nil {
		return nil, nil, err
	}
	ids, err := fetch(pageLimit, afterID)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if len(ids) > int(limit) {
		ids = ids[:limit]
		if len(ids) > 0 {
			next = deliveryNextCursor(ids[len(ids)-1])
		}
	}
	return ids, next, nil
}

func validateDeliveryCollectionScope(projectID, targetID, environment string) (string, string, string, error) {
	projectID, err := textID(projectID, "project id")
	if err != nil {
		return "", "", "", err
	}
	targetID, err = textID(targetID, "target id")
	if err != nil {
		return "", "", "", err
	}
	environment, err = textID(environment, "environment")
	if err != nil {
		return "", "", "", err
	}
	return projectID, targetID, environment, nil
}

func (r *Repository) validateDeliveryCursorTarget(ctx context.Context, kind, afterID, projectID, targetID, environment string) error {
	if afterID == "" {
		return nil
	}
	var rowTarget string
	switch kind {
	case "publication":
		row, err := r.Publication(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not a publication in this scope", ErrInvalid)
		}
		rowTarget = row.TargetID
	case "generation":
		row, err := r.Generation(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not a generation in this scope", ErrInvalid)
		}
		rowTarget = row.TargetID
	case "plan":
		row, err := r.Plan(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not a plan in this scope", ErrInvalid)
		}
		rowTarget = row.TargetID
	case "build":
		row, err := r.BuildAttempt(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not a build attempt in this scope", ErrInvalid)
		}
		plan, err := r.Plan(ctx, row.PlanID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor build plan is unavailable", ErrInvalid)
		}
		rowTarget = plan.TargetID
	case "candidate":
		row, err := r.Candidate(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not a candidate in this scope", ErrInvalid)
		}
		rowTarget = row.TargetID
	case "approval":
		row, err := r.ApprovalRequest(ctx, afterID)
		if err != nil {
			return fmt.Errorf("%w: delivery cursor is not an approval request in this scope", ErrInvalid)
		}
		rowTarget = row.TargetID
	default:
		return fmt.Errorf("%w: unsupported delivery cursor collection", ErrInvalid)
	}
	if rowTarget != targetID {
		return fmt.Errorf("%w: delivery cursor is outside the requested target", ErrInvalid)
	}
	target, err := r.Target(ctx, targetID)
	if err != nil || target.ProjectID != projectID || target.Environment != environment {
		return fmt.Errorf("%w: delivery cursor is outside the requested project", ErrInvalid)
	}
	return nil
}

// ListPlans returns immutable target-owned plans in deterministic, bounded
// pages. Expired plans remain visible because they are lifecycle evidence.
func (r *Repository) ListPlans(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (DeliveryPlanPage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return DeliveryPlanPage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "plan", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListDeliveryPlanIDs(ctx, depdb.ListDeliveryPlanIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return DeliveryPlanPage{}, err
	}
	items := make([]DeliveryPlan, 0, len(ids))
	for _, id := range ids {
		item, err := r.Plan(ctx, id)
		if err != nil {
			return DeliveryPlanPage{}, err
		}
		items = append(items, item)
	}
	return DeliveryPlanPage{Items: items, NextCursor: next}, nil
}

// ListBuildAttempts returns immutable build lifecycle and termination evidence
// in deterministic, target-scoped pages. Running, aborted, and indeterminate
// attempts are intentionally retained for incident recovery.
func (r *Repository) ListBuildAttempts(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (DeliveryBuildAttemptPage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return DeliveryBuildAttemptPage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "build", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListDeliveryBuildAttemptIDs(ctx, depdb.ListDeliveryBuildAttemptIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return DeliveryBuildAttemptPage{}, err
	}
	items := make([]DeliveryBuildAttempt, 0, len(ids))
	for _, id := range ids {
		item, err := r.BuildAttempt(ctx, id)
		if err != nil {
			return DeliveryBuildAttemptPage{}, err
		}
		items = append(items, item)
	}
	return DeliveryBuildAttemptPage{Items: items, NextCursor: next}, nil
}

// ListCandidates returns immutable candidate lifecycle evidence in
// deterministic, target-scoped pages. Rejected and retired candidates remain
// visible so an operator can explain why a build was not publishable.
func (r *Repository) ListCandidates(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (DeliveryCandidatePage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return DeliveryCandidatePage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "candidate", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListDeliveryCandidateIDs(ctx, depdb.ListDeliveryCandidateIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return DeliveryCandidatePage{}, err
	}
	items := make([]DeliveryCandidate, 0, len(ids))
	for _, id := range ids {
		item, err := r.Candidate(ctx, id)
		if err != nil {
			return DeliveryCandidatePage{}, err
		}
		items = append(items, item)
	}
	return DeliveryCandidatePage{Items: items, NextCursor: next}, nil
}

// ApprovalRequest returns immutable request evidence and its latest
// append-only decision. It intentionally does not expose credential metadata
// through the HTTP adapter; the authority needs it internally for audit and
// separation-of-duty checks.
func (r *Repository) ApprovalRequest(ctx context.Context, id string) (ApprovalRequest, error) {
	db, err := requireDB(r)
	if err != nil {
		return ApprovalRequest{}, err
	}
	id, err = uuidID(id, "approval request id", false)
	if err != nil {
		return ApprovalRequest{}, err
	}
	return loadApprovalRequest(ctx, db, id)
}

// ListApprovalRequests returns immutable request and latest-decision evidence
// in deterministic, target-scoped pages. Expired requests are retained for
// audit and recovery diagnosis rather than filtered as if they never existed.
func (r *Repository) ListApprovalRequests(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (ApprovalRequestPage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return ApprovalRequestPage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "approval", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListDeliveryApprovalRequestIDs(ctx, depdb.ListDeliveryApprovalRequestIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return ApprovalRequestPage{}, err
	}
	db, err := requireDB(r)
	if err != nil {
		return ApprovalRequestPage{}, err
	}
	items := make([]ApprovalRequest, 0, len(ids))
	for _, id := range ids {
		item, err := loadApprovalRequest(ctx, db, id)
		if err != nil {
			return ApprovalRequestPage{}, err
		}
		items = append(items, item)
	}
	return ApprovalRequestPage{Items: items, NextCursor: next}, nil
}

// ListPublications returns immutable publication evidence in deterministic,
// bounded pages scoped by the target's project and environment.
func (r *Repository) ListPublications(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (DeliveryPublicationPage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return DeliveryPublicationPage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "publication", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListDeliveryPublicationIDs(ctx, depdb.ListDeliveryPublicationIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return DeliveryPublicationPage{}, err
	}
	items := make([]DeliveryPublication, 0, len(ids))
	for _, id := range ids {
		item, err := r.Publication(ctx, id)
		if err != nil {
			return DeliveryPublicationPage{}, err
		}
		items = append(items, item)
	}
	return DeliveryPublicationPage{Items: items, NextCursor: next}, nil
}

// ListRetainedGenerations returns only generations with a currently live or
// retiring generation retention root. Expired roots are deliberately excluded
// so a rollback picker cannot offer a target that the authority no longer
// retains.
func (r *Repository) ListRetainedGenerations(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (DeliveryGenerationPage, error) {
	projectID, targetID, environment, err := validateDeliveryCollectionScope(projectID, targetID, environment)
	if err != nil {
		return DeliveryGenerationPage{}, err
	}
	ids, next, err := deliveryPageIDs(limit, token, func(pageLimit int32, afterID string) ([]string, error) {
		db, err := requireDB(r)
		if err != nil {
			return nil, err
		}
		if err := r.validateDeliveryCursorTarget(ctx, "generation", afterID, projectID, targetID, environment); err != nil {
			return nil, err
		}
		return depdb.New(db).ListRetainedDeliveryGenerationIDs(ctx, depdb.ListRetainedDeliveryGenerationIDsParams{ProjectID: projectID, TargetID: targetID, Environment: environment, AfterID: afterID, PageLimit: pageLimit})
	})
	if err != nil {
		return DeliveryGenerationPage{}, err
	}
	items := make([]DeliveryGeneration, 0, len(ids))
	for _, id := range ids {
		item, err := r.Generation(ctx, id)
		if err != nil {
			return DeliveryGenerationPage{}, err
		}
		items = append(items, item)
	}
	return DeliveryGenerationPage{Items: items, NextCursor: next}, nil
}
