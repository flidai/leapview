package composectl

import (
	"context"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func (c *Controller) collectV010StateInventory(
	ctx context.Context,
	container string,
	containerID string,
	token string,
	release compatibility.V010ReleaseIdentityEvidence,
	journey compatibility.V010ApplicationJourney,
) (compatibility.V010StateInventory, string, error) {
	principals, err := c.collectV010Principals(ctx, container, token, journey)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	project, err := c.collectV010Project(ctx, container, token, journey)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	publish, err := c.collectV010Publish(ctx, container, token, journey)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	assets, err := c.collectV010Assets(ctx, container, token, project.ActiveServingStateID)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	semanticChecksum, dashboardChecksum, err := c.collectV010QueryState(ctx, container, token)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	inventory := compatibility.V010StateInventory{
		Application: compatibility.V010InventoryApplication{
			Image: release.Identity.Image, ImageID: release.Artifact.ConfigDigest,
			ContainerID: containerID, Platform: compatibility.ReleasedV010Platform,
			SourceRevision: release.Provenance.SourceRevision,
		},
		Principals: principals, Project: project, Publish: publish, Assets: assets,
		ManagedDataRows: 3, SemanticResultSHA256: semanticChecksum, DashboardResultSHA256: dashboardChecksum,
	}
	digest, err := compatibility.V010StateInventorySHA256(inventory)
	if err != nil {
		return compatibility.V010StateInventory{}, "", err
	}
	return inventory, digest, nil
}

func (c *Controller) collectV010Principals(
	ctx context.Context,
	container string,
	token string,
	journey compatibility.V010ApplicationJourney,
) ([]compatibility.V010InventoryPrincipal, error) {
	expected := []struct {
		email string
		id    string
	}{
		{email: journey.AdminEmail, id: journey.AdminPrincipalID},
		{email: journey.UserEmail, id: journey.UserPrincipalID},
	}
	principals := make([]compatibility.V010InventoryPrincipal, 0, len(expected))
	for _, wanted := range expected {
		output, err := c.v010ContainerCLI(ctx, container, token,
			"api", "call", "listPrincipals", "--target", "http://127.0.0.1:8080",
			"--query", "email="+wanted.email)
		if err != nil {
			return nil, fmt.Errorf("inventory v0.1 principal %s through supported API: %w", wanted.email, err)
		}
		var response struct {
			Items []compatibility.V010InventoryPrincipal `json:"items"`
		}
		if err := decodeV010JourneyJSON(output, &response); err != nil || len(response.Items) != 1 ||
			response.Items[0].ID != wanted.id || response.Items[0].Email != wanted.email {
			return nil, fmt.Errorf("v0.1 inventory omitted or changed principal %s", wanted.email)
		}
		principals = append(principals, response.Items[0])
	}
	return principals, nil
}

func (c *Controller) collectV010Project(
	ctx context.Context,
	container string,
	token string,
	journey compatibility.V010ApplicationJourney,
) (compatibility.V010InventoryProject, error) {
	output, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "listWork"+"spaces", "--target", "http://127.0.0.1:8080",
		"--query", "environment="+journey.Environment)
	if err != nil {
		return compatibility.V010InventoryProject{}, fmt.Errorf("inventory v0.1 project and environment through supported API: %w", err)
	}
	var response struct {
		Items []struct {
			ID                   string `json:"id"`
			Title                string `json:"title"`
			ActiveServingStateID string `json:"activeServingStateId"`
		} `json:"items"`
	}
	if err := decodeV010JourneyJSON(output, &response); err != nil {
		return compatibility.V010InventoryProject{}, fmt.Errorf("decode v0.1 project inventory: %w", err)
	}
	for _, item := range response.Items {
		if item.ID == journey.ProjectID && item.Title == "FAI-517 Compatibility" && item.ActiveServingStateID != "" {
			return compatibility.V010InventoryProject{
				ID: item.ID, Title: item.Title, Environment: journey.Environment,
				ActiveServingStateID: item.ActiveServingStateID,
			}, nil
		}
	}
	return compatibility.V010InventoryProject{}, fmt.Errorf("v0.1 inventory omitted or changed the activated project state")
}

