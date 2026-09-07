#!/bin/sh
set -eu

GOTOOLCHAIN=go1.26.7 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote
go run ./internal/app/tools/configgen
go run ./internal/app/tools/layoutcontractgen

go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target leapview-v1
go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target leapview-v1
go run ./internal/app/tools/apigenpatch

go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target ui-signals
go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target ui-signals
go run ./internal/app/tools/signalcontracts

go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target desktop-discovery-contracts
go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target desktop-discovery-contracts

go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target data-resource-contracts
go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target data-resource-contracts
go run ./internal/project/contracts/generate

go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target visualization-ir
go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target visualization-ir

go run ./cmd/leapview schema export --format json-schema --out schemas/json
