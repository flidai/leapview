//go:build windows

package hostinstall

func readPrivateAgentFile(string, ...string) ([]byte, error) { return nil, errAgentTransition }
