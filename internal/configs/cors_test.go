package configs

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCORSOnUnlessSwitchedOff(t *testing.T) {
	for _, c := range []struct {
		yaml string
		want bool
	}{
		{"GRPCPort: 1", true},
		{"CORS: {}", true},
		{"CORS: {Enabled: true}", true},
		{"CORS: {Enabled: false}", false},
	} {
		var s Server
		if err := yaml.Unmarshal([]byte(c.yaml), &s); err != nil {
			t.Fatal(err)
		}
		if got := s.CORS.On(); got != c.want {
			t.Errorf("%q: CORS on %v, want %v", c.yaml, got, c.want)
		}
	}
}
