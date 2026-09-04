package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const govulnProtocolVersion = "v1.0.0"

type govulnStream struct {
	findings []govulnFinding
}

type govulnConfig struct {
	ProtocolVersion string `json:"protocol_version"`
	ScannerName     string `json:"scanner_name"`
	Database        string `json:"db"`
	DBLastModified  string `json:"db_last_modified"`
	ScanMode        string `json:"scan_mode"`
}

type govulnFinding struct {
	OSV   string `json:"osv"`
	Trace []struct {
		Module string `json:"module"`
	} `json:"trace"`
}

type govulnSBOM struct {
	GoVersion string `json:"go_version"`
	Modules   []struct {
		Path string `json:"path"`
	} `json:"modules"`
	Roots []string `json:"roots"`
}

func parseGovulnStream(data []byte) (govulnStream, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	stream := govulnStream{}
	osvIDs := map[string]struct{}{}
	messageCount := 0
	sawSBOM := false
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			if messageCount == 0 {
				return govulnStream{}, errors.New("stream is empty")
			}
			if !sawSBOM {
				return govulnStream{}, errors.New("source SBOM is missing")
			}
			return stream, nil
		}
		if err != nil {
			return govulnStream{}, err
		}
		if err := rejectDuplicateJSONKeys(raw); err != nil {
			return govulnStream{}, err
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil || len(envelope) != 1 {
			return govulnStream{}, errors.New("each message must be an object with exactly one field")
		}
		for kind, payload := range envelope {
			if messageCount == 0 && kind != "config" {
				return govulnStream{}, errors.New("config must be the first message")
			}
			switch kind {
			case "config":
				if messageCount != 0 {
					return govulnStream{}, errors.New("config must occur exactly once")
				}
				var config govulnConfig
				if err := json.Unmarshal(payload, &config); err != nil {
					return govulnStream{}, errors.New("config is malformed")
				}
				if config.ProtocolVersion != govulnProtocolVersion || config.ScannerName != "govulncheck" || config.ScanMode != "source" {
					return govulnStream{}, errors.New("config identity is unsupported")
				}
				if strings.TrimSpace(config.Database) == "" || strings.TrimSpace(config.DBLastModified) == "" {
					return govulnStream{}, errors.New("config vulnerability database identity is incomplete")
				}
			case "progress":
				var object map[string]json.RawMessage
				if err := json.Unmarshal(payload, &object); err != nil || object == nil {
					return govulnStream{}, fmt.Errorf("%s message is malformed", kind)
				}
			case "SBOM":
				if sawSBOM {
					return govulnStream{}, errors.New("source SBOM must occur exactly once")
				}
				var sbom govulnSBOM
				if err := json.Unmarshal(payload, &sbom); err != nil || strings.TrimSpace(sbom.GoVersion) == "" || len(sbom.Modules) == 0 || len(sbom.Roots) == 0 {
					return govulnStream{}, errors.New("source SBOM is incomplete")
				}
				for _, module := range sbom.Modules {
					if strings.TrimSpace(module.Path) == "" || strings.TrimSpace(module.Path) != module.Path {
						return govulnStream{}, errors.New("source SBOM module path is missing")
					}
				}
				for _, root := range sbom.Roots {
					if strings.TrimSpace(root) == "" || strings.TrimSpace(root) != root {
						return govulnStream{}, errors.New("source SBOM root is missing")
					}
				}
				sawSBOM = true
			case "osv":
				if !sawSBOM {
					return govulnStream{}, errors.New("osv message precedes source SBOM")
				}
				var osv struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(payload, &osv); err != nil || strings.TrimSpace(osv.ID) == "" || strings.TrimSpace(osv.ID) != osv.ID {
					return govulnStream{}, errors.New("osv identity is missing")
				}
				osvIDs[osv.ID] = struct{}{}
			case "finding":
				if !sawSBOM {
					return govulnStream{}, errors.New("finding message precedes source SBOM")
				}
				var finding govulnFinding
				if err := json.Unmarshal(payload, &finding); err != nil || strings.TrimSpace(finding.OSV) == "" || strings.TrimSpace(finding.OSV) != finding.OSV {
					return govulnStream{}, errors.New("finding osv identity is missing")
				}
				if _, ok := osvIDs[finding.OSV]; !ok {
					return govulnStream{}, errors.New("finding references an osv entry not present in the stream")
				}
				if len(finding.Trace) == 0 || strings.TrimSpace(finding.Trace[0].Module) == "" || strings.TrimSpace(finding.Trace[0].Module) != finding.Trace[0].Module {
					return govulnStream{}, errors.New("finding trace module is missing")
				}
				stream.findings = append(stream.findings, finding)
			default:
				return govulnStream{}, fmt.Errorf("unsupported message %q", kind)
			}
		}
		messageCount++
	}
}