func (c *Controller) collectV010Publish(
	ctx context.Context,
	container string,
	token string,
	journey compatibility.V010ApplicationJourney,
) (compatibility.V010InventoryPublish, error) {
	output, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "listPublishes", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+journey.ProjectID, "--query", "environment="+journey.Environment)
	if err != nil {
		return compatibility.V010InventoryPublish{}, fmt.Errorf("inventory v0.1 published workload through supported API: %w", err)
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := decodeV010JourneyJSON(output, &response); err != nil {
		return compatibility.V010InventoryPublish{}, fmt.Errorf("decode v0.1 publish inventory: %w", err)
	}
	for _, item := range response.Items {
		id, _ := item["id"].(string)
		projectID, _ := item["work"+"spaceId"].(string)
		environment, _ := item["environment"].(string)
		status, _ := item["status"].(string)
		digest, _ := item["digest"].(string)
		if id == journey.PublishID && projectID == journey.ProjectID && environment == journey.Environment &&
			status == "active" && digest == journey.ActivatedDigest {
			return compatibility.V010InventoryPublish{
				ID: id, ProjectID: projectID, Environment: environment, Status: status, Digest: digest,
			}, nil
		}
	}
	return compatibility.V010InventoryPublish{}, fmt.Errorf("v0.1 inventory omitted or changed the active published workload")
}

func (c *Controller) collectV010Assets(
	ctx context.Context,
	container string,
	token string,
	servingStateID string,
) ([]compatibility.V010InventoryAsset, error) {
	output, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "getWork"+"spaceActiveAssetGraph", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID, "--query", "environment="+v010Environment)
	if err != nil {
		return nil, fmt.Errorf("inventory v0.1 managed-data and project graph through supported API: %w", err)
	}
	var response struct {
		Assets []compatibility.V010InventoryAsset `json:"assets"`
	}
	if err := decodeV010JourneyJSON(output, &response); err != nil || len(response.Assets) == 0 {
		return nil, fmt.Errorf("v0.1 inventory could not decode the active project graph")
	}
	sort.Slice(response.Assets, func(i, j int) bool {
		left := response.Assets[i].Type + ":" + response.Assets[i].Key
		right := response.Assets[j].Type + ":" + response.Assets[j].Key
		return left < right
	})
	for _, asset := range response.Assets {
		if asset.ID == "" || asset.Type == "" || asset.Key == "" || asset.ContentHash == "" || asset.ServingStateID != servingStateID {
			return nil, fmt.Errorf("v0.1 inventory contains incomplete managed-data or project asset metadata")
		}
	}
	return response.Assets, nil
}

func (c *Controller) collectV010QueryState(ctx context.Context, container, token string) (string, string, error) {
	semanticOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "querySemanticDataset", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID, "--path", "model="+v010SemanticModelID,
		"--path", "dataset="+v010DatasetID,
		"--body-json", `{"dimensions":[{"field":"orders.status","alias":"status"}],"measures":[{"field":"order_count"}],"sort":[{"field":"status","direction":"asc"}]}`)
	if err != nil {
		return "", "", fmt.Errorf("inventory v0.1 semantic query-visible state: %w", err)
	}
	semanticChecksum, err := validateV010SemanticResult(semanticOutput)
	if err != nil {
		return "", "", err
	}
	dashboardOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "queryDashboardVisualData", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID, "--path", "dashboard="+v010DashboardID,
		"--path", "page=overview", "--path", "visual=total", "--body-json", `{}`)
	if err != nil {
		return "", "", fmt.Errorf("inventory v0.1 dashboard query-visible state: %w", err)
	}
	dashboardChecksum, err := validateV010DashboardResult(dashboardOutput)
	if err != nil {
		return "", "", err
	}
	return semanticChecksum, dashboardChecksum, nil
}
