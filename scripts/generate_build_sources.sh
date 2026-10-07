#!/bin/sh
set -eu

# Match setup-ci's HTTP/1.1 workaround for checksum-service HTTP/2 stream
# failures; transport only, with sqlc checksums and TLS integrity unchanged.
./scripts/time_build_phase.sh sqlc env GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.7 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote
./scripts/time_build_phase.sh config go run ./internal/app/tools/configgen
./scripts/time_build_phase.sh layout-contract go run ./internal/app/tools/layoutcontractgen

./scripts/time_build_phase.sh leapview-v1-typespec go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target leapview-v1
./scripts/time_build_phase.sh leapview-v1 go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target leapview-v1
./scripts/time_build_phase.sh api-patch go run ./internal/app/tools/apigenpatch

./scripts/time_build_phase.sh ui-signals-typespec go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target ui-signals
./scripts/time_build_phase.sh ui-signals go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target ui-signals
./scripts/time_build_phase.sh signal-contracts go run ./internal/app/tools/signalcontracts

./scripts/time_build_phase.sh desktop-discovery-typespec go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target desktop-discovery-contracts
./scripts/time_build_phase.sh desktop-discovery go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target desktop-discovery-contracts

./scripts/time_build_phase.sh data-resource-typespec go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target data-resource-contracts
./scripts/time_build_phase.sh data-resource go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target data-resource-contracts
./scripts/time_build_phase.sh project-contracts go run ./internal/project/contracts/generate

./scripts/time_build_phase.sh visualization-ir-typespec go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target visualization-ir
./scripts/time_build_phase.sh visualization-ir go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target visualization-ir

./scripts/time_build_phase.sh json-schema go run ./cmd/leapview schema export --format json-schema --out schemas/json
