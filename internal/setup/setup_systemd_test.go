package setup

import (
	"strings"
	"testing"
)

func TestBuildSystemdUnits_NoNetworkOnline(t *testing.T) {
	daemon := buildDaemonUnit("/usr/local/bin/yesmem", ":0", "/home/u/.Xauthority", "unix:path=/run/user/1000/bus")
	proxy := buildProxyUnit("/usr/local/bin/yesmem")

	for name, unit := range map[string]string{"daemon": daemon, "proxy": proxy} {
		if strings.Contains(unit, "network-online") {
			t.Fatalf("%s unit still references network-online.target — services are local and must not block on network setup", name)
		}
		if strings.Contains(unit, "After=") {
			t.Fatalf("%s unit has unexpected After= ordering dependency", name)
		}
		if !strings.Contains(unit, "[Install]") || !strings.Contains(unit, "WantedBy=default.target") {
			t.Fatalf("%s unit missing WantedBy=default.target install section", name)
		}
		if !strings.Contains(unit, "Restart=always") || !strings.Contains(unit, "RestartSec=10") {
			t.Fatalf("%s unit missing restart policy", name)
		}
	}

	if !strings.Contains(daemon, "ExecStart=/usr/local/bin/yesmem daemon --replace") {
		t.Errorf("daemon unit wrong ExecStart:\n%s", daemon)
	}
	if !strings.Contains(daemon, `Environment="DISPLAY=:0"`) ||
		!strings.Contains(daemon, `Environment="XAUTHORITY=/home/u/.Xauthority"`) ||
		!strings.Contains(daemon, `Environment="DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"`) {
		t.Errorf("daemon unit missing graphical session env:\n%s", daemon)
	}
	if !strings.Contains(proxy, "ExecStart=/usr/local/bin/yesmem proxy") {
		t.Errorf("proxy unit wrong ExecStart:\n%s", proxy)
	}
}
