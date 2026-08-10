#!/bin/sh
set -eu

APIGEN=github.com/Yacobolo/toolbelt/apigen/cmd/apigen

go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
go run ./internal/app/tools/configgen
go run ./internal/app/tools/layoutcontractgen

go run "$APIGEN" typespec-compile -manifest api/apigen.yaml -target leapview-v1
go run "$APIGEN" all -manifest api/apigen.yaml -target leapview-v1
go run ./internal/app/tools/apigenpatch

go run "$APIGEN" typespec-compile -manifest api/apigen.yaml -target ui-signals
go run "$APIGEN" all -manifest api/apigen.yaml -target ui-signals
go run ./internal/app/tools/signalcontracts

go run "$APIGEN" typespec-compile -manifest api/apigen.yaml -target desktop-discovery-contracts
go run "$APIGEN" all -manifest api/apigen.yaml -target desktop-discovery-contracts

go run "$APIGEN" typespec-compile -manifest api/apigen.yaml -target visualization-ir
go run "$APIGEN" all -manifest api/apigen.yaml -target visualization-ir

go run ./cmd/leapview schema export --format json-schema --out schemas/json
