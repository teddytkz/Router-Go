package config

import "testing"

func TestResolvePortPrecedence(t *testing.T) {
	cases := []struct {
		name            string
		flag, env, save int
		want            int
		src             string
	}{
		{"default", 0, 0, 0, 20128, PortFromDefault},
		{"saved", 0, 0, 9000, 9000, PortFromSettings},
		{"env beats saved", 0, 8000, 9000, 8000, PortFromEnv},
		{"flag beats all", 7000, 8000, 9000, 7000, PortFromFlag},
		{"bad saved value is ignored", 0, 0, 70000, 20128, PortFromDefault},
	}
	for _, c := range cases {
		cfg := &Config{envPort: c.env}
		if err := cfg.ResolvePort(c.flag, c.save); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p, src := cfg.PortInfo(); p != c.want || src != c.src {
			t.Errorf("%s: got %d/%s want %d/%s", c.name, p, src, c.want, c.src)
		}
	}
	if err := (&Config{}).ResolvePort(70000, 0); err == nil {
		t.Error("--port out of range must be rejected")
	}
	if got := (&Config{Host: "127.0.0.1", port: 8080}).Addr(); got != "127.0.0.1:8080" {
		t.Errorf("Addr = %s", got)
	}
	c := &Config{port: 8080}
	if got := c.Addr(); got != ":8080" {
		t.Errorf("default must bind all interfaces, got %s", got)
	}
	c.SetLocalOnly(true)
	if got := c.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("local only = %s", got)
	}
	c.Host = "10.0.0.5" // HOSTNAME_BIND wins over the setting
	if got := c.Addr(); got != "10.0.0.5:8080" {
		t.Errorf("env host = %s", got)
	}
}
