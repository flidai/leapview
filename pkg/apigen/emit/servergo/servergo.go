// Package servergo emits Go server scaffolding from JSON IR.
package servergo

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	agenttoolemit "github.com/Yacobolo/toolbelt/apigen/emit/agenttool"
	openapiemit "github.com/Yacobolo/toolbelt/apigen/emit/openapi"
	"github.com/Yacobolo/toolbelt/apigen/ir"
	"go.yaml.in/yaml/v4"
)

// Options configures Go server emission.
type Options struct {
	PackageName             string
	EmbeddedOpenAPISpecJSON string
}

// Emit renders Go server scaffolding from IR.
func Emit(doc ir.Document, opts Options) ([]byte, error) {
	if err := ir.Validate(doc); err != nil {
		return nil, fmt.Errorf("validate ir document: %w", err)
	}
	normalized := cloneDocumentForEmit(doc)
	if err := ir.Normalize(&normalized); err != nil {
		return nil, fmt.Errorf("normalize ir document: %w", err)
	}
	return emit(normalized, opts)
}

func cloneDocumentForEmit(doc ir.Document) ir.Document {
	clone := doc
	clone.Endpoints = append([]ir.Endpoint(nil), doc.Endpoints...)
	for i := range clone.Endpoints {
		endpoint := &clone.Endpoints[i]
		endpoint.Tags = append([]string(nil), endpoint.Tags...)
		endpoint.Parameters = append([]ir.Parameter(nil), endpoint.Parameters...)
		for j := range endpoint.Parameters {
			if endpoint.Parameters[j].Explode != nil {
				explode := *endpoint.Parameters[j].Explode
				endpoint.Parameters[j].Explode = &explode
			}
		}
		if endpoint.RequestBody != nil {
			requestBody := *endpoint.RequestBody
			requestBody.Contents = append([]ir.BodyContent(nil), endpoint.RequestBody.Contents...)
			for k := range requestBody.Contents {
				content := &requestBody.Contents[k]
				if content.Schema != nil {
					schema := *content.Schema
					content.Schema = &schema
				}
				content.AnyOf = append([]ir.SchemaRef(nil), content.AnyOf...)
				content.Parts = append([]ir.MultipartPart(nil), content.Parts...)
			}
			endpoint.RequestBody = &requestBody
		}
		if endpoint.CLI != nil {
			endpoint.CLI = cloneCLI(endpoint.CLI)
		}
		if endpoint.Command != nil {
			command := *endpoint.Command
			command.AdditionalExposures = append([]string(nil), endpoint.Command.AdditionalExposures...)
			if endpoint.Command.Target != nil {
				target := *endpoint.Command.Target
				command.Target = &target
			}
			endpoint.Command = &command
		}
		endpoint.Security = cloneSecurityRequirements(endpoint.Security)
		endpoint.Responses = append([]ir.Response(nil), endpoint.Responses...)
		for j := range endpoint.Responses {
			response := &endpoint.Responses[j]
			response.Headers = append([]ir.Header(nil), response.Headers...)
			response.Contents = append([]ir.BodyContent(nil), response.Contents...)
			for k := range response.Contents {
				content := &response.Contents[k]
				if content.Schema != nil {
					schema := *content.Schema
					content.Schema = &schema
				}
				content.AnyOf = append([]ir.SchemaRef(nil), content.AnyOf...)
				content.Parts = append([]ir.MultipartPart(nil), content.Parts...)
			}
		}
	}
	return clone
}

func cloneCLI(cli *ir.CLI) *ir.CLI {
	clone := *cli
	clone.Command = append([]string(nil), cli.Command...)
	clone.Args = append([]ir.CLIArg(nil), cli.Args...)
	if cli.Output != nil {
		output := *cli.Output
		output.TableColumns = append([]string(nil), cli.Output.TableColumns...)
		output.QuietFields = append([]string(nil), cli.Output.QuietFields...)
		clone.Output = &output
	}
	if cli.Pagination != nil {
		pagination := *cli.Pagination
		clone.Pagination = &pagination
	}
	return &clone
}

func cloneSecurityRequirements(requirements []ir.SecurityRequirement) []ir.SecurityRequirement {
	clone := append([]ir.SecurityRequirement(nil), requirements...)
	for i := range clone {
		if clone[i] == nil {
			continue
		}
		requirement := make(ir.SecurityRequirement, len(clone[i]))
		for name, scopes := range clone[i] {
			requirement[name] = append([]string(nil), scopes...)
		}
		clone[i] = requirement
	}
	return clone
}

