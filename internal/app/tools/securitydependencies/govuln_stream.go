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
	ScannerVersion  string `json:"scanner_version"`
	Database        string `json:"db"`
	DBLastModified  string `json:"db_last_modified"`
	ScanLevel       string `json:"scan_level"`
	ScanMode        string `json:"scan_mode"`
}

type govulnFinding struct {
	OSV   string        `json:"osv"`
	Trace []govulnFrame `json:"trace"`
}

type govulnFrame struct {
	Module   string          `json:"module"`
	Version  string          `json:"version,omitempty"`
	Package  string          `json:"package,omitempty"`
	Function string          `json:"function,omitempty"`
	Receiver string          `json:"receiver,omitempty"`
	Position *govulnPosition `json:"position,omitempty"`
}

type govulnPosition struct {
	Filename string `json:"filename,omitempty"`
	Offset   int    `json:"offset"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

func (f *govulnFrame) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("frame must be an object")
	}
	decodeString := func(name string, destination *string, required bool) error {
		raw, present := fields[name]
		if !present {
			if required {
				return fmt.Errorf("frame %s is missing", name)
			}
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, destination) != nil {
			return fmt.Errorf("frame %s is not a string", name)
		}
		return nil
	}
	if err := decodeString("module", &f.Module, true); err != nil {
		return err
	}
	if err := decodeString("version", &f.Version, false); err != nil {
		return err
	}
	if err := decodeString("package", &f.Package, false); err != nil {
		return err
	}
	if err := decodeString("function", &f.Function, false); err != nil {
		return err
	}
	if err := decodeString("receiver", &f.Receiver, false); err != nil {
		return err
	}
	if raw, present := fields["position"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("frame position is not an object")
		}
		var position govulnPosition
		if err := json.Unmarshal(raw, &position); err != nil {
			return fmt.Errorf("frame position is malformed: %w", err)
		}
		f.Position = &position
	}
	return nil
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
	sawConfig := false
	sawSBOM := false
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			if messageCount == 0 {
				return govulnStream{}, errors.New("stream is empty")
			}
			if !sawConfig {
				return govulnStream{}, errors.New("source config is missing")
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
				if config.ProtocolVersion != govulnProtocolVersion || config.ScannerName != "govulncheck" || config.ScannerVersion != govulncheckVersion || config.ScanMode != "source" || config.ScanLevel != "symbol" {
					return govulnStream{}, errors.New("config identity is unsupported")
				}
				if strings.TrimSpace(config.Database) == "" || strings.TrimSpace(config.DBLastModified) == "" {
					return govulnStream{}, errors.New("config vulnerability database identity is incomplete")
				}
				sawConfig = true
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
				callable, err := validateGovulnTrace(finding.Trace)
				if err != nil {
					return govulnStream{}, fmt.Errorf("finding trace is malformed: %w", err)
				}
				if callable {
					stream.findings = append(stream.findings, finding)
				}
			default:
				return govulnStream{}, fmt.Errorf("unsupported message %q", kind)
			}
		}
		messageCount++
	}
}

// validateGovulnTrace classifies a source finding by the shape documented by
// govulncheck's JSON protocol. Module and package findings are intentionally
// valid in a symbol scan, but only a callable trace can block the gate.
//
// A module or package finding has exactly one frame. A callable finding has a
// non-empty package and function in every frame; the first frame is the
// vulnerable symbol and subsequent frames are its call path. Anything between
// those shapes is ambiguous and must fail closed.
func validateGovulnTrace(trace []govulnFrame) (bool, error) {
	if len(trace) == 0 {
		return false, errors.New("trace is empty")
	}
	for i, frame := range trace {
		if strings.TrimSpace(frame.Module) == "" || strings.TrimSpace(frame.Module) != frame.Module {
			return false, fmt.Errorf("frame %d module is missing or malformed", i)
		}
		if strings.TrimSpace(frame.Version) != frame.Version {
			return false, fmt.Errorf("frame %d version is malformed", i)
		}
		if strings.TrimSpace(frame.Package) != frame.Package {
			return false, fmt.Errorf("frame %d package is malformed", i)
		}
		if strings.TrimSpace(frame.Function) != frame.Function {
			return false, fmt.Errorf("frame %d function is malformed", i)
		}
		if strings.TrimSpace(frame.Receiver) != frame.Receiver {
			return false, fmt.Errorf("frame %d receiver is malformed", i)
		}
		if frame.Receiver != "" && frame.Function == "" {
			return false, fmt.Errorf("frame %d receiver has no function", i)
		}
	}

	first := trace[0]
	if first.Function == "" {
		if len(trace) != 1 {
			return false, errors.New("non-callable trace has multiple frames")
		}
		if first.Position != nil {
			return false, errors.New("non-callable trace has a position")
		}
		if first.Package == "" {
			return false, nil // module-level finding
		}
		return false, nil // package-level finding
	}
	if first.Package == "" {
		return false, errors.New("function has no package")
	}
	for i, frame := range trace {
		if frame.Package == "" || frame.Function == "" {
			return false, fmt.Errorf("callable frame %d is missing package or function", i)
		}
	}
	return true, nil
}
