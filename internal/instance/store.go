package instance

import (
	"errors"
	"fmt"
	"net"
	"regexp"
)

var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

type record struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Port         int    `json:"port"`
	Desired      bool   `json:"desired"`
	PendingImage string `json:"pending_image,omitempty"`
	Replacing    bool   `json:"replacing,omitzero"`
}
type state struct {
	Version   int               `json:"version"`
	Instances map[string]record `json:"instances"`
}

func allocatePort(s state) (int, error) {
	used := map[int]bool{}
	for _, r := range s.Instances {
		used[r.Port] = true
	}
	for port := 19000; port <= 29999; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		if err := listener.Close(); err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, errors.New("no available backend port in 19000–29999")
}

func validPort(port int) bool {
	if port < 19000 {
		return false
	}
	return port <= 29999
}

func validateRecord(name string, r record) error {
	if !namePattern.MatchString(name) {
		return errors.New("invalid name in instance state")
	}
	if r.Name != name {
		return errors.New("inconsistent name in instance state")
	}
	if r.Replacing {
		if r.PendingImage == "" {
			return errors.New("incomplete update intent in instance state")
		}
	}
	if !validPort(r.Port) {
		return errors.New("invalid port in instance state")
	}
	return nil
}

func validateState(s state) error {
	if s.Version != 1 {
		return errors.New("unsupported or incomplete instance state")
	}
	if s.Instances == nil {
		return errors.New("unsupported or incomplete instance state")
	}
	ports := map[int]bool{}
	for name, r := range s.Instances {
		if err := validateRecord(name, r); err != nil {
			return err
		}
		if ports[r.Port] {
			return errors.New("duplicate port in instance state")
		}
		if r.Image == "" {
			return errors.New("missing image in instance state")
		}
		ports[r.Port] = true
	}
	return nil
}