func emit(doc ir.Document, opts Options) ([]byte, error) {
	toolContracts, err := agenttoolemit.Build(doc)
	if err != nil {
		return nil, err
	}
	toolContractsJSON, err := json.Marshal(toolContracts)
	if err != nil {
		return nil, fmt.Errorf("marshal agent tool contracts: %w", err)
	}
	specJSON := opts.EmbeddedOpenAPISpecJSON
	if specJSON == "" {
		var err error
		specJSON, err = emitSpecJSON(doc)
		if err != nil {
			return nil, err
		}
	}

	var b strings.Builder
	packageName := packageName(opts)
	usesTime := docUsesTimeTypes(doc)
	// The generated registration surface always exposes strict dispatch and its
	// injected error responder, including health-only APIs.
	hasStrictOperations := true
	hasRequestBodies := false
	hasMultipartBodies := false
	hasFileBodies := docUsesFileBodies(doc)
	usesFmt := hasFileBodies
	hasTools := len(toolContracts) > 0
	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID != "getHealth" {
			hasStrictOperations = true
		}
		if endpoint.RequestBody != nil {
			hasRequestBodies = true
			usesFmt = true
			if content, ok := ir.PrimaryRequestBodyContent(endpoint); ok && content.BodyKind == "multipart" {
				hasMultipartBodies = true
			}
		}
		for _, response := range endpoint.Responses {
			if len(responseHeaderFieldsWithDefaults(response)) > 0 {
				usesFmt = true
			}
		}
	}
	b.WriteString("package ")
	b.WriteString(packageName)
	b.WriteString("\n\n")
	b.WriteString("import (\n")
	if hasStrictOperations {
		b.WriteString("\t\"context\"\n")
		if usesFmt {
			b.WriteString("\t\"fmt\"\n")
		}
		if hasRequestBodies || hasFileBodies {
			b.WriteString("\t\"io\"\n")
		}
		if hasRequestBodies || hasFileBodies {
			b.WriteString("\t\"mime\"\n")
		}
		if hasMultipartBodies {
			b.WriteString("\t\"os\"\n")
		}
		if hasRequestBodies {
			b.WriteString("\t\"strings\"\n")
		}
	}
	b.WriteString("\t\"encoding/json\"\n")
	b.WriteString("\t\"net/http\"\n\n")
	if usesTime {
		b.WriteString("\t\"time\"\n\n")
	}
	b.WriteString("\tapigenchi \"github.com/Yacobolo/toolbelt/apigen/runtime/chi\"\n")
	b.WriteString("\tapigencommand \"github.com/Yacobolo/toolbelt/apigen/runtime/command\"\n")
	if hasTools {
		b.WriteString("\tapigenagenttool \"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool\"\n")
	}
	b.WriteString(")\n\n")
	b.WriteString("const embeddedOpenAPISpecJSON = `")
	b.WriteString(specJSON)
	b.WriteString("`\n\n")
	b.WriteString("// GetEmbeddedOpenAPISpec returns the canonical OpenAPI document as generic JSON map.\n")
	b.WriteString("func GetEmbeddedOpenAPISpec() (map[string]any, error) {\n")
	b.WriteString("\tvar doc map[string]any\n")
	b.WriteString("\tif err := json.Unmarshal([]byte(embeddedOpenAPISpecJSON), &doc); err != nil {\n")
	b.WriteString("\t\treturn nil, err\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn doc, nil\n")
	b.WriteString("}\n\n")
	if hasTools {
		b.WriteString("const embeddedAPIGenToolContractsJSON = `")
		b.Write(toolContractsJSON)
		b.WriteString("`\n\n")
		b.WriteString("var genAPIGenToolContracts = func() map[string]apigenagenttool.Contract {\n")
		b.WriteString("\tcontracts, err := apigenagenttool.DecodeContracts([]byte(embeddedAPIGenToolContractsJSON))\n")
		b.WriteString("\tif err != nil { panic(err) }\n")
		b.WriteString("\treturn contracts\n")
		b.WriteString("}()\n\n")
		b.WriteString("// GetAPIGenToolContracts returns defensive copies of generated endpoint-derived tools.\n")
		b.WriteString("func GetAPIGenToolContracts() map[string]apigenagenttool.Contract {\n")
		b.WriteString("\tout := make(map[string]apigenagenttool.Contract, len(genAPIGenToolContracts))\n")
		b.WriteString("\tfor name, contract := range genAPIGenToolContracts { out[name] = apigenagenttool.CloneContract(contract) }\n")
		b.WriteString("\treturn out\n")
		b.WriteString("}\n\n")
		b.WriteString("// GetAPIGenToolContract returns one generated endpoint-derived tool.\n")
		b.WriteString("func GetAPIGenToolContract(name string) (apigenagenttool.Contract, bool) {\n")
		b.WriteString("\tcontract, ok := genAPIGenToolContracts[name]\n")
		b.WriteString("\tif !ok { return apigenagenttool.Contract{}, false }\n")
		b.WriteString("\treturn apigenagenttool.CloneContract(contract), true\n")
		b.WriteString("}\n\n")
	}
	b.WriteString("// GenOperationContract captures APIGen-owned contract metadata for one operation.\n")
	b.WriteString("type GenOperationKind string\n\n")
	b.WriteString("const (\n\tGenOperationKindCommand GenOperationKind = \"command\"\n\tGenOperationKindQuery GenOperationKind = \"query\"\n)\n\n")
	b.WriteString("type GenOperationSurface string\n\n")
	b.WriteString("const (\n\tGenOperationSurfaceUI GenOperationSurface = \"ui\"\n\tGenOperationSurfaceAgent GenOperationSurface = \"agent\"\n\tGenOperationSurfaceAutomation GenOperationSurface = \"automation\"\n)\n\n")
	b.WriteString("type GenAuditPolicy struct {\n\tRequired bool\n\tSuccessAction string\n\tGuarantee string\n}\n\n")
	b.WriteString("type GenOperationTarget struct {\n\tParameter string\n\tType string\n}\n\n")
	b.WriteString("type GenCommandContract struct {\n")
	b.WriteString("\tOwner string\n\tAudit GenAuditPolicy\n\tAdditionalExposures []GenOperationSurface\n\tTarget *GenOperationTarget\n")
	b.WriteString("\tIdempotency string\n\tConcurrency string\n\tAuthzMode string\n\tPrivilege string\n")
	b.WriteString("}\n\n")
	b.WriteString("type GenOperationContract struct {\n")
	b.WriteString("\tOperationID string\n")
	b.WriteString("\tKind GenOperationKind\n")
	b.WriteString("\tNamespace string\n")
	b.WriteString("\tMethod string\n")
	b.WriteString("\tPath string\n")
	b.WriteString("\tTags []string\n")
	b.WriteString("\tDocumentedStatusCodes []int\n")
	b.WriteString("\tRequestBodyRequired bool\n")
	b.WriteString("\tAuthzMode string\n")
	b.WriteString("\tProtected bool\n")
	b.WriteString("\tManual bool\n")
	b.WriteString("\tCommand *GenCommandContract\n")
	b.WriteString("\tExtensions map[string]any\n")
	b.WriteString("}\n\n")
	b.WriteString("var genOperationContracts = map[string]GenOperationContract{\n")
	for _, endpoint := range doc.Endpoints {
		extensions, err := renderGoAnyMap(endpoint.Extensions)
		if err != nil {
			return nil, fmt.Errorf("render operation %q extensions: %w", endpoint.OperationID, err)
		}
		command := renderGenCommandContract(endpoint.Command)
		fmt.Fprintf(&b, "\t%q: {OperationID: %q, Kind: %q, Namespace: %q, Method: %q, Path: %q, Tags: %s, DocumentedStatusCodes: %s, RequestBodyRequired: %t, AuthzMode: %q, Protected: %t, Manual: %t, Command: %s, Extensions: %s},\n",
			endpoint.OperationID,
			endpoint.OperationID,
			endpoint.Kind,
			endpoint.Namespace,
			strings.ToUpper(endpoint.Method),
			ir.JoinAPIPath(doc.API.BasePath, endpoint.Path),
			renderGoStringSlice(endpoint.Tags),
			renderGoIntSlice(documentedStatusCodes(doc, endpoint)),
			endpoint.RequestBody != nil && endpoint.RequestBody.Required,
			endpointAuthzMode(endpoint),
			endpointProtected(endpoint),
			endpointManual(endpoint),
			command,
			extensions,
		)
	}
	b.WriteString("}\n\n")
	b.WriteString("// GetAPIGenOperationContracts returns a defensive copy of the generated contract registry.\n")
	b.WriteString("func GetAPIGenOperationContracts() map[string]GenOperationContract {\n")
	b.WriteString("\tout := make(map[string]GenOperationContract, len(genOperationContracts))\n")
	b.WriteString("\tfor operationID, contract := range genOperationContracts {\n")
	b.WriteString("\t\tout[operationID] = cloneAPIGenOperationContract(contract)\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn out\n")
	b.WriteString("}\n\n")
	b.WriteString("// GetAPIGenOperationContract returns generated contract metadata for a single operation.\n")
	b.WriteString("func GetAPIGenOperationContract(operationID string) (GenOperationContract, bool) {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\treturn GenOperationContract{}, false\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn cloneAPIGenOperationContract(contract), true\n")
	b.WriteString("}\n\n")
	b.WriteString("// GetAPIGenCommandRuntimeContract returns the normalized runtime audit contract for a generated command.\n")
	b.WriteString("func GetAPIGenCommandRuntimeContract(operationID string) (apigencommand.Contract, bool) {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n")
	b.WriteString("\tif !ok || contract.Command == nil || !contract.Command.Audit.Required { return apigencommand.Contract{}, false }\n")
	b.WriteString("\treturn apigencommand.Contract{OperationID: contract.OperationID, Owner: contract.Command.Owner, AuditAction: contract.Command.Audit.SuccessAction, Guarantee: apigencommand.Guarantee(contract.Command.Audit.Guarantee)}, true\n")
	b.WriteString("}\n\n")
	b.WriteString("// APIGenOperationAllowsStatus reports whether a status code is documented for an operation.\n")
	b.WriteString("//nolint:revive // exported generated helper name matches the APIGen contract registry namespace.\n")
	b.WriteString("func APIGenOperationAllowsStatus(operationID string, statusCode int) bool {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\treturn false\n")
	b.WriteString("\t}\n")
	b.WriteString("\tfor _, documented := range contract.DocumentedStatusCodes {\n")
	b.WriteString("\t\tif documented == statusCode {\n")
	b.WriteString("\t\t\treturn true\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn false\n")
	b.WriteString("}\n\n")
	b.WriteString("func cloneAPIGenOperationContract(contract GenOperationContract) GenOperationContract {\n")
	b.WriteString("\tcontract.Tags = append([]string(nil), contract.Tags...)\n")
	b.WriteString("\tcontract.DocumentedStatusCodes = append([]int(nil), contract.DocumentedStatusCodes...)\n")
	b.WriteString("\tif contract.Command != nil { command := *contract.Command; command.AdditionalExposures = append([]GenOperationSurface(nil), contract.Command.AdditionalExposures...); if contract.Command.Target != nil { target := *contract.Command.Target; command.Target = &target }; contract.Command = &command }\n")
	b.WriteString("\tcontract.Extensions = cloneAPIGenAnyMap(contract.Extensions)\n")
	b.WriteString("\treturn contract\n")
	b.WriteString("}\n\n")
	b.WriteString("func cloneAPIGenAnyMap(in map[string]any) map[string]any {\n")
	b.WriteString("\tif in == nil {\n")
	b.WriteString("\t\treturn nil\n")
	b.WriteString("\t}\n")
	b.WriteString("\tout := make(map[string]any, len(in))\n")
	b.WriteString("\tfor key, value := range in {\n")
	b.WriteString("\t\tout[key] = cloneAPIGenAny(value)\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn out\n")
	b.WriteString("}\n\n")
	b.WriteString("func cloneAPIGenAny(value any) any {\n")
	b.WriteString("\tswitch typed := value.(type) {\n")
	b.WriteString("\tcase map[string]any:\n")
	b.WriteString("\t\treturn cloneAPIGenAnyMap(typed)\n")
	b.WriteString("\tcase []any:\n")
	b.WriteString("\t\tout := make([]any, len(typed))\n")
	b.WriteString("\t\tfor i, item := range typed {\n")
	b.WriteString("\t\t\tout[i] = cloneAPIGenAny(item)\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\treturn out\n")
	b.WriteString("\tdefault:\n")
	b.WriteString("\t\treturn typed\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")
	b.WriteString("// GenServerInterface dispatches generated operations.\n")
	b.WriteString("type GenServerInterface interface {\n")
	b.WriteString("\tHandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request)\n")
	b.WriteString("}\n\n")
	b.WriteString("// RegisterAPIGenRoutes mounts generated routes on the supported Chi runtime boundary.\n")
	b.WriteString("func RegisterAPIGenRoutes(router apigenchi.Router, server GenServerInterface) {\n")
	b.WriteString("\tapigenchi.RegisterRoutes(router, []apigenchi.Route{\n")
	for _, endpoint := range doc.Endpoints {
		method := strings.ToUpper(endpoint.Method)
		fmt.Fprintf(&b, "\t\t{Method: %q, Path: %q, OperationID: %q},\n", method, ir.JoinAPIPath(doc.API.BasePath, endpoint.Path), endpoint.OperationID)
	}
	b.WriteString("\t}, server.HandleAPIGen)\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("// RegisterAPIGenStrictRoutes mounts generated routes backed by strict handlers.\n")
	b.WriteString("func RegisterAPIGenStrictRoutes(router apigenchi.Router, handler GenStrictServerInterface, responder GenTransportErrorResponder) {\n")
	b.WriteString("\tRegisterAPIGenRoutes(router, genStrictAdapter{handler: handler, responder: responder})\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("// GenOperationDispatcher is the dispatch target for generated operations.\n")
	b.WriteString("type GenOperationDispatcher interface {\n")
	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID == "getHealth" {
			continue
		}
		name := exportedName(endpoint.OperationID)
		signature := "\t" + name + "(w http.ResponseWriter, r *http.Request"
		for _, p := range endpointPathParams(endpoint) {
			signature += ", " + lowerCamelName(p.Name) + " " + pathParamTypeName(p)
		}
		queryParams := endpointQueryParams(endpoint)
		headerParams := endpointHeaderParams(endpoint)
		if len(queryParams) > 0 {
			signature += ", params Gen" + name + "Params"
		}
		if len(headerParams) > 0 {
			signature += ", headers Gen" + name + "Headers"
		}
		signature += ")\n"
		b.WriteString(signature)
	}
	b.WriteString("}\n\n")
	b.WriteString("// DispatchAPIGenOperation dispatches operation IDs to generated wrapper methods.\n")
	b.WriteString("func DispatchAPIGenOperation(operationID string, dispatcher GenOperationDispatcher, responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request) bool {\n")
	b.WriteString("\tswitch operationID {\n")
	for _, endpoint := range doc.Endpoints {
		name := exportedName(endpoint.OperationID)
		b.WriteString("\tcase \"" + endpoint.OperationID + "\":\n")
		if endpoint.OperationID == "getHealth" {
			b.WriteString("\t\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
			b.WriteString("\t\tw.WriteHeader(http.StatusOK)\n")
			b.WriteString("\t\t_ = json.NewEncoder(w).Encode(map[string]string{\"status\": \"ok\"})\n")
			b.WriteString("\t\treturn true\n")
			continue
		}

		pathParams := endpointPathParams(endpoint)
		queryParams := endpointQueryParams(endpoint)
		headerParams := endpointHeaderParams(endpoint)
		if len(pathParams) > 0 || len(queryParams) > 0 || len(headerParams) > 0 {
			b.WriteString("\t\tvar err error\n")
		}

		for _, p := range pathParams {
			varName := lowerCamelName(p.Name)
			typeName := pathParamTypeName(p)
			required := "false"
			if p.Required {
				required = "true"
			}
			b.WriteString("\t\tvar " + varName + " " + typeName + "\n")
			b.WriteString("\t\terr = apigenchi.BindPathParameter(\"" + p.Name + "\", apigenchi.URLParam(r, \"" + p.Name + "\"), " + required + ", &" + varName + ")\n")
			b.WriteString("\t\tif err != nil {\n")
			writeTransportErrorCall(&b, doc, endpoint.OperationID, "path_parameter", "err", "\t\t\t")
			b.WriteString("\t\t\treturn true\n")
			b.WriteString("\t\t}\n")
		}

		if len(queryParams) > 0 {
			b.WriteString("\t\tvar params Gen" + name + "Params\n")
			for _, p := range queryParams {
				fieldName := exportedName(p.Name)
				required := "false"
				if p.Required {
					required = "true"
				}
				b.WriteString("\t\terr = apigenchi.BindQueryParameter(r.URL.Query(), \"" + p.Name + "\", " + required + ", &params." + fieldName + ")\n")
				b.WriteString("\t\tif err != nil {\n")
				writeTransportErrorCall(&b, doc, endpoint.OperationID, "query_parameter", "err", "\t\t\t")
				b.WriteString("\t\t\treturn true\n")
				b.WriteString("\t\t}\n")
			}
		}
		if len(headerParams) > 0 {
			b.WriteString("\t\tvar headers Gen" + name + "Headers\n")
			for _, p := range headerParams {
				fieldName := exportedName(p.Name)
				required := "false"
				if p.Required {
					required = "true"
				}
				b.WriteString("\t\terr = apigenchi.BindHeaderParameter(r.Header, \"" + p.Name + "\", " + required + ", &headers." + fieldName + ")\n")
				b.WriteString("\t\tif err != nil {\n")
				writeTransportErrorCall(&b, doc, endpoint.OperationID, "header_parameter", "err", "\t\t\t")
				b.WriteString("\t\t\treturn true\n")
				b.WriteString("\t\t}\n")
			}
		}

		call := "\t\tdispatcher." + name + "(w, r"
		for _, p := range pathParams {
			call += ", " + lowerCamelName(p.Name)
		}
		if len(queryParams) > 0 {
			call += ", params"
		}
		if len(headerParams) > 0 {
			call += ", headers"
		}
		call += ")\n"
		b.WriteString(call)
		b.WriteString("\t\treturn true\n")
	}
	b.WriteString("\tdefault:\n")
	b.WriteString("\t\treturn false\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	if hasStrictOperations {
		b.WriteString("// GenTransportError describes a transport-layer failure without prescribing its wire model.\n")
		b.WriteString("type GenTransportError struct {\n")
		b.WriteString("\tOperationID string\n\tKind string\n\tStatusCode int\n\tCode string\n\tPublicDetail string\n\tCause error\n")
		b.WriteString("}\n\n")
		b.WriteString("// GenTransportErrorResponder owns serialization of generated transport failures.\n")
		b.WriteString("type GenTransportErrorResponder interface {\n")
		b.WriteString("\tRespondTransportError(ctx context.Context, w http.ResponseWriter, r *http.Request, failure GenTransportError)\n")
		b.WriteString("}\n\n")
		b.WriteString("func writeAPIGenError(responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request, failure GenTransportError) {\n")
		b.WriteString("\tif responder == nil { panic(\"apigen: nil transport error responder\") }\n")
		b.WriteString("\tresponder.RespondTransportError(r.Context(), w, r, failure)\n")
		b.WriteString("}\n\n")
		if hasRequestBodies {
			b.WriteString("func validateAPIGenContentType(r *http.Request, expected string) error {\n")
			b.WriteString("\tactual, _, err := mime.ParseMediaType(r.Header.Get(\"Content-Type\"))\n")
			b.WriteString("\tif err != nil { return fmt.Errorf(\"parse Content-Type: %w\", err) }\n")
			b.WriteString("\twant, _, err := mime.ParseMediaType(expected)\n")
			b.WriteString("\tif err != nil { return fmt.Errorf(\"parse generated Content-Type %q: %w\", expected, err) }\n")
			b.WriteString("\tif !strings.EqualFold(actual, want) { return fmt.Errorf(\"unsupported Content-Type %q; expected %q\", actual, want) }\n")
			b.WriteString("\treturn nil\n")
			b.WriteString("}\n\n")
		}
	}
	if hasFileBodies {
		b.WriteString("// GenFile represents a TypeSpec Http.File payload with transport metadata.\n")
		b.WriteString("type GenFile struct {\n")
		b.WriteString("\tContents []byte\n")
		b.WriteString("\tReader io.ReadCloser\n")
		b.WriteString("\tContentType string\n")
		b.WriteString("\tFilename *string\n")
		b.WriteString("\tSize *int64\n")
		b.WriteString("}\n\n")
		b.WriteString("func apigenContentLengthPointer(contentLength int64) *int64 {\n")
		b.WriteString("\tif contentLength < 0 {\n")
		b.WriteString("\t\treturn nil\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn &contentLength\n")
		b.WriteString("}\n\n")
		b.WriteString("func writeAPIGenFileResponse(w http.ResponseWriter, file GenFile, defaultContentType string, statusCode int) error {\n")
		b.WriteString("\tcontentType := file.ContentType\n")
		b.WriteString("\tif contentType == \"\" {\n")
		b.WriteString("\t\tcontentType = defaultContentType\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif contentType != \"\" {\n")
		b.WriteString("\t\tw.Header().Set(\"Content-Type\", contentType)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif file.Filename != nil && *file.Filename != \"\" {\n")
		b.WriteString("\t\tw.Header().Set(\"Content-Disposition\", mime.FormatMediaType(\"attachment\", map[string]string{\"filename\": *file.Filename}))\n")
		b.WriteString("\t}\n")
		b.WriteString("\tw.WriteHeader(statusCode)\n")
		b.WriteString("\tif file.Reader != nil {\n")
		b.WriteString("\t\tdefer file.Reader.Close()\n")
		b.WriteString("\t\t_, err := io.Copy(w, file.Reader)\n")
		b.WriteString("\t\treturn err\n")
		b.WriteString("\t}\n")
		b.WriteString("\t_, err := w.Write(file.Contents)\n")
		b.WriteString("\treturn err\n")
		b.WriteString("}\n\n")
	}
	if hasRequestBodies {
		b.WriteString("func decodeAPIGenJSONBody(body io.Reader, dest any, requiredBody bool, requiredFields ...string) error {\n")
		b.WriteString("\traw, err := io.ReadAll(body)\n")
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"read request body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif len(strings.TrimSpace(string(raw))) == 0 {\n")
		b.WriteString("\t\tif !requiredBody {\n")
		b.WriteString("\t\t\treturn nil\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\treturn fmt.Errorf(\"request body must not be empty\")\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif len(requiredFields) > 0 {\n")
		b.WriteString("\t\tvar envelope map[string]json.RawMessage\n")
		b.WriteString("\t\tif err := json.Unmarshal(raw, &envelope); err == nil {\n")
		b.WriteString("\t\t\tfor _, field := range requiredFields {\n")
		b.WriteString("\t\t\t\tif _, ok := envelope[field]; !ok {\n")
		b.WriteString("\t\t\t\t\treturn fmt.Errorf(\"%s is required\", field)\n")
		b.WriteString("\t\t\t\t}\n")
		b.WriteString("\t\t\t}\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
		b.WriteString("\tdecoder := json.NewDecoder(strings.NewReader(string(raw)))\n")
		b.WriteString("\tdecoder.DisallowUnknownFields()\n")
		b.WriteString("\tif err := decoder.Decode(dest); err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"invalid JSON body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tvar extra json.RawMessage\n")
		b.WriteString("\tif err := decoder.Decode(&extra); err != io.EOF {\n")
		b.WriteString("\t\tif err == nil {\n")
		b.WriteString("\t\t\treturn fmt.Errorf(\"request body must contain a single JSON value\")\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\treturn fmt.Errorf(\"invalid JSON body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn nil\n")
		b.WriteString("}\n\n")
		b.WriteString("func decodeAPIGenTextBody(body io.Reader, requiredBody bool) (string, error) {\n")
		b.WriteString("\traw, err := io.ReadAll(body)\n")
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\treturn \"\", fmt.Errorf(\"read request body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif len(raw) == 0 && requiredBody {\n")
		b.WriteString("\t\treturn \"\", fmt.Errorf(\"request body must not be empty\")\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn string(raw), nil\n")
		b.WriteString("}\n\n")
		b.WriteString("func decodeAPIGenBytesBody(body io.Reader, requiredBody bool) ([]byte, error) {\n")
		b.WriteString("\traw, err := io.ReadAll(body)\n")
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\treturn nil, fmt.Errorf(\"read request body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif len(raw) == 0 && requiredBody {\n")
		b.WriteString("\t\treturn nil, fmt.Errorf(\"request body must not be empty\")\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn raw, nil\n")
		b.WriteString("}\n\n")
		b.WriteString("func decodeAPIGenFormBody(r *http.Request, dest any, requiredBody bool, requiredFields ...string) error {\n")
		b.WriteString("\tif err := r.ParseForm(); err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"parse form body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif len(r.PostForm) == 0 {\n")
		b.WriteString("\t\tif !requiredBody {\n")
		b.WriteString("\t\t\treturn nil\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\treturn fmt.Errorf(\"request body must not be empty\")\n")
		b.WriteString("\t}\n")
		b.WriteString("\tfor _, field := range requiredFields {\n")
		b.WriteString("\t\tif strings.TrimSpace(r.PostForm.Get(field)) == \"\" {\n")
		b.WriteString("\t\t\treturn fmt.Errorf(\"%s is required\", field)\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
		b.WriteString("\tpayload := map[string]any{}\n")
		b.WriteString("\tfor key, values := range r.PostForm {\n")
		b.WriteString("\t\tif len(values) == 1 {\n")
		b.WriteString("\t\t\tpayload[key] = values[0]\n")
		b.WriteString("\t\t} else {\n")
		b.WriteString("\t\t\tpayload[key] = values\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
		b.WriteString("\tencoded, err := json.Marshal(payload)\n")
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"encode form body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\tdecoder := json.NewDecoder(strings.NewReader(string(encoded)))\n")
		b.WriteString("\tdecoder.DisallowUnknownFields()\n")
		b.WriteString("\tif err := decoder.Decode(dest); err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"invalid form body: %w\", err)\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn nil\n")
		b.WriteString("}\n\n")
		if hasMultipartBodies {
			b.WriteString("type apigenMultipartPart struct {\n")
			b.WriteString("\tName string\n")
			b.WriteString("\tFilename string\n")
			b.WriteString("\tContentType string\n")
			b.WriteString("\tRaw []byte\n")
			b.WriteString("\tFile *os.File\n")
			b.WriteString("\tTempPath string\n")
			b.WriteString("\tSize *int64\n")
			b.WriteString("}\n\n")
			b.WriteString("func readAPIGenMultipartParts(r *http.Request, fileNames map[string]bool, fileIndexes map[int]bool) ([]apigenMultipartPart, error) {\n")
			b.WriteString("\treader, err := r.MultipartReader()\n")
			b.WriteString("\tif err != nil {\n")
			b.WriteString("\t\treturn nil, fmt.Errorf(\"parse multipart body: %w\", err)\n")
			b.WriteString("\t}\n")
			b.WriteString("\tparts := []apigenMultipartPart{}\n")
			b.WriteString("\tindex := 0\n")
			b.WriteString("\tfor {\n")
			b.WriteString("\t\tpart, err := reader.NextPart()\n")
			b.WriteString("\t\tif err == io.EOF {\n")
			b.WriteString("\t\t\tbreak\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\tif err != nil {\n")
			b.WriteString("\t\t\treturn nil, fmt.Errorf(\"read multipart part: %w\", err)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\tmultipartPart := apigenMultipartPart{Name: part.FormName(), Filename: part.FileName(), ContentType: part.Header.Get(\"Content-Type\")}\n")
			b.WriteString("\t\tif fileNames[part.FormName()] || fileIndexes[index] {\n")
			b.WriteString("\t\t\ttempFile, err := os.CreateTemp(\"\", \"apigen-multipart-*\")\n")
			b.WriteString("\t\t\tif err != nil {\n")
			b.WriteString("\t\t\t\t_ = part.Close()\n")
			b.WriteString("\t\t\t\treturn nil, fmt.Errorf(\"create multipart temp file: %w\", err)\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tsize, copyErr := io.Copy(tempFile, part)\n")
			b.WriteString("\t\t\tif closeErr := part.Close(); copyErr == nil && closeErr != nil {\n")
			b.WriteString("\t\t\t\tcopyErr = closeErr\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tif copyErr == nil {\n")
			b.WriteString("\t\t\t\t_, copyErr = tempFile.Seek(0, io.SeekStart)\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tif copyErr != nil {\n")
			b.WriteString("\t\t\t\t_ = tempFile.Close()\n")
			b.WriteString("\t\t\t\t_ = os.Remove(tempFile.Name())\n")
			b.WriteString("\t\t\t\treturn nil, fmt.Errorf(\"read multipart part %s: %w\", part.FormName(), copyErr)\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tmultipartPart.File = tempFile\n")
			b.WriteString("\t\t\tmultipartPart.TempPath = tempFile.Name()\n")
			b.WriteString("\t\t\tmultipartPart.Size = &size\n")
			b.WriteString("\t\t} else {\n")
			b.WriteString("\t\t\traw, err := io.ReadAll(part)\n")
			b.WriteString("\t\t\tif closeErr := part.Close(); err == nil && closeErr != nil {\n")
			b.WriteString("\t\t\t\terr = closeErr\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tif err != nil {\n")
			b.WriteString("\t\t\t\treturn nil, fmt.Errorf(\"read multipart part %s: %w\", part.FormName(), err)\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tmultipartPart.Raw = raw\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\tparts = append(parts, multipartPart)\n")
			b.WriteString("\t\tindex++\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn parts, nil\n")
			b.WriteString("}\n\n")
			b.WriteString("func cleanupAPIGenMultipartParts(parts []apigenMultipartPart) {\n")
			b.WriteString("\tfor _, part := range parts {\n")
			b.WriteString("\t\tif part.File != nil {\n")
			b.WriteString("\t\t\t_ = part.File.Close()\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\tif part.TempPath != \"\" {\n")
			b.WriteString("\t\t\t_ = os.Remove(part.TempPath)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
			b.WriteString("}\n\n")
			b.WriteString("func apigenMultipartPartsByName(parts []apigenMultipartPart, name string) []apigenMultipartPart {\n")
			b.WriteString("\tmatched := []apigenMultipartPart{}\n")
			b.WriteString("\tfor _, part := range parts {\n")
			b.WriteString("\t\tif part.Name == name {\n")
			b.WriteString("\t\t\tmatched = append(matched, part)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn matched\n")
			b.WriteString("}\n\n")
			b.WriteString("func apigenMultipartPartsByIndex(parts []apigenMultipartPart, index int) []apigenMultipartPart {\n")
			b.WriteString("\tif index < 0 || index >= len(parts) {\n")
			b.WriteString("\t\treturn nil\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn []apigenMultipartPart{parts[index]}\n")
			b.WriteString("}\n\n")
			b.WriteString("type apigenMultipartRule struct {\n")
			b.WriteString("\tRepeated bool\n")
			b.WriteString("}\n\n")
			b.WriteString("func validateAPIGenMultipartParts(parts []apigenMultipartPart, namedRules map[string]apigenMultipartRule, positionalLimit int) error {\n")
			b.WriteString("\tif positionalLimit > 0 && len(namedRules) == 0 {\n")
			b.WriteString("\t\tif len(parts) > positionalLimit {\n")
			b.WriteString("\t\t\treturn fmt.Errorf(\"unexpected multipart mixed part at index %d\", positionalLimit+1)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\treturn nil\n")
			b.WriteString("\t}\n")
			b.WriteString("\tseen := map[string]int{}\n")
			b.WriteString("\tfor idx, part := range parts {\n")
			b.WriteString("\t\tif part.Name == \"\" {\n")
			b.WriteString("\t\t\tif idx >= positionalLimit {\n")
			b.WriteString("\t\t\t\treturn fmt.Errorf(\"unexpected multipart mixed part at index %d\", idx+1)\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t\tcontinue\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\trule, ok := namedRules[part.Name]\n")
			b.WriteString("\t\tif !ok {\n")
			b.WriteString("\t\t\treturn fmt.Errorf(\"unexpected multipart part %q\", part.Name)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t\tseen[part.Name]++\n")
			b.WriteString("\t\tif seen[part.Name] > 1 && !rule.Repeated {\n")
			b.WriteString("\t\t\treturn fmt.Errorf(\"duplicate multipart part %q\", part.Name)\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn nil\n")
			b.WriteString("}\n\n")
			if hasFileBodies {
				b.WriteString("func genFileFromMultipartPart(part apigenMultipartPart, defaultContentType string) GenFile {\n")
				b.WriteString("\tcontentType := part.ContentType\n")
				b.WriteString("\tif contentType == \"\" {\n")
				b.WriteString("\t\tcontentType = defaultContentType\n")
				b.WriteString("\t}\n")
				b.WriteString("\tvar filename *string\n")
				b.WriteString("\tif part.Filename != \"\" {\n")
				b.WriteString("\t\tvalue := part.Filename\n")
				b.WriteString("\t\tfilename = &value\n")
				b.WriteString("\t}\n")
				b.WriteString("\tif part.File != nil {\n")
				b.WriteString("\t\treturn GenFile{Reader: part.File, ContentType: contentType, Filename: filename, Size: part.Size}\n")
				b.WriteString("\t}\n")
				b.WriteString("\treturn GenFile{Contents: part.Raw, ContentType: contentType, Filename: filename, Size: part.Size}\n")
				b.WriteString("}\n\n")
			}
		}
	}
	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID == "getHealth" {
			continue
		}
		name := exportedName(endpoint.OperationID)
		pathParams := endpointPathParams(endpoint)
		queryParams := endpointQueryParams(endpoint)
		headerParams := endpointHeaderParams(endpoint)
		if len(queryParams) > 0 {
			b.WriteString("// Gen" + name + "Params represents the APIGen strict query parameter contract for " + name + ".\n")
			b.WriteString("type Gen" + name + "Params struct {\n")
			for _, p := range queryParams {
				fieldType := schemaTypeName(p.Schema)
				if !p.Required {
					fieldType = "*" + fieldType
				}
				b.WriteString("\t" + exportedName(p.Name) + " " + fieldType + "\n")
			}
			b.WriteString("}\n\n")
		}
		if len(headerParams) > 0 {
			b.WriteString("// Gen" + name + "Headers represents the APIGen strict header parameter contract for " + name + ".\n")
			b.WriteString("type Gen" + name + "Headers struct {\n")
			for _, p := range headerParams {
				fieldType := schemaTypeName(p.Schema)
				if !p.Required {
					fieldType = "*" + fieldType
				}
				b.WriteString("\t" + exportedName(p.Name) + " " + fieldType + "\n")
			}
			b.WriteString("}\n\n")
		}
		b.WriteString("// Gen" + name + "Request represents the APIGen strict request contract for " + name + ".\n")
		b.WriteString("type Gen" + name + "Request struct {\n")
		for _, p := range pathParams {
			b.WriteString("\t" + exportedName(p.Name) + " " + pathParamTypeName(p) + "\n")
		}
		if len(queryParams) > 0 {
			b.WriteString("\tParams Gen" + name + "Params\n")
		}
		if len(headerParams) > 0 {
			b.WriteString("\tHeaders Gen" + name + "Headers\n")
		}
		if endpoint.RequestBody != nil {
			b.WriteString("\tBody *Gen" + name + "Body\n")
		}
		b.WriteString("}\n\n")
		b.WriteString("// Gen" + name + "Response represents the APIGen strict response contract for " + name + ".\n")
		b.WriteString("type Gen" + name + "Response interface {\n")
		b.WriteString("\tVisit" + name + "Response(w http.ResponseWriter) error\n")
		b.WriteString("}\n\n")
		for _, response := range endpoint.Responses {
			emitOperationResponse(&b, doc, name, response)
		}
		if endpoint.RequestBody != nil {
			if content, ok := ir.PrimaryRequestBodyContent(endpoint); ok && content.BodyKind == "multipart" {
				if err := emitMultipartRequestBody(&b, doc, name, content); err != nil {
					return nil, err
				}
			}
			bodyTypeName, err := requestBodyTypeName(doc, endpoint)
			if err != nil {
				return nil, err
			}
			b.WriteString("// Gen" + name + "Body aliases the APIGen strict request body schema for " + name + ".\n")
			b.WriteString("type Gen" + name + "Body = " + bodyTypeName + "\n\n")
		}
	}

	b.WriteString("// GenStrictServerInterface represents strict handlers for APIGen transport dispatch.\n")
	b.WriteString("type GenStrictServerInterface interface {\n")
	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID == "getHealth" {
			continue
		}
		name := exportedName(endpoint.OperationID)
		b.WriteString("\t" + name + "(ctx context.Context, request Gen" + name + "Request) (Gen" + name + "Response, error)\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("type genStrictAdapter struct {\n")
	b.WriteString("\thandler GenStrictServerInterface\n")
	b.WriteString("\tresponder GenTransportErrorResponder\n")
	b.WriteString("}\n\n")

	b.WriteString("func (a genStrictAdapter) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request) {\n")
	b.WriteString("\tif ok := DispatchAPIGenStrictOperation(operationID, a.handler, a.responder, w, r); !ok {\n")
	b.WriteString("\t\thttp.NotFound(w, r)\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")

	b.WriteString("type genStrictBridge struct {\n")
	b.WriteString("\thandler GenStrictServerInterface\n")
	b.WriteString("\tresponder GenTransportErrorResponder\n")
	b.WriteString("}\n\n")

	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID == "getHealth" {
			continue
		}
		name := exportedName(endpoint.OperationID)
		pathParams := endpointPathParams(endpoint)
		queryParams := endpointQueryParams(endpoint)
		headerParams := endpointHeaderParams(endpoint)

		sig := "func (b genStrictBridge) " + name + "(w http.ResponseWriter, r *http.Request"
		for _, p := range pathParams {
			sig += ", " + lowerCamelName(p.Name) + " " + pathParamTypeName(p)
		}
		if len(queryParams) > 0 {
			sig += ", params Gen" + name + "Params"
		}
		if len(headerParams) > 0 {
			sig += ", headers Gen" + name + "Headers"
		}
		sig += ") {\n"
		b.WriteString(sig)
		b.WriteString("\tvar request Gen" + name + "Request\n")

		for _, p := range pathParams {
			fieldName := exportedName(p.Name)
			paramName := lowerCamelName(p.Name)
			b.WriteString("\trequest." + fieldName + " = " + paramName + "\n")
		}
		if len(queryParams) > 0 {
			b.WriteString("\trequest.Params = params\n")
		}
		if len(headerParams) > 0 {
			b.WriteString("\trequest.Headers = headers\n")
		}

		if endpoint.RequestBody != nil {
			content, _ := ir.PrimaryRequestBodyContent(endpoint)
			b.WriteString("\tvar body Gen" + name + "Body\n")
			requiredFields := requestBodyRequiredFields(doc, endpoint)
			requiredBody := "false"
			if endpoint.RequestBody.Required {
				requiredBody = "true"
			}
			requiredFieldArgs := ""
			if len(requiredFields) > 0 {
				requiredFieldArgs = ", " + renderGoStringSlice(requiredFields) + "..."
			}
			b.WriteString("\tif r.Header.Get(\"Content-Type\") != \"\" {\n")
			b.WriteString("\t\tif err := validateAPIGenContentType(r, " + strconv.Quote(content.ContentType) + "); err != nil {\n")
			writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "unsupported_media_type", "err", "b.responder", "\t\t\t")
			b.WriteString("\t\t\treturn\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
			switch content.BodyKind {
			case "text":
				b.WriteString("\tvalue, err := decodeAPIGenTextBody(r.Body, " + requiredBody + ")\n")
				b.WriteString("\tif err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "malformed_body", "err", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
				b.WriteString("\tbody = value\n")
			case "binary":
				b.WriteString("\tvalue, err := decodeAPIGenBytesBody(r.Body, " + requiredBody + ")\n")
				b.WriteString("\tif err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "malformed_body", "err", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
				b.WriteString("\tbody = value\n")
			case "file":
				b.WriteString("\tif " + requiredBody + " && r.ContentLength == 0 {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "malformed_body", "fmt.Errorf(\"request body must not be empty\")", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
				b.WriteString("\tbody = GenFile{Reader: r.Body, ContentType: r.Header.Get(\"Content-Type\"), Size: apigenContentLengthPointer(r.ContentLength)}\n")
			case "form_urlencoded":
				b.WriteString("\tif err := decodeAPIGenFormBody(r, &body, " + requiredBody + requiredFieldArgs + "); err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "malformed_body", "err", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
			case "multipart":
				b.WriteString("\tif " + requiredBody + " || r.ContentLength != 0 {\n")
				b.WriteString("\t\tparts, err := readAPIGenMultipartParts(r, " + renderMultipartFileNameMap(content) + ", " + renderMultipartFileIndexMap(content) + ")\n")
				b.WriteString("\t\tif err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "multipart", "err", "b.responder", "\t\t\t")
				b.WriteString("\t\t\treturn\n")
				b.WriteString("\t\t}\n")
				b.WriteString("\t\tdefer cleanupAPIGenMultipartParts(parts)\n")
				b.WriteString("\t\tif err := validateAPIGenMultipartParts(parts, " + renderMultipartNamedRuleMap(content) + ", " + strconv.Itoa(renderMultipartPositionalLimit(content)) + "); err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "multipart", "err", "b.responder", "\t\t\t")
				b.WriteString("\t\t\treturn\n")
				b.WriteString("\t\t}\n")
				if err := emitMultipartDecode(&b, doc, name, content); err != nil {
					return nil, err
				}
				b.WriteString("\t}\n")
			default:
				b.WriteString("\tif err := decodeAPIGenJSONBody(r.Body, &body, " + requiredBody + requiredFieldArgs + "); err != nil {\n")
				writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "malformed_body", "err", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
			}
			b.WriteString("\trequest.Body = &body\n")
		}

		b.WriteString("\tresponse, err := b.handler." + name + "(r.Context(), request)\n")
		b.WriteString("\tif err != nil {\n")
		writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "handler", "err", "b.responder", "\t\t")
		b.WriteString("\t\treturn\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif err := response.Visit" + name + "Response(w); err != nil {\n")
		writeTransportErrorCallWithResponder(&b, doc, endpoint.OperationID, "response_serialization", "err", "b.responder", "\t\t")
		b.WriteString("\t}\n")
		b.WriteString("}\n\n")
	}

	b.WriteString("// DispatchAPIGenStrictOperation dispatches to strict handlers without oapi strict wrappers.\n")
	b.WriteString("func DispatchAPIGenStrictOperation(operationID string, handler GenStrictServerInterface, responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request) bool {\n")
	b.WriteString("\treturn DispatchAPIGenOperation(operationID, genStrictBridge{handler: handler, responder: responder}, responder, w, r)\n")
	b.WriteString("}\n")

	return []byte(b.String()), nil
}

func emitSpecJSON(docIR ir.Document) (string, error) {
	yamlBytes, err := openapiemit.EmitYAML(docIR, openapiemit.Options{})
	if err != nil {
		return "", fmt.Errorf("emit embedded openapi yaml: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return "", fmt.Errorf("decode emitted openapi yaml: %w", err)
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal embedded openapi json: %w", err)
	}
	return string(jsonBytes), nil
}

func emitMultipartRequestBody(b *strings.Builder, doc ir.Document, operationName string, content ir.BodyContent) error {
	b.WriteString("// Gen" + operationName + "MultipartBody represents the APIGen strict multipart request body for " + operationName + ".\n")
	b.WriteString("type Gen" + operationName + "MultipartBody struct {\n")
	for _, part := range content.Parts {
		partType, err := multipartPartTypeName(doc, part)
		if err != nil {
			return err
		}
		if part.Repeated {
			partType = "[]" + partType
		} else if !part.Required {
			partType = "*" + partType
		}
		b.WriteString("\t" + exportedName(part.Name) + " " + partType + "\n")
	}
	b.WriteString("}\n\n")
	return nil
}

func emitMultipartDecode(b *strings.Builder, doc ir.Document, operationName string, content ir.BodyContent) error {
	for idx, part := range content.Parts {
		fieldName := exportedName(part.Name)
		localName := lowerCamelName(part.Name)
		partsName := localName + "Parts"
		wireName := part.WireName
		if wireName == "" || strings.EqualFold(content.ContentType, "multipart/mixed") {
			b.WriteString("\t" + partsName + " := apigenMultipartPartsByIndex(parts, " + strconv.Itoa(idx) + ")\n")
		} else {
			b.WriteString("\t" + partsName + " := apigenMultipartPartsByName(parts, " + strconv.Quote(wireName) + ")\n")
		}
		if part.Repeated {
			if part.Required {
				b.WriteString("\tif len(" + partsName + ") == 0 {\n")
				writeTransportErrorCallWithResponder(b, doc, operationName, "multipart", "fmt.Errorf("+strconv.Quote(part.Name+" is required")+")", "b.responder", "\t\t")
				b.WriteString("\t\treturn\n")
				b.WriteString("\t}\n")
			}
			b.WriteString("\tfor _, " + localName + "Part := range " + partsName + " {\n")
			if err := emitMultipartPartValue(b, doc, operationName, part, localName, localName+"Part"); err != nil {
				return err
			}
			b.WriteString("\t\tbody." + fieldName + " = append(body." + fieldName + ", " + localName + "Value)\n")
			b.WriteString("\t}\n")
			continue
		}
		okName := localName + "OK"
		b.WriteString("\t" + okName + " := len(" + partsName + ") > 0\n")
		if part.Required {
			b.WriteString("\tif !" + okName + " {\n")
			writeTransportErrorCallWithResponder(b, doc, operationName, "multipart", "fmt.Errorf("+strconv.Quote(part.Name+" is required")+")", "b.responder", "\t\t")
			b.WriteString("\t\treturn\n")
			b.WriteString("\t}\n")
		} else {
			b.WriteString("\tif " + okName + " {\n")
		}
		b.WriteString("\t" + localName + "Part := " + partsName + "[0]\n")
		if err := emitMultipartPartValue(b, doc, operationName, part, localName, localName+"Part"); err != nil {
			return err
		}
		if part.Required {
			b.WriteString("\tbody." + fieldName + " = " + localName + "Value\n")
		} else {
			b.WriteString("\tbody." + fieldName + " = &" + localName + "Value\n")
			b.WriteString("\t}\n")
		}
	}
	return nil
}

func emitMultipartPartValue(
	b *strings.Builder,
	doc ir.Document,
	operationName string,
	part ir.MultipartPart,
	localName string,
	partExpr string,
) error {
	valueName := localName + "Value"
	switch part.BodyKind {
	case "json", "form_urlencoded":
		partType, err := multipartPartTypeName(doc, part)
		if err != nil {
			return err
		}
		b.WriteString("\tvar " + valueName + " " + partType + "\n")
		b.WriteString("\tif err := json.Unmarshal(" + partExpr + ".Raw, &" + valueName + "); err != nil {\n")
		writeTransportErrorCallWithResponder(b, doc, operationName, "multipart", "fmt.Errorf("+strconv.Quote("invalid multipart part "+part.Name+": %w")+", err)", "b.responder", "\t\t")
		b.WriteString("\t\treturn\n")
		b.WriteString("\t}\n")
	case "text":
		b.WriteString("\t" + valueName + " := string(" + partExpr + ".Raw)\n")
	case "binary":
		b.WriteString("\t" + valueName + " := " + partExpr + ".Raw\n")
	case "file":
		b.WriteString("\t" + valueName + " := genFileFromMultipartPart(" + partExpr + ", " + strconv.Quote(part.ContentType) + ")\n")
	default:
		return fmt.Errorf("multipart request body generation for %s part %q has unsupported body kind %q", operationName, part.Name, part.BodyKind)
	}
	return nil
}

func multipartPartTypeName(doc ir.Document, part ir.MultipartPart) (string, error) {
	switch part.BodyKind {
	case "text":
		return "string", nil
	case "binary":
		return "[]byte", nil
	case "file":
		return "GenFile", nil
	case "json", "form_urlencoded":
		if part.Schema == nil {
			return "", fmt.Errorf("multipart part %q requires a schema", part.Name)
		}
		if ref, ok := normalizedSchemaRefName(*part.Schema); ok {
			if _, ok := doc.Schemas[ref]; ok {
				return "GenSchema" + exportedName(ref), nil
			}
		}
		if strings.EqualFold(part.Schema.Type, "object") {
			return "", fmt.Errorf("multipart part %q requires a named IR schema", part.Name)
		}
		return schemaTypeName(*part.Schema), nil
	default:
		return "", fmt.Errorf("multipart part %q has unsupported body kind %q", part.Name, part.BodyKind)
	}
}

func renderMultipartFileNameMap(content ir.BodyContent) string {
	values := make([]string, 0)
	for _, part := range content.Parts {
		if part.BodyKind == "file" && part.WireName != "" && !strings.EqualFold(content.ContentType, "multipart/mixed") {
			values = append(values, strconv.Quote(part.WireName)+": true")
		}
	}
	if len(values) == 0 {
		return "map[string]bool{}"
	}
	sort.Strings(values)
	return "map[string]bool{" + strings.Join(values, ", ") + "}"
}

func renderMultipartFileIndexMap(content ir.BodyContent) string {
	values := make([]string, 0)
	for idx, part := range content.Parts {
		if part.BodyKind == "file" && (part.WireName == "" || strings.EqualFold(content.ContentType, "multipart/mixed")) {
			values = append(values, strconv.Itoa(idx)+": true")
		}
	}
	if len(values) == 0 {
		return "map[int]bool{}"
	}
	return "map[int]bool{" + strings.Join(values, ", ") + "}"
}

func renderMultipartNamedRuleMap(content ir.BodyContent) string {
	if strings.EqualFold(content.ContentType, "multipart/mixed") {
		return "map[string]apigenMultipartRule{}"
	}
	values := make([]string, 0)
	for _, part := range content.Parts {
		if part.WireName == "" {
			continue
		}
		values = append(values, strconv.Quote(part.WireName)+": {Repeated: "+strconv.FormatBool(part.Repeated)+"}")
	}
	if len(values) == 0 {
		return "map[string]apigenMultipartRule{}"
	}
	sort.Strings(values)
	return "map[string]apigenMultipartRule{" + strings.Join(values, ", ") + "}"
}

func renderMultipartPositionalLimit(content ir.BodyContent) int {
	if strings.EqualFold(content.ContentType, "multipart/mixed") {
		return len(content.Parts)
	}
	limit := 0
	for idx, part := range content.Parts {
		if part.WireName == "" {
			limit = idx + 1
		}
	}
	return limit
}

func emitOperationResponse(b *strings.Builder, doc ir.Document, operationName string, response ir.Response) {
	statusCode := fmt.Sprintf("%d", response.StatusCode)
	headersFields := responseHeaderFieldsWithDefaults(response)
	headersTypeName := "Gen" + operationName + statusCode + "ResponseHeaders"
	if len(headersFields) > 0 {
		emitOwnedResponseHeaders(b, headersTypeName, headersFields)
	}
	if len(response.Contents) == 0 {
		typeName := "Gen" + operationName + statusCode + "Response"
		b.WriteString("// " + typeName + " is the APIGen concrete response for " + operationName + " " + statusCode + ".\n")
		b.WriteString("type " + typeName + " struct")
		if len(headersFields) > 0 {
			b.WriteString(" {\n\tHeaders " + headersTypeName + "\n}")
		} else {
			b.WriteString("{}")
		}
		b.WriteString("\n\n")
		b.WriteString("// Visit" + operationName + "Response writes " + operationName + " " + statusCode + " responses to the client.\n")
		b.WriteString("func (response " + typeName + ") Visit" + operationName + "Response(w http.ResponseWriter) error {\n")
		emitDirectHeaderWrites(b, headersFields)
		b.WriteString("\tw.WriteHeader(" + statusCode + ")\n")
		b.WriteString("\treturn nil\n")
		b.WriteString("}\n\n")
		return
	}

	multiContent := len(response.Contents) > 1
	for _, content := range response.Contents {
		emitOperationContentResponse(b, doc, operationName, response, content, headersFields, headersTypeName, multiContent)
	}
}

func emitOperationContentResponse(
	b *strings.Builder,
	doc ir.Document,
	operationName string,
	response ir.Response,
	content ir.BodyContent,
	headersFields []ownedHeaderField,
	headersTypeName string,
	multiContent bool,
) {
	statusCode := fmt.Sprintf("%d", response.StatusCode)
	typeName := "Gen" + operationName + statusCode + responseTypeSuffix(content, true, multiContent)
	b.WriteString("// " + typeName + " is the APIGen concrete response for " + operationName + " " + statusCode + ".\n")
	bodyType := responseContentTypeName(doc, content)
	b.WriteString("type " + typeName + " struct {\n")
	b.WriteString("\tBody " + bodyType + "\n")
	if len(headersFields) > 0 {
		b.WriteString("\tHeaders " + headersTypeName + "\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// Visit" + operationName + "Response writes " + operationName + " " + statusCode + " responses to the client.\n")
	b.WriteString("func (response " + typeName + ") Visit" + operationName + "Response(w http.ResponseWriter) error {\n")
	if content.BodyKind != "text" && content.BodyKind != "binary" && content.BodyKind != "file" {
		b.WriteString("\tpayload, err := json.Marshal(response.Body)\n")
		b.WriteString("\tif err != nil { return err }\n")
	}
	emitDirectHeaderWrites(b, headersFields)
	if content.BodyKind == "file" {
		b.WriteString("\treturn writeAPIGenFileResponse(w, response.Body, " + strconv.Quote(content.ContentType) + ", " + statusCode + ")\n")
	} else {
		b.WriteString("\tw.Header().Set(\"Content-Type\", " + strconv.Quote(content.ContentType) + ")\n")
		b.WriteString("\tw.WriteHeader(" + statusCode + ")\n")
		emitResponseBodyWrite(b, content)
	}
	b.WriteString("}\n\n")
}

func responseTypeSuffix(content ir.BodyContent, ok bool, multiContent bool) string {
	if !ok {
		return "Response"
	}
	if multiContent {
		return mediaTypeResponseSuffix(content.ContentType)
	}
	switch content.BodyKind {
	case "json":
		return "JSONResponse"
	case "text":
		return "TextResponse"
	case "binary":
		return "BinaryResponse"
	case "file":
		return "FileResponse"
	case "form_urlencoded":
		return "FormResponse"
	default:
		return "Response"
	}
}

func mediaTypeResponseSuffix(contentType string) string {
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	parts := strings.FieldsFunc(mediaType, func(r rune) bool {
		switch r {
		case '/', '+', '.', '-', '_':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return "Response"
	}
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.EqualFold(part, "json") {
			b.WriteString("JSON")
			continue
		}
		b.WriteString(exportedName(part))
	}
	if b.Len() == 0 {
		return "Response"
	}
	b.WriteString("Response")
	return b.String()
}

func responseContentTypeName(doc ir.Document, content ir.BodyContent) string {
	if content.Schema == nil {
		return "[]byte"
	}
	switch content.BodyKind {
	case "text":
		return "string"
	case "binary":
		return "[]byte"
	case "file":
		return "GenFile"
	default:
		return responseBodyTypeName(doc, *content.Schema)
	}
}

func emitResponseBodyWrite(b *strings.Builder, content ir.BodyContent) {
	switch content.BodyKind {
	case "text":
		b.WriteString("\t_, err := w.Write([]byte(response.Body))\n")
		b.WriteString("\treturn err\n")
	case "binary", "file":
		if content.BodyKind == "file" {
			b.WriteString("\treturn writeAPIGenFileResponse(w, response.Body, " + strconv.Quote(content.ContentType) + ", http.StatusOK)\n")
			return
		}
		b.WriteString("\t_, err := w.Write(response.Body)\n")
		b.WriteString("\treturn err\n")
	default:
		b.WriteString("\tpayload = append(payload, '\\n')\n")
		b.WriteString("\t_, err = w.Write(payload)\n")
		b.WriteString("\treturn err\n")
	}
}

func exportedName(operationID string) string {
	parts := splitIdentifier(operationID)
	if len(parts) == 0 {
		return "Operation"
	}
	for i := range parts {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func splitIdentifier(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	replacer := strings.NewReplacer("-", " ", "_", " ", ".", " ", "/", " ")
	value = replacer.Replace(value)
	chunks := strings.Fields(value)
	if len(chunks) > 0 {
		return chunks
	}
	return []string{value}
}

func lowerCamelName(value string) string {
	parts := splitIdentifier(value)
	if len(parts) == 0 {
		return "value"
	}
	parts[0] = strings.ToLower(parts[0][:1]) + parts[0][1:]
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func renderGoStringSlice(values []string) string {
	if len(values) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]string{")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", value)
	}
	b.WriteString("}")
	return b.String()
}

func renderGenCommandContract(command *ir.Command) string {
	if command == nil {
		return "nil"
	}
	var exposures strings.Builder
	if len(command.AdditionalExposures) == 0 {
		exposures.WriteString("nil")
	} else {
		exposures.WriteString("[]GenOperationSurface{")
		for index, exposure := range command.AdditionalExposures {
			if index > 0 {
				exposures.WriteString(", ")
			}
			fmt.Fprintf(&exposures, "%q", exposure)
		}
		exposures.WriteString("}")
	}
	target := "nil"
	if command.Target != nil {
		target = fmt.Sprintf("&GenOperationTarget{Parameter: %q, Type: %q}", command.Target.Parameter, command.Target.Type)
	}
	return fmt.Sprintf(
		"&GenCommandContract{Owner: %q, Audit: GenAuditPolicy{Required: %t, SuccessAction: %q, Guarantee: %q}, AdditionalExposures: %s, Target: %s, Idempotency: %q, Concurrency: %q, AuthzMode: %q, Privilege: %q}",
		command.Owner,
		command.Audit.Required,
		command.Audit.SuccessAction,
		command.Audit.Guarantee,
		exposures.String(),
		target,
		command.Idempotency,
		command.Concurrency,
		command.AuthzMode,
		command.Privilege,
	)
}

func renderGoIntSlice(values []int) string {
	if len(values) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]int{")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d", value)
	}
	b.WriteString("}")
	return b.String()
}

func renderGoAnyMap(values map[string]any) (string, error) {
	if len(values) == 0 {
		return "nil", nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("map[string]any{")
	for i, key := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		value, err := renderGoAny(values[key])
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		fmt.Fprintf(&b, "%q: %s", key, value)
	}
	b.WriteString("}")
	return b.String(), nil
}

func renderGoAny(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "nil", nil
	case string:
		return strconv.Quote(typed), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(typed), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return "int64(" + strconv.FormatInt(typed, 10) + ")", nil
	case uint:
		return "uint(" + strconv.FormatUint(uint64(typed), 10) + ")", nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return "uint32(" + strconv.FormatUint(uint64(typed), 10) + ")", nil
	case uint64:
		return "uint64(" + strconv.FormatUint(typed, 10) + ")", nil
	case float32:
		if math.IsInf(float64(typed), 0) || math.IsNaN(float64(typed)) {
			return "", fmt.Errorf("number must be finite")
		}
		return "float32(" + strconv.FormatFloat(float64(typed), 'g', -1, 32) + ")", nil
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return "", fmt.Errorf("number must be finite")
		}
		return "float64(" + strconv.FormatFloat(typed, 'g', -1, 64) + ")", nil
	case []any:
		return renderGoAnySlice(typed)
	case []string:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}
		return renderGoAnySlice(values)
	case map[string]any:
		return renderGoAnyMap(typed)
	default:
		return "", fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func renderGoAnySlice(values []any) (string, error) {
	var b strings.Builder
	b.WriteString("[]any{")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		rendered, err := renderGoAny(value)
		if err != nil {
			return "", err
		}
		b.WriteString(rendered)
	}
	b.WriteString("}")
	return b.String(), nil
}

func documentedStatusCodes(doc ir.Document, endpoint ir.Endpoint) []int {
	seen := make(map[int]struct{}, len(endpoint.Responses)+8)
	codes := make([]int, 0, len(endpoint.Responses)+8)
	for _, response := range endpoint.Responses {
		if _, ok := seen[response.StatusCode]; ok {
			continue
		}
		seen[response.StatusCode] = struct{}{}
		codes = append(codes, response.StatusCode)
	}
	if doc.TransportErrors != nil {
		for _, failure := range doc.TransportErrors.Failures {
			if _, ok := seen[failure.StatusCode]; ok {
				continue
			}
			seen[failure.StatusCode] = struct{}{}
			codes = append(codes, failure.StatusCode)
		}
	}
	sort.Ints(codes)
	return codes
}

func writeTransportErrorCall(
	b *strings.Builder,
	doc ir.Document,
	operationID string,
	kind string,
	causeExpr string,
	indent string,
) {
	writeTransportErrorCallWithResponder(b, doc, operationID, kind, causeExpr, "responder", indent)
}

func writeTransportErrorCallWithResponder(
	b *strings.Builder,
	doc ir.Document,
	operationID string,
	kind string,
	causeExpr string,
	responderExpr string,
	indent string,
) {
	failure := transportFailure(doc, kind)
	fmt.Fprintf(b, "%swriteAPIGenError(%s, w, r, GenTransportError{OperationID: %q, Kind: %q, StatusCode: %d, Code: %q, PublicDetail: %q, Cause: %s})\n",
		indent, responderExpr, operationID, kind, failure.StatusCode, failure.Code, failure.PublicDetail, causeExpr)
}

func transportFailure(doc ir.Document, kind string) ir.TransportFailure {
	if doc.TransportErrors != nil {
		if failure, ok := doc.TransportErrors.Failures[kind]; ok {
			return failure
		}
		if kind == "path_parameter" || kind == "query_parameter" || kind == "header_parameter" || kind == "multipart" {
			if failure, ok := doc.TransportErrors.Failures["malformed_body"]; ok {
				return failure
			}
		}
	}
	switch kind {
	case "unsupported_media_type":
		return ir.TransportFailure{StatusCode: httpStatusUnsupportedMediaType, Code: "unsupported_media_type", PublicDetail: "Unsupported media type."}
	case "handler", "response_serialization":
		return ir.TransportFailure{StatusCode: httpStatusInternalServerError, Code: "internal_error", PublicDetail: "Internal server error."}
	default:
		return ir.TransportFailure{StatusCode: httpStatusBadRequest, Code: "invalid_request", PublicDetail: "Invalid request."}
	}
}

const (
	httpStatusBadRequest           = 400
	httpStatusUnsupportedMediaType = 415
	httpStatusInternalServerError  = 500
)

func endpointProtected(endpoint ir.Endpoint) bool {
	for _, response := range endpoint.Responses {
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return true
		}
	}
	return false
}

func endpointManual(endpoint ir.Endpoint) bool {
	if len(endpoint.Extensions) == 0 {
		return false
	}
	raw, ok := endpoint.Extensions["x-apigen-manual"]
	if !ok {
		return false
	}
	manual, ok := raw.(bool)
	return ok && manual
}

func endpointAuthzMode(endpoint ir.Endpoint) string {
	if len(endpoint.Extensions) == 0 {
		return ""
	}
	raw, ok := endpoint.Extensions["x-authz"]
	if !ok {
		return ""
	}
	extension, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	mode, _ := extension["mode"].(string)
	return mode
}

func endpointPathParams(endpoint ir.Endpoint) []ir.Parameter {
	var out []ir.Parameter
	for _, p := range endpoint.Parameters {
		if strings.EqualFold(p.In, "path") {
			out = append(out, p)
		}
	}
	return out
}

func endpointQueryParams(endpoint ir.Endpoint) []ir.Parameter {
	var out []ir.Parameter
	for _, p := range endpoint.Parameters {
		if strings.EqualFold(p.In, "query") {
			out = append(out, p)
		}
	}
	return out
}

func endpointHeaderParams(endpoint ir.Endpoint) []ir.Parameter {
	var out []ir.Parameter
	for _, p := range endpoint.Parameters {
		if strings.EqualFold(p.In, "header") {
			out = append(out, p)
		}
	}
	return out
}

func pathParamTypeName(param ir.Parameter) string {
	return schemaTypeName(param.Schema)
}

func schemaTypeName(schema ir.SchemaRef) string {
	if schema.Ref != "" {
		return exportedName(schema.Ref)
	}

	schemaType := strings.ToLower(strings.TrimSpace(schema.Type))
	schemaFormat := strings.ToLower(strings.TrimSpace(schema.Format))

	switch schemaType {
	case "integer":
		switch schemaFormat {
		case "int32":
			return "int32"
		case "int64":
			return "int64"
		}
		return "int"
	case "number":
		switch schemaFormat {
		case "float", "float32":
			return "float32"
		case "double", "float64":
			return "float64"
		}
		return "float64"
	case "boolean", "bool":
		return "bool"
	case "array":
		if schema.Items != nil {
			return "[]" + schemaTypeName(*schema.Items)
		}
		return "[]any"
	case "string":
		if schemaFormat == "date-time" {
			return "time.Time"
		}
		return "string"
	default:
		return "string"
	}
}

func docUsesTimeTypes(doc ir.Document) bool {
	for _, endpoint := range doc.Endpoints {
		for _, param := range endpoint.Parameters {
			if schemaTypeName(param.Schema) == "time.Time" {
				return true
			}
		}
	}
	return false
}

func docUsesFileBodies(doc ir.Document) bool {
	for _, endpoint := range doc.Endpoints {
		if endpoint.RequestBody != nil {
			for _, content := range endpoint.RequestBody.Contents {
				if content.BodyKind == "file" {
					return true
				}
				for _, part := range content.Parts {
					if part.BodyKind == "file" {
						return true
					}
				}
			}
		}
		for _, response := range endpoint.Responses {
			for _, content := range response.Contents {
				if content.BodyKind == "file" {
					return true
				}
			}
		}
	}
	return false
}

func requestBodyTypeName(doc ir.Document, endpoint ir.Endpoint) (string, error) {
	if endpoint.RequestBody == nil {
		return "", fmt.Errorf("request body generation for %s requires a named IR schema", endpoint.OperationID)
	}
	content, ok := ir.PrimaryRequestBodyContent(endpoint)
	if !ok {
		return "", fmt.Errorf("request body generation for %s requires content", endpoint.OperationID)
	}
	switch content.BodyKind {
	case "text":
		return "string", nil
	case "binary":
		return "[]byte", nil
	case "file":
		return "GenFile", nil
	case "multipart":
		return "Gen" + exportedName(endpoint.OperationID) + "MultipartBody", nil
	}

	if schemaName, ok := ir.ResolveRequestBodySchemaName(doc, endpoint); ok {
		return "GenSchema" + exportedName(schemaName), nil
	}
	if content.Schema != nil {
		if strings.EqualFold(content.Schema.Type, "object") {
			return "", fmt.Errorf("request body generation for %s requires a named IR schema", endpoint.OperationID)
		}
		return schemaTypeName(*content.Schema), nil
	}
	return "", fmt.Errorf("request body generation for %s requires a schema", endpoint.OperationID)
}

func requestBodyRequiredFields(doc ir.Document, endpoint ir.Endpoint) []string {
	if endpoint.RequestBody == nil {
		return nil
	}
	content, ok := ir.PrimaryRequestBodyContent(endpoint)
	if !ok || content.Schema == nil {
		return nil
	}
	schema, ok := resolveSchema(doc, *content.Schema)
	if !ok {
		return nil
	}
	if schema.Type != "object" || len(schema.Required) == 0 {
		return nil
	}
	fields := append([]string(nil), schema.Required...)
	sort.Strings(fields)
	return fields
}

func resolveSchema(doc ir.Document, schemaRef ir.SchemaRef) (ir.Schema, bool) {
	if schemaRef.Ref != "" {
		return ir.ResolveSchema(doc, schemaRef)
	}
	if schemaRef.Type == "" {
		return ir.Schema{}, false
	}
	return ir.Schema{Type: schemaRef.Type}, true
}

func emitMissingSharedErrorResponses(b *strings.Builder, endpoint ir.Endpoint) {
	present := make(map[int]struct{}, len(endpoint.Responses))
	for _, response := range endpoint.Responses {
		present[response.StatusCode] = struct{}{}
	}
	sharedStatuses := []int{400, 401, 403, 404, 409, 429, 500, 502}
	name := exportedName(endpoint.OperationID)
	for _, statusCode := range sharedStatuses {
		if _, ok := present[statusCode]; ok {
			continue
		}
		response := ir.Response{
			StatusCode: statusCode,
			Headers:    defaultSharedErrorHeaders(statusCode),
			Contents: []ir.BodyContent{{
				ContentType: "application/json",
				BodyKind:    "json",
				Schema:      &ir.SchemaRef{Ref: "Error"},
			}},
		}
		shared, ok := sharedErrorResponseType(response)
		if !ok {
			continue
		}
		statusCodeText := fmt.Sprintf("%d", statusCode)
		b.WriteString("// Gen" + name + statusCodeText + "ResponseHeaders aliases the APIGen shared response headers for " + name + " " + statusCodeText + " errors.\n")
		b.WriteString("type Gen" + name + statusCodeText + "ResponseHeaders = Gen" + shared + "ResponseHeaders\n\n")
		b.WriteString("// Gen" + name + statusCodeText + "JSONResponse is the APIGen shared JSON response for " + name + " " + statusCodeText + ".\n")
		b.WriteString("type Gen" + name + statusCodeText + "JSONResponse struct{ Gen" + shared + "JSONResponse }\n\n")
		b.WriteString("// Visit" + name + "Response writes " + name + " " + statusCodeText + " responses to the client.\n")
		b.WriteString("func (response Gen" + name + statusCodeText + "JSONResponse) Visit" + name + "Response(w http.ResponseWriter) error {\n")
		emitDirectHeaderWrites(b, responseHeaderFields(response))
		b.WriteString("\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
		b.WriteString("\tw.WriteHeader(" + statusCodeText + ")\n")
		b.WriteString("\treturn json.NewEncoder(w).Encode(response.Body)\n")
		b.WriteString("}\n\n")
	}
}

func emitSharedErrorResponseTypes(b *strings.Builder, doc ir.Document) {
	sharedTypes := []struct {
		name       string
		statusCode int
	}{
		{name: "BadRequest", statusCode: 400},
		{name: "Conflict", statusCode: 409},
		{name: "Forbidden", statusCode: 403},
		{name: "InternalError", statusCode: 500},
		{name: "NotFound", statusCode: 404},
		{name: "RateLimitExceeded", statusCode: 429},
		{name: "Unauthorized", statusCode: 401},
	}

	for _, shared := range sharedTypes {
		b.WriteString("// Gen" + shared.name + "ResponseHeaders represents the APIGen shared response headers for " + shared.name + " JSON errors.\n")
		b.WriteString("type Gen" + shared.name + "ResponseHeaders struct {\n")
		for _, header := range sharedErrorHeaders(doc, shared.statusCode) {
			b.WriteString("\t" + headerFieldName(header.Name) + " " + schemaTypeName(header.Schema) + "\n")
		}
		b.WriteString("}\n\n")
		b.WriteString("// Gen" + shared.name + "JSONResponse represents the APIGen shared JSON error body for " + shared.name + " responses.\n")
		b.WriteString("type Gen" + shared.name + "JSONResponse struct {\n")
		b.WriteString("\tBody Error\n\n")
		b.WriteString("\tHeaders Gen" + shared.name + "ResponseHeaders\n")
		b.WriteString("}\n\n")
	}
}

func responseBodyTypeName(doc ir.Document, schema ir.SchemaRef) string {
	if ref, ok := normalizedSchemaRefName(schema); ok {
		name := exportedName(ref)
		if _, ok := doc.Schemas[ref]; ok {
			return "GenSchema" + name
		}
		return name
	}
	return schemaTypeName(schema)
}

func emitOwnedResponseHeaders(b *strings.Builder, typeName string, fields []ownedHeaderField) {
	b.WriteString("// " + typeName + " represents the APIGen-owned response headers for generated concrete responses.\n")
	b.WriteString("type " + typeName + " struct {\n")
	for _, field := range fields {
		b.WriteString("\t" + field.Name + " " + field.Type + "\n")
	}
	b.WriteString("}\n\n")
}

func emitDirectHeaderWrites(b *strings.Builder, fields []ownedHeaderField) {
	for _, field := range fields {
		if field.HeaderName == "" {
			continue
		}
		b.WriteString("\tw.Header().Set(\"" + field.HeaderName + "\", fmt.Sprint(response.Headers." + field.Name + "))\n")
	}
}

func responseHeaderFields(response ir.Response) []ownedHeaderField {
	fields := make([]ownedHeaderField, 0, len(response.Headers))
	for _, header := range response.Headers {
		fields = append(fields, ownedHeaderField{
			Name:       headerFieldName(header.Name),
			HeaderName: header.Name,
			Type:       schemaTypeName(header.Schema),
		})
	}
	return fields
}

func responseHeaderFieldsWithDefaults(response ir.Response) []ownedHeaderField {
	if len(response.Headers) > 0 {
		return responseHeaderFields(response)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return responseHeaderFields(ir.Response{Headers: defaultSharedErrorHeaders(response.StatusCode)})
	}
	return nil
}

func sharedErrorResponseType(response ir.Response) (string, bool) {
	content, ok := ir.PrimaryResponseContent(response)
	if !ok || content.Schema == nil || !isErrorSchema(*content.Schema) {
		return "", false
	}

	switch response.StatusCode {
	case 400:
		return "BadRequest", true
	case 401:
		return "Unauthorized", true
	case 403:
		return "Forbidden", true
	case 404:
		return "NotFound", true
	case 409:
		return "Conflict", true
	case 429:
		return "RateLimitExceeded", true
	case 500:
		return "InternalError", true
	case 502:
		return "InternalError", true
	default:
		return "", false
	}
}

func sharedErrorHeaders(doc ir.Document, statusCode int) []ir.Header {
	for _, endpoint := range doc.Endpoints {
		for _, response := range endpoint.Responses {
			if response.StatusCode != statusCode {
				continue
			}
			if _, ok := sharedErrorResponseType(response); !ok {
				continue
			}
			if len(response.Headers) > 0 {
				return response.Headers
			}
		}
	}
	return defaultSharedErrorHeaders(statusCode)
}

func defaultSharedErrorHeaders(statusCode int) []ir.Header {
	headers := []ir.Header{
		{Name: "X-RateLimit-Limit", Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
		{Name: "X-RateLimit-Remaining", Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
		{Name: "X-RateLimit-Reset", Schema: ir.SchemaRef{Type: "integer", Format: "int64"}},
	}
	if statusCode == 429 {
		headers = append([]ir.Header{{Name: "Retry-After", Schema: ir.SchemaRef{Type: "integer", Format: "int32"}}}, headers...)
	}
	return headers
}

func headerFieldName(name string) string {
	return exportedName(strings.NewReplacer("-", " ", "_", " ").Replace(name))
}

func isErrorSchema(schema ir.SchemaRef) bool {
	if schema.Ref == "" {
		return false
	}
	ref := strings.TrimSpace(schema.Ref)
	ref = strings.TrimPrefix(ref, "#/components/schemas/")
	ref = strings.TrimPrefix(ref, "#/schemas/")
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		ref = ref[idx+1:]
	}
	return exportedName(ref) == "Error"
}

type ownedHeaderField struct {
	Name       string
	HeaderName string
	Type       string
}

func usesDirectOwnedResponseSchema(response ir.Response, doc ir.Document) bool {
	content, ok := ir.PrimaryResponseContent(response)
	if !ok || content.Schema == nil {
		return false
	}
	if shape, ok, err := ir.ResponseShapeMetadata(response); err == nil && ok && shape.Kind == "wrapped_json" {
		return false
	}
	if isErrorSchema(*content.Schema) {
		return false
	}
	_, ok = responseSchemaTypeName(doc, *content.Schema)
	return ok
}

func responseSchemaTypeName(doc ir.Document, schema ir.SchemaRef) (string, bool) {
	ref, ok := normalizedSchemaRefName(schema)
	if !ok {
		return "", false
	}
	if _, ok := doc.Schemas[ref]; !ok {
		return "", false
	}
	return exportedName(ref), true
}

func normalizedSchemaRefName(schema ir.SchemaRef) (string, bool) {
	if schema.Ref == "" {
		return "", false
	}
	ref := strings.TrimSpace(schema.Ref)
	ref = strings.TrimPrefix(ref, "#/components/schemas/")
	ref = strings.TrimPrefix(ref, "#/schemas/")
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		ref = ref[idx+1:]
	}
	if ref == "" {
		return "", false
	}
	return ref, true
}

// ValidateOperationIDs checks for exported handler name collisions.
func ValidateOperationIDs(doc ir.Document) error {
	seen := make(map[string]string, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		exported := exportedName(endpoint.OperationID)
		if prev, exists := seen[exported]; exists {
			return fmt.Errorf("operation name collision %q for %q and %q", exported, prev, endpoint.OperationID)
		}
		seen[exported] = endpoint.OperationID
	}
	return nil
}

// SortedOperationIDs returns operation IDs in deterministic order.
func SortedOperationIDs(doc ir.Document) []string {
	ids := make([]string, 0, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		ids = append(ids, endpoint.OperationID)
	}
	sort.Strings(ids)
	return ids
}

func packageName(opts Options) string {
	if strings.TrimSpace(opts.PackageName) == "" {
		return "api"
	}
	return opts.PackageName
}
