package duckdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

// ProviderAdmission spans secret resolution, external reads and acknowledged
// detach/secret cleanup. Cancellation alone never acknowledges the drain.
type ProviderAdmission interface {
	Acquire(context.Context) (context.Context, func(), error)
}

type sourceProviderWork struct{ cleanupUncertain bool }

func (work *sourceProviderWork) cleanup(session analyticsresource.Session, model *semanticmodel.Model, attached map[string]struct{}) error {
	err := cleanupSourceAccess(session, model, attached)
	if err != nil {
		work.cleanupUncertain = true
	}
	return err
}

func (r *SourceRuntime) Prepare(ctx context.Context, model *semanticmodel.Model) (prepared analyticsmaterialize.PreparedSources, err error) {
	if r == nil || r.db == nil || model == nil {
		return nil, fmt.Errorf("source preparer and semantic model are required")
	}
	release := func() {}
	if r.providerAdmission != nil {
		ctx, release, err = r.providerAdmission.Acquire(ctx)
		if err != nil {
			return nil, err
		}
	}
	work := &sourceProviderWork{}
	returned := false
	defer func() {
		if recover() != nil {
			panic(connectionbinding.ErrProviderUnavailable)
		}
		// A panic or failed provider cleanup quarantines this lease. The process
		// cannot safely acknowledge a credential cutover with uncertain clients.
		if returned && !work.cleanupUncertain {
			release()
		}
	}()
	prepared, err = r.prepare(ctx, model, work)
	if errors.Is(err, connectionbinding.ErrProviderCleanupUncertain) {
		work.cleanupUncertain = true
	}
	returned = true
	return prepared, err
}

func (r *SourceRuntime) prepare(ctx context.Context, model *semanticmodel.Model, work *sourceProviderWork) (analyticsmaterialize.PreparedSources, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("source preparer is not initialized")
	}
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	session, err := r.db.Session(ctx)
	if err != nil {
		return nil, err
	}
	closeSession := true
	defer func() {
		if closeSession {
			if closer, ok := session.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	}()
	resolved, err := r.resolveCredentials(ctx, model)
	if err != nil {
		return nil, err
	}
	telemetry, _ := r.db.(refreshTelemetry)
	requiredExtensions := RequiredExtensions(resolved)
	if len(requiredExtensions) > 0 {
		if r.extensionAdmission == nil {
			return nil, fmt.Errorf("source preparation requires extension admission for %s", strings.Join(requiredExtensions, ", "))
		}
		for _, extension := range requiredExtensions {
			admitted, err := r.extensionAdmission.AdmitExtension(ctx, extension)
			if err != nil {
				return nil, fmt.Errorf("extension %s was not admitted: %w", extension, err)
			}
			if err := validateAdmittedExtension(extension, admitted); err != nil {
				return nil, err
			}
			if _, err := session.ExecContext(ctx, loadExtensionStatement(admitted.Path)); err != nil {
				return nil, fmt.Errorf("loading admitted extension %s: %w", extension, err)
			}
		}
	}
	releaseScopes := lockSourceScopes(resolved, telemetry)
	defer releaseScopes()
	prepared := &PreparedSources{model: resolved, session: session, relations: map[string]stagedRelation{}, relationQueries: map[string]string{}, telemetry: telemetry}
	prepared.reporter, _ = r.db.(fatalReporter)
	for _, sourceName := range sortedKeys(resolved.Sources) {
		source := resolved.Sources[sourceName]
		connection := resolved.Connections[source.Connection]
		if connection.Kind == "managed" {
			relation, err := SourceRelation(resolved, source)
			if err != nil {
				_ = prepared.Close()
				return nil, safeSourceError(sourceName, err)
			}
			columns, err := describeRelationSchema(ctx, session, "("+relation+")")
			if err != nil {
				_ = prepared.Close()
				return nil, safeSourceError(sourceName, err)
			}
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			resolved.Sources[sourceName] = source
			// Keep the resolved relation on the live prepared session so the
			// freshness observation seam can query it before Close releases the
			// target-owned connection.
			prepared.relations[sourceName] = stagedRelation{value: relation, kind: stagedRelationQuery}
			prepared.relationQueries[sourceName] = "(" + relation + ")"
			original := model.Sources[sourceName]
			original.Schema = source.Schema
			model.Sources[sourceName] = original
			continue
		}
		sourceModel := refreshSourceModel(resolved, sourceName, source)
		attached := map[string]struct{}{}
		if err := prepareRefreshSourceAccess(ctx, session, sourceModel, attached); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			cleanupErr := work.cleanup(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, fmt.Errorf("preparing refresh source %q failed", sourceName)
		}
		relation, err := SourceRelation(sourceModel, source)
		if err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := work.cleanup(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		table := fmt.Sprintf("leapview_stage_%d_%s", sourceStageSequence.Add(1), sourceName)
		if err := validateIdentifier(table); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := work.cleanup(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, err
		}
		if _, err := session.ExecContext(ctx, "CREATE TEMP TABLE "+quoteIdentifier(table)+" AS SELECT * FROM ("+relation+")"); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := work.cleanup(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		prepared.tables = append(prepared.tables, table)
		prepared.relations[sourceName] = stagedRelation{value: quoteIdentifier(table), kind: stagedRelationTable}
		prepared.relationQueries[sourceName] = quoteIdentifier(table)
		columns, err := describeRelationSchema(ctx, session, quoteIdentifier(table))
		if err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := work.cleanup(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		source.Schema = semanticmodel.TableSchema{Columns: columns}
		resolved.Sources[sourceName] = source
		original := model.Sources[sourceName]
		original.Schema = source.Schema
		model.Sources[sourceName] = original
		if err := work.cleanup(session, sourceModel, attached); err != nil {
			reportCleanup(r.db, telemetry, err)
			_ = prepared.Close()
			return nil, fmt.Errorf("cleaning refresh source %q access failed", sourceName)
		}
		reportCleanup(r.db, telemetry, nil)
		observeSource(telemetry, connection.Kind, "succeeded")
	}
	if err := resolved.ValidateDiscoveredSourceSchemas(); err != nil {
		_ = prepared.Close()
		return nil, fmt.Errorf("validating staged source schemas: %w", err)
	}
	closeSession = false
	return prepared, nil
}
